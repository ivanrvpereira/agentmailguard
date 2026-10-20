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
func (g *Guard) Search(ctx context.Context, q model.SearchQuery, count int) ([]views.MailSummary, error) {
	if err := checkCount(count); err != nil {
		return nil, err
	}
	nonempty := func(s *string) bool { return s != nil && *s != "" }
	if !nonempty(q.Text) && !nonempty(q.Sender) && !nonempty(q.Subject) && q.After == nil && q.Before == nil {
		return nil, GuardError("search needs at least one of text, from, subject, after, before")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	messages, err := g.mailbox.Search(ctx, q, count, false)
	if err != nil {
		return nil, safeErr(ctx, err)
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
