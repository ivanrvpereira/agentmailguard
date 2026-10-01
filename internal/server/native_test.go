package server_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	"syscall"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/screening"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

type nativeStore struct{ store }

func (s *nativeStore) LoadOAuth(ctx context.Context, p, a string) (credentials.OAuth, error) {
	if p == "gmail" {
		s.loads.Add(1)
		return credentials.OAuth{AccessToken: "synthetic-gmail", ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), Scopes: []string{"https://www.googleapis.com/auth/gmail.modify", "https://www.googleapis.com/auth/calendar.events.owned.readonly"}, Username: "gmail@example.test"}, nil
	}
	return s.store.LoadOAuth(ctx, p, a)
}

type externalWrite struct {
	path, method string
	body         map[string]any
}
type nativeAPI struct {
	mu                  sync.Mutex
	bodyReads, jevCalls map[string]int
	categories          map[string][]string
	writes              []externalWrite
}

func newNativeAPI() *nativeAPI {
	return &nativeAPI{bodyReads: map[string]int{}, jevCalls: map[string]int{}, categories: map[string][]string{}}
}
func response(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
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
		if r.Method == "PATCH" {
			w.WriteHeader(204)
		} else {
			response(w, map[string]any{"id": "draft-1"})
		}
		return
	}
	switch r.URL.Path {
	case "/me":
		response(w, map[string]string{"userPrincipalName": "work@example.test"})
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
		e := map[string]any{"id": "e", "subject": "private-subject-event", "start": map[string]string{"dateTime": "2026-03-31T10:00:00", "timeZone": "UTC"}, "end": map[string]string{"dateTime": "2026-03-31T11:00:00", "timeZone": "UTC"}, "body": map[string]string{"contentType": "text", "content": "private-body-event"}, "categories": []string{}}
		if r.URL.Path == "/me/calendarView" {
			response(w, map[string]any{"value": []any{e}})
		} else {
			response(w, e)
		}
	default:
		if strings.HasPrefix(r.URL.Path, "/me/messages/") {
			id := strings.TrimPrefix(r.URL.Path, "/me/messages/")
			if id == "gone" {
				w.WriteHeader(404)
				return
			}
			if strings.Contains(r.URL.Query().Get("$select"), "conversationIndex") {
				response(w, map[string]any{"isDraft": true, "conversationIndex": base64.StdEncoding.EncodeToString(make([]byte, 22))})
				return
			}
			body := strings.Contains(r.URL.Query().Get("$select"), ",body")
			if body {
				a.bodyReads[id]++
			}
			response(w, messageJSON(id, body, a.categories[id]))
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
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		w.WriteHeader(400)
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
	if id == "retry" && n == 1 {
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
		response(w, map[string]string{"emailAddress": "gmail@example.test"})
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
func TestNativeBinaryLongLivedWorkflow(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(t.TempDir(), "agentmailguard")
	build := exec.Command("go", "build", "-o", binary, "./cmd/agentmailguard")
	build.Dir = root
	if b, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build: %v %s", e, b)
	}
	verifyNativeRefusal(t, binary)
	verifyNativeFileServe(t, binary)
	home := t.TempDir()
	configFile(t, home)
	f, e := os.OpenFile(filepath.Join(home, "config.toml"), os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	_, e = f.WriteString("[accounts.gmail]\nprovider='gmail'\nclient_id='synthetic'\nclient_secret='private-config-canary'\n")
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(home, "rules.toml"), []byte("[accounts.work.archive]\nfolder='archive'\ninstructions='private-rule-canary'\n[accounts.work.work]\ncategory='Work'\ninstructions='private-rule-canary'\n"), 0600); e != nil {
		t.Fatal(e)
	}
	api := newNativeAPI()
	graph := httptest.NewServer(http.HandlerFunc(api.graph))
	defer graph.Close()
	google := httptest.NewServer(http.HandlerFunc(api.gmail))
	defer google.Close()
	jev := httptest.NewServer(http.HandlerFunc(api.jev))
	defer jev.Close()
	creds := &nativeStore{}
	screen := screening.New(screening.Options{Endpoint: jev.URL, Credentials: creds, LookupEnv: func(string) string { return "synthetic-key" }})
	s, e := server.New(context.Background(), home, server.Options{Credentials: creds, Screener: screen, M365: m365.Options{BaseURL: graph.URL}, Gmail: gmail.Options{GmailURL: google.URL + "/gmail", ProfileURL: google.URL + "/gmail/profile", CalendarURL: google.URL + "/calendar"}, Now: func() time.Time { return time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC) }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	h := httptest.NewServer(s)
	defer h.Close()
	// Child clients receive only a remote URL and an empty temporary home. They cannot see the server's configuration or credentials.
	clientHome := t.TempDir()
	run := func(args ...string) (string, string, int) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, append([]string{"--server", h.URL}, args...)...)
		cmd.Env = []string{"HOME=" + clientHome, "AGENTMAILGUARD_HOME=" + filepath.Join(clientHome, "does-not-exist")}
		var out, errout bytes.Buffer
		cmd.Stdout = &out
		cmd.Stderr = &errout
		e := cmd.Run()
		code := 0
		if e != nil {
			if ex, ok := e.(*exec.ExitError); ok {
				code = ex.ExitCode()
			} else {
				code = -1
			}
		}
		return out.String(), errout.String(), code
	}
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
	verifyNativeOutputFailure(t, binary, s)
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
	preview := ok("mail", "organize", "--since", "7d")
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
			out, errout, code := run("mail", "organize", "--since", "7d", "--apply")
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
	again := ok("mail", "organize", "--apply")
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
	ok("draft", "reply", "r", "--body", "private-reply-body")
	ok("draft", "forward", "r", "--to", "first@example.test", "--to", "second@example.test")
	assertPatches([]string{"draft", "update", "draft-1", "--body", "private-replacement-body"}, map[string]map[string]any{
		"/me/messages/draft-1": {"body": map[string]any{"contentType": "text", "content": "private-replacement-body"}},
	})
	assertPatches([]string{"draft", "update", "draft-1", "--body", ""}, map[string]map[string]any{
		"/me/messages/draft-1": {"body": map[string]any{"contentType": "text", "content": ""}},
	})
	api.mu.Lock()
	writes := append([]externalWrite{}, api.writes...)
	api.mu.Unlock()
	foundNew, foundReply, foundForward := false, false, false
	flags := map[string]bool{}
	for _, w := range writes {
		if f, ok := w.body["flag"].(map[string]any); ok {
			flags[f["flagStatus"].(string)] = true
		}
		if w.path == "/me/messages" && w.method == "POST" {
			foundNew = w.body["subject"] == "private-draft-subject" && w.body["body"].(map[string]any)["content"] == "private-draft-body" && reflect.DeepEqual(w.body["toRecipients"], []any{map[string]any{"emailAddress": map[string]any{"address": "first@example.test"}}, map[string]any{"emailAddress": map[string]any{"address": "second@example.test"}}})
		}
		if strings.HasSuffix(w.path, "/createReply") {
			foundReply = w.path == "/me/messages/r/createReply" && w.body["comment"] == "private-reply-body"
		}
		if strings.HasSuffix(w.path, "/createForward") {
			foundForward = w.path == "/me/messages/r/createForward" && reflect.DeepEqual(w.body["message"].(map[string]any)["toRecipients"], []any{map[string]any{"emailAddress": map[string]any{"address": "first@example.test"}}, map[string]any{"emailAddress": map[string]any{"address": "second@example.test"}}})
		}
	}
	if !foundNew || !foundReply || !foundForward || len(flags) != 3 {
		t.Fatal("exact mutation/draft payloads differ")
	}
	ok("calendar", "list", "--from", "2026-03-31", "--to", "+1d")
	ok("calendar", "get", "e")
	ok("auth", "status")
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
	ok("--account", "gmail", "auth", "status")
	ok("--account", "gmail", "mail", "folders")
	ok("--account", "gmail", "calendar", "list", "--from", "2026-03-31", "--to", "+1d")
	stats := ok("stats")
	for _, account := range []string{"work", "other", "gmail"} {
		if !strings.Contains(stats, `"`+account+`":`) {
			t.Errorf("stats missing %s", account)
		}
	}
	out, errout, code := run("mail", "get", "gone")
	if code != 1 || out != "" || strings.Contains(errout, "private-") {
		t.Fatal("whole-call error leakage or exit mismatch")
	}
	log, e := os.ReadFile(filepath.Join(home, "audit.jsonl"))
	if e != nil {
		t.Fatal(e)
	}
	for _, canary := range []string{"private-body", "private-name", "private-subject", "private-draft", "private-reply", "private-replacement", "private-rule", "synthetic-key", "private-config", "sender@example.test"} {
		if bytes.Contains(log, []byte(canary)) {
			t.Fatalf("audit contains %s", canary)
		}
	}
	clear := ok("cache", "clear")
	if !strings.Contains(clear, `"deleted": 7`) {
		t.Errorf("clear count: %s", clear)
	}
	if strings.TrimSpace(ok("stats")) != "{}" {
		t.Fatal("cache did not clear")
	}
	entries, e := os.ReadDir(clientHome)
	if e != nil || len(entries) != 0 {
		t.Fatal("remote client created local state")
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
			args := []string{"--server", h.URL, "mail", "get", "r"}
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

// Missing configuration must be refused before any credential backend is accessed.
func verifyNativeFileServe(t *testing.T, binary string) {
	t.Helper()
	for _, source := range []string{"environment", "pipe"} {
		t.Run("native_file_"+source, func(t *testing.T) {
			home := t.TempDir()
			configFile(t, home)
			f, err := os.OpenFile(filepath.Join(home, "config.toml"), os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.WriteString("\n[credentials]\nbackend='file'\n")
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "serve", "--home", home, "--listen", "127.0.0.1:0")
			cmd.Env = []string{"HOME=" + home, "OPENROUTER_API_KEY=synthetic-env-key"}
			if source == "environment" {
				cmd.Env = append(cmd.Env, credentials.PasswordEnvironment+"=synthetic-password")
			} else {
				cmd.Stdin = strings.NewReader("synthetic-password\n")
			}
			pipe, err := cmd.StderrPipe()
			if err != nil {
				t.Fatal(err)
			}
			cmd.Stdout = io.Discard
			if cmd.Start() != nil {
				t.Fatal("native serve start failed")
			}
			listening := make(chan struct{}, 1)
			done := make(chan error, 1)
			var output strings.Builder
			go func() {
				scanner := bufio.NewScanner(pipe)
				for scanner.Scan() {
					line := scanner.Text()
					output.WriteString(line + "\n")
					if line == "server listening" {
						listening <- struct{}{}
					}
				}
				done <- cmd.Wait()
			}()
			select {
			case <-listening:
				if cmd.Process.Signal(syscall.SIGTERM) != nil {
					t.Fatal("native SIGTERM failed")
				}
			case <-done:
				t.Fatal("native file serve refused start")
			case <-ctx.Done():
				t.Fatal("native file serve did not listen")
			}
			if err := <-done; err != nil {
				t.Fatal("native file serve did not shut down cleanly")
			}
			text := output.String()
			if !strings.Contains(text, "credential check: credentials (file): ok") || !strings.Contains(text, "not logged in;") || strings.Contains(text, "synthetic-") || strings.Contains(text, "Keychain") {
				t.Fatal("native file preload output incorrect")
			}
			data, err := os.ReadFile(filepath.Join(home, "credentials.age"))
			if err != nil || !bytes.Contains(data, []byte(" 18\n")) {
				t.Fatal("native writer did not use production work factor")
			}
			// Even an environment Jev key cannot bypass a failed file store.
			bad := exec.CommandContext(ctx, binary, "serve", "--home", home, "--listen", "127.0.0.1:0")
			bad.Env = []string{"HOME=" + home, "OPENROUTER_API_KEY=synthetic-env-key", credentials.PasswordEnvironment + "=wrong-password"}
			var stderr bytes.Buffer
			bad.Stderr = &stderr
			if bad.Run() == nil || !strings.Contains(stderr.String(), "credentials (file): wrong password or damaged file") || strings.Contains(stderr.String(), "server listening") {
				t.Fatal("native wrong password did not fail closed")
			}
		})
	}
}

func verifyNativeRefusal(t *testing.T, binary string) {
	t.Helper()
	home := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "serve", "--home", home, "--listen", "127.0.0.1:0")
	cmd.Env = []string{"HOME=" + home}
	var stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = io.Discard, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || stderr.String() != "error: configuration unavailable\n" {
		t.Fatalf("native serve without configuration: %v %q", err, stderr.String())
	}
}
