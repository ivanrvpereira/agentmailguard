package m365

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/loopback"
)

// Login is an interactive server-host operation, never a remote API operation.
// The prompt receives the verification URI and user code for device login, and the sign-in URL without a code for
// browser login, which the loopback callback or a pasted redirect from Options.ReadRedirect ends.
func (c *Client) Login(ctx context.Context, deviceCode bool, prompt func(uri, code string)) (user string, granted []string, retErr error) {
	ctx, cancel := context.WithTimeout(ctx, 300*time.Second)
	defer cancel()
	defer func() {
		if retErr != nil {
			switch ctx.Err() {
			case context.DeadlineExceeded:
				retErr = loginTimeout
			case context.Canceled:
				retErr = loginCanceled
			}
		}
	}()
	select {
	case c.authGate <- struct{}{}:
		defer func() { <-c.authGate }()
	case <-ctx.Done():
		return "", nil, loginCanceled
	}
	if ctx.Err() != nil {
		return "", nil, loginCanceled
	}
	var response tokenResponse
	var err error
	if deviceCode {
		response, err = c.deviceLogin(ctx, prompt)
	} else {
		response, err = c.browserLogin(ctx, prompt)
	}
	if err != nil {
		return "", nil, err
	}
	if response.Error != "" {
		return "", nil, loginTokenExchange
	}
	if _, err := checkedScopes(response.AccessToken); err != nil {
		return "", nil, loginInvalidToken
	}
	if !c.now().Before(response.expiresAt) {
		return "", nil, loginInvalidToken
	}
	profile, err := c.requestToken(ctx, http.MethodGet, c.endpoint("/me", url.Values{"$select": {"userPrincipalName,mail"}}), nil, false, credentials.OAuth{AccessToken: response.AccessToken, ExpiresAt: response.expiresAt}, nil)
	if err != nil {
		return "", nil, loginProfile
	}
	username := text(profile, "userPrincipalName")
	if username == "" {
		username = text(profile, "mail")
	}
	if username == "" {
		return "", nil, loginProfile
	}
	v, err := c.acceptToken(ctx, response, credentials.OAuth{Username: username})
	if err != nil {
		if _, saveFailed := err.(credentialSaveError); saveFailed {
			return "", nil, loginCredentialSave
		}
		return "", nil, loginInvalidToken
	}
	c.token = &v
	return v.Username, append([]string(nil), v.Scopes...), nil
}
func (c *Client) deviceLogin(ctx context.Context, prompt func(string, string)) (tokenResponse, error) {
	if prompt == nil {
		return tokenResponse{}, loginDeviceRequest
	}
	code, err := c.oauth(ctx, "/devicecode", url.Values{"client_id": {c.account.ClientID}, "scope": {requestedScopes}})
	if err != nil || code.Error != "" || code.DeviceCode == "" || code.UserCode == "" || code.VerificationURI == "" || code.ExpiresIn <= 0 || code.ExpiresIn > 3600 {
		return tokenResponse{}, loginDeviceRequest
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(code.ExpiresIn)*time.Second)
	defer cancel()
	interval := code.Interval
	if interval <= 0 {
		interval = 5
	}
	if interval > 300 {
		return tokenResponse{}, loginDeviceRequest
	}
	prompt(code.VerificationURI, code.UserCode)
	wait := c.wait
	if wait == nil {
		wait = func(ctx context.Context, d time.Duration) error {
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
	for {
		if ctx.Err() != nil || wait(ctx, time.Duration(interval)*time.Second) != nil {
			switch ctx.Err() {
			case context.DeadlineExceeded:
				return tokenResponse{}, loginTimeout
			case context.Canceled:
				return tokenResponse{}, loginCanceled
			default:
				return tokenResponse{}, loginDevicePolling
			}
		}
		response, err := c.oauth(ctx, "/token", url.Values{"client_id": {c.account.ClientID}, "grant_type": {"urn:ietf:params:oauth:grant-type:device_code"}, "device_code": {code.DeviceCode}})
		if err != nil {
			return tokenResponse{}, loginTokenExchange
		}
		switch response.Error {
		case "":
			return response, nil
		case "authorization_pending":
		case "slow_down":
			interval += 5
		case "expired_token":
			return tokenResponse{}, loginTimeout
		default:
			return tokenResponse{}, loginDeviceAuthorization
		}
	}
}
func (c *Client) browserLogin(ctx context.Context, prompt func(string, string)) (tokenResponse, error) {
	state, err := loopback.Random()
	if err != nil {
		return tokenResponse{}, loginBrowserStart
	}
	verifier, err := loopback.Random()
	if err != nil {
		return tokenResponse{}, loginBrowserStart
	}
	// Match azure-identity's localhost redirect registration while binding only loopback.
	callback, err := loopback.Listen("localhost", state)
	if err != nil {
		return tokenResponse{}, loginBrowserStart
	}
	defer callback.Close()
	redirect := "http://" + callback.Host
	hash := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {c.account.ClientID}, "response_type": {"code"}, "response_mode": {"query"}, "redirect_uri": {redirect}, "scope": {requestedScopes}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "code_challenge_method": {"S256"}}
	uri := c.authority + "/authorize?" + q.Encode()
	if prompt != nil {
		prompt(uri, "")
	}
	code, err := callback.Wait(ctx, uri, c.openBrowser, c.readRedirect)
	if errors.Is(err, loopback.ErrDenied) {
		return tokenResponse{}, loginAuthorization
	}
	if err != nil {
		return tokenResponse{}, loginCanceled
	}
	token, err := c.oauth(ctx, "/token", url.Values{"client_id": {c.account.ClientID}, "grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "code_verifier": {verifier}, "scope": {requestedScopes}})
	if err != nil {
		return tokenResponse{}, loginTokenExchange
	}
	return token, nil
}
