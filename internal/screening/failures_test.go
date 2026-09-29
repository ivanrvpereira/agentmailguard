package screening_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
	"github.com/ivanrvpereira/agentmailguard-go/internal/screening"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}
func safeFailure(t *testing.T, r model.Classification) {
	t.Helper()
	if r.Screening == nil || r.Screening.Verdict != model.Error || r.Screening.Score != nil || r.Screening.Error == nil || model.ValidateFailureText(r.Screening.Error) != nil {
		t.Fatalf("unsafe failure: %+v", r)
	}
	if model.ValidateFailureText(r.Error) != nil {
		t.Fatal("unsafe organization error")
	}
}
func TestWholeRequestFailuresFailClosedEvenOrgOnly(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		want       string
	}{
		{"API", `SECRET RESPONSE`, 429, "TypeSafeAPIError 429"},
		{"invalid JSON", `SECRET RESPONSE`, 200, "ValidationError"},
		{"missing model", `{"usage":{},"answers":{}}`, 200, "ValidationError"},
		{"missing usage", `{"model":"jev","answers":{}}`, 200, "ValidationError"},
		{"null answers", `{"model":"jev","usage":{},"answers":null}`, 200, "ValidationError"},
		{"invalid type", reply(`"folder":{"choice":"finance"}`), 200, "ValidationError"},
		{"string score", reply(strings.Replace(validAnswers, `"noul":0.1`, `"noul":"0.1"`, 1)), 200, "ValidationError"},
		{"null probability", reply(strings.Replace(validAnswers, `"stay":0.2`, `"stay":null`, 1)), 200, "ValidationError"},
		{"bad usage", `{"model":"jev","usage":{"input_tokens":"SECRET"},"answers":{}}`, 200, "ValidationError"},
		{"malformed extra SDK score", reply(validAnswers + `,"extra":{"type":"score","score":1,"confidence":1,"legend":{"0":"low"},"probabilities":{"0":null}}`), 200, "ValidationError"},
		{"oversized", strings.Repeat("x", screening.MaxResponseBytes+1), 200, "TypeSafeAPIError"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls atomic.Int32
			s := screening.New(screening.Options{LookupEnv: envKey, Credentials: missingStore(), HTTPClient: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				r := response(c.body)
				r.StatusCode = c.status
				return r, nil
			})}})
			r, err := s.Classify(context.Background(), map[string]any{"body": "SECRET"}, false, orgQuestions())
			if err != nil {
				t.Fatal(err)
			}
			safeFailure(t, r)
			if *r.Screening.Error != c.want || r.Error == nil || *r.Error != c.want || calls.Load() != 1 {
				t.Fatalf("wrong safe error or retry: %+v count=%d", r, calls.Load())
			}
		})
	}
}
func TestEmptyQuestionsAndInvalidStateNeverMakeRequest(t *testing.T) {
	s := screening.New(screening.Options{LookupEnv: envKey, Credentials: missingStore(), HTTPClient: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) {
		t.Error("unexpected request")
		return nil, errors.New("SECRET")
	})}})
	r, err := s.Classify(context.Background(), nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	safeFailure(t, r)
	r, err = s.Classify(context.Background(), map[string]any{"bad": make(chan int)}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	safeFailure(t, r)
}

// Fail a reused socket before its second write, as can happen with a stale keep-alive connection.
type staleConn struct {
	net.Conn
	writes int
}

func (c *staleConn) Write(p []byte) (int, error) {
	c.writes++
	if c.writes > 1 {
		return 0, io.ErrClosedPipe
	}
	return c.Conn.Write(p)
}

func TestJevDoesNotReplayPOSTOnStaleConnection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = io.WriteString(w, reply(validAnswers))
	}))
	defer server.Close()
	transport := &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &staleConn{Conn: conn}, nil
	}}
	defer transport.CloseIdleConnections()
	s := screening.New(screening.Options{Endpoint: server.URL, LookupEnv: envKey, Credentials: missingStore(), HTTPClient: &http.Client{Transport: transport}})
	first, err := s.Screen(context.Background(), nil)
	if err != nil || first.Verdict != model.Green {
		t.Fatalf("initial request failed: %+v %v", first, err)
	}
	second, err := s.Screen(context.Background(), nil)
	if err != nil || second.Verdict != model.Error || second.Error == nil || *second.Error != "TypeSafeAPIError" {
		t.Fatalf("failed POST was retried instead of failing closed: %+v %v", second, err)
	}
}

