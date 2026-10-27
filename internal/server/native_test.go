package server_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

type externalWrite struct {
	path, method string
	body         map[string]any
}
type nativeAPI struct {
	mu                  sync.Mutex
	bodyReads, jevCalls map[string]int
	categories          map[string][]string
	writes              []externalWrite
	// actionScore answers each Jev action check; a value outside 0..1 makes the check fail.
	actionScore float64
	// actionChecks records the request body of each Jev action check.
	actionChecks []map[string]any
}

func newNativeAPI() *nativeAPI {
	return &nativeAPI{bodyReads: map[string]int{}, jevCalls: map[string]int{}, categories: map[string][]string{}, actionScore: .1}
}

// checks returns the recorded Jev action check requests.
func (a *nativeAPI) checks() []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]map[string]any{}, a.actionChecks...)
}

func messageJSON(id string, body bool, cats []string) map[string]any {
	if cats == nil {
		cats = []string{}
	}
	m := map[string]any{"id": id, "subject": "private-subject-" + id, "from": map[string]any{"emailAddress": map[string]string{"address": "sender@example.test", "name": "private-name"}}, "receivedDateTime": "2026-03-30T10:00:00Z", "isDraft": false, "isRead": false, "categories": cats, "parentFolderId": "inbox", "attachments": []any{}}
	if body {
		m["body"] = map[string]string{"contentType": "text", "content": "private-body-" + id + " https://example.test/report"}
		m["internetMessageHeaders"] = []any{map[string]string{"name": "Authentication-Results", "value": "mx; spf=pass dkim=pass dmarc=pass compauth=pass"}}
	}
	return m
}
func (a *nativeAPI) graph(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if r.Method != "GET" {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		a.writes = append(a.writes, externalWrite{r.URL.Path, r.Method, b})
		if cats, ok := b["categories"].([]any); ok {
			id := strings.TrimPrefix(r.URL.Path, "/me/messages/")
			a.categories[id] = nil
			for _, v := range cats {
				a.categories[id] = append(a.categories[id], v.(string))
			}
		}
		if strings.Contains(r.URL.Path, "/gone/") {
			w.WriteHeader(404)
			io.WriteString(w, `{"error":{"message":"private-provider-canary"}}`)
			return
		}
		switch {
		case r.Method == "PATCH":
			// Graph answers a PATCH of a message with the updated message.
			response(w, map[string]any{"id": strings.TrimPrefix(r.URL.Path, "/me/messages/")})
		case strings.HasSuffix(r.URL.Path, "/createReply") || strings.HasSuffix(r.URL.Path, "/createReplyAll"):
			response(w, map[string]any{"id": "draft-1", "body": map[string]string{"contentType": "html", "content": "<html><body><div>quoted</div></body></html>"}})
		default:
			response(w, map[string]any{"id": "draft-1"})
		}
		return
	}
	switch r.URL.Path {
	case "/me":
		response(w, map[string]string{"userPrincipalName": workEmail})
	case "/me/mailFolders/deleteditems":
		response(w, map[string]any{"id": "deleted"})
	case "/me/mailFolders":
		response(w, map[string]any{"value": []any{map[string]any{"id": "f1", "displayName": "Folder", "childFolderCount": 0}}})
	case "/me/outlook/masterCategories":
		v := []any{}
		for _, c := range []string{"Work", "Red", "Yellow", "Sealed"} {
			v = append(v, map[string]string{"displayName": c})
		}
		response(w, map[string]any{"value": v})
	case "/me/mailFolders/inbox/messages", "/me/messages":
		v := []any{}
		for _, id := range []string{"g", "r", "y", "s"} {
			v = append(v, messageJSON(id, false, a.categories[id]))
		}
		response(w, map[string]any{"value": v})
	case "/me/calendarView", "/me/events/e":
		e := map[string]any{"id": "e", "subject": "private-subject-event", "start": map[string]string{"dateTime": "2026-03-31T10:00:00", "timeZone": "UTC"}, "end": map[string]string{"dateTime": "2026-03-31T11:00:00", "timeZone": "UTC"}, "isOrganizer": false, "body": map[string]string{"contentType": "text", "content": "private-body-event"}, "categories": []string{}}
		if r.URL.Path == "/me/calendarView" {
			response(w, map[string]any{"value": []any{e}})
		} else {
			response(w, e)
		}
	default:
		if id, ok := strings.CutPrefix(r.URL.Path, "/me/messages/"); ok {
			if id == "gone" {
				w.WriteHeader(404)
				return
			}
			body := strings.Contains(r.URL.Query().Get("$select"), ",body")
			if body {
				a.bodyReads[id]++
			}
			m := messageJSON(id, body, a.categories[id])
			// The drafts that the workflow makes are draft-1; Graph gives a draft's recipient lists.
			if strings.HasPrefix(id, "draft-") {
				m["isDraft"], m["toRecipients"], m["ccRecipients"], m["bccRecipients"] = true, []any{}, []any{}, []any{}
			}
			response(w, m)
			return
		}
		w.WriteHeader(404)
	}
}
func (a *nativeAPI) jev(w http.ResponseWriter, r *http.Request) {
	var req struct {
		State     map[string]any
		Questions map[string]json.RawMessage
	}
	data, err := io.ReadAll(r.Body)
	if err != nil || json.Unmarshal(data, &req) != nil {
		w.WriteHeader(400)
		return
	}
	if _, ok := req.Questions["suspicious"]; ok {
		var body map[string]any
		_ = json.Unmarshal(data, &body)
		a.mu.Lock()
		a.actionChecks = append(a.actionChecks, body)
		score := a.actionScore
		a.mu.Unlock()
		response(w, map[string]any{"model": "jev", "usage": map[string]any{}, "answers": map[string]any{"suspicious": map[string]any{"type": "noul", "noul": score}}})
		return
	}
	subject, _ := req.State["subject"].(string)
	id := strings.TrimPrefix(subject, "private-subject-")
	a.mu.Lock()
	a.jevCalls[id]++
	if _, ok := req.Questions["injection"]; ok {
		a.jevCalls["security:"+id]++
	}
	n := a.jevCalls[id]
	a.mu.Unlock()
	// The screening of retry fails once, that of down every time.
	if id == "retry" && n == 1 || id == "down" {
		w.WriteHeader(503)
		io.WriteString(w, "private-jev-canary")
		return
	}
	score, security := .1, .1
	switch id {
	case "r", "retry":
		score = .9
	case "y":
		score = .35
	case "s":
		security = .9
	}
	answers := map[string]any{}
	for name := range req.Questions {
		switch name {
		case "injection":
			answers[name] = map[string]any{"type": "noul", "noul": score}
		case "account_security":
			answers[name] = map[string]any{"type": "noul", "noul": security}
		case "folder":
			answers[name] = map[string]any{"type": "choice", "choice": "archive", "confidence": .9, "probabilities": map[string]float64{"archive": .9, "stay": .1}}
		case "category:work":
			answers[name] = map[string]any{"type": "noul", "noul": .9}
		}
	}
	response(w, map[string]any{"model": "jev", "usage": map[string]any{}, "answers": answers})
}
func (a *nativeAPI) gmail(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/gmail/profile":
		response(w, map[string]string{"emailAddress": gmailEmail})
	case "/gmail/labels":
		response(w, map[string]any{"labels": []any{map[string]string{"id": "INBOX", "name": "INBOX", "type": "system"}, map[string]string{"id": "Label_1", "name": "Work", "type": "user"}}})
	case "/gmail/messages/g":
		response(w, map[string]any{"id": "g", "internalDate": "1774864800000", "labelIds": []string{"INBOX"}, "payload": map[string]any{"mimeType": "text/plain", "headers": []any{map[string]string{"name": "Subject", "value": "private-subject-gmail"}, map[string]string{"name": "From", "value": "Synthetic <sender@example.test>"}}, "body": map[string]any{"data": base64.RawURLEncoding.EncodeToString([]byte("private-body-gmail"))}}})
	case "/gmail/messages":
		response(w, map[string]any{"messages": []any{map[string]string{"id": "g"}}})
	case "/calendar/events":
		response(w, map[string]any{"items": []any{}})
	default:
		w.WriteHeader(404)
	}
}

