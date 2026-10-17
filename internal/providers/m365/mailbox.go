package m365

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

const meta = "id,from,sender,subject,receivedDateTime,toRecipients,ccRecipients,parentFolderId,conversationId,isDraft,isRead,flag,categories"
const attachments = "attachments($select=name)"

type mailbox struct{ c *Client }

func (c *Client) Mailbox() model.Mailbox { return &mailbox{c} }
func messageQuery(body bool) url.Values {
	fields := meta
	if body {
		fields += ",body,internetMessageHeaders"
	}
	return url.Values{"$select": {fields}, "$expand": {attachments}}
}
func address(d object) model.Address {
	email := obj(d, "emailAddress")
	return model.Address{Name: text(email, "name"), Address: text(email, "address")}
}
func addresses(d object, k string) []model.Address {
	out := []model.Address{}
	for _, r := range array(d, k) {
		out = append(out, address(r))
	}
	return out
}
func names(d object) []string {
	out := []string{}
	for _, a := range array(d, "attachments") {
		out = append(out, text(a, "name"))
	}
	return out
}
func content(d object) (*string, *model.ContentType) {
	b := obj(d, "body")
	var value *string
	if json.Unmarshal(b["content"], &value) != nil {
		value = nil
	}
	var typ *model.ContentType
	switch strings.ToLower(text(b, "contentType")) {
	case "text":
		v := model.Text
		typ = &v
	case "html":
		v := model.HTML
		typ = &v
	}
	return value, typ
}
func message(d object, body bool) (model.Message, error) {
	m := model.Message{ID: text(d, "id"), ThreadID: text(d, "conversationId"), Subject: text(d, "subject"), IsDraft: boolean(d, "isDraft"), IsRead: boolean(d, "isRead"), Flag: model.NoFlag, Categories: stringsAt(d, "categories"), Attachments: names(d), To: addresses(d, "toRecipients"), CC: addresses(d, "ccRecipients"), ReceivedAt: time.Unix(0, 0).UTC(), Folders: []string{}}
	if m.ID == "" {
		return m, invalidMetadata
	}
	sender := obj(d, "from")
	if len(sender) == 0 {
		sender = obj(d, "sender")
	}
	m.Sender = address(sender)
	if date := text(d, "receivedDateTime"); date != "" {
		stamp, err := time.Parse(time.RFC3339Nano, date)
		if err != nil {
			return m, invalidMetadata
		}
		m.ReceivedAt = stamp
	}
	if f := text(d, "parentFolderId"); f != "" {
		m.Folders = []string{f}
	}
	switch text(obj(d, "flag"), "flagStatus") {
	case "flagged":
		m.Flag = model.Flagged
	case "complete":
		m.Flag = model.Complete
	}
	if body {
		m.Body, m.ContentType = content(d)
		auth := parseAuth(array(d, "internetMessageHeaders"))
		m.Auth = &auth
	}
	return m, nil
}
func (b *mailbox) Get(ctx context.Context, ids []string, body bool) ([]model.Message, error) {
	out := make([]model.Message, len(ids))
	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Go(func() {
			d, err := b.c.get(ctx, b.c.endpoint("/me/messages/"+escapeID(id), messageQuery(body)), false)
			if err == nil {
				out[i], err = message(d, body)
			}
			errs[i] = err
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (b *mailbox) List(ctx context.Context, folder string, count, skip int, body bool) ([]model.Message, error) {
	if count < 1 || skip < 0 {
		return nil, invalidMetadata
	}
	q := messageQuery(body)
	q.Set("$top", strconv.Itoa(count))
	q.Set("$skip", strconv.Itoa(skip))
	q.Set("$orderby", "receivedDateTime desc")
	return b.collection(ctx, b.c.endpoint("/me/mailFolders/"+escapeID(folder)+"/messages", q), body)
}

// notFound is the error of a Graph 404, which a missing message gets.
const notFound = model.MailboxError("Graph error 404")

// Thread filters /me/messages, which covers every folder, by conversationId
// (https://learn.microsoft.com/en-us/graph/api/user-list-messages?view=graph-rest-1.0). Graph needs a property of
// $orderby to come first in $filter, or it answers InefficientFilter, so receivedDateTime leads with a bound that
// keeps every message. One page of count+1 messages, newest first, tells whether older ones were left out.
func (b *mailbox) Thread(ctx context.Context, thread string, count int) ([]model.Message, bool, error) {
	if count < 1 {
		return nil, false, invalidMetadata
	}
	q := messageQuery(false)
	q.Set("$filter", "receivedDateTime ge 1900-01-01T00:00:00Z and conversationId eq '"+strings.ReplaceAll(thread, "'", "''")+"'")
	q.Set("$orderby", "receivedDateTime desc")
	q.Set("$top", strconv.Itoa(count+1))
	out, err := b.collection(ctx, b.c.endpoint("/me/messages", q), false)
	if err != nil {
		return nil, false, err
	}
	if len(out) == 0 {
		return nil, false, notFound
	}
	older := len(out) > count
	out = out[:min(len(out), count)]
	slices.Reverse(out)
	return out, older, nil
}
func kql(q model.SearchQuery) string {
	term := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		if strings.Contains(s, " ") {
			return `\"` + s + `\"`
		}
		return s
	}
	parts := []string{}
	if q.Sender != nil && *q.Sender != "" {
		parts = append(parts, "from:"+term(*q.Sender))
	}
	// Graph $search on messages (https://learn.microsoft.com/en-us/graph/search-query-parameter, "Use $search on
	// message collections") searches the to and cc properties and joins terms with OR.
	if q.To != nil && *q.To != "" {
		parts = append(parts, "(to:"+term(*q.To)+" OR cc:"+term(*q.To)+")")
	}
	if q.Subject != nil && *q.Subject != "" {
		parts = append(parts, "subject:"+term(*q.Subject))
	}
	if q.After != nil {
		parts = append(parts, "received>="+q.After.Format("2006-01-02"))
	}
	if q.Before != nil {
		parts = append(parts, "received<="+q.Before.Format("2006-01-02"))
	}
	if q.Text != nil && *q.Text != "" {
		parts = append(parts, term(*q.Text))
	}
	return `"` + strings.Join(parts, " ") + `"`
}
func (b *mailbox) Search(ctx context.Context, query model.SearchQuery, count int, body bool) ([]model.Message, error) {
	if count < 1 {
		return nil, invalidMetadata
	}
	path := "/me/messages"
	if query.Folder != nil {
		path = "/me/mailFolders/" + escapeID(*query.Folder) + "/messages"
	}
	q := messageQuery(body)
	q.Set("$top", strconv.Itoa(count))
	q.Set("$search", kql(query))
	return b.collection(ctx, b.c.endpoint(path, q), body)
}
func (b *mailbox) collection(ctx context.Context, raw string, body bool) ([]model.Message, error) {
	d, err := b.c.get(ctx, raw, false)
	if err != nil {
		return nil, err
	}
	items, err := pageItems(d)
	if err != nil {
		return nil, err
	}
	out := []model.Message{}
	for _, d := range items {
		m, err := message(d, body)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// Folders marks Deleted Items and every folder below it, so moves there can be refused. A failed lookup fails closed.
func (b *mailbox) Folders(ctx context.Context) ([]model.Folder, error) {
	deleted, err := b.c.get(ctx, b.c.endpoint("/me/mailFolders/deleteditems", url.Values{"$select": {"id"}}), false)
	if err != nil {
		return nil, err
	}
	deletedID := text(deleted, "id")
	if deletedID == "" {
		return nil, invalidMetadata
	}
	type listing struct {
		link  string
		trash bool
	}
	q := url.Values{"$select": {"id,displayName,childFolderCount"}, "$top": {"100"}}
	pending := []listing{{link: b.c.endpoint("/me/mailFolders", q)}}
	seen := map[string]bool{}
	out := []model.Folder{}
	for len(pending) > 0 {
		first := pending[0]
		pending = pending[1:]
		items, err := b.c.all(ctx, first.link, false)
		if err != nil {
			return nil, err
		}
		for _, d := range items {
			id := text(d, "id")
			if id == "" || seen[id] || len(seen) >= 10000 {
				return nil, invalidPage
			}
			seen[id] = true
			trash := first.trash || id == deletedID
			out = append(out, model.Folder{ID: id, Name: text(d, "displayName"), Trash: trash})
			var children int
			_ = json.Unmarshal(d["childFolderCount"], &children)
			if children > 0 {
				pending = append(pending, listing{b.c.endpoint("/me/mailFolders/"+escapeID(id)+"/childFolders", q), trash})
			}
		}
	}
	return out, nil
}
func (b *mailbox) Categories(ctx context.Context) ([]string, error) {
	items, err := b.c.all(ctx, b.c.endpoint("/me/outlook/masterCategories", url.Values{"$select": {"displayName"}}), false)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, d := range items {
		if name := text(d, "displayName"); name != "" {
			out = append(out, name)
		}
	}
	return out, nil
}

var commentPattern = regexp.MustCompile(`\([^()]*\)`)
var resultPattern = regexp.MustCompile(`(?i)(spf|dkim|dmarc|compauth)\s*=\s*([a-z]+)\b`)
var reasonPattern = regexp.MustCompile(`(?i)^\s*reason\s*=\s*([0-9]+)\b`)

func rank(s string) int {
	if s == "fail" {
		return 2
	}
	if s == "pass" {
		return 0
	}
	return 1
}
func word(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' }
func wordAt(s string, pos int) bool {
	if pos >= len(s) {
		return false
	}
	r, _ := utf8.DecodeRuneInString(s[pos:])
	return word(r)
}
func parseAuth(headers []object) model.AuthResults {
	results := map[string]string{}
	var reason *string
	for _, h := range headers {
		if strings.ToLower(text(h, "name")) != "authentication-results" {
			continue
		}
		s := commentPattern.ReplaceAllString(text(h, "value"), " ")
		for _, loc := range resultPattern.FindAllStringSubmatchIndex(s, -1) {
			if loc[0] > 0 {
				ch, _ := utf8.DecodeLastRuneInString(s[:loc[0]])
				if word(ch) || strings.ContainsRune(".=-", ch) {
					continue
				}
			}
			if wordAt(s, loc[1]) {
				continue
			}
			method, value := strings.ToLower(s[loc[2]:loc[3]]), strings.ToLower(s[loc[4]:loc[5]])
			if prev, ok := results[method]; ok && rank(value) <= rank(prev) {
				continue
			}
			results[method] = value
			if method == "compauth" {
				reason = nil
				if r := reasonPattern.FindStringSubmatch(s[loc[1]:]); r != nil && !wordAt(s, loc[1]+len(r[0])) {
					v := r[1]
					reason = &v
				}
			}
		}
	}
	ptr := func(k string) *string {
		v, ok := results[k]
		if !ok {
			return nil
		}
		return &v
	}
	return model.AuthResults{SPF: ptr("spf"), DKIM: ptr("dkim"), DMARC: ptr("dmarc"), CompAuth: ptr("compauth"), CompAuthReason: reason}
}
