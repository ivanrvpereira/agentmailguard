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

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
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
		{"POST", "/v1/call", "application/json", "", `{"operation":"mail.list","params":{"count":9223372036854775808}}`, 400},
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
	for _, op := range []string{"auth.login", "auth.token", "rules.set", "mail.delete", "mail.permanentDelete", "mail.classify", "mail.raw", "calendar.raw", "calendar.create", "folder.create", "category.create", "graph.request"} {
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

// The CLI resolves organize days; the server takes exact RFC 3339 times, refuses a since after its clock and an
// until that is not after since, and computes no days.
func TestOrganizeTakesExactTimesAndRefusesDays(t *testing.T) {
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		params, want string
		status       int
	}{
		{`{"since":"2026-03-24T00:00:00-04:00"}`, "2026-03-24T04:00:00Z", 200},
		{`{"since":"2026-03-31T12:00:00Z"}`, "2026-03-31T12:00:00Z", 200},
		{`{"since":"2026-03-30T00:00:00+01:00","until":"2026-04-02T00:00:00+01:00"}`, "2026-03-29T23:00:00Z", 200},
		{`{"since":"2026-03-31T12:00:01Z"}`, "", 400},
		{`{"since":"2026-03-30T00:00:00Z","until":"2026-03-30T00:00:00Z"}`, "", 400},
		{`{"since":"2026-03-30T00:00:00Z","until":"2026-03-29T00:00:00Z"}`, "", 400},
		{`{"since":"0000-12-31T00:00:00Z"}`, "", 400},
		{`{}`, "", 400},
		{`{"since":"7d"}`, "", 400},
		{`{"since":"2026-03-24"}`, "", 400},
		{`{"since":"2026-03-24T00:00:00Z","until":"1d"}`, "", 400},
		{`{"since":"2026-03-24T00:00:00Z","until":"2026-03-30"}`, "", 400},
	} {
		home := t.TempDir()
		configFile(t, home)
		s, e := server.New(context.Background(), home, server.Options{Credentials: &hostileStore{}, Now: func() time.Time { return now }})
		if e != nil {
			t.Fatal(e)
		}
		h := httptest.NewServer(s)
		status, b := rpc(t, h.URL, `{"operation":"mail.organize","params":`+tc.params+`}`)
		if status != tc.status || (tc.want != "" && !strings.Contains(b, `"since":"`+tc.want+`"`)) {
			t.Errorf("%s: %d %s", tc.params, status, b)
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
	for op, params := range map[string]string{"rules.list": `{}`, "mail.organize": `{"since":"2020-01-01T00:00:00Z"}`} {
		status, b := rpc(t, h.URL, `{"operation":"`+op+`","params":`+params+`}`)
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