// buildNative builds the agent client amg.
func buildNative(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "amg")
	build := exec.Command("go", "build", "-o", binary, "./cmd/amg")
	build.Dir = root
	if b, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build: %v %s", e, b)
	}
	return binary
}

// nativeFakes are the logins, extra configuration, external services, and client settings behind startNative.
// jev is the fake Jev engine.
type nativeFakes struct {
	logins        map[string]credentials.OAuth
	config        string
	jev           *nativeAPI
	graph, google http.HandlerFunc
	now           time.Time
	clientEnv     []string
}

// startNative serves a daemon for the work, other, and gmail accounts against fakes, with config appended. It returns
// the daemon and a runner for the compiled client. The client home must stay empty until the test ends.
func startNative(t *testing.T, binary string, f nativeFakes) (*daemon, func(args ...string) (string, string, int)) {
	t.Helper()
	graph := httptest.NewServer(f.graph)
	t.Cleanup(graph.Close)
	google := httptest.NewServer(f.google)
	t.Cleanup(google.Close)
	d := startDaemon(t, daemonSetup{config: workConfig + gmailTable + f.config, record: record(f.logins), jev: f.jev,
		options: server.Options{M365: m365.Options{BaseURL: graph.URL}, Gmail: googleOptions(google.URL), Now: func() time.Time { return f.now }}})
	h := httptest.NewServer(d.s)
	t.Cleanup(h.Close)
	// Child clients receive only a remote URL and an empty temporary home. They cannot see the daemon's configuration or credentials.
	clientHome := t.TempDir()
	t.Cleanup(func() {
		if entries, err := os.ReadDir(clientHome); err != nil || len(entries) != 0 {
			t.Error("remote client created local state")
		}
	})
	env := append([]string{"HOME=" + clientHome}, f.clientEnv...)
	run := func(args ...string) (string, string, int) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, append([]string{"-H", h.URL}, args...)...)
		cmd.Env = env
		var out, errout bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errout
		code := 0
		if e := cmd.Run(); e != nil {
			code = -1
			if ex, ok := e.(*exec.ExitError); ok {
				code = ex.ExitCode()
			}
		}
		return out.String(), errout.String(), code
	}
	return d, run
}

