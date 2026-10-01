package m365_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
)

// The external socket fails before the write on reuse, which normally permits
// net/http to replay a request when GetBody is available.
type staleConn struct {
	net.Conn
	writes int
}

func (c *staleConn) Write(p []byte) (int, error) {
	c.writes++
	if c.writes > 1 {
		return 0, io.ErrClosedPipe
	}
	return c.Conn.Write(p)
}
func staleTransport() *http.Transport {
	return &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &staleConn{Conn: conn}, nil
	}}
}
func TestWritesNeverReplayOnStaleKeepAlive(t *testing.T) {
	for _, operation := range []string{"move", "patch", "draft", "refresh"} {
		t.Run(operation, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				_, _ = io.Copy(io.Discard, r.Body)
				if operation == "refresh" {
					jsonResponse(w, map[string]any{"access_token": goodToken(), "expires_in": 1})
				} else {
					jsonResponse(w, map[string]string{"id": "m"})
				}
			}))
			defer server.Close()
			tr := staleTransport()
			defer tr.CloseIdleConnections()
			s := validStore()
			clock := now
			if operation == "refresh" {
				s.token.ExpiresAt = now.Add(-time.Second)
			}
			c := m365.New(account(), s, m365.Options{HTTPClient: &http.Client{Transport: tr}, BaseURL: server.URL + "/v1.0", AuthorityURL: server.URL, Now: func() time.Time { return clock }})
			if operation == "refresh" {
				if _, _, err := c.Status(context.Background()); err != nil {
					t.Fatal(err)
				}
				clock = now.Add(2 * time.Second)
				if _, _, err := c.Status(context.Background()); err == nil {
					t.Fatal("refresh replayed")
				}
				if s.saves != 1 {
					t.Fatal("failed refresh saved")
				}
			} else {
				if _, err := c.Mailbox().Get(context.Background(), []string{"m"}, false); err != nil {
					t.Fatal(err)
				}
				switch operation {
				case "move":
					r, err := c.Mailbox().Archive(context.Background(), []string{"m"})
					if err != nil || r[0].OK {
						t.Fatal("move replayed")
					}
				case "patch":
					r, err := c.Mailbox().MarkRead(context.Background(), []string{"m"}, true)
					if err != nil || r[0].OK {
						t.Fatal("PATCH replayed")
					}
				case "draft":
					if _, err := c.Mailbox().DraftNew(context.Background(), model.NewDraft{Body: "draft"}); err == nil {
						t.Fatal("draft creation replayed")
					}
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("write retried: %d requests", calls.Load())
			}
		})
	}
}
func TestRedirectsNeverCarryProviderSecrets(t *testing.T) {
	for _, status := range []int{301, 302, 303, 307, 308} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var leaked atomic.Int64
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
			defer target.Close()
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, status) }))
			defer origin.Close()
			client := origin.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return nil }
			s := validStore()
			c := m365.New(account(), s, m365.Options{HTTPClient: client, BaseURL: origin.URL + "/v1.0", AuthorityURL: origin.URL, Now: func() time.Time { return now }})
			if _, err := c.Mailbox().Get(context.Background(), []string{"m"}, false); err == nil {
				t.Fatal("Graph redirect accepted")
			}
			if _, _, err := c.Login(context.Background(), true, func(string, string) {}); err == nil {
				t.Fatal("OAuth redirect accepted")
			}
			if leaked.Load() != 0 {
				t.Fatal("credential-bearing redirect followed")
			}
		})
	}
}
func TestMailboxAndCalendarShareFourRequestLimit(t *testing.T) {
	var active, peak atomic.Int32
	started := make(chan struct{}, 20)
	release := make(chan struct{})
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		if strings.Contains(r.URL.Path, "/events/") {
			jsonResponse(w, event())
		} else {
			jsonResponse(w, map[string]string{"id": "m"})
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := c.Mailbox().Get(ctx, []string{"m"}, false); err != nil {
				t.Error(err)
			}
		})
		wg.Go(func() {
			if _, err := c.Calendar().Get(ctx, "e"); err != nil {
				t.Error(err)
			}
		})
	}
	for range 4 {
		select {
		case <-started:
		case <-ctx.Done():
			close(release)
			wg.Wait()
			t.Fatal("four requests did not start")
		}
	}
	select {
	case <-started:
		t.Error("fifth concurrent request")
	case <-time.After(30 * time.Millisecond):
	}
	close(release)
	wg.Wait()
	if peak.Load() != 4 {
		t.Fatalf("shared limit %d", peak.Load())
	}
}

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestInvalidHTTPStatusCannotEscapeSafeFailureContract(t *testing.T) {
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(777); _, _ = w.Write([]byte("SECRET")) })
	results, err := c.Mailbox().MarkRead(context.Background(), []string{"m"}, true)
	if err != nil || len(results) != 1 || results[0].OK || results[0].Error == nil {
		t.Fatal("missing failure")
	}
	if err := model.ValidateFailureText(results[0].Error); err != nil {
		t.Fatalf("unsafe failure text %q", *results[0].Error)
	}
}
func TestRequestTimeoutCancellationAndSafeNetworkErrors(t *testing.T) {
	transport := transportFunc(func(r *http.Request) (*http.Response, error) {
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > 30*time.Second {
			t.Error("request not bounded")
		}
		return nil, errors.New("SECRET network body")
	})
	c := m365.New(account(), validStore(), m365.Options{HTTPClient: &http.Client{Transport: transport}, BaseURL: "http://synthetic.invalid/v1.0", Now: func() time.Time { return now }})
	if _, err := c.Mailbox().Get(context.Background(), []string{"m"}, true); err == nil || err.Error() != "Graph error RequestFailed" {
		t.Fatalf("unsafe network error %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Calendar().Get(ctx, "e"); err == nil {
		t.Fatal("cancellation ignored")
	}
}
