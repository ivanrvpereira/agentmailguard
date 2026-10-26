package m365_test

import (
	"context"
	"encoding/json"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
)

func graphClient(t *testing.T, h http.HandlerFunc) *m365.Client {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+goodToken() {
			t.Error("missing checked credential")
		}
		if !strings.Contains(r.Header.Get("Prefer"), `IdType="ImmutableId"`) {
			t.Error("mutable IDs requested")
		}
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return m365.New(account(), validStore(), m365.Options{HTTPClient: s.Client(), BaseURL: s.URL + "/v1.0", Now: func() time.Time { return now }})
}
func jsonResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func TestReadCollectionsSearchAndCurrentNestedFolders(t *testing.T) {
	round := 0
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("read wrote")
		}
		switch r.URL.Path {
		case "/v1.0/me/mailFolders/deleteditems":
			jsonResponse(w, map[string]any{"id": "deleted"})
		case "/v1.0/me/mailFolders":
			if r.URL.Query().Get("page") == "2" {
				jsonResponse(w, map[string]any{"value": []any{map[string]any{"id": "f2", "displayName": "Sent"}}})
				return
			}
			jsonResponse(w, map[string]any{"value": []any{map[string]any{"id": "f1", "displayName": "Inbox", "childFolderCount": 1}}, "@odata.nextLink": "http://" + r.Host + "/v1.0/me/mailFolders?page=2"})
		case "/v1.0/me/mailFolders/f1/childFolders":
			jsonResponse(w, map[string]any{"value": []any{map[string]any{"id": "nested", "displayName": "Child"}}})
		case "/v1.0/me/outlook/masterCategories":
			round++
			name := "Old"
			if round == 2 {
				name = "Current"
			}
			jsonResponse(w, map[string]any{"value": []any{map[string]any{"displayName": name}}})
		case "/v1.0/me/mailFolders/inbox/messages":
			q := r.URL.Query()
			if q.Get("$top") != "10" || q.Has("$skip") || q.Get("$orderby") != "receivedDateTime desc" || strings.Contains(q.Get("$select"), "body") {
				t.Errorf("bad list query %v", q)
			}
			jsonResponse(w, map[string]any{"value": []any{map[string]any{"id": "listed", "body": map[string]string{"content": "unrequested"}}}})
		case "/v1.0/me/messages":
			q := r.URL.Query()
			if q.Get("$search") != `"from:bob subject:\"hello world\" received>=2026-09-20 received<=2026-09-22 back\\slash"` || !strings.Contains(q.Get("$select"), "internetMessageHeaders") {
				t.Errorf("bad search %s", q.Get("$search"))
			}
			jsonResponse(w, map[string]any{"value": []any{map[string]any{"id": "searched", "receivedDateTime": "2026-09-21T00:00:00Z"}}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	})
	b := c.Mailbox()
	ctx := context.Background()
	folders, err := b.Folders(ctx)
	if err != nil || !reflect.DeepEqual(folders, []model.Folder{{ID: "f1", Name: "Inbox"}, {ID: "f2", Name: "Sent"}, {ID: "nested", Name: "Child"}}) {
		t.Fatalf("folders %v %v", folders, err)
	}
	a, _ := b.Categories(ctx)
	z, _ := b.Categories(ctx)
	if !reflect.DeepEqual(a, []string{"Old"}) || !reflect.DeepEqual(z, []string{"Current"}) {
		t.Fatal("stale categories")
	}
	page, err := b.List(ctx, "inbox", model.PageRequest{Size: 10}, false)
	list := page.Messages
	if err != nil || len(list) != 1 || list[0].Body != nil {
		t.Fatalf("list %v %v", list, err)
	}
	sender, subject, term, later := "bob", "hello world", `back\slash`, now.Add(time.Hour)
	page, err = b.Search(ctx, model.SearchQuery{Sender: &sender, Subject: &subject, Text: &term, After: &now, Before: &later}, model.PageRequest{Size: 10}, true)
	search := page.Messages
	if err != nil || len(search) != 1 || search[0].ID != "searched" || search[0].Body != nil {
		t.Fatalf("search %v %v", search, err)
	}
}
func TestFoldersMarkDeletedItemsAndEveryFolderBelowIt(t *testing.T) {
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/me/mailFolders/deleteditems":
			jsonResponse(w, map[string]any{"id": "del"})
		case "/v1.0/me/mailFolders":
			jsonResponse(w, map[string]any{"value": []any{
				map[string]any{"id": "inbox", "displayName": "Inbox", "childFolderCount": 1},
				map[string]any{"id": "del", "displayName": "Deleted Items", "childFolderCount": 1},
				map[string]any{"id": "arch", "displayName": "Archive"},
			}})
		case "/v1.0/me/mailFolders/inbox/childFolders":
			jsonResponse(w, map[string]any{"value": []any{map[string]any{"id": "receipts", "displayName": "Receipts"}}})
		case "/v1.0/me/mailFolders/del/childFolders":
			jsonResponse(w, map[string]any{"value": []any{map[string]any{"id": "old", "displayName": "Old", "childFolderCount": 1}}})
		case "/v1.0/me/mailFolders/old/childFolders":
			jsonResponse(w, map[string]any{"value": []any{map[string]any{"id": "older", "displayName": "Older"}}})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	})
	folders, err := c.Mailbox().Folders(context.Background())
	want := []model.Folder{
		{ID: "inbox", Name: "Inbox"},
		{ID: "del", Name: "Deleted Items", Trash: true},
		{ID: "arch", Name: "Archive"},
		{ID: "receipts", Name: "Receipts"},
		{ID: "old", Name: "Old", Trash: true},
		{ID: "older", Name: "Older", Trash: true},
	}
	if err != nil || !reflect.DeepEqual(folders, want) {
		t.Fatalf("folders = %+v, %v; want %+v", folders, err, want)
	}
}
func TestFoldersFailWhenDeletedItemsLookupFails(t *testing.T) {
	for name, lookup := range map[string]http.HandlerFunc{
		"graph error": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) },
		"missing id":  func(w http.ResponseWriter, _ *http.Request) { jsonResponse(w, map[string]any{}) },
	} {
		t.Run(name, func(t *testing.T) {
			c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1.0/me/mailFolders/deleteditems":
					lookup(w, r)
				case "/v1.0/me/mailFolders":
					jsonResponse(w, map[string]any{"value": []any{map[string]any{"id": "del", "displayName": "Deleted Items"}}})
				default:
					w.WriteHeader(404)
				}
			})
			folders, err := c.Mailbox().Folders(context.Background())
			if err == nil || folders != nil {
				t.Fatalf("folders = %+v, %v; want failure", folders, err)
			}
		})
	}
}
func TestMalformedBodyIsNeverPresentedAsFetchedEmpty(t *testing.T) {
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		jsonResponse(w, map[string]any{"id": "m", "body": map[string]any{"content": 17, "contentType": "text"}})
	})
	messages, err := c.Mailbox().Get(context.Background(), []string{"m"}, true)
	if err == nil && messages[0].Body != nil {
		t.Fatal("malformed body became fetched empty")
	}
}
func TestGetPreservesBodyAbsenceAndEscapesIDs(t *testing.T) {
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.EscapedPath() != "/v1.0/me/messages/a%2Fb%3Fc%23d%20%25" {
			t.Errorf("unsafe request %s %s", r.Method, r.URL.EscapedPath())
		}
		jsonResponse(w, map[string]any{"id": "immutable", "sender": map[string]any{"emailAddress": map[string]string{"address": "fallback@example.test"}}, "isDraft": true, "body": map[string]string{"content": "", "contentType": "text"}, "internetMessageHeaders": []map[string]string{{"name": "Authentication-Results-Original", "value": "spf=pass"}, {"name": "Authentication-Results", "value": "spf=pass; spf=fail; dkim=none; compauth=fail reason=001"}}})
	})
	meta, err := c.Mailbox().Get(context.Background(), []string{"a/b?c#d %"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(meta) != 1 || meta[0].Body != nil || meta[0].Auth != nil || meta[0].ContentType != nil {
		t.Fatalf("metadata leaked body: %+v", meta)
	}
	full, err := c.Mailbox().Get(context.Background(), []string{"a/b?c#d %"}, true)
	if err != nil {
		t.Fatal(err)
	}
	m := full[0]
	if m.Body == nil || *m.Body != "" || m.ContentType == nil || m.Sender.Address != "fallback@example.test" || m.Auth.SPF == nil || *m.Auth.SPF != "fail" || *m.Auth.CompAuthReason != "001" || !m.ReceivedAt.Equal(time.Unix(0, 0)) {
		t.Fatalf("bad mapping %+v", m)
	}
}
func TestListSearchAndGetSelectAndCarryTheConversationID(t *testing.T) {
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		if !slices.Contains(strings.Split(r.URL.Query().Get("$select"), ","), "conversationId") {
			t.Errorf("%s selects %q", r.URL.Path, r.URL.Query().Get("$select"))
		}
		m := map[string]any{"id": "m", "conversationId": "conversation-" + path.Base(r.URL.Path)}
		if r.URL.Path == "/v1.0/me/messages/m" {
			jsonResponse(w, m)
			return
		}
		jsonResponse(w, map[string]any{"value": []any{m}})
	})
	b := c.Mailbox()
	ctx := t.Context()
	text := "hello"
	page, err := b.List(ctx, "inbox", model.PageRequest{}, false)
	list := page.Messages
	if err != nil || len(list) != 1 || list[0].ThreadID != "conversation-messages" {
		t.Fatalf("list %+v %v", list, err)
	}
	page, err = b.Search(ctx, model.SearchQuery{Text: &text}, model.PageRequest{}, false)
	search := page.Messages
	if err != nil || len(search) != 1 || search[0].ThreadID != "conversation-messages" {
		t.Fatalf("search %+v %v", search, err)
	}
	get, err := b.Get(ctx, []string{"m"}, true)
	if err != nil || len(get) != 1 || get[0].ThreadID != "conversation-m" {
		t.Fatalf("get %+v %v", get, err)
	}
}

