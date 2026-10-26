package daemon_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/daemon"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

type loginScenario struct {
	mode, want              string
	device, ok              bool
	tokens, profiles, saves int32
}

// checkLoginDiagnostic runs amgd account login against a Microsoft endpoint that fails at one step. The command
// prints a fixed message for the step, crosses no later boundary, and never prints a token, a code, or a
// provider response.
func checkLoginDiagnostic(t *testing.T, scenario loginScenario) {
	t.Helper()
	duration := bound
	if scenario.mode == "timeout" {
		duration = time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	home := writeHome(t, "version = 1\ndefault_account = 'work'\n[accounts.work]\nprovider = 'm365'\nemail = 'synthetic@example.test'\nclient_id = 'synthetic'\ntenant_id = 'synthetic'\n")
	var tokens, profiles atomic.Int32
	scope := "User.Read Mail.ReadWrite Calendars.Read MailboxSettings.Read"
	if scenario.mode == "scopes" {
		scope += " Contacts.Read"
	}
	claims, _ := json.Marshal(map[string]string{"scp": scope})
	token := "private-token-canary." + base64.RawURLEncoding.EncodeToString(claims) + ".private-signature-canary"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/devicecode" {
			if scenario.mode == "device-request" {
				w.WriteHeader(403)
				_, _ = io.WriteString(w, "private-device-request-canary")
				return
			}
			_, _ = io.WriteString(w, `{"device_code":"private-device-canary","user_code":"SYNTHETIC-CODE","verification_uri":"https://example.test/verify","expires_in":60,"interval":1}`)
			return
		}
		if r.URL.Path == "/token" {
			tokens.Add(1)
			switch scenario.mode {
			case "device-denied":
				w.WriteHeader(400)
				_, _ = io.WriteString(w, `{"error":"authorization_declined","error_description":"private-device-denial-canary"}`)
				return
			case "token-error":
				w.WriteHeader(400)
				_, _ = io.WriteString(w, `{"error":"private-error-canary","error_description":"private-description-canary"}`)
				return
			case "token-http":
				w.WriteHeader(503)
				_, _ = io.WriteString(w, "private-body-canary")
				return
			case "token-json":
				_, _ = io.WriteString(w, "private-malformed-json-canary")
				return
			}
			access, expires := token, 3600
			switch scenario.mode {
			case "token-invalid":
				access = "private-opaque-token-canary"
			case "token-expired":
				expires = 0
			case "token-roles":
				access = "x." + base64.RawURLEncoding.EncodeToString([]byte(`{"scp":"User.Read","roles":"Mail.Send"}`)) + ".x"
			case "app-roles":
				access = "x." + base64.RawURLEncoding.EncodeToString([]byte(`{"scp":"User.Read","roles":["Mail.Send"]}`)) + ".x"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "expires_in": expires, "refresh_token": "private-refresh-canary"})
			return
		}
		profiles.Add(1)
		switch scenario.mode {
		case "profile-http":
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"error":{"message":"private-profile-canary"}}`)
			return
		case "profile-invalid":
			_, _ = io.WriteString(w, `{"private":"private-profile-canary"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"userPrincipalName": "synthetic@example.test"})
	}))
	defer api.Close()
	var callbacks atomic.Int32
	open := func(ctx context.Context, raw string) error {
		if scenario.device {
			t.Error("device flow opened browser")
		}
		switch scenario.mode {
		case "browser-open":
			return errors.New("private-browser-canary")
		case "timeout":
			return nil
		case "canceled":
			cancel()
			return nil
		}
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		q := u.Query()
		values := url.Values{"state": {q.Get("state")}, "code": {"private-authorization-canary"}}
		if scenario.mode == "browser-denied" {
			values.Del("code")
			values.Set("error", "private-auth-error-canary")
			values.Set("error_description", "private-auth-description-canary")
		}
		req, err := http.NewRequestWithContext(ctx, "GET", q.Get("redirect_uri")+"?"+values.Encode(), nil)
		if err != nil {
			return err
		}
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
		if scenario.mode == "browser-denied" && (!strings.Contains(string(body), "Return to the terminal") || strings.Contains(string(body), "private-")) {
			t.Error("callback does not direct user to terminal or leaks response")
		}
		if resp.StatusCode != 200 {
			t.Error("synthetic browser callback refused")
		}
		callbacks.Add(1)
		return err
	}
	k := &keychain{failWrite: scenario.mode == "keychain"}
	var out, stderr bytes.Buffer
	args := []string{"--home", home, "account", "login", "work"}
	if scenario.device {
		args = append(args, "--device-code")
	}
	code := daemon.Run(ctx, args, &out, &stderr, daemon.Options{Platform: k, LookupEnv: noEnv, Stdin: strings.NewReader(""), Server: server.Options{M365: m365.Options{AuthorityURL: api.URL, BaseURL: api.URL, OpenBrowser: open, Wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }}}})
	if scenario.ok {
		if code != 0 || !strings.Contains(out.String(), `"email": "synthetic@example.test"`) || !strings.Contains(stderr.String(), scenario.want+"\n") {
			t.Fatalf("login with extra grants did not succeed with a warning: exit=%d stderr=%q", code, stderr.String())
		}
	} else if code != 1 || out.Len() != 0 || !strings.HasSuffix(stderr.String(), "error: "+scenario.want+"\n") {
		t.Fatalf("missing safe login diagnostic: exit=%d stderr=%q", code, stderr.String())
	}
	_, saves := k.item()
	if tokens.Load() != scenario.tokens || profiles.Load() != scenario.profiles || int32(saves) != scenario.saves {
		t.Fatalf("failure crossed wrong boundaries: token=%d profile=%d save=%d", tokens.Load(), profiles.Load(), saves)
	}
	wantCallbacks := int32(1)
	if scenario.device || scenario.mode == "browser-open" || scenario.mode == "timeout" || scenario.mode == "canceled" {
		wantCallbacks = 0
	}
	if callbacks.Load() != wantCallbacks {
		t.Fatal("unexpected browser callback count")
	}
	if strings.Contains(out.String()+stderr.String(), "private-") || strings.Contains(stderr.String(), "localhost") {
		t.Fatal("login diagnostic leaked token/callback data")
	}
}