func TestNoCredentialRedirects(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	client := origin.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return nil }
	s := screening.New(screening.Options{LookupEnv: envKey, Credentials: missingStore(), HTTPClient: client, Endpoint: origin.URL})
	r, err := s.Classify(context.Background(), nil, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	safeFailure(t, r)
	if leaked.Load() != 0 || *r.Screening.Error != "TypeSafeAPIError 307" {
		t.Fatal("redirect followed")
	}
}
func TestTimeoutAndCancellation(t *testing.T) {
	started := make(chan struct{}, 2)
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		started <- struct{}{}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	s := screening.New(screening.Options{LookupEnv: envKey, Credentials: missingStore(), HTTPClient: client, Timeout: 20 * time.Millisecond})
	r, err := s.Classify(context.Background(), nil, false, orgQuestions())
	if err != nil {
		t.Fatal(err)
	}
	safeFailure(t, r)
	if *r.Screening.Error != "TimeoutError" {
		t.Fatal(*r.Screening.Error)
	}
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := s.Screen(ctx, nil); done <- err }()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not returned: %v", err)
	}
}
func TestAtMostEightAndDeadlineStartsAtActualRequest(t *testing.T) {
	var active, peak atomic.Int32
	started := make(chan struct{}, 40)
	release := make(chan struct{})
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		deadline, ok := r.Context().Deadline()
		remaining := time.Until(deadline)
		if !ok || remaining > 30*time.Second || remaining < 29*time.Second {
			t.Errorf("request deadline %v", remaining)
		}
		started <- struct{}{}
		select {
		case <-release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return response(reply(validAnswers)), nil
	})}
	s := screening.New(screening.Options{LookupEnv: envKey, Credentials: missingStore(), HTTPClient: client})
	var wg sync.WaitGroup
	for range 40 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.Screen(context.Background(), nil)
			if err != nil || r.Verdict != model.Green {
				t.Error("parallel screening failed")
			}
		}()
	}
	for range 8 {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			close(release)
			t.Fatal("eight requests did not start")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Screen(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("queued cancellation ignored")
	}
	close(release)
	wg.Wait()
	if peak.Load() != 8 {
		t.Fatalf("peak=%d", peak.Load())
	}
}
func TestLazyKeyResolutionRetriesFailureCachesSuccess(t *testing.T) {
	var loads atomic.Int32
	store := fakeStore{load: func(context.Context) (string, error) {
		if loads.Add(1) == 1 {
			return "", errors.New("SECRET-CREDENTIAL")
		}
		return " keychain-key \n", nil
	}}
	client := &http.Client{Transport: transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer keychain-key" {
			t.Error("wrong key")
		}
		return response(reply(validAnswers)), nil
	})}
	s := screening.New(screening.Options{LookupEnv: func(string) string { return " " }, Credentials: store, HTTPClient: client})
	if loads.Load() != 0 {
		t.Fatal("eager keychain access")
	}
	first, err := s.Classify(context.Background(), nil, false, orgQuestions())
	if err != nil {
		t.Fatal(err)
	}
	safeFailure(t, first)
	if *first.Screening.Error != "ConfigError" {
		t.Fatal("credential text leaked")
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := s.Screen(context.Background(), nil)
			if err != nil || r.Verdict != model.Green {
				t.Error("key retry failed")
			}
		}()
	}
	wg.Wait()
	if loads.Load() != 2 {
		t.Fatalf("successful key not cached: %d", loads.Load())
	}
}
func TestScreenerFormattingDoesNotExposeResolvedKey(t *testing.T) {
	s := screening.New(screening.Options{LookupEnv: envKey, Credentials: missingStore(), HTTPClient: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return response(reply(validAnswers)), nil })}})
	if _, err := s.Screen(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%d"} {
		if strings.Contains(fmt.Sprintf(format, s), "synthetic-key") {
			t.Fatalf("key exposed through %s", format)
		}
	}
}

func TestEnvKeyPrecedesKeychainAndTransportErrorsAreSafe(t *testing.T) {
	store := fakeStore{load: func(context.Context) (string, error) { t.Error("Keychain accessed with env key"); return "", nil }}
	s := screening.New(screening.Options{LookupEnv: envKey, Credentials: store, HTTPClient: &http.Client{Transport: transportFunc(func(*http.Request) (*http.Response, error) { return nil, fmt.Errorf("SECRET-MAIL SECRET-KEY") })}})
	r, err := s.Classify(context.Background(), nil, false, orgQuestions())
	if err != nil {
		t.Fatal(err)
	}
	safeFailure(t, r)
	if *r.Screening.Error != "TypeSafeAPIError" {
		t.Fatal(*r.Screening.Error)
	}
}
