package m365_test

import (
	"context"
	"encoding/json"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
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
			if q.Get("$top") != "10" || q.Get("$skip") != "5" || q.Get("$orderby") != "receivedDateTime desc" || strings.Contains(q.Get("$select"), "body") {
				t.Errorf("bad list query %v", q)
			}
			jsonResponse(w, map[string]any{"value": []any{map[string]any{"id": "listed", "body": map[string]string{"content": "unrequested"}}}})
		case "/v1.0/me/messages":
			q := r.URL.Query()
			if q.Get("$search") != `"from:bob subject:\"hello world\" received>=2026-09-21 received<=2026-09-21 back\\slash"` || !strings.Contains(q.Get("$select"), "internetMessageHeaders") {
				t.Errorf("bad search %s", q.Get("$search"))
			}
			jsonResponse(w, map[string]any{"value": []any{map[string]any{"id": "searched"}}})
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
	list, err := b.List(ctx, "inbox", 10, 5, false)
	if err != nil || len(list) != 1 || list[0].Body != nil {
		t.Fatalf("list %v %v", list, err)
	}
	sender, subject, term := "bob", "hello world", `back\slash`
	search, err := b.Search(ctx, model.SearchQuery{Sender: &sender, Subject: &subject, Text: &term, After: &now, Before: &now}, 10, true)
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