func TestNativeBinaryLongLivedWorkflow(t *testing.T) {
	binary := buildNative(t)
	api := newNativeAPI()
	logins := map[string]credentials.OAuth{"m365/" + workEmail: m365Login(m365Grant), "m365/" + otherEmail: m365Login(m365Grant), "gmail/" + gmailEmail: gmailLogin(gmailReadGrant)}
	d, run := startNative(t, binary, nativeFakes{logins: logins, jev: api, graph: api.graph, google: api.gmail, now: time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)})
	if e := os.WriteFile(filepath.Join(d.home, "rules.toml"), []byte("[accounts.\""+workEmail+"\".archive]\nfolder='archive'\ninstructions='private-rule-canary'\n[accounts.\""+workEmail+"\".work]\ncategory='Work'\ninstructions='private-rule-canary'\n"), 0600); e != nil {
		t.Fatal(e)
	}
	d.reload(t)
	ok := func(args ...string) string {
		t.Helper()
		out, errout, code := run(args...)
		if code != 0 || errout != "" {
			t.Fatalf("command %v code %d stderr %s", args, code, errout)
		}
		return out
	}
	hidden := ok("mail", "get", "r")
	if strings.Contains(hidden, "private-") || !strings.Contains(hidden, `"verdict": "red"`) {
		t.Fatal("cold hidden view leak")
	}
	hidden = ok("mail", "get", "r", "--pretty")
	if strings.Contains(hidden, "private-") || !strings.Contains(hidden, "Verdict:     red") {
		t.Fatal("pretty hidden view leak")
	}
	verifyNativeOutputFailure(t, binary, d.s)
	refused, refusal, exit := run("mail", "link", "r", "--all")
	if exit != 1 || refused != "" || refusal != "error: link refused: message is red, not green\n" {
		t.Fatal("guarded link refusal text lost")
	}
	retry := ok("mail", "get", "retry")
	if strings.Contains(retry, "private-") || !strings.Contains(retry, `"verdict": "error"`) {
		t.Fatal("error must fail closed")
	}
	retry = ok("mail", "get", "retry")
	if !strings.Contains(retry, `"verdict": "red"`) {
		t.Fatal("error not retried")
	}
	for range 2 {
		green := ok("mail", "get", "g")
		if !strings.Contains(green, "private-body-g") {
			t.Fatal("green body missing")
		}
	}
	api.mu.Lock()
	if api.bodyReads["r"] != 1 || api.bodyReads["retry"] != 2 || api.jevCalls["g"] != 1 {
		t.Error("cold/cached/error behavior differs")
	}
	api.mu.Unlock()
	ok("mail", "list")
	ok("mail", "search", "--text", "synthetic")
	links := ok("mail", "link", "g", "L1", "L0", "unknown")
	var linkResults []map[string]string
	if json.Unmarshal([]byte(links), &linkResults) != nil || !reflect.DeepEqual(linkResults, []map[string]string{
		{"link": "L1", "url": "https://example.test/report"},
		{"link": "L0", "error": "no such link in this message"},
		{"link": "unknown", "error": "no such link in this message"},
	}) {
		t.Fatal("guarded links lost valid URLs or per-link errors")
	}
	ok("mail", "folders")
	ok("mail", "categories")
	preview := ok("mail", "organize", "--since", "2026-03-24")
	if !strings.Contains(preview, `"rule": "archive"`) {
		t.Fatal("classification preview missing")
	}
	api.mu.Lock()
	if len(api.writes) != 0 {
		t.Error("preview wrote mailbox")
	}
	api.mu.Unlock()
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			out, errout, code := run("mail", "organize", "--since", "2026-03-24", "--apply")
			if code != 0 || errout != "" || !json.Valid([]byte(out)) {
				t.Error("concurrent apply failed")
			}
		})
	}
	wg.Wait()
	api.mu.Lock()
	if len(api.writes) != 5 {
		t.Errorf("concurrent organize repeated/lost writes: %d", len(api.writes))
	}
	if !reflect.DeepEqual(api.categories["g"], []string{"Work"}) || !reflect.DeepEqual(api.categories["r"], []string{"Red"}) || !reflect.DeepEqual(api.categories["y"], []string{"Yellow"}) || !reflect.DeepEqual(api.categories["s"], []string{"Sealed"}) {
		t.Error("business/security tags differ")
	}
	api.mu.Unlock()
	again := ok("mail", "organize", "--since", "2026-03-24", "--apply")
	if !strings.Contains(again, `"planned": []`) {
		t.Fatal("completion not reused")
	}
	api.mu.Lock()
	if len(api.writes) != 5 {
		t.Error("completed apply wrote again")
	}
	api.mu.Unlock()
	assertPatches := func(args []string, want map[string]map[string]any) {
		t.Helper()
		api.mu.Lock()
		before := len(api.writes)
		api.mu.Unlock()
		ok(args...)
		api.mu.Lock()
		writes := append([]externalWrite{}, api.writes[before:]...)
		api.mu.Unlock()
		got := map[string]map[string]any{}
		for _, w := range writes {
			if w.method != "PATCH" || got[w.path] != nil {
				t.Fatalf("command %v produced a non-PATCH or repeated source", args)
			}
			got[w.path] = w.body
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("command %v: exact source IDs and PATCH bodies differ: got %#v want %#v", args, got, want)
		}
	}
	// Bind each command to its own writes, including every requested source ID.
	assertPatches([]string{"mail", "read", "r", "y"}, map[string]map[string]any{
		"/me/messages/r": {"isRead": true}, "/me/messages/y": {"isRead": true},
	})
	assertPatches([]string{"mail", "unread", "y", "r"}, map[string]map[string]any{
		"/me/messages/y": {"isRead": false}, "/me/messages/r": {"isRead": false},
	})
	for _, state := range []string{"flagged", "complete", "none"} {
		ok("mail", "flag", "r", "--state", state)
	}
	ok("mail", "categorize", "r", "--add", "Work", "--remove", "Red")
	ok("mail", "move", "f1", "r")
	ok("mail", "trash", "r")
	partial := ok("mail", "archive", "r", "gone")
	if !strings.Contains(partial, `"ok": false`) {
		t.Fatal("partial result missing")
	}
	ok("draft", "new", "--to", "first@example.test", "--to", "second@example.test", "--subject", "private-draft-subject", "--body", "private-draft-body")
	ok("draft", "reply", "y", "--body", "private-reply-body")
	// The Jev action check reads the forwarded text, so the source must be green; a yellow source would wait for approval.
	ok("draft", "forward", "g", "--to", "first@example.test", "--to", "second@example.test")
	assertPatches([]string{"draft", "update", "draft-1", "--body", "private-replacement-body"}, map[string]map[string]any{
		"/me/messages/draft-1": {"body": map[string]any{"contentType": "text", "content": "private-replacement-body"}},
	})
	assertPatches([]string{"draft", "update", "draft-1", "--body", ""}, map[string]map[string]any{
		"/me/messages/draft-1": {"body": map[string]any{"contentType": "text", "content": ""}},
	})
	api.mu.Lock()
	writes := append([]externalWrite{}, api.writes...)
	api.mu.Unlock()
	foundNew, replied, foundReply, foundForward := false, false, false, false
	flags := map[string]bool{}
	for _, w := range writes {
		if f, ok := w.body["flag"].(map[string]any); ok {
			flags[f["flagStatus"].(string)] = true
		}
		if w.path == "/me/messages" && w.method == "POST" {
			foundNew = w.body["subject"] == "private-draft-subject" && w.body["body"].(map[string]any)["content"] == "private-draft-body" && reflect.DeepEqual(w.body["toRecipients"], []any{map[string]any{"emailAddress": map[string]any{"address": "first@example.test"}}, map[string]any{"emailAddress": map[string]any{"address": "second@example.test"}}})
		}
		// The reply draft is createReply without a body, then a PATCH of the agent's text into Graph's quote.
		if strings.HasSuffix(w.path, "/createReply") {
			replied = w.path == "/me/messages/y/createReply" && len(w.body) == 0
		}
		if body, _ := w.body["body"].(map[string]any); replied && w.method == "PATCH" && w.path == "/me/messages/draft-1" && body["content"] == "<html><body>private-reply-body<div>quoted</div></body></html>" {
			foundReply = true
		}
		if strings.HasSuffix(w.path, "/createForward") {
			foundForward = w.path == "/me/messages/g/createForward" && reflect.DeepEqual(w.body["message"].(map[string]any)["toRecipients"], []any{map[string]any{"emailAddress": map[string]any{"address": "first@example.test"}}, map[string]any{"emailAddress": map[string]any{"address": "second@example.test"}}})
		}
	}
	if !foundNew || !foundReply || !foundForward || len(flags) != 3 {
		t.Fatal("exact mutation/draft payloads differ")
	}
	ok("calendar", "list", "--from", "2026-03-31", "--to", "+1d")
	ok("calendar", "get", "e")
	ok("account", "status")
	rules := ok("rules", "list")
	if strings.Contains(rules, "private-rule") {
		t.Fatal("rules instructions exposed")
	}
	ok("--account", "other", "mail", "get", "r")
	api.mu.Lock()
	if api.jevCalls["security:r"] != 2 {
		t.Error("accounts reused each other's screening")
	}
	api.mu.Unlock()
	gmailView := ok("--account", "gmail", "mail", "get", "g")
	if !strings.Contains(gmailView, "private-body-gmail") {
		t.Fatal("Gmail route not used")
	}
	ok("--account", "gmail", "account", "status")
	ok("--account", "gmail", "mail", "folders")
	ok("--account", "gmail", "calendar", "list", "--from", "2026-03-31", "--to", "+1d")
	stats := ok("stats")
	for _, account := range []string{workEmail, otherEmail, gmailEmail} {
		if !strings.Contains(stats, `"`+account+`":`) {
			t.Errorf("stats missing %s", account)
		}
	}
	out, errout, code := run("mail", "get", "gone")
	if code != 1 || out != "" || strings.Contains(errout, "private-") {
		t.Fatal("whole-call error leakage or exit mismatch")
	}
	log, e := os.ReadFile(filepath.Join(d.home, "audit.jsonl"))
	if e != nil {
		t.Fatal(e)
	}
	for _, canary := range []string{"private-body", "private-name", "private-subject", "private-draft", "private-reply", "private-replacement", "private-rule", "synthetic-screening-key", "private-gmail-canary", "private-refresh-canary", "private-config", "sender@example.test"} {
		if bytes.Contains(log, []byte(canary)) {
			t.Fatalf("audit contains %s", canary)
		}
	}
}

