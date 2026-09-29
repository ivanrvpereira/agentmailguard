package server_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/gmail"
	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard-go/internal/server"
)

// preloadStore records every backend access so tests can prove when the backend is touched.
type preloadStore struct {
	mu         sync.Mutex
	records    map[string]credentials.OAuth
	loadErr    map[string]error
	saveErr    error
	key        string
	keyErr     error
	keySaveErr error
	reads      []string
	writes     []string
	saved      []credentials.OAuth
}

func (s *preloadStore) LoadOAuth(_ context.Context, p, a string) (credentials.OAuth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, p+"/"+a)
	if e := s.loadErr[p+"/"+a]; e != nil {
		return credentials.OAuth{}, e
	}
	v, ok := s.records[p+"/"+a]
	if !ok {
		return credentials.OAuth{}, credentials.ErrNotFound
	}
	return v, nil
}
func (s *preloadStore) SaveOAuth(_ context.Context, p, a string, v credentials.OAuth) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, p+"/"+a)
	s.saved = append(s.saved, v)
	if s.saveErr != nil {
		return s.saveErr
	}
	s.records[p+"/"+a] = v
	return nil
}
func (s *preloadStore) LoadOpenRouterKey(context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads = append(s.reads, "jev")
	if s.keyErr != nil {
		return "", s.keyErr
	}
	if s.key == "" {
		return "", credentials.ErrNotFound
	}
	return s.key, nil
}
func (s *preloadStore) SaveOpenRouterKey(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, "jev")
	if s.keySaveErr != nil {
		return s.keySaveErr
	}
	s.key = key
	return nil
}
func (s *preloadStore) accesses() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.reads...), append([]string(nil), s.writes...)
}

const m365Grant = "User.Read Mail.ReadWrite Calendars.Read MailboxSettings.Read"
const gmailGrant = "https://www.googleapis.com/auth/gmail.modify https://www.googleapis.com/auth/calendar.events.owned.readonly"

func twoAccountHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if e := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[accounts.work]\nprovider='m365'\nclient_id='synthetic'\ntenant_id='synthetic'\n[accounts.personal]\nprovider='gmail'\nclient_id='synthetic'\nclient_secret='synthetic'\n"), 0600); e != nil {
		t.Fatal(e)
	}
	return home
}
func loggedIn(now time.Time) *preloadStore {
	return &preloadStore{key: "private-jev-canary", records: map[string]credentials.OAuth{
		"m365/work":      {AccessToken: statusJWT(m365Grant), RefreshToken: "private-refresh-0", ExpiresAt: now.Add(time.Hour), Scopes: strings.Fields(m365Grant), Username: "work@example.test"},
		"gmail/personal": {AccessToken: "private-gmail-canary", RefreshToken: "private-gmail-refresh", ExpiresAt: now.Add(time.Hour), Scopes: strings.Fields(gmailGrant), Username: "personal@example.test"},
	}}
}

