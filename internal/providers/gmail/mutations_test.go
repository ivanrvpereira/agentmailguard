package gmail_test

import (
	"context"
	"encoding/json"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestMailboxMutationsAreLabelOnlyAndPerID(t *testing.T) {
	tests := []struct {
		name        string
		call        func(*gmail.Client) ([]model.MutationResult, error)
		add, remove []string
		trash       bool
	}{
		{"archive", func(c *gmail.Client) ([]model.MutationResult, error) {
			return c.Mailbox().Archive(context.Background(), []string{"ok", "gone"})
		}, []string{}, []string{"INBOX"}, false},
		{"read", func(c *gmail.Client) ([]model.MutationResult, error) {
			return c.Mailbox().MarkRead(context.Background(), []string{"ok", "gone"}, true)
		}, []string{}, []string{"UNREAD"}, false},
		{"unread", func(c *gmail.Client) ([]model.MutationResult, error) {
			return c.Mailbox().MarkRead(context.Background(), []string{"ok", "gone"}, false)
		}, []string{"UNREAD"}, []string{}, false},
		{"flagged", func(c *gmail.Client) ([]model.MutationResult, error) {
			return c.Mailbox().Flag(context.Background(), []string{"ok", "gone"}, model.Flagged)
		}, []string{"STARRED"}, []string{}, false},
		{"none", func(c *gmail.Client) ([]model.MutationResult, error) {
			return c.Mailbox().Flag(context.Background(), []string{"ok", "gone"}, model.NoFlag)
		}, []string{}, []string{"STARRED"}, false},
		{"move", func(c *gmail.Client) ([]model.MutationResult, error) {
			return c.Mailbox().Move(context.Background(), []string{"ok", "gone"}, "Label_1")
		}, []string{"Label_1"}, []string{"INBOX"}, false},
		{"inbox", func(c *gmail.Client) ([]model.MutationResult, error) {
			return c.Mailbox().Move(context.Background(), []string{"ok", "gone"}, "INBOX")
		}, []string{"INBOX"}, []string{}, false},
		{"categories", func(c *gmail.Client) ([]model.MutationResult, error) {
			return c.Mailbox().Categorize(context.Background(), []string{"ok", "gone"}, []string{"Work"}, []string{"Work"})
		}, []string{"Label_1"}, []string{}, false},
		{"trash", func(c *gmail.Client) ([]model.MutationResult, error) {
			return c.Mailbox().Trash(context.Background(), []string{"ok", "gone"})
		}, nil, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/labels" {
					respond(w, map[string]any{"labels": []any{map[string]string{"id": "Label_1", "name": "Work", "type": "user"}}})
					return
				}
				if r.Method != "POST" || strings.Contains(r.URL.Path, "send") {
					t.Error("unsafe write")
				}
				if tt.trash {
					if !strings.HasSuffix(r.URL.Path, "/trash") {
						t.Error("not trash")
					}
				} else {
					if !strings.HasSuffix(r.URL.Path, "/modify") {
						t.Error("not modify")
					}
					var body map[string][]string
					if json.NewDecoder(r.Body).Decode(&body) != nil || len(body) != 2 || !reflect.DeepEqual(body["addLabelIds"], tt.add) || !reflect.DeepEqual(body["removeLabelIds"], tt.remove) {
						t.Errorf("label mutation: %#v", body)
					}
				}
				if strings.Contains(r.URL.Path, "gone") {
					w.WriteHeader(404)
					respond(w, map[string]any{"error": map[string]any{"message": "SECRET", "errors": []any{map[string]string{"reason": "notFound"}}}})
					return
				}
				respond(w, map[string]string{"id": "ok"})
			}))
			defer server.Close()
			c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL})
			got, err := tt.call(c)
			if err != nil || len(got) != 2 || !got[0].OK || got[1].OK || got[1].Error == nil || *got[1].Error != "Gmail error 404 notFound" {
				t.Fatalf("results: %+v %v", got, err)
			}
		})
	}
}
func TestRefusedMutationsDoNotWrite(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("refused mutation wrote")
		}
		respond(w, map[string]any{"labels": []any{map[string]string{"id": "TRASH", "type": "system", "name": "TRASH"}}})
	}))
	defer server.Close()
	c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL})
	if _, err := c.Mailbox().Move(context.Background(), []string{"m"}, "TRASH"); err == nil {
		t.Fatal("system move accepted")
	}
	if _, err := c.Mailbox().Flag(context.Background(), []string{"m"}, model.Complete); err == nil {
		t.Fatal("complete accepted")
	}
	if _, err := c.Mailbox().Categorize(context.Background(), []string{"m"}, []string{"unknown"}, nil); err == nil {
		t.Fatal("unknown category accepted")
	}
}
