package m365

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
)

// Login is an interactive server-host operation, never a remote API operation.
// The prompt receives only the verification URI and user code for device login.
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
		response, err = c.browserLogin(ctx)
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
func randomCode() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", authError
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}
func (c *Client) browserLogin(ctx context.Context) (tokenResponse, error) {
	state, err := randomCode()
	if err != nil {
		return tokenResponse{}, loginBrowserStart
	}
	verifier, err := randomCode()
	if err != nil {
		return tokenResponse{}, loginBrowserStart
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return tokenResponse{}, loginBrowserStart
	}
	defer listener.Close()
	_, port, _ := net.SplitHostPort(listener.Addr().String())
	// Match azure-identity's localhost redirect registration while binding only loopback.
	host := net.JoinHostPort("localhost", port)
	redirect := "http://" + host
	type callback struct {
		code string
		err  bool
	}
	result := make(chan callback, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		q := r.URL.Query()
		if r.Method != http.MethodGet || r.URL.Path != "/" || r.Host != host || len(q["state"]) != 1 || subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(state)) != 1 {
			w.WriteHeader(400)
			_, _ = w.Write([]byte("Invalid callback"))
			return
		}
		response := callback{code: q.Get("code"), err: q.Get("error") != "" || len(q["code"]) != 1 || q.Get("code") == ""}
		select {
		case result <- response:
		default:
		}
		_, _ = w.Write([]byte("Sign-in response received. Return to the terminal for the login result."))
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 16 << 10}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	hash := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {c.account.ClientID}, "response_type": {"code"}, "response_mode": {"query"}, "redirect_uri": {redirect}, "scope": {requestedScopes}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "code_challenge_method": {"S256"}}
	open := c.openBrowser
	if open == nil {
		open = func(ctx context.Context, uri string) error { return exec.CommandContext(ctx, "open", uri).Run() }
	}
	if open(ctx, c.authority+"/authorize?"+q.Encode()) != nil {
		return tokenResponse{}, loginBrowserOpen
	}
	select {
	case <-ctx.Done():
		return tokenResponse{}, loginCanceled
	case response := <-result:
		if response.err {
			return tokenResponse{}, loginAuthorization
		}
		token, err := c.oauth(ctx, "/token", url.Values{"client_id": {c.account.ClientID}, "grant_type": {"authorization_code"}, "code": {response.code}, "redirect_uri": {redirect}, "code_verifier": {verifier}, "scope": {requestedScopes}})
		if err != nil {
			return tokenResponse{}, loginTokenExchange
		}
		return token, nil
	}
}
