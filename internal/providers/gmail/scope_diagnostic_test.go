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

	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
)

func TestStatusMissingScopesRefuseWithDiagnostics(t *testing.T) {
	for _, refreshed := range []bool{false, true} {
		t.Run(fmt.Sprint("refreshed=", refreshed), func(t *testing.T) {
			grant := "openid https://www.googleapis.com/auth/gmail.send"
			missing := []string{"https://www.googleapis.com/auth/gmail.modify", "https://www.googleapis.com/auth/calendar.events.owned"}
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
			var diagnostic interface{ MissingScopes() []string }
			if err == nil || err.Error() != "Gmail error ScopeError" || user != "" || len(scopes) != 0 || !errors.As(err, &diagnostic) {
				t.Fatalf("missing-scope refusal lost diagnostics: %v", err)
			}
			if !reflect.DeepEqual(diagnostic.MissingScopes(), missing) {
				t.Fatalf("diagnostic lost missing scopes: %v", diagnostic.MissingScopes())
			}
			copy := diagnostic.MissingScopes()
			copy[0] = "changed"
			if !reflect.DeepEqual(diagnostic.MissingScopes(), missing) {
				t.Fatal("diagnostic exposes mutable scope storage")
			}
			for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
				if text := fmt.Sprintf(format, err); strings.Contains(text, "googleapis") || strings.Contains(text, "private-") {
					t.Fatal("scope error formatting leaks diagnostic data")
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

func TestExtraScopesListsOnlyUnrequestedGrants(t *testing.T) {
	scopes := append(strings.Fields(granted), "openid", "email", "https://www.googleapis.com/auth/gmail.send", "https://www.googleapis.com/auth/drive")
	if extra := gmail.ExtraScopes(scopes); !reflect.DeepEqual(extra, []string{"https://www.googleapis.com/auth/gmail.send", "https://www.googleapis.com/auth/drive"}) {
		t.Fatalf("extra scopes: %v", extra)
	}
	if extra := gmail.ExtraScopes(strings.Fields(granted + " " + writeGranted)); len(extra) != 0 {
		t.Fatalf("requested scopes reported as extra: %v", extra)
	}
}
