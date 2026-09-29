// Package credentials stores server-host secrets in macOS Keychain only.
package credentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

var ErrNotFound = errors.New("credential not found")
var ErrUnavailable = errors.New("macOS Keychain unavailable")

type OAuth struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	Scopes       []string  `json:"scopes"`
	Username     string    `json:"username"`
}

func (OAuth) String() string     { return "OAuth{redacted}" }
func (o OAuth) GoString() string { return o.String() }

// Format also redacts unsupported verbs, which otherwise expose struct fields.
func (o OAuth) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, o.String()) }

// Store is the credential dependency for provider and Jev adapters. It does not
// obtain tokens, expand scopes, or expose provider secrets to remote clients.
type Store interface {
	LoadOAuth(ctx context.Context, provider, account string) (OAuth, error)
	SaveOAuth(ctx context.Context, provider, account string, credential OAuth) error
	LoadOpenRouterKey(ctx context.Context) (string, error)
	SaveOpenRouterKey(ctx context.Context, key string) error
}

// Platform is the native secret-store seam. A missing item returns nil, nil.
// Implementations must not use command arguments, environment variables, or files for secrets.
type Platform interface {
	Read(ctx context.Context, service, account string) ([]byte, error)
	Write(ctx context.Context, service, account string, value []byte) error
}
type Keychain struct{ platform Platform }

// New injects the platform seam; production uses NewMacOS. nil fails closed.
func New(platform Platform) *Keychain { return &Keychain{platform: platform} }

func service(provider string) (string, error) {
	switch provider {
	case "m365":
		return "agentmailguard-go-m365", nil
	case "gmail":
		return "agentmailguard-go-gmail", nil
	default:
		return "", errors.New("unsupported credential provider")
	}
}
func (k *Keychain) read(ctx context.Context, service, account string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if k.platform == nil {
		return nil, ErrUnavailable
	}
	value, err := k.platform.Read(ctx, service, account)
	if err != nil {
		return nil, safeError(ctx, err)
	}
	if len(value) == 0 {
		return nil, ErrNotFound
	}
	return value, nil
}
func (k *Keychain) write(ctx context.Context, service, account string, value []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if k.platform == nil {
		return ErrUnavailable
	}
	if err := k.platform.Write(ctx, service, account, value); err != nil {
		return safeError(ctx, err)
	}
	return nil
}
func safeError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ErrNotFound) {
		return ErrNotFound
	}
	// Do not wrap the backend error: even its message or unwrap chain may contain secrets.
	return ErrUnavailable
}

func (k *Keychain) LoadOAuth(ctx context.Context, provider, account string) (OAuth, error) {
	if strings.TrimSpace(account) == "" {
		return OAuth{}, errors.New("credential account is empty")
	}
	namespace, err := service(provider)
	if err != nil {
		return OAuth{}, err
	}
	value, err := k.read(ctx, namespace, account)
	if err != nil {
		return OAuth{}, err
	}
	var token OAuth
	if err := json.Unmarshal(value, &token); err != nil || (token.AccessToken == "" && token.RefreshToken == "") {
		return OAuth{}, errors.New("invalid OAuth credential")
	}
	return token, nil
}
func (k *Keychain) SaveOAuth(ctx context.Context, provider, account string, credential OAuth) error {
	if strings.TrimSpace(account) == "" {
		return errors.New("credential account is empty")
	}
	if credential.AccessToken == "" && credential.RefreshToken == "" {
		return errors.New("OAuth credential is empty")
	}
	namespace, err := service(provider)
	if err != nil {
		return err
	}
	value, err := json.Marshal(credential)
	if err != nil {
		return errors.New("cannot encode OAuth credential")
	}
	return k.write(ctx, namespace, account, value)
}
func (k *Keychain) LoadOpenRouterKey(ctx context.Context) (string, error) {
	value, err := k.read(ctx, "agentmailguard-go-openrouter", "agentmailguard-go")
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(value))
	if key == "" {
		return "", ErrNotFound
	}
	return key, nil
}
func (k *Keychain) SaveOpenRouterKey(ctx context.Context, key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("OpenRouter key is empty")
	}
	return k.write(ctx, "agentmailguard-go-openrouter", "agentmailguard-go", []byte(key))
}
