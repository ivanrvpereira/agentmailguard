// Package client sends a single bounded JSON call to the daemon without local mailbox state or retries.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const DefaultServer = "http://127.0.0.1:8787"
const DefaultTimeout = 30 * time.Minute
const MaxResponseBytes = 64 << 20

type Options struct {
	HTTPClient *http.Client
	Timeout    time.Duration
}

func (Options) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "ClientOptions{redacted}") }

type Client struct {
	url     string
	http    http.Client
	timeout time.Duration
}

func (Client) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "Client{redacted}") }

// Error contains fixed local text only, never the remote body or transport error.
type Error struct {
	Status  int
	message string
}

func (e *Error) Error() string { return e.message }
func New(base string, opts Options) (*Client, error) {
	u, e := url.Parse(base)
	if e != nil || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(base, "#") || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("invalid daemon URL")
	}
	if opts.Timeout < 0 {
		return nil, errors.New("invalid client timeout")
	}
	if opts.Timeout == 0 {
		opts.Timeout = DefaultTimeout
	}
	h := http.Client{}
	if opts.HTTPClient != nil {
		h = *opts.HTTPClient
	}
	h.Timeout = opts.Timeout
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{url: strings.TrimRight(u.String(), "/") + "/v1/call", http: h, timeout: opts.Timeout}, nil
}
func (c *Client) Call(ctx context.Context, operation, account string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if params == nil {
		params = struct{}{}
	}
	data, e := json.Marshal(struct {
		Operation string `json:"operation"`
		Account   string `json:"account,omitempty"`
		Params    any    `json:"params"`
	}{operation, account, params})
	if e != nil || len(data) > 1<<20 {
		return nil, errors.New("invalid request")
	}
	req, e := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(data))
	if e != nil {
		return nil, errors.New("invalid daemon URL")
	}
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, e := c.http.Do(req)
	if e != nil {
		return nil, errors.New("daemon request failed")
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if e != nil || len(b) > MaxResponseBytes {
		return nil, errors.New("invalid daemon response")
	}
	if resp.StatusCode != 200 {
		message := "daemon request failed"
		// Only exact protocol refusals are safe to display. All other bodies are discarded.
		var v struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(b, &v) == nil {
			switch v.Error {
			case "invalid request", "operation failed", "no accounts; run amgd account add", "unknown account; run amg account list", "sign-in failed; check the network, or run amgd account login", "daemon unavailable", "daemon busy", "browser requests refused", "move refused: the destination must be a folder ID from `folders`", "move refused: use mail.trash", "refused: messages in the trash stay there", "flag state must be one of flagged, complete, none", "categorize needs at least one category to add or remove", "categorize refused: not in the category list",
				"link refused: message is red, not green", "link refused: message is yellow, not green", "link refused: message is sealed, not green", "link refused: message is error, not green",
				"search needs at least one of text, from, to, subject, after, before",
				"calendar writes are not supported for this account", "calendar availability is not supported for this account", "missing_scopes",
				"calendar write refused: recurring events are not supported", "calendar write refused: event is cancelled",
				"calendar write refused: only the organizer can update or cancel", "calendar response refused: organizer cannot respond",
				"calendar cancel refused: comments are not supported for this account", "draft update refused: not a draft",
				"draft delete refused: not a draft", "draft send refused: not a draft",
				"calendar response refused: responses without notification are not supported for this account",
				"refused by policy: mail_draft", "refused by policy: mail_send", "refused by policy: calendar_invite", "refused by policy: calendar_update", "refused by policy: calendar_cancel", "refused by policy: calendar_respond",
				"approval not found":
				message = v.Error
			}
		}
		return nil, &Error{Status: resp.StatusCode, message: message}
	}
	if !json.Valid(b) {
		return nil, errors.New("invalid daemon response")
	}
	return json.RawMessage(b), nil
}
