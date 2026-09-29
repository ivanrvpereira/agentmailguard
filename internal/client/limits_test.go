package client_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/client"
)

func TestLongOrganizerDeadlineAndBoundedResponses(t *testing.T) {
	for _, timeout := range []time.Duration{0, 2 * time.Hour} {
		c, e := client.New("http://synthetic.invalid", client.Options{Timeout: timeout, HTTPClient: &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
			deadline, ok := r.Context().Deadline()
			minimum := 29 * time.Minute
			if timeout != 0 {
				minimum = 119 * time.Minute
			}
			if !ok || time.Until(deadline) < minimum {
				t.Error("hidden short whole-operation deadline")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`)), Header: make(http.Header)}, nil
		})}})
		if e != nil {
			t.Fatal(e)
		}
		if _, e := c.Call(context.Background(), "mail.organize", "", nil); e != nil {
			t.Fatal(e)
		}
	}
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		chunk := strings.Repeat("x", 64<<10)
		for range client.MaxResponseBytes/(64<<10) + 1 {
			if _, e := io.WriteString(w, chunk); e != nil {
				return
			}
		}
	}))
	defer h.Close()
	c, e := client.New(h.URL, client.Options{})
	if e != nil {
		t.Fatal(e)
	}
	if _, e := c.Call(context.Background(), "stats", "", nil); e == nil || e.Error() != "invalid server response" {
		t.Fatal("unbounded response accepted")
	}
}
