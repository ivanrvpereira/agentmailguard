//go:build darwin

package cli_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/cli"
	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

func TestSetApprovalPasswordTerminalConfirmationAndEcho(t *testing.T) {
	for _, mode := range []string{"match", "mismatch"} {
		t.Run(mode, func(t *testing.T) {
			master, slave := passwordTerminal(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			prompts := 0
			stderr := &promptWriter{}
			stderr.respond = func(text string) {
				answer := ""
				switch {
				case strings.Contains(text, "New approval password:"):
					answer = "private-terminal-pass\n"
				case strings.Contains(text, "Confirm new approval password:"):
					answer = "private-terminal-pass\n"
					if mode == "mismatch" {
						answer = "private-other-pass\n"
					}
				default:
					return
				}
				prompts++
				if terminalEcho(t, slave) {
					t.Error("approval password prompt left echo enabled")
				}
				_, _ = io.WriteString(master, answer)
			}
			store := approvalStore(t)
			code := cli.Run(ctx, []string{"auth", "set-approval-password"}, io.Discard, stderr, cli.Options{Stdin: slave, LookupEnv: func(string) string { return "" }, Server: server.Options{Credentials: store}})
			_, err := store.LoadApprovalHash(context.Background())
			if (code == 0) != (mode == "match") || prompts != 2 || (err == nil) != (mode == "match") {
				t.Fatalf("exit=%d prompts=%d stored=%v", code, prompts, err == nil)
			}
			if mode == "mismatch" && (!errors.Is(err, credentials.ErrNotFound) || !strings.Contains(stderr.String(), "error: approval passwords do not match")) {
				t.Fatal("mismatch was not refused")
			}
			if !terminalEcho(t, slave) {
				t.Fatal("terminal echo not restored")
			}
			if strings.Contains(stderr.String(), "private-") {
				t.Fatal("approval password leaked")
			}
		})
	}
}
