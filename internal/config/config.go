// Package config reads account configuration without accessing credentials or providers.
package config

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Home uses Go-specific state only. An explicit override wins over the Go environment variable.
func Home(override string) (string, error) {
	path := override
	if path == "" {
		path = os.Getenv("AGENTMAILGUARD_HOME")
	}
	if path == "" {
		path = "~/.agentmailguard"
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("cannot determine Go state home")
		}
		path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
	}
	return filepath.Clean(path), nil
}

type Account struct {
	Name, Provider, ClientID, TenantID string
	ClientSecret                       string `json:"-"`
	Policy                             Policy
}

func (a Account) String() string {
	return fmt.Sprintf("Account{name:%q provider:%q client_id:%q tenant_id:%q}", a.Name, a.Provider, a.ClientID, a.TenantID)
}
func (a Account) GoString() string { return a.String() }

// Format also redacts unsupported verbs, which otherwise expose struct fields.
func (a Account) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, a.String()) }

func document(ctx context.Context, path string) (map[string]any, toml.MetaData, error) {
	if err := ctx.Err(); err != nil {
		return nil, toml.MetaData{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, toml.MetaData{}, fmt.Errorf("cannot read config file: %s", path)
	}
	var document map[string]any
	metadata, err := toml.Decode(string(data), &document)
	// Parser errors may contain file values. Never return them.
	if err != nil {
		return nil, toml.MetaData{}, fmt.Errorf("invalid TOML in %s", path)
	}
	if _, err := credentialBackend(document); err != nil {
		return nil, toml.MetaData{}, err
	}
	return document, metadata, nil
}

func credentialBackend(document map[string]any) (string, error) {
	value, exists := document["credentials"]
	if !exists {
		return "keychain", nil
	}
	table, ok := value.(map[string]any)
	if !ok || len(table) != 1 {
		return "", fmt.Errorf("invalid credentials configuration")
	}
	backend, ok := table["backend"].(string)
	if !ok || (backend != "keychain" && backend != "file") {
		return "", fmt.Errorf("invalid credentials configuration")
	}
	return backend, nil
}

// CredentialsBackend validates the optional backend setting; absent means Keychain.
func CredentialsBackend(ctx context.Context, path string) (string, error) {
	d, _, err := document(ctx, path)
	if err != nil {
		return "", err
	}
	return credentialBackend(d)
}

// accounts also returns the whole document for the global [policy] table.
func accounts(ctx context.Context, path string) (map[string]any, []string, map[string]any, error) {
	document, metadata, err := document(ctx, path)
	if err != nil {
		return nil, nil, nil, err
	}
	tables, ok := document["accounts"].(map[string]any)
	if !ok || len(tables) == 0 {
		return nil, nil, nil, fmt.Errorf("no [accounts.<name>] tables in %s", path)
	}
	names := []string{}
	seen := map[string]bool{}
	for _, key := range metadata.Keys() {
		if len(key) >= 2 && key[0] == "accounts" && !seen[key[1]] {
			names = append(names, key[1])
			seen[key[1]] = true
		}
	}
	return tables, names, document, ctx.Err()
}

func AccountNames(ctx context.Context, path string) ([]string, error) {
	_, names, _, err := accounts(ctx, path)
	return names, err
}

// LoadAccounts validates every configured account before credentials can be migrated.
func LoadAccounts(ctx context.Context, path string) ([]Account, error) {
	tables, names, document, err := accounts(ctx, path)
	if err != nil {
		return nil, err
	}
	policies, err := resolvePolicies(document, tables, names)
	if err != nil {
		return nil, err
	}
	result := make([]Account, len(names))
	for i, name := range names {
		result[i], err = loadAccount(path, name, tables[name], policies[name])
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

// LoadAccount selects only the requested account; nil selects the first in file order.
func LoadAccount(ctx context.Context, path string, name *string) (Account, error) {
	tables, names, document, err := accounts(ctx, path)
	if err != nil {
		return Account{}, err
	}
	policies, err := resolvePolicies(document, tables, names)
	if err != nil {
		return Account{}, err
	}
	selected := names[0]
	if name != nil {
		selected = *name
	}
	value, exists := tables[selected]
	if !exists {
		return Account{}, fmt.Errorf("unknown account %q; known: %s", selected, strings.Join(names, ", "))
	}
	return loadAccount(path, selected, value, policies[selected])
}

func loadAccount(path, selected string, value any, policy Policy) (Account, error) {
	table, _ := value.(map[string]any)
	get := func(key string) (string, error) {
		text, ok := table[key].(string)
		if !ok || strings.TrimSpace(text) == "" {
			return "", fmt.Errorf("account %q: missing or empty %q in %s", selected, key, path)
		}
		return strings.TrimSpace(text), nil
	}
	provider, err := get("provider")
	if err != nil {
		return Account{}, err
	}
	if provider != "m365" && provider != "gmail" {
		return Account{}, fmt.Errorf("account %q: unsupported provider; supported: m365, gmail", selected)
	}
	clientID, err := get("client_id")
	if err != nil {
		return Account{}, err
	}
	result := Account{Name: selected, Provider: provider, ClientID: clientID, Policy: policy}
	if provider == "m365" {
		result.TenantID, err = get("tenant_id")
	} else {
		result.ClientSecret, err = get("client_secret")
	}
	return result, err
}