func verifyNativeOutputFailure(t *testing.T, binary string, handler http.Handler) {
	t.Helper()
	for _, format := range []string{"json", "pretty"} {
		t.Run("output_failure_"+format, func(t *testing.T) {
			var calls atomic.Int32
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				handler.ServeHTTP(w, r)
			}))
			defer h.Close()
			home := t.TempDir()
			path := filepath.Join(home, "stdout")
			if e := os.WriteFile(path, nil, 0600); e != nil {
				t.Fatal(e)
			}
			out, e := os.Open(path) // A real stdout descriptor that rejects writes.
			if e != nil {
				t.Fatal(e)
			}
			defer out.Close()
			args := []string{"-H", h.URL, "mail", "get", "r"}
			if format == "pretty" {
				args = append(args, "--pretty")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, args...)
			cmd.Env = []string{"HOME=" + home}
			cmd.Stdout = out
			var errout bytes.Buffer
			cmd.Stderr = &errout
			e = cmd.Run()
			if exit, ok := e.(*exec.ExitError); !ok || exit.ExitCode() != 1 || errout.String() != "error: output failed\n" || calls.Load() != 1 {
				t.Fatalf("error=%v stderr=%q HTTP attempts=%d", e, errout.String(), calls.Load())
			}
		})
	}
}
