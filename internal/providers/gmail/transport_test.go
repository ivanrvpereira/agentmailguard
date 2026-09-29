package gmail_test

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/gmail"
)

type staleWriteConn struct {
	net.Conn
	fail     *atomic.Bool
	attempts *atomic.Int32
}

func (c *staleWriteConn) Write(p []byte) (int, error) {
	if c.fail.CompareAndSwap(true, false) {
		c.attempts.Add(1)
		return 0, errors.New("synthetic stale connection")
	}
	return c.Conn.Write(p)
}

func TestEmptyTrashDoesNotReplayOnStaleConnection(t *testing.T) {
	for _, stale := range []bool{false, true} {
		name := "ordinary"
		if stale {
			name = "failed-first-write"
		}
		t.Run(name, func(t *testing.T) {
			var fail atomic.Bool
			var failed, dials, posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					posts.Add(1)
					data, err := io.ReadAll(r.Body)
					if err != nil || len(data) != 0 || r.URL.Path != "/messages/m/trash" {
						t.Error("Trash must retain an empty decoded request body")
					}
				}
				respond(w, map[string]any{"labels": []any{}, "id": "m"})
			}))
			defer server.Close()
			transport := &http.Transport{DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				dials.Add(1)
				conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
				if err != nil {
					return nil, err
				}
				return &staleWriteConn{conn, &fail, &failed}, nil
			}}
			defer transport.CloseIdleConnections()
			c := gmail.New(account, validStore(), gmail.Options{HTTPClient: &http.Client{Transport: transport}, GmailURL: server.URL})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if _, err := c.Mailbox().Folders(ctx); err != nil {
				t.Fatal(err)
			}
			fail.Store(stale)
			got, err := c.Mailbox().Trash(ctx, []string{"m"})
			t.Logf("failed writes=%d connections=%d successful POSTs=%d", failed.Load(), dials.Load(), posts.Load())
			if err != nil || len(got) != 1 {
				t.Fatal("unexpected batch result", err)
			}
			if stale {
				if failed.Load() != 1 || dials.Load() != 1 || posts.Load() != 0 || got[0].OK || got[0].Error == nil {
					t.Fatal("failed bodyless mutation replayed or reported success")
				}
			} else if dials.Load() != 1 || posts.Load() != 1 || !got[0].OK {
				t.Fatal("ordinary empty Trash failed or keep-alive disabled")
			}
		})
	}
}
