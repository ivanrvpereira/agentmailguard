package cli_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/cli"
	"github.com/ivanrvpereira/agentmailguard-go/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard-go/internal/server"
)

// serveStore is a logged-in fake backend; key and missing control the startup checks.
type serveStore struct {
	credentials.Store
	mu      sync.Mutex
	key     string
	missing bool
	writes  int
}

func (s *serveStore) LoadOAuth(context.Context, string, string) (credentials.OAuth, error) {
	if s.missing {
		return credentials.OAuth{}, credentials.ErrNotFound
	}
	return credentials.OAuth{RefreshToken: "private-refresh-canary"}, nil
}
func (s *serveStore) SaveOAuth(context.Context, string, string, credentials.OAuth) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes++
	return nil
}
func (s *serveStore) LoadOpenRouterKey(context.Context) (string, error) {
	if s.key == "" {
		return "", credentials.ErrNotFound
	}
	return s.key, nil
}
func serveHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if e := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[accounts.work]\nprovider='m365'\nclient_id='synthetic'\ntenant_id='synthetic'\n"), 0600); e != nil {
		t.Fatal(e)
	}
	return home
}

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
		listen   string
		warning  bool
		loggedIn bool
	}{{"127.0.0.1:0", false, true}, {"0.0.0.0:0", true, true}, {"127.0.0.1:0", false, false}} {
		ctx, cancel := context.WithCancel(context.Background())
		logs := &notices{ready: make(chan struct{})}
		done := make(chan int, 1)
		home := serveHome(t)
		go func() {
			done <- cli.Run(ctx, []string{"serve", "--home", home, "--listen", tc.listen}, io.Discard, logs, cli.Options{LookupEnv: func(string) string { return "" }, Server: server.Options{Credentials: &serveStore{key: "private-jev-canary", missing: !tc.loggedIn}}})
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
		account := "ok"
		if !tc.loggedIn {
			// An account that is not logged in does not block the start; its commands fail.
			account = "not logged in; commands for this account fail until auth login and a server restart"
		}
		checks := "checking credentials; approve any Keychain prompts now\ncredential check: account \"work\" (m365): " + account + "\ncredential check: Jev key: ok\nserver listening\n"
		if !strings.HasSuffix(text, checks) || strings.Contains(text, "private-") {
			t.Errorf("credential checks must precede listening: %q", text)
		}
		if strings.Contains(text, "unauthenticated mailbox access") != tc.warning || tc.warning && (!strings.Contains(text, "no built-in TLS") || !strings.Contains(text, "tunnel")) {
			t.Errorf("exposure warning mismatch: %s", text)
		}
	}
}

func TestServeRefusesToStartWhenCredentialsAreMissing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		home  func(*testing.T) string
		store *serveStore
		want  []string
	}{
		// A piped or absent stdin never gets a key prompt; automated hosts use OPENROUTER_API_KEY.
		{"key missing without terminal", serveHome, &serveStore{}, []string{"credential check: Jev key: missing; run auth set-jev-key or set OPENROUTER_API_KEY", "error: credential check failed"}},
		{"no configuration", func(t *testing.T) string { return t.TempDir() }, &serveStore{key: "k"}, []string{"error: configuration unavailable"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stderr strings.Builder
			code := cli.Run(context.Background(), []string{"serve", "--home", tc.home(t), "--listen", "127.0.0.1:0"}, io.Discard, &stderr, cli.Options{Stdin: strings.NewReader("sk-piped\n"), LookupEnv: func(string) string { return "" }, Server: server.Options{Credentials: tc.store}})
			text := stderr.String()
			if code != 1 || strings.Contains(text, "server listening") || strings.Contains(text, "private-") {
				t.Fatalf("exit=%d stderr=%q", code, text)
			}
			for _, w := range tc.want {
				if !strings.Contains(text, w) {
					t.Fatalf("missing %q in %q", w, text)
				}
			}
		})
	}
}
