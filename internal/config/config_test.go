package config_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
)

// head is the top of a valid file whose default account is work; work is the table of that account.
const (
	head = "version = 1\ndefault_account = 'work'\n"
	work = "[accounts.work]\nprovider = 'm365'\nemail = 'work@example.test'\nclient_id = 'c'\ntenant_id = 't'\n"
)

func file(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func parse(t *testing.T, text string) (config.Config, error) {
	t.Helper()
	return config.Load(context.Background(), file(t, text))
}

// refusal fails the test unless err is the config error for path with no file value in it.
func refusal(t *testing.T, err error, path string) {
	t.Helper()
	e, ok := errors.AsType[config.Error](err)
	if !ok || e.Path != path || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("error=%v, want a config error at %q", err, path)
	}
}

func TestHomeDefaultEnvironmentAndExplicitOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AMG_HOME", "")
	got, err := config.Home("")
	if err != nil || got != filepath.Join(home, ".agentmailguard") {
		t.Fatalf("home=%q error=%v", got, err)
	}
	t.Setenv("AMG_HOME", "~/separate")
	got, err = config.Home("")
	if err != nil || got != filepath.Join(home, "separate") {
		t.Fatalf("home=%q error=%v", got, err)
	}
	got, err = config.Home(filepath.Join(home, "override"))
	if err != nil || got != filepath.Join(home, "override") {
		t.Fatalf("home=%q error=%v", got, err)
	}
}

func TestMissingFileIsAnEmptyConfiguration(t *testing.T) {
	got, err := config.Load(context.Background(), filepath.Join(t.TempDir(), "config.toml"))
	want := config.Config{Accounts: []config.Account{}, Screening: config.Screening{Provider: "jev"}, Console: config.Console{Listen: "localhost:8788", Username: "admin"}, Backend: "file"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("config=%+v error=%v", got, err)
	}
}

