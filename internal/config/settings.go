package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

// The secret keys live in the credential store, never in config.toml. Their values come from stdin only.
const (
	ScreeningAPIKey = "screening.api_key"
	ConsolePassword = "console.password"
)

// RestartKey reports whether a change of key takes effect only when the daemon starts again.
func RestartKey(key string) bool { return key == "console.listen" || key == "credentials.backend" }

// Key is one setting of `amgd config`, addressed by a dotted path such as policy.mail_send.
type Key struct {
	Name string
	path []string
	// Choice is true when the value is one of a fixed set (a policy decision, a provider, a backend). Only such
	// values go into audit records; free text such as an endpoint or an address does not.
	Choice bool
	// Alias names the account of an accounts.ALIAS key, else it is empty.
	Alias string
	list  bool
}

var (
	baseKeys   = []string{"mail_trash", "mail_draft", "mail_send", "calendar_invite", "calendar_update", "calendar_cancel", "calendar_respond"}
	sealedKeys = []string{"mail_trash"}
)

// policyKey resolves the rest of a policy path: KEY or sealed.KEY.
func policyKey(rest []string) bool {
	return len(rest) == 1 && slices.Contains(baseKeys, rest[0]) || len(rest) == 2 && rest[0] == "sealed" && slices.Contains(sealedKeys, rest[1])
}

// ErrAccountCommandsOnly refuses a change of the email or the provider of an account: a key that only amgd account
// remove and amgd account add change. Such a key can still be read.
var ErrAccountCommandsOnly = errors.New("changes only with amgd account remove and amgd account add")

// ParseKey resolves a dotted key against c. It refuses unknown keys, the secret keys (they are not in the file),
// version, and the email and provider of an account, which change only through account add and remove.
func ParseKey(c Config, name string) (Key, error) {
	parts := strings.Split(name, ".")
	k := Key{Name: name, path: parts}
	switch {
	case name == ScreeningAPIKey || name == ConsolePassword:
		return Key{}, fmt.Errorf("%s is a secret; run amgd config set %s and give the value on stdin", name, name)
	case name == "version":
		return Key{}, errors.New("version is fixed; this program writes version = 1")
	case name == "default_account", name == "screening.endpoint", name == "screening.model", name == "console.listen", name == "console.username":
		return k, nil
	case name == "screening.provider", name == "credentials.backend":
		k.Choice = true
		return k, nil
	case name == "console.hosts":
		k.list = true
		return k, nil
	case parts[0] == "policy" && policyKey(parts[1:]):
		k.Choice = true
		return k, nil
	case parts[0] == "accounts" && len(parts) >= 3:
		a, ok := c.Account(parts[1])
		if !ok {
			return Key{}, fmt.Errorf("unknown account %s; run amgd account add", keyPath(parts[1]))
		}
		k.Alias = a.Alias
		switch field := parts[2]; {
		case len(parts) == 3 && (field == "email" || field == "provider"):
			return Key{}, fmt.Errorf("%s %w", keyPath(parts...), ErrAccountCommandsOnly)
		case len(parts) == 3 && (field == "client_id" && a.Provider != "fake" || field == "tenant_id" && a.Provider == "m365" || field == "client_secret" && a.Provider == "gmail"):
			return k, nil
		case field == "policy" && policyKey(parts[3:]):
			k.Choice = true
			return k, nil
		}
	}
	return Key{}, fmt.Errorf("unknown key %s", keyPath(parts...))
}

// Set returns the change for Edit that sets the key. A list key takes a comma-separated value.
func (k Key) Set(value string) func(map[string]any) error {
	return func(document map[string]any) error {
		var v any = value
		if k.list {
			items := []any{}
			for item := range strings.SplitSeq(value, ",") {
				if item = strings.TrimSpace(item); item != "" {
					items = append(items, item)
				}
			}
			v = items
		}
		table := document
		for _, part := range k.path[:len(k.path)-1] {
			next, ok := table[part].(map[string]any)
			if !ok {
				next = map[string]any{}
				table[part] = next
			}
			table = next
		}
		table[k.path[len(k.path)-1]] = v
		return nil
	}
}

// Unset returns the change for Edit that removes the key, so its default applies again. Tables that it leaves
// empty go too.
func (k Key) Unset() func(map[string]any) error {
	return func(document map[string]any) error {
		var remove func(table map[string]any, path []string)
		remove = func(table map[string]any, path []string) {
			if len(path) == 1 {
				delete(table, path[0])
				return
			}
			if next, ok := table[path[0]].(map[string]any); ok {
				remove(next, path[1:])
				if len(next) == 0 {
					delete(table, path[0])
				}
			}
		}
		remove(document, k.path)
		return nil
	}
}

// Setting is one line of `amgd config list`: a key, its effective value, and whether the file sets it.
type Setting struct {
	Key, Value string
	// Default is true when the file does not set the key: the value is the default or the [policy] value.
	Default bool
	// Secret is true when Value only says set or not set.
	Secret bool
}

// List returns every key of the file format with its effective value, in a fixed order. Secrets in the file
// (a Gmail client secret) show only set. The secret keys of the credential store are not in the list.
func List(ctx context.Context, path string) ([]Setting, error) {
	c, err := Load(ctx, path)
	if err != nil {
		return nil, err
	}
	document := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if document, err = decode(data); err != nil {
			return nil, err
		}
	}
	set := func(path ...string) bool {
		var v any = document
		for _, p := range path {
			table, ok := v.(map[string]any)
			if !ok {
				return false
			}
			if v, ok = table[p]; !ok {
				return false
			}
		}
		return true
	}
	var out []Setting
	add := func(value string, path ...string) {
		out = append(out, Setting{Key: strings.Join(path, "."), Value: value, Default: !set(path...)})
	}
	add(fmt.Sprint(Version), "version")
	add(c.DefaultAccount, "default_account")
	policy := func(p Policy, prefix ...string) {
		for _, key := range baseKeys {
			add(string(*p.Base.field(key)), append(slices.Clone(prefix), key)...)
		}
		for _, key := range sealedKeys {
			add(string(*p.Sealed.field(key)), append(slices.Clone(prefix), "sealed", key)...)
		}
	}
	global, _ := globalPolicy(document)
	policy(global, "policy")
	add(c.Screening.Provider, "screening", "provider")
	add(c.Screening.Endpoint, "screening", "endpoint")
	add(c.Screening.Model, "screening", "model")
	add(c.Console.Listen, "console", "listen")
	add(strings.Join(c.Console.Hosts, ","), "console", "hosts")
	add(c.Console.Username, "console", "username")
	add(c.Backend, "credentials", "backend")
	for _, a := range c.Accounts {
		add(a.Provider, "accounts", a.Alias, "provider")
		add(a.Email, "accounts", a.Alias, "email")
		if a.Provider != "fake" {
			add(a.ClientID, "accounts", a.Alias, "client_id")
		}
		if a.Provider == "m365" {
			add(a.TenantID, "accounts", a.Alias, "tenant_id")
		}
		if a.Provider == "gmail" {
			out = append(out, Setting{Key: "accounts." + a.Alias + ".client_secret", Value: "set", Secret: true})
		}
		policy(a.Policy, "accounts", a.Alias, "policy")
	}
	return out, nil
}
