package gmail_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/gmail"
)

func TestStatusScopeDiagnosticsPreserveRefusal(t *testing.T) {
	for _, refreshed := range []bool{false, true} {
		for _, missing := range []bool{false, true} {
			t.Run(fmt.Sprint("refreshed=", refreshed, "/missing=", missing), func(t *testing.T) {
				extra := []string{"https://www.googleapis.com/auth/gmail.send", "https://www.googleapis.com/auth/drive", "private-scope-canary\x00"}
				grant := granted + " " + strings.Join(extra, " ")
				if missing {
					grant = "openid"
					extra = nil
				}
				now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
				s := validStore()
				if refreshed {
					s.token.ExpiresAt = now
				} else {
					s.token.Scopes = strings.Fields(grant)
				}
				old := s.token
				var exchanges, uses atomic.Int32
				h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/token" {
						exchanges.Add(1)
						respond(w, map[string]any{"access_token": "private-new-token", "refresh_token": "private-new-refresh", "scope": grant, "expires_in": 3600})
						return
					}
					uses.Add(1)
					w.WriteHeader(500)
				}))
				defer h.Close()
				c := gmail.New(account, s, gmail.Options{TokenURL: h.URL + "/token", ProfileURL: h.URL + "/profile", GmailURL: h.URL, Now: func() time.Time { return now }})
				user, scopes, err := c.Status(context.Background())
				var diagnostic interface{ ExtraScopes() []string }
				if err == nil || err.Error() != "Gmail error ScopeError" || user != "" || len(scopes) != 0 || !errors.As(err, &diagnostic) {
					t.Fatalf("scope refusal lost diagnostics: %v", err)
				}
				if !reflect.DeepEqual(diagnostic.ExtraScopes(), extra) {
					t.Fatal("diagnostic lost rejected scopes")
				}
				if !missing {
					copy := diagnostic.ExtraScopes()
					copy[0] = "changed"
					if !reflect.DeepEqual(diagnostic.ExtraScopes(), extra) {
						t.Fatal("diagnostic exposes mutable scope storage")
					}
				}
				value := reflect.ValueOf(err)
				if value.Kind() == reflect.Pointer {
					value = value.Elem()
				}
				pointer := reflect.New(value.Type())
				pointer.Elem().Set(value)
				for _, v := range []any{value.Interface(), pointer.Interface()} {
					for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
						text := fmt.Sprintf(format, v)
						if strings.Contains(text, "canary") || strings.Contains(text, "googleapis") || strings.Contains(text, "private-") {
							t.Fatal("scope error formatting leaks diagnostic data")
						}
					}
				}
				if _, err := c.Mailbox().Get(context.Background(), []string{"message"}, false); err == nil || err.Error() != "Gmail error ScopeError" {
					t.Fatal("refused token authorized mailbox access")
				}
				wantExchanges := int32(0)
				if refreshed {
					wantExchanges = 2
				}
				if exchanges.Load() != wantExchanges || uses.Load() != 0 || s.saves != 0 || !reflect.DeepEqual(s.token, old) {
					t.Fatal("refusal changed refresh, persistence, or token-use behavior")
				}
			})
		}
	}
}
