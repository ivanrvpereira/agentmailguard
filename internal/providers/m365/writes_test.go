package m365_test

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
)

func TestDraftNewCreatesWithoutSending(t *testing.T) {
	var paths []string
	var bodies []map[string]any
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.EscapedPath())
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		jsonResponse(w, map[string]string{"id": "new-immutable"})
	})
	draft, err := c.Mailbox().DraftNew(context.Background(), model.NewDraft{To: []string{"a@example.test"}, Subject: "Hi", Body: "text"})
	if draft.ID != "new-immutable" || err != nil {
		t.Fatal(draft, err)
	}
	if !reflect.DeepEqual(paths, []string{"POST /v1.0/me/messages"}) {
		t.Fatalf("unsafe endpoints %v", paths)
	}
	expected := []map[string]any{{"subject": "Hi", "body": map[string]any{"contentType": "text", "content": "text"}, "toRecipients": []any{map[string]any{"emailAddress": map[string]any{"address": "a@example.test"}}}, "ccRecipients": []any{}, "bccRecipients": []any{}}}
	if !reflect.DeepEqual(bodies, expected) {
		t.Fatalf("draft bodies %v", bodies)
	}
}

// draftUpdateGraph serves message m with the JSON fields of read to every GET, and answers a PATCH with the updated
// draft in conversation conv. It records each request and each PATCH body.
func draftUpdateGraph(t *testing.T, read map[string]any) (*[]string, *[]map[string]any, *m365.Client) {
	t.Helper()
	var paths []string
	var patches []map[string]any
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.EscapedPath()+" "+r.URL.Query().Get("$select"))
		if r.Method == "GET" {
			jsonResponse(w, read)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		patches = append(patches, body)
		jsonResponse(w, map[string]string{"id": "m", "conversationId": "conv"})
	})
	return &paths, &patches, c
}

// A draft update checks isDraft and then sends one PATCH of only the fields that it is given: a list replaces the
// recipients, an empty list clears them, and the body has contentType text or html. The draft keeps its ID, and the
// conversation comes from Graph's answer.
func TestDraftUpdatePatchesOnlyTheGivenFields(t *testing.T) {
	recipient := func(a string) map[string]any { return map[string]any{"emailAddress": map[string]any{"address": a}} }
	for _, tc := range []struct {
		name   string
		change model.DraftChange
		patch  map[string]any
	}{
		{"subject", model.DraftChange{Subject: new("New")}, map[string]any{"subject": "New"}},
		{"lists", model.DraftChange{To: new([]string{"a@example.test", "b@example.test"}), CC: new([]string{}), BCC: new([]string{"c@example.test"})},
			map[string]any{"toRecipients": []any{recipient("a@example.test"), recipient("b@example.test")}, "ccRecipients": []any{}, "bccRecipients": []any{recipient("c@example.test")}}},
		{"plain body", model.DraftChange{Body: new("line one\nline two")}, map[string]any{"body": map[string]any{"contentType": "text", "content": "line one\nline two"}}},
		{"html body", model.DraftChange{Body: new("<p>Hi</p>"), HTML: true}, map[string]any{"body": map[string]any{"contentType": "html", "content": "<p>Hi</p>"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			paths, patches, c := draftUpdateGraph(t, map[string]any{"id": "m", "isDraft": true})
			id, thread, err := c.Mailbox().DraftUpdate(context.Background(), "m", tc.change)
			if err != nil || id != "m" || thread != "conv" {
				t.Fatalf("update=%q %q %v", id, thread, err)
			}
			if want := []string{"GET /v1.0/me/messages/m isDraft", "PATCH /v1.0/me/messages/m "}; !reflect.DeepEqual(*paths, want) {
				t.Fatalf("requests=%q", *paths)
			}
			if !reflect.DeepEqual(*patches, []map[string]any{tc.patch}) {
				t.Fatalf("patch=%v\nwant %v", *patches, tc.patch)
			}
		})
	}
}

// A message that is not a draft, or whose isDraft is missing or not a boolean, is refused before any PATCH.
func TestDraftUpdateRefusesAMessageThatIsNotADraftBeforeAnyPatch(t *testing.T) {
	for _, read := range []map[string]any{{"id": "m", "isDraft": false}, {"id": "m"}, {"id": "m", "isDraft": "true"}, {"id": "m", "isDraft": nil}, {"isDraft": true}} {
		paths, _, c := draftUpdateGraph(t, read)
		if _, _, err := c.Mailbox().DraftUpdate(context.Background(), "m", model.DraftChange{Subject: new("New")}); err == nil || len(*paths) != 1 {
			t.Fatalf("read %v: err=%v requests=%q", read, err, *paths)
		}
	}
}

// ReadDraft reads isDraft, the recipients, the subject, and the body with its type. A message that is not a draft
// gives only IsDraft false; a draft body of another type fails.
func TestReadDraftMapsTheDraftFields(t *testing.T) {
	recipients := func(a ...string) []any {
		out := []any{}
		for _, v := range a {
			out = append(out, map[string]any{"emailAddress": map[string]any{"name": "Name", "address": v}})
		}
		return out
	}
	draft := map[string]any{"id": "m", "isDraft": true, "subject": "Plan", "toRecipients": recipients("a@example.test", "b@example.test"), "ccRecipients": recipients(), "bccRecipients": recipients("c@example.test"), "body": map[string]any{"contentType": "HTML", "content": "<p>Hi</p>"}}
	paths, _, c := draftUpdateGraph(t, draft)
	got, err := c.Mailbox().ReadDraft(context.Background(), "m")
	want := model.DraftContent{IsDraft: true, To: []string{"a@example.test", "b@example.test"}, CC: []string{}, BCC: []string{"c@example.test"}, Subject: "Plan", Body: "<p>Hi</p>", HTML: true}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("draft=%+v %v", got, err)
	}
	if want := []string{"GET /v1.0/me/messages/m isDraft,toRecipients,ccRecipients,bccRecipients,subject,body"}; !reflect.DeepEqual(*paths, want) {
		t.Fatalf("requests=%q", *paths)
	}
	received := maps.Clone(draft)
	received["isDraft"] = false
	_, _, c = draftUpdateGraph(t, received)
	if got, err := c.Mailbox().ReadDraft(context.Background(), "m"); err != nil || !reflect.DeepEqual(got, model.DraftContent{}) {
		t.Fatalf("received message=%+v %v", got, err)
	}
	draft["body"] = map[string]any{"contentType": "rtf", "content": "x"}
	_, _, c = draftUpdateGraph(t, draft)
	if _, err := c.Mailbox().ReadDraft(context.Background(), "m"); err == nil {
		t.Fatal("unknown body type accepted")
	}
	draft["body"] = map[string]any{"contentType": "text", "content": "x"}
	// A recipient list that is not an array, or a recipient without an address, fails.
	for _, bad := range []any{"a@example.test", nil, map[string]any{}, []any{map[string]any{"emailAddress": map[string]any{"name": "No address"}}}, []any{map[string]any{}}, []any{"a@example.test"}} {
		for _, key := range []string{"toRecipients", "ccRecipients", "bccRecipients"} {
			broken := maps.Clone(draft)
			broken[key] = bad
			_, _, c = draftUpdateGraph(t, broken)
			if got, err := c.Mailbox().ReadDraft(context.Background(), "m"); err == nil {
				t.Errorf("%s=%v accepted: %+v", key, bad, got)
			}
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
