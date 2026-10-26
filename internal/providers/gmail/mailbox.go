package gmail

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

type mailbox struct{ c *Client }

func (c *Client) Mailbox() model.Mailbox { return &mailbox{c} }
func (m *mailbox) path(id string) string { return m.c.options.GmailURL + "/messages/" + segment(id) }

type label struct{ ID, Name, Type string }

func (m *mailbox) labels(ctx context.Context) ([]label, error) {
	v, err := m.c.get(ctx, "Gmail", m.c.options.GmailURL+"/labels", nil)
	if err != nil {
		return nil, err
	}
	a, err := items(v, "labels", "Gmail")
	if err != nil {
		return nil, err
	}
	out := []label{}
	for _, raw := range a {
		var l label
		if json.Unmarshal(raw, &l) != nil || l.ID == "" {
			return nil, failure("Gmail", "InvalidMetadata")
		}
		out = append(out, l)
	}
	return out, nil
}
func (m *mailbox) userLabels(ctx context.Context) (map[string]string, error) {
	a, err := m.labels(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, l := range a {
		if l.Type == "user" {
			out[l.ID] = l.Name
		}
	}
	return out, nil
}
func (m *mailbox) labelID(ctx context.Context, id string) (string, error) {
	a, err := m.labels(ctx)
	if err != nil {
		return "", err
	}
	for _, l := range a {
		if l.Type == "system" && l.ID == strings.ToUpper(id) {
			return l.ID, nil
		}
	}
	return id, nil
}
func (m *mailbox) Folders(ctx context.Context) ([]model.Folder, error) {
	a, err := m.labels(ctx)
	if err != nil {
		return nil, err
	}
	out := []model.Folder{}
	for _, l := range a {
		if l.ID != "UNREAD" && l.ID != "STARRED" {
			out = append(out, model.Folder{ID: l.ID, Name: l.Name})
		}
	}
	return out, nil
}
func (m *mailbox) Categories(ctx context.Context) ([]string, error) {
	a, err := m.labels(ctx)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, l := range a {
		if l.Type == "user" {
			out = append(out, l.Name)
		}
	}
	return out, nil
}
func (m *mailbox) ids(ctx context.Context, q url.Values, count int) ([]string, error) {
	out := []string{}
	if count <= 0 {
		return out, nil
	}
	q.Set("maxResults", strconv.Itoa(min(count, 500)))
	seen := map[string]bool{}
	for len(out) < count {
		v, err := m.c.get(ctx, "Gmail", m.c.options.GmailURL+"/messages", q)
		if err != nil {
			return nil, err
		}
		a, err := items(v, "messages", "Gmail")
		if err != nil {
			return nil, err
		}
		for _, raw := range a {
			v, err := object(raw, "Gmail")
			if err != nil {
				return nil, err
			}
			id := stringField(v, "id")
			if strings.TrimSpace(id) == "" {
				return nil, failure("Gmail", "InvalidMetadata")
			}
			out = append(out, id)
		}
		token, err := pageToken(v, seen, "Gmail")
		if err != nil {
			return nil, err
		}
		if token == "" {
			break
		}
		q.Set("pageToken", token)
	}
	return out[:min(len(out), count)], nil
}
func (m *mailbox) List(ctx context.Context, folder string, count, skip int, _ bool) ([]model.Message, error) {
	if count < 0 || skip < 0 || count > int(^uint(0)>>1)-skip {
		return nil, failure("Gmail", "ValueError")
	}
	if count == 0 {
		return []model.Message{}, nil
	}
	id, err := m.labelID(ctx, folder)
	if err != nil {
		return nil, err
	}
	ids, err := m.ids(ctx, url.Values{"labelIds": {id}}, count+skip)
	if err != nil {
		return nil, err
	}
	return m.Get(ctx, ids[min(skip, len(ids)):], true)
}
func phrase(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || strings.ContainsRune("_@.+&-", r) {
			return unicode.ToLower(r)
		}
		return ' '
	}, s)
	return `"` + strings.Join(strings.Fields(s), " ") + `"`
}
func (m *mailbox) Search(ctx context.Context, query model.SearchQuery, count int, _ bool) ([]model.Message, error) {
	if count < 0 {
		return nil, failure("Gmail", "ValueError")
	}
	terms := []string{}
	if query.Sender != nil && *query.Sender != "" {
		terms = append(terms, "from:"+phrase(*query.Sender))
	}
	// Gmail search operators (https://support.google.com/mail/answer/7190): to: and cc: match the recipient
	// fields, and { } matches any of the terms inside.
	if query.To != nil && *query.To != "" {
		terms = append(terms, "{to:"+phrase(*query.To)+" cc:"+phrase(*query.To)+"}")
	}
	if query.Subject != nil && *query.Subject != "" {
		terms = append(terms, "subject:"+phrase(*query.Subject))
	}
	if query.After != nil {
		terms = append(terms, "after:"+query.After.Format("2006/01/02"))
	}
	if query.Before != nil {
		terms = append(terms, "before:"+query.Before.Format("2006/01/02"))
	}
	if query.Text != nil && *query.Text != "" {
		terms = append(terms, phrase(*query.Text))
	}
	q := url.Values{"q": {strings.Join(terms, " ")}}
	if query.Folder != nil {
		id, err := m.labelID(ctx, *query.Folder)
		if err != nil {
			return nil, err
		}
		q.Set("labelIds", id)
	}
	ids, err := m.ids(ctx, q, count)
	if err != nil {
		return nil, err
	}
	return m.Get(ctx, ids, true)
}