func TestSearchByRecipientMatchesToOrCc(t *testing.T) {
	var searches []string
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1.0/me/messages" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		searches = append(searches, r.URL.Query().Get("$search"))
		jsonResponse(w, map[string]any{"value": []any{}})
	})
	b := c.Mailbox()
	to, sender := "Ana Board", "bob@example.test"
	for _, q := range []model.SearchQuery{{To: &to}, {Sender: &sender, To: &to}} {
		if _, err := b.Search(t.Context(), q, model.PageRequest{}, false); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{`"(to:\"Ana Board\" OR cc:\"Ana Board\")"`, `"from:bob@example.test (to:\"Ana Board\" OR cc:\"Ana Board\")"`}
	if !reflect.DeepEqual(searches, want) {
		t.Fatalf("$search=%q want %q", searches, want)
	}
}

// KQL received reads only a day, so Search asks for one more UTC day on each side and keeps the messages received
// in [After, Before). The page keeps Graph's nextLink even when the filter leaves fewer messages than $top. A message
// without receivedDateTime is outside any bound.
func TestSearchWidensReceivedDaysAndKeepsExactTimes(t *testing.T) {
	var searches []string
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		searches = append(searches, r.URL.Query().Get("$search"))
		next := r.URL.Query()
		next.Set("$skiptoken", "opaque-token")
		jsonResponse(w, map[string]any{"value": []any{
			map[string]any{"id": "at-before", "receivedDateTime": "2026-03-09T07:00:00Z"},
			map[string]any{"id": "last-second", "receivedDateTime": "2026-03-09T06:59:59Z"},
			map[string]any{"id": "at-after", "receivedDateTime": "2026-03-07T07:00:00Z"},
			map[string]any{"id": "day-before", "receivedDateTime": "2026-03-07T06:59:59Z"},
			map[string]any{"id": "no-time"},
		}, "@odata.nextLink": "http://" + r.Host + r.URL.Path + "?" + next.Encode()})
	})
	zone := time.FixedZone("UTC-7", -7*60*60)
	after, before := time.Date(2026, 3, 7, 0, 0, 0, 0, zone), time.Date(2026, 3, 9, 0, 0, 0, 0, zone)
	for _, tc := range []struct {
		query model.SearchQuery
		want  []string
	}{
		{model.SearchQuery{After: &after, Before: &before}, []string{"last-second", "at-after"}},
		{model.SearchQuery{Before: &before}, []string{"last-second", "at-after", "day-before"}},
	} {
		page, err := c.Mailbox().Search(t.Context(), tc.query, model.PageRequest{Size: 5}, false)
		got := []string{}
		for _, m := range page.Messages {
			got = append(got, m.ID)
		}
		if err != nil || !reflect.DeepEqual(got, tc.want) || !strings.Contains(page.Next, "opaque-token") {
			t.Fatalf("ids=%v next=%q err=%v", got, page.Next, err)
		}
	}
	if want := []string{`"received>=2026-03-06 received<=2026-03-10"`, `"received<=2026-03-10"`}; !reflect.DeepEqual(searches, want) {
		t.Fatalf("$search=%q want %q", searches, want)
	}
}

