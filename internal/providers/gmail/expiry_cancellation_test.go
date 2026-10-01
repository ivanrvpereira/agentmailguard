package gmail_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
)

type cancelAfterSaveStore struct {
	*memoryStore
	cancel context.CancelFunc
}

func (s *cancelAfterSaveStore) SaveOAuth(ctx context.Context, provider, account string, token credentials.OAuth) error {
	err := s.memoryStore.SaveOAuth(ctx, provider, account, token)
	s.cancel()
	return err
}

func TestExpiredBackoffCancellationDuringRefreshAndReadmission(t *testing.T) {
	for _, phase := range []string{"token-exchange", "readmission-after-save"} {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			var advanced atomic.Bool
			var profiles, refreshes atomic.Int32
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					refreshes.Add(1)
					if phase == "token-exchange" {
						cancel()
						<-release
						return
					}
					respond(w, map[string]any{"access_token": "fresh", "scope": granted, "expires_in": 3600})
					return
				}
				profiles.Add(1)
				w.WriteHeader(429)
				respond(w, map[string]any{})
			}))
			defer server.Close()
			defer close(release)
			memory := validStore()
			memory.token.ExpiresAt = now.Add(time.Second)
			var store credentials.Store = memory
			if phase == "readmission-after-save" {
				store = &cancelAfterSaveStore{memory, cancel}
			}
			c := gmail.New(account, store, gmail.Options{HTTPClient: server.Client(), ProfileURL: server.URL + "/profile", TokenURL: server.URL + "/token", Now: func() time.Time {
				if advanced.Load() {
					return now.Add(2 * time.Second)
				}
				return now
			}, Sleep: func(ctx context.Context, _ time.Duration) error {
				advanced.Store(true)
				return ctx.Err()
			}})
			_, _, err := c.Status(ctx)
			if !errors.Is(err, context.Canceled) || profiles.Load() != 1 || refreshes.Load() != 1 {
				t.Fatalf("cancellation at %s: error=%v profiles=%d refreshes=%d", phase, err, profiles.Load(), refreshes.Load())
			}
		})
	}
}
