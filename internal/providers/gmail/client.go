// Package gmail implements Gmail and primary Google Calendar without a permanent-delete path.
package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

const gmailScope = "https://www.googleapis.com/auth/gmail.modify"

// calendarScope reads and writes events on owned calendars; login requests it. calendarReadScope is the
// read-only grant of earlier logins, which still reads the calendar.
const calendarScope = "https://www.googleapis.com/auth/calendar.events.owned"
const calendarReadScope = "https://www.googleapis.com/auth/calendar.events.owned.readonly"

// CalendarWriteScope is the grant that calendar writes need.
const CalendarWriteScope = calendarScope
const responseLimit = 50 << 20

// Options supplies external-system seams (APIs, browser, terminal), not user settings. API URLs include the users/me or calendars/primary prefix; UploadURL is the drafts upload endpoint and SendUploadURL the messages.send upload endpoint.
type Options struct {
	HTTPClient                                                                     *http.Client
	GmailURL, UploadURL, SendUploadURL, CalendarURL, TokenURL, AuthURL, ProfileURL string
	Now                                                                            func() time.Time
	OpenBrowser                                                                    func(context.Context, string) error
	Sleep                                                                          func(context.Context, time.Duration) error
	// ReadRedirect returns the browser's redirected address, pasted on hosts the loopback callback cannot reach.
	// Login calls it again with retry set after an address that does not belong to this sign-in. It must return
	// when ctx ends: Login cancels ctx and waits for it before Login returns.
	ReadRedirect func(ctx context.Context, retry bool) (string, error)
}

type Client struct {
	account   config.Account
	store     credentials.Store
	options   Options
	http      *http.Client
	slots     chan struct{}
	tokenGate chan struct{}
	token     *credentials.OAuth
}

func (Client) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "GmailClient{redacted}") }

// New is lazy: it does not access credentials or the network.
func New(account config.Account, store credentials.Store, opts Options) *Client {
	defaults := []struct {
		p *string
		v string
	}{
		{&opts.GmailURL, "https://gmail.googleapis.com/gmail/v1/users/me"},
		{&opts.UploadURL, "https://gmail.googleapis.com/upload/gmail/v1/users/me/drafts"},
		{&opts.SendUploadURL, "https://gmail.googleapis.com/upload/gmail/v1/users/me/messages/send"},
		{&opts.CalendarURL, "https://www.googleapis.com/calendar/v3/calendars/primary"},
		{&opts.TokenURL, "https://oauth2.googleapis.com/token"},
		{&opts.AuthURL, "https://accounts.google.com/o/oauth2/auth"},
		{&opts.ProfileURL, "https://gmail.googleapis.com/gmail/v1/users/me/profile"},
	}
	for _, d := range defaults {
		if *d.p == "" {
			*d.p = d.v
		}
		*d.p = strings.TrimRight(*d.p, "/")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Sleep == nil {
		opts.Sleep = func(ctx context.Context, d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
	}
	if opts.OpenBrowser == nil {
		opener := "xdg-open"
		if runtime.GOOS == "darwin" {
			opener = "open"
		}
		opts.OpenBrowser = func(ctx context.Context, uri string) error { return exec.CommandContext(ctx, opener, uri).Run() }
	}
	h := http.Client{}
	if opts.HTTPClient != nil {
		h = *opts.HTTPClient
	}
	h.Timeout = 30 * time.Second
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{account: account, store: store, options: opts, http: &h, slots: make(chan struct{}, 8), tokenGate: make(chan struct{}, 1)}
}
func failure(name, code string) error { return model.MailboxError(name + " error " + code) }
func safeContext(ctx context.Context, name string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return failure(name, "RequestFailed")
}

// exchange performs exactly one bounded request, including for mutation bodies. No redirect or automatic body replay is allowed.
func (c *Client) exchange(ctx context.Context, method, endpoint, token, contentType string, body []byte) (int, http.Header, []byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return 0, nil, nil, failure("Gmail", "RequestFailed")
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, failure("Gmail", "RequestFailed")
	}
	req.GetBody = nil // Go's Transport must not replay writes after a stale keep-alive failure.
	if method != "GET" && len(body) == 0 {
		// NoBody/length zero permits replay even without GetBody. Keep the wire body empty.
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = -1
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, nil, safeContext(ctx, "Gmail")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, responseLimit+1))
	if err != nil || len(data) > responseLimit {
		return 0, nil, nil, safeContext(ctx, "Gmail")
	}
	return resp.StatusCode, resp.Header, data, nil
}

var identifier = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

