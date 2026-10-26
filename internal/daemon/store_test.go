package daemon_test

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/daemon"
)

// keychainAccountConfig is fileConfig on the Keychain backend.
var keychainAccountConfig = strings.Replace(fileConfig, "backend = 'file'", "backend = 'keychain'", 1)

// seedStore saves a screening key and a login into store, as earlier commands would have.
func seedStore(t *testing.T, store *credentials.RecordStore) {
	t.Helper()
	if err := store.SaveScreeningKey(t.Context(), "private-key"); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveOAuth(t.Context(), "m365", "synthetic@example.test", credentials.OAuth{RefreshToken: "private-token"}); err != nil {
		t.Fatal(err)
	}
}

// checkSeeded fails unless store holds what seedStore saved.
func checkSeeded(t *testing.T, store *credentials.RecordStore) {
	t.Helper()
	if key, err := store.LoadScreeningKey(t.Context()); err != nil || key != "private-key" {
		t.Fatal("key lost")
	}
	if login, err := store.LoadOAuth(t.Context(), "m365", "synthetic@example.test"); err != nil || login.RefreshToken != "private-token" {
		t.Fatal("login lost")
	}
}

func TestStoreCommandsRefusePasswordArguments(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"store", "migrate", "--to", "keychain", "private-canary"},
		{"store", "migrate", "--to", "keychain", "--password", "private-canary"},
		{"store", "change-password", "private-canary"},
		{"store", "change-password", "--password", "private-canary"},
	} {
		home := fileHome(t)
		code, out, errout := amgd(t, home, daemon.Options{Platform: &keychain{}}, "", args...)
		if code != 2 || out != "" || errout != "error: invalid command usage; use --help\n" {
			t.Fatalf("%v: exit %d stdout %q stderr %q", args, code, out, errout)
		}
		if exists(t, home, "credentials.lock") {
			t.Fatalf("%v: refused command accessed credentials", args)
		}
	}
}

func TestStoreCommandsRefuseTheSameOrAnInvalidBackendAndAChangeWithoutTerminal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, config string
		args         []string
		code         int
		want         string
	}{
		{"same", fileConfig, []string{"store", "migrate", "--to", "file"}, 1, "error: migration requires the other credential backend\n"},
		{"invalid", fileConfig, []string{"store", "migrate", "--to", "private-canary"}, 2, "error: invalid command usage; use --help\n"},
		{"missing target", fileConfig, []string{"store", "migrate"}, 2, "error: invalid command usage; use --help\n"},
		{"keychain password", keychainAccountConfig, []string{"store", "change-password"}, 1, "error: password changes require the file backend\n"},
		{"nonterminal password", fileConfig, []string{"store", "change-password"}, 1, "error: new credential password requires a terminal\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := writeHome(t, tc.config)
			code, out, errout := amgd(t, home, daemon.Options{Platform: &keychain{}}, "private-old\nprivate-new\nprivate-new\n", tc.args...)
			if code != tc.code || out != "" || errout != tc.want {
				t.Fatalf("exit %d stdout %q stderr %q", code, out, errout)
			}
			if exists(t, home, "credentials.lock") {
				t.Fatal("refusal accessed credential backend")
			}
		})
	}
}

func TestMigrateFileToKeychainUsesStdinKeepsTheSourceAndSwitchesTheBackend(t *testing.T) {
	t.Parallel()
	home, k := fileHome(t), &keychain{}
	seedStore(t, credentials.NewFile(home, "private-old-password"))
	before := readFile(t, filepath.Join(home, "credentials.age"))
	code, out, errout := amgd(t, home, daemon.Options{Platform: k}, "private-old-password\n", "store", "migrate", "--to", "keychain")
	if code != 0 || out != "Credentials copied to the keychain backend; config.toml now uses it. The source credentials are unchanged.\nrestart needed: credentials.backend changed\n" || strings.Contains(out+errout, "private-") {
		t.Fatalf("migration: exit %d stdout %q stderr %q", code, out, errout)
	}
	checkSeeded(t, credentials.New(k, home))
	if readFile(t, filepath.Join(home, "credentials.age")) != before {
		t.Fatal("source changed")
	}
	if code, out, _ := amgd(t, home, daemon.Options{Platform: k}, "", "config", "get", "credentials.backend"); code != 0 || out != "keychain\n" {
		t.Fatalf("backend after migration: exit %d stdout %q", code, out)
	}
	want := []map[string]any{{"event": "store_migrated"}, {"event": "config_changed", "key": "credentials.backend", "value": "keychain"}}
	if got := auditEvents(t, home, "store_migrated", "config_changed"); !reflect.DeepEqual(got, want) {
		t.Fatalf("audit: %v", got)
	}
}

func TestMigrateKeychainToFileUsesTheEnvironmentPasswordAndNeverOverwritesATarget(t *testing.T) {
	t.Parallel()
	home, k := writeHome(t, keychainAccountConfig), &keychain{}
	seedStore(t, credentials.New(k, home))
	item, writes := k.item()
	opts := daemon.Options{Platform: k, LookupEnv: func(name string) string {
		if name == credentials.PasswordEnvironment {
			return "private-file-password"
		}
		return ""
	}}
	code, out, errout := amgd(t, home, opts, "", "store", "migrate", "--to", "file")
	if code != 0 || strings.Contains(out+errout, "private-") {
		t.Fatalf("migration: exit %d stdout %q stderr %q", code, out, errout)
	}
	checkSeeded(t, credentials.NewFile(home, "private-file-password"))
	if after, afterWrites := k.item(); string(after) != string(item) || afterWrites != writes {
		t.Fatal("source changed")
	}
	if code, out, _ := amgd(t, home, opts, "", "config", "get", "credentials.backend"); code != 0 || out != "file\n" {
		t.Fatalf("backend after migration: exit %d stdout %q", code, out)
	}

	// Back on the Keychain backend, a second migration finds the file and leaves it.
	if code, _, errout := amgd(t, home, opts, "", "config", "set", "credentials.backend", "keychain"); code != 0 {
		t.Fatalf("switch back: exit %d stderr %q", code, errout)
	}
	before := readFile(t, filepath.Join(home, "credentials.age"))
	code, out, errout = amgd(t, home, opts, "", "store", "migrate", "--to", "file")
	if code != 1 || out != "" || errout != "error: target credentials already exist\n" {
		t.Fatalf("second migration: exit %d stdout %q stderr %q", code, out, errout)
	}
	if readFile(t, filepath.Join(home, "credentials.age")) != before {
		t.Fatal("target changed")
	}
}
