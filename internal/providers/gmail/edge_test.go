package gmail_test

import (
	"context"
	"encoding/base64"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMIMECharsetsAreMIMECharsetsNotHTMLAliases(t *testing.T) {
	cases := []struct {
		charset string
		raw     []byte
		want    string
	}{
		{"iso-8859-1", []byte{0x80, 0xe9}, "\u0080é"},
		{"windows-1252", []byte{0x80, 0x91, 0x92}, "€‘’"},
		{"us-ascii", []byte{'a', 0xff}, "a\ufffd"},
		{"utf-8", []byte{'a', 0xff}, "a\ufffd"},
		{"utf-8", []byte{0xff, 0xff, 0xe2, 0x82}, "\ufffd\ufffd\ufffd"},
		{"unknown-codec", []byte("naïve"), "naïve"},
	}
	for _, tt := range cases {
		t.Run(tt.charset, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/labels" {
					respond(w, map[string]any{})
					return
				}
				p := part("text/plain", "")
				p["body"] = map[string]any{"data": base64.RawURLEncoding.EncodeToString(tt.raw)}
				p["headers"] = []map[string]string{{"name": "Content-Type", "value": "text/plain; charset=" + tt.charset}}
				respond(w, message("m", p))
			}))
			defer server.Close()
			c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL})
			got, err := c.Mailbox().Get(context.Background(), []string{"m"}, true)
			if err != nil {
				t.Fatal(err)
			}
			if *got[0].Body != tt.want {
				t.Fatalf("charset %s got %q want %q", tt.charset, *got[0].Body, tt.want)
			}
		})
	}
}
func TestSearchSanitizesStructuredValues(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/labels" {
			respond(w, map[string]any{"labels": []any{map[string]string{"id": "INBOX", "type": "system"}}})
			return
		}
		if r.URL.Path == "/messages" {
			q := r.URL.Query()
			if q.Get("labelIds") != "INBOX" {
				t.Error("label missing")
			}
			if q.Get("q") != `from:"a or from b" subject:"café über 日本" after:1788245999 before:1790751601 "r&d q3 draft final"` {
				t.Error("search injection", q.Get("q"))
			}
			respond(w, map[string]any{"messages": []any{map[string]string{"id": "m1"}}})
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/messages/")
		v := message(id, part("text/plain", "BODY"))
		v["internalDate"] = "1788300000000"
		respond(w, v)
	}))
	defer server.Close()
	c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL})
	text, sender, subject, folder := `R&D: Q3 (draft) "final"`, `a" OR from:b`, "Café ÜBER 日本", "inbox"
	pdt := time.FixedZone("UTC-7", -7*60*60)
	after := time.Date(2026, 9, 1, 0, 0, 0, 0, pdt)
	before := time.Date(2026, 9, 30, 0, 0, 0, 0, pdt)
	found, err := c.Mailbox().Search(context.Background(), model.SearchQuery{Text: &text, Sender: &sender, Subject: &subject, Folder: &folder, After: &after, Before: &before}, model.PageRequest{}, false)
	if err != nil || len(found.Messages) != 1 || found.Messages[0].Body == nil {
		t.Fatal("full search", err)
	}
}
func TestDraftReplyPrefersPlainAndOwnSentTo(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			v := message("m", map[string]any{"mimeType": "multipart/alternative", "headers": []map[string]string{{"name": "To", "value": "own-recipient@example.test"}, {"name": "Reply-To", "value": "wrong@example.test"}, {"name": "Subject", "value": "RE: Subject"}}, "parts": []any{part("text/html", "<p>HTML</p>"), part("text/plain", "PLAIN")}})
			v["labelIds"] = []string{"SENT"}
			respond(w, v)
			return
		}
		msg, _ := draftMIME(t, r)
		if msg.Header.Get("To") != "own-recipient@example.test" || msg.Header.Get("Subject") != "RE: Subject" || msg.Header.Get("References") != "" || !strings.HasSuffix(draftText(t, msg), "> PLAIN\n") {
			t.Error("own sent reply")
		}
		respond(w, map[string]any{"message": map[string]string{"id": "created"}})
	}))
	defer server.Close()
	c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL})
	if _, err := c.Mailbox().DraftReply(context.Background(), "m", "Thanks"); err != nil {
		t.Fatal(err)
	}
}

// Gmail takes after: and before: in seconds and does not document whether the boundary second matches, so Search
// asks for one more second on each side and keeps the messages whose internalDate is in [After, Before). A message
// without internalDate is outside any bound; the page keeps nextPageToken.
func TestSearchWidensBySecondsAndKeepsExactReceivedTimes(t *testing.T) {
	dates := map[string]string{"before-after": "1788220800499", "at-after": "1788220800500", "last": "1788307200499", "at-before": "1788307200500", "no-time": ""}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/labels":
			respond(w, map[string]any{"labels": []any{map[string]string{"id": "INBOX", "type": "system"}}})
		case "/messages":
			if q := r.URL.Query().Get("q"); q != "after:1788220799 before:1788307202" {
				t.Errorf("q=%s", q)
			}
			ids := []any{}
			for _, id := range []string{"at-before", "last", "no-time", "at-after", "before-after"} {
				ids = append(ids, map[string]string{"id": id})
			}
			respond(w, map[string]any{"messages": ids, "nextPageToken": "next"})
		default:
			id := strings.TrimPrefix(r.URL.Path, "/messages/")
			v := message(id, part("text/plain", "BODY"))
			v["internalDate"] = dates[id]
			respond(w, v)
		}
	}))
	defer server.Close()
	c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL})
	after := time.Date(2026, 9, 1, 0, 0, 0, 500000000, time.UTC)
	before := after.Add(24 * time.Hour)
	found, err := c.Mailbox().Search(context.Background(), model.SearchQuery{After: &after, Before: &before}, model.PageRequest{}, false)
	got := []string{}
	for _, m := range found.Messages {
		got = append(got, m.ID)
	}
	if err != nil || !reflect.DeepEqual(got, []string{"last", "at-after"}) || found.Next != "next" {
		t.Fatalf("ids=%v next=%q err=%v", got, found.Next, err)
	}
}
