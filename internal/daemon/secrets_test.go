package daemon_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/daemon"
)

const keychainConfig = "version = 1\n[credentials]\nbackend = 'keychain'\n"

// setSecret runs amgd config set KEY with stdin on a new keychain home and returns the home and the result.
func setSecret(t *testing.T, k *keychain, key, stdin string, args ...string) (string, int, string, string) {
	t.Helper()
	home := writeHome(t, keychainConfig)
	code, out, errout := amgd(t, home, daemon.Options{Platform: k}, stdin, append([]string{"config", "set", key}, args...)...)
	return home, code, out, errout
}

func consoleHash(t *testing.T, store *credentials.RecordStore) string {
	t.Helper()
	hash, err := store.LoadConsoleHash(t.Context())
	if err != nil {
		t.Fatalf("no console password hash: %v", err)
	}
	return hash
}

func TestSetScreeningKeySavesTrimmedKeyFromStdin(t *testing.T) {
	t.Parallel()
	k := &keychain{}
	home, code, out, errout := setSecret(t, k, "screening.api_key", "  sk-or-private-canary \n")
	if code != 0 || out != "" || !strings.Contains(errout, "Screening key saved.\n") || strings.Contains(errout, "private-canary") {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, errout)
	}
	if key, err := credentials.New(k, home).LoadScreeningKey(t.Context()); err != nil || key != "sk-or-private-canary" {
		t.Fatal("the trimmed key is not saved")
	}
	if got := auditEvents(t, home, "screening_api_key_set"); !reflect.DeepEqual(got, []map[string]any{{"event": "screening_api_key_set"}}) {
		t.Fatalf("audit: %v", got)
	}
}

func TestSetScreeningKeyRefusesUnsafeInputWithoutSaving(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, input, want string }{
		{"empty", " \n", "error: Screening key is empty\n"},
		{"eof", "", "error: Screening key is empty\n"},
		{"inner space", "sk-or private-canary\n", "error: Screening key must be one line without spaces or control characters\n"},
		{"control", "sk-or\x1bprivate-canary\n", "error: Screening key must be one line without spaces or control characters\n"},
		{"too long", "private-canary" + strings.Repeat("k", 4096), "error: Screening key is too long\n"},
		{"two lines", "sk-or-a\nprivate-canary\n", "error: Screening key must be one line without spaces or control characters\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := &keychain{}
			_, code, out, errout := setSecret(t, k, "screening.api_key", tc.input)
			if _, writes := k.item(); code != 1 || out != "" || writes != 0 || !strings.HasSuffix(errout, tc.want) || strings.Contains(errout, "private-canary") {
				t.Fatalf("exit %d writes %d stderr %q", code, writes, errout)
			}
		})
	}
}

// A secret given as an argument would stay in the shell history and the process list, so it is refused.
func TestSecretsAreRefusedAsArguments(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"screening.api_key", "console.password"} {
		k := &keychain{}
		_, code, out, errout := setSecret(t, k, key, "", "private-canary-secret")
		if _, writes := k.item(); code != 1 || out != "" || writes != 0 || errout != "error: "+key+" is a secret; give the value on stdin, never as an argument\n" {
			t.Fatalf("%s: exit %d writes %d stderr %q", key, code, writes, errout)
		}
	}
}

func TestSetSecretReportsStoreFailureWithoutTheSecret(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ key, input string }{
		{"screening.api_key", "sk-or-private-canary\n"},
		{"console.password", "private-canary-pass\n"},
	} {
		_, code, out, errout := setSecret(t, &keychain{failWrite: true}, tc.key, tc.input)
		if code != 1 || out != "" || !strings.HasSuffix(errout, "error: credential save failed\n") || strings.Contains(errout, "private-") {
			t.Fatalf("%s: exit %d stderr %q", tc.key, code, errout)
		}
	}
}

