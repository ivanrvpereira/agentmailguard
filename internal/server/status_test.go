package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/cli"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

func TestAccountStatusSafeDiagnostics(t *testing.T) {
	for _, provider := range []string{"m365", "gmail"} {
		for _, refreshed := range []bool{false, true} {
			for _, kind := range []string{"scope", "scope-limit", "malformed-labels", "missing-scopes", "other-error", "missing-login", "success"} {
				if provider == "m365" && kind == "missing-scopes" {
					continue // M365 has no required-scope policy.
				}
				t.Run(fmt.Sprintf("%s/refreshed=%t/%s", provider, refreshed, kind), func(t *testing.T) {
					now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
					good := "User.Read Mail.ReadWrite Calendars.Read MailboxSettings.Read"
					extra := []string{"Mail.Send.Shared", "Contacts.Read"}
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
					login := credentials.OAuth{AccessToken: token, RefreshToken: "private-refresh-canary", Scopes: strings.Fields(grant), ExpiresAt: now.Add(time.Hour)}
					if refreshed {
						login.ExpiresAt = now.Add(-time.Second)
						login.Scopes = []string{"Stale.Scope"}
					}
					email, logins := workEmail, map[string]credentials.OAuth{}
					if provider == "gmail" {
						email = gmailEmail
					}
					if kind != "missing-login" {
						logins[provider+"/"+email] = login
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
					config := workConfig
					if provider == "gmail" {
						config = gmailConfig
					}
					d := startDaemon(t, daemonSetup{config: config + fakeScreen, record: record(logins), options: server.Options{M365: m365Options(api.URL), Gmail: gmail.Options{GmailURL: api.URL, TokenURL: api.URL + "/token", ProfileURL: api.URL + "/profile"}, Now: func() time.Time { return now }}})
					h := httptest.NewServer(d.s)
					defer h.Close()
					var out, stderr bytes.Buffer
					code := cli.Run(context.Background(), []string{"-H", h.URL, "account", "status"}, &out, &stderr, cli.Options{LookupEnv: func(string) string { return "" }})
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
						wantError = "warning: token grants extra scopes the daemon does not use: " + strings.Join(quoted, ", ") + ". Remove the extra consent to reduce exposure.\n"
					}
					var got map[string]any
					if code != wantCode || stderr.String() != wantError || json.Unmarshal(out.Bytes(), &got) != nil {
						t.Fatalf("exit=%d stderr=%q output=%s", code, stderr.String(), out.String())
					}
					want := map[string]any{"account": provider, "email": email, "ok": ok, "logged_in": kind != "missing-login"}
					if provider == "m365" {
						want["account"] = "work"
					}
					if ok {
						want["scopes"] = strings.Fields(good)
					}
					if extraGrant {
						want["extra_scopes"] = extra
					}
					if kind == "missing-scopes" {
						want["missing_scopes"] = []string{"https://www.googleapis.com/auth/gmail.modify", "https://www.googleapis.com/auth/calendar.events.owned"}
						if stored, err := d.store.LoadOAuth(context.Background(), provider, email); uses.Load() != 0 || err != nil || !credentials.SameOAuth(stored, login) {
							t.Fatal("refused token was used or saved")
						}
					}
					if stored, err := d.store.LoadOAuth(context.Background(), provider, email); ok && refreshed && (exchanges.Load() != 1 || err != nil || !stored.ExpiresAt.After(now)) {
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
