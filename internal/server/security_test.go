package server_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard-go/internal/server"
)

type hostileStore struct {
	credentials.Store
	loads   atomic.Int32
	mode    string
	started chan struct{}
}

func (s *hostileStore) LoadOAuth(ctx context.Context, _, _ string) (credentials.OAuth, error) {
	s.loads.Add(1)
	switch s.mode {
	case "panic":
		panic("private-panic-canary")
	case "wait":
		close(s.started)
		<-ctx.Done()
		return credentials.OAuth{}, ctx.Err()
	default:
		return credentials.OAuth{}, credentials.ErrNotFound
	}
}
func TestHTTPBoundsBrowserAndDeniedSurface(t *testing.T) {
	home := t.TempDir()
	configFile(t, home)
	creds := &hostileStore{}
	s, e := server.New(context.Background(), home, server.Options{Credentials: creds})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	h := httptest.NewServer(s)
	defer h.Close()
	for _, tc := range []struct {
		method, path, typ, origin, body string
		status                          int
	}{
		{"GET", "/v1/call", "application/json", "", `{}`, 405},
		{"POST", "/v1/call", "text/plain", "", `{}`, 415},
		{"POST", "/v1/call", "application/json", "https://evil.test", `{"operation":"cache.clear","params":{}}`, 403},
		{"POST", "/v1/call?secret=x", "application/json", "", `{}`, 404},
		{"POST", "/v1/call", "application/json", "", strings.Repeat("x", server.MaxRequestBytes+1), 400},
		{"POST", "/v1/call", "application/json", "", `{"operation":"mail.get","params":{"id":"a","\u0069d":"b"}}`, 400},
		{"POST", "/v1/call", "application/json", "", `{"operation":"mail.get","params":{"id":123}}`, 400},
		{"POST", "/v1/call", "application/json", "", `{"operation":"mail.get","params":{"id":null}}`, 400},
		{"POST", "/v1/call", "application/json", "", `{"operation":"stats","account":null,"params":{}}`, 400},
		{"POST", "/v1/call", "application/json", "", `{"operation":"mail.list","params":{"skip":2147483647}}`, 400},
	} {
		req, _ := http.NewRequest(tc.method, h.URL+tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", tc.typ)
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		res, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != tc.status || res.Header.Get("Access-Control-Allow-Origin") != "" || strings.Contains(string(b), "secret") {
			t.Errorf("status %d expected %d body %s", res.StatusCode, tc.status, b)
		}
	}
	for _, op := range []string{"auth.login", "auth.token", "rules.set", "mail.send", "mail.delete", "mail.permanentDelete", "mail.classify", "mail.raw", "calendar.raw", "calendar.create", "folder.create", "category.create", "graph.request"} {
		status, _ := rpc(t, h.URL, `{"operation":"`+op+`","params":{}}`)
		if status != 400 {
			t.Errorf("denied %s: %d", op, status)
		}
	}
	if creds.loads.Load() != 0 {
		t.Fatal("invalid input accessed credentials")
	}
}
func TestPanicCancellationAndSafeFormatting(t *testing.T) {
	for _, mode := range []string{"panic", "wait"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			configFile(t, home)
			creds := &hostileStore{mode: mode, started: make(chan struct{})}
			opts := server.Options{Credentials: creds}
			s, e := server.New(context.Background(), home, opts)
			if e != nil {
				t.Fatal(e)
			}
			defer s.Close()
			h := httptest.NewServer(s)
			defer h.Close()
			for _, v := range []any{s, *s, opts, &opts} {
				for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
					if strings.Contains(fmt.Sprintf(verb, v), "canary") {
						t.Fatal("nested secret exposed")
					}
				}
			}
			done := make(chan string, 1)
			go func() {
				resp, e := http.Post(h.URL+"/v1/call", "application/json", strings.NewReader(`{"operation":"auth.status","params":{}}`))
				if e != nil {
					done <- "network error"
					return
				}
				b, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				done <- string(b)
			}()
			if mode == "wait" {
				select {
				case <-creds.started:
				case <-time.After(time.Second):
					t.Fatal("request did not start")
				}
				if e := s.Close(); e != nil {
					t.Fatal(e)
				}
			}
			select {
			case b := <-done:
				if strings.Contains(b, "canary") {
					t.Fatal("panic payload exposed")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("request did not cancel")
			}
			if mode == "wait" {
				status, _ := rpc(t, h.URL, `{"operation":"stats","params":{}}`)
				if status != 503 {
					t.Fatal("closed server admitted request")
				}
			}
		})
	}
}
func TestOrganizeCalendarMonthsAndServerClock(t *testing.T) {
	for _, tc := range []struct {
		now, since, until, want string
		status                  int
	}{
		{"2024-03-31T23:00:00-07:00", "1m", "", "2024-03-01T00:00:00Z", 200},
		{"2024-03-31T12:00:00Z", "1m", "", "2024-02-29T00:00:00Z", 200},
		{"2023-03-31T12:00:00Z", "1m", "", "2023-02-28T00:00:00Z", 200},
		{"2026-03-09T01:00:00Z", "1d", "", "2026-03-08T00:00:00Z", 200},
		{"2026-03-31T12:00:00Z", "1w", "1d", "2026-03-24T00:00:00Z", 200},
		{"2026-03-31T12:00:00Z", "2026-03-31", "2026-03-31", "", 400},
		{"2026-03-31T12:00:00Z", "1d", "2d", "", 400},
		{"2026-03-31T12:00:00Z", "2026-04-01", "", "", 400},
		{"2026-03-31T12:00:00Z", "999999999999999999999m", "", "", 400},
	} {
		home := t.TempDir()
		configFile(t, home)
		now, _ := time.Parse(time.RFC3339, tc.now)
		s, e := server.New(context.Background(), home, server.Options{Credentials: &hostileStore{}, Now: func() time.Time { return now }})
		if e != nil {
			t.Fatal(e)
		}
		h := httptest.NewServer(s)
		body := `{"operation":"mail.organize","params":{"since":"` + tc.since + `"`
		if tc.until != "" {
			body += `,"until":"` + tc.until + `"`
		}
		status, b := rpc(t, h.URL, body+`}}`)
		if status != tc.status || (tc.want != "" && !strings.Contains(b, `"since":"`+tc.want+`"`)) {
			t.Errorf("%+v: %d %s", tc, status, b)
		}
		h.Close()
		s.Close()
	}
}
func TestRulesFailBeforeCredentialsAndMissingAuthStatus(t *testing.T) {
	home := t.TempDir()
	configFile(t, home)
	creds := &hostileStore{}
	s, e := server.New(context.Background(), home, server.Options{Credentials: creds})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	h := httptest.NewServer(s)
	defer h.Close()
	if e := os.WriteFile(filepath.Join(home, "rules.toml"), []byte("[accounts.unknown.x]\ncategory='X'\ninstructions='private-canary'\n"), 0600); e != nil {
		t.Fatal(e)
	}
	for _, op := range []string{"rules.list", "mail.organize"} {
		status, b := rpc(t, h.URL, `{"operation":"`+op+`","params":{}}`)
		if status != 500 || b != `{"error":"rules unavailable"}` {
			t.Fatalf("rules error %d %s", status, b)
		}
	}
	if creds.loads.Load() != 0 {
		t.Fatal("rules failure accessed credentials")
	}
	status, b := rpc(t, h.URL, `{"operation":"auth.status","params":{}}`)
	if status != 200 || b != `{"account":"work","ok":false,"logged_in":false}` {
		t.Fatalf("missing auth status %d %s", status, b)
	}
}
