// Package m365 implements guarded Microsoft Graph adapters. Construction has no side effects.
package m365

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// Options injects external-system seams, not account settings. BaseURL includes /v1.0;
// AuthorityURL includes the tenant and /oauth2/v2.0. Defaults use Microsoft endpoints.
type Options struct {
	HTTPClient            *http.Client
	BaseURL, AuthorityURL string
	Now                   func() time.Time
	Wait                  func(context.Context, time.Duration) error
	OpenBrowser           func(context.Context, string) error
}
type Client struct {
	account         config.Account
	store           credentials.Store
	http            *http.Client
	base, authority string
	now             func() time.Time
	wait            func(context.Context, time.Duration) error
	openBrowser     func(context.Context, string) error
	authGate        chan struct{}
	token           *credentials.OAuth
	limit           chan struct{}
	mutationGate    chan struct{}
}

// Format redacts both Client values and pointers, including unsupported verbs.
func (Client) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "m365.Client(redacted)") }

func New(account config.Account, store credentials.Store, opts Options) *Client {
	h := http.Client{}
	if opts.HTTPClient != nil {
		h = *opts.HTTPClient
	}
	h.Timeout = 30 * time.Second
	// Refuse every redirect, including same-origin redirects of credential-bearing POSTs.
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	base := opts.BaseURL
	if base == "" {
		base = "https://graph.microsoft.com/v1.0"
	}
	authority := opts.AuthorityURL
	if authority == "" {
		authority = "https://login.microsoftonline.com/" + escapeID(account.TenantID) + "/oauth2/v2.0"
	}
	clock := opts.Now
	if clock == nil {
		clock = time.Now
	}
	return &Client{account: account, store: store, http: &h, base: strings.TrimRight(base, "/"), authority: strings.TrimRight(authority, "/"), now: clock, wait: opts.Wait, openBrowser: opts.OpenBrowser, authGate: make(chan struct{}, 1), mutationGate: make(chan struct{}, 1), limit: make(chan struct{}, 4)}
}

const authError = model.MailboxError("AuthError")

// Keep storage failures distinct without retaining a backend error or changing its safe code.
type credentialSaveError struct{}

func (credentialSaveError) Error() string { return string(authError) }

// checkedScopes refuses only unusable tokens. Extra grants are the user's choice;
// application roles are returned with a role: prefix so callers can warn about them.
func checkedScopes(token string) ([]string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, authError
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, authError
	}
	var claims map[string]json.RawMessage
	if json.Unmarshal(b, &claims) != nil || claims == nil {
		return nil, authError
	}
	var roles []string
	if raw, ok := claims["roles"]; ok && json.Unmarshal(raw, &roles) != nil {
		return nil, authError
	}
	var scope string
	if json.Unmarshal(claims["scp"], &scope) != nil {
		return nil, authError
	}
	scopes := strings.Fields(scope)
	if len(scopes) == 0 {
		return nil, authError
	}
	for _, r := range roles {
		scopes = append(scopes, "role:"+r)
	}
	return scopes, nil
}

