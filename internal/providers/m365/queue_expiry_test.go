package m365_test

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

	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/m365"
)

func TestQueuedRequestsRecheckExpiryBeforeTransmission(t *testing.T) {
	for _, operation := range []string{"read", "write"} {
		for _, outcome := range []string{"refresh", "unsafe", "cancel", "cancel-refresh", "rejected"} {
			t.Run(operation+"/"+outcome, func(t *testing.T) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				var advanced, watch atomic.Bool
				checked := make(chan struct{}, 1)
				started := make(chan struct{}, 4)
				release := make(chan struct{})
				var once sync.Once
				unblock := func() { once.Do(func() { close(release) }) }
				var refreshes, transmitted atomic.Int32
				refreshStarted := make(chan struct{})
				fresh := jwt(map[string]any{"scp": "User.Read Mail.ReadWrite", "version": "fresh"})
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch r.URL.Path {
					case "/token":
						refreshes.Add(1)
						_ = r.ParseForm()
						if outcome == "cancel-refresh" {
							close(refreshStarted)
							<-r.Context().Done()
							return
						}
						token := fresh
						if outcome == "unsafe" {
							token = "opaque"
						}
						jsonResponse(w, map[string]any{"access_token": token, "expires_in": 3600})
					case "/v1.0/me/messages/queued":
						transmitted.Add(1)
						if r.Header.Get("Authorization") != "Bearer "+fresh || outcome == "rejected" {
							w.WriteHeader(401)
							return
						}
						jsonResponse(w, map[string]string{"id": "queued"})
					default:
						started <- struct{}{}
						select {
						case <-release:
						case <-r.Context().Done():
							return
						}
						jsonResponse(w, map[string]string{"id": "blocked"})
					}
				}))
				defer server.Close()
				defer unblock()
				s := validStore()
				s.token.ExpiresAt = now.Add(time.Second)
				old := s.token
				c := m365.New(account(), s, m365.Options{HTTPClient: server.Client(), BaseURL: server.URL + "/v1.0", AuthorityURL: server.URL, Now: func() time.Time {
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
				blocked := make(chan error, 4)
				for range 4 {
					go func() { _, err := c.Mailbox().Get(ctx, []string{"blocked"}, false); blocked <- err }()
				}
				for range 4 {
					select {
					case <-started:
					case <-ctx.Done():
						t.Fatal("four API slots did not fill")
					}
				}
				watch.Store(true)
				queuedCtx, cancelQueued := context.WithCancel(ctx)
				defer cancelQueued()
				result := make(chan error, 1)
				go func() {
					if operation == "read" {
						_, err := c.Mailbox().Get(queuedCtx, []string{"queued"}, false)
						result <- err
						return
					}
					r, err := c.Mailbox().MarkRead(queuedCtx, []string{"queued"}, false)
					if err == nil && (len(r) != 1 || !r[0].OK) {
						err = errors.New("mutation failed")
					}
					result <- err
				}()
				select {
				case <-checked:
				case <-ctx.Done():
					t.Fatal("queued request did not resolve credential")
				}
				advanced.Store(true)
				if outcome == "cancel" {
					cancelQueued()
				} else {
					unblock()
				}
				if outcome == "cancel-refresh" {
					select {
					case <-refreshStarted:
						cancelQueued()
					case <-ctx.Done():
						t.Fatal("queued request did not start refresh")
					}
				}
				var err error
				select {
				case err = <-result:
				case <-ctx.Done():
					t.Fatal("queued request did not finish")
				}
				unblock()
				for range 4 {
					if err := <-blocked; err != nil {
						t.Fatal(err)
					}
				}
				if outcome == "refresh" || outcome == "rejected" {
					if (err == nil) != (outcome == "refresh") || refreshes.Load() != 1 || transmitted.Load() != 1 || s.saves != 1 {
						t.Fatalf("queued request: err=%v refreshes=%d transmissions=%d saves=%d", err, refreshes.Load(), transmitted.Load(), s.saves)
					}
				} else {
					wantRefreshes := int32(0)
					if outcome == "unsafe" || outcome == "cancel-refresh" {
						wantRefreshes = 1
					}
					if err == nil || refreshes.Load() != wantRefreshes || transmitted.Load() != 0 || s.saves != 0 || !reflect.DeepEqual(s.token, old) {
						t.Fatalf("refused request: err=%v refreshes=%d transmissions=%d saves=%d", err, refreshes.Load(), transmitted.Load(), s.saves)
					}
				}
			})
		}
	}
}
