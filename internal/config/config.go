// Package config reads and writes config.toml without accessing credentials or providers.
package config

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/BurntSushi/toml"
)

// Version is the only config.toml format that this program reads. A later program can refuse an older file.
const Version = 1

// Home is --home, then AMG_HOME, then ~/.agentmailguard. One home per machine: homes share one Keychain item.
func Home(override string) (string, error) {
	path := override
	if path == "" {
		path = os.Getenv("AMG_HOME")
	}
	if path == "" {
		path = "~/.agentmailguard"
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errors.New("cannot determine the home directory")
		}
		path = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
	}
	return filepath.Clean(path), nil
}

// Account is one [accounts.ALIAS] table. Email, in lowercase, is the account key everywhere outside
// config.toml; the alias is only a short name for the command line.
type Account struct {
	Alias, Email, Provider, ClientID, TenantID string
	ClientSecret                               string `json:"-"`
	Policy                                     Policy
}

// Key is the account key: the lowercase email.
func (a Account) Key() string { return a.Email }

func (a Account) String() string {
	return fmt.Sprintf("Account{alias:%q email:%q provider:%q client_id:%q tenant_id:%q}", a.Alias, a.Email, a.Provider, a.ClientID, a.TenantID)
}
func (a Account) GoString() string { return a.String() }

// Format also redacts unsupported verbs, which otherwise expose struct fields.
func (a Account) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, a.String()) }

// DefaultConsoleListen and DefaultConsoleUsername are the approval console address and sign-in username when
// [console] sets none.
const (
	DefaultConsoleListen   = "localhost:8788"
	DefaultConsoleUsername = "admin"
)

// Console is the [console] table: the approval console listen address, extra Host names it accepts, and the
// username that a console sign-in needs with the console password.
type Console struct {
	Listen   string
	Hosts    []string
	Username string
}

// Screening is the [screening] table. Provider is jev or fake. Endpoint is the HTTPS URL of the jev engine and
// Model is the model name that it asks; empty means the built-in default. The fake engine ignores both.
type Screening struct{ Provider, Endpoint, Model string }

// Config is the whole of config.toml, validated. Accounts are in alias order.
type Config struct {
	DefaultAccount string
	Accounts       []Account
	Screening      Screening
	Console        Console
	// Backend is the credential store: file (the default) or keychain.
	Backend string
}

// Account returns the account with the alias.
func (c Config) Account(alias string) (Account, bool) {
	i := slices.IndexFunc(c.Accounts, func(a Account) bool { return a.Alias == alias })
	if i < 0 {
		return Account{}, false
	}
	return c.Accounts[i], true
}

// ByEmail returns the account with the email, compared in lowercase.
func (c Config) ByEmail(email string) (Account, bool) {
	i := slices.IndexFunc(c.Accounts, func(a Account) bool { return a.Email == strings.ToLower(email) })
	if i < 0 {
		return Account{}, false
	}
	return c.Accounts[i], true
}

// Emails lists the account keys.
func (c Config) Emails() []string {
	out := make([]string, len(c.Accounts))
	for i, a := range c.Accounts {
		out[i] = a.Email
	}
	return out
}

// Error names the invalid key path of config.toml and a fixed problem. It never contains a configured value.
type Error struct{ Path, Problem string }

func (e Error) Error() string {
	if e.Path == "" {
		return "config.toml: " + e.Problem
	}
	return "config.toml: " + e.Path + ": " + e.Problem
}

func fail(problem string, keys ...string) error { return Error{keyPath(keys...), problem} }

// Load reads and validates config.toml. A missing file is an empty configuration: no accounts and defaults.
func Load(ctx context.Context, path string) (Config, error) {
	if err := ctx.Err(); err != nil {
		return Config{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Parse(nil)
	}
	if err != nil {
		return Config{}, Error{"", "cannot read the file"}
	}
	if data == nil {
		data = []byte{}
	}
	return Parse(data)
}

// Parse validates the text of config.toml. Nil means that no file exists.
func Parse(data []byte) (Config, error) {
	document := map[string]any{}
	if data != nil {
		var err error
		if document, err = decode(data); err != nil {
			return Config{}, err
		}
	}
	return validate(document, data != nil)
}

func decode(data []byte) (map[string]any, error) {
	var document map[string]any
	// Parser errors may contain file values. Never return them.
	if _, err := toml.Decode(string(data), &document); err != nil {
		return nil, Error{"", "invalid TOML"}
	}
	if document == nil {
		document = map[string]any{}
	}
	return document, nil
}

var aliasPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ValidAlias reports whether alias can name an account: letters, digits, _ and -, at most 64.
func ValidAlias(alias string) bool { return aliasPattern.MatchString(alias) }

// ValidEmail accepts one address without spaces or control characters: local@domain, at most 254 bytes.
func ValidEmail(email string) bool {
	local, domain, ok := strings.Cut(email, "@")
	return ok && local != "" && domain != "" && !strings.Contains(domain, "@") && len(email) <= 254 &&
		strings.IndexFunc(email, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r > unicode.MaxASCII }) < 0
}

