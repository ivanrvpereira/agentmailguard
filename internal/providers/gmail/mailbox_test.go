package gmail_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
)

func part(kind, text string) map[string]any {
	return map[string]any{"mimeType": kind, "body": map[string]any{"data": base64.RawURLEncoding.EncodeToString([]byte(text)), "size": len(text)}}
}
func message(id string, p map[string]any) map[string]any {
	return map[string]any{"id": id, "threadId": "thread", "internalDate": "1788220800001", "labelIds": []string{"INBOX", "UNREAD", "STARRED", "Label_1"}, "payload": p}
}
func TestMailboxFullMIMEAndLiveLabels(t *testing.T) {
	label := "Work"
	p := map[string]any{"mimeType": "multipart/mixed", "headers": []map[string]string{
		{"name": "From", "value": "=?utf-8?q?Lu=C3=ADs?= <luis@example.test>"},
		{"name": "Subject", "value": "=?iso-8859-1?q?caf=E9?= menu"},
		{"name": "Authentication-Results", "value": "mx.google.com.evil; spf=fail"},
		{"name": "Authentication-Results", "value": "mx.google.com; spf=pass; spf=fail; dkim=pass; compauth=pass"},
		{"name": "Authentication-Results", "value": "mx.google.com; dkim=fail"},
	}, "parts": []any{map[string]any{"mimeType": "message/rfc822", "filename": "forward.eml", "parts": []any{part("text/html", "ATTACHED")}}, part("text/plain", "PLAIN"), part("text/html", "<p>HTML</p>"), map[string]any{"mimeType": "application/pdf", "filename": "a.pdf"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer access" {
			t.Error("token header")
		}
		switch r.URL.Path {
		case "/labels":
			respond(w, map[string]any{"labels": []any{map[string]string{"id": "INBOX", "type": "system", "name": "INBOX"}, map[string]string{"id": "UNREAD", "type": "system"}, map[string]string{"id": "STARRED", "type": "system"}, map[string]string{"id": "Label_1", "type": "user", "name": label}}})
		case "/messages":
			if r.URL.Query().Get("labelIds") != "INBOX" {
				t.Error("system label case")
			}
			respond(w, map[string]any{"messages": []any{map[string]string{"id": "m"}}})
		case "/messages/m":
			if r.URL.Query().Get("format") != "full" {
				t.Error("not full")
			}
			respond(w, message("m", p))
		default:
			t.Error("unexpected path")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL})
	page, err := c.Mailbox().List(context.Background(), "inbox", model.PageRequest{}, false)
	if err != nil {
		t.Fatal(err)
	}
	m := page.Messages[0]
	if *m.Body != "<p>HTML</p>" || *m.ContentType != model.HTML || m.Sender.Name != "Luís" || m.Subject != "café menu" || !reflect.DeepEqual(m.Attachments, []string{"forward.eml", "a.pdf"}) || !m.ReceivedAt.Equal(time.Date(2026, 9, 1, 0, 0, 0, 1000000, time.UTC)) {
		t.Fatalf("message mapping: %+v", m)
	}
	if m.Auth == nil || *m.Auth.SPF != "fail" || *m.Auth.DKIM != "pass" || m.Auth.CompAuth != nil || m.Flag != model.Flagged || m.IsRead {
		t.Fatal("auth/state mapping")
	}
	label = "Renamed"
	found, err := c.Mailbox().Get(context.Background(), []string{"m"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if found[0].Body != nil || found[0].ContentType != nil || found[0].Auth != nil || !reflect.DeepEqual(found[0].Categories, []string{"Renamed"}) {
		t.Fatal("stale labels or body=false ignored")
	}
	folders, err := c.Mailbox().Folders(context.Background())
	if err != nil || len(folders) != 2 {
		t.Fatal("state labels exposed as folders")
	}
}

func TestMissingMIMEBodyFailsClosed(t *testing.T) {
	for _, body := range []any{map[string]any{}, map[string]any{"size": 3}, map[string]any{"size": 3, "data": "a"}} {
		t.Run(fmt.Sprint(body), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/labels" {
					respond(w, map[string]any{})
					return
				}
				respond(w, message("m", map[string]any{"mimeType": "text/plain", "body": body}))
			}))
			defer server.Close()
			c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL})
			if _, err := c.Mailbox().Get(context.Background(), []string{"m"}, true); err == nil {
				t.Fatal("missing or corrupt body accepted")
			}
		})
	}
}