func TestPreloadReadsEverythingAtStartThenServesFromMemory(t *testing.T) {
	var mu sync.Mutex
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	var refreshes []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			_ = r.ParseForm()
			mu.Lock()
			refreshes = append(refreshes, r.Form.Get("refresh_token"))
			n := len(refreshes)
			mu.Unlock()
			response(w, map[string]any{"access_token": statusJWT(m365Grant), "refresh_token": "private-refresh-" + string(rune('0'+n)), "expires_in": 3600})
			return
		}
		response(w, map[string]string{"emailAddress": "personal@example.test"})
	}))
	defer api.Close()
	home := twoAccountHome(t)
	store := loggedIn(now)
	before := map[string]credentials.OAuth{"m365/work": store.records["m365/work"], "gmail/personal": store.records["gmail/personal"]}
	s, e := server.New(context.Background(), home, server.Options{Credentials: store, LookupEnv: func(string) string { return "" }, M365: m365.Options{BaseURL: api.URL, AuthorityURL: api.URL}, Gmail: gmail.Options{GmailURL: api.URL, TokenURL: api.URL + "/token", ProfileURL: api.URL + "/profile"}, Now: clock})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	checks, e := s.Preload(context.Background(), nil)
	want := []server.CredentialCheck{{Item: `account "work" (m365)`, Status: "ok"}, {Item: `account "personal" (gmail)`, Status: "ok"}, {Item: "Jev key", Status: "ok"}}
	if e != nil || !reflect.DeepEqual(checks, want) {
		t.Fatalf("checks=%v err=%v", checks, e)
	}
	reads, writes := store.accesses()
	if !reflect.DeepEqual(reads, []string{"m365/work", "gmail/personal", "jev"}) || !reflect.DeepEqual(writes, []string{"m365/work", "gmail/personal"}) {
		t.Fatalf("startup reads=%v writes=%v", reads, writes)
	}
	// The write probe stores the loaded record unchanged, so modify permission is requested at start too.
	if !reflect.DeepEqual(store.saved, []credentials.OAuth{before["m365/work"], before["gmail/personal"]}) {
		t.Fatal("write probe changed a credential record")
	}
	h := httptest.NewServer(s)
	defer h.Close()
	status := func(account string) {
		t.Helper()
		code, body := rpc(t, h.URL, `{"operation":"auth.status","account":"`+account+`","params":{}}`)
		if code != 200 || !strings.Contains(body, `"ok":true`) {
			t.Fatalf("status %s: %d %s", account, code, body)
		}
	}
	status("work")
	status("personal")
	// Expiry forces two M365 refreshes; the second must use the refresh token saved by the first.
	for range 2 {
		mu.Lock()
		now = now.Add(2 * time.Hour)
		mu.Unlock()
		status("work")
	}
	reads, writes = store.accesses()
	if len(reads) != 3 {
		t.Fatalf("backend read after startup: %v", reads)
	}
	if !reflect.DeepEqual(writes, []string{"m365/work", "gmail/personal", "m365/work", "m365/work"}) {
		t.Fatalf("refresh saves did not reach the backend: %v", writes)
	}
	if !reflect.DeepEqual(refreshes, []string{"private-refresh-0", "private-refresh-1"}) {
		t.Fatalf("memory did not keep the saved refresh token: %v", refreshes)
	}
}

func TestPreloadRefusesWithFixedStatuses(t *testing.T) {
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	canary := errors.New("private-backend-canary")
	for _, tc := range []struct {
		name   string
		edit   func(*preloadStore)
		prompt func(context.Context) (string, error)
		item   string
		status string
	}{
		{"not logged in", func(s *preloadStore) { delete(s.records, "m365/work") }, nil, `account "work" (m365)`, "not logged in; run auth login"},
		{"denied", func(s *preloadStore) { s.loadErr = map[string]error{"gmail/personal": credentials.ErrUnavailable} }, nil, `account "personal" (gmail)`, "Keychain access failed or denied"},
		{"unreadable", func(s *preloadStore) { s.loadErr = map[string]error{"m365/work": canary} }, nil, `account "work" (m365)`, "credential record unreadable; run auth login"},
		{"write denied", func(s *preloadStore) { s.saveErr = canary }, nil, `account "work" (m365)`, "Keychain write failed or denied"},
		{"key missing", func(s *preloadStore) { s.key = "" }, nil, "Jev key", "missing; run auth set-jev-key or set OPENROUTER_API_KEY"},
		{"key denied", func(s *preloadStore) { s.keyErr = canary }, nil, "Jev key", "Keychain access failed or denied"},
		{"key not entered", func(s *preloadStore) { s.key = "" }, func(context.Context) (string, error) { return "", canary }, "Jev key", "not entered; run auth set-jev-key or set OPENROUTER_API_KEY"},
		{"key save denied", func(s *preloadStore) { s.key = ""; s.keySaveErr = canary }, func(context.Context) (string, error) { return "sk-private-canary", nil }, "Jev key", "Keychain write failed or denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := loggedIn(now)
			tc.edit(store)
			s, e := server.New(context.Background(), twoAccountHome(t), server.Options{Credentials: store, LookupEnv: func(string) string { return "" }, Now: func() time.Time { return now }})
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			checks, e := s.Preload(context.Background(), tc.prompt)
			if e == nil {
				t.Fatal("preload accepted a failed credential")
			}
			found := false
			for _, c := range checks {
				found = found || c == server.CredentialCheck{Item: tc.item, Status: tc.status}
				if strings.Contains(c.Item+c.Status+e.Error(), "private") {
					t.Fatal("check exposed backend text or secrets")
				}
			}
			if !found || len(checks) != 3 {
				t.Fatalf("checks=%v", checks)
			}
		})
	}
}

