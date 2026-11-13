package server

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strconv"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
)

// Batch limits: the pending actions of one batch decision, and its form body, which holds maxBatchIDs IDs.
const (
	maxBatchIDs   = 500
	maxBatchBytes = 32 << 10
)

// batch decides the selected pending actions from a form with the repeated field id, one decision, and an optional
// return, then shows the list with the outcome. Approve runs them one after another in list order, each through
// Approve with its own claim, and skips the Messages and Meetings actions that need review; deny denies each. An ID
// that is not pending, or unknown, counts as already handled. Nothing is retried.
func (c *console) batch(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	form, ok := readForm(w, r, maxBatchBytes, "id", "decision", "return")
	ids := slices.Compact(slices.Sorted(slices.Values(form["id"])))
	decision := form.Get("decision")
	if !ok || len(form["decision"]) != 1 || (decision != decisionApprove && decision != decisionDeny) || len(form["return"]) > 1 ||
		len(ids) == 0 || len(ids) > maxBatchIDs || slices.ContainsFunc(ids, func(id string) bool { return !approvalIDPattern.MatchString(id) }) {
		plain(w, 400, "invalid request")
		return
	}
	var items []cache.Pending
	for _, id := range ids {
		item, err := c.s.cache.GetPending(ctx, id)
		switch {
		case errors.Is(err, cache.ErrPendingNotFound):
		case err != nil:
			plain(w, 500, "approvals unavailable")
			return
		case item.State == cache.StatePending:
			items = append(items, item)
		}
	}
	// List order: the groups in page order, each newest first.
	slices.SortStableFunc(items, func(a, b cache.Pending) int {
		return cmp.Or(groupRank(a.Op)-groupRank(b.Op), b.Created.Compare(a.Created))
	})
	n := batchCounts{handled: len(ids) - len(items)}
	// A run that started finishes even when the browser goes away; Close still cancels it.
	ctx = context.WithoutCancel(ctx)
	for _, item := range items {
		if decision == decisionApprove && needsReview(item) {
			n.review++
			continue
		}
		run, ran := c.s.Deny, &n.denied
		if decision == decisionApprove {
			run, ran = c.s.Approve, &n.approved
		}
		err := run(ctx, item.ID)
		// The daemon is closing: nothing more runs, and the rest is not counted.
		if errors.Is(err, ErrUnavailable) {
			if n.approved+n.denied+n.failed == 0 {
				plain(w, 503, err.Error())
				return
			}
			break
		}
		switch {
		case errors.Is(err, ErrApprovalNotFound), errors.Is(err, ErrAlreadyHandled):
			n.handled++
		case err != nil:
			n.failed++
		default:
			*ran++
		}
	}
	q, _ := c.formReturn(form)
	v := q.values()
	for _, p := range noticeParts {
		if count := *p.count(&n); count > 0 {
			v.Set(p.key, strconv.Itoa(count))
		}
	}
	http.Redirect(w, r, listURL(v), http.StatusSeeOther)
}

// groupRank is the place of the list group of op in page order; an operation without a group comes last.
func groupRank(op string) int {
	i := slices.IndexFunc(listGroups, func(g listGroup) bool { return g.Key == listedOps[op].group })
	if i < 0 {
		return len(listGroups)
	}
	return i
}