func TestThreadReadsEveryPageOfTheConversationFilterNewestFirst(t *testing.T) {
	// requests holds the query of each request; the nextLink page is a token only.
	requests := []url.Values{}
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		requests = append(requests, q)
		if r.Method != "GET" || r.URL.Path != "/v1.0/me/messages" {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		switch {
		case q.Get("page") == "2":
			jsonResponse(w, map[string]any{"value": []any{
				map[string]any{"id": "n1", "conversationId": "c'1 &x", "receivedDateTime": "2026-03-01T09:00:00Z", "parentFolderId": "sent-id"},
			}})
		case strings.Contains(q.Get("$filter"), "gone"):
			jsonResponse(w, map[string]any{"value": []any{}})
		default:
			jsonResponse(w, map[string]any{"value": []any{
				map[string]any{"id": "n3", "conversationId": "c'1 &x", "receivedDateTime": "2026-03-03T09:00:00Z", "parentFolderId": "drafts-id", "isDraft": true},
				map[string]any{"id": "n2", "conversationId": "c'1 &x", "receivedDateTime": "2026-03-02T09:00:00Z", "parentFolderId": "deleted-id"},
			}, "@odata.nextLink": "http://" + r.Host + "/v1.0/me/messages?page=2"})
		}
	})
	b := c.Mailbox()
	ctx := t.Context()
	ids := func(ms []model.Message) []string {
		out := []string{}
		for _, m := range ms {
			out = append(out, m.ID+"@"+m.Folders[0])
		}
		return out
	}
	got, err := b.Thread(ctx, "c'1 &x")
	if err != nil || !reflect.DeepEqual(ids(got), []string{"n3@drafts-id", "n2@deleted-id", "n1@sent-id"}) || got[0].ThreadID != "c'1 &x" || !got[0].IsDraft {
		t.Fatalf("ids=%v err=%v", ids(got), err)
	}
	if got, err := b.Thread(ctx, "gone"); err != model.MailboxError("Graph error 404") || len(got) != 0 {
		t.Fatalf("unknown thread: ids=%v err=%v", ids(got), err)
	}
	first := func(filter string) url.Values {
		return url.Values{
			"$filter":  {filter},
			"$orderby": {"receivedDateTime desc"},
			"$select":  {"id,from,sender,subject,receivedDateTime,toRecipients,ccRecipients,parentFolderId,conversationId,isDraft,isRead,flag,categories"},
			"$expand":  {"attachments($select=name)"},
		}
	}
	want := []url.Values{
		first("receivedDateTime ge 1900-01-01T00:00:00Z and conversationId eq 'c''1 &x'"),
		{"page": {"2"}},
		first("receivedDateTime ge 1900-01-01T00:00:00Z and conversationId eq 'gone'"),
	}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%v", requests)
	}
}

