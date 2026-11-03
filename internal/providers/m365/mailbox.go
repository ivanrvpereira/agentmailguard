package m365

import (
	"cmp"
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

const meta = "id,from,sender,subject,receivedDateTime,toRecipients,ccRecipients,bccRecipients,parentFolderId,conversationId,isDraft,isRead,flag,categories"
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

// message reads BCC only of a draft or of a message from the account's address, which is one that the account sent,
// as ReplyRecipients tells them apart.
func (b *mailbox) message(d object, body bool) (model.Message, error) {
	m := model.Message{ID: text(d, "id"), ThreadID: text(d, "conversationId"), Subject: text(d, "subject"), IsDraft: boolean(d, "isDraft"), IsRead: boolean(d, "isRead"), Flag: model.NoFlag, Categories: stringsAt(d, "categories"), Attachments: names(d), To: addresses(d, "toRecipients"), CC: addresses(d, "ccRecipients"), BCC: []model.Address{}, ReceivedAt: model.UnknownReceived, Folders: []string{}}
	if m.ID == "" {
		return m, invalidMetadata
	}
	if m.IsDraft || strings.EqualFold(address(obj(d, "from")).Address, b.c.account.Email) {
		m.BCC = addresses(d, "bccRecipients")
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
				out[i], err = b.message(d, body)
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

// List reads one page of folder, newest first. $top goes only with a size, as given, so Graph's default (10) and
// maximum (1,000) apply (https://learn.microsoft.com/en-us/graph/api/user-list-messages?view=graph-rest-1.0).
func (b *mailbox) List(ctx context.Context, folder string, page model.PageRequest, body bool) (model.MessagePage, error) {
	q := messageQuery(body)
	q.Set("$orderby", "receivedDateTime desc")
	return b.page(ctx, "/me/mailFolders/"+escapeID(folderName(folder))+"/messages", q, page, body)
}

// notFound is the error of a Graph 404, which a missing message gets.
const notFound = model.MailboxError("Graph error 404")

// Thread filters /me/messages, which covers every folder, by conversationId
// (https://learn.microsoft.com/en-us/graph/api/user-list-messages?view=graph-rest-1.0) and reads every page in
// Graph's page size. Graph needs a property of $orderby to come first in $filter, or it answers InefficientFilter,
// so receivedDateTime leads with a bound that keeps every message.
func (b *mailbox) Thread(ctx context.Context, thread string) ([]model.Message, error) {
	q := messageQuery(false)
	q.Set("$filter", "receivedDateTime ge 1900-01-01T00:00:00Z and conversationId eq '"+strings.ReplaceAll(thread, "'", "''")+"'")
	q.Set("$orderby", "receivedDateTime desc")
	items, err := b.c.all(ctx, b.c.endpoint("/me/messages", q), false)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, notFound
	}
	out := []model.Message{}
	for _, d := range items {
		m, err := b.message(d, false)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func folderName(folder string) string {
	switch strings.ToLower(folder) {
	case "trash":
		return "deleteditems"
	case "spam":
		return "junkemail"
	}
	return folder
}

func (b *mailbox) ResolveFolder(ctx context.Context, folder string) (string, error) {
	d, err := b.c.get(ctx, b.c.endpoint("/me/mailFolders/"+escapeID(folderName(folder)), url.Values{"$select": {"id"}}), false)
	if err != nil {
		return "", err
	}
	id := text(d, "id")
	if id == "" {
		return "", invalidMetadata
	}
	return id, nil
}

// ThreadMessagesIn compares parentFolderId with the resolved folder ID; a failed lookup fails closed.
func (b *mailbox) ThreadMessagesIn(ctx context.Context, thread, folder string) ([]string, error) {
	id, err := b.ResolveFolder(ctx, folder)
	if err != nil {
		return nil, err
	}
	all, err := b.Thread(ctx, thread)
	if err != nil {
		return nil, err
	}
	out := []string{}
	for _, m := range all {
		if slices.Contains(m.Folders, id) {
			out = append(out, m.ID)
		}
	}
	return out, nil
}

// kql builds the $search value. KQL received takes a day and ignores any time, in the search time zone or UTC
// without one (MS-KQL Date Tokens,
// https://learn.microsoft.com/en-us/openspecs/sharepoint_protocols/ms-kql/21b23dbb-6c49-4d35-86e4-80fb1d66b301),
// so the days reach one UTC day past each bound, which covers any time zone; Search then keeps exact times.
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
		parts = append(parts, "received>="+q.After.UTC().AddDate(0, 0, -1).Format("2006-01-02"))
	}
	if q.Before != nil {
		parts = append(parts, "received<="+q.Before.UTC().AddDate(0, 0, 1).Format("2006-01-02"))
	}
	if q.Text != nil && *q.Text != "" {
		parts = append(parts, term(*q.Text))
	}
	return `"` + strings.Join(parts, " ") + `"`
}
func (b *mailbox) Search(ctx context.Context, query model.SearchQuery, page model.PageRequest, body bool) (model.MessagePage, error) {
	path := "/me/messages"
	if query.Folder != nil {
		path = "/me/mailFolders/" + escapeID(folderName(*query.Folder)) + "/messages"
	}
	q := messageQuery(body)
	q.Set("$search", kql(query))
	found, err := b.page(ctx, path, q, page, body)
	if err != nil {
		return found, err
	}
	// The KQL days are wider than [After, Before), so a page can hold fewer messages than its size; Next still
	// continues the same Graph search.
	found.Messages = slices.DeleteFunc(found.Messages, func(m model.Message) bool { return !query.Received(m.ReceivedAt) })
	return found, nil
}

// maxTop is the largest $top that Graph documents for messages
// (https://learn.microsoft.com/en-us/graph/api/user-list-messages?view=graph-rest-1.0).
const maxTop = 1000

// page reads one page of messages at path with q, plus $top when a size is given. A page with more items than the
// size, or than maxTop without one, is refused. A page is a nextLink that Graph
// gave for the same request, applied as is (https://learn.microsoft.com/en-us/graph/paging); one that pageLink or
// validURL refuses fails before any credential read. The next page is the response's nextLink.
func (b *mailbox) page(ctx context.Context, path string, q url.Values, page model.PageRequest, body bool) (model.MessagePage, error) {
	if page.Size < 0 {
		return model.MessagePage{}, invalidMetadata
	}
	if page.Size > 0 {
		q.Set("$top", strconv.Itoa(page.Size))
	}
	first := b.c.endpoint(path, q)
	link := first
	if page.Page != "" {
		if !pageLink(page.Page, first) {
			return model.MessagePage{}, invalidPage
		}
		link = page.Page
	}
	d, err := b.c.get(ctx, link, false)
	if err != nil {
		return model.MessagePage{}, err
	}
	items, err := pageItems(d)
	if err != nil {
		return model.MessagePage{}, err
	}
	if len(items) > min(cmp.Or(page.Size, maxTop), maxTop) {
		return model.MessagePage{}, invalidPage
	}
	out := []model.Message{}
	for _, d := range items {
		m, err := b.message(d, body)
		if err != nil {
			return model.MessagePage{}, err
		}
		out = append(out, m)
	}
	next, err := b.c.next(d, first)
	if err != nil {
		return model.MessagePage{}, err
	}
	return model.MessagePage{Messages: out, Next: next}, nil
}

func (b *mailbox) Folders(ctx context.Context) ([]model.Folder, error) {
	q := url.Values{"$select": {"id,displayName,childFolderCount"}, "$top": {"100"}}
	pending := []string{b.c.endpoint("/me/mailFolders", q)}
	seen := map[string]bool{}
	out := []model.Folder{}
	for len(pending) > 0 {
		first := pending[0]
		pending = pending[1:]
		items, err := b.c.all(ctx, first, false)
		if err != nil {
			return nil, err
		}
		for _, d := range items {
			id := text(d, "id")
			if id == "" || seen[id] || len(seen) >= 10000 {
				return nil, invalidPage
			}
			seen[id] = true
			out = append(out, model.Folder{ID: id, Name: text(d, "displayName")})
			var children int
			_ = json.Unmarshal(d["childFolderCount"], &children)
			if children > 0 {
				pending = append(pending, b.c.endpoint("/me/mailFolders/"+escapeID(id)+"/childFolders", q))
			}
		}
	}
	return out, nil
}
func (b *mailbox) ClosedFolders(ctx context.Context) (map[string]model.Bin, error) {
	out := map[string]model.Bin{}
	// Hidden descendants are closed too; Graph excludes them unless explicitly requested.
	q := url.Values{"$select": {"id,childFolderCount"}, "$top": {"100"}, "includeHiddenFolders": {"true"}}
	for _, root := range []struct {
		name string
		bin  model.Bin
	}{{"deleteditems", model.Trash}, {"junkemail", model.Spam}} {
		id, err := b.ResolveFolder(ctx, root.name)
		if err != nil {
			return nil, err
		}
		if _, seen := out[id]; seen || len(out) >= 10000 {
			return nil, invalidPage
		}
		out[id] = root.bin
		pending := []string{id}
		for len(pending) > 0 {
			parent := pending[0]
			pending = pending[1:]
			items, err := b.c.all(ctx, b.c.endpoint("/me/mailFolders/"+escapeID(parent)+"/childFolders", q), false)
			if err != nil {
				return nil, err
			}
			for _, d := range items {
				id := text(d, "id")
				if _, seen := out[id]; id == "" || seen || len(out) >= 10000 {
					return nil, invalidPage
				}
				out[id] = root.bin
				var children int
				if json.Unmarshal(d["childFolderCount"], &children) != nil || children < 0 {
					return nil, invalidMetadata
				}
				if children > 0 {
					pending = append(pending, id)
				}
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
