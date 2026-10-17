package cli_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/cli"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
	"golang.org/x/crypto/bcrypt"
)

// consoleStore is a credential record with a screening key and, when password is true, an approval password.
func consoleStore(t *testing.T, password bool) credentials.Store {
	t.Helper()
	store := approvalStore(t)
	if err := store.SaveScreeningKey(context.Background(), "private-jev-canary"); err != nil {
		t.Fatal(err)
	}
	if password {
		hash, err := bcrypt.GenerateFromPassword([]byte("private-console-pass"), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.SaveApprovalHash(context.Background(), string(hash)); err != nil {
			t.Fatal(err)
		}
	}
	return store
}

// freeAddress returns a loopback address that nothing listens on.
func freeAddress(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	l.Close()
	return address
}

// consoleHome is a serve home whose approval console listens on listen.
func consoleHome(t *testing.T, listen string) string {
	t.Helper()
	home := serveHome(t)
	f, err := os.OpenFile(filepath.Join(home, "config.toml"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString("[approvals]\nlisten='" + listen + "'\n"); err != nil {
		t.Fatal(err)
	}
	return home
}

// serveBound is how long a test waits for serve to start or stop before it fails. Parallel race runs are slow.
const serveBound = 30 * time.Second

// startServe runs serve until the test ends and returns its log once the server listens.
func startServe(t *testing.T, home string, store credentials.Store) *notices {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	logs := &notices{ready: make(chan struct{})}
	done := make(chan int, 1)
	go func() {
		done <- cli.Run(ctx, []string{"serve", "--home", home, "--listen", "127.0.0.1:0"}, io.Discard, logs, cli.Options{LookupEnv: func(string) string { return "" }, Server: server.Options{Credentials: store}})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case code := <-done:
			if code != 0 {
				t.Errorf("shutdown exit %d", code)
			}
		case <-time.After(serveBound):
			t.Error("server did not stop")
		}
	})
	select {
	case <-logs.ready:
	case code := <-done:
		t.Fatalf("serve exit %d: %s", code, logs.text.String())
	case <-time.After(serveBound):
		t.Fatal("listener did not start")
	}
	return logs
}

func consoleGet(t *testing.T, address string, password string) (int, error) {
	t.Helper()
	r, err := http.NewRequest("GET", "http://"+address+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if password != "" {
		r.SetBasicAuth("me", password)
	}
	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return 0, err
	}
	resp.Body.Close()
	return resp.StatusCode, nil
}

func TestServeStartsTheApprovalConsoleWhenAPasswordIsSet(t *testing.T) {
	address := freeAddress(t)
	logs := startServe(t, consoleHome(t, address), consoleStore(t, true))
	if code, err := consoleGet(t, address, ""); err != nil || code != 401 {
		t.Fatalf("without credentials: %d %v", code, err)
	}
	if code, err := consoleGet(t, address, "private-console-pass"); err != nil || code != 200 {
		t.Fatalf("signed in: %d %v", code, err)
	}
	logs.mu.Lock()
	text := logs.text.String()
	logs.mu.Unlock()
	if !strings.Contains(text, "credential check: approval password: ok\napproval console listening\nserver listening\n") || strings.Contains(text, "WARNING") || strings.Contains(text, "private-") {
		t.Fatalf("log=%q", text)
	}
}

func TestServeLeavesTheApprovalConsoleOffWithoutAPassword(t *testing.T) {
	address := freeAddress(t)
	logs := startServe(t, consoleHome(t, address), consoleStore(t, false))
	if _, err := consoleGet(t, address, "private-console-pass"); err == nil {
		t.Fatal("console answers without a password")
	}
	logs.mu.Lock()
	defer logs.mu.Unlock()
	if strings.Contains(logs.text.String(), "approval console listening") {
		t.Fatalf("log=%q", logs.text.String())
	}
}

func TestServeWarnsWhenTheApprovalConsoleListensBeyondLoopback(t *testing.T) {
	_, port, err := net.SplitHostPort(freeAddress(t))
	if err != nil {
		t.Fatal(err)
	}
	logs := startServe(t, consoleHome(t, "0.0.0.0:"+port), consoleStore(t, true))
	logs.mu.Lock()
	defer logs.mu.Unlock()
	if !strings.Contains(logs.text.String(), "WARNING: the approval console listens beyond loopback. Use an HTTPS tunnel such as tailscale serve.\napproval console listening\n") {
		t.Fatalf("log=%q", logs.text.String())
	}
}

func TestServeRefusesToStartWhenTheConsolePortIsTaken(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	ctx, cancel := context.WithTimeout(context.Background(), serveBound)
	defer cancel()
	var stderr strings.Builder
	code := cli.Run(ctx, []string{"serve", "--home", consoleHome(t, taken.Addr().String()), "--listen", "127.0.0.1:0"}, io.Discard, &stderr, cli.Options{LookupEnv: func(string) string { return "" }, Server: server.Options{Credentials: consoleStore(t, true)}})
	text := stderr.String()
	if code != 1 || !strings.HasSuffix(text, "error: approval console listener unavailable\n") || strings.Contains(text, "listening") {
		t.Fatalf("exit=%d stderr=%q", code, text)
	}
}
