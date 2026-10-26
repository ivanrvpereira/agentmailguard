package gmail

import (
	"context"
	"encoding/json"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

func (m *mailbox) ScanInbox(ctx context.Context, since time.Time, until *time.Time) (model.InboxScan, error) {
	out := model.InboxScan{Messages: []model.InboxEntry{}, Failures: []model.ScanFailure{}}
	// Go time.Time always denotes an instant, including year one; zero is a valid cutoff.
	if since.Year() < 1 || since.Year() > 9999 || (until != nil && (until.Year() < 1 || until.Year() > 9999 || !until.After(since))) {
		return out, failure("Gmail", "ValueError")
	}
	search := "after:" + strconv.FormatInt(since.Unix()-86400, 10)
	if until != nil {
		search += " before:" + strconv.FormatInt(until.Unix()+86400, 10)
	}
	q := url.Values{"labelIds": {"INBOX"}, "q": {search}, "maxResults": {"500"}}
	ids := []string{}
	seen := map[string]bool{}
	for {
		v, err := m.c.get(ctx, "Gmail", m.c.options.GmailURL+"/messages", q)
		if ctx.Err() != nil {
			return out, ctx.Err()
		}
		if err != nil {
			out.Failures = append(out.Failures, model.ScanFailure{Error: "Gmail error RequestFailed"})
			break
		}
		a, err := items(v, "messages", "Gmail")
		if err != nil {
			out.Failures = append(out.Failures, model.ScanFailure{Error: "Gmail error InvalidPage"})
			break
		}
		for _, raw := range a {
			obj, err := object(raw, "Gmail")
			id := stringField(obj, "id")
			if err != nil || strings.TrimSpace(id) == "" {
				out.Failures = append(out.Failures, model.ScanFailure{Error: "Gmail error InvalidMetadata"})
			} else {
				ids = append(ids, id)
			}
		}
		token, err := pageToken(v, seen, "Gmail")
		if err != nil {
			out.Failures = append(out.Failures, model.ScanFailure{Error: "Gmail error InvalidPage"})
			break
		}
		if token == "" {
			break
		}
		q.Set("pageToken", token)
	}
	entries := make([]*model.InboxEntry, len(ids))
	failures := make([]*model.ScanFailure, len(ids))
	run(len(ids), func(i int) {
		id := ids[i]
		v, err := m.c.get(ctx, "Gmail", m.path(id), url.Values{"format": {"minimal"}, "fields": {"id,labelIds,internalDate"}})
		if err != nil {
			failures[i] = &model.ScanFailure{ID: &id, Error: "Gmail error RequestFailed"}
			return
		}
		invalid := func() { failures[i] = &model.ScanFailure{ID: &id, Error: "Gmail error InvalidMetadata"} }
		var labels []string
		if json.Unmarshal(v["labelIds"], &labels) != nil || labels == nil || stringField(v, "id") != id {
			invalid()
			return
		}
		if slices.Contains(labels, "") {
			invalid()
			return
		}
		received, err := timestamp(stringField(v, "internalDate"))
		if err != nil {
			invalid()
			return
		}
		if contains(labels, "DRAFT") || received.Before(since) || (until != nil && !received.Before(*until)) {
			return
		}
		entries[i] = &model.InboxEntry{ID: id, ReceivedAt: received}
	})
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	for i := range ids {
		if entries[i] != nil {
			out.Messages = append(out.Messages, *entries[i])
		}
		if failures[i] != nil {
			out.Failures = append(out.Failures, *failures[i])
		}
	}
	return out, nil
}
