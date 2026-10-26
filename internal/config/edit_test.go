package config_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
)

func edit(t *testing.T, path, key, value string) (config.Config, error) {
	t.Helper()
	c, err := config.Load(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	k, err := config.ParseKey(c, key)
	if err != nil {
		return config.Config{}, err
	}
	return config.Edit(context.Background(), path, k.Set(value))
}

func TestEditCreatesAVersionedFileWithTheHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "home", "config.toml")
	got, err := edit(t, path, "policy.mail_send", "approve")
	if err != nil || got.Accounts == nil || len(got.Accounts) != 0 {
		t.Fatalf("config=%+v error=%v", got, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "# Written by amgd. Comments are not kept.\nversion = 1\n\n[policy]\n  mail_send = \"approve\"\n"
	if string(data) != want {
		t.Fatalf("file:\n%s\nwant:\n%s", data, want)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("mode=%v error=%v", info.Mode(), err)
	}
}

func TestEditRefusesAnInvalidResultAndKeepsTheFile(t *testing.T) {
	original := "# my comment\n" + head + work
	path := file(t, original)
	for _, tc := range []struct{ key, value, problem string }{
		{"policy.mail_send", "always", "config.toml: policy.mail_send: must be allow, approve, or deny"},
		{"console.listen", "nowhere", "config.toml: console.listen: must be HOST:PORT"},
		{"default_account", "home", "config.toml: default_account: names no account"},
		{"screening.endpoint", "http://jev.example.test", "config.toml: screening.endpoint: must be an https URL"},
	} {
		if _, err := edit(t, path, tc.key, tc.value); err == nil || err.Error() != tc.problem {
			t.Errorf("%s: error=%v, want %q", tc.key, err, tc.problem)
		}
	}
	if data, _ := os.ReadFile(path); string(data) != original {
		t.Fatalf("file changed:\n%s", data)
	}
}

func TestEditKeepsEveryValueAndDropsComments(t *testing.T) {
	path := file(t, "# my comment\n"+head+"[console]\nlisten = '127.0.0.1:9000' # inline\n"+work+"[accounts.work.policy]\ncalendar_update = 'allow'\n")
	got, err := edit(t, path, "accounts.work.policy.sealed.mail_trash", "approve")
	if err != nil {
		t.Fatal(err)
	}
	a := got.Accounts[0]
	if got.Console.Listen != "127.0.0.1:9000" || a.Email != "work@example.test" || a.ClientID != "c" || a.Policy.Base.CalendarUpdate != config.Allow || a.Policy.Sealed.MailTrash != config.Approve {
		t.Fatalf("config=%+v account=%+v", got, a.Policy)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), "comment") || strings.Contains(string(data), "inline") || !strings.HasPrefix(string(data), config.Header) {
		t.Fatalf("file:\n%s", data)
	}
}

func TestUnsetRestoresTheDefaultAndRemovesEmptyTables(t *testing.T) {
	path := file(t, head+"[policy]\nmail_send = 'approve'\n"+work)
	c, _ := config.Load(context.Background(), path)
	k, err := config.ParseKey(c, "policy.mail_send")
	if err != nil {
		t.Fatal(err)
	}
	got, err := config.Edit(context.Background(), path, k.Unset())
	if err != nil || got.Accounts[0].Policy.Base.MailSend != config.Deny {
		t.Fatalf("config=%+v error=%v", got, err)
	}
	if data, _ := os.ReadFile(path); strings.Contains(string(data), "[policy]") {
		t.Fatalf("empty table kept:\n%s", data)
	}
}

func TestConsoleHostsTakeACommaSeparatedList(t *testing.T) {
	path := file(t, head+work)
	got, err := edit(t, path, "console.hosts", "my-mac.example.ts.net, 100.64.0.1:9000")
	if err != nil || !reflect.DeepEqual(got.Console.Hosts, []string{"my-mac.example.ts.net", "100.64.0.1:9000"}) {
		t.Fatalf("hosts=%v error=%v", got.Console.Hosts, err)
	}
	got, err = edit(t, path, "console.hosts", "")
	if err != nil || len(got.Console.Hosts) != 0 {
		t.Fatalf("hosts=%v error=%v", got.Console.Hosts, err)
	}
}