// messagesPath is the List path of the inbox; inboxQuery is the query that List sends for it without a size.
const messagesPath = "/v1.0/me/mailFolders/inbox/messages"

func inboxQuery() url.Values {
	return url.Values{
		"$select":  {"id,from,sender,subject,receivedDateTime,toRecipients,ccRecipients,parentFolderId,conversationId,isDraft,isRead,flag,categories"},
		"$expand":  {"attachments($select=name)"},
		"$orderby": {"receivedDateTime desc"},
	}
}

// List and Search read one page each. Without a size they send no $top, so Graph's default (10) applies. The next
// page is the whole @odata.nextLink, which Graph says to apply as is
// (https://learn.microsoft.com/en-us/graph/api/user-list-messages?view=graph-rest-1.0).
func TestListAndSearchReadOneGraphPageAndReturnTheNextLink(t *testing.T) {
	var requests []string
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.Path+"?"+r.URL.RawQuery)
		next := r.URL.Query()
		if next.Has("$skip") || next.Has("$skiptoken") {
			jsonResponse(w, map[string]any{"value": []any{map[string]any{"id": "second"}}})
			return
		}
		if next.Has("$search") {
			next.Set("$skiptoken", "opaque-token")
		} else {
			next.Set("$top", "10")
			next.Set("$skip", "10")
		}
		jsonResponse(w, map[string]any{"value": []any{map[string]any{"id": "first"}}, "@odata.nextLink": "http://" + r.Host + r.URL.Path + "?" + next.Encode()})
	})
	b := c.Mailbox()
	first, err := b.List(t.Context(), "inbox", model.PageRequest{}, false)
	if err != nil || len(first.Messages) != 1 || first.Messages[0].ID != "first" || !strings.HasSuffix(first.Next, "%24skip=10&%24top=10") {
		t.Fatalf("first page %+v %v", first, err)
	}
	last, err := b.List(t.Context(), "inbox", model.PageRequest{Page: first.Next}, false)
	if err != nil || len(last.Messages) != 1 || last.Messages[0].ID != "second" || last.Next != "" {
		t.Fatalf("last page %+v %v", last, err)
	}
	text := "hello"
	found, err := b.Search(t.Context(), model.SearchQuery{Text: &text}, model.PageRequest{Size: 5}, false)
	if err != nil || !strings.HasSuffix(found.Next, "%24skiptoken=opaque-token&%24top=5") {
		t.Fatalf("search page %+v %v", found, err)
	}
	if _, err := b.Search(t.Context(), model.SearchQuery{Text: &text}, model.PageRequest{Size: 5, Page: found.Next}, false); err != nil {
		t.Fatal(err)
	}
	listed := inboxQuery()
	searched := url.Values{"$select": listed["$select"], "$expand": listed["$expand"], "$search": {`"hello"`}, "$top": {"5"}}
	want := []string{messagesPath + "?" + listed.Encode(), messagesPath + "?" + strings.SplitN(first.Next, "?", 2)[1],
		"/v1.0/me/messages?" + searched.Encode(), "/v1.0/me/messages?" + strings.SplitN(found.Next, "?", 2)[1]}
	if !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%q\nwant %q", requests, want)
	}
}

