package gmail_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
)

func TestQueuedStatusRefreshesBeforeTransmission(t *testing.T) {
	testQueuedExpiry(t, "status", granted)
}

func TestQueuedMailReadRefreshesBeforeTransmission(t *testing.T) {
	testQueuedExpiry(t, "read", granted)
}

func TestQueuedMutationRefreshesBeforeTransmission(t *testing.T) {
	testQueuedExpiry(t, "trash", granted)
}

func TestQueuedMutationRefusesUnvalidatedRefreshWithoutWrite(t *testing.T) {
	for _, scope := range []string{"", "https://www.googleapis.com/auth/gmail.modify", "HTTP401"} {
		t.Run(scope, func(t *testing.T) { testQueuedExpiry(t, "trash", scope) })
	}
}

func TestMutationNeverRefreshesOrRetriesAfterTransmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var advanced atomic.Bool
	var writes, refreshes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			refreshes.Add(1)
			respond(w, map[string]any{"access_token": "fresh", "scope": granted, "expires_in": 3600})
			return
		}
		writes.Add(1)
		advanced.Store(true)
		w.WriteHeader(401)
		respond(w, map[string]any{})
	}))
	defer server.Close()
	store := validStore()
	store.token.ExpiresAt = now.Add(time.Second)
	c := gmail.New(account, store, gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL, TokenURL: server.URL + "/token", Now: func() time.Time {
		if advanced.Load() {
			return now.Add(2 * time.Second)
		}
		return now
	}})
	results, err := c.Mailbox().Trash(ctx, []string{"m"})
	if err != nil || len(results) != 1 || results[0].OK || writes.Load() != 1 || refreshes.Load() != 0 {
		t.Fatal("transmitted mutation was retried/refreshed or accepted", err, writes.Load(), refreshes.Load())
	}
}

func testQueuedExpiry(t *testing.T, operation, scope string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var advanced, watch atomic.Bool
	checked := make(chan struct{}, 1)
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var refreshes, stale, labels, writes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			refreshes.Add(1)
			if scope == "HTTP401" {
				w.WriteHeader(401)
			}
			respond(w, map[string]any{"access_token": "fresh", "scope": scope, "expires_in": 3600})
			return
		}
		if r.URL.Path == "/labels" && labels.Add(1) <= 8 {
			started <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			respond(w, map[string]any{"labels": []any{}})
			return
		}
		if r.Method != "GET" {
			writes.Add(1)
		}
		if r.Header.Get("Authorization") != "Bearer fresh" {
			stale.Add(1)
			w.WriteHeader(401)
			respond(w, map[string]any{})
			return
		}
		switch r.URL.Path {
		case "/profile":
			respond(w, map[string]string{"emailAddress": "synthetic@example.test"})
		case "/labels":
			respond(w, map[string]any{"labels": []any{}})
		case "/messages/m":
			respond(w, message("m", part("text/plain", "synthetic body")))
		case "/messages/m/trash":
			respond(w, map[string]string{"id": "m"})
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	defer unblock()
	store := validStore()
	store.token.ExpiresAt = now.Add(time.Second)
	old := store.token
	client := gmail.New(account, store, gmail.Options{HTTPClient: server.Client(), GmailURL: server.URL, ProfileURL: server.URL + "/profile", TokenURL: server.URL + "/token", Now: func() time.Time {
		value := now
		if advanced.Load() {
			value = value.Add(2 * time.Second)
		}
		if watch.Load() {
			select {
			case checked <- struct{}{}:
			default:
			}
		}
		return value
	}})
	reads := make(chan error, 8)
	for range 8 {
		go func() { _, err := client.Mailbox().Folders(ctx); reads <- err }()
	}
	for range 8 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("eight requests not admitted")
		}
	}
	watch.Store(true)
	result := make(chan error, 1)
	go func() {
		var err error
		switch operation {
		case "status":
			_, _, err = client.Status(ctx)
		case "read":
			messages, e := client.Mailbox().Get(ctx, []string{"m"}, true)
			err = e
			if err == nil && (len(messages) != 1 || messages[0].Body == nil || *messages[0].Body != "synthetic body") {
				err = errors.New("mail read lost body")
			}
		case "trash":
			results, e := client.Mailbox().Trash(ctx, []string{"m"})
			err = e
			if err == nil && !results[0].OK {
				err = errors.New(*results[0].Error)
			}
		}
		result <- err
	}()
	select {
	case <-checked:
	case <-ctx.Done():
		t.Fatal("queued Status did not select credentials")
	}
	advanced.Store(true)
	unblock()
	err := <-result
	if (err != nil) != (scope != granted) || refreshes.Load() != 1 || stale.Load() != 0 {
		t.Fatalf("queued %s: error=%v refreshes=%d expired transmissions=%d", operation, err, refreshes.Load(), stale.Load())
	}
	if operation == "trash" {
		want := int32(1)
		if scope != granted {
			want = 0
		}
		if writes.Load() != want {
			t.Fatalf("provider writes=%d, want %d", writes.Load(), want)
		}
	}
	for range 8 {
		if err := <-reads; err != nil {
			t.Fatal(err)
		}
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if scope != granted && (store.saves != 0 || !reflect.DeepEqual(store.token, old)) {
		t.Fatal("refused refresh changed saved credentials")
	}
}
