package gmail

import (
	"cmp"
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

// systemLabels maps the common folder names to their Gmail system label IDs
// (https://developers.google.com/workspace/gmail/api/guides/labels#manage_labels_on_messages_threads).
var systemLabels = map[model.FolderName]string{model.Inbox: "INBOX", model.Trash: "TRASH", model.Spam: "SPAM"}

// bins marks the Gmail system labels that are the trash and spam.
var bins = map[string]model.FolderName{systemLabels[model.Trash]: model.Trash, systemLabels[model.Spam]: model.Spam}

// labelOf gives the label ID of folder: a common folder name maps to its system label, and any other value is a
// label ID as given.
func labelOf(folder string) string {
	if n, ok := model.ParseFolderName(folder); ok {
		return systemLabels[n]
	}
	return folder
}

// inFolder makes q read the label of folder. messages.list skips the trash and spam unless includeSpamTrash is set
// (https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages/list), so a read of either
// sets it.
func inFolder(q url.Values, folder string) url.Values {
	id := labelOf(folder)
	q.Set("labelIds", id)
	if bins[id] != "" {
		q.Set("includeSpamTrash", "true")
	}
	return q
}

func (m *mailbox) Folders(ctx context.Context) ([]model.Folder, error) {
	a, err := m.labels(ctx)
	if err != nil {
		return nil, err
	}
	out := []model.Folder{}
	for _, l := range a {
		if l.ID != "UNREAD" && l.ID != "STARRED" {
			out = append(out, model.Folder{ID: l.ID, Name: l.Name, Bin: bins[l.ID]})
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

// maxPage is the largest messages.list page that Gmail documents (maxResults at most 500).
const maxPage = 500

// ids reads one messages.list page (https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages/list).
// maxResults goes only with a size, as given, so Gmail's default and maximum apply. A page with more IDs than the
// size or than maxPage is refused before any message is read. The page is a pageToken that only Gmail
// reads, sent as a query value; a nextPageToken equal to it is refused, as it would never end.
func (m *mailbox) ids(ctx context.Context, q url.Values, page model.PageRequest) ([]string, string, error) {
	if page.Size > 0 {
		q.Set("maxResults", strconv.Itoa(page.Size))
	}
	if page.Page != "" {
		q.Set("pageToken", page.Page)
	}
	v, err := m.c.get(ctx, "Gmail", m.c.options.GmailURL+"/messages", q)
	if err != nil {
		return nil, "", err
	}
	a, err := items(v, "messages", "Gmail")
	if err != nil {
		return nil, "", err
	}
	if len(a) > min(cmp.Or(page.Size, maxPage), maxPage) {
		return nil, "", failure("Gmail", "InvalidPage")
	}
	out := []string{}
	for _, raw := range a {
		v, err := object(raw, "Gmail")
		if err != nil {
			return nil, "", err
		}
		id := stringField(v, "id")
		if strings.TrimSpace(id) == "" {
			return nil, "", failure("Gmail", "InvalidMetadata")
		}
		out = append(out, id)
	}
	next, err := pageToken(v, map[string]bool{page.Page: true}, "Gmail")
	return out, next, err
}

// messages reads the page that q and page select, each message in full.
func (m *mailbox) messages(ctx context.Context, q url.Values, page model.PageRequest) (model.MessagePage, error) {
	ids, next, err := m.ids(ctx, q, page)
	if err != nil {
		return model.MessagePage{}, err
	}
	out, err := m.Get(ctx, ids, true)
	if err != nil {
		return model.MessagePage{}, err
	}
	return model.MessagePage{Messages: out, Next: next}, nil
}
func (m *mailbox) List(ctx context.Context, folder string, page model.PageRequest, _ bool) (model.MessagePage, error) {
	if page.Size < 0 {
		return model.MessagePage{}, failure("Gmail", "ValueError")
	}
	return m.messages(ctx, inFolder(url.Values{}, folder), page)
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
func (m *mailbox) Search(ctx context.Context, query model.SearchQuery, page model.PageRequest, _ bool) (model.MessagePage, error) {
	if page.Size < 0 {
		return model.MessagePage{}, failure("Gmail", "ValueError")
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
	// Gmail reads a date in after: and before: as midnight Pacific time; a value in seconds is an exact time
	// (https://developers.google.com/workspace/gmail/api/guides/filtering). The guide does not say whether the
	// boundary second matches, so the query reaches one second past each bound, and Search keeps exact times.
	if query.After != nil {
		terms = append(terms, "after:"+strconv.FormatInt(query.After.Unix()-1, 10))
	}
	if query.Before != nil {
		end := query.Before.Unix()
		if query.Before.Nanosecond() > 0 {
			end++
		}
		terms = append(terms, "before:"+strconv.FormatInt(end+1, 10))
	}
	if query.Text != nil && *query.Text != "" {
		terms = append(terms, phrase(*query.Text))
	}
	q := url.Values{"q": {strings.Join(terms, " ")}}
	if query.Folder != nil {
		inFolder(q, *query.Folder)
	}
	found, err := m.messages(ctx, q, page)
	if err != nil {
		return found, err
	}
	// ReceivedAt is internalDate. The wider query can leave fewer messages than the page size; Next still continues
	// the same Gmail search.
	found.Messages = slices.DeleteFunc(found.Messages, func(msg model.Message) bool { return !query.Received(msg.ReceivedAt) })
	return found, nil
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

// ThreadMessagesIn keeps the messages of Thread that carry the label of folder, which becomes a label ID as List
// makes it. A folder that is no label of the account fails, so that a wrong folder is not an empty selection.
// Trash and spam keep a message's other labels, such as SENT and user labels, so a folder other than TRASH or SPAM
// skips their messages, as messages.list does by default (includeSpamTrash,
// https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages/list).
func (m *mailbox) ThreadMessagesIn(ctx context.Context, thread, folder string) ([]string, error) {
	all, err := m.Thread(ctx, thread)
	if err != nil {
		return nil, err
	}
	a, err := m.labels(ctx)
	if err != nil {
		return nil, err
	}
	id := labelOf(folder)
	if !slices.ContainsFunc(a, func(l label) bool { return l.ID == id }) {
		return nil, failure("Gmail", "FolderError")
	}
	out := []string{}
	for _, msg := range all {
		if !slices.Contains(msg.Folders, id) {
			continue
		}
		if id != "TRASH" && id != "SPAM" && (slices.Contains(msg.Folders, "TRASH") || slices.Contains(msg.Folders, "SPAM")) {
			continue
		}
		out = append(out, msg.ID)
	}
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
