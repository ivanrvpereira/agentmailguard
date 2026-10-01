package cli_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/cli"
	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

const manualScopes = "https://www.googleapis.com/auth/gmail.modify https://www.googleapis.com/auth/calendar.events.owned.readonly"

func manualHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[credentials]\nbackend='file'\n[accounts.gmail]\nprovider='gmail'\nclient_id='synthetic'\nclient_secret='private-client-secret'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return home
}

type manualWriter struct {
	bytes.Buffer
	respond func(string)
}

func (w *manualWriter) WriteString(s string) (int, error) { return w.Write([]byte(s)) }
func (w *manualWriter) Write(p []byte) (int, error) {
	n, err := w.Buffer.Write(p)
	if w.respond != nil {
		w.respond(string(p))
	}
	return n, err
}

func manualAuthorization(t *testing.T, text string) *url.URL {
	t.Helper()
	raw, err := strconv.Unquote(strings.TrimSpace(strings.TrimPrefix(text, "Provider sign-in URL: ")))
	if err != nil {
		t.Fatal("invalid sign-in URL output")
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal("invalid authorization URL")
	}
	return u
}

func TestManualCLIRejectsOtherProvidersDeviceAndRemote(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		env   string
		exit  int
		want  string
	}{
		{"non-gmail", nil, "", 1, "manual login is Gmail only"},
		{"device", []string{"--device-code"}, "", 2, "invalid command usage"},
		{"device-alias", []string{"--no-device-code=false"}, "", 2, "invalid command usage"},
		{"remote", []string{"--server", "https://remote.example.test"}, "", 1, "login is local only"},
		{"empty-server", []string{"--server", ""}, "", 1, "login is local only"},
		{"remote-env", nil, "https://remote.example.test", 1, "login is local only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			args := append([]string{"--home", fileHome(t), "auth", "login", "--manual"}, tc.flags...)
			code := cli.Run(context.Background(), args, &out, &stderr, cli.Options{Stdin: failedPasswordReader{}, LookupEnv: func(key string) string {
				if key == "AGENTMAILGUARD_SERVER" {
					return tc.env
				}
				return ""
			}})
			if code != tc.exit || out.Len() != 0 || !strings.Contains(stderr.String(), tc.want) || strings.Contains(stderr.String(), "private-") {
				t.Fatalf("manual guard: exit=%d stderr=%q", code, stderr.String())
			}
		})
	}
}

func TestManualCLIInputRefusalsDoNotLeakOrSave(t *testing.T) {
	for _, mode := range []string{"eof", "partial-eof", "empty", "oversize", "invalid", "read-error", "cancel", "provider-error"} {
		t.Run(mode, func(t *testing.T) {
			home := manualHome(t)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			store := &diagnosticStore{}
			tokens := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tokens++
				w.WriteHeader(400)
				io.WriteString(w, `{"error":"private-provider-error","error_description":"private-description"}`)
			}))
			defer api.Close()
			var input bytes.Buffer
			var stdin io.Reader = &input
			if mode == "read-error" {
				stdin = failedPasswordReader{}
			}
			if mode == "cancel" {
				r, w := io.Pipe()
				defer r.Close()
				defer w.Close()
				stdin = r
			}
			stderr := &manualWriter{respond: func(text string) {
				if strings.HasPrefix(text, "Provider sign-in URL: ") {
					q := manualAuthorization(t, text).Query()
					pasted := q.Get("redirect_uri") + "?" + url.Values{"state": {q.Get("state")}, "code": {"private-callback-code"}}.Encode()
					switch mode {
					case "partial-eof":
						input.WriteString(pasted)
					case "empty":
						input.WriteString("\n")
					case "oversize":
						input.WriteString(pasted + "&extra=" + strings.Repeat("x", 4097) + "\n")
					case "invalid":
						input.WriteString("https://private-callback.invalid/?code=private-code\n")
					case "provider-error":
						input.WriteString(pasted + "\r\nfollowing-line\n")
					}
				}
				if mode == "cancel" && strings.Contains(text, "full callback URL") {
					cancel()
				}
			}}
			var out bytes.Buffer
			code := cli.Run(ctx, []string{"--home", home, "auth", "login", "--manual"}, &out, stderr, cli.Options{Stdin: stdin, LookupEnv: func(string) string { return "" }, Server: server.Options{Credentials: store, Gmail: gmail.Options{AuthURL: api.URL, TokenURL: api.URL, OpenBrowser: func(context.Context, string) error {
				t.Error("manual login opened browser")
				return nil
			}}}})
			wantTokens := 0
			if mode == "provider-error" {
				wantTokens = 1
			}
			if mode == "provider-error" && input.String() != "following-line\n" {
				t.Fatal("callback reader consumed a later stdin line")
			}
			if code != 1 || tokens != wantTokens || store.saves.Load() != 0 || out.Len() != 0 || strings.Contains(stderr.String(), "private-") || !strings.Contains(stderr.String(), "provider login failed") {
				t.Fatalf("unsafe refusal: mode=%s exit=%d tokens=%d stderr=%q", mode, code, tokens, stderr.String())
			}
		})
	}
}

