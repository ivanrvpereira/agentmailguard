package m365_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/config"
	"github.com/ivanrvpereira/agentmailguard-go/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/m365"
)

var now = time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)

func jwt(claims any) string {
	b, _ := json.Marshal(claims)
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".signature"
}
func goodToken() string {
	return jwt(map[string]any{"scp": "User.Read Mail.ReadWrite Calendars.Read MailboxSettings.Read openid profile email"})
}

type store struct {
	mu           sync.Mutex
	token        credentials.OAuth
	err          error
	loads, saves int
}

func (s *store) LoadOAuth(_ context.Context, p, a string) (credentials.OAuth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loads++
	if p != "m365" || a != "work" {
		panic("wrong namespace")
	}
	return s.token, s.err
}
func (s *store) SaveOAuth(_ context.Context, p, a string, v credentials.OAuth) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saves++
	s.token = v
	return s.err
}
func (*store) LoadOpenRouterKey(context.Context) (string, error) { panic("unexpected key access") }
func (*store) SaveOpenRouterKey(context.Context, string) error   { panic("unexpected key access") }
func account() config.Account {
	return config.Account{Name: "work", Provider: "m365", ClientID: "client", TenantID: "tenant"}
}
func validStore() *store {
	return &store{token: credentials.OAuth{AccessToken: goodToken(), RefreshToken: "refresh", ExpiresAt: now.Add(time.Hour), Username: "user@example.test"}}
}
func TestStatusLazyChecksActualScopesAndRetriesCredentialFailure(t *testing.T) {
	s := validStore()
	s.err = errors.New("SECRET")
	c := m365.New(account(), s, m365.Options{Now: func() time.Time { return now }})
	if s.loads != 0 {
		t.Fatal("constructor accessed credentials")
	}
	if _, _, err := c.Status(context.Background()); err == nil || err.Error() != "AuthError" {
		t.Fatalf("unsafe error %v", err)
	}
	s.err = nil
	user, scopes, err := c.Status(context.Background())
	if err != nil || user != "user@example.test" || len(scopes) != 7 {
		t.Fatalf("status %q %v %v", user, scopes, err)
	}
	if _, _, err = c.Status(context.Background()); err != nil || s.loads != 2 {
		t.Fatalf("success not cached: %d %v", s.loads, err)
	}
}
func TestCancelledStatusDoesNotWaitForAnotherRefresh(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		jsonResponse(w, map[string]any{"access_token": goodToken(), "expires_in": 3600})
	}))
	defer server.Close()
	s := validStore()
	s.token.ExpiresAt = now.Add(-time.Second)
	c := m365.New(account(), s, m365.Options{HTTPClient: server.Client(), AuthorityURL: server.URL, Now: func() time.Time { return now }})
	first := make(chan error, 1)
	go func() { _, _, err := c.Status(context.Background()); first <- err }()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	second := make(chan error, 1)
	go func() { _, _, err := c.Status(ctx); second <- err }()
	select {
	case err := <-second:
		if err == nil {
			t.Error("cancelled status succeeded")
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("cancelled status waited on refresh lock")
	}
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}
func TestRefreshExpirySharedAndScopeCheckedBeforeSave(t *testing.T) {
	s := validStore()
	var ticks atomic.Int64
	var calls atomic.Int64
	var unsafe atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = r.ParseForm()
		if r.Method != "POST" || r.URL.Path != "/token" || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("refresh_token") != "refresh" {
			t.Error("bad refresh request")
		}
		token := goodToken()
		if unsafe.Load() {
			token = jwt(map[string]any{"scp": "Mail.Send"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": token, "expires_in": 3600, "refresh_token": "rotated"})
	}))
	defer server.Close()
	c := m365.New(account(), s, m365.Options{AuthorityURL: server.URL, HTTPClient: server.Client(), Now: func() time.Time { return now.Add(time.Duration(ticks.Load()) * time.Second) }})
	if _, _, err := c.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	ticks.Store(3601)
	unsafe.Store(true)
	if _, _, err := c.Status(context.Background()); err == nil {
		t.Fatal("expanded refreshed scopes accepted")
	}
	if s.saves != 0 || s.token.RefreshToken != "refresh" {
		t.Fatal("failed refresh replaced credential")
	}
	unsafe.Store(false)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			if _, _, err := c.Status(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 2 || s.saves != 1 || s.token.RefreshToken != "rotated" {
		t.Fatalf("refresh not coordinated: calls=%d saves=%d", calls.Load(), s.saves)
	}
}
func TestStatusRefreshesRefreshOnlyCredential(t *testing.T) {
	for _, expiry := range []time.Time{{}, now.Add(time.Hour)} {
		t.Run(expiry.String(), func(t *testing.T) {
			s := &store{token: credentials.OAuth{RefreshToken: "refresh", ExpiresAt: expiry, Username: "user@example.test"}}
			fresh := jwt(map[string]any{"scp": "User.Read Mail.ReadWrite Calendars.Read MailboxSettings.Read"})
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				if r.Method != "POST" || r.URL.Path != "/token" || r.Form.Get("grant_type") != "refresh_token" || r.Form.Get("client_id") != "client" || r.Form.Get("refresh_token") != "refresh" {
					t.Error("bad refresh request")
				}
				if r.Form.Get("scope") != "https://graph.microsoft.com/User.Read https://graph.microsoft.com/Mail.ReadWrite https://graph.microsoft.com/Calendars.Read https://graph.microsoft.com/MailboxSettings.Read offline_access openid profile" {
					t.Error("refresh did not request restricted scopes")
				}
				jsonResponse(w, map[string]any{"access_token": fresh, "expires_in": 3600, "refresh_token": "rotated"})
			}))
			defer server.Close()
			c := m365.New(account(), s, m365.Options{HTTPClient: server.Client(), BaseURL: server.URL, AuthorityURL: server.URL, Now: func() time.Time { return now }})
			user, scopes, err := c.Status(context.Background())
			if err != nil || user != "user@example.test" || !reflect.DeepEqual(scopes, []string{"User.Read", "Mail.ReadWrite", "Calendars.Read", "MailboxSettings.Read"}) {
				t.Fatalf("refresh-only status: user=%q scopes=%v err=%v", user, scopes, err)
			}
			if calls.Load() != 1 || s.saves != 1 || s.token.AccessToken != fresh || s.token.RefreshToken != "rotated" || !s.token.ExpiresAt.Equal(now.Add(time.Hour)) || !reflect.DeepEqual(s.token.Scopes, []string{"User.Read", "Mail.ReadWrite", "Calendars.Read", "MailboxSettings.Read"}) {
				t.Fatalf("refresh-only credential not saved: requests=%d saves=%d", calls.Load(), s.saves)
			}
		})
	}
}

