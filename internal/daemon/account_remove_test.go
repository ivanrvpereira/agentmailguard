package daemon_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
)

func (k *keychain) refuseWrites(refuse bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.failWrite = refuse
}

// A removal that fails before config.toml changes keeps the account, so amgd account remove can run again.
func TestAccountRemoveThatFailsKeepsTheAccountForAnotherTry(t *testing.T) {
	t.Parallel()
	home, k, c := writeHome(t, accountConfig), &keychain{}, newClock()
	opts := providerOptions(k, newM365(t), nil, c)
	if code, _, errout := amgd(t, home, opts, "", append([]string{"account", "add", "work"}, signInArgs["m365"]...)...); code != 0 {
		t.Fatalf("add: exit %d stderr %q", code, errout)
	}
	path := filepath.Join(home, "config.toml")
	keep := func(step string) {
		t.Helper()
		cfg, err := config.Load(t.Context(), path)
		if _, ok := cfg.Account("work"); err != nil || !ok {
			t.Fatalf("%s: the account is gone from config.toml:\n%s", step, readFile(t, path))
		}
		k.login(t, home, "m365", "ana@example.test")
	}

	// The cache cannot open: cache.db is a directory.
	if err := os.Mkdir(filepath.Join(home, "cache.db"), 0700); err != nil {
		t.Fatal(err)
	}
	if code, _, errout := amgd(t, home, opts, "", "account", "remove", "work", "--yes"); code != 1 || !strings.Contains(errout, "cache unavailable") {
		t.Fatalf("cache failure: exit %d stderr %q", code, errout)
	}
	if err := os.Remove(filepath.Join(home, "cache.db")); err != nil {
		t.Fatal(err)
	}
	keep("cache failure")

	// The Keychain refuses to delete the login.
	k.refuseWrites(true)
	if code, _, errout := amgd(t, home, opts, "", "account", "remove", "work", "--yes"); code != 1 || !strings.Contains(errout, "run amgd account remove again") {
		t.Fatalf("keychain failure: exit %d stderr %q", code, errout)
	}
	k.refuseWrites(false)
	keep("keychain failure")

	// config.toml cannot be edited: its lock file is a directory.
	if err := os.Remove(filepath.Join(home, "config.toml.lock")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "config.toml.lock"), 0700); err != nil {
		t.Fatal(err)
	}
	if code, _, errout := amgd(t, home, opts, "", "account", "remove", "work", "--yes"); code != 1 || !strings.HasSuffix(errout, "error: config.toml: cannot open the lock file; the account stays in config.toml, run amgd account remove again\n") {
		t.Fatalf("config failure: exit %d stderr %q", code, errout)
	}
	if err := os.Remove(filepath.Join(home, "config.toml.lock")); err != nil {
		t.Fatal(err)
	}
	if cfg, err := config.Load(t.Context(), path); err != nil || len(cfg.Accounts) != 1 {
		t.Fatalf("config failure: config.toml:\n%s", readFile(t, path))
	}

	if code, _, errout := amgd(t, home, opts, "", "account", "remove", "work", "--yes"); code != 0 || !strings.Contains(errout, "Removed account work (ana@example.test).") {
		t.Fatalf("retry: exit %d stderr %q", code, errout)
	}
	if cfg, err := config.Load(t.Context(), path); err != nil || len(cfg.Accounts) != 0 {
		t.Fatalf("config.toml after removal:\n%s", readFile(t, path))
	}
	if _, err := credentials.New(k, home).LoadOAuth(t.Context(), "m365", "ana@example.test"); !errors.Is(err, credentials.ErrNotFound) {
		t.Fatalf("login after removal: %v", err)
	}
	if got := auditEvents(t, home, "account_removed"); !reflect.DeepEqual(got, []map[string]any{{"event": "account_removed", "account": "ana@example.test"}}) {
		t.Fatalf("audit: %v", got)
	}
}

// Once config.toml or the credential store changed, the daemon must load it, even when a later step fails: the
// command reports the failure and still sends the reload.
func TestAChangeWrittenToConfigOrStoreReloadsTheDaemonEvenWhenItsAuditFails(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		stdin string
		args  []string
	}{
		{"", []string{"config", "set", "policy.mail_send", "approve"}},
		{"", []string{"config", "unset", "policy.mail_draft"}},
		{"", []string{"account", "remove", "second", "--yes"}},
		{"private-console-pass\n", []string{"config", "set", "console.password"}},
		{"sk-private-key\n", []string{"config", "set", "screening.api_key"}},
		{"", []string{"account", "login", "work"}},
	} {
		t.Run(strings.Join(tc.args[:3], " "), func(t *testing.T) {
			t.Parallel()
			api, console := freeAddress(t), freeAddress(t)
			home := writeHome(t, "version = 1\ndefault_account = 'demo'\n[credentials]\nbackend = 'keychain'\n[screening]\nprovider = 'fake'\n[console]\nlisten = '"+console+"'\n[policy]\nmail_draft = 'allow'\n[accounts.demo]\nprovider = 'fake'\nemail = 'demo@fake.invalid'\n[accounts.second]\nprovider = 'fake'\nemail = 'second@fake.invalid'\n")
			k := &keychain{}
			opts := providerOptions(k, newM365(t), nil, newClock())
			if code, _, errout := amgd(t, home, opts, "", append([]string{"account", "add", "work"}, signInArgs["m365"]...)...); code != 0 {
				t.Fatalf("add: exit %d stderr %q", code, errout)
			}
			serveOpts := opts
			serveOpts.Reloads = make(chan os.Signal, 1)
			startServe(t, home, api, serveOpts)
			configBefore := readFile(t, filepath.Join(home, "config.toml"))
			_, writesBefore := k.item()
			// audit.jsonl cannot be written.
			if err := os.Remove(filepath.Join(home, "audit.jsonl")); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(home, "audit.jsonl"), 0700); err != nil {
				t.Fatal(err)
			}
			var notified atomic.Int32
			opts.Notify = func(int) error { notified.Add(1); return nil }
			code, _, errout := amgd(t, home, opts, tc.stdin, tc.args...)
			if code != 1 || !strings.Contains(errout, "error: audit record failed\n") || notified.Load() != 1 || !strings.Contains(errout, "reload sent to daemon") || strings.Contains(errout, "private-") {
				t.Fatalf("exit %d notified %d stderr %q", code, notified.Load(), errout)
			}
			if _, writes := k.item(); readFile(t, filepath.Join(home, "config.toml")) == configBefore && writes == writesBefore {
				t.Fatal("neither config.toml nor the credential store changed")
			}
		})
	}
}