func TestListSearchAndGetCarryTheGmailThreadID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/labels":
			respond(w, map[string]any{"labels": []any{map[string]string{"id": "INBOX", "type": "system", "name": "INBOX"}}})
		case "/messages":
			respond(w, map[string]any{"messages": []any{map[string]string{"id": "a"}, map[string]string{"id": "b"}}})
		case "/messages/a", "/messages/b":
			id := r.URL.Path[len("/messages/"):]
			respond(w, map[string]any{"id": id, "threadId": "thread-" + id, "internalDate": "1788220800001", "labelIds": []string{"INBOX"}, "payload": part("text/plain", "text")})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	b := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL}).Mailbox()
	ctx := t.Context()
	text := "text"
	list, err := b.List(ctx, "inbox", model.PageRequest{}, false)
	if err != nil {
		t.Fatal(err)
	}
	search, err := b.Search(ctx, model.SearchQuery{Text: &text}, model.PageRequest{}, false)
	if err != nil {
		t.Fatal(err)
	}
	get, err := b.Get(ctx, []string{"b"}, false)
	if err != nil {
		t.Fatal(err)
	}
	threads := func(ms []model.Message) []string {
		out := []string{}
		for _, m := range ms {
			out = append(out, m.ThreadID)
		}
		return out
	}
	for name, got := range map[string][]string{"list": threads(list.Messages), "search": threads(search.Messages), "get": threads(get)} {
		want := []string{"thread-a", "thread-b"}
		if name == "get" {
			want = want[1:]
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s threads=%q want %q", name, got, want)
		}
	}
}

func TestSearchByRecipientMatchesToOrCc(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messages" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		queries = append(queries, r.URL.Query().Get("q"))
		respond(w, map[string]any{})
	}))
	defer server.Close()
	b := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL}).Mailbox()
	to, sender := `Ana "Board" <ana@example.test>`, "bob@example.test"
	for _, q := range []model.SearchQuery{{To: &to}, {Sender: &sender, To: &to}} {
		if _, err := b.Search(t.Context(), q, model.PageRequest{}, false); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{`{to:"ana board ana@example.test" cc:"ana board ana@example.test"}`, `from:"bob@example.test" {to:"ana board ana@example.test" cc:"ana board ana@example.test"}`}
	if !reflect.DeepEqual(queries, want) {
		t.Fatalf("q=%q want %q", queries, want)
	}
}

