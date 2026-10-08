package guard

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/audit"
	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// Policy keys. A key is the API operation name with "." changed to "_"; mail_draft covers all draft operations.
const (
	keyTrash = "mail_trash"
	keyDraft = "mail_draft"
)

type approvalKey struct{}

// WithApproval marks ctx for the run of an action that a person approved. A guard write with this context
// skips the policy: it makes no decision, no sealed lookup, no policy record, and no pending item, and runs
// as an allowed action with its normal operation record. Only the approval run may create this context.
func WithApproval(ctx context.Context) context.Context {
	return context.WithValue(ctx, approvalKey{}, true)
}

func approved(ctx context.Context) bool { return ctx.Value(approvalKey{}) != nil }

// SafeCode gives the safe failure code of err, for storage with a failed approval.
func SafeCode(err error) string { return safeCode(err) }

// refused is the fixed public refusal for a write that the policy denies.
func refused(key string) GuardError { return GuardError("refused by policy: " + key) }

// rank orders decisions allow < approve < deny. Any other value, also the zero value, counts as deny.
func rank(d config.Decision) int {
	switch d {
	case config.Allow:
		return 0
	case config.Approve:
		return 1
	}
	return 2
}

// normal maps the zero value and unknown values to deny, so a guard without a policy fails closed.
func normal(d config.Decision) config.Decision {
	return [...]config.Decision{config.Allow, config.Approve, config.Deny}[rank(d)]
}

// base is the base decision of key, or allow for an approved run. decide also skips the sealed decision of an approved run.
func (g *Guard) base(ctx context.Context, key string) config.Decision {
	if approved(ctx) {
		return config.Allow
	}
	return g.baseDecision(key)
}

func (g *Guard) baseDecision(key string) config.Decision {
	b := g.policy.Base
	switch key {
	case keyTrash:
		return normal(b.MailTrash)
	case keyDraft:
		return normal(b.MailDraft)
	case string(audit.CalendarInvite):
		return normal(b.CalendarInvite)
	case string(audit.CalendarUpdate):
		return normal(b.CalendarUpdate)
	case string(audit.CalendarCancel):
		return normal(b.CalendarCancel)
	case string(audit.CalendarRespond):
		return normal(b.CalendarRespond)
	}
	return config.Deny
}

func (g *Guard) sealedDecision(key string) config.Decision {
	switch key {
	case keyTrash:
		return normal(g.policy.Sealed.MailTrash)
	case keyDraft:
		return normal(g.policy.Sealed.MailDraft)
	}
	return config.Deny
}

// decision is the final policy decision for one action. sealed is true when the sealed decision made it stricter.
type decision struct {
	value  config.Decision
	sealed bool
}

