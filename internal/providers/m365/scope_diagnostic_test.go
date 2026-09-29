package m365_test

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

	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/m365"
)

func TestStatusScopeDiagnosticsPreserveRefusal(t *testing.T) {
	for _, refreshed := range []bool{false, true} {
		t.Run(fmt.Sprint("refreshed=", refreshed), func(t *testing.T) {
			extra := []string{"Mail.Send", "Contacts.Read", "private-scope-canary\x00"}
			token := jwt(map[string]any{"scp": "User.Read " + strings.Join(extra, " ")})
			s := validStore()
			if refreshed {
				s.token.ExpiresAt = now.Add(-time.Second)
			} else {
				s.token.AccessToken = token
			}
			old := s.token
			var exchanges, uses atomic.Int32
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/token" {
					exchanges.Add(1)
					jsonResponse(w, map[string]any{"access_token": token, "refresh_token": "private-new-refresh", "expires_in": 3600})
					return
				}
				uses.Add(1)
				w.WriteHeader(500)
			}))
			defer h.Close()
			c := m365.New(account(), s, m365.Options{BaseURL: h.URL, AuthorityURL: h.URL, Now: func() time.Time { return now }})
			user, scopes, err := c.Status(context.Background())
			var diagnostic interface{ ExtraScopes() []string }
			if err == nil || err.Error() != "AuthError" || user != "" || len(scopes) != 0 || !errors.As(err, &diagnostic) {
				t.Fatalf("scope refusal lost diagnostics: %v", err)
			}
			if !reflect.DeepEqual(diagnostic.ExtraScopes(), extra) {
				t.Fatal("diagnostic lost rejected scopes")
			}
			copy := diagnostic.ExtraScopes()
			copy[0] = "changed"
			if !reflect.DeepEqual(diagnostic.ExtraScopes(), extra) {
				t.Fatal("diagnostic exposes mutable scope storage")
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
					if strings.Contains(text, "canary") || strings.Contains(text, "Mail.Send") || strings.Contains(text, token) {
						t.Fatal("scope error formatting leaks diagnostic data")
					}
				}
			}
			if _, err := c.Mailbox().Get(context.Background(), []string{"message"}, false); err == nil || err.Error() != "AuthError" {
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
	for _, token := range []string{"opaque", jwt(map[string]any{"scp": "User.Read", "roles": []string{"Mail.Read"}})} {
		s := validStore()
		s.token.AccessToken = token
		c := m365.New(account(), s, m365.Options{Now: func() time.Time { return now }})
		_, _, err := c.Status(context.Background())
		var diagnostic interface{ ExtraScopes() []string }
		if err == nil || err.Error() != "AuthError" || errors.As(err, &diagnostic) {
			t.Fatal("malformed token became a scope diagnostic")
		}
	}
}
