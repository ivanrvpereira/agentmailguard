package daemon_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/daemon"
)

// The email and the provider of an account can be read with config get, but change only through account remove
// and account add.
func TestConfigGetShowsTheEmailOfAnAccountThatOnlyAccountCommandsChange(t *testing.T) {
	t.Parallel()
	home := writeHome(t, keychainAccountConfig)
	if code, out, errout := amgd(t, home, daemon.Options{Platform: &keychain{}}, "", "config", "get", "accounts.work.email"); code != 0 || out != "synthetic@example.test\n" {
		t.Fatalf("get: exit %d stdout %q stderr %q", code, out, errout)
	}
	before := readFile(t, filepath.Join(home, "config.toml"))
	code, _, errout := amgd(t, home, daemon.Options{Platform: &keychain{}}, "", "config", "set", "accounts.work.email", "other@example.test")
	if code != 1 || !strings.HasSuffix(errout, "error: accounts.work.email changes only with amgd account remove and amgd account add\n") || readFile(t, filepath.Join(home, "config.toml")) != before {
		t.Fatalf("set: exit %d stderr %q", code, errout)
	}
}
