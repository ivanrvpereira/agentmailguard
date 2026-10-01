package gmail_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestBrowserLoginPKCERefreshAndPreservesPreviousOnRefusal(t *testing.T) {
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
			c := gmail.New(account, store, opts)
			prompts := 0
			name, scopes, err := c.Login(context.Background(), false, func(uri, code string) {
				prompts++
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
func TestPastedRedirectCompletesLoginWithoutBrowser(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(fmt.Sprint(denied), func(t *testing.T) {
			store := validStore()
			var redirect, state string
			steps := []string{}
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/profile" {
					steps = append(steps, "profile")
					respond(w, map[string]string{"emailAddress": "pasted@example.test"})
					return
				}
				_ = r.ParseForm()
				steps = append(steps, r.Form.Get("grant_type"))
				if r.Form.Get("grant_type") == "authorization_code" && (r.Form.Get("code") != "pasted-code" || r.Form.Get("redirect_uri") != redirect) {
					t.Error("pasted code exchange mismatch")
				}
				respond(w, map[string]any{"access_token": "refreshed", "refresh_token": "initial-refresh", "expires_in": 3600, "scope": granted})
			}))
			defer api.Close()
			auth := make(chan *url.URL, 1)
			var retries []bool
			opts := gmail.Options{HTTPClient: api.Client(), TokenURL: api.URL + "/token", AuthURL: api.URL + "/auth", ProfileURL: api.URL + "/profile",
				// Like xdg-open running a foreground browser, this opener returns only at the end, and with an error.
				OpenBrowser: func(ctx context.Context, uri string) error {
					parsed, _ := url.Parse(uri)
					auth <- parsed
					<-ctx.Done()
					return fmt.Errorf("no browser on this host")
				},
				ReadRedirect: func(_ context.Context, retry bool) (string, error) {
					retries = append(retries, retry)
					if len(retries) == 1 {
						u := <-auth
						redirect, state = u.Query().Get("redirect_uri"), u.Query().Get("state")
						return u.String(), nil // The sign-in URL is not the redirect.
					}
					query := "?state=" + url.QueryEscape(state) + "&code=pasted-code&scope=" + url.QueryEscape(granted)
					if denied {
						query = "?state=" + url.QueryEscape(state) + "&error=access_denied"
					}
					switch len(retries) {
					case 2:
						return redirect + "?state=wrong&code=pasted-code", nil
					case 3:
						return strings.Replace(redirect, "127.0.0.1", "localhost", 1) + query, nil
					default:
						return "  " + redirect + query + "\n", nil
					}
				}}
			c := gmail.New(account, store, opts)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			name, _, err := c.Login(ctx, false, nil)
			if denied {
				if err == nil || ctx.Err() != nil || store.saves != 0 || len(steps) != 0 {
					t.Fatal("denied paste reached the token endpoint", steps)
				}
				return
			}
			if err != nil || name != "pasted@example.test" || store.saves != 1 || strings.Join(steps, ",") != "authorization_code,refresh_token,profile" || fmt.Sprint(retries) != "[false true true true]" {
				t.Fatal(name, err, steps, retries)
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