func TestM365LoginWarnsAboutExtraGrants(t *testing.T) {
	t.Parallel()
	extra := `warning: token grants extra scopes the daemon does not use: "Contacts.Read". Remove the extra consent to reduce exposure.`
	checkLoginDiagnostic(t, loginScenario{mode: "scopes", ok: true, want: extra, tokens: 1, profiles: 1, saves: 1})
	checkLoginDiagnostic(t, loginScenario{mode: "scopes", device: true, ok: true, want: extra, tokens: 1, profiles: 1, saves: 1})
	roles := `warning: token grants extra scopes the daemon does not use: "role:Mail.Send". role: entries are application permissions. Remove the extra consent to reduce exposure.`
	checkLoginDiagnostic(t, loginScenario{mode: "app-roles", ok: true, want: roles, tokens: 1, profiles: 1, saves: 1})
}

func TestM365LoginReportsAuthorizationFailures(t *testing.T) {
	t.Parallel()
	for _, scenario := range []loginScenario{
		{mode: "browser-open", want: "provider login failed: could not open the browser"},
		{mode: "browser-denied", want: "provider login failed: authorization response failed; check Microsoft sign-in logs"},
		{mode: "timeout", want: "provider login failed: login timed out"},
		{mode: "canceled", want: "provider login failed: login canceled"},
		{mode: "device-request", device: true, want: "provider login failed: device-code request failed"},
		{mode: "device-denied", device: true, want: "provider login failed: device-code authorization refused; check Microsoft sign-in logs", tokens: 1},
		{mode: "token-http", device: true, want: "provider login failed: token exchange failed", tokens: 1},
	} {
		t.Run(scenario.mode, func(t *testing.T) { checkLoginDiagnostic(t, scenario) })
	}
}

func TestM365LoginReportsInvalidTokens(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"token-invalid", "token-expired", "token-roles"} {
		t.Run(mode, func(t *testing.T) {
			checkLoginDiagnostic(t, loginScenario{mode: mode, want: "provider login failed: token validation failed", tokens: 1})
		})
	}
}

// The sign-in writes to a staging store, so a Keychain that refuses the write fails after the email check with
// the fixed save message.
func TestM365LoginReportsProfileAndKeychainFailures(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"profile-http", "profile-invalid"} {
		t.Run(mode, func(t *testing.T) {
			checkLoginDiagnostic(t, loginScenario{mode: mode, want: "provider login failed: profile lookup failed", tokens: 1, profiles: 1})
		})
	}
	t.Run("keychain", func(t *testing.T) {
		checkLoginDiagnostic(t, loginScenario{mode: "keychain", want: "credential save failed", tokens: 1, profiles: 1, saves: 1})
	})
}

func TestM365LoginReportsTokenExchangeFailures(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"token-error", "token-http", "token-json"} {
		t.Run(mode, func(t *testing.T) {
			checkLoginDiagnostic(t, loginScenario{mode: mode, want: "provider login failed: token exchange failed", tokens: 1})
		})
	}
}