func TestPreloadPromptsForMissingKeyAndSkipsEnvironmentKey(t *testing.T) {
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	store := loggedIn(now)
	store.key = ""
	s, e := server.New(context.Background(), twoAccountHome(t), server.Options{Credentials: store, LookupEnv: func(string) string { return "" }, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	checks, e := s.Preload(context.Background(), func(context.Context) (string, error) { return "sk-entered", nil })
	if e != nil || checks[2] != (server.CredentialCheck{Item: "Jev key", Status: "saved"}) || store.key != "sk-entered" {
		t.Fatalf("checks=%v err=%v", checks, e)
	}

	store = loggedIn(now)
	s2, e := server.New(context.Background(), twoAccountHome(t), server.Options{Credentials: store, LookupEnv: func(k string) string {
		if k == "OPENROUTER_API_KEY" {
			return "sk-env"
		}
		return ""
	}, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer s2.Close()
	checks, e = s2.Preload(context.Background(), nil)
	reads, _ := store.accesses()
	if e != nil || checks[2] != (server.CredentialCheck{Item: "Jev key", Status: "ok (OPENROUTER_API_KEY)"}) || len(reads) != 2 {
		t.Fatalf("checks=%v reads=%v err=%v", checks, reads, e)
	}
}

func TestPreloadRefusesBadConfigurationAndLateAccounts(t *testing.T) {
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	store := loggedIn(now)
	s, e := server.New(context.Background(), t.TempDir(), server.Options{Credentials: store, LookupEnv: func(string) string { return "" }})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	if _, e := s.Preload(context.Background(), nil); e == nil || e.Error() != "configuration unavailable" {
		t.Fatalf("missing configuration: %v", e)
	}
	if reads, _ := store.accesses(); len(reads) != 0 {
		t.Fatal("backend read without configuration")
	}

	home := twoAccountHome(t)
	s2, e := server.New(context.Background(), home, server.Options{Credentials: store, LookupEnv: func(string) string { return "" }, Now: func() time.Time { return now }})
	if e != nil {
		t.Fatal(e)
	}
	defer s2.Close()
	if _, e := s2.Preload(context.Background(), nil); e != nil {
		t.Fatal(e)
	}
	late, _ := os.ReadFile(filepath.Join(home, "config.toml"))
	late = append(late, "[accounts.late]\nprovider='m365'\nclient_id='synthetic'\ntenant_id='synthetic'\n"...)
	if e := os.WriteFile(filepath.Join(home, "config.toml"), late, 0600); e != nil {
		t.Fatal(e)
	}
	h := httptest.NewServer(s2)
	defer h.Close()
	code, body := rpc(t, h.URL, `{"operation":"auth.status","account":"late","params":{}}`)
	if code != 500 || !strings.Contains(body, "restart required") {
		t.Fatalf("late account: %d %s", code, body)
	}
	if reads, _ := store.accesses(); len(reads) != 3 {
		t.Fatalf("late account reached the backend: %v", reads)
	}
}
