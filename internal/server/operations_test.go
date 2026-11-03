package server_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"sync/atomic"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

// The API is closed: a new operation is a reviewed change of this list. No operation manages accounts,
// credentials, the console password, settings, or the cache.
func TestAPIOperationsAreReviewed(t *testing.T) {
	reviewed := []string{
		"account.list", "account.status", "approvals.status",
		"calendar.availability", "calendar.cancel", "calendar.get", "calendar.invite", "calendar.list", "calendar.respond", "calendar.search", "calendar.update",
		"draft.delete", "draft.forward", "draft.new", "draft.reply", "draft.send", "draft.update",
		"mail.archive", "mail.categories", "mail.categorize", "mail.flag", "mail.folders", "mail.forward", "mail.get", "mail.link", "mail.list",
		"mail.move", "mail.organize", "mail.read", "mail.reply", "mail.search", "mail.send", "mail.spam", "mail.thread", "mail.trash", "mail.unread",
		"rules.list", "stats",
	}
	if got := server.Operations(); !slices.Equal(got, reviewed) {
		t.Fatalf("operations=%q\nreviewed=%q", got, reviewed)
	}
}

func TestUnknownOperationsAndMalformedCallsAreRefused(t *testing.T) {
	d := startDaemon(t, daemonSetup{config: fakeScreen})
	h := httptest.NewServer(d.s)
	defer h.Close()
	for _, tc := range []struct {
		body   string
		status int
		want   string
	}{
		{`{"operation":"stats","params":{}}`, 200, `{}`},
		{`{"operation":"account.list","params":{}}`, 200, `[]`},
		{`{"operation":"cache.clear","params":{}}`, 400, `{"error":"invalid request"}`},
		{`{"operation":"stats","params":{"days":0}}`, 400, `{"error":"invalid request"}`},
		{`{"operation":"stats","params":{"Days":1}}`, 400, `{"error":"invalid request"}`},
		{`{"operation":"stats","operation":"account.list","params":{}}`, 400, `{"error":"invalid request"}`},
		{`{"operation":"stats","params":{}} {}`, 400, `{"error":"invalid request"}`},
		{`{"operation":"mail.delete","params":{}}`, 400, `{"error":"invalid request"}`},
		{`{"operation":"stats","params":null}`, 400, `{"error":"invalid request"}`},
		{`{"operation":"account.status","params":{}}`, 500, `{"error":"no accounts; run amgd account add"}`},
	} {
		if status, body := rpc(t, h.URL, tc.body); status != tc.status || body != tc.want {
			t.Errorf("%s: status %d body %s", tc.body, status, body)
		}
	}
}

func TestAccountListShowsAliasesProvidersEmailsAndTheDefault(t *testing.T) {
	d := startDaemon(t, daemonSetup{config: workConfig + gmailTable, record: record(nil)})
	status, body := accountRPC(t, d.s, "account.list", "", `{}`)
	want := `[{"account":"gmail","provider":"gmail","email":"gmail@account.test","default":false},{"account":"other","provider":"m365","email":"other@account.test","default":false},{"account":"work","provider":"m365","email":"work@account.test","default":true}]`
	if status != 200 || body != want {
		t.Fatalf("%d %s", status, body)
	}
	if status, body := accountRPC(t, d.s, "mail.folders", "absent", `{}`); status != 500 || body != `{"error":"unknown account; run amg account list"}` {
		t.Fatalf("unknown alias: %d %s", status, body)
	}
}

