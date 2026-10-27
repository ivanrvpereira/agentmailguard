package audit_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/audit"
)

// records returns the audit lines without their time field.
func records(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	out := []map[string]any{}
	for line := range strings.Lines(string(data)) {
		var v map[string]any
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatal(err)
		}
		if _, ok := v["time"].(string); !ok {
			t.Fatalf("record without time: %s", line)
		}
		delete(v, "time")
		out = append(out, v)
	}
	return out
}

func TestAdminRecordsCarryOnlyKeysCodesAndFixedChoices(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	log := audit.New(path)
	allow := "allow"
	for _, err := range []error{
		log.Account(ctx, "account_added", "admin@example.test"),
		log.Account(ctx, "account_login", "admin@example.test"),
		log.Account(ctx, "account_removed", "admin@example.test"),
		log.Credential(ctx, "screening_api_key_set"),
		log.Credential(ctx, "console_password_set"),
		log.Credential(ctx, "store_migrated"),
		log.Credential(ctx, "store_password_changed"),
		log.Config(ctx, "config_changed", "policy.mail_send", "", &allow),
		log.Config(ctx, "config_changed", "accounts.work.policy.calendar_update", "admin@example.test", &allow),
		log.Config(ctx, "config_changed", "screening.endpoint", "", nil),
		log.Config(ctx, "config_removed", "console.hosts", "", nil),
		log.Reload(ctx, "ok"),
		log.Reload(ctx, "config_invalid"),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := []map[string]any{
		{"event": "account_added", "account": "admin@example.test"},
		{"event": "account_login", "account": "admin@example.test"},
		{"event": "account_removed", "account": "admin@example.test"},
		{"event": "screening_api_key_set"},
		{"event": "console_password_set"},
		{"event": "store_migrated"},
		{"event": "store_password_changed"},
		{"event": "config_changed", "key": "policy.mail_send", "value": "allow"},
		{"event": "config_changed", "key": "accounts.work.policy.calendar_update", "account": "admin@example.test", "value": "allow"},
		{"event": "config_changed", "key": "screening.endpoint"},
		{"event": "config_removed", "key": "console.hosts"},
		{"event": "reload", "result": "ok"},
		{"event": "reload", "result": "config_invalid"},
	}
	if got := records(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("records:\n%v\nwant:\n%v", got, want)
	}
}

func TestAdminRecordsRefuseFreeTextWithoutAppending(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	log := audit.New(path)
	endpoint, secret := "https://jev.example.test/CANARY", "CANARY-secret"
	for name, err := range map[string]error{
		"account event":     log.Account(ctx, "account_renamed", "admin@example.test"),
		"account not a key": log.Account(ctx, "account_added", "CANARY work"),
		"credential event":  log.Credential(ctx, "screening_api_key_CANARY"),
		"config value":      log.Config(ctx, "config_changed", "screening.endpoint", "", &endpoint),
		"config secret":     log.Config(ctx, "config_changed", "screening.api_key", "", &secret),
		"removed value":     log.Config(ctx, "config_removed", "policy.mail_send", "", new("allow")),
		"config key":        log.Config(ctx, "config_changed", "policy.CANARY value", "", nil),
		"config event":      log.Config(ctx, "config_CANARY", "policy.mail_send", "", nil),
		"reload result":     log.Reload(ctx, "CANARY"),
	} {
		if err == nil || strings.Contains(err.Error(), "CANARY") {
			t.Errorf("%s: error=%v", name, err)
		}
	}
	if got := records(t, path); len(got) != 0 {
		t.Fatalf("refused records appended: %v", got)
	}
}