func TestExpiredUnsafeCredentialUsesOnlyValidatedRefresh(t *testing.T) {
	for _, oldToken := range []struct{ name, token string }{
		{"overbroad", jwt(map[string]any{"scp": "User.Read Mail.Send"})},
		{"malformed", "opaque"},
	} {
		for _, result := range []struct {
			name, token, oauthError string
			allowed                 bool
		}{
			{name: "restricted", token: jwt(map[string]any{"scp": "User.Read Mail.ReadWrite Calendars.Read MailboxSettings.Read"}), allowed: true},
			{name: "overbroad", token: jwt(map[string]any{"scp": "User.Read Mail.ReadWrite Calendars.Read MailboxSettings.Read Mail.Send"})},
			{name: "malformed", token: "opaque"},
			{name: "failed", oauthError: "invalid_grant"},
		} {
			t.Run(oldToken.name+"/"+result.name, func(t *testing.T) {
				old := credentials.OAuth{AccessToken: oldToken.token, RefreshToken: "refresh", ExpiresAt: now.Add(-time.Second), Username: "user@example.test", Scopes: []string{"Mail.Send"}}
				s := &store{token: old}
				var exchanges, graphCalls atomic.Int64
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/token" {
						exchanges.Add(1)
						_ = r.ParseForm()
						if r.Method != "POST" || r.Form.Get("refresh_token") != "refresh" || r.Form.Get("scope") != "https://graph.microsoft.com/User.Read https://graph.microsoft.com/Mail.ReadWrite https://graph.microsoft.com/Calendars.Read https://graph.microsoft.com/MailboxSettings.Read offline_access openid profile" {
							t.Error("bad restricted refresh request")
						}
						jsonResponse(w, map[string]any{"access_token": result.token, "expires_in": 3600, "refresh_token": "rotated", "error": result.oauthError})
						return
					}
					graphCalls.Add(1)
					if !result.allowed || r.Method != "GET" || r.URL.Path != "/me/messages/message" || r.Header.Get("Authorization") != "Bearer "+result.token {
						t.Error("Graph used an unvalidated token or unexpected request")
					}
					jsonResponse(w, map[string]any{"id": "message"})
				}))
				defer server.Close()
				c := m365.New(account(), s, m365.Options{HTTPClient: server.Client(), BaseURL: server.URL, AuthorityURL: server.URL, Now: func() time.Time { return now }})
				user, scopes, err := c.Status(context.Background())
				if result.allowed {
					if err != nil || user != "user@example.test" || !reflect.DeepEqual(scopes, []string{"User.Read", "Mail.ReadWrite", "Calendars.Read", "MailboxSettings.Read"}) {
						t.Fatalf("restricted refresh status: user=%q scopes=%v err=%v", user, scopes, err)
					}
					if _, err := c.Mailbox().Get(context.Background(), []string{"message"}, false); err != nil {
						t.Fatal(err)
					}
					if exchanges.Load() != 1 || graphCalls.Load() != 1 || s.saves != 1 || s.token.AccessToken != result.token || s.token.RefreshToken != "rotated" || !reflect.DeepEqual(s.token.Scopes, []string{"User.Read", "Mail.ReadWrite", "Calendars.Read", "MailboxSettings.Read"}) {
						t.Fatalf("validated refresh not used: exchanges=%d Graph=%d saves=%d", exchanges.Load(), graphCalls.Load(), s.saves)
					}
				} else {
					if err == nil || err.Error() != "AuthError" || user != "" || len(scopes) != 0 {
						t.Fatalf("unsafe refresh status: user=%q scopes=%v err=%v", user, scopes, err)
					}
					if _, err := c.Mailbox().Get(context.Background(), []string{"message"}, false); err == nil || err.Error() != "AuthError" {
						t.Fatalf("unsafe refresh authorized Graph: %v", err)
					}
					if exchanges.Load() != 2 || graphCalls.Load() != 0 || s.saves != 0 || !reflect.DeepEqual(s.token, old) {
						t.Fatalf("refused refresh was cached, used, or saved: exchanges=%d Graph=%d saves=%d", exchanges.Load(), graphCalls.Load(), s.saves)
					}
				}
			})
		}
	}
}

