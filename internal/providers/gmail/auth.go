package gmail

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
)

type scopeError struct{ missing []string }

func (scopeError) Error() string              { return "Gmail error ScopeError" }
func (scopeError) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "Gmail error ScopeError") }
func (e scopeError) MissingScopes() []string  { return append([]string(nil), e.missing...) }

// scopes refuses only grants that cannot work. Extra grants are the user's choice; callers warn through ExtraScopes.
// Either calendar grant reads the calendar; a missing one is reported as the grant that login requests.
func scopes(raw string) ([]string, error) {
	list := strings.Fields(raw)
	var missing []string
	if !slices.Contains(list, gmailScope) {
		missing = append(missing, gmailScope)
	}
	if !slices.Contains(list, calendarScope) && !slices.Contains(list, calendarReadScope) {
		missing = append(missing, calendarScope)
	}
	if len(missing) != 0 {
		return nil, scopeError{missing: missing}
	}
	return list, nil
}

// ExtraScopes lists granted scopes the adapter does not request.
func ExtraScopes(scopes []string) []string {
	var extra []string
	for _, s := range scopes {
		switch s {
		case gmailScope, calendarScope, calendarReadScope, "openid", "email", "profile", "https://www.googleapis.com/auth/userinfo.email", "https://www.googleapis.com/auth/userinfo.profile":
		default:
			extra = append(extra, s)
		}
	}
	return extra
}
func (c *Client) tokenRequest(ctx context.Context, form url.Values) (credentials.OAuth, error) {
	form.Set("client_id", c.account.ClientID)
	form.Set("client_secret", c.account.ClientSecret)
	status, _, data, err := c.exchange(ctx, "POST", c.options.TokenURL, "", "application/x-www-form-urlencoded", []byte(form.Encode()))
	if err != nil {
		return credentials.OAuth{}, safeContext(ctx, "Gmail")
	}
	if status != 200 {
		return credentials.OAuth{}, failure("Gmail", "AuthError")
	}
	var token struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		Scope        string `json:"scope"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	if json.Unmarshal(data, &token) != nil || token.AccessToken == "" || strings.ContainsAny(token.AccessToken, "\r\n") || token.ExpiresIn <= 0 || token.ExpiresIn > 31536000 {
		return credentials.OAuth{}, failure("Gmail", "AuthError")
	}
	allowed, err := scopes(token.Scope)
	if err != nil {
		return credentials.OAuth{}, err
	}
	return credentials.OAuth{AccessToken: token.AccessToken, RefreshToken: token.RefreshToken, Scopes: allowed, ExpiresAt: c.options.Now().Add(time.Duration(token.ExpiresIn) * time.Second)}, nil
}
func (c *Client) refresh(ctx context.Context, old credentials.OAuth) (credentials.OAuth, error) {
	if old.RefreshToken == "" {
		return credentials.OAuth{}, failure("Gmail", "AuthError")
	}
	token, err := c.tokenRequest(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {old.RefreshToken}})
	if err != nil {
		return credentials.OAuth{}, err
	}
	if token.RefreshToken == "" {
		token.RefreshToken = old.RefreshToken
	}
	token.Username = old.Username
	return token, nil
}
func (c *Client) credential(ctx context.Context) (credentials.OAuth, error) {
	select {
	case c.tokenGate <- struct{}{}:
		defer func() { <-c.tokenGate }()
	case <-ctx.Done():
		return credentials.OAuth{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return credentials.OAuth{}, err
	}
	if c.store == nil {
		return credentials.OAuth{}, failure("Gmail", "CredentialError")
	}
	// An expired token is refreshed from the store, never from the cache: a save can keep a newer login that
	// amgd account login stored instead of this client's refresh, and that login's refresh token must be used.
	var token credentials.OAuth
	if c.token != nil && c.token.AccessToken != "" && c.options.Now().Before(c.token.ExpiresAt) {
		token = *c.token
	} else {
		var err error
		token, err = c.store.LoadOAuth(ctx, "gmail", c.account.Email)
		if err != nil {
			return credentials.OAuth{}, safeContext(ctx, "Gmail")
		}
	}
	if token.AccessToken == "" || !c.options.Now().Before(token.ExpiresAt) {
		var err error
		token, err = c.refresh(ctx, token)
		if err != nil {
			return credentials.OAuth{}, err
		}
		if err = c.store.SaveOAuth(ctx, "gmail", c.account.Email, token); err != nil {
			return credentials.OAuth{}, safeContext(ctx, "Gmail")
		}
	} else if _, err := scopes(strings.Join(token.Scopes, " ")); err != nil {
		return credentials.OAuth{}, err
	}
	if strings.ContainsAny(token.AccessToken, "\r\n") {
		return credentials.OAuth{}, failure("Gmail", "AuthError")
	}
	c.token = &token
	return token, nil
}
func (c *Client) profile(ctx context.Context, token *credentials.OAuth, refresh func(context.Context) (credentials.OAuth, error)) (string, error) {
	v, err := c.requestWithToken(ctx, "Gmail", "GET", c.options.ProfileURL, nil, nil, "", token, refresh)
	if err != nil {
		return "", err
	}
	name := stringField(v, "emailAddress")
	if name == "" {
		return "", failure("Gmail", "AuthError")
	}
	return name, nil
}
func (c *Client) Status(ctx context.Context) (string, []string, error) {
	token, err := c.credential(ctx)
	if err != nil {
		return "", nil, err
	}
	name, err := c.profile(ctx, &token, c.credential)
	if err != nil {
		return "", nil, err
	}
	return name, append([]string{}, token.Scopes...), nil
}
