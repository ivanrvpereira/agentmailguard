package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

type recordPlatform struct {
	mu                  sync.Mutex
	data                []byte
	reads, writes       int
	failRead, failWrite bool
}

func (p *recordPlatform) Read(context.Context, string, string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reads++
	if p.failRead {
		return nil, errors.New("private-platform-canary")
	}
	return append([]byte(nil), p.data...), nil
}
func (p *recordPlatform) Write(_ context.Context, _, _ string, v []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writes++
	if p.failWrite {
		return errors.New("private-platform-canary")
	}
	p.data = append([]byte(nil), v...)
	return nil
}

type panicRecordStore struct{ credentials.Store }

func (panicRecordStore) Preload(context.Context) (credentials.Record, error) {
	panic("private-platform-canary")
}

func TestRecordPreloadRefusesFailuresEvenWithEnvironmentKey(t *testing.T) {
	for _, mode := range []string{"read", "write", "unsupported", "malformed", "panic"} {
		t.Run(mode, func(t *testing.T) {
			home := twoAccountHome(t)
			p := &recordPlatform{data: []byte(`{"version":1,"oauth":{},"openrouter_key":""}`)}
			var store credentials.Store = credentials.New(p, home, nil)
			switch mode {
			case "read":
				p.failRead = true
			case "write":
				p.failWrite = true
			case "unsupported":
				p.data = []byte(`{"version":2,"oauth":{},"openrouter_key":""}`)
			case "malformed":
				p.data = []byte(`{"version":1,`)
			case "panic":
				store = panicRecordStore{}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			s, err := server.New(ctx, home, server.Options{Credentials: store, LookupEnv: func(string) string { return "synthetic-env-key" }})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			_, err = s.Preload(ctx, func(context.Context) (string, error) { t.Fatal("failed store prompted for a key"); return "", nil })
			if err == nil || err.Error() != "credential check failed" {
				t.Fatal("store failure was not safely refused")
			}
		})
	}
}
func TestRecordPreloadKeepsMissingLoginAndJevPromptSemantics(t *testing.T) {
	home := twoAccountHome(t)
	p := &recordPlatform{data: []byte(`{"version":1,"oauth":{},"openrouter_key":""}`)}
	store := credentials.New(p, home, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s, err := server.New(ctx, home, server.Options{Credentials: store, LookupEnv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	prompts := 0
	checks, err := s.Preload(ctx, func(context.Context) (string, error) { prompts++; return "synthetic-prompt-key", nil })
	if err != nil || len(checks) != 4 || prompts != 1 || checks[3].Status != "saved" {
		t.Fatal("record preload did not prompt and start")
	}
	for _, check := range checks[1:3] {
		if !strings.HasPrefix(check.Status, "not logged in;") {
			t.Fatal("missing account no longer starts disabled")
		}
	}
	if key, err := store.LoadOpenRouterKey(ctx); err != nil || key != "synthetic-prompt-key" {
		t.Fatal("prompted key was not persisted")
	}
}

func TestRecordPreloadUsesOneReadRewriteAndRuntimeMemory(t *testing.T) {
	home := twoAccountHome(t)
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	var clockMu sync.Mutex
	clock := func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	old := loggedIn(now)
	p := &recordPlatform{}
	p.data, _ = json.Marshal(credentials.Record{Version: 1, OAuth: old.records, OpenRouterKey: old.key})
	store := credentials.New(p, home, nil)
	var refreshMu sync.Mutex
	var refreshes []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		refreshMu.Lock()
		refreshes = append(refreshes, r.Form.Get("refresh_token"))
		refreshMu.Unlock()
		response(w, map[string]any{"access_token": statusJWT(m365Grant), "refresh_token": "refreshed-synthetic", "expires_in": 3600})
	}))
	defer api.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s, err := server.New(ctx, home, server.Options{Credentials: store, LookupEnv: func(string) string { return "" }, Now: clock, M365: m365.Options{BaseURL: api.URL, AuthorityURL: api.URL}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	checks, err := s.Preload(ctx, nil)
	if err != nil || len(checks) != 4 {
		t.Fatalf("preload failed: %v", err)
	}
	p.mu.Lock()
	reads, writes := p.reads, p.writes
	p.failRead = true
	p.mu.Unlock()
	if reads != 1 || writes != 1 {
		t.Fatalf("whole-record startup reads=%d writes=%d", reads, writes)
	}
	h := httptest.NewServer(s)
	defer h.Close()
	if code, body := rpc(t, h.URL, `{"operation":"auth.status","account":"work","params":{}}`); code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatal("runtime load reached unavailable backend")
	}
	p.mu.Lock()
	p.failRead = false
	p.mu.Unlock()
	// A separate local command changes the key after the server has loaded its snapshot.
	local := credentials.New(p, home, nil)
	if err := local.SaveOpenRouterKey(ctx, "new-local-key"); err != nil {
		t.Fatal(err)
	}
	clockMu.Lock()
	now = now.Add(2 * time.Hour)
	clockMu.Unlock()
	p.mu.Lock()
	p.failWrite = true
	p.mu.Unlock()
	if _, body := rpc(t, h.URL, `{"operation":"auth.status","account":"work","params":{}}`); strings.Contains(body, `"ok":true`) {
		t.Fatal("refresh succeeded despite backend write failure")
	}
	p.mu.Lock()
	p.failWrite = false
	p.mu.Unlock()
	if code, body := rpc(t, h.URL, `{"operation":"auth.status","account":"work","params":{}}`); code != 200 || !strings.Contains(body, `"ok":true`) {
		t.Fatal("token refresh failed")
	}
	refreshMu.Lock()
	defer refreshMu.Unlock()
	if len(refreshes) != 2 || refreshes[0] != "private-refresh-0" || refreshes[1] != "private-refresh-0" {
		t.Fatal("failed save advanced server memory")
	}
	if key, err := local.LoadOpenRouterKey(ctx); err != nil || key != "new-local-key" {
		t.Fatal("runtime refresh overwrote a local command")
	}
}