// The page is agent input. Only a link that Graph can give for the same options is followed: the Graph origin, the
// same path, every query value that the options set, and otherwise only $top, $skip, and $skiptoken. Anything else
// is refused before a credential read or a request.
func TestListAndSearchRefuseAPageLinkThatDoesNotMatchTheRequest(t *testing.T) {
	var calls atomic.Int64
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		jsonResponse(w, map[string]any{"value": []any{}})
	}))
	t.Cleanup(s.Close)
	store := validStore()
	b := m365.New(account(), store, m365.Options{HTTPClient: s.Client(), BaseURL: s.URL + "/v1.0", Now: func() time.Time { return now }}).Mailbox()
	link := func(base, path string, change func(url.Values)) string {
		q := inboxQuery()
		q.Set("$top", "10")
		q.Set("$skip", "10")
		change(q)
		return base + path + "?" + q.Encode()
	}
	same := func(url.Values) {}
	good := link(s.URL, messagesPath, same)
	text := "hello"
	search := func(q url.Values) {
		q.Del("$orderby")
		q.Del("$skip")
		q.Set("$search", `"hello"`)
		q.Set("$skiptoken", "opaque")
	}
	for name, c := range map[string]struct {
		page   string
		size   int
		search bool
	}{
		"other host":        {page: link("http://evil.invalid", messagesPath, same)},
		"other scheme":      {page: link(strings.Replace(s.URL, "http:", "https:", 1), messagesPath, same)},
		"user info":         {page: link(strings.Replace(s.URL, "http://", "http://user@", 1), messagesPath, same)},
		"fragment":          {page: good + "#x"},
		"other folder":      {page: link(s.URL, "/v1.0/me/mailFolders/archive/messages", same)},
		"other resource":    {page: link(s.URL, "/v1.0/me/messages", same)},
		"no select":         {page: link(s.URL, messagesPath, func(q url.Values) { q.Del("$select") })},
		"more fields":       {page: link(s.URL, messagesPath, func(q url.Values) { q.Set("$select", "body") })},
		"other order":       {page: link(s.URL, messagesPath, func(q url.Values) { q.Set("$orderby", "subject") })},
		"filter":            {page: link(s.URL, messagesPath, func(q url.Values) { q.Set("$filter", "isRead eq false") })},
		"key without $":     {page: link(s.URL, messagesPath, func(q url.Values) { q.Set("select", "body") })},
		"two skips":         {page: link(s.URL, messagesPath, func(q url.Values) { q.Add("$skip", "20") })},
		"top other than 5":  {page: good, size: 5},
		"no top with size":  {page: link(s.URL, messagesPath, func(q url.Values) { q.Del("$top") }), size: 5},
		"not a link":        {page: "10"},
		"bad escape":        {page: s.URL + messagesPath + "?%zz"},
		"other search":      {page: link(s.URL, "/v1.0/me/messages", func(q url.Values) { search(q); q.Set("$search", `"other"`) }), search: true},
		"search other path": {page: link(s.URL, messagesPath, search), search: true},
	} {
		var err error
		if c.search {
			_, err = b.Search(t.Context(), model.SearchQuery{Text: &text}, model.PageRequest{Size: c.size, Page: c.page}, false)
		} else {
			_, err = b.List(t.Context(), "inbox", model.PageRequest{Size: c.size, Page: c.page}, false)
		}
		if err != model.MailboxError("Graph error InvalidPage") || calls.Load() != 0 || store.loads != 0 {
			t.Errorf("%s: err=%v calls=%d loads=%d", name, err, calls.Load(), store.loads)
		}
	}
	for _, c := range []struct {
		page   string
		size   int
		search bool
	}{{page: good}, {page: good, size: 10}, {page: link(s.URL, "/v1.0/me/messages", search), search: true}} {
		var err error
		if c.search {
			_, err = b.Search(t.Context(), model.SearchQuery{Text: &text}, model.PageRequest{Size: c.size, Page: c.page}, false)
		} else {
			_, err = b.List(t.Context(), "inbox", model.PageRequest{Size: c.size, Page: c.page}, false)
		}
		if err != nil {
			t.Errorf("%s: %v", c.page, err)
		}
	}
	if calls.Load() != 3 {
		t.Fatalf("calls=%d", calls.Load())
	}
}

