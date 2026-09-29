package server_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/cli"
	"github.com/ivanrvpereira/agentmailguard-go/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/gmail"
	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard-go/internal/server"
)

type statusStore struct {
	credentials.Store
	token   credentials.OAuth
	missing bool
	saves   atomic.Int32
}

func (s *statusStore) LoadOAuth(context.Context, string, string) (credentials.OAuth, error) {
	if s.missing {
		return credentials.OAuth{}, credentials.ErrNotFound
	}
	return s.token, nil
}
func (s *statusStore) SaveOAuth(context.Context, string, string, credentials.OAuth) error {
	s.saves.Add(1)
	return nil
}

func statusJWT(scopes string) string {
	b, _ := json.Marshal(map[string]string{"scp": scopes})
	return "x." + base64.RawURLEncoding.EncodeToString(b) + ".x"
}

func TestAuthStatusSafeDiagnostics(t *testing.T) {
	for _, provider := range []string{"m365", "gmail"} {
		for _, refreshed := range []bool{false, true} {
			for _, kind := range []string{"scope", "scope-limit", "malformed-labels", "missing-scopes", "other-error", "missing-login", "success"} {
				if provider == "m365" && kind == "missing-scopes" {
					continue // M365 has no required-scope policy.
				}
				t.Run(fmt.Sprintf("%s/refreshed=%t/%s", provider, refreshed, kind), func(t *testing.T) {
					now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
					good := "User.Read Mail.ReadWrite Calendars.Read MailboxSettings.Read"
					extra := []string{"Mail.Send", "Contacts.Read"}
					if provider == "gmail" {
						good = "https://www.googleapis.com/auth/gmail.modify https://www.googleapis.com/auth/calendar.events.owned.readonly"
						extra = []string{"https://www.googleapis.com/auth/gmail.send", "https://www.googleapis.com/auth/drive"}
					}
					if kind == "scope-limit" {
						extra = nil
						for i := range 105 {
							label := fmt.Sprintf("Extra.Scope%d", i)
							if provider == "gmail" {
								label = fmt.Sprintf("https://www.googleapis.com/auth/extra.scope%d", i)
							}
							extra = append(extra, label)
						}
					}
					grant := good
					extraGrant := kind == "scope" || kind == "scope-limit" || kind == "malformed-labels"
					if extraGrant {
						grant += " " + strings.Join(extra, " ")
					}
					if kind == "scope-limit" {
						extra = extra[:100]
					}
					if kind == "malformed-labels" {
						grant += " Mail." + strings.Repeat("x", 257) + " https://www.googleapis.com/auth/" + strings.Repeat("x", 257)
						grant += " private-canary\x00 " + strings.Repeat("private-canary", 100) + " Mail.Send\x1b[31m https://user@www.googleapis.com/auth/gmail.send https://www.googleapis.com/auth/gmail.send?private-canary https://www.googleapis.com/auth/gmail.send#private-canary https://www.googleapis.com.evil/auth/gmail.send https://www.googleapis.com/auth/../private-canary https://www.googleapis.com/auth/gmail%2Esend"
					}
					if kind == "missing-scopes" {
						grant = "openid"
						extra = []string{}
					}
					token := "private-access-canary"
					if provider == "m365" {
						token = statusJWT(grant)
						if kind == "other-error" {
							token = "private-malformed-token-canary"
						}
					}
					creds := &statusStore{token: credentials.OAuth{AccessToken: token, RefreshToken: "private-refresh-canary", Scopes: strings.Fields(grant), ExpiresAt: now.Add(time.Hour), Username: "synthetic@example.test"}, missing: kind == "missing-login"}
					if refreshed {
						creds.token.ExpiresAt = now.Add(-time.Second)
						creds.token.Scopes = []string{"Stale.Scope"}
					}
					var exchanges, uses atomic.Int32
					api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if r.URL.Path == "/token" {
							exchanges.Add(1)
							response(w, map[string]any{"access_token": token, "scope": grant, "expires_in": 3600})
							return
						}
						uses.Add(1)
						if kind == "other-error" {
							w.WriteHeader(400)
							response(w, map[string]string{"error": "private-provider-error-canary"})
							return
						}
						response(w, map[string]string{"emailAddress": "synthetic@example.test"})
					}))
					defer api.Close()
					home := t.TempDir()
					configuration := "[accounts.work]\nprovider='" + provider + "'\nclient_id='synthetic'\ntenant_id='synthetic'\nclient_secret='synthetic'\n"
					if e := os.WriteFile(filepath.Join(home, "config.toml"), []byte(configuration), 0600); e != nil {
						t.Fatal(e)
					}
					s, err := server.New(context.Background(), home, server.Options{Credentials: creds, M365: m365.Options{BaseURL: api.URL, AuthorityURL: api.URL}, Gmail: gmail.Options{GmailURL: api.URL, TokenURL: api.URL + "/token", ProfileURL: api.URL + "/profile"}, Now: func() time.Time { return now }})
					if err != nil {
						t.Fatal(err)
					}
					defer s.Close()
					h := httptest.NewServer(s)
					defer h.Close()
					var out, stderr bytes.Buffer
					code := cli.Run(context.Background(), []string{"--server", h.URL, "auth", "status"}, &out, &stderr, cli.Options{LookupEnv: func(string) string { return "" }})
					ok := kind == "success" || extraGrant
					wantCode, wantError := 1, ""
					if kind == "missing-scopes" {
						wantError = "error: authentication refused: required scopes missing\n"
					} else if kind == "other-error" {
						wantError = "error: authentication failed\n"
					} else if ok {
						wantCode = 0
					}
					if extraGrant {
						quoted := make([]string, len(extra))
						for i, s := range extra {
							quoted[i] = fmt.Sprintf("%q", s)
						}
						wantError = "warning: token grants extra scopes the server does not use: " + strings.Join(quoted, ", ") + ". Remove the extra consent to reduce exposure.\n"
					}
					var got map[string]any
					if code != wantCode || stderr.String() != wantError || json.Unmarshal(out.Bytes(), &got) != nil {
						t.Fatalf("exit=%d stderr=%q output=%s", code, stderr.String(), out.String())
					}
					want := map[string]any{"account": "work", "ok": ok, "logged_in": kind != "missing-login"}
					if kind != "missing-login" {
						want["username"] = "synthetic@example.test"
					}
					if ok {
						want["scopes"] = strings.Fields(good)
					}
					if extraGrant {
						want["extra_scopes"] = extra
					}
					if kind == "missing-scopes" {
						want["missing_scopes"] = []string{"https://www.googleapis.com/auth/gmail.modify", "https://www.googleapis.com/auth/calendar.events.owned.readonly"}
						if uses.Load() != 0 || creds.saves.Load() != 0 {
							t.Fatal("refused token was used or saved")
						}
					}
					if ok && refreshed && (exchanges.Load() != 1 || creds.saves.Load() != 1) {
						t.Fatal("usable refreshed token was not saved once")
					}
					encoded, _ := json.Marshal(want)
					var expected map[string]any
					json.Unmarshal(encoded, &expected)
					if !reflect.DeepEqual(got, expected) || strings.Contains(out.String()+stderr.String(), "private-") || strings.Contains(out.String(), "Stale.Scope") {
						t.Fatal("status changed public fields or exposed unsafe/stale diagnostics")
					}
				})
			}
		}
	}
}
