package daemon_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/daemon"
	"github.com/ivanrvpereira/agentmailguard/internal/version"
)

func TestVersionPrintsTheBuildVersionWithoutTouchingTheHome(t *testing.T) {
	t.Parallel()
	home := filepath.Join(t.TempDir(), "missing")
	for _, args := range [][]string{{"--version"}, {"-v"}} {
		code, out, errout := amgd(t, home, daemon.Options{}, "", args...)
		if code != 0 || out != "amgd version "+version.String()+"\n" || errout != "" {
			t.Errorf("args=%v exit=%d stdout=%q stderr=%q", args, code, out, errout)
		}
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Fatalf("version created the home: %v", err)
	}
}
