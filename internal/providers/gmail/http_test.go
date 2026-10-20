package gmail_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGETRetriesBoundedButWritesNeverRetry(t *testing.T) {
	for _, mode := range []string{"read", "archive", "trash", "draft"} {
		t.Run(mode, func(t *testing.T) {
			attempts := 0
			sleeps := []time.Duration{}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				w.Header().Set("Retry-After", "90")
				status := 503
				if attempts == 2 {
					status = 403
				}
				if attempts == 3 {
					status = 429
				}
				w.WriteHeader(status)
				respond(w, map[string]any{"error": map[string]any{"message": "SECRET access refresh", "errors": []any{map[string]string{"reason": "rateLimitExceeded"}}}})
			}))
			defer server.Close()
			c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL, Sleep: func(ctx context.Context, d time.Duration) error { sleeps = append(sleeps, d); return ctx.Err() }})
			var err error
			switch mode {
			case "read":
				_, err = c.Mailbox().Folders(context.Background())
			case "archive":
				result, e := c.Mailbox().Archive(context.Background(), []string{"m"})
				err = e
				if err == nil && result[0].Error != nil {
					err = errors.New(*result[0].Error)
				}
			case "trash":
				result, e := c.Mailbox().Trash(context.Background(), []string{"m"})
				err = e
				if err == nil && result[0].Error != nil {
					err = errors.New(*result[0].Error)
				}
			case "draft":
				_, err = c.Mailbox().DraftNew(context.Background(), model.NewDraft{Body: "x"})
			}
			if err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "access") {
				t.Fatal("unsafe API failure", err)
			}
			if mode == "read" {
				if attempts != 7 || !reflect.DeepEqual(sleeps, []time.Duration{32 * time.Second, 32 * time.Second, 32 * time.Second, 32 * time.Second, 32 * time.Second, 32 * time.Second}) {
					t.Fatal("retry bound", attempts, sleeps)
				}
			} else if attempts != 1 || len(sleeps) != 0 {
				t.Fatal("write retried")
			}
		})
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestTransportReplayDisabledAndErrorsRedacted(t *testing.T) {
	calls := 0
	httpClient := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != "POST" || r.GetBody != nil {
			t.Error("mutation replay enabled")
		}
		return nil, errors.New("SECRET access refresh")
	})}
	c := gmail.New(account, validStore(), gmail.Options{HTTPClient: httpClient, GmailURL: "https://synthetic.invalid"})
	_, err := c.Mailbox().DraftNew(context.Background(), model.NewDraft{Body: "x"})
	if err == nil || err.Error() != "Gmail error RequestFailed" || calls != 1 {
		t.Fatal("transport failure", err, calls)
	}
}
func TestCredentialRedirectAndHostileResponsesRefused(t *testing.T) {
	redirected := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected++; respond(w, map[string]any{}) }))
	defer target.Close()
	for _, mode := range []string{"redirect", "array", "null", "oversized", "reason"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "redirect":
					http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
				case "array":
					_, _ = io.WriteString(w, `["SECRET"]`)
				case "null":
					_, _ = io.WriteString(w, `null`)
				case "oversized":
					_, _ = io.WriteString(w, `{"labels":[],"padding":"`)
					chunk := strings.Repeat("x", 1<<20)
					for range 51 {
						if _, err := io.WriteString(w, chunk); err != nil {
							return
						}
					}
					_, _ = io.WriteString(w, `"}`)
				case "reason":
					w.WriteHeader(400)
					respond(w, map[string]any{"error": map[string]any{"message": "SECRET", "errors": []any{map[string]string{"reason": "SECRET with spaces"}}}})
				}
			}))
			defer server.Close()
			c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL})
			_, err := c.Mailbox().Folders(context.Background())
			if err == nil || strings.Contains(err.Error(), "SECRET") {
				t.Fatal("hostile response accepted/leaked", err)
			}
		})
	}
	if redirected != 0 {
		t.Fatal("credential redirect followed")
	}
}
func TestMailboxCalendarShareEightSlotsIncludingBackoff(t *testing.T) {
	var first atomic.Int32
	blocked := make(chan struct{}, 32)
	release := make(chan struct{})
	var peak atomic.Int32
	var active atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if first.Add(1) <= 8 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
			respond(w, map[string]any{})
			return
		}
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		if strings.HasPrefix(r.URL.Path, "/events/") {
			e := calendarEvent()
			e["id"] = strings.TrimPrefix(r.URL.Path, "/events/")
			respond(w, e)
		} else {
			respond(w, map[string]any{"labels": []any{}})
		}
	}))
	defer server.Close()
	c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL, CalendarURL: server.URL, Sleep: func(ctx context.Context, d time.Duration) error {
		blocked <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := range 16 {
		wg.Go(func() {
			var err error
			if i%2 == 0 {
				_, err = c.Mailbox().Folders(ctx)
			} else {
				_, err = c.Calendar().Get(ctx, "ev")
			}
			errs <- err
		})
	}
	for range 8 {
		select {
		case <-blocked:
		case <-ctx.Done():
			t.Fatal("did not fill shared slots")
		}
	}
	// The ninth request must remain queued while all eight slots back off.
	if first.Load() != 8 {
		t.Error("backoff released a slot", first.Load())
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if peak.Load() > 8 {
		t.Fatal("exceeded concurrency")
	}
}
func TestExpiredTokenRefreshSharedByConcurrentCalls(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	store := validStore()
	store.token.ExpiresAt = now.Add(time.Second)
	var refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			refreshes.Add(1)
			respond(w, map[string]any{"access_token": "renewed", "expires_in": 3600, "scope": granted})
			return
		}
		if r.Header.Get("Authorization") != "Bearer renewed" {
			t.Error("expired access used")
		}
		respond(w, map[string]any{"labels": []any{}})
	}))
	defer server.Close()
	c := gmail.New(account, store, gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL, TokenURL: server.URL + "/token", Now: func() time.Time { return now.Add(2 * time.Second) }})
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			if _, err := c.Mailbox().Folders(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if refreshes.Load() != 1 || store.saves != 1 || store.loads != 1 {
		t.Fatal("refresh not synchronized", refreshes.Load(), store.saves, store.loads)
	}
}
func TestWaitingForSharedRefreshHonorsCancellation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			close(started)
			<-release
			respond(w, map[string]any{"access_token": "renewed", "expires_in": 3600, "scope": granted})
			return
		}
		respond(w, map[string]any{"labels": []any{}})
	}))
	defer server.Close()
	defer close(release)
	store := validStore()
	store.token.ExpiresAt = time.Time{}
	c := gmail.New(account, store, gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL, TokenURL: server.URL + "/token"})
	first, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _, _ = c.Mailbox().Folders(first) }()
	<-started
	ctx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer stop()
	done := make(chan error, 1)
	go func() { _, err := c.Mailbox().Folders(ctx); done <- err }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("refresh wait ignored cancellation", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("refresh wait blocked beyond caller deadline")
	}
}

func TestBackoffCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
		_ = json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer server.Close()
	c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.Mailbox().Folders(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("backoff ignored cancellation", err)
	}
}