func TestThreadReadsEveryMessageOfThreadsGetNewestFirst(t *testing.T) {
	var requests []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.EscapedPath()+"?"+r.URL.RawQuery)
		mu.Unlock()
		switch r.URL.EscapedPath() {
		case "/labels":
			respond(w, map[string]any{"labels": []any{map[string]string{"id": "INBOX", "type": "system", "name": "INBOX"}}})
		case "/threads/t%2F1%20%3F":
			respond(w, map[string]any{"id": "t/1 ?", "messages": []any{
				map[string]any{"id": "first", "threadId": "t/1 ?", "labelIds": []string{"SENT"}},
				map[string]any{"id": "draft", "threadId": "t/1 ?", "labelIds": []string{"DRAFT"}},
				map[string]any{"id": "third", "threadId": "t/1 ?", "labelIds": []string{"TRASH"}},
			}})
		case "/threads/gone":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"errors":[{"reason":"notFound"}]}}`))
		case "/messages/first", "/messages/draft", "/messages/third":
			id := r.URL.Path[len("/messages/"):]
			// The draft was saved after the last message arrived, so its internalDate is the newest.
			date := map[string]string{"first": "1788220800001", "draft": "1788220800004", "third": "1788220800003"}[id]
			label := map[string]string{"first": "SENT", "draft": "DRAFT", "third": "TRASH"}[id]
			respond(w, map[string]any{"id": id, "threadId": "t/1 ?", "internalDate": date, "labelIds": []string{label}, "payload": part("text/plain", "text")})
		default:
			t.Errorf("unexpected path %s", r.URL.EscapedPath())
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	b := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL}).Mailbox()
	ctx := t.Context()
	ids := func(ms []model.Message) []string {
		out := []string{}
		for _, m := range ms {
			out = append(out, m.ID+"@"+strings.Join(m.Folders, ","))
		}
		return out
	}
	got, err := b.Thread(ctx, "t/1 ?")
	if err != nil || !reflect.DeepEqual(ids(got), []string{"draft@DRAFT", "third@TRASH", "first@SENT"}) || !got[0].IsDraft || got[1].ThreadID != "t/1 ?" {
		t.Fatalf("ids=%v err=%v", ids(got), err)
	}
	if !slices.Contains(requests, "GET /threads/t%2F1%20%3F?format=minimal") {
		t.Fatalf("requests=%q", requests)
	}
	if got, err := b.Thread(ctx, "gone"); err != model.MailboxError("Gmail error 404 notFound") || len(got) != 0 {
		t.Fatalf("unknown thread: ids=%v err=%v", ids(got), err)
	}
}

func TestThreadMessagesInKeepsTheThreadMessagesThatCarryTheFolderLabel(t *testing.T) {
	var requests []string
	var mu sync.Mutex
	// Trash removes INBOX but keeps SENT and user labels; spam keeps user labels too (live check, 2026-10-29).
	labels := map[string][]string{"in-old": {"INBOX"}, "sent": {"SENT"}, "labeled": {"Label_7"}, "draft": {"DRAFT"}, "in-new": {"INBOX", "UNREAD", "Label_7"}, "binned": {"TRASH", "SENT", "Label_7"}, "spam": {"SPAM", "Label_7"}}
	dates := map[string]string{"in-old": "1788220800001", "sent": "1788220800002", "labeled": "1788220800003", "draft": "1788220800004", "in-new": "1788220800005", "binned": "1788220800006", "spam": "1788220800007"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.EscapedPath()+"?"+r.URL.RawQuery)
		mu.Unlock()
		switch r.URL.EscapedPath() {
		case "/labels":
			respond(w, map[string]any{"labels": []any{map[string]string{"id": "INBOX", "type": "system", "name": "INBOX"}, map[string]string{"id": "SENT", "type": "system", "name": "SENT"}, map[string]string{"id": "DRAFT", "type": "system", "name": "DRAFT"}, map[string]string{"id": "TRASH", "type": "system", "name": "TRASH"}, map[string]string{"id": "SPAM", "type": "system", "name": "SPAM"}, map[string]string{"id": "Label_7", "type": "user", "name": "Projects"}}})
		case "/threads/t-7":
			messages := []any{}
			for _, id := range []string{"in-old", "sent", "labeled", "draft", "in-new", "binned", "spam"} {
				messages = append(messages, map[string]any{"id": id, "threadId": "t-7", "labelIds": labels[id]})
			}
			respond(w, map[string]any{"id": "t-7", "messages": messages})
		case "/threads/gone":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"errors":[{"reason":"notFound"}]}}`))
		default:
			id := strings.TrimPrefix(r.URL.Path, "/messages/")
			if _, ok := labels[id]; !ok {
				t.Errorf("unexpected path %s", r.URL.EscapedPath())
				w.WriteHeader(404)
				return
			}
			respond(w, map[string]any{"id": id, "threadId": "t-7", "internalDate": dates[id], "labelIds": labels[id], "payload": part("text/plain", "text")})
		}
	}))
	defer server.Close()
	b := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL}).Mailbox()
	ctx := t.Context()
	// A folder other than the trash or spam skips their messages, as messages.list does by default
	// (includeSpamTrash, https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages/list).
	for folder, want := range map[string][]string{"inbox": {"in-new", "in-old"}, "Label_7": {"in-new", "labeled"}, "sent": {"sent"}, "trash": {"binned"}, "spam": {"spam"}} {
		requests = nil
		got, trash, err := b.ThreadMessagesIn(ctx, "t-7", folder)
		if err != nil || !reflect.DeepEqual(got, want) || trash != (folder == "trash") {
			t.Fatalf("%s: ids=%q trash=%v err=%v", folder, got, trash, err)
		}
		// threads.get, then Get reads the user labels and each message (in parallel), then the folder becomes a label.
		reads := slices.Clone(requests[2:9])
		slices.Sort(reads)
		seen := append(append(slices.Clone(requests[:2]), reads...), requests[9:]...)
		wantRequests := []string{"GET /threads/t-7?format=minimal", "GET /labels?", "GET /messages/binned?format=full", "GET /messages/draft?format=full", "GET /messages/in-new?format=full", "GET /messages/in-old?format=full", "GET /messages/labeled?format=full", "GET /messages/sent?format=full", "GET /messages/spam?format=full", "GET /labels?"}
		if !reflect.DeepEqual(seen, wantRequests) {
			t.Fatalf("%s: requests=%q", folder, requests)
		}
	}
	requests = nil
	if got, _, err := b.ThreadMessagesIn(ctx, "gone", "inbox"); err != model.MailboxError("Gmail error 404 notFound") || len(got) != 0 {
		t.Fatalf("unknown thread: ids=%q err=%v", got, err)
	}
	if !reflect.DeepEqual(requests, []string{"GET /threads/gone?format=minimal"}) {
		t.Fatalf("unknown thread requests=%q", requests)
	}
	if got, _, err := b.ThreadMessagesIn(ctx, "t-7", "Label_404"); err != model.MailboxError("Gmail error FolderError") || len(got) != 0 {
		t.Fatalf("unknown folder: ids=%q err=%v", got, err)
	}
}