func TestManualCLILoginStoresUsableRefreshedIdentity(t *testing.T) {
	home := manualHome(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var challenge, redirect string
	steps := []string{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/profile" {
			if r.Header.Get("Authorization") != "Bearer private-refreshed-token" {
				t.Error("identity did not use refreshed token")
			}
			steps = append(steps, "profile")
			io.WriteString(w, `{"emailAddress":"synthetic@example.test"}`)
			return
		}
		if r.URL.Path != "/token" || r.Method != "POST" {
			t.Error("unexpected provider request")
			w.WriteHeader(400)
			return
		}
		_ = r.ParseForm()
		access := "private-exchanged-token"
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			steps = append(steps, "code")
			hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if len(r.Form.Get("code_verifier")) != 43 || base64.RawURLEncoding.EncodeToString(hash[:]) != challenge || r.Form.Get("redirect_uri") != redirect || r.Form.Get("code") != "private-auth-code" || r.Form.Get("client_secret") != "private-client-secret" {
				t.Error("manual PKCE exchange mismatch")
			}
		case "refresh_token":
			steps = append(steps, "refresh")
			access = "private-refreshed-token"
			if r.Form.Get("refresh_token") != "private-refresh-token" {
				t.Error("initial refresh not validated")
			}
		default:
			t.Error("unexpected grant")
		}
		json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": "private-refresh-token", "scope": manualScopes, "expires_in": 3600})
	}))
	defer api.Close()
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	callbacks := make(chan string, 1)
	go func() {
		_, _ = io.WriteString(writer, "private-file-password\n")
		select {
		case pasted := <-callbacks:
			_, _ = io.WriteString(writer, pasted+"\n") // Keep stdin open: login must not wait for EOF.
		case <-ctx.Done():
		}
	}()
	var out bytes.Buffer
	stderr := &manualWriter{respond: func(text string) {
		if strings.HasPrefix(text, "Provider sign-in URL: ") {
			q := manualAuthorization(t, text).Query()
			challenge, redirect = q.Get("code_challenge"), q.Get("redirect_uri")
			callbacks <- redirect + "?" + url.Values{"state": {q.Get("state")}, "code": {"private-auth-code"}}.Encode()
		}
	}}
	opts := gmail.Options{HTTPClient: api.Client(), AuthURL: api.URL + "/auth", TokenURL: api.URL + "/token", ProfileURL: api.URL + "/profile", OpenBrowser: func(context.Context, string) error {
		t.Error("manual CLI opened browser")
		return nil
	}}
	code := cli.Run(ctx, []string{"--home", home, "--account", "gmail", "auth", "login", "--manual"}, &out, stderr, cli.Options{Stdin: input, LookupEnv: func(string) string { return "" }, Server: server.Options{Gmail: opts}})
	if code != 0 || !strings.Contains(out.String(), `"username": "synthetic@example.test"`) || !strings.Contains(stderr.String(), "full callback URL") || !strings.Contains(stderr.String(), "Restart the server") || strings.Contains(out.String()+stderr.String(), "private-") || strings.Join(steps, ",") != "code,refresh,profile" {
		t.Fatalf("manual login: exit=%d stdout=%q stderr=%q steps=%v", code, out.String(), stderr.String(), steps)
	}
	store := credentials.NewFile(home, "private-file-password")
	token, err := store.LoadOAuth(ctx, "gmail", "gmail")
	if err != nil || token.AccessToken != "private-refreshed-token" || token.RefreshToken != "private-refresh-token" {
		t.Fatal("refreshed credentials not stored")
	}
	fresh := gmail.New(config.Account{Name: "gmail", Provider: "gmail", ClientID: "synthetic", ClientSecret: "private-client-secret"}, store, opts)
	name, scopes, err := fresh.Status(ctx)
	if err != nil || name != "synthetic@example.test" || len(scopes) != 2 {
		t.Fatal("stored identity unusable")
	}
}
