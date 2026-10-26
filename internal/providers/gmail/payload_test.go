package gmail_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
)

func TestFullBodyRequiresUsablePayload(t *testing.T) {
	for _, fixture := range []struct {
		name    string
		payload any
		valid   bool
	}{
		{name: "missing"},
		{name: "null"},
		{name: "empty-object", payload: map[string]any{}},
		{name: "missing-mime-type", payload: map[string]any{"body": map[string]any{"size": 0, "data": ""}}},
		{name: "invalid-mime-type", payload: map[string]any{"mimeType": "not-a-media-type"}},
		{name: "wrong-shape", payload: "malformed"},
		{name: "empty-text", payload: part("text/plain", ""), valid: true},
		{name: "attachment-only", payload: map[string]any{"mimeType": "application/pdf", "filename": "a.pdf", "body": map[string]any{"attachmentId": "attachment", "size": 42}}, valid: true},
		{name: "multipart-without-text", payload: map[string]any{"mimeType": "multipart/mixed", "parts": []any{map[string]any{"mimeType": "image/png", "body": map[string]any{"attachmentId": "image", "size": 42}}}}, valid: true},
	} {
		for _, operation := range []string{"get", "list", "search", "reply"} {
			t.Run(fixture.name+"/"+operation, func(t *testing.T) {
				var writes atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/labels":
						respond(w, map[string]any{"labels": []any{}})
					case "/messages":
						respond(w, map[string]any{"messages": []any{map[string]string{"id": "m"}}})
					case "/messages/m":
						v := message("m", nil)
						v["payload"] = fixture.payload
						if fixture.name == "missing" {
							delete(v, "payload")
						}
						respond(w, v)
					case "/drafts":
						writes.Add(1)
						respond(w, map[string]any{"message": map[string]string{"id": "draft"}})
					default:
						t.Error("unexpected request")
						w.WriteHeader(http.StatusNotFound)
					}
				}))
				defer server.Close()
				m := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL}).Mailbox()
				ctx := context.Background()
				var messages []model.Message
				var draft string
				var err error
				switch operation {
				case "get":
					messages, err = m.Get(ctx, []string{"m"}, true)
				case "list":
					var page model.MessagePage
					page, err = m.List(ctx, "INBOX", model.PageRequest{Size: 1}, false)
					messages = page.Messages
				case "search":
					var page model.MessagePage
					page, err = m.Search(ctx, model.SearchQuery{}, model.PageRequest{Size: 1}, false)
					messages = page.Messages
				case "reply":
					draft, err = m.DraftReply(ctx, "m", "Reply")
				}
				if !fixture.valid {
					if err == nil || len(messages) != 0 || draft != "" || writes.Load() != 0 {
						t.Fatal("unusable payload accepted or draft written")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if operation == "reply" {
					if draft != "draft" || writes.Load() != 1 {
						t.Fatal("empty source reply not created")
					}
				} else if len(messages) != 1 || messages[0].Body == nil || *messages[0].Body != "" {
					t.Fatal("fetched-empty body not preserved")
				}
			})
		}
	}
}
