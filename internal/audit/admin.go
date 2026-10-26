package audit

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode"
)

// adminRecord is a change of an account, a credential, a setting, or a reload. It holds fixed event names, account
// keys, setting key names, fixed choice values, and fixed result codes only: never a secret or free text.
type adminRecord struct {
	Time    string `json:"time"`
	Event   string `json:"event"`
	Key     string `json:"key,omitempty"`
	Account string `json:"account,omitempty"`
	Value   string `json:"value,omitempty"`
	Result  string `json:"result,omitempty"`
}

// Fixed choice values of settings that config records may carry: policy decisions, screening providers, backends.
var choices = map[string]bool{"allow": true, "approve": true, "deny": true, "jev": true, "fake": true, "keychain": true, "file": true}

// Reload results: ok, or the refusal code of a reload that kept the old settings.
var reloadResults = map[string]bool{"ok": true, "config_invalid": true, "rules_invalid": true, "credentials_failed": true, "canceled": true}

var settingKey = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$`)

// validAccount accepts an account key: one line of printable characters with an @.
func validAccount(account string) bool {
	return strings.Contains(account, "@") && len(account) <= 254 && strings.IndexFunc(account, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) < 0
}

// Account appends account_added, account_login, or account_removed with the account key.
func (l *Log) Account(ctx context.Context, event, account string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if event != "account_added" && event != "account_login" && event != "account_removed" || !validAccount(account) {
		return errors.New("invalid account audit record")
	}
	return l.append(ctx, adminRecord{Time: now(), Event: event, Account: account})
}

// Credential appends a credential change without any value: screening_api_key_set, console_password_set,
// store_migrated, or store_password_changed.
func (l *Log) Credential(ctx context.Context, event string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	switch event {
	case "screening_api_key_set", "console_password_set", "store_migrated", "store_password_changed":
	default:
		return errors.New("invalid credential audit record")
	}
	return l.append(ctx, adminRecord{Time: now(), Event: event})
}

// Config appends config_changed or config_removed with the setting key. account is the key of the account that
// an accounts.ALIAS key changes, else empty. value is set only for a key with fixed choices; free text such as an
// endpoint, a model, or an address is never recorded.
func (l *Log) Config(ctx context.Context, event, key, account string, value *string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if event != "config_changed" && event != "config_removed" || !settingKey.MatchString(key) || len(key) > 256 ||
		account != "" && !validAccount(account) || value != nil && (event != "config_changed" || !choices[*value]) {
		return errors.New("invalid config audit record")
	}
	r := adminRecord{Time: now(), Event: event, Key: key, Account: account}
	if value != nil {
		r.Value = *value
	}
	return l.append(ctx, r)
}

// Reload appends the result of a reload: ok or a fixed refusal code.
func (l *Log) Reload(ctx context.Context, result string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !reloadResults[result] {
		return errors.New("invalid reload audit record")
	}
	return l.append(ctx, adminRecord{Time: now(), Event: "reload", Result: result})
}
