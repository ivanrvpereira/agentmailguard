package m365

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
)

func (c *Client) lockMutation(ctx context.Context) bool {
	select {
	case c.mutationGate <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}
func recipients(addresses []string) []map[string]any {
	out := []map[string]any{}
	for _, a := range addresses {
		out = append(out, map[string]any{"emailAddress": map[string]string{"address": a}})
	}
	return out
}
func (b *mailbox) draft(ctx context.Context, path string, body map[string]any) (string, error) {
	if !b.c.lockMutation(ctx) {
		return "", requestFailed
	}
	defer func() { <-b.c.mutationGate }()
	d, err := b.c.request(ctx, http.MethodPost, b.c.endpoint(path, nil), body, false)
	if err != nil {
		return "", err
	}
	id := text(d, "id")
	if id == "" {
		return "", invalidMetadata
	}
	return id, nil
}
func (b *mailbox) DraftNew(ctx context.Context, draft model.NewDraft) (string, error) {
	return b.draft(ctx, "/me/messages", map[string]any{"subject": draft.Subject, "body": map[string]string{"contentType": "text", "content": draft.Body}, "toRecipients": recipients(draft.To)})
}
func (b *mailbox) DraftReply(ctx context.Context, id, comment string) (string, error) {
	return b.draft(ctx, "/me/messages/"+escapeID(id)+"/createReply", map[string]any{"comment": comment})
}
func (b *mailbox) DraftForward(ctx context.Context, id string, to []string) (string, error) {
	return b.draft(ctx, "/me/messages/"+escapeID(id)+"/createForward", map[string]any{"comment": "", "message": map[string]any{"toRecipients": recipients(to)}})
}
func (b *mailbox) DraftUpdate(ctx context.Context, id, body string) (string, error) {
	if !b.c.lockMutation(ctx) {
		return "", requestFailed
	}
	defer func() { <-b.c.mutationGate }()
	q := url.Values{"$select": {"isDraft,conversationIndex"}, "$expand": {"singleValueExtendedProperties($filter=id eq 'String 0x1042')"}}
	d, err := b.c.get(ctx, b.c.endpoint("/me/messages/"+escapeID(id), q), false)
	if err != nil {
		return "", err
	}
	encoded := text(d, "conversationIndex")
	index, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || strings.ContainsAny(encoded, "\r\n") || len(index) != 22 || !boolean(d, "isDraft") {
		return "", invalidMetadata
	}
	if raw, ok := d["singleValueExtendedProperties"]; ok && string(raw) != "null" {
		var props []object
		if json.Unmarshal(raw, &props) != nil || len(props) > 0 {
			return "", invalidMetadata
		}
	}
	if err := b.patch(ctx, id, map[string]any{"body": map[string]string{"contentType": "text", "content": body}}); err != nil {
		return "", err
	}
	return id, nil
}
func (b *mailbox) each(ctx context.Context, ids []string, call func(string) error) ([]model.MutationResult, error) {
	// Serialize mutation operations for this account, including category read-modify-write.
	out := make([]model.MutationResult, len(ids))
	if !b.c.lockMutation(ctx) {
		for i, id := range ids {
			code := string(requestFailed)
			out[i] = model.MutationResult{ID: id, Error: &code}
		}
		return out, nil
	}
	defer func() { <-b.c.mutationGate }()
	var wg sync.WaitGroup
	// Duplicate IDs must not race their own read-modify-write, but retain one result per input.
	unique := map[string][]int{}
	for i, id := range ids {
		unique[id] = append(unique[id], i)
	}
	for id, positions := range unique {
		wg.Go(func() {
			for _, i := range positions {
				err := call(id)
				out[i] = model.MutationResult{ID: id, OK: err == nil}
				if err != nil {
					s := err.Error()
					out[i].Error = &s
				}
			}
		})
	}
	wg.Wait()
	return out, nil
}
func (b *mailbox) Archive(ctx context.Context, ids []string) ([]model.MutationResult, error) {
	return b.Move(ctx, ids, "archive")
}
func (b *mailbox) Trash(ctx context.Context, ids []string) ([]model.MutationResult, error) {
	return b.Move(ctx, ids, "deleteditems")
}
func (b *mailbox) Move(ctx context.Context, ids []string, dest string) ([]model.MutationResult, error) {
	return b.each(ctx, ids, func(id string) error {
		_, err := b.c.request(ctx, http.MethodPost, b.c.endpoint("/me/messages/"+escapeID(id)+"/move", nil), map[string]any{"destinationId": dest}, false)
		return err
	})
}
func (b *mailbox) patch(ctx context.Context, id string, patch map[string]any) error {
	_, err := b.c.request(ctx, http.MethodPatch, b.c.endpoint("/me/messages/"+escapeID(id), nil), patch, false)
	return err
}
func (b *mailbox) MarkRead(ctx context.Context, ids []string, read bool) ([]model.MutationResult, error) {
	return b.each(ctx, ids, func(id string) error { return b.patch(ctx, id, map[string]any{"isRead": read}) })
}
func (b *mailbox) Flag(ctx context.Context, ids []string, state model.FlagState) ([]model.MutationResult, error) {
	var status string
	switch state {
	case model.Flagged:
		status = "flagged"
	case model.Complete:
		status = "complete"
	case model.NoFlag:
		status = "notFlagged"
	default:
		return nil, invalidMetadata
	}
	return b.each(ctx, ids, func(id string) error {
		return b.patch(ctx, id, map[string]any{"flag": map[string]string{"flagStatus": status}})
	})
}
func (b *mailbox) Categorize(ctx context.Context, ids []string, add, remove []string) ([]model.MutationResult, error) {
	return b.each(ctx, ids, func(id string) error {
		d, err := b.c.get(ctx, b.c.endpoint("/me/messages/"+escapeID(id), url.Values{"$select": {"categories"}}), false)
		if err != nil {
			return err
		}
		var current []*string
		if json.Unmarshal(d["categories"], &current) != nil || current == nil {
			return invalidMetadata
		}
		out := []string{}
		for _, name := range current {
			if name == nil {
				return invalidMetadata
			}
			if !slices.Contains(remove, *name) {
				out = append(out, *name)
			}
		}
		for _, name := range add {
			if !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
		return b.patch(ctx, id, map[string]any{"categories": out})
	})
}
