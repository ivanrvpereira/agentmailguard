package gmail_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/gmail"
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
	found, err := c.Mailbox().List(context.Background(), "inbox", 10, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	m := found[0]
	if *m.Body != "<p>HTML</p>" || *m.ContentType != model.HTML || m.Sender.Name != "Luís" || m.Subject != "café menu" || !reflect.DeepEqual(m.Attachments, []string{"forward.eml", "a.pdf"}) || !m.ReceivedAt.Equal(time.Date(2026, 9, 1, 0, 0, 0, 1000000, time.UTC)) {
		t.Fatalf("message mapping: %+v", m)
	}
	if m.Auth == nil || *m.Auth.SPF != "fail" || *m.Auth.DKIM != "pass" || m.Auth.CompAuth != nil || m.Flag != model.Flagged || m.IsRead {
		t.Fatal("auth/state mapping")
	}
	label = "Renamed"
	found, err = c.Mailbox().Get(context.Background(), []string{"m"}, false)
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
