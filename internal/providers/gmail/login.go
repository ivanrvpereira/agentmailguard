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
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, MaxHeaderBytes: 8192}
	server.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || r.Method != "GET" || r.URL.Path != "/" || r.Host != listener.Addr().String() || len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			w.WriteHeader(400)
			_, _ = io.WriteString(w, "Invalid authorization callback.")
			return
		}
		got := result{code: q.Get("code")}
		if len(q["code"]) != 1 || got.code == "" || q.Get("error") != "" {
			got.err = failure("Gmail", "AuthError")
		}
		select {
		case callback <- got:
		default:
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
	if err = c.options.OpenBrowser(ctx, uri.String()); err != nil {
		return "", nil, safeContext(ctx, "Gmail")
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
