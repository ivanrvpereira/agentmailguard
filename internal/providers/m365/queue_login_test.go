package m365_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
)

func TestQueuedLoginKeepsItsIdentityAndDoesNotDeadlockRefresh(t *testing.T) {
	for _, mode := range []string{"coexist", "expired", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			var advanced, watch, issued atomic.Bool
			checked := make(chan struct{}, 4)
			prepared := make(chan struct{})
			var preparedOnce sync.Once
			started := make(chan struct{}, 4)
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			fresh := jwt(map[string]any{"scp": "User.Read Mail.ReadWrite", "version": "login"})
			var profiles, refreshes, queuedCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/devicecode":
					jsonResponse(w, map[string]any{"device_code": "device", "user_code": "code", "verification_uri": "https://synthetic.invalid", "expires_in": 60})
				case "/token":
					_ = r.ParseForm()
					if r.Form.Get("grant_type") == "refresh_token" {
						refreshes.Add(1)
						w.WriteHeader(400)
						jsonResponse(w, map[string]string{"error": "invalid_grant"})
						return
					}
					issued.Store(true)
					lifetime := 3600
					if mode == "expired" {
						lifetime = 1
					}
					jsonResponse(w, map[string]any{"access_token": fresh, "refresh_token": "login-refresh", "expires_in": lifetime})
				case "/v1.0/me":
					profiles.Add(1)
					if r.Header.Get("Authorization") != "Bearer "+fresh {
						t.Error("Login profile used stored identity")
					}
					jsonResponse(w, map[string]string{"userPrincipalName": "login@example.test"})
				case "/v1.0/me/messages/queued":
					queuedCalls.Add(1)
					if r.Header.Get("Authorization") != "Bearer "+fresh {
						t.Error("queued read used expired identity")
					}
					jsonResponse(w, map[string]string{"id": "queued", "parentFolderId": "inbox-id"})
				default:
					started <- struct{}{}
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
					jsonResponse(w, map[string]string{"id": "blocked", "parentFolderId": "inbox-id"})
				}
			}))
			defer server.Close()
			defer unblock()
			s := validStore()
			s.token.ExpiresAt = now.Add(time.Second)
			old := s.token
			c := m365.New(account(), s, m365.Options{HTTPClient: server.Client(), BaseURL: server.URL + "/v1.0", AuthorityURL: server.URL, Wait: func(context.Context, time.Duration) error { return nil }, Now: func() time.Time {
				value := now
				if advanced.Load() {
					value = value.Add(2 * time.Second)
				}
				if issued.Load() {
					preparedOnce.Do(func() { close(prepared) })
				} else if watch.Load() {
					select {
					case checked <- struct{}{}:
					default:
					}
				}
				return value
			}})
			if _, _, err := c.Status(ctx); err != nil {
				t.Fatal(err)
			}
			blocked := make(chan error, 4)
			for range 4 {
				go func() { _, err := c.Mailbox().Get(ctx, []string{"blocked"}, false); blocked <- err }()
			}
			for range 4 {
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal("API slots did not fill")
				}
			}
			queued := make(chan error, 4)
			if mode == "coexist" {
				watch.Store(true)
				for range 4 {
					go func() { _, err := c.Mailbox().Get(ctx, []string{"queued"}, false); queued <- err }()
				}
				for range 4 {
					select {
					case <-checked:
					case <-ctx.Done():
						t.Fatal("queued reads did not resolve credentials")
					}
				}
			}
			loginCtx, cancelLogin := context.WithCancel(ctx)
			defer cancelLogin()
			login := make(chan error, 1)
			go func() { _, _, err := c.Login(loginCtx, true, func(string, string) {}); login <- err }()
			select {
			case <-prepared:
			case <-ctx.Done():
				t.Fatal("Login token was not prepared")
			}
			advanced.Store(true)
			if mode == "cancel" {
				cancelLogin()
			} else {
				unblock()
			}
			select {
			case err := <-login:
				if (err == nil) != (mode == "coexist") {
					t.Fatalf("queued Login: %v", err)
				}
			case <-ctx.Done():
				t.Fatal("Login blocked behind requests waiting for authentication")
			}
			unblock()
			for range 4 {
				if err := <-blocked; err != nil {
					t.Fatal(err)
				}
			}
			if mode == "coexist" {
				for range 4 {
					select {
					case err := <-queued:
						if err != nil {
							t.Fatal(err)
						}
					case <-ctx.Done():
						t.Fatal("queued read did not finish after Login")
					}
				}
				if profiles.Load() != 1 || queuedCalls.Load() != 4 || s.saves != 1 || s.token.Username != "login@example.test" || !s.token.ExpiresAt.Equal(now.Add(time.Hour)) {
					t.Fatal("Login identity or acquisition deadline not preserved")
				}
			} else if profiles.Load() != 0 || s.saves != 0 || !reflect.DeepEqual(s.token, old) {
				t.Fatal("expired/cancelled Login fetched profile or replaced credentials")
			}
			if refreshes.Load() != 0 {
				t.Fatal("Login refreshed the old stored identity")
			}
		})
	}
}
