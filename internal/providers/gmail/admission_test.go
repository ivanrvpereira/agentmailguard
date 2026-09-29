package gmail_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/gmail"
)

func TestStatusSharesEightAPISlots(t *testing.T) {
	for _, cancelQueued := range []bool{false, true} {
		name := "waits-for-slot"
		if cancelQueued {
			name = "queued-cancellation"
		}
		t.Run(name, func(t *testing.T) {
			entered := make(chan struct{}, 8)
			profile := make(chan struct{}, 1)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/profile" {
					profile <- struct{}{}
					respond(w, map[string]string{"emailAddress": "synthetic@example.test"})
					return
				}
				entered <- struct{}{}
				<-release
				respond(w, map[string]any{"labels": []any{}})
			}))
			defer server.Close()
			defer unblock()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL, ProfileURL: server.URL + "/profile"})
			reads := make(chan error, 8)
			for range 8 {
				go func() { _, err := c.Mailbox().Folders(ctx); reads <- err }()
			}
			for range 8 {
				select {
				case <-entered:
				case <-ctx.Done():
					t.Fatal("did not fill API slots")
				}
			}
			statusCtx, stop := context.WithCancel(ctx)
			defer stop()
			done := make(chan error, 1)
			go func() { _, _, err := c.Status(statusCtx); done <- err }()
			select {
			case <-profile:
				t.Fatal("Status issued a ninth simultaneous API request")
			case <-time.After(100 * time.Millisecond):
			}
			if cancelQueued {
				stop()
			} else {
				unblock()
			}
			select {
			case err := <-done:
				if cancelQueued && !errors.Is(err, context.Canceled) || !cancelQueued && err != nil {
					t.Fatal("unexpected Status result", err)
				}
			case <-ctx.Done():
				t.Fatal("Status admission ignored cancellation or slot release")
			}
			unblock()
			for range 8 {
				if err := <-reads; err != nil {
					t.Fatal(err)
				}
			}
			if cancelQueued && len(profile) != 0 {
				t.Fatal("cancelled queued Status reached HTTP")
			}
		})
	}
}

func TestStatusRetriesRefreshExpiredTokenWithoutConsumingAttempt(t *testing.T) {
	initial := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var now atomic.Int64
	now.Store(initial.Unix())
	store := validStore()
	store.token.ExpiresAt = initial.Add(time.Second)
	var profiles, refreshes, sleeps atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			refreshes.Add(1)
			respond(w, map[string]any{"access_token": "renewed", "scope": granted, "expires_in": 3600})
			return
		}
		n := profiles.Add(1)
		want := "Bearer access"
		if n > 1 {
			want = "Bearer renewed"
		}
		if r.Header.Get("Authorization") != want {
			t.Error("profile retry transmitted an expired token")
		}
		if n <= 7 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			respond(w, map[string]any{})
			return
		}
		respond(w, map[string]string{"emailAddress": "synthetic@example.test"})
	}))
	defer server.Close()
	c := gmail.New(account, store, gmail.Options{HTTPClient: server.Client(), TokenURL: server.URL + "/token", ProfileURL: server.URL + "/profile", Now: func() time.Time {
		return time.Unix(now.Load(), 0)
	}, Sleep: func(ctx context.Context, _ time.Duration) error {
		sleeps.Add(1)
		now.Store(initial.Add(2 * time.Second).Unix())
		return ctx.Err()
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, _, err := c.Status(ctx); err == nil || profiles.Load() != 7 || sleeps.Load() != 6 || refreshes.Load() != 1 {
		t.Fatal("profile retry bound or pre-transmission refresh violated")
	}
	if name, _, err := c.Status(ctx); err != nil || name != "synthetic@example.test" || profiles.Load() != 8 || refreshes.Load() != 1 {
		t.Fatal("following Status did not reuse fresh token and recover", err)
	}
}

func TestLoginProfileAndConcurrentReadsDoNotDeadlock(t *testing.T) {
	entered := make(chan struct{}, 8)
	browser := make(chan string, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/labels":
			if reads.Add(1) <= 8 {
				entered <- struct{}{}
				<-release
			} else if r.Header.Get("Authorization") != "Bearer new-access" {
				t.Error("queued read did not use completed login")
			}
			respond(w, map[string]any{"labels": []any{}})
		case "/token":
			respond(w, map[string]any{"access_token": "new-access", "refresh_token": "new-refresh", "scope": granted, "expires_in": 3600})
		case "/profile":
			if r.Header.Get("Authorization") != "Bearer new-access" {
				t.Error("login profile did not use its supplied new token")
			}
			respond(w, map[string]string{"emailAddress": "new@example.test"})
		default:
			t.Error("unexpected request")
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	defer unblock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	store := validStore()
	c := gmail.New(account, store, gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL, TokenURL: server.URL + "/token", ProfileURL: server.URL + "/profile", AuthURL: server.URL + "/auth", OpenBrowser: func(_ context.Context, uri string) error {
		browser <- uri
		return nil
	}})
	results := make(chan error, 16)
	for range 8 {
		go func() { _, err := c.Mailbox().Folders(ctx); results <- err }()
	}
	for range 8 {
		select {
		case <-entered:
		case <-ctx.Done():
			t.Fatal("did not fill API slots")
		}
	}
	login := make(chan error, 1)
	go func() { _, _, err := c.Login(ctx, false, nil); login <- err }()
	var uri string
	select {
	case uri = <-browser: // Login owns the token gate until profile and storage complete.
	case <-ctx.Done():
		t.Fatal("login did not open synthetic browser")
	}
	for range 8 {
		go func() { _, err := c.Mailbox().Folders(ctx); results <- err }()
	}
	unblock()
	for range 8 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	// Give queued readers a chance to occupy slots if admission precedes credentials.
	time.Sleep(100 * time.Millisecond)
	parsed, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	req, err := http.NewRequestWithContext(ctx, "GET", q.Get("redirect_uri")+"?state="+url.QueryEscape(q.Get("state"))+"&code=synthetic-code", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	select {
	case err := <-login:
		if err != nil {
			t.Fatal("login deadlocked or failed", err)
		}
	case <-ctx.Done():
		t.Fatal("login/profile deadlocked with queued reads")
	}
	for range 8 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.saves != 1 || store.token.Username != "new@example.test" || reads.Load() != 16 {
		t.Fatal("login/read completion not preserved")
	}
}
