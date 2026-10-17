package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/cli"
)

// runOffline runs a command that must not read local state or reach a server.
func runOffline(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errout bytes.Buffer
	code := cli.Run(context.Background(), args, &out, &errout, cli.Options{LookupEnv: func(k string) string {
		if stateful(k) {
			t.Errorf("command %v read local state", args)
		}
		return ""
	}})
	return code, out.String(), errout.String()
}

func TestGroupWithoutSubcommandPrintsItsOwnHelp(t *testing.T) {
	for _, tc := range []struct{ group, want, absent string }{
		{"mail", "organize", "calendar"},
		{"auth", "set-screening-key", "calendar"},
		{"cache", "clear", "calendar"},
		{"rules", "list", "calendar"},
		{"approvals", "status", "calendar"},
		{"calendar", "get", "organize"},
		{"draft", "forward", "organize"},
	} {
		code, out, errout := runOffline(t, tc.group)
		if code != 2 || !strings.Contains(out, tc.want) || strings.Contains(out, tc.absent) || errout != "" {
			t.Errorf("%s: exit=%d stdout=%q stderr=%q", tc.group, code, out, errout)
		}
	}
}

func TestHelpExitsZeroOnEveryCommandPath(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"-h"}, {"mail", "--help"}, {"mail", "get", "-h"}, {"auth", "login", "--help"}, {"serve", "--help"}, {"stats", "--help"}, {"draft", "new", "--help"}} {
		code, out, errout := runOffline(t, args...)
		if code != 0 || out == "" || errout != "" {
			t.Errorf("args=%v exit=%d stdout=%q stderr=%q", args, code, out, errout)
		}
	}
}

func TestCredentialCommandHelpIsScopedAndOffline(t *testing.T) {
	for _, command := range []string{"migrate", "change-password"} {
		code, out, errout := runOffline(t, "auth", command, "--help")
		if code != 0 || !strings.Contains(out, "agentmailguard auth "+command+" [flags]") || !strings.Contains(out, "--home") || strings.Contains(out, "--device-code") || errout != "" {
			t.Errorf("%s: exit=%d stdout=%q stderr=%q", command, code, out, errout)
		}
		if strings.Contains(out, "--to string") != (command == "migrate") {
			t.Errorf("%s: migration target flag has the wrong scope", command)
		}
	}
	code, out, errout := runOffline(t, "--help")
	if code != 0 || errout != "" || !strings.Contains(out, "serve, auth login, set-screening-key, set-approval-password, migrate, and change-password only") {
		t.Error("root help does not explain the supported local home scope")
	}
}

func TestUnknownCommandsFlagsAndValuesAreUsageErrors(t *testing.T) {
	for _, args := range [][]string{{"mail", "bogus"}, {"mail", "get", "--bogus", "x"}, {"--bogus"}, {"mail", "list", "--count", "not-a-number"}, {"--timeout", "not-a-duration", "stats"}, {"--timeout", "0s", "stats"}} {
		code, out, errout := runOffline(t, args...)
		if code != 2 || out != "" || errout != "error: invalid command usage; use --help\n" {
			t.Errorf("args=%v exit=%d stdout=%q stderr=%q", args, code, out, errout)
		}
	}
}

// A command framework adds its own help and shell-completion commands; neither belongs to this interface,
// so each one stays an unknown word: exit 2, and never a list of completion choices on stdout.
func TestFrameworkHelpAndCompletionCommandsAreNotPartOfTheInterface(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"help", "mail"}, {"completion"}, {"__complete", "mail", ""}, {"__completeNoDesc", "mail", ""}} {
		code, out, errout := runOffline(t, args...)
		if code != 2 || (out != "" && !strings.Contains(out, "Usage:")) {
			t.Errorf("args=%v exit=%d stdout=%q stderr=%q", args, code, out, errout)
		}
	}
}

func TestPositionalsKeepFlagLookingText(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		op, params string
	}{
		{"separator", []string{"mail", "archive", "--", "--not-a-flag"}, "mail.archive", `{"ids":["--not-a-flag"]}`},
		{"interspersed", []string{"mail", "archive", "a", "--account", "remote", "b"}, "mail.archive", `{"ids":["a","b"]}`},
		{"option values", []string{"draft", "new", "--to", "a@test", "--subject", "--x", "--body", "--not-a-flag"}, "draft.new", `{"to":["a@test"],"subject":"--x","body":"--not-a-flag"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var req struct {
					Operation string
					Params    json.RawMessage
				}
				if json.NewDecoder(r.Body).Decode(&req) != nil {
					t.Fatal("invalid request")
				}
				var want, got any
				json.Unmarshal([]byte(tc.params), &want)
				json.Unmarshal(req.Params, &got)
				a, _ := json.Marshal(want)
				b, _ := json.Marshal(got)
				if req.Operation != tc.op || string(a) != string(b) {
					t.Errorf("request %s %s", req.Operation, b)
				}
				io.WriteString(w, `{"ok":true}`)
			}))
			defer h.Close()
			var out, errout bytes.Buffer
			code := cli.Run(context.Background(), append([]string{"--server", h.URL}, tc.args...), &out, &errout, cli.Options{LookupEnv: func(string) string { return "" }})
			if code != 0 || calls != 1 || errout.Len() != 0 {
				t.Errorf("exit=%d calls=%d stderr=%q", code, calls, errout.String())
			}
		})
	}
}
