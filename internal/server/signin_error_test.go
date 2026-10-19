package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

// expiredStore holds a login whose access token has expired, so every call first asks the provider for a new one.
type expiredStore struct{ store }

func (s *expiredStore) LoadOAuth(context.Context, string, string) (credentials.OAuth, error) {
	s.loads.Add(1)
	return credentials.OAuth{RefreshToken: "private-refresh-canary", ExpiresAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), Username: "synthetic@example.test"}, nil
}

func TestRefusedSignInTellsTheClientToSignInAgain(t *testing.T) {
	for _, provider := range []string{"gmail", "m365"} {
		t.Run(provider, func(t *testing.T) {
			// The provider refuses the saved refresh token, as Google does when a Testing token expires.
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":"invalid_grant","error_description":"CANARY raw error"}`)
			}))
			defer api.Close()
			home := t.TempDir()
			configFile(t, home)
			if provider == "gmail" {
				useGmail(t, home)
			}
			s, err := server.New(context.Background(), home, server.Options{Credentials: &expiredStore{}, Screener: newNativeAPI().screener(t), M365: m365.Options{BaseURL: api.URL, AuthorityURL: api.URL}, Gmail: gmail.Options{HTTPClient: api.Client(), TokenURL: api.URL + "/token", GmailURL: api.URL}})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			status, body := meetingRPC(t, s, "mail.folders", `{}`)
			if want := `{"error":"sign-in failed; check the network, or run auth login and restart the server"}`; status != 500 || body != want || strings.Contains(body, "CANARY") {
				t.Fatalf("response=%d %s", status, body)
			}
		})
	}
}