// Invalid input and calls that need no mailbox never reach the provider.
func TestValidatedAccountOperationsReachTheProviderOnlyWhenValid(t *testing.T) {
	var calls atomic.Int32
	d := graphDaemon(t, "", m365Grant, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/me/mailFolders/deleteditems":
			io.WriteString(w, `{"id":"deleted"}`)
		case "/me/mailFolders":
			io.WriteString(w, `{"value":[{"id":"f1","displayName":"Correspondência 日本","childFolderCount":0}]}`)
		default:
			t.Errorf("unexpected provider request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}), newNativeAPI())
	for op, params := range map[string]string{"rules.list": `{}`, "mail.organize": `{"since":"2026-09-30T00:00:00Z"}`} {
		if status, b := accountRPC(t, d.s, op, "", params); status != 200 {
			t.Fatalf("%s: %d %s", op, status, b)
		}
	}
	for _, body := range []struct{ op, params string }{
		{"mail.get", `{}`}, {"mail.get", `{"id":""}`},
		{"draft.update", `{"id":"x"}`}, {"draft.update", `{"id":"x","html":true}`}, {"draft.update", `{"id":"x","subject":"s","html":true}`},
		{"draft.update", `{"id":"x","to":null}`}, {"draft.update", `{"id":"x","subject":null}`}, {"draft.update", `{"id":"x","cc":[""]}`}, {"draft.update", `{"id":"x","from":["a@example.test"]}`},
		{"draft.delete", `{}`}, {"draft.delete", `{"id":""}`}, {"draft.delete", `{"ids":["x"]}`}, {"draft.delete", `{"id":"x","body":"b"}`},
		{"mail.archive", `{"ids":[]}`},
		// A conversation action has a thread instead of IDs, never both; in only with a thread; never on mail.flag.
		{"mail.archive", `{"ids":["m1"],"thread":"t1"}`}, {"mail.trash", `{"ids":[],"thread":"t1"}`}, {"mail.read", `{"in":"inbox"}`}, {"mail.unread", `{"ids":["m1"],"in":"inbox"}`},
		{"mail.archive", `{"thread":""}`}, {"mail.archive", `{"thread":"t1","in":""}`}, {"mail.archive", `{"thread":"t1","in":null}`}, {"mail.archive", `{"thread":["t1"]}`},
		{"mail.move", `{"folder":"f1","ids":["m1"],"thread":"t1"}`}, {"mail.move", `{"folder":"f1","in":"inbox"}`}, {"mail.move", `{"thread":"t1"}`},
		{"mail.categorize", `{"add":["Work"],"ids":["m1"],"thread":"t1"}`}, {"mail.categorize", `{"add":["Work"],"in":"inbox"}`},
		{"mail.flag", `{"thread":"t1","state":"flagged"}`}, {"mail.flag", `{"ids":["x"],"state":"flagged","in":"inbox"}`},
		{"mail.list", `{"count":-1}`}, {"mail.list", `{"page":"a b"}`}, {"mail.search", `{"text":"x","page":"a\u0000"}`}, {"calendar.search", `{"text":"x","page":"\u00e9"}`}, {"mail.flag", `{"ids":["x"],"state":"urgent"}`},
		{"mail.organize", `{"since":"0d"}`}, {"mail.organize", `{"since":"2026-10-08"}`},
		{"calendar.list", `{"start":"2026-01-01T00:00:00Z","end":"2026-01-01T00:00:00Z"}`},
	} {
		if status, _ := accountRPC(t, d.s, body.op, "", body.params); status != 400 {
			t.Errorf("invalid request status %d: %s %s", status, body.op, body.params)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("empty rules or invalid input reached the provider")
	}
	if status, b := accountRPC(t, d.s, "mail.folders", "", `{}`); status != 200 || b != `[{"id":"f1","name":"Correspondência 日本"}]` {
		t.Fatalf("folders %d %s", status, b)
	}
}

func TestMoveIntoDeletedItemsIsRefusedWithPublicReason(t *testing.T) {
	var writes atomic.Int32
	d := graphDaemon(t, "", m365Grant, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writes.Add(1)
			w.WriteHeader(500)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/me/mailFolders/deleteditems":
			io.WriteString(w, `{"id":"del"}`)
		case "/me/mailFolders":
			io.WriteString(w, `{"value":[{"id":"f1","displayName":"Finance","childFolderCount":0},{"id":"del","displayName":"Deleted Items","childFolderCount":1}]}`)
		case "/me/mailFolders/del/childFolders":
			io.WriteString(w, `{"value":[{"id":"old","displayName":"Old","childFolderCount":1}]}`)
		case "/me/mailFolders/old/childFolders":
			io.WriteString(w, `{"value":[{"id":"older","displayName":"Older","childFolderCount":0}]}`)
		case "/me/mailFolders/junkemail":
			io.WriteString(w, `{"id":"junk"}`)
		case "/me/mailFolders/junk/childFolders":
			io.WriteString(w, `{"value":[]}`)
		case "/me/mailFolders/del", "/me/mailFolders/older":
			response(w, map[string]any{"id": path.Base(r.URL.Path)})
		case "/me/messages/m1":
			io.WriteString(w, `{"id":"m1","parentFolderId":"inbox"}`)
		default:
			t.Errorf("unexpected provider request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
		}
	}), newNativeAPI())
	for _, dest := range []string{"del", "older", "trash"} {
		status, b := accountRPC(t, d.s, "mail.move", "", `{"ids":["m1"],"folder":"`+dest+`"}`)
		if status != 500 || b != `{"error":"move refused: use mail.trash"}` {
			t.Fatalf("move to %s: %d %s", dest, status, b)
		}
	}
	if writes.Load() != 0 {
		t.Fatal("refused move reached the provider")
	}
	status, b := accountRPC(t, d.s, "mail.folders", "", `{}`)
	if status != 200 || b != `[{"id":"f1","name":"Finance"},{"id":"del","name":"Deleted Items"},{"id":"old","name":"Old"},{"id":"older","name":"Older"}]` {
		t.Fatalf("folders %d %s", status, b)
	}
}
