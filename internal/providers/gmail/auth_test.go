package gmail_test

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

	"github.com/ivanrvpereira/agentmailguard-go/internal/config"
	"github.com/ivanrvpereira/agentmailguard-go/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/gmail"
)

const granted = "https://www.googleapis.com/auth/gmail.modify https://www.googleapis.com/auth/calendar.events.owned.readonly"

var account = config.Account{Name: "synthetic", Provider: "gmail", ClientID: "client", ClientSecret: "secret"}

type memoryStore struct {
	mu           sync.Mutex
	token        credentials.OAuth
	loads, saves int
	fail         bool
}

func (s *memoryStore) LoadOAuth(context.Context, string, string) (credentials.OAuth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	if s.fail {
		s.fail = false
		return credentials.OAuth{}, errors.New("SECRET")
	}
	return s.token, nil
}
func (s *memoryStore) SaveOAuth(_ context.Context, _, _ string, v credentials.OAuth) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	s.token = v
	return nil
}
func (*memoryStore) LoadOpenRouterKey(context.Context) (string, error) {
	return "", errors.New("unused")
}
func (*memoryStore) SaveOpenRouterKey(context.Context, string) error { return errors.New("unused") }
func validStore() *memoryStore {
	return &memoryStore{token: credentials.OAuth{AccessToken: "access", RefreshToken: "refresh", ExpiresAt: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), Scopes: strings.Fields(granted), Username: "synthetic@example.test"}}
}
func respond(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestStatusLazyRefreshAndRawScopes(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, scope := range []string{granted, "", granted + " https://www.googleapis.com/auth/gmail.readonly"} {
		t.Run(scope, func(t *testing.T) {
			store := validStore()
			store.token.ExpiresAt = now
			old := store.token
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					if r.Method != "POST" {
						t.Error("token method")
					}
					_ = r.ParseForm()
					if r.Form.Get("refresh_token") != "refresh" || r.Form.Get("client_secret") != "secret" {
						t.Error("refresh form")
					}
					respond(w, map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "scope": scope, "expires_in": 3600})
					return
				}
				if r.URL.Path == "/profile" {
					if r.Header.Get("Authorization") != "Bearer new-access" {
						t.Error("profile token")
					}
					respond(w, map[string]any{"emailAddress": "synthetic@example.test"})
					return
				}
				t.Error("unexpected path")
				w.WriteHeader(404)
			}))
			defer server.Close()
			c := gmail.New(account, store, gmail.Options{HTTPClient: server.Client(), TokenURL: server.URL + "/token", ProfileURL: server.URL + "/profile", Now: func() time.Time { return now }})
			if store.loads != 0 {
				t.Fatal("eager credential access")
			}
			name, scopes, err := c.Status(context.Background())
			if scope != granted {
				if err == nil || store.saves != 0 || store.token.AccessToken != old.AccessToken {
					t.Fatal("invalid raw scopes accepted or saved")
				}
				return
			}
			if err != nil || name != "synthetic@example.test" || len(scopes) != 2 || store.saves != 1 {
				t.Fatalf("status: %q %v %v saves=%d", name, scopes, err, store.saves)
			}
			_, _, err = c.Status(context.Background())
			if err != nil || store.loads != 1 || store.saves != 1 {
				t.Fatal("successful token not cached")
			}
		})
	}
}

func TestFailedCredentialReadCanRecover(t *testing.T) {
	store := validStore()
	store.fail = true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]string{"emailAddress": "synthetic@example.test"})
	}))
	defer server.Close()
	c := gmail.New(account, store, gmail.Options{HTTPClient: server.Client(), ProfileURL: server.URL})
	if _, _, err := c.Status(context.Background()); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("unsafe or missing failure")
	}
	if _, _, err := c.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
}
