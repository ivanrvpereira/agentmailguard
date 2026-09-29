package cli_test

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/cli"
	"github.com/ivanrvpereira/agentmailguard-go/internal/server"
)

type notices struct {
	mu    sync.Mutex
	text  strings.Builder
	ready chan struct{}
	once  sync.Once
}

func (n *notices) Write(p []byte) (int, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.text.Write(p)
	if strings.Contains(n.text.String(), "server listening") {
		n.once.Do(func() { close(n.ready) })
	}
	return len(p), nil
}
func TestServeExposureWarningAndBoundedShutdown(t *testing.T) {
	for _, tc := range []struct {
		listen  string
		warning bool
	}{{"127.0.0.1:0", false}, {"0.0.0.0:0", true}} {
		ctx, cancel := context.WithCancel(context.Background())
		logs := &notices{ready: make(chan struct{})}
		done := make(chan int, 1)
		home := t.TempDir()
		go func() {
			done <- cli.Run(ctx, []string{"serve", "--home", home, "--listen", tc.listen}, io.Discard, logs, cli.Options{LookupEnv: func(string) string { return "" }, Server: server.Options{Credentials: &loginStore{}}})
		}()
		select {
		case <-logs.ready:
		case <-time.After(3 * time.Second):
			cancel()
			t.Fatal("listener did not start")
		}
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("shutdown exit %d", code)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("idle server did not stop")
		}
		logs.mu.Lock()
		text := logs.text.String()
		logs.mu.Unlock()
		if strings.Contains(text, "unauthenticated mailbox access") != tc.warning || tc.warning && (!strings.Contains(text, "no built-in TLS") || !strings.Contains(text, "tunnel")) {
			t.Errorf("exposure warning mismatch: %s", text)
		}
	}
}
