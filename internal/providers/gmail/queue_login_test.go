package gmail_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/gmail"
)

func syntheticBrowser(ctx context.Context, uri string) error {
	u, err := url.Parse(uri)
	if err != nil {
		return err
	}
	q := u.Query()
	r, err := http.NewRequestWithContext(ctx, "GET", q.Get("redirect_uri")+"?state="+url.QueryEscape(q.Get("state"))+"&code=synthetic", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(r)
	if err == nil {
		_ = resp.Body.Close()
	}
	return err
}

func TestLoginRetainsTransientTokenExpiry(t *testing.T) {
	for _, queued := range []bool{true, false} {
		name := "profile-response-does-not-extend-expiry"
		if queued {
			name = "expired-in-queue-fails-without-transmission"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			var advanced atomic.Bool
			var profiles, clockCalls atomic.Int32
			acquired := make(chan struct{}, 1)
			entered := make(chan struct{}, 8)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/labels":
					entered <- struct{}{}
					<-release
					respond(w, map[string]any{"labels": []any{}})
				case "/token":
					respond(w, map[string]any{"access_token": "login-access", "refresh_token": "login-refresh", "scope": granted, "expires_in": 1})
				case "/profile":
					profiles.Add(1)
					if r.Header.Get("Authorization") != "Bearer login-access" {
						t.Error("Login substituted stored identity")
					}
					advanced.Store(true)
					respond(w, map[string]string{"emailAddress": "new@example.test"})
				default:
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			defer unblock()
			store := validStore()
			old := store.token
			var watch atomic.Bool
			c := gmail.New(account, store, gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL, TokenURL: server.URL + "/token", ProfileURL: server.URL + "/profile", AuthURL: server.URL + "/auth", OpenBrowser: syntheticBrowser, Now: func() time.Time {
				value := now
				if advanced.Load() {
					value = value.Add(2 * time.Second)
				}
				if watch.Load() && clockCalls.Add(1) == 2 {
					acquired <- struct{}{}
				}
				return value
			}})
			reads := make(chan error, 8)
			if queued {
				for range 8 {
					go func() { _, err := c.Mailbox().Folders(ctx); reads <- err }()
				}
				for range 8 {
					select {
					case <-entered:
					case <-ctx.Done():
						t.Fatal("API slots not filled")
					}
				}
			}
			watch.Store(true)
			result := make(chan error, 1)
			go func() { _, _, err := c.Login(ctx, false, nil); result <- err }()
			if queued {
				select {
				case <-acquired:
				case <-ctx.Done():
					t.Fatal("Login did not obtain its transient token")
				}
				advanced.Store(true)
				unblock()
			}
			err := <-result
			if queued {
				if err == nil || profiles.Load() != 0 || store.saves != 0 || !reflect.DeepEqual(store.token, old) {
					t.Fatal("expired Login token reached profile or changed credentials", err)
				}
				for range 8 {
					if err := <-reads; err != nil {
						t.Fatal(err)
					}
				}
			} else if err != nil || profiles.Load() != 1 || store.saves != 1 || store.token.Username != "new@example.test" || !store.token.ExpiresAt.Equal(now.Add(time.Second)) {
				t.Fatal("profile response reset token acquisition expiry", err)
			}
		})
	}
}

func TestExpiredBackoffReleasesSlotsForLogin(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var advanced atomic.Bool
	var tokenCalls, stale atomic.Int32
	backoff := make(chan struct{}, 8)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	loginToken := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if tokenCalls.Add(1) == 2 {
				loginToken <- struct{}{}
			}
			respond(w, map[string]any{"access_token": "login-access", "refresh_token": "login-refresh", "scope": granted, "expires_in": 3600})
		case "/labels":
			if r.Header.Get("Authorization") == "Bearer access" {
				if advanced.Load() {
					stale.Add(1)
				}
				w.WriteHeader(429)
				respond(w, map[string]any{})
				return
			}
			respond(w, map[string]any{"labels": []any{}})
		case "/profile":
			if r.Header.Get("Authorization") != "Bearer login-access" {
				t.Error("profile lost Login identity")
			}
			respond(w, map[string]string{"emailAddress": "new@example.test"})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	defer unblock()
	store := validStore()
	store.token.ExpiresAt = now.Add(time.Second)
	c := gmail.New(account, store, gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL, ProfileURL: server.URL + "/profile", TokenURL: server.URL + "/token", AuthURL: server.URL + "/auth", OpenBrowser: syntheticBrowser, Now: func() time.Time {
		if advanced.Load() {
			return now.Add(2 * time.Second)
		}
		return now
	}, Sleep: func(ctx context.Context, _ time.Duration) error {
		select {
		case backoff <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	reads := make(chan error, 8)
	for range 8 {
		go func() { _, err := c.Mailbox().Folders(ctx); reads <- err }()
	}
	for range 8 {
		select {
		case <-backoff:
		case <-ctx.Done():
			t.Fatal("eight requests did not back off")
		}
	}
	login := make(chan error, 1)
	go func() { _, _, err := c.Login(ctx, false, nil); login <- err }()
	select {
	case <-loginToken: // Login owns tokenGate while profile needs an API slot.
	case <-ctx.Done():
		t.Fatal("Login did not reach its refresh")
	}
	advanced.Store(true)
	unblock()
	if err := <-login; err != nil {
		t.Fatal("Login blocked behind expired readers", err)
	}
	for range 8 {
		if err := <-reads; err != nil {
			t.Fatal("expired reader did not recover", err)
		}
	}
	if stale.Load() != 0 || tokenCalls.Load() != 2 {
		t.Fatal("expired transmission or redundant refresh", stale.Load(), tokenCalls.Load())
	}
}
