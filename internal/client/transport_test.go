package client_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/ivanrvpereira/agentmailguard-go/internal/client"
)

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
func TestMutationNotReplayedAfterStaleConnection(t *testing.T) {
	var calls, dials atomic.Int32
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.Copy(io.Discard, r.Body)
		io.WriteString(w, `[]`)
	}))
	defer h.Close()
	tr := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		dials.Add(1)
		conn, e := (&net.Dialer{}).DialContext(ctx, network, address)
		if e != nil {
			return nil, e
		}
		return &staleConn{Conn: conn}, nil
	}}
	defer tr.CloseIdleConnections()
	c, e := client.New(h.URL, client.Options{HTTPClient: &http.Client{Transport: tr}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := c.Call(context.Background(), "mail.folders", "", nil); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Call(context.Background(), "mail.archive", "", map[string]any{"ids": []string{"x"}}); e == nil {
		t.Fatal("mutation replayed")
	}
	if calls.Load() != 1 || dials.Load() != 1 {
		t.Fatalf("replay: calls %d dials %d", calls.Load(), dials.Load())
	}
}