func TestParseKeyRefusesIdentityVersionSecretsAndUnknownKeys(t *testing.T) {
	c, err := parse(t, head+work)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"accounts.work.email":                   "accounts.work.email changes only with amgd account remove and amgd account add",
		"accounts.work.provider":                "accounts.work.provider changes only with amgd account remove and amgd account add",
		"accounts.work.client_secret":           "unknown key accounts.work.client_secret",
		"accounts.home.policy.mail_send":        "unknown account home; run amgd account add",
		"version":                               "version is fixed; this program writes version = 1",
		"screening.api_key":                     "screening.api_key is a secret; run amgd config set screening.api_key and give the value on stdin",
		"console.password":                      "console.password is a secret; run amgd config set console.password and give the value on stdin",
		"policy.mail_sned":                      "unknown key policy.mail_sned",
		"policy.sealed.calendar_invite":         "unknown key policy.sealed.calendar_invite",
		"policy.sealed.mail_draft":              "unknown key policy.sealed.mail_draft",
		"accounts.work.policy.sealed.mail_send": "unknown key accounts.work.policy.sealed.mail_send",
		"approvals.listen":                      "unknown key approvals.listen",
		"policy":                                "unknown key policy",
	} {
		if _, err := config.ParseKey(c, key); err == nil || err.Error() != want {
			t.Errorf("%s: error=%v, want %q", key, err, want)
		}
	}
	for key, choice := range map[string]bool{
		"policy.mail_send": true, "policy.sealed.mail_trash": true, "accounts.work.policy.calendar_update": true, "accounts.work.policy.sealed.mail_trash": true,
		"screening.provider": true, "credentials.backend": true,
		"screening.endpoint": false, "screening.model": false, "console.listen": false, "console.hosts": false, "default_account": false,
		"accounts.work.client_id": false, "accounts.work.tenant_id": false,
	} {
		k, err := config.ParseKey(c, key)
		if err != nil || k.Choice != choice {
			t.Errorf("%s: choice=%v error=%v", key, k.Choice, err)
		}
	}
	if k, _ := config.ParseKey(c, "accounts.work.policy.mail_send"); k.Alias != "work" {
		t.Errorf("alias=%q", k.Alias)
	}
}

func TestListShowsEffectiveValuesAndMarksDefaults(t *testing.T) {
	path := file(t, head+"[policy]\nmail_send = 'approve'\n"+work+"[accounts.home]\nprovider = 'gmail'\nemail = 'home@example.test'\nclient_id = 'h'\nclient_secret = 'SECRET-CANARY'\n[accounts.home.policy]\nmail_send = 'deny'\n")
	list, err := config.List(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]config.Setting{}
	for _, s := range list {
		got[s.Key] = s
		if strings.Contains(s.Value, "SECRET") {
			t.Fatalf("%s shows a secret", s.Key)
		}
	}
	for key, want := range map[string]config.Setting{
		"version":                                {Key: "version", Value: "1"},
		"default_account":                        {Key: "default_account", Value: "work"},
		"policy.mail_send":                       {Key: "policy.mail_send", Value: "approve"},
		"policy.mail_trash":                      {Key: "policy.mail_trash", Value: "allow", Default: true},
		"accounts.work.policy.mail_send":         {Key: "accounts.work.policy.mail_send", Value: "approve", Default: true},
		"accounts.home.policy.mail_send":         {Key: "accounts.home.policy.mail_send", Value: "deny"},
		"accounts.home.client_secret":            {Key: "accounts.home.client_secret", Value: "set", Secret: true},
		"accounts.work.email":                    {Key: "accounts.work.email", Value: "work@example.test"},
		"console.listen":                         {Key: "console.listen", Value: "localhost:8788", Default: true},
		"credentials.backend":                    {Key: "credentials.backend", Value: "file", Default: true},
		"screening.provider":                     {Key: "screening.provider", Value: "jev", Default: true},
		"screening.endpoint":                     {Key: "screening.endpoint", Default: true},
		"screening.model":                        {Key: "screening.model", Default: true},
		"accounts.home.policy.sealed.mail_trash": {Key: "accounts.home.policy.sealed.mail_trash", Value: "deny", Default: true},
	} {
		if got[key] != want {
			t.Errorf("%s=%+v, want %+v", key, got[key], want)
		}
	}
	for key := range got {
		if strings.Contains(key, ".sealed.") && !strings.HasSuffix(key, ".sealed.mail_trash") {
			t.Errorf("listed sealed key %s; the sealed tables accept only mail_trash", key)
		}
	}
}

// The lock file of an edit is private, also when it existed with wider permissions.
func TestEditKeepsTheLockFilePrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path+".lock", nil, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path+".lock", 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := edit(t, path, "policy.mail_send", "approve"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("lock file mode %v", info.Mode().Perm())
	}
}