// decide returns the decision of key for each message ID. It looks up or screens a message only when the
// sealed decision is stricter than the base decision. A sealed or error verdict, or a failed lookup, counts
// as sealed. The caller holds the mutation gate and not the security gate: screening can take the security gate.
func (g *Guard) decide(ctx context.Context, key string, ids []string) (map[string]decision, error) {
	base, sealed := g.base(ctx, key), g.sealedDecision(key)
	out := make(map[string]decision, len(ids))
	for _, id := range ids {
		out[id] = decision{value: base}
	}
	if approved(ctx) || rank(sealed) <= rank(base) {
		return out, nil
	}
	// A zero verdict, from a failed cache read or a failed screening, counts as sealed.
	verdicts := make([]model.Verdict, len(ids))
	if rows, err := g.cached(ctx, ids); err == nil {
		// The work never fails; a cancellation is returned below.
		_ = parallelItems(ctx, len(ids), func(i int) error {
			if row, ok := rows[ids[i]]; ok {
				verdicts[i] = row.Verdict
			} else if _, row, _, err := g.read(ctx, ids[i]); err == nil {
				verdicts[i] = row.Verdict
			}
			return nil
		})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for i, id := range ids {
		switch verdicts[i] {
		case model.Green, model.Yellow, model.Red:
		default:
			out[id] = decision{value: sealed, sealed: true}
		}
	}
	return out, nil
}

// PendingError reports that the action was stored for approval and did not run. It is not a failure.
type PendingError struct{ ApprovalID string }

func (e PendingError) Error() string { return audit.Pending }

// apiOperations maps audit operations to the API operation names that pending items and policy records use.
var apiOperations = map[audit.Operation]string{
	audit.Trash: "mail.trash", audit.DraftNew: "draft.new", audit.DraftReply: "draft.reply", audit.DraftForward: "draft.forward", audit.DraftUpdate: "draft.update",
	audit.CalendarInvite: "calendar.invite", audit.CalendarUpdate: "calendar.update", audit.CalendarCancel: "calendar.cancel", audit.CalendarRespond: "calendar.respond",
}

// recordDecision writes the policy record of one action. A failed write fails closed.
// An approved run has no decision, so it writes no record.
func (g *Guard) recordDecision(ctx context.Context, op audit.Operation, key string, d decision, id, approval, rule *string) error {
	if approved(ctx) {
		return nil
	}
	reasons := []string{"policy"}
	if d.sealed {
		reasons = append(reasons, "sealed")
	}
	if err := g.log.Policy(context.WithoutCancel(ctx), g.account, audit.Decision{Operation: apiOperations[op], Key: key, Decision: string(d.value), Reasons: reasons, ID: id, ApprovalID: approval, Rule: rule}); err != nil {
		return GuardError("AuditError")
	}
	return nil
}

// hold stores an approve action with args in the shape of the API params, then records the decision and the
// new item. If an audit write fails, the item is denied so that it can never run without its records.
func (g *Guard) hold(ctx context.Context, op audit.Operation, key string, d decision, id, rule *string, args any) (string, error) {
	data, err := json.Marshal(args)
	if err != nil {
		return "", GuardError("ValueError")
	}
	approval, err := g.store.AddPending(ctx, g.account, apiOperations[op], data, g.now())
	if err != nil {
		return "", safeErr(ctx, err)
	}
	err = g.recordDecision(ctx, op, key, d, id, &approval, rule)
	if err == nil && g.log.Approval(context.WithoutCancel(ctx), g.account, apiOperations[op], "created", approval) != nil {
		err = GuardError("AuditError")
	}
	if err != nil {
		_, _ = g.store.DenyPending(context.WithoutCancel(ctx), approval, g.now())
		return "", err
	}
	return approval, nil
}

// single applies a decision to an action that is not per ID. It returns nil only for allow.
// deny gives the fixed refusal; approve stores args and gives PendingError.
func (g *Guard) single(ctx context.Context, op audit.Operation, key string, d decision, id *string, args any) error {
	switch d.value {
	case config.Allow:
		return nil
	case config.Approve:
		approval, err := g.hold(ctx, op, key, d, id, nil, args)
		if err != nil {
			return err
		}
		return PendingError{approval}
	}
	if err := g.recordDecision(ctx, op, key, d, id, nil, nil); err != nil {
		return err
	}
	return refused(key)
}

// trashPolicy decides each trash ID before any provider access. It records deny and approve decisions,
// stores each approve ID as its own pending item, and returns the results of the IDs it held back and
// the distinct IDs that the policy allows.
func (g *Guard) trashPolicy(ctx context.Context, ids []string, rule *string) (map[string]model.MutationResult, []string, error) {
	unique := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	decisions, err := g.decide(ctx, keyTrash, unique)
	if err != nil {
		return nil, nil, err
	}
	held := map[string]model.MutationResult{}
	allowed := []string{}
	for _, id := range unique {
		d := decisions[id]
		switch d.value {
		case config.Allow:
			allowed = append(allowed, id)
		case config.Approve:
			approval, err := g.hold(ctx, audit.Trash, keyTrash, d, &id, rule, idsArgs{[]string{id}})
			if err != nil {
				return nil, nil, err
			}
			held[id] = model.MutationResult{ID: id, Error: pointer(audit.Pending), ApprovalID: &approval}
		default:
			if err := g.recordDecision(ctx, audit.Trash, keyTrash, d, &id, nil, rule); err != nil {
				return nil, nil, err
			}
			held[id] = model.MutationResult{ID: id, Error: pointer(string(refused(keyTrash)))}
		}
	}
	return held, allowed, nil
}

// Stored args mirror the API params of each operation, so a stored action can run through the normal API call.
type idsArgs struct {
	IDs []string `json:"ids"`
}
type newDraftArgs struct {
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Body    string   `json:"body"`
}
type bodyArgs struct {
	ID   string `json:"id"`
	Body string `json:"body"`
}
type forwardArgs struct {
	ID string   `json:"id"`
	To []string `json:"to"`
}
type inviteArgs struct {
	To       []string `json:"to"`
	Subject  string   `json:"subject"`
	Body     string   `json:"body"`
	Start    string   `json:"start"`
	End      string   `json:"end"`
	Location string   `json:"location"`
	Teams    bool     `json:"teams"`
}
type changeArgs struct {
	ID       string   `json:"id"`
	Subject  *string  `json:"subject,omitempty"`
	Location *string  `json:"location,omitempty"`
	Start    *string  `json:"start,omitempty"`
	End      *string  `json:"end,omitempty"`
	Duration *string  `json:"duration,omitempty"`
	Add      []string `json:"add,omitempty"`
	Remove   []string `json:"remove,omitempty"`
	Teams    *bool    `json:"teams,omitempty"`
}
type cancelArgs struct {
	ID      string `json:"id"`
	Comment string `json:"comment"`
}
type respondArgs struct {
	ID       string                `json:"id"`
	Response model.MeetingResponse `json:"response"`
	Comment  string                `json:"comment"`
}

func stamp3339(t *time.Time) *string {
	if t == nil {
		return nil
	}
	return pointer(t.Format(time.RFC3339Nano))
}

// updateArgs gives only the fields of the original request: optional fields that were not given stay out.
func updateArgs(id string, change model.EventChange, duration time.Duration) changeArgs {
	args := changeArgs{ID: id, Subject: change.Subject, Location: change.Location, Start: stamp3339(change.Start), End: stamp3339(change.End)}
	if duration > 0 {
		args.Duration = pointer(duration.String())
	}
	if len(change.AddAttendees) > 0 {
		args.Add = change.AddAttendees
	}
	if len(change.RemoveAttendees) > 0 {
		args.Remove = change.RemoveAttendees
	}
	if change.Teams {
		args.Teams = pointer(true)
	}
	return args
}
