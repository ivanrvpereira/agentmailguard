package config_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
)

func file(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	text = strings.ReplaceAll(text, `\n`, "\n")
	if err := os.WriteFile(path, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestHomeDefaultEnvironmentAndExplicitOverride(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENTMAILGUARD_HOME", "")
	got, err := config.Home("")
	if err != nil || got != filepath.Join(home, ".agentmailguard") {
		t.Fatalf("home=%q error=%v", got, err)
	}
	t.Setenv("AGENTMAILGUARD_HOME", "~/separate")
	got, err = config.Home("")
	if err != nil || got != filepath.Join(home, "separate") {
		t.Fatalf("home=%q error=%v", got, err)
	}
	got, err = config.Home(filepath.Join(home, "override"))
	if err != nil || got != filepath.Join(home, "override") {
		t.Fatalf("home=%q error=%v", got, err)
	}
}

func TestInvalidAccountsAreRefusedWithoutFileValues(t *testing.T) {
	for _, text := range []string{
		"", "[accounts.work", "accounts = 1", "[accounts]\\nwork = 1", "[accounts.work]\\nprovider = 'm365'\\nclient_id = 'x'",
		"[accounts.work]\\nprovider = 'SECRET-CANARY'", "[accounts.work]\\nprovider = 'gmail'\\nclient_id = 'x'\\nclient_secret = true",
		"[accounts.work]\\nprovider = 'gmail'\\nclient_id = 'x'\\nclient_secret = '  '", "[accounts.work]\\nprovider = 'gmail'\\nprovider = 'SECRET-CANARY'",
	} {
		path := file(t, text)
		if _, err := config.LoadAccount(context.Background(), path, nil); err == nil || strings.Contains(err.Error(), "SECRET-CANARY") {
			t.Fatalf("expected safe rejection: %v", err)
		}
	}
	path := filepath.Join(t.TempDir(), "missing")
	if _, err := config.AccountNames(context.Background(), path); err == nil {
		t.Fatal("missing config accepted")
	}
}

func TestFakeAccountsNeedNoProviderFields(t *testing.T) {
	path := file(t, "[accounts.demo]\nprovider = 'fake'\n[accounts.demo.policy]\nmail_send = 'allow'\n")
	got, err := config.LoadAccount(context.Background(), path, nil)
	if err != nil || got.Name != "demo" || got.Provider != "fake" || got.ClientID != "" || got.Policy.Base.MailSend != config.Allow {
		t.Fatalf("account=%v policy=%+v error=%v", got, got.Policy, err)
	}
	path = file(t, "[accounts.demo]\nprovider = 'other'\n")
	if _, err := config.LoadAccount(context.Background(), path, nil); err == nil || !strings.HasSuffix(err.Error(), "supported: m365, gmail, fake") {
		t.Fatalf("unsupported provider error=%v", err)
	}
}

func TestAccountSelectionUsesFileOrderAndProviderFields(t *testing.T) {
	path := file(t, `[accounts.work]
provider = " m365 "
client_id = " work-client "
tenant_id = " work-tenant "
client_secret = "ignored"
[accounts.home]
provider = "gmail"
client_id = "home-client"
client_secret = "SECRET-CANARY"
tenant_id = "ignored"
`)
	ctx := context.Background()
	names, err := config.AccountNames(ctx, path)
	if err != nil || !reflect.DeepEqual(names, []string{"work", "home"}) {
		t.Fatalf("names=%v error=%v", names, err)
	}
	got, err := config.LoadAccount(ctx, path, nil)
	if err != nil || got.Name != "work" || got.Provider != "m365" || got.ClientID != "work-client" || got.TenantID != "work-tenant" || got.ClientSecret != "" {
		t.Fatalf("account=%v error=%v", got, err)
	}
	name := "home"
	got, err = config.LoadAccount(ctx, path, &name)
	if err != nil || got.ClientSecret != "SECRET-CANARY" || got.TenantID != "" {
		t.Fatalf("account=%v error=%v", got, err)
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%d", "%f"} {
		if strings.Contains(fmt.Sprintf(format, got), "SECRET-CANARY") {
			t.Fatal("account formatting exposes secret")
		}
	}
}
