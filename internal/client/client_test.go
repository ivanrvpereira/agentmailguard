package client_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/client"
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

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
