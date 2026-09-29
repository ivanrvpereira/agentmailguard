package m365_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/providers/m365"
)

func TestLoginProfileDelayDoesNotExtendTokenLifetime(t *testing.T) {
	for _, delay := range []time.Duration{30 * time.Second, time.Minute} {
		t.Run(delay.String(), func(t *testing.T) {
			var elapsed atomic.Int64
			s := validStore()
			old := s.token
			fresh := jwt(map[string]any{"scp": "User.Read Mail.ReadWrite", "version": "login"})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/devicecode":
					jsonResponse(w, map[string]any{"device_code": "device", "user_code": "code", "verification_uri": "https://synthetic.invalid", "expires_in": 60})
				case "/token":
					jsonResponse(w, map[string]any{"access_token": fresh, "refresh_token": "login-refresh", "expires_in": 60})
				case "/v1.0/me":
					if r.Header.Get("Authorization") != "Bearer "+fresh {
						t.Error("Login profile used another identity")
					}
					elapsed.Store(int64(delay))
					jsonResponse(w, map[string]string{"userPrincipalName": "login@example.test"})
				default:
					t.Error("unexpected API call")
				}
			}))
			defer server.Close()
			c := m365.New(account(), s, m365.Options{HTTPClient: server.Client(), BaseURL: server.URL + "/v1.0", AuthorityURL: server.URL, Now: func() time.Time { return now.Add(time.Duration(elapsed.Load())) }, Wait: func(context.Context, time.Duration) error { return nil }})
			user, _, err := c.Login(context.Background(), true, func(string, string) {})
			if delay == time.Minute {
				if err == nil || user != "" || s.saves != 0 || !reflect.DeepEqual(s.token, old) {
					t.Fatal("expired Login token replaced stored identity")
				}
				return
			}
			if err != nil || user != "login@example.test" || s.saves != 1 || !s.token.ExpiresAt.Equal(now.Add(time.Minute)) {
				t.Fatalf("profile extended original token deadline: error=%v saves=%d expiry=%v", err, s.saves, s.token.ExpiresAt)
			}
		})
	}
}
