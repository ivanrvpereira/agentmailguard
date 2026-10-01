package gmail

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func randomText() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", failure("Gmail", "AuthError")
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// Login runs on the server host only. The callback binds to IPv4 loopback and never redirects or logs authorization codes.
// A pasted redirect from Options.ReadRedirect gets the same checks as the callback; the first accepted one wins.
func (c *Client) Login(ctx context.Context, deviceCode bool, prompt func(uri, code string)) (string, []string, error) {
	if deviceCode {
		return "", nil, failure("Gmail", "DeviceCodeError")
	}
	ctx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	select {
	case c.tokenGate <- struct{}{}:
		defer func() { <-c.tokenGate }()
	case <-ctx.Done():
		return "", nil, ctx.Err()
	}
	if ctx.Err() != nil {
		return "", nil, ctx.Err()
	}
	if c.store == nil {
		return "", nil, failure("Gmail", "CredentialError")
	}
	verifier, err := randomText()
	if err != nil {
		return "", nil, err
	}
	state, err := randomText()
	if err != nil {
		return "", nil, err
	}
	challenge := sha256.Sum256([]byte(verifier))
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", nil, failure("Gmail", "AuthError")
	}
	redirect := "http://" + listener.Addr().String() + "/"
	type result struct {
		code string
		err  error
	}
	callback := make(chan result, 1)
	// accept reports whether a redirect belongs to this sign-in. A matching redirect that carries a provider error still ends the login.
	accept := func(host, path, rawQuery string) bool {
		q, err := url.ParseQuery(rawQuery)
		if err != nil || path != "/" || host != listener.Addr().String() || len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			return false
		}
		got := result{code: q.Get("code")}
		if len(q["code"]) != 1 || got.code == "" || q.Get("error") != "" {
			got.err = failure("Gmail", "AuthError")
		}
		select {
		case callback <- got:
		default:
		}
		return true
	}
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, MaxHeaderBytes: 8192}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if r.Method != "GET" || !accept(r.Host, r.URL.Path, r.URL.RawQuery) {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, "Invalid authorization callback.")
			return
		}
		_, _ = io.WriteString(w, "Authorization received. You may close this window.")
	})
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	uri, err := url.Parse(c.options.AuthURL)
	if err != nil || uri.Host == "" || uri.User != nil || uri.Fragment != "" || (uri.Scheme != "https" && uri.Scheme != "http") {
		return "", nil, failure("Gmail", "AuthError")
	}
	q := uri.Query()
	q.Set("client_id", c.account.ClientID)
	q.Set("redirect_uri", redirect)
	q.Set("response_type", "code")
	q.Set("scope", gmailScope+" "+calendarScope)
	q.Set("state", state)
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:]))
	q.Set("code_challenge_method", "S256")
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	uri.RawQuery = q.Encode()
	if prompt != nil {
		prompt(uri.String(), "")
	}
	// The prompt already printed the URL, so a host without a browser can still sign in elsewhere.
	// Login does not wait: xdg-open can run a browser in the foreground until it exits.
	go func() { _ = c.options.OpenBrowser(ctx, uri.String()) }()
	if read := c.options.ReadRedirect; read != nil {
		go func() {
			for retry := false; ; retry = true {
				line, err := read(ctx, retry)
				if err != nil {
					return // Closed input leaves the loopback callback.
				}
				u, err := url.Parse(strings.TrimSpace(line))
				if err == nil && u.Scheme == "http" && u.User == nil && accept(u.Host, u.Path, u.RawQuery) {
					return
				}
			}
		}()
	}
	var got result
	select {
	case <-ctx.Done():
		return "", nil, ctx.Err()
	case got = <-callback:
	}
	if got.err != nil {
		return "", nil, got.err
	}
	initial, err := c.tokenRequest(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {got.code}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
	if err != nil {
		return "", nil, err
	}
	token, err := c.refresh(ctx, initial)
	if err != nil {
		return "", nil, err
	}
	token.Username, err = c.profile(ctx, &token, nil)
	if err != nil {
		return "", nil, err
	}
	if err = c.store.SaveOAuth(ctx, "gmail", c.account.Name, token); err != nil {
		return "", nil, safeContext(ctx, "Gmail")
	}
	c.token = &token
	return token.Username, append([]string{}, token.Scopes...), nil
}