// A Graph message page holds at most $top items, and at most 1,000 without it
// (https://learn.microsoft.com/en-us/graph/api/user-list-messages?view=graph-rest-1.0, $top 1 to 1,000). A larger
// page is refused, so a provider answer cannot make the server screen without bound.
func TestListAndSearchRefuseAGraphPageLargerThanAsked(t *testing.T) {
	var items int
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		value := []any{}
		for i := range items {
			value = append(value, map[string]any{"id": "m" + strconv.Itoa(i)})
		}
		jsonResponse(w, map[string]any{"value": value})
	})
	b := c.Mailbox()
	text := "hello"
	for name, call := range map[string]func(model.PageRequest) (model.MessagePage, error){
		"list": func(p model.PageRequest) (model.MessagePage, error) { return b.List(t.Context(), "inbox", p, false) },
		"search": func(p model.PageRequest) (model.MessagePage, error) {
			return b.Search(t.Context(), model.SearchQuery{Text: &text}, p, false)
		},
	} {
		for _, c := range []struct{ size, items int }{{2, 3}, {0, 1001}, {5000, 1001}} {
			items = c.items
			if got, err := call(model.PageRequest{Size: c.size}); err != model.MailboxError("Graph error InvalidPage") || got.Messages != nil {
				t.Errorf("%s size %d with %d items: %d messages, %v", name, c.size, c.items, len(got.Messages), err)
			}
		}
		for _, c := range []struct{ size, items int }{{3, 3}, {0, 1000}} {
			items = c.items
			if got, err := call(model.PageRequest{Size: c.size}); err != nil || len(got.Messages) != c.items {
				t.Errorf("%s size %d with %d items: %d messages, %v", name, c.size, c.items, len(got.Messages), err)
			}
		}
	}
}

