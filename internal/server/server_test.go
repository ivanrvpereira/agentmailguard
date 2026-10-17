package server_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

func TestCacheOnlyAndClosedSchema(t *testing.T) {
	s, err := server.New(context.Background(), t.TempDir(), server.Options{Credentials: &preloadStore{}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := httptest.NewServer(s)
	defer h.Close()
	for _, tc := range []struct {
		body   string
		status int
		want   string
	}{
		{`{"operation":"stats","params":{}}`, 200, `{}`},
		{`{"operation":"cache.clear","params":{}}`, 200, `{"deleted":0}`},
		{`{"operation":"stats","params":{"days":0}}`, 400, `{"error":"invalid request"}`},
		{`{"operation":"stats","params":{"Days":1}}`, 400, `{"error":"invalid request"}`},
		{`{"operation":"stats","operation":"cache.clear","params":{}}`, 400, `{"error":"invalid request"}`},
		{`{"operation":"stats","params":{}} {}`, 400, `{"error":"invalid request"}`},
		{`{"operation":"mail.delete","params":{}}`, 400, `{"error":"invalid request"}`},
		{`{"operation":"stats","params":null}`, 400, `{"error":"invalid request"}`},
	} {
		req, _ := http.NewRequest("POST", h.URL+"/v1/call", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.status || strings.TrimSpace(string(b)) != tc.want {
			t.Errorf("%s: status %d body %s", tc.body, resp.StatusCode, b)
		}
	}
}