// Thread takes the message IDs of the whole conversation from threads.get, with every label, spam and trash
// included (https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.threads/get), and reads them
// as List does. The API does not document the order of the messages, so they are sorted by received time, newest
// first; ties keep the reverse threads.get order.
func (m *mailbox) Thread(ctx context.Context, thread string) ([]model.Message, error) {
	v, err := m.c.get(ctx, "Gmail", m.c.options.GmailURL+"/threads/"+segment(thread), url.Values{"format": {"minimal"}})
	if err != nil {
		return nil, err
	}
	a, err := items(v, "messages", "Gmail")
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, raw := range a {
		v, err := object(raw, "Gmail")
		if err != nil {
			return nil, err
		}
		id := stringField(v, "id")
		if strings.TrimSpace(id) == "" {
			return nil, failure("Gmail", "InvalidMetadata")
		}
		ids = append(ids, id)
	}
	slices.Reverse(ids)
	out, err := m.Get(ctx, ids, true)
	if err != nil {
		return nil, err
	}
	slices.SortStableFunc(out, func(a, b model.Message) int { return b.ReceivedAt.Compare(a.ReceivedAt) })
	return out, nil
}

// run limits goroutines as well as shared HTTP slots, while preserving caller order.
func run(n int, call func(int)) {
	var wg sync.WaitGroup
	jobs := make(chan int)
	for range min(n, 8) {
		wg.Go(func() {
			for i := range jobs {
				call(i)
			}
		})
	}
	for i := range n {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
}
func (m *mailbox) Get(ctx context.Context, ids []string, body bool) ([]model.Message, error) {
	out := make([]model.Message, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	labels, err := m.userLabels(ctx)
	if err != nil {
		return nil, err
	}
	errs := make([]error, len(ids))
	run(len(ids), func(i int) {
		v, e := m.c.get(ctx, "Gmail", m.path(ids[i]), url.Values{"format": {"full"}})
		if e == nil {
			out[i], e = parseMessage(v, labels, body)
			if e == nil && out[i].ID != ids[i] {
				e = failure("Gmail", "InvalidMetadata")
			}
		}
		errs[i] = e
	})
	for _, e := range errs {
		if e != nil {
			return nil, e
		}
	}
	return out, nil
}
