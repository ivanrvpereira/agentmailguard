package server

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
)

// CredentialCheck is one startup result. Item and Status never contain secrets or backend text.
type CredentialCheck struct{ Item, Status string }

var credentialCheckFailed = errors.New("credential check failed")

// An account without a login does not block the start. Its commands fail with the provider's auth error.
const notLoggedIn = "not logged in; commands for this account fail until auth login and a server restart"

const approvalNotSet = "not set; the approval console stays off until auth set-approval-password and a server restart"

// memoryStore passes through to the backend until Preload succeeds.
// After that it answers loads from memory only. Runtime saves still reach the backend.
// Saves always reach the backend first; memory changes only after the backend accepts them.
type memoryStore struct {
	backend   safeStore
	mu        sync.Mutex
	loaded    bool
	oauth     map[[2]string]credentials.OAuth
	jevKey    string
	hasJevKey bool
}

func (m *memoryStore) LoadOAuth(ctx context.Context, p, a string) (credentials.OAuth, error) {
	m.mu.Lock()
	if !m.loaded {
		m.mu.Unlock()
		return m.backend.LoadOAuth(ctx, p, a)
	}
	defer m.mu.Unlock()
	v, ok := m.oauth[[2]string{p, a}]
	if !ok {
		return credentials.OAuth{}, credentials.ErrNotFound
	}
	v.Scopes = append([]string(nil), v.Scopes...)
	return v, nil
}
func (m *memoryStore) SaveOAuth(ctx context.Context, p, a string, v credentials.OAuth) error {
	if err := m.backend.SaveOAuth(ctx, p, a, v); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loaded {
		v.Scopes = append([]string(nil), v.Scopes...)
		m.oauth[[2]string{p, a}] = v
	}
	return nil
}
func (m *memoryStore) LoadOpenRouterKey(ctx context.Context) (string, error) {
	m.mu.Lock()
	if !m.loaded {
		m.mu.Unlock()
		return m.backend.LoadOpenRouterKey(ctx)
	}
	defer m.mu.Unlock()
	if !m.hasJevKey {
		return "", credentials.ErrNotFound
	}
	return m.jevKey, nil
}
func (m *memoryStore) SaveOpenRouterKey(ctx context.Context, key string) error {
	if err := m.backend.SaveOpenRouterKey(ctx, key); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loaded {
		m.jevKey, m.hasJevKey = strings.TrimSpace(key), true
	}
	return nil
}

// Preload reads every credential the configured accounts and Jev need, so backend prompts
// (for example macOS Keychain access dialogs) happen at server start while an operator is present.
// The record store rewrites the whole record once to request modify permission now.
// Injected legacy Store implementations retain their per-account write probe.
// jevKey is called only when the Jev key is missing; nil refuses.
// Missing logins do not block startup. After success, loads use memory; backend saves reread under a lock.
// Accounts added to the configuration later require a restart.
func (s *Server) Preload(ctx context.Context, jevKey func(context.Context) (string, error)) ([]CredentialCheck, error) {
	path := filepath.Join(s.home, "config.toml")
	accounts, err := config.LoadAccounts(ctx, path)
	if err != nil {
		return nil, configError
	}
	name, err := config.CredentialsBackend(ctx, path)
	if err != nil {
		return nil, configError
	}
	backend := s.creds.backend
	record, whole, err := backend.preload(ctx)
	status := "ok"
	if err != nil {
		switch {
		case errors.Is(err, credentials.ErrDecrypt):
			status = "wrong password or damaged file"
		case errors.Is(err, credentials.ErrPasswordMissing):
			status = "password missing"
		case errors.Is(err, credentials.ErrUnsupportedVersion):
			status = "unsupported version"
		default:
			status = "access denied"
		}
	}
	checks := []CredentialCheck{{"credentials (" + name + ")", status}}
	if err != nil {
		if ctx.Err() != nil {
			return checks, ctx.Err()
		}
		return checks, credentialCheckFailed
	}
	failed := false
	add := func(item, status string) {
		checks = append(checks, CredentialCheck{item, status})
		failed = failed || (status != "ok" && status != "saved" && status != "ok (OPENROUTER_API_KEY)" && status != notLoggedIn && status != approvalNotSet)
	}
	oauth := map[[2]string]credentials.OAuth{}
	for _, a := range accounts {
		item := fmt.Sprintf("account %q (%s)", a.Name, a.Provider)
		var v credentials.OAuth
		var err error
		if whole {
			var ok bool
			v, ok = record.OAuth[a.Provider+"/"+a.Name]
			if !ok {
				err = credentials.ErrNotFound
			}
		} else {
			v, err = backend.LoadOAuth(ctx, a.Provider, a.Name)
		}
		switch {
		case ctx.Err() != nil:
			return checks, ctx.Err()
		case errors.Is(err, credentials.ErrNotFound):
			add(item, notLoggedIn)
		case errors.Is(err, credentials.ErrUnavailable):
			add(item, "Keychain access failed or denied")
		case err != nil:
			add(item, "credential record unreadable; run auth login")
		case !whole && backend.SaveOAuth(ctx, a.Provider, a.Name, v) != nil:
			if ctx.Err() != nil {
				return checks, ctx.Err()
			}
			add(item, "Keychain write failed or denied")
		default:
			oauth[[2]string{a.Provider, a.Name}] = v
			add(item, "ok")
		}
	}
	key, hasKey := "", false
	if strings.TrimSpace(s.options.LookupEnv("OPENROUTER_API_KEY")) != "" {
		add("Jev key", "ok (OPENROUTER_API_KEY)")
	} else {
		v := record.OpenRouterKey
		var err error
		if whole {
			if v == "" {
				err = credentials.ErrNotFound
			}
		} else {
			v, err = backend.LoadOpenRouterKey(ctx)
		}
		switch {
		case ctx.Err() != nil:
			return checks, ctx.Err()
		case err == nil:
			key, hasKey = v, true
			add("Jev key", "ok")
		case !errors.Is(err, credentials.ErrNotFound):
			add("Jev key", "Keychain access failed or denied")
		case jevKey == nil:
			add("Jev key", "missing; run auth set-jev-key or set OPENROUTER_API_KEY")
		default:
			entered, err := jevKey(ctx)
			entered = strings.TrimSpace(entered)
			if ctx.Err() != nil {
				return checks, ctx.Err()
			}
			if err != nil || entered == "" {
				add("Jev key", "not entered; run auth set-jev-key or set OPENROUTER_API_KEY")
			} else if backend.SaveOpenRouterKey(ctx, entered) != nil {
				add("Jev key", "Keychain write failed or denied")
			} else {
				key, hasKey = entered, true
				add("Jev key", "saved")
			}
		}
	}
	// A missing approval password keeps the approval console off; it never blocks the start.
	// Injected stores without a whole record have no approval password.
	approvalHash := record.ApprovalHash
	if approvalHash != "" {
		add("approval password", "ok")
	} else {
		add("approval password", approvalNotSet)
	}
	if failed {
		return checks, credentialCheckFailed
	}
	s.creds.mu.Lock()
	s.creds.oauth, s.creds.jevKey, s.creds.hasJevKey, s.creds.loaded = oauth, key, hasKey, true
	s.creds.mu.Unlock()
	for _, a := range accounts {
		if _, err := s.instance(a); err != nil {
			return checks, err
		}
	}
	s.mu.Lock()
	s.preloaded = true
	s.approvalHash = approvalHash
	s.mu.Unlock()
	return checks, nil
}