func reason(data []byte) string {
	var v struct {
		Error struct {
			Status string
			Errors []struct{ Reason string }
		}
	}
	if json.Unmarshal(data, &v) != nil {
		return ""
	}
	s := v.Error.Status
	if len(v.Error.Errors) > 0 && v.Error.Errors[0].Reason != "" {
		s = v.Error.Errors[0].Reason
	}
	if len(s) > 128 || !identifier.MatchString(s) {
		return ""
	}
	return s
}
func statusError(name string, status int, data []byte) error {
	s := strconv.Itoa(status)
	if r := reason(data); r != "" {
		s += " " + r
	}
	return failure(name, s)
}
func object(data []byte, name string) (map[string]json.RawMessage, error) {
	var v map[string]json.RawMessage
	if json.Unmarshal(data, &v) != nil || v == nil {
		return nil, failure(name, "ValueError")
	}
	return v, nil
}
func (c *Client) request(ctx context.Context, name, method, endpoint string, q url.Values, body []byte, contentType string) (map[string]json.RawMessage, error) {
	// Login holds tokenGate while fetching profile: never wait for credentials with an API slot.
	token, err := c.credential(ctx)
	if err != nil {
		return nil, err
	}
	return c.requestWithToken(ctx, name, method, endpoint, q, body, contentType, &token, c.credential)
}
func (c *Client) requestWithToken(ctx context.Context, name, method, endpoint string, q url.Values, body []byte, contentType string, token *credentials.OAuth, refresh func(context.Context) (credentials.OAuth, error)) (map[string]json.RawMessage, error) {
	admitted := false
	defer func() {
		if admitted {
			<-c.slots
		}
	}()
	if len(q) > 0 {
		endpoint += "?" + q.Encode()
	}
	for attempt := 0; ; attempt++ {
		for {
			if !admitted {
				select {
				case c.slots <- struct{}{}:
					admitted = true
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if c.options.Now().Before(token.ExpiresAt) {
				break
			}
			// Login holds tokenGate while awaiting profile admission. Release before refresh.
			<-c.slots
			admitted = false
			if refresh == nil {
				// Login's transient identity is not saved yet; never replace it from the store.
				return nil, failure(name, "AuthError")
			}
			fresh, err := refresh(ctx)
			if err != nil {
				return nil, err
			}
			*token = fresh
		}
		status, headers, data, err := c.exchange(ctx, method, endpoint, token.AccessToken, contentType, body)
		if err != nil {
			return nil, safeContext(ctx, name)
		}
		if status == http.StatusNoContent {
			// Calendar deletes answer with no body.
			return map[string]json.RawMessage{}, nil
		}
		if status >= 200 && status < 300 {
			return object(data, name)
		}
		r := reason(data)
		limited := status == 429 || status >= 500 || (status == 403 && (r == "rateLimitExceeded" || r == "userRateLimitExceeded"))
		if method != "GET" || !limited || attempt >= 6 {
			return nil, statusError(name, status, data)
		}
		delay := float64(int64(1) << attempt)
		if seconds, e := strconv.ParseFloat(headers.Get("Retry-After"), 64); e == nil && !math.IsNaN(seconds) {
			delay = math.Max(0, math.Min(seconds, 32))
		}
		if err = c.options.Sleep(ctx, time.Duration(delay*float64(time.Second))); err != nil {
			return nil, safeContext(ctx, name)
		}
	}
}
func (c *Client) get(ctx context.Context, name, endpoint string, q url.Values) (map[string]json.RawMessage, error) {
	return c.request(ctx, name, "GET", endpoint, q, nil, "")
}
func (c *Client) write(ctx context.Context, method, endpoint string, body any) (map[string]json.RawMessage, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, failure("Gmail", "ValueError")
	}
	return c.request(ctx, "Gmail", method, endpoint, nil, data, "application/json")
}
func stringField(v map[string]json.RawMessage, k string) string {
	var s string
	_ = json.Unmarshal(v[k], &s)
	return s
}
func pageToken(v map[string]json.RawMessage, seen map[string]bool, name string) (string, error) {
	raw, ok := v["nextPageToken"]
	if !ok || string(raw) == "null" {
		return "", nil
	}
	var s string
	if json.Unmarshal(raw, &s) != nil || s == "" || seen[s] {
		return "", failure(name, "InvalidPage")
	}
	seen[s] = true
	return s, nil
}
func items(v map[string]json.RawMessage, k, name string) ([]json.RawMessage, error) {
	raw, ok := v[k]
	if !ok {
		return []json.RawMessage{}, nil
	}
	var a []json.RawMessage
	if json.Unmarshal(raw, &a) != nil || a == nil {
		return nil, failure(name, "InvalidPage")
	}
	return a, nil
}
func segment(s string) string {
	if s == "." {
		return "%2E"
	}
	if s == ".." {
		return "%2E%2E"
	}
	return url.PathEscape(s)
}
