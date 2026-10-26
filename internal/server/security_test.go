package server_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

func TestHTTPBoundsBrowserAndDeniedSurface(t *testing.T) {
	var requests atomic.Int32
	d := graphDaemon(t, "", m365Grant, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.WriteHeader(500) }), newNativeAPI())
	h := httptest.NewServer(d.s)
	defer h.Close()
	for _, tc := range []struct {
		method, path, typ, origin, body string
		status                          int
	}{
		{"GET", "/v1/call", "application/json", "", `{}`, 405},
		{"POST", "/v1/call", "text/plain", "", `{}`, 415},
		{"POST", "/v1/call", "application/json", "https://evil.test", `{"operation":"stats","params":{}}`, 403},
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
	for _, op := range []string{"auth.login", "auth.token", "auth.status", "account.add", "account.login", "account.remove", "cache.clear", "config.set", "rules.set", "mail.delete", "mail.permanentDelete", "mail.classify", "mail.raw", "calendar.raw", "calendar.create", "folder.create", "category.create", "graph.request"} {
		status, _ := rpc(t, h.URL, `{"operation":"`+op+`","params":{}}`)
		if status != 400 {
			t.Errorf("denied %s: %d", op, status)
		}
	}
	if requests.Load() != 0 {
		t.Fatal("invalid input reached the provider")
	}
}

// The server, its options, and its store print no secret, and Close cancels a call that waits on the provider.
func TestCloseCancelsARunningCallAndFormattingIsSafe(t *testing.T) {
	started := make(chan struct{}, 1)
	graph := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	})
	api := httptest.NewServer(graph)
	defer api.Close()
	opts := server.Options{M365: m365Options(api.URL)}
	d := startDaemon(t, daemonSetup{config: workConfig + fakeScreen, record: record(map[string]credentials.OAuth{"m365/" + workEmail: m365Login(m365Grant)}), options: opts})
	opts.Store = d.store
	for _, v := range []any{d.s, opts, &opts, d.store} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			if text := fmt.Sprintf(verb, v); strings.Contains(text, "canary") || strings.Contains(text, "screening-key") {
				t.Fatalf("formatting exposed a secret: %s", text)
			}
		}
	}
	h := httptest.NewServer(d.s)
	defer h.Close()
	done := make(chan string, 1)
	go func() {
		resp, e := http.Post(h.URL+"/v1/call", "application/json", strings.NewReader(`{"operation":"mail.folders","params":{}}`))
		if e != nil {
			done <- "network error"
			return
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		done <- string(b)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach the provider")
	}
	if e := d.s.Close(); e != nil {
		t.Fatal(e)
	}
	select {
	case b := <-done:
		if strings.Contains(b, "canary") {
			t.Fatal("response exposed a secret")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request did not cancel")
	}
	if status, _ := rpc(t, h.URL, `{"operation":"stats","params":{}}`); status != 503 {
		t.Fatal("closed server admitted request")
	}
}

// The CLI resolves organize days; the daemon takes exact RFC 3339 times, refuses a since after its clock and an
// until that is not after since, and computes no days.
func TestOrganizeTakesExactTimesAndRefusesDays(t *testing.T) {
	now := time.Date(2026, 3, 31, 12, 0, 0, 0, time.UTC)
	d := startDaemon(t, daemonSetup{config: workConfig + fakeScreen, options: server.Options{Now: func() time.Time { return now }}})
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
		status, b := accountRPC(t, d.s, "mail.organize", "", tc.params)
		if status != tc.status || (tc.want != "" && !strings.Contains(b, `"since":"`+tc.want+`"`)) {
			t.Errorf("%s: %d %s", tc.params, status, b)
		}
	}
}
