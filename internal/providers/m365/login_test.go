package m365_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
)

func TestDeviceLoginPendingSlowDownAndConsentFailure(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "denied"}[denied], func(t *testing.T) {
			s := validStore()
			polls := 0
			var waits []time.Duration
			var promptURI, promptCode string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/devicecode":
					_ = r.ParseForm()
					if r.Form.Get("client_id") != "client" || r.Form.Get("scope") != "https://graph.microsoft.com/User.Read https://graph.microsoft.com/Mail.ReadWrite https://graph.microsoft.com/Calendars.Read https://graph.microsoft.com/Calendars.ReadWrite https://graph.microsoft.com/MailboxSettings.Read offline_access openid profile" {
						t.Error("bad device request")
					}
					jsonResponse(w, map[string]any{"device_code": "DEVICE-SECRET", "user_code": "USER-CODE", "verification_uri": "https://login.example.test/device", "expires_in": 600, "interval": 1})
				case "/token":
					_ = r.ParseForm()
					if r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:device_code" || r.Form.Get("device_code") != "DEVICE-SECRET" {
						t.Error("bad device poll")
					}
					polls++
					switch {
					case denied:
						w.WriteHeader(400)
						jsonResponse(w, map[string]string{"error": "authorization_declined", "error_description": "SECRET"})
					case polls == 1:
						w.WriteHeader(400)
						jsonResponse(w, map[string]string{"error": "authorization_pending"})
					case polls == 2:
						w.WriteHeader(400)
						jsonResponse(w, map[string]string{"error": "slow_down"})
					default:
						jsonResponse(w, map[string]any{"access_token": goodToken(), "expires_in": 3600, "refresh_token": "refresh"})
					}
				case "/v1.0/me":
					jsonResponse(w, map[string]string{"mail": "device@example.test"})
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
				}
			}))
			defer server.Close()
			c := m365.New(account(), s, m365.Options{HTTPClient: server.Client(), AuthorityURL: server.URL, BaseURL: server.URL + "/v1.0", Now: func() time.Time { return now }, Wait: func(ctx context.Context, d time.Duration) error { waits = append(waits, d); return ctx.Err() }, OpenBrowser: func(context.Context, string) error { t.Error("device opened browser"); return nil }})
			user, _, err := c.Login(context.Background(), true, func(uri, code string) { promptURI, promptCode = uri, code })
			if promptURI != "https://login.example.test/device" || promptCode != "USER-CODE" {
				t.Fatal("wrong prompt or leaked device secret")
			}
			if denied {
				if err == nil || err.Error() != "AuthError" || polls != 1 || s.saves != 0 {
					t.Fatalf("consent failure retried/saved %v %d %d", err, polls, s.saves)
				}
				return
			}
			if err != nil || user != "device@example.test" || s.saves != 1 || polls != 3 || len(waits) != 3 || waits[0] != time.Second || waits[2] != 6*time.Second {
				t.Fatalf("device flow %s %v %v", user, err, waits)
			}
		})
	}
}
func TestBrowserLoginConsentErrorsAndCancellationNeverPersist(t *testing.T) {
	for _, mode := range []string{"denied", "timeout", "browser-error"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
			defer server.Close()
			s := validStore()
			browser := func(ctx context.Context, raw string) error {
				deadline, ok := ctx.Deadline()
				if !ok || time.Until(deadline) > 300*time.Second {
					t.Error("login deadline missing")
				}
				if mode == "browser-error" {
					return errors.New("SECRET browser error")
				}
				if mode == "timeout" {
					return nil
				}
				u, _ := url.Parse(raw)
				q := u.Query()
				req, _ := http.NewRequestWithContext(ctx, "GET", q.Get("redirect_uri")+"?state="+url.QueryEscape(q.Get("state"))+"&error=access_denied&error_description=SECRET", nil)
				h := &http.Client{Timeout: time.Second}
				resp, err := h.Do(req)
				if err != nil {
					return err
				}
				return resp.Body.Close()
			}
			c := m365.New(account(), s, m365.Options{HTTPClient: server.Client(), AuthorityURL: server.URL, BaseURL: server.URL + "/v1.0", OpenBrowser: browser})
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if _, _, err := c.Login(ctx, false, nil); err == nil || err.Error() != "AuthError" {
				t.Fatalf("unsafe login error %v", err)
			}
			if s.saves != 0 || calls.Load() != 0 {
				t.Fatal("failed login exchanged or persisted credentials")
			}
		})
	}
}
func TestLoginRejectsUnusableTokenBeforeProfileOrSave(t *testing.T) {
	for _, token := range []string{"opaque", jwt(map[string]any{"scp": ""}), jwt(map[string]any{"scp": "User.Read", "roles": "Mail.Read"})} {
		s := validStore()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/devicecode":
				jsonResponse(w, map[string]any{"device_code": "device", "user_code": "user", "verification_uri": "https://synthetic.invalid", "expires_in": 60, "interval": 1})
			case "/token":
				jsonResponse(w, map[string]any{"access_token": token, "expires_in": 3600})
			default:
				t.Error("unsafe access token reached profile")
			}
		}))
		c := m365.New(account(), s, m365.Options{HTTPClient: server.Client(), AuthorityURL: server.URL, BaseURL: server.URL + "/v1.0", Wait: func(context.Context, time.Duration) error { return nil }})
		if _, _, err := c.Login(context.Background(), true, func(string, string) {}); err == nil || s.saves != 0 {
			t.Fatal("unsafe login token accepted")
		}
		server.Close()
	}
}
func TestBrowserLoginPKCEStateAndCheckedCredentialPersistence(t *testing.T) {
	s := validStore()
	var challenge, redirect, state string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			hash := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if r.Form.Get("grant_type") != "authorization_code" || r.Form.Get("code") != "authorized" || r.Form.Get("redirect_uri") != redirect || base64.RawURLEncoding.EncodeToString(hash[:]) != challenge {
				t.Error("bad PKCE exchange")
			}
			jsonResponse(w, map[string]any{"access_token": goodToken(), "refresh_token": "new-refresh", "expires_in": 3600})
		case "/v1.0/me":
			if r.Header.Get("Authorization") != "Bearer "+goodToken() || r.Header.Get("Prefer") != `IdType="ImmutableId"` {
				t.Error("profile token not checked")
			}
			jsonResponse(w, map[string]string{"userPrincipalName": "login@example.test"})
		default:
			t.Errorf("unexpected OAuth path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	browser := func(ctx context.Context, raw string) error {
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		q := u.Query()
		challenge = q.Get("code_challenge")
		redirect = q.Get("redirect_uri")
		state = q.Get("state")
		if q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || len(state) < 32 || len(challenge) < 32 || q.Get("scope") != "https://graph.microsoft.com/User.Read https://graph.microsoft.com/Mail.ReadWrite https://graph.microsoft.com/Calendars.Read https://graph.microsoft.com/Calendars.ReadWrite https://graph.microsoft.com/MailboxSettings.Read offline_access openid profile" {
			t.Error("unsafe authorize request")
		}
		cb, err := url.Parse(redirect)
		if err != nil {
			return err
		}
		if cb.Hostname() != "localhost" {
			t.Error("callback must preserve the source localhost app registration")
		}
		h := &http.Client{Timeout: 2 * time.Second}
		for _, st := range []string{"wrong-state", state} {
			req, _ := http.NewRequestWithContext(ctx, "GET", redirect+"?state="+url.QueryEscape(st)+"&code=authorized", nil)
			resp, err := h.Do(req)
			if err != nil {
				return err
			}
			_ = resp.Body.Close()
			if st == "wrong-state" && resp.StatusCode != 400 {
				t.Error("bad state accepted")
			}
			if st == state && resp.StatusCode != 200 {
				t.Error("good state rejected")
			}
		}
		return nil
	}
	c := m365.New(account(), s, m365.Options{HTTPClient: server.Client(), AuthorityURL: server.URL, BaseURL: server.URL + "/v1.0", Now: func() time.Time { return now }, OpenBrowser: browser})
	user, scopes, err := c.Login(context.Background(), false, nil)
	if err != nil || user != "login@example.test" || len(scopes) != 7 || s.saves != 1 || s.token.RefreshToken != "new-refresh" {
		t.Fatalf("login %s %v %v saves=%d", user, scopes, err, s.saves)
	}
	if _, _, err := c.Status(context.Background()); err != nil || s.loads != 0 {
		t.Fatal("login token not cached")
	}
}