func validate(document map[string]any, exists bool) (Config, error) {
	c := Config{Screening: Screening{Provider: "jev"}, Console: Console{Listen: DefaultConsoleListen, Username: DefaultConsoleUsername}, Backend: "file", Accounts: []Account{}}
	for _, key := range slices.Sorted(maps.Keys(document)) {
		switch key {
		case "version", "default_account", "accounts", "policy", "screening", "console", "credentials":
		default:
			return Config{}, fail("unknown key", key)
		}
	}
	if exists {
		if v, ok := document["version"]; !ok {
			return Config{}, fail("missing; add version = 1", "version")
		} else if n, ok := v.(int64); !ok || n != Version {
			return Config{}, fail("unsupported; this program reads version = 1", "version")
		}
	}
	var err error
	if c.Backend, err = credentialBackend(document); err != nil {
		return Config{}, err
	}
	if c.Screening, err = screening(document); err != nil {
		return Config{}, err
	}
	if c.Console, err = console(document); err != nil {
		return Config{}, err
	}
	global, err := globalPolicy(document)
	if err != nil {
		return Config{}, err
	}
	tables := map[string]any{}
	if value, present := document["accounts"]; present {
		var ok bool
		if tables, ok = value.(map[string]any); !ok {
			return Config{}, fail("must be a table of accounts", "accounts")
		}
	}
	emails := map[string]string{}
	for _, alias := range slices.Sorted(maps.Keys(tables)) {
		a, err := account(alias, tables[alias], global)
		if err != nil {
			return Config{}, err
		}
		if other, taken := emails[a.Email]; taken {
			return Config{}, fail("same email as account "+other, "accounts", alias, "email")
		}
		emails[a.Email] = alias
		c.Accounts = append(c.Accounts, a)
	}
	if value, present := document["default_account"]; present {
		alias, ok := value.(string)
		if !ok {
			return Config{}, fail("must be the alias of an account", "default_account")
		}
		if _, known := c.Account(alias); !known {
			return Config{}, fail("names no account", "default_account")
		}
		c.DefaultAccount = alias
	} else if len(c.Accounts) > 0 {
		return Config{}, fail("missing; set it to the alias of an account", "default_account")
	}
	return c, nil
}

func account(alias string, value any, global Policy) (Account, error) {
	if !ValidAlias(alias) {
		return Account{}, fail("an alias uses only letters, digits, _ and -", "accounts", alias)
	}
	table, ok := value.(map[string]any)
	if !ok {
		return Account{}, fail("must be a table", "accounts", alias)
	}
	text := func(key string) (string, error) {
		v, ok := table[key].(string)
		if !ok || strings.TrimSpace(v) == "" {
			return "", fail("missing or empty", "accounts", alias, key)
		}
		return strings.TrimSpace(v), nil
	}
	a := Account{Alias: alias}
	var err error
	if a.Provider, err = text("provider"); err != nil {
		return Account{}, err
	}
	allowed := map[string][]string{"fake": {}, "m365": {"client_id", "tenant_id"}, "gmail": {"client_id", "client_secret"}}
	fields, known := allowed[a.Provider]
	if !known {
		return Account{}, fail("unsupported; supported: m365, gmail, fake", "accounts", alias, "provider")
	}
	for _, key := range slices.Sorted(maps.Keys(table)) {
		if key != "provider" && key != "email" && key != "policy" && !slices.Contains(fields, key) {
			return Account{}, fail("unknown key for provider "+a.Provider, "accounts", alias, key)
		}
	}
	if a.Email, err = text("email"); err != nil {
		return Account{}, err
	}
	if a.Email = strings.ToLower(a.Email); !ValidEmail(a.Email) {
		return Account{}, fail("not an email address", "accounts", alias, "email")
	}
	for _, key := range fields {
		v, err := text(key)
		if err != nil {
			return Account{}, err
		}
		switch key {
		case "client_id":
			a.ClientID = v
		case "tenant_id":
			a.TenantID = v
		case "client_secret":
			a.ClientSecret = v
		}
	}
	a.Policy = global
	if err := a.Policy.apply(table, "accounts", alias, "policy"); err != nil {
		return Account{}, err
	}
	return a, nil
}

func credentialBackend(document map[string]any) (string, error) {
	value, exists := document["credentials"]
	if !exists {
		return "file", nil
	}
	table, ok := value.(map[string]any)
	if !ok {
		return "", fail("must be a table", "credentials")
	}
	for _, key := range slices.Sorted(maps.Keys(table)) {
		if key != "backend" {
			return "", fail("unknown key", "credentials", key)
		}
	}
	backend, ok := table["backend"].(string)
	if !ok || (backend != "keychain" && backend != "file") {
		return "", fail("must be keychain or file", "credentials", "backend")
	}
	return backend, nil
}
