package client_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/client"
)

func TestJSONClientNoRedirectOrRawErrors(t *testing.T) {
	var requests atomic.Int32
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		b, _ := io.ReadAll(r.Body)
		if r.Method != "POST" || r.URL.Path != "/v1/call" || !strings.Contains(string(b), `"operation":"mail.archive"`) {
			t.Error("incorrect call")
		}
		w.Header().Set("Location", "/canary")
		w.WriteHeader(307)
		io.WriteString(w, "private-body-canary")
	}))
	defer h.Close()
	c, e := client.New(h.URL, client.Options{})
	if e != nil {
		t.Fatal(e)
	}
	_, e = c.Call(context.Background(), "mail.archive", "", map[string]any{"ids": []string{"x"}})
	if e == nil || strings.Contains(e.Error(), "canary") || requests.Load() != 1 {
		t.Fatal("redirect or unsafe failure")
	}
	for _, bad := range []string{"ftp://example.test", "http://user:secret@example.test", "http://example.test?token=secret", "http://example.test/#secret", "//example.test"} {
		if _, e := client.New(bad, client.Options{}); e == nil || strings.Contains(e.Error(), "secret") {
			t.Fatal("unsafe URL")
		}
	}
	for _, v := range []any{c, *c, client.Options{}} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
			if strings.Contains(fmt.Sprintf(verb, v), h.URL) {
				t.Fatal("URL formatting leak")
			}
		}
	}
}
func TestClientCancellationAndRequestReplayDisabled(t *testing.T) {
	c, e := client.New("http://synthetic.invalid", client.Options{HTTPClient: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		if r.GetBody != nil || r.ContentLength < 1 {
			t.Error("request can be replayed")
		}
		<-r.Context().Done()
		return nil, fmt.Errorf("private-network-canary")
	})}, Timeout: 20 * time.Millisecond})
	if e != nil {
		t.Fatal(e)
	}
	_, e = c.Call(context.Background(), "mail.trash", "", map[string]any{"ids": []string{"x"}})
	if e == nil || strings.Contains(e.Error(), "canary") {
		t.Fatal("unsafe error")
	}
}

func TestClientDisplaysOnlyExactFixedRefusals(t *testing.T) {
	for _, fixed := range []string{
		"move refused: use mail.trash",
		"calendar writes are not supported for this account",
		"calendar availability is not supported for this account",
		"calendar write refused: recurring events are not supported",
		"calendar write refused: event is cancelled",
		"calendar write refused: only the organizer can update or cancel",
		"calendar response refused: organizer cannot respond",
		"missing_scopes",
	} {
		for _, suffix := range []string{"", " CANARY private"} {
			t.Run(fixed+suffix, func(t *testing.T) {
				h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(500)
					json.NewEncoder(w).Encode(map[string]string{"error": fixed + suffix})
				}))
				defer h.Close()
				c, err := client.New(h.URL, client.Options{})
				if err != nil {
					t.Fatal(err)
				}
				_, err = c.Call(context.Background(), "calendar.update", "work", map[string]any{"id": "e1", "subject": ""})
				want := fixed
				if suffix != "" {
					want = "server request failed"
				}
				if err == nil || err.Error() != want {
					t.Fatalf("error=%v want=%q", err, want)
				}
			})
		}
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