// The tests that hash run in parallel: bcrypt.DefaultCost is slow under -race.
func TestSetConsolePasswordStoresAHashOfThePipedPassword(t *testing.T) {
	t.Parallel()
	k := &keychain{}
	home, code, out, errout := setSecret(t, k, "console.password", "private-first-pass\n")
	if code != 0 || out != "" || !strings.HasPrefix(errout, "Console password saved. Sign in to the console as admin (console.username).\n") || strings.Contains(errout, "private-") {
		t.Fatalf("exit %d stdout %q stderr %q", code, out, errout)
	}
	if bcrypt.CompareHashAndPassword([]byte(consoleHash(t, credentials.New(k, home))), []byte("private-first-pass")) != nil {
		t.Fatal("stored hash does not verify the password")
	}
	if got := auditEvents(t, home, "console_password_set"); !reflect.DeepEqual(got, []map[string]any{{"event": "console_password_set"}}) {
		t.Fatalf("audit: %v", got)
	}
}

// A forgotten console password can be reset on the daemon host: a new one replaces it without the current one.
func TestSetConsolePasswordReplacesThePasswordWithoutTheCurrentOne(t *testing.T) {
	t.Parallel()
	k := &keychain{}
	home, code, _, errout := setSecret(t, k, "console.password", "private-first-pass\n")
	if code != 0 {
		t.Fatalf("first: exit %d stderr %q", code, errout)
	}
	if code, _, errout := amgd(t, home, daemon.Options{Platform: k}, "private-second-pass\n", "config", "set", "console.password"); code != 0 || strings.Contains(errout, "private-") {
		t.Fatalf("replace: exit %d stderr %q", code, errout)
	}
	hash := consoleHash(t, credentials.New(k, home))
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("private-second-pass")) != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte("private-first-pass")) == nil {
		t.Fatal("the new password did not replace the old one")
	}
}

func TestSetConsolePasswordRefusesUnsafeInputWithoutSaving(t *testing.T) {
	t.Parallel()
	const invalid = "error: console password must be 8 to 72 bytes without control characters\n"
	for _, tc := range []struct{ name, input string }{
		{"empty", "\n"},
		{"eof", ""},
		{"short", "private\n"},
		{"control", "private-\x1b[31m-pass\n"},
		{"carriage return", "private-pass\r\n"},
		{"too long", "private-" + strings.Repeat("p", 65) + "\n"},
		{"bad utf8", "private-\xff-pass\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := &keychain{}
			home, code, out, errout := setSecret(t, k, "console.password", tc.input)
			if code != 1 || out != "" || errout != invalid {
				t.Fatalf("exit %d stderr %q", code, errout)
			}
			if _, err := credentials.New(k, home).LoadConsoleHash(t.Context()); !errors.Is(err, credentials.ErrNotFound) {
				t.Fatal("refused password was stored")
			}
		})
	}
}

func TestSetConsolePasswordAcceptsSeventyTwoBytes(t *testing.T) {
	t.Parallel()
	k := &keychain{}
	password := strings.Repeat("é", 36)
	home, code, _, errout := setSecret(t, k, "console.password", password+"\n")
	if code != 0 {
		t.Fatalf("72-byte password refused: %q", errout)
	}
	if bcrypt.CompareHashAndPassword([]byte(consoleHash(t, credentials.New(k, home))), []byte(password)) != nil {
		t.Fatal("stored hash does not verify the password")
	}
}

func TestSetConsolePasswordReadsTheLineAfterTheFilePassword(t *testing.T) {
	t.Parallel()
	home := fileHome(t)
	code, _, errout := amgd(t, home, daemon.Options{}, "synthetic-password\nprivate-first-pass\n", "config", "set", "console.password")
	if code != 0 || strings.Contains(errout, "private-") || strings.Contains(errout, "synthetic-") {
		t.Fatalf("exit %d stderr %q", code, errout)
	}
	if bcrypt.CompareHashAndPassword([]byte(consoleHash(t, credentials.NewFile(home, "synthetic-password"))), []byte("private-first-pass")) != nil {
		t.Fatal("file backend hash does not verify the password")
	}
}
