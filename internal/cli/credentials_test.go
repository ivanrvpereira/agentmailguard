package cli_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/cli"
	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

type localPlatform struct {
	mu     sync.Mutex
	values map[[2]string][]byte
}

func (p *localPlatform) Read(_ context.Context, service, account string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]byte(nil), p.values[[2]string{service, account}]...), nil
}
func (p *localPlatform) Write(_ context.Context, service, account string, v []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.values[[2]string{service, account}] = append([]byte(nil), v...)
	return nil
}
func TestLocalCommandsMigrateBeforeSaving(t *testing.T) {
	for _, command := range []string{"set-jev-key", "login"} {
		t.Run(command, func(t *testing.T) {
			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[accounts.work]\nprovider='m365'\nclient_id='synthetic'\ntenant_id='synthetic'\n[accounts.personal]\nprovider='gmail'\nclient_id='synthetic'\nclient_secret='synthetic'\n"), 0600); err != nil {
				t.Fatal(err)
			}
			p := &localPlatform{values: map[[2]string][]byte{
				{"agentmailguard-go-gmail", "personal"}:               []byte(`{"refresh_token":"private-legacy-canary"}`),
				{"agentmailguard-go-openrouter", "agentmailguard-go"}: []byte("private-key-canary"),
			}}
			store := credentials.New(p, home, []config.Account{{Provider: "m365", Name: "work"}, {Provider: "gmail", Name: "personal"}})
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/devicecode":
					io.WriteString(w, `{"device_code":"synthetic","user_code":"synthetic","verification_uri":"https://example.test/verify","expires_in":60,"interval":1}`)
				case "/token":
					token := "x." + base64.RawURLEncoding.EncodeToString([]byte(`{"scp":"User.Read Mail.ReadWrite Calendars.Read MailboxSettings.Read"}`)) + ".x"
					json.NewEncoder(w).Encode(map[string]any{"access_token": token, "refresh_token": "private-new-canary", "expires_in": 3600})
				case "/me":
					io.WriteString(w, `{"userPrincipalName":"synthetic@example.test"}`)
				default:
					t.Error("unexpected fake provider request")
					w.WriteHeader(500)
				}
			}))
			defer api.Close()
			opts := cli.Options{Stdin: strings.NewReader("private-new-key-canary\n"), LookupEnv: func(string) string { return "" }, Server: server.Options{Credentials: store, M365: m365.Options{AuthorityURL: api.URL, BaseURL: api.URL, Wait: func(context.Context, time.Duration) error { return nil }}}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			args := []string{"--home", home, "auth", command}
			if command == "login" {
				args = append(args, "--device-code")
			}
			var out, stderr bytes.Buffer
			code := cli.Run(ctx, args, &out, &stderr, opts)
			if code != 0 {
				t.Fatalf("local command failed: code=%d", code)
			}
			if strings.Contains(out.String()+stderr.String(), "private-") {
				t.Fatal("local command exposed secrets")
			}
			reopened := credentials.New(p, home, nil)
			if v, err := reopened.LoadOAuth(ctx, "gmail", "personal"); err != nil || v.RefreshToken != "private-legacy-canary" {
				t.Fatal("local command hid another account's legacy token")
			}
			if command == "login" {
				if key, err := reopened.LoadOpenRouterKey(ctx); err != nil || key != "private-key-canary" {
					t.Fatal("login lost legacy Jev key")
				}
			} else {
				if key, err := reopened.LoadOpenRouterKey(ctx); err != nil || key != "private-new-key-canary" {
					t.Fatal("set-jev-key did not save new key")
				}
			}
		})
	}
}