func TestFreshOverbroadCredentialIsNotRefreshedOrUsed(t *testing.T) {
	old := credentials.OAuth{AccessToken: jwt(map[string]any{"scp": "User.Read Mail.Send"}), RefreshToken: "refresh", ExpiresAt: now.Add(time.Hour), Scopes: []string{"User.Read"}}
	s := &store{token: old}
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		jsonResponse(w, map[string]any{"access_token": goodToken(), "expires_in": 3600})
	}))
	defer server.Close()
	c := m365.New(account(), s, m365.Options{HTTPClient: server.Client(), BaseURL: server.URL, AuthorityURL: server.URL, Now: func() time.Time { return now }})
	if user, scopes, err := c.Status(context.Background()); err == nil || err.Error() != "AuthError" || user != "" || len(scopes) != 0 {
		t.Fatalf("fresh overbroad status: user=%q scopes=%v err=%v", user, scopes, err)
	}
	if _, err := c.Mailbox().Get(context.Background(), []string{"message"}, false); err == nil || err.Error() != "AuthError" {
		t.Fatalf("fresh overbroad token authorized Graph: %v", err)
	}
	if calls.Load() != 0 || s.saves != 0 || !reflect.DeepEqual(s.token, old) {
		t.Fatalf("fresh overbroad token was refreshed, used, or saved: requests=%d saves=%d", calls.Load(), s.saves)
	}
}

func TestStatusRejectsUnsafeActualToken(t *testing.T) {
	for _, token := range []string{"", "opaque", "a.b.c", jwt([]string{"User.Read"}), jwt(map[string]any{}), jwt(map[string]any{"scp": []string{"User.Read"}}), jwt(map[string]any{"scp": "User.Read Mail.Send"}), jwt(map[string]any{"scp": "User.Read", "roles": []string{"Mail.Read"}})} {
		t.Run(token, func(t *testing.T) {
			s := validStore()
			s.token.AccessToken = token
			if token == "" {
				s.token.RefreshToken = ""
			}
			s.token.Scopes = []string{"User.Read"}
			c := m365.New(account(), s, m365.Options{Now: func() time.Time { return now }})
			if _, _, err := c.Status(context.Background()); err == nil {
				t.Fatal("unsafe token accepted")
			}
			if s.saves != 0 {
				t.Fatal("unsafe credential saved")
			}
		})
	}
}
