package server_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

func TestRefusedSignInTellsTheClientToSignInAgain(t *testing.T) {
	for _, provider := range []string{"gmail", "m365"} {
		t.Run(provider, func(t *testing.T) {
			// The provider refuses the saved refresh token, as Google does when a Testing token expires.
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(400)
				io.WriteString(w, `{"error":"invalid_grant","error_description":"CANARY raw error"}`)
			}))
			defer api.Close()
			// The login holds only an expired refresh token, so every call first asks the provider for a new one.
			expired := credentials.OAuth{RefreshToken: "private-refresh-canary", ExpiresAt: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)}
			config := workConfig
			if provider == "gmail" {
				config = gmailConfig
			}
			d := startDaemon(t, daemonSetup{config: config, record: record(map[string]credentials.OAuth{"m365/" + workEmail: expired, "gmail/" + gmailEmail: expired}), jev: newNativeAPI(),
				options: server.Options{M365: m365Options(api.URL), Gmail: googleOptions(api.URL)}})
			status, body := accountRPC(t, d.s, "mail.folders", "", `{}`)
			if want := `{"error":"sign-in failed; check the network, or run amgd account login"}`; status != 500 || body != want || strings.Contains(body, "CANARY") {
				t.Fatalf("response=%d %s", status, body)
			}
		})
	}
}