// ExtraScopes lists granted permissions the adapter does not request, including role: entries.
func ExtraScopes(scopes []string) []string {
	var extra []string
	for _, s := range scopes {
		switch strings.ToLower(s) {
		case "user.read", "mail.readwrite", "calendars.read", "mailboxsettings.read", "offline_access", "openid", "profile", "email":
		default:
			extra = append(extra, s)
		}
	}
	return extra
}
func (c *Client) credential(ctx context.Context) (credentials.OAuth, error) {
	select {
	case c.authGate <- struct{}{}:
		defer func() { <-c.authGate }()
	case <-ctx.Done():
		return credentials.OAuth{}, authError
	}
	if ctx.Err() != nil {
		return credentials.OAuth{}, authError
	}
	if c.token != nil && c.now().Before(c.token.ExpiresAt) {
		return *c.token, nil
	}
	if c.store == nil {
		return credentials.OAuth{}, authError
	}
	v, err := c.store.LoadOAuth(ctx, "m365", c.account.Name)
	if err != nil {
		return credentials.OAuth{}, authError
	}
	if v.AccessToken == "" || !c.now().Before(v.ExpiresAt) {
		if v.RefreshToken == "" {
			return credentials.OAuth{}, authError
		}
		result, err := c.oauth(ctx, "/token", url.Values{"grant_type": {"refresh_token"}, "client_id": {c.account.ClientID}, "refresh_token": {v.RefreshToken}, "scope": {requestedScopes}})
		if err != nil {
			return credentials.OAuth{}, err
		}
		v, err = c.acceptToken(ctx, result, v)
		if _, saveFailed := err.(credentialSaveError); saveFailed {
			// Only Login reports save failures separately; mailbox callers derive codes from the type.
			return credentials.OAuth{}, authError
		}
		if err != nil {
			return credentials.OAuth{}, err
		}
	} else {
		scopes, err := checkedScopes(v.AccessToken)
		if err != nil {
			return credentials.OAuth{}, err
		}
		v.Scopes = scopes
	}
	c.token = &v
	return v, nil
}

// Status never returns provider tokens and never starts interactive authentication.
func (c *Client) Status(ctx context.Context) (string, []string, error) {
	v, err := c.credential(ctx)
	if err != nil {
		return "", nil, err
	}
	return v.Username, append([]string(nil), v.Scopes...), nil
}

const requestedScopes = "https://graph.microsoft.com/User.Read https://graph.microsoft.com/Mail.ReadWrite https://graph.microsoft.com/Calendars.Read https://graph.microsoft.com/MailboxSettings.Read offline_access openid profile"

type tokenResponse struct {
	AccessToken     string `json:"access_token"`
	RefreshToken    string `json:"refresh_token"`
	IDToken         string `json:"id_token"`
	ExpiresIn       int    `json:"expires_in"`
	Error           string `json:"error"`
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	Interval        int    `json:"interval"`
	expiresAt       time.Time
}

func (c *Client) oauth(ctx context.Context, path string, form url.Values) (tokenResponse, error) {
	var out tokenResponse
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.authority+path, strings.NewReader(form.Encode()))
	if err != nil {
		return out, authError
	}
	req.GetBody = nil
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return out, authError
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 && resp.StatusCode != 400 {
		return out, authError
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out) != nil {
		return tokenResponse{}, authError
	}
	if resp.StatusCode == 400 && out.Error == "" {
		return tokenResponse{}, authError
	}
	if out.AccessToken != "" && out.ExpiresIn > 0 && out.ExpiresIn <= 31536000 {
		// Preserve acquisition time across Login's profile queue and network work.
		out.expiresAt = c.now().Add(time.Duration(out.ExpiresIn) * time.Second)
	}
	return out, nil
}
func (c *Client) acceptToken(ctx context.Context, r tokenResponse, old credentials.OAuth) (credentials.OAuth, error) {
	if r.Error != "" || !c.now().Before(r.expiresAt) {
		return credentials.OAuth{}, authError
	}
	scopes, err := checkedScopes(r.AccessToken)
	if err != nil {
		return credentials.OAuth{}, err
	}
	old.AccessToken = r.AccessToken
	old.Scopes = scopes
	old.ExpiresAt = r.expiresAt
	if r.RefreshToken != "" {
		old.RefreshToken = r.RefreshToken
	}
	if c.store == nil || c.store.SaveOAuth(ctx, "m365", c.account.Name, old) != nil {
		return credentials.OAuth{}, credentialSaveError{}
	}
	return old, nil
}
func escapeID(id string) string { // Implemented without path cleaning: an ID is exactly one segment.
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(id); i++ {
		ch := id[i]
		if ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_' || ch == '~' {
			b.WriteByte(ch)
		} else {
			b.WriteByte('%')
			b.WriteByte(hex[ch>>4])
			b.WriteByte(hex[ch&15])
		}
	}
	return b.String()
}