func TestVersionIsRequired(t *testing.T) {
	for _, text := range []string{"", "default_account = 'work'\n" + work, "version = 2\n", "version = '1'\n", "version = 1.0\n"} {
		_, err := parse(t, text)
		refusal(t, err, "version")
	}
	if _, err := parse(t, "version = 1\n"); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultAccountMustNameAnAccount(t *testing.T) {
	_, err := parse(t, "version = 1\n"+work)
	refusal(t, err, "default_account")
	_, err = parse(t, "version = 1\ndefault_account = 'home'\n"+work)
	refusal(t, err, "default_account")
	_, err = parse(t, "version = 1\ndefault_account = 'work'\n")
	refusal(t, err, "default_account")
	got, err := parse(t, head+work)
	if err != nil || got.DefaultAccount != "work" {
		t.Fatalf("config=%+v error=%v", got, err)
	}
}

func TestInvalidAccountsNameTheKeyWithoutFileValues(t *testing.T) {
	for _, tc := range []struct{ text, path string }{
		{"[accounts.work", ""},
		{"accounts = 1\n", "accounts"},
		{"[accounts]\nwork = 1\n", "accounts.work"},
		{"[accounts.work]\nprovider = 'm365'\nemail = 'work@example.test'\nclient_id = 'x'\n", "accounts.work.tenant_id"},
		{"[accounts.work]\nprovider = 'SECRET-CANARY'\nemail = 'work@example.test'\n", "accounts.work.provider"},
		{"[accounts.work]\nprovider = 'gmail'\nemail = 'work@example.test'\nclient_id = 'x'\nclient_secret = true\n", "accounts.work.client_secret"},
		{"[accounts.work]\nprovider = 'gmail'\nemail = 'work@example.test'\nclient_id = 'x'\nclient_secret = '  '\n", "accounts.work.client_secret"},
		{"[accounts.work]\nprovider = 'm365'\nclient_id = 'c'\ntenant_id = 't'\n", "accounts.work.email"},
		{"[accounts.work]\nprovider = 'fake'\nemail = 'SECRET-CANARY'\n", "accounts.work.email"},
		{"[accounts.work]\nprovider = 'fake'\nemail = 'SECRET CANARY@example.test'\n", "accounts.work.email"},
		{"[accounts.work]\nprovider = 'fake'\nemail = 'work@example.test'\nclient_id = 'SECRET-CANARY'\n", "accounts.work.client_id"},
		{"[accounts.work]\nprovider = 'm365'\nemail = 'work@example.test'\nclient_id = 'c'\ntenant_id = 't'\nclient_secret = 'SECRET-CANARY'\n", "accounts.work.client_secret"},
		{"[accounts.\"my work\"]\nprovider = 'fake'\nemail = 'work@example.test'\n", `accounts."my work"`},
		{"unknown = 'SECRET-CANARY'\n[accounts.work]\nprovider = 'fake'\nemail = 'work@example.test'\n", "unknown"},
	} {
		_, err := parse(t, head+tc.text)
		refusal(t, err, tc.path)
	}
}

func TestEmailsAreLowercaseAndUnique(t *testing.T) {
	got, err := parse(t, "version = 1\ndefault_account = 'work'\n[accounts.work]\nprovider = 'fake'\nemail = ' Admin@Example.TEST '\n")
	if err != nil || got.Accounts[0].Email != "admin@example.test" || got.Accounts[0].Key() != "admin@example.test" {
		t.Fatalf("config=%+v error=%v", got, err)
	}
	_, err = parse(t, "version = 1\ndefault_account = 'work'\n[accounts.work]\nprovider = 'fake'\nemail = 'admin@example.test'\n[accounts.other]\nprovider = 'fake'\nemail = 'ADMIN@example.test'\n")
	refusal(t, err, "accounts.work.email")
	if !strings.Contains(err.Error(), "same email as account other") {
		t.Fatalf("error=%v", err)
	}
}

func TestFakeAccountsNeedNoProviderFields(t *testing.T) {
	got, err := parse(t, "version = 1\ndefault_account = 'demo'\n[accounts.demo]\nprovider = 'fake'\nemail = 'demo@fake.invalid'\n[accounts.demo.policy]\nmail_send = 'allow'\n")
	if err != nil || len(got.Accounts) != 1 {
		t.Fatalf("config=%+v error=%v", got, err)
	}
	a := got.Accounts[0]
	if a.Alias != "demo" || a.Provider != "fake" || a.Email != "demo@fake.invalid" || a.ClientID != "" || a.Policy.Base.MailSend != config.Allow {
		t.Fatalf("account=%v policy=%+v", a, a.Policy)
	}
}

func TestAccountsAreInAliasOrderWithProviderFields(t *testing.T) {
	got, err := parse(t, `version = 1
default_account = "work"
[accounts.work]
provider = " m365 "
email = "work@example.test"
client_id = " work-client "
tenant_id = " work-tenant "
[accounts.home]
provider = "gmail"
email = "home@example.test"
client_id = "home-client"
client_secret = "SECRET-CANARY"
`)
	if err != nil {
		t.Fatal(err)
	}
	want := []config.Account{
		{Alias: "home", Email: "home@example.test", Provider: "gmail", ClientID: "home-client", ClientSecret: "SECRET-CANARY", Policy: config.DefaultPolicy},
		{Alias: "work", Email: "work@example.test", Provider: "m365", ClientID: "work-client", TenantID: "work-tenant", Policy: config.DefaultPolicy},
	}
	if !reflect.DeepEqual(got.Accounts, want) {
		t.Fatalf("accounts=%v", got.Accounts)
	}
	if a, ok := got.ByEmail("HOME@example.test"); !ok || a.Alias != "home" {
		t.Fatalf("by email=%v %v", a, ok)
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d", "%f"} {
		if strings.Contains(fmt.Sprintf(format, got.Accounts[0]), "SECRET-CANARY") {
			t.Fatal("account formatting exposes secret")
		}
	}
}

func TestCredentialsBackend(t *testing.T) {
	got, err := parse(t, "version = 1\n[credentials]\nbackend = 'file'\n")
	if err != nil || got.Backend != "file" {
		t.Fatalf("config=%+v error=%v", got, err)
	}
	got, err = parse(t, "version = 1\n[credentials]\nbackend = 'keychain'\n")
	if err != nil || got.Backend != "keychain" {
		t.Fatalf("config=%+v error=%v", got, err)
	}
	// An absent table selects the password-encrypted file, which works on every supported host.
	got, err = parse(t, "version = 1\n")
	if err != nil || got.Backend != "file" {
		t.Fatalf("default config=%+v error=%v", got, err)
	}
	for _, text := range []string{"[credentials]\nbackend = 'SECRET-CANARY'\n", "[credentials]\n"} {
		_, err := parse(t, "version = 1\n"+text)
		refusal(t, err, "credentials.backend")
	}
	_, err = parse(t, "version = 1\n[credentials]\nbackend = 'file'\nother = 'SECRET-CANARY'\n")
	refusal(t, err, "credentials.other")
}
