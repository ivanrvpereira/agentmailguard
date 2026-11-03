package gmail

import (
	"context"
	"slices"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

func (m *mailbox) each(ctx context.Context, ids []string, call func(string) error) ([]model.MutationResult, error) {
	out := make([]model.MutationResult, len(ids))
	run(len(ids), func(i int) {
		err := call(ids[i])
		out[i] = model.MutationResult{ID: ids[i], OK: err == nil}
		if err != nil {
			safe := "Gmail error RequestFailed"
			if v, ok := err.(model.MailboxError); ok {
				safe = string(v)
			}
			out[i].Error = &safe
		}
	})
	if ctx.Err() != nil {
		return out, ctx.Err()
	}
	return out, nil
}
func (m *mailbox) modify(ctx context.Context, ids, add, remove []string) ([]model.MutationResult, error) {
	if add == nil {
		add = []string{}
	}
	if remove == nil {
		remove = []string{}
	}
	return m.each(ctx, ids, func(id string) error {
		_, err := m.c.write(ctx, "POST", m.path(id)+"/modify", map[string]any{"addLabelIds": add, "removeLabelIds": remove})
		return err
	})
}
func (m *mailbox) Archive(ctx context.Context, ids []string) ([]model.MutationResult, error) {
	return m.modify(ctx, ids, nil, []string{"INBOX"})
}
func (m *mailbox) Trash(ctx context.Context, ids []string) ([]model.MutationResult, error) {
	return m.each(ctx, ids, func(id string) error {
		_, err := m.c.request(ctx, "Gmail", "POST", m.path(id)+"/trash", nil, nil, "")
		return err
	})
}
func (m *mailbox) Spam(ctx context.Context, ids []string) ([]model.MutationResult, error) {
	return m.modify(ctx, ids, []string{"SPAM"}, []string{"INBOX"})
}
func (m *mailbox) MarkRead(ctx context.Context, ids []string, read bool) ([]model.MutationResult, error) {
	if read {
		return m.modify(ctx, ids, nil, []string{"UNREAD"})
	}
	return m.modify(ctx, ids, []string{"UNREAD"}, nil)
}
func (m *mailbox) Flag(ctx context.Context, ids []string, state model.FlagState) ([]model.MutationResult, error) {
	switch state {
	case model.Flagged:
		return m.modify(ctx, ids, []string{"STARRED"}, nil)
	case model.NoFlag:
		return m.modify(ctx, ids, nil, []string{"STARRED"})
	default:
		return nil, failure("Gmail", "FlagError")
	}
}

// Move adds the dest label and removes the label of from, which becomes a label ID as List makes it
// (https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages/modify). One labels read
// serves the dest check and the source. A source that is the destination is only added, so no label is both added
// and removed.
func (m *mailbox) Move(ctx context.Context, ids []string, from, dest string) ([]model.MutationResult, error) {
	a, err := m.labels(ctx)
	if err != nil {
		return nil, err
	}
	if dest != "INBOX" && !slices.ContainsFunc(a, func(l label) bool { return l.Type == "user" && l.ID == dest }) {
		return nil, failure("Gmail", "FolderError")
	}
	var remove []string
	if source := labelIn(a, from); source != dest {
		remove = []string{source}
	}
	return m.modify(ctx, ids, []string{dest}, remove)
}
func (m *mailbox) Categorize(ctx context.Context, ids, add, remove []string) ([]model.MutationResult, error) {
	user, err := m.userLabels(ctx)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for id, name := range user {
		names[name] = id
	}
	added, removed := []string{}, []string{}
	for _, name := range add {
		id, ok := names[name]
		if !ok {
			return nil, failure("Gmail", "CategoryError")
		}
		if !contains(added, id) {
			added = append(added, id)
		}
	}
	for _, name := range remove {
		if contains(add, name) {
			continue
		}
		id, ok := names[name]
		if !ok {
			return nil, failure("Gmail", "CategoryError")
		}
		if !contains(removed, id) {
			removed = append(removed, id)
		}
	}
	return m.modify(ctx, ids, added, removed)
}
