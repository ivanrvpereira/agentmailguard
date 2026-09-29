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
		path = os.Getenv("AGENTMAILGUARD_GO_HOME")
	}
	if path == "" {
		path = "~/.agentmailguard-go"
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
}

func (a Account) String() string {
	return fmt.Sprintf("Account{name:%q provider:%q client_id:%q tenant_id:%q}", a.Name, a.Provider, a.ClientID, a.TenantID)
}
func (a Account) GoString() string { return a.String() }

// Format also redacts unsupported verbs, which otherwise expose struct fields.
func (a Account) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, a.String()) }

func accounts(ctx context.Context, path string) (map[string]any, []string, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot read config file: %s", path)
	}
	var document map[string]any
	metadata, err := toml.Decode(string(data), &document)
	// Parser errors may contain file values. Never return them.
	if err != nil {
		return nil, nil, fmt.Errorf("invalid TOML in %s", path)
	}
	tables, ok := document["accounts"].(map[string]any)
	if !ok || len(tables) == 0 {
		return nil, nil, fmt.Errorf("no [accounts.<name>] tables in %s", path)
	}
	names := []string{}
	seen := map[string]bool{}
	for _, key := range metadata.Keys() {
		if len(key) >= 2 && key[0] == "accounts" && !seen[key[1]] {
			names = append(names, key[1])
			seen[key[1]] = true
		}
	}
	return tables, names, ctx.Err()
}

func AccountNames(ctx context.Context, path string) ([]string, error) {
	_, names, err := accounts(ctx, path)
	return names, err
}

// LoadAccount selects only the requested account; nil selects the first in file order.
func LoadAccount(ctx context.Context, path string, name *string) (Account, error) {
	tables, names, err := accounts(ctx, path)
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
	result := Account{Name: selected, Provider: provider, ClientID: clientID}
	if provider == "m365" {
		result.TenantID, err = get("tenant_id")
	} else {
		result.ClientSecret, err = get("client_secret")
	}
	return result, err
}
