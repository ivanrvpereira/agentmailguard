package e2e_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A release build sets the version with the same linker flag as the Makefile and the release workflow.
func TestReleaseBuildReportsTheVersionItWasBuiltWith(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"amg", "amgd"} {
		binary := filepath.Join(dir, name)
		build := exec.Command("go", "build", "-ldflags", "-X github.com/ivanrvpereira/agentmailguard/internal/version.version=v9.8.7-test", "-o", binary, "./cmd/"+name)
		build.Dir = "../.."
		if name == "amg" {
			build.Env = append(os.Environ(), "CGO_ENABLED=0")
		}
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", name, err, out)
		}
		out, err := exec.Command(binary, "--version").Output()
		if err != nil || string(out) != name+" version v9.8.7-test\n" {
			t.Errorf("%s --version: %q %v", name, out, err)
		}
	}
}
