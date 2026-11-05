package gmail

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/loopback"
)

// Login runs on the server host only. The loopback callback or a pasted redirect from Options.ReadRedirect ends
// the browser step (see loopback.Callback.Wait).
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
	verifier, err := loopback.Random()
	if err != nil {
		return "", nil, failure("Gmail", "AuthError")
	}
	state, err := loopback.Random()
	if err != nil {
		return "", nil, failure("Gmail", "AuthError")
	}
	challenge := sha256.Sum256([]byte(verifier))
	callback, err := loopback.Listen("127.0.0.1", state)
	if err != nil {
		return "", nil, failure("Gmail", "AuthError")
	}
	defer callback.Close()
	redirect := "http://" + callback.Host + "/"
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
	code, err := callback.Wait(ctx, uri.String(), c.options.OpenBrowser, c.options.ReadRedirect)
	if errors.Is(err, loopback.ErrDenied) {
		return "", nil, failure("Gmail", "AuthError")
	}
	if err != nil {
		return "", nil, err
	}
	initial, err := c.tokenRequest(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}})
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
	if err = c.store.SaveOAuth(ctx, "gmail", c.account.Email, token); err != nil {
		return "", nil, safeContext(ctx, "Gmail")
	}
	c.token = &token
	return token.Username, append([]string{}, token.Scopes...), nil
}