// List and Search read one messages.list page each
// (https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages/list): maxResults only when
// a size is given, as given, so Gmail's default (100) and maximum (500) apply; pageToken is the page; nextPageToken
// is the next page.
func TestListAndSearchReadOneGmailPage(t *testing.T) {
	var queries []string
	next := "token-2"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/labels":
			respond(w, map[string]any{"labels": []any{map[string]string{"id": "INBOX", "type": "system", "name": "INBOX"}}})
		case "/messages":
			q := r.URL.Query()
			q.Del("labelIds")
			q.Del("q")
			queries = append(queries, q.Encode())
			v := map[string]any{"messages": []any{map[string]string{"id": "a"}}}
			if next != "" {
				v["nextPageToken"] = next
			}
			respond(w, v)
		case "/messages/a":
			respond(w, map[string]any{"id": "a", "threadId": "t", "internalDate": "1788220800001", "labelIds": []string{"INBOX"}, "payload": part("text/plain", "text")})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	b := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL}).Mailbox()
	text := "text"
	for name, call := range map[string]func(model.PageRequest) (model.MessagePage, error){
		"list": func(p model.PageRequest) (model.MessagePage, error) { return b.List(t.Context(), "inbox", p, false) },
		"search": func(p model.PageRequest) (model.MessagePage, error) {
			return b.Search(t.Context(), model.SearchQuery{Text: &text}, p, false)
		},
	} {
		queries, next = nil, "token-2"
		first, err := call(model.PageRequest{})
		if err != nil || len(first.Messages) != 1 || first.Next != "token-2" {
			t.Fatalf("%s first: %+v %v", name, first, err)
		}
		next = ""
		last, err := call(model.PageRequest{Size: 600, Page: "token-2"})
		if err != nil || len(last.Messages) != 1 || last.Next != "" {
			t.Fatalf("%s last: %+v %v", name, last, err)
		}
		if want := []string{"", "maxResults=600&pageToken=token-2"}; !reflect.DeepEqual(queries, want) {
			t.Fatalf("%s queries=%q want %q", name, queries, want)
		}
		// A next page equal to the page asked for would never end.
		next = "token-2"
		if got, err := call(model.PageRequest{Page: "token-2"}); err == nil || got.Messages != nil {
			t.Fatalf("%s: a repeated page token was accepted", name)
		}
		queries = nil
		if _, err := call(model.PageRequest{Size: -1}); err == nil || len(queries) != 0 {
			t.Fatalf("%s: a negative size was accepted or sent: %q", name, queries)
		}
	}
}

// A messages.list page holds at most maxResults IDs, and at most 500 without one
// (https://developers.google.com/workspace/gmail/api/reference/rest/v1/users.messages/list). A larger page is
// refused before any message is read, so a provider answer cannot make the server read and screen without bound.
func TestListAndSearchRefuseAGmailPageLargerThanAsked(t *testing.T) {
	var listed int
	var gets atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/labels":
			respond(w, map[string]any{"labels": []any{map[string]string{"id": "INBOX", "type": "system", "name": "INBOX"}}})
		case r.URL.Path == "/messages":
			ids := []any{}
			for i := range listed {
				ids = append(ids, map[string]string{"id": fmt.Sprintf("m%d", i)})
			}
			respond(w, map[string]any{"messages": ids})
		default:
			gets.Add(1)
			id := strings.TrimPrefix(r.URL.Path, "/messages/")
			respond(w, map[string]any{"id": id, "threadId": "t", "internalDate": "1788220800001", "labelIds": []string{"INBOX"}, "payload": part("text/plain", "text")})
		}
	}))
	defer server.Close()
	b := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL}).Mailbox()
	text := "text"
	for name, call := range map[string]func(model.PageRequest) (model.MessagePage, error){
		"list": func(p model.PageRequest) (model.MessagePage, error) { return b.List(t.Context(), "inbox", p, false) },
		"search": func(p model.PageRequest) (model.MessagePage, error) {
			return b.Search(t.Context(), model.SearchQuery{Text: &text}, p, false)
		},
	} {
		for _, c := range []struct{ size, listed int }{{2, 3}, {0, 501}, {1000, 501}} {
			listed = c.listed
			if got, err := call(model.PageRequest{Size: c.size}); err != model.MailboxError("Gmail error InvalidPage") || got.Messages != nil || gets.Load() != 0 {
				t.Errorf("%s size %d with %d IDs: %d messages, %v, gets=%d", name, c.size, c.listed, len(got.Messages), err, gets.Load())
			}
		}
		listed = 3
		if got, err := call(model.PageRequest{Size: 3}); err != nil || len(got.Messages) != 3 {
			t.Errorf("%s size 3 with 3 IDs: %d messages, %v", name, len(got.Messages), err)
		}
		gets.Store(0)
	}
}
