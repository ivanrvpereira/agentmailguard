package guard

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/ivanrvpereira/agentmailguard/internal/content"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/views"
)

type LinkResult struct {
	Link  string  `json:"link"`
	URL   *string `json:"url,omitempty"`
	Error *string `json:"error,omitempty"`
}

func (g *Guard) Links(ctx context.Context, id string, ids []string) ([]LinkResult, error) {
	_, row, r, err := g.read(ctx, id)
	if err != nil {
		return nil, err
	}
	if row.Verdict != model.Green || r == nil {
		return nil, GuardError("link refused: message is " + string(row.Verdict) + ", not green")
	}
	if len(ids) == 0 {
		ids = make([]string, 0, len(r.Links))
		for id := range r.Links {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			a, _ := strconv.Atoi(strings.TrimPrefix(ids[i], "L"))
			b, _ := strconv.Atoi(strings.TrimPrefix(ids[j], "L"))
			return a < b
		})
	}
	out := make([]LinkResult, 0, len(ids))
	for _, id := range ids {
		item := LinkResult{Link: id}
		url, ok := r.Links[id]
		if !ok {
			item.Error = new("no such link in this message")
		} else if content.IsSecretURL(url) {
			item.Error = new("the URL likely carries a secret (sign-in, reset, token)")
		} else {
			item.URL = new(url)
		}
		out = append(out, item)
	}
	return out, ctx.Err()
}

// Search screens one page of the provider's search results, as List does.
func (g *Guard) Search(ctx context.Context, q model.SearchQuery, page model.PageRequest) (views.MailPage, error) {
	if err := checkPage(page); err != nil {
		return views.MailPage{}, err
	}
	nonempty := func(s *string) bool { return s != nil && *s != "" }
	if !nonempty(q.Text) && !nonempty(q.Sender) && !nonempty(q.To) && !nonempty(q.Subject) && q.After == nil && q.Before == nil {
		return views.MailPage{}, GuardError("search needs at least one of text, from, to, subject, after, before")
	}
	if err := ctx.Err(); err != nil {
		return views.MailPage{}, err
	}
	found, err := g.mailbox.Search(ctx, q, page, false)
	if err != nil {
		return views.MailPage{}, safeErr(ctx, err)
	}
	return g.mailPage(ctx, found)
}

// threadValid checks a Thread result against the model.Mailbox contract: at least one message, all in the
// requested thread, newest first. summaries refuses empty and duplicate message IDs.
func threadValid(messages []model.Message, thread string) bool {
	if len(messages) < 1 {
		return false
	}
	for i, m := range messages {
		if m.ThreadID != thread || i > 0 && m.ReceivedAt.After(messages[i-1].ReceivedAt) {
			return false
		}
	}
	return true
}

// Thread screens the messages of a conversation, newest first, exactly as List screens a folder.
func (g *Guard) Thread(ctx context.Context, thread string) ([]views.MailSummary, error) {
	if thread == "" {
		return nil, GuardError("give a thread ID")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	messages, err := g.mailbox.Thread(ctx, thread)
	if err != nil {
		return nil, safeErr(ctx, err)
	}
	if !threadValid(messages, thread) {
		return nil, GuardError("ValueError")
	}
	categories, err := g.Categories(ctx)
	if err != nil {
		return nil, err
	}
	return g.summaries(ctx, messages, categories)
}
func (g *Guard) Folders(ctx context.Context) ([]model.Folder, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	folders, err := g.mailbox.Folders(ctx)
	if err != nil {
		return nil, safeErr(ctx, err)
	}
	out := make([]model.Folder, 0, len(folders))
	for _, f := range folders {
		out = append(out, model.Folder{ID: f.ID, Name: content.CleanSubject(f.Name)})
	}
	return out, ctx.Err()
}
