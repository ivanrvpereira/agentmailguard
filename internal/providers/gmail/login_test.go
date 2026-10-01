package gmail_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBrowserLoginPKCERefreshAndPreservesPreviousOnRefusal(t *testing.T) {
	checkLoginPKCE(t, false)
}

func TestManualLoginPKCERefreshAndPreservesPreviousOnRefusal(t *testing.T) {
	checkLoginPKCE(t, true)
}

func checkLoginPKCE(t *testing.T, manual bool) {
	for _, badRefresh := range []bool{false, true} {
		t.Run(fmt.Sprint(badRefresh), func(t *testing.T) {
			store := validStore()
			old := store.token
			var challenge, redirect string
			steps := []string{}
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/profile" {
					if r.Header.Get("Authorization") != "Bearer refreshed" {
						t.Error("profile before refresh")
					}
					steps = append(steps, "profile")
					respond(w, map[string]string{"emailAddress": "new@example.test"})
					return
				}
				if r.URL.Path != "/token" || r.Method != "POST" {
					t.Error("unexpected auth request")
				}
				_ = r.ParseForm()
				if r.Form.Get("client_secret") != "secret" {
					t.Error("missing secret")
				}
				scope := granted
				token := "exchanged"
				switch r.Form.Get("grant_type") {
				case "authorization_code":
					steps = append(steps, "code")
					hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
					if base64.RawURLEncoding.EncodeToString(hash[:]) != challenge || r.Form.Get("redirect_uri") != redirect || r.Form.Get("code") != "one-time" {
						t.Error("PKCE exchange mismatch")
					}
				case "refresh_token":
					steps = append(steps, "refresh")
					token = "refreshed"
					if r.Form.Get("refresh_token") != "initial-refresh" {
						t.Error("login did not refresh")
					}
					if badRefresh {
						scope = ""
					}
				default:
					t.Error("unexpected grant")
				}
				respond(w, map[string]any{"access_token": token, "refresh_token": "initial-refresh", "expires_in": 3600, "scope": scope})
			}))
			defer api.Close()
			opts := gmail.Options{HTTPClient: api.Client(), TokenURL: api.URL + "/token", AuthURL: api.URL + "/auth", ProfileURL: api.URL + "/profile", OpenBrowser: func(ctx context.Context, uri string) error {
				parsed, err := url.Parse(uri)
				if err != nil {
					t.Fatal(err)
				}
				q := parsed.Query()
				challenge = q.Get("code_challenge")
				redirect = q.Get("redirect_uri")
				if len(challenge) != 43 || q.Get("code_challenge_method") != "S256" || q.Get("scope") != granted || q.Get("access_type") != "offline" || q.Get("prompt") != "consent" || q.Get("response_type") != "code" {
					t.Error("authorization parameters")
				}
				callback, _ := url.Parse(redirect)
				if callback.Hostname() != "127.0.0.1" || callback.Scheme != "http" {
					t.Error("non-loopback callback")
				}
				resp, err := http.Get(redirect + "?state=wrong&code=SECRET")
				if err != nil {
					t.Fatal(err)
				}
				response, _ := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if resp.StatusCode != 400 || strings.Contains(string(response), "SECRET") {
					t.Error("state failure unsafe")
				}
				resp, err = http.Get(redirect + "?state=" + url.QueryEscape(q.Get("state")) + "&code=one-time")
				if err != nil {
					return err
				}
				_ = resp.Body.Close()
				return nil
			}}
			var pasted string
			if manual {
				opts.OpenBrowser = func(context.Context, string) error {
					t.Error("manual login opened browser")
					return fmt.Errorf("private-browser-error")
				}
				opts.ReadCallback = func(context.Context) (string, error) { return pasted, nil }
			}
			c := gmail.New(account, store, opts)
			prompts := 0
			name, scopes, err := c.Login(context.Background(), false, func(uri, code string) {
				prompts++
				if manual {
					u, _ := url.Parse(uri)
					q := u.Query()
					challenge, redirect = q.Get("code_challenge"), q.Get("redirect_uri")
					if len(challenge) != 43 || q.Get("code_challenge_method") != "S256" || q.Get("scope") != granted {
						t.Error("manual authorization parameters")
					}
					pasted = redirect + "?state=" + url.QueryEscape(q.Get("state")) + "&code=one-time"
				}
				if code != "" || !strings.HasPrefix(uri, api.URL+"/auth?") || strings.Contains(uri, "secret") {
					t.Error("prompt leakage")
				}
			})
			if badRefresh {
				if err == nil || store.saves != 0 || store.token.AccessToken != old.AccessToken || len(steps) != 2 {
					t.Fatal("refused login changed credentials")
				}
				return
			}
			if err != nil || name != "new@example.test" || len(scopes) != 2 || prompts != 1 || store.saves != 1 || store.token.AccessToken != "refreshed" || strings.Join(steps, ",") != "code,refresh,profile" {
				t.Fatal(name, scopes, err, steps)
			}
			for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
				if s := fmt.Sprintf(format, c); strings.Contains(s, "refreshed") || strings.Contains(s, "secret") {
					t.Fatal("client formatting leaks")
				}
			}
		})
	}
}
func TestManualLoginRefusesUntrustedCallbackBeforeExchange(t *testing.T) {
	for _, mode := range []string{"state", "host", "port", "path", "encoded-path", "scheme", "userinfo", "fragment", "empty-fragment", "opaque", "malformed", "query", "duplicate-state", "duplicate-code", "empty-code", "missing-code", "error", "empty-error", "duplicate-error", "duplicate-description", "oversize", "eof", "cancel", "read-error"} {
		t.Run(mode, func(t *testing.T) {
			store := validStore()
			old := store.token
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Error("refused callback reached the network")
				w.WriteHeader(400)
			}))
			defer api.Close()
			var pasted, redirect string
			c := gmail.New(account, store, gmail.Options{TokenURL: api.URL, AuthURL: api.URL, OpenBrowser: func(context.Context, string) error {
				t.Error("manual login opened browser")
				return nil
			}, ReadCallback: func(ctx context.Context) (string, error) {
				switch mode {
				case "eof":
					return "", io.EOF
				case "cancel":
					cancel()
					return "", ctx.Err()
				case "read-error":
					return "", fmt.Errorf("private-read-error")
				}
				return pasted, nil
			}})
			_, _, err := c.Login(ctx, false, func(raw, _ string) {
				auth, _ := url.Parse(raw)
				redirect = auth.Query().Get("redirect_uri")
				u, _ := url.Parse(redirect)
				q := url.Values{"state": {auth.Query().Get("state")}, "code": {"private-code"}}
				switch mode {
				case "state":
					q.Set("state", "wrong")
				case "host":
					u.Host = strings.TrimPrefix(api.URL, "http://")
				case "port":
					u.Host = "127.0.0.1:0"
				case "path":
					u.Path = "/other"
				case "encoded-path":
					u.RawPath = "/%2e%2e"
					u.Path = "/.."
				case "scheme":
					u.Scheme = "https"
				case "userinfo":
					u.User = url.User("private-user")
				case "fragment":
					u.Fragment = "private-fragment"
				case "empty-fragment":
					u.ForceQuery = true
				case "opaque":
					u.Opaque, u.Host = "private-opaque", ""
				case "duplicate-state":
					q.Add("state", q.Get("state"))
				case "duplicate-code":
					q.Add("code", "private-second-code")
				case "empty-code":
					q.Set("code", "")
				case "missing-code":
					q.Del("code")
				case "error", "duplicate-error":
					q.Set("error", "private-error")
					if mode == "duplicate-error" {
						q.Add("error", "private-second-error")
					}
				case "empty-error":
					q.Set("error", "")
				case "duplicate-description":
					q["error_description"] = []string{"private-one", "private-two"}
				case "oversize":
					q.Set("extra", strings.Repeat("x", 4097))
				}
				u.RawQuery = q.Encode()
				pasted = u.String()
				switch mode {
				case "query":
					pasted += "&extra=%GG"
				case "malformed":
					pasted = "%private-malformed"
				case "empty-fragment":
					pasted += "#"
				}
			})
			if err == nil || strings.Contains(err.Error(), "private-") || store.saves != 0 || !reflect.DeepEqual(store.token, old) {
				t.Fatalf("unsafe refusal: err=%v saves=%d", err, store.saves)
			}
			u, _ := url.Parse(redirect)
			conn, err := net.DialTimeout("tcp4", u.Host, 50*time.Millisecond)
			if err == nil {
				conn.Close()
				t.Fatal("manual callback listener leaked")
			}
		})
	}
}

func TestLoginDeviceCodeAndCancellation(t *testing.T) {
	store := validStore()
	calls := 0
	c := gmail.New(account, store, gmail.Options{OpenBrowser: func(context.Context, string) error { calls++; return nil }})
	if _, _, err := c.Login(context.Background(), true, nil); err == nil || calls != 0 || store.loads != 0 {
		t.Fatal("device login attempted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, _, err := c.Login(ctx, false, nil); err == nil || store.saves != 0 {
		t.Fatal("cancelled login accepted")
	}
}
