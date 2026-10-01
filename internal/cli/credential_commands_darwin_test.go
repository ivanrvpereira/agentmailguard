//go:build darwin

package cli_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/cli"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
)

func TestNativeChangePasswordInTemporaryHome(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "agentmailguard")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/agentmailguard")
	build.Dir = "../.."
	if data, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build failed: %s", data)
	}
	home := fileHome(t)
	credentialFileFixture(t, home)
	master, slave := passwordTerminal(t)
	var out bytes.Buffer
	stderr := &promptWriter{respond: func(text string) {
		if strings.Contains(text, "Credential file password:") || strings.Contains(text, "Confirm credential file password:") {
			if terminalEcho(t, slave) {
				t.Error("native input echoed")
			}
			_, _ = io.WriteString(master, "private-new-password\n")
		}
	}}
	cmd := exec.CommandContext(ctx, binary, "auth", "change-password", "--home", home)
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=/usr/bin:/bin", credentials.PasswordEnvironment + "=private-old-password"}
	cmd.Stdin = slave
	cmd.Stdout = &out
	cmd.Stderr = struct{ io.Writer }{stderr} // Keep os/exec's copy on Write, not bytes.Buffer.ReadFrom.
	if err := cmd.Run(); err != nil {
		t.Fatalf("native password change failed: %s", stderr.String())
	}
	if !terminalEcho(t, slave) {
		t.Fatal("native echo not restored")
	}
	if strings.Contains(out.String()+stderr.String(), "private-") || !strings.Contains(out.String(), "Restart") {
		t.Fatal("native output unsafe or incomplete")
	}
	fresh := credentials.NewFile(home, "private-new-password")
	if key, err := fresh.LoadOpenRouterKey(ctx); err != nil || key != "private-key" {
		t.Fatal("native password change lost record")
	}
	before, _ := os.ReadFile(filepath.Join(home, "credentials.age"))
	refused := exec.CommandContext(ctx, binary, "auth", "set-jev-key", "--home", home)
	refused.Env = cmd.Env
	refused.Stdin = strings.NewReader("replacement-key\n")
	if data, err := refused.CombinedOutput(); err == nil || bytes.Contains(data, []byte("private-")) {
		t.Fatal("native old password was accepted or leaked")
	}
	after, _ := os.ReadFile(filepath.Join(home, "credentials.age"))
	if !bytes.Equal(before, after) {
		t.Fatal("native old password replaced changed file")
	}
}

func TestChangePasswordTerminalSafety(t *testing.T) {
	for _, mode := range []string{"match-env", "match-stdin", "mismatch", "cancel", "invalid", "wrong-current", "unavailable-current", "echo-already-off"} {
		t.Run(mode, func(t *testing.T) {
			home := fileHome(t)
			before := credentialFileFixture(t, home)
			if mode == "unavailable-current" {
				if os.Geteuid() == 0 {
					t.Skip("root bypasses permissions")
				}
				if os.Chmod(filepath.Join(home, "credentials.age"), 0000) != nil {
					t.Fatal("deny file read")
				}
			}
			master, slave := passwordTerminal(t)
			if mode == "echo-already-off" {
				cmd := exec.Command("/bin/stty", "-echo")
				cmd.Stdin = slave
				if cmd.Run() != nil {
					t.Fatal("disable echo fixture")
				}
			}
			initialEcho := terminalEcho(t, slave)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			prompts := 0
			var out bytes.Buffer
			stderr := &promptWriter{}
			stderr.respond = func(text string) {
				if strings.Contains(text, "Credential file password:") || strings.Contains(text, "Confirm credential file password:") {
					prompts++
					if terminalEcho(t, slave) {
						t.Error("password echoed")
					}
					if mode == "cancel" {
						cancel()
						return
					}
					value := "private-new-password\n"
					if mode == "match-stdin" && prompts == 1 {
						value = "private-old-password\n"
					}
					if mode == "mismatch" && strings.Contains(text, "Confirm") {
						value = "different\n"
					}
					if mode == "invalid" {
						value = "\n"
					}
					_, _ = io.WriteString(master, value)
				}
			}
			lookup := func(k string) string {
				if k == credentials.PasswordEnvironment && mode != "match-stdin" {
					if mode == "wrong-current" {
						return "wrong-password"
					}
					return "private-old-password"
				}
				return ""
			}
			code := cli.Run(ctx, []string{"auth", "change-password", "--home", home}, &out, stderr, cli.Options{Stdin: slave, LookupEnv: lookup})
			if mode == "unavailable-current" {
				if os.Chmod(filepath.Join(home, "credentials.age"), 0600) != nil {
					t.Fatal("restore file read")
				}
				if !strings.Contains(stderr.String(), "credential store unavailable") || strings.Contains(stderr.String(), "Keychain") {
					t.Error("file-only failure did not identify the credential store")
				}
			}
			success := mode == "match-env" || mode == "match-stdin" || mode == "echo-already-off"
			if (code == 0) != success {
				t.Fatalf("unexpected result %d: %s", code, stderr.String())
			}
			if terminalEcho(t, slave) != initialEcho {
				t.Error("original echo state not restored")
			}
			if strings.Contains(out.String()+stderr.String(), "private-") {
				t.Fatal("secret output")
			}
			if success {
				wantPrompts := 2
				if mode == "match-stdin" {
					wantPrompts = 3
				}
				if prompts != wantPrompts {
					t.Fatal("new password not confirmed")
				}
				fresh := credentials.NewFile(home, "private-new-password")
				if key, err := fresh.LoadOpenRouterKey(ctx); err != nil || key != "private-key" {
					t.Fatal("key lost")
				}
				if !strings.Contains(out.String(), "Restart") {
					t.Fatal("restart instruction missing")
				}
			} else {
				after, _ := os.ReadFile(filepath.Join(home, "credentials.age"))
				if !bytes.Equal(before, after) {
					t.Fatal("failure changed file")
				}
			}
		})
	}
}
