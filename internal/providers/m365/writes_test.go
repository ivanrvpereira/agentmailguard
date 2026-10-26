package m365_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

func TestDraftsCreateWithoutSendingAndUpdateOnlyNewDraft(t *testing.T) {
	var paths []string
	var bodies []map[string]any
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			q := r.URL.Query()
			if q.Get("$select") != "isDraft,conversationIndex" || q.Get("$expand") != "singleValueExtendedProperties($filter=id eq 'String 0x1042')" {
				t.Error("missing draft safety signals")
			}
			jsonResponse(w, map[string]any{"isDraft": true, "conversationIndex": base64.StdEncoding.EncodeToString(make([]byte, 22))})
			return
		}
		paths = append(paths, r.Method+" "+r.URL.EscapedPath())
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		jsonResponse(w, map[string]string{"id": "new-immutable"})
	})
	b := c.Mailbox()
	ctx := context.Background()
	draft, err := b.DraftNew(ctx, model.NewDraft{To: []string{"a@example.test"}, Subject: "Hi", Body: "text"})
	if draft.ID != "new-immutable" || err != nil {
		t.Fatal(draft, err)
	}
	id, err := b.DraftUpdate(ctx, "draft", "replacement")
	if id != "draft" || err != nil {
		t.Fatal(id, err)
	}
	if !reflect.DeepEqual(paths, []string{"POST /v1.0/me/messages", "PATCH /v1.0/me/messages/draft"}) {
		t.Fatalf("unsafe endpoints %v", paths)
	}
	expected := []map[string]any{{"subject": "Hi", "body": map[string]any{"contentType": "text", "content": "text"}, "toRecipients": []any{map[string]any{"emailAddress": map[string]any{"address": "a@example.test"}}}, "ccRecipients": []any{}, "bccRecipients": []any{}}, {"body": map[string]any{"contentType": "text", "content": "replacement"}}}
	if !reflect.DeepEqual(bodies, expected) {
		t.Fatalf("draft bodies %v", bodies)
	}
}
func TestDraftUpdateRefusesMissingOrReplySignals(t *testing.T) {
	index := base64.StdEncoding.EncodeToString(make([]byte, 22))
	for _, data := range []map[string]any{{}, {"isDraft": false, "conversationIndex": index}, {"isDraft": "true", "conversationIndex": index}, {"isDraft": true}, {"isDraft": true, "conversationIndex": "bad"}, {"isDraft": true, "conversationIndex": index + "\n"}, {"isDraft": true, "conversationIndex": base64.StdEncoding.EncodeToString(make([]byte, 27))}, {"isDraft": true, "conversationIndex": index, "singleValueExtendedProperties": []any{map[string]string{"id": "String 0x1042", "value": "reply"}}}, {"isDraft": true, "conversationIndex": index, "singleValueExtendedProperties": "invalid"}} {
		c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				t.Error("unsafe draft was patched")
			}
			jsonResponse(w, data)
		})
		if _, err := c.Mailbox().DraftUpdate(context.Background(), "m", "body"); err == nil {
			t.Fatalf("unsafe draft accepted %v", data)
		}
	}
}
func TestCancelledMutationDoesNotWaitForAnotherOperation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			close(started)
			<-release
			jsonResponse(w, map[string]any{"categories": []string{"Keep"}})
			return
		}
		w.WriteHeader(204)
	})
	first := make(chan []model.MutationResult, 1)
	go func() {
		r, _ := c.Mailbox().Categorize(context.Background(), []string{"m"}, []string{"Add"}, nil)
		first <- r
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	second := make(chan []model.MutationResult, 1)
	go func() { r, _ := c.Mailbox().MarkRead(ctx, []string{"other"}, true); second <- r }()
	select {
	case r := <-second:
		if len(r) != 1 || r[0].OK {
			t.Error("cancelled mutation not isolated")
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("cancelled mutation waited on operation lock")
	}
	close(release)
	if r := <-first; len(r) != 1 || !r[0].OK {
		t.Fatal("first mutation failed")
	}
}
func TestCategoryUpdateFailsClosedOnMalformedCurrentCategories(t *testing.T) {
	for _, categories := range []any{nil, "bad", []any{"Keep", 17}, []any{"Keep", nil}} {
		c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "GET" {
				t.Error("malformed categories overwritten")
			}
			jsonResponse(w, map[string]any{"categories": categories})
		})
		results, err := c.Mailbox().Categorize(context.Background(), []string{"m"}, []string{"Add"}, nil)
		if err != nil || len(results) != 1 || results[0].OK {
			t.Fatalf("bad metadata accepted %v %v", results, err)
		}
	}
}
func TestMarkReadPatchesExactlyRequestedBoolean(t *testing.T) {
	for _, read := range []bool{true, false} {
		t.Run(map[bool]string{true: "read", false: "unread"}[read], func(t *testing.T) {
			calls := 0
			c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "PATCH" || r.URL.EscapedPath() != "/v1.0/me/messages/message" {
					t.Error("read state must use the message PATCH endpoint")
				}
				var body map[string]any
				if json.NewDecoder(r.Body).Decode(&body) != nil || !reflect.DeepEqual(body, map[string]any{"isRead": read}) {
					t.Errorf("PATCH must contain only isRead=%t", read)
				}
				w.WriteHeader(http.StatusNoContent)
			})
			results, err := c.Mailbox().MarkRead(context.Background(), []string{"message"}, read)
			if err != nil || calls != 1 || len(results) != 1 || results[0].ID != "message" || !results[0].OK || results[0].Error != nil {
				t.Fatalf("read-state mutation failed: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestMutationsAreMovesOrFixedPatchesAndFailuresIsolated(t *testing.T) {
	var mu sync.Mutex
	var writes []map[string]any
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/gone") {
			w.WriteHeader(404)
			_, _ = w.Write([]byte(`{"error":{"code":"SECRET","message":"SECRET"}}`))
			return
		}
		if r.Method == "GET" {
			if r.URL.Query().Get("$select") != "categories" {
				t.Error("category read overfetch")
			}
			jsonResponse(w, map[string]any{"categories": []string{"Work", "Old"}})
			return
		}
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid body")
		}
		mu.Lock()
		writes = append(writes, body)
		mu.Unlock()
		switch r.Method {
		case "POST":
			if !strings.HasSuffix(r.URL.EscapedPath(), "/move") || len(body) != 1 {
				t.Error("not a move")
			}
		case "PATCH":
			if len(body) != 1 {
				t.Error("multiple patch fields")
			}
			for key, v := range body {
				switch key {
				case "isRead", "categories":
				case "flag":
					f := v.(map[string]any)
					if len(f) != 1 || f["flagStatus"] != "flagged" {
						t.Error("flag broadened")
					}
				default:
					t.Errorf("forbidden patch %q", key)
				}
			}
		default:
			t.Errorf("forbidden verb %q", r.Method)
		}
		w.WriteHeader(204)
	})
	b := c.Mailbox()
	ctx := context.Background()
	for _, call := range []func() ([]model.MutationResult, error){func() ([]model.MutationResult, error) { return b.Archive(ctx, []string{"m"}) }, func() ([]model.MutationResult, error) { return b.Trash(ctx, []string{"m"}) }, func() ([]model.MutationResult, error) { return b.Move(ctx, []string{"m"}, "folder") }, func() ([]model.MutationResult, error) { return b.Flag(ctx, []string{"m"}, model.Flagged) }, func() ([]model.MutationResult, error) {
		return b.Categorize(ctx, []string{"m"}, []string{"Later", "Work"}, []string{"Old"})
	}} {
		r, err := call()
		if err != nil || len(r) != 1 || !r[0].OK {
			t.Fatalf("mutation %v %v", r, err)
		}
	}
	results, err := b.MarkRead(ctx, []string{"ok", "gone", "other"}, false)
	if err != nil || len(results) != 3 || !results[0].OK || results[1].OK || results[1].Error == nil || *results[1].Error != "Graph error 404" || !results[2].OK {
		t.Fatalf("partial results %v %v", results, err)
	}
	if writes[0]["destinationId"] != "archive" || writes[1]["destinationId"] != "deleteditems" || writes[2]["destinationId"] != "folder" || !reflect.DeepEqual(writes[4]["categories"], []any{"Work", "Later"}) {
		t.Fatalf("wrong write shapes %v", writes)
	}
	if _, err := b.Flag(ctx, []string{"m"}, model.FlagState("hostile")); err == nil {
		t.Fatal("invalid flag accepted")
	}
}