// Live Graph writes the folder of a nextLink in OData key form, mailFolders('inbox'), though the request used
// mailFolders/inbox. Both name the same folder, so the page is followed; the key form of another folder is not.
func TestListFollowsANextLinkInODataKeyForm(t *testing.T) {
	var requests []string
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.EscapedPath())
		next := r.URL.Query()
		if next.Has("$skip") {
			jsonResponse(w, map[string]any{"value": []any{map[string]any{"id": "second"}}})
			return
		}
		next.Set("$skip", "1")
		jsonResponse(w, map[string]any{"value": []any{map[string]any{"id": "first"}}, "@odata.nextLink": "http://" + r.Host + "/v1.0/me/mailFolders('inbox')/messages?" + next.Encode()})
	})
	b := c.Mailbox()
	first, err := b.List(t.Context(), "inbox", model.PageRequest{Size: 1}, false)
	if err != nil || len(first.Messages) != 1 || first.Next == "" {
		t.Fatalf("first page %+v %v", first, err)
	}
	last, err := b.List(t.Context(), "inbox", model.PageRequest{Size: 1, Page: first.Next}, false)
	if err != nil || len(last.Messages) != 1 || last.Messages[0].ID != "second" {
		t.Fatalf("last page %+v %v", last, err)
	}
	if want := []string{messagesPath, "/v1.0/me/mailFolders('inbox')/messages"}; !reflect.DeepEqual(requests, want) {
		t.Fatalf("requests=%q want %q", requests, want)
	}
	for _, other := range []string{"/v1.0/me/mailFolders('sentitems')/messages", "/v1.0/me/mailFolders('inbox')/messages/x", "/v1.0/me/mailFolders('inbox'/messages"} {
		page := strings.Replace(first.Next, "/v1.0/me/mailFolders('inbox')/messages", other, 1)
		if _, err := b.List(t.Context(), "inbox", model.PageRequest{Size: 1, Page: page}, false); err == nil {
			t.Fatalf("followed %s", other)
		}
	}
	if len(requests) != 2 {
		t.Fatalf("a refused page reached Graph: %q", requests)
	}
}
