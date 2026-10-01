package gmail_test

import (
	"context"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDraftUpdateResolvesPagesAndRefusesDestructiveReplacement(t *testing.T) {
	for _, mode := range []string{"text", "reply", "empty-references", "named-container", "attached-container", "inline-binary", "rfc822", "missing"} {
		t.Run(mode, func(t *testing.T) {
			writes := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "PUT" {
					writes++
					if r.URL.Path != "/drafts/draft%20id" && r.URL.Path != "/drafts/draft id" {
						t.Error("draft ID path")
					}
					msg, _ := draftMIME(t, r)
					if msg.Header.Get("To") != "a@example.test" || msg.Header.Get("Cc") != "c@example.test" || msg.Header.Get("Bcc") != "b@example.test" || msg.Header.Get("Subject") != "Plan" || msg.Header.Get("From") != "" || draftText(t, msg) != "Replacement\n" {
						t.Error("kept headers/body")
					}
					respond(w, map[string]any{"message": map[string]string{"id": "changed"}})
					return
				}
				if r.URL.Path == "/drafts" {
					if r.URL.Query().Get("pageToken") == "" {
						respond(w, map[string]any{"drafts": []any{}, "nextPageToken": "next"})
						return
					}
					id := "m"
					if mode == "missing" {
						id = "other"
					}
					respond(w, map[string]any{"drafts": []any{map[string]any{"id": "draft id", "message": map[string]string{"id": id}}}})
					return
				}
				p := part("text/plain", "Old")
				headers := []map[string]string{{"name": "To", "value": "a@example.test"}, {"name": "Cc", "value": "c@example.test"}, {"name": "Bcc", "value": "b@example.test"}, {"name": "Subject", "value": "Plan"}}
				switch mode {
				case "reply":
					headers = append(headers, map[string]string{"name": "In-Reply-To", "value": "<m@x>"})
				case "empty-references":
					headers = append(headers, map[string]string{"name": "REFERENCES", "value": ""})
				case "named-container":
					p = map[string]any{"mimeType": "multipart/mixed", "filename": "empty.mime"}
				case "attached-container":
					p = map[string]any{"mimeType": "multipart/mixed"}
					headers = append(headers, map[string]string{"name": "Content-Disposition", "value": " Attachment"})
				case "inline-binary":
					p = part("image/png", "PNG")
				case "rfc822":
					p = map[string]any{"mimeType": "message/rfc822", "parts": []any{p}}
				}
				p["headers"] = headers
				v := message("m", p)
				v["labelIds"] = []string{"DRAFT"}
				respond(w, v)
			}))
			defer server.Close()
			c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL})
			id, err := c.Mailbox().DraftUpdate(context.Background(), "m", "Replacement")
			if mode == "text" {
				if err != nil || id != "changed" || writes != 1 {
					t.Fatal(id, err, writes)
				}
			} else if err == nil || writes != 0 {
				t.Fatal("unsafe update accepted")
			}
		})
	}
}
