package cli_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/cli"
)

func TestLocalCommandsRefuseInvalidPolicyWithKeyPath(t *testing.T) {
	// The file backend keeps a regression away from the Keychain: it would stop at the password prompt.
	config := "[credentials]\nbackend='file'\n[accounts.work]\nprovider='m365'\nclient_id='synthetic'\ntenant_id='synthetic'\n[accounts.work.policy.sealed]\ncalendar_invite='SECRET-CANARY'\n"
	for _, args := range [][]string{
		{"serve", "--listen", "127.0.0.1:0"},
		{"auth", "login"},
		{"auth", "set-jev-key"},
		{"auth", "change-password"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			home := t.TempDir()
			if e := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); e != nil {
				t.Fatal(e)
			}
			var stderr strings.Builder
			code := cli.Run(context.Background(), append(args, "--home", home), io.Discard, &stderr, cli.Options{Stdin: strings.NewReader(""), LookupEnv: func(string) string { return "" }})
			text := stderr.String()
			if code != 1 || !strings.HasSuffix(text, "error: invalid policy: accounts.work.policy.sealed.calendar_invite\n") || strings.Contains(text, "server listening") || strings.Contains(text, "SECRET-CANARY") {
				t.Fatalf("exit=%d stderr=%q", code, text)
			}
		})
	}
}
