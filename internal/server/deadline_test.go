package server_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

type deadlineWriter struct {
	*httptest.ResponseRecorder
	deadline, emittedDeadline time.Time
	emitted                   bool
}

func (w *deadlineWriter) SetReadDeadline(time.Time) error    { return nil }
func (w *deadlineWriter) SetWriteDeadline(d time.Time) error { w.deadline = d; return nil }
func (w *deadlineWriter) observe() {
	if !w.emitted {
		w.emitted = true
		w.emittedDeadline = w.deadline
	}
}
func (w *deadlineWriter) WriteHeader(status int) {
	w.observe()
	w.ResponseRecorder.WriteHeader(status)
}
func (w *deadlineWriter) Write(p []byte) (int, error) {
	w.observe()
	return w.ResponseRecorder.Write(p)
}
func checkResponseDeadline(t *testing.T, s http.Handler, req *http.Request, status int) {
	t.Helper()
	w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	s.ServeHTTP(w, req)
	if w.Code != status || w.emittedDeadline.IsZero() {
		t.Fatalf("status=%d want=%d write deadline present=%t", w.Code, status, !w.emittedDeadline.IsZero())
	}
	if left := time.Until(w.emittedDeadline); left < 29*time.Second || left > 31*time.Second {
		t.Fatalf("response deadline is not 30 seconds: %s", left)
	}
}

func TestResponsesHaveWriteDeadlineBeforeEmission(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body, contentType string
		status                                int
	}{
		{"success", "POST", "/v1/call", `{"operation":"stats","params":{}}`, "application/json", 200},
		{"path", "POST", "/elsewhere", `{}`, "application/json", 404},
		{"method", "GET", "/v1/call", `{}`, "application/json", 405},
		{"origin", "POST", "/v1/call", `{}`, "application/json", 403},
		{"type", "POST", "/v1/call", `{}`, "text/plain", 415},
		{"schema", "POST", "/v1/call", `{}`, "application/json", 400},
		{"operation", "POST", "/v1/call", `{"operation":"mail.send","params":{}}`, "application/json", 400},
		{"config", "POST", "/v1/call", `{"operation":"auth.status","params":{}}`, "application/json", 500},
		{"panic", "POST", "/v1/call", `{"operation":"stats","params":{}}`, "application/json", 500},
		{"closed", "POST", "/v1/call", `{"operation":"stats","params":{}}`, "application/json", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := server.Options{Credentials: &hostileStore{}}
			if tc.name == "panic" {
				opts.Now = func() time.Time { panic("private-clock-canary") }
			}
			s, err := server.New(context.Background(), t.TempDir(), opts)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if tc.name == "closed" {
				if err := s.Close(); err != nil {
					t.Fatal(err)
				}
			}
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			req.Header.Set("Content-Type", tc.contentType)
			if tc.name == "origin" {
				req.Header.Set("Origin", "https://example.test")
			}
			checkResponseDeadline(t, s, req, tc.status)
		})
	}
}

func TestBusyResponseHasWriteDeadline(t *testing.T) {
	started := make(chan struct{}, 64)
	release := make(chan struct{})
	s, err := server.New(context.Background(), t.TempDir(), server.Options{Credentials: &hostileStore{}, Now: func() time.Time { started <- struct{}{}; <-release; return time.Now() }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var wg sync.WaitGroup
	defer func() { close(release); wg.Wait() }()
	request := func() *http.Request {
		r := httptest.NewRequest("POST", "/v1/call", strings.NewReader(`{"operation":"stats","params":{}}`))
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	for range 64 {
		wg.Go(func() { s.ServeHTTP(httptest.NewRecorder(), request()) })
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for range 64 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("requests did not reach clock seam")
		}
	}
	checkResponseDeadline(t, s, request(), 503)
}

func TestResponseDeadlineDoesNotLimitOperation(t *testing.T) {
	w := &deadlineWriter{ResponseRecorder: httptest.NewRecorder()}
	s, err := server.New(context.Background(), t.TempDir(), server.Options{Credentials: &hostileStore{}, Now: func() time.Time {
		if !w.deadline.IsZero() {
			t.Error("response write deadline started before operation completed")
		}
		return time.Now()
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	req := httptest.NewRequest("POST", "/v1/call", strings.NewReader(`{"operation":"stats","params":{}}`))
	req.Header.Set("Content-Type", "application/json")
	s.ServeHTTP(w, req)
	if w.Code != 200 || w.emittedDeadline.IsZero() {
		t.Fatal("operation did not finish with a bounded response")
	}
}
