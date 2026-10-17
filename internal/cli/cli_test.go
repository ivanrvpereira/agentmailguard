package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/cli"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

// stateful reports whether reading k would let the environment change what a command does. The color
// variables are display-only: they choose escape sequences and never a server, an account, or a file.
func stateful(k string) bool {
	switch k {
	case "NO_COLOR", "CLICOLOR", "CLICOLOR_FORCE", "TERM", "COLORTERM":
		return false
	}
	return true
}

func TestCommandsUseHTTPWithoutLocalState(t *testing.T) {
	cases := []struct {
		args       []string
		op, params string
	}{
		{[]string{"stats"}, "stats", `{"days":30}`},
		{[]string{"cache", "clear"}, "cache.clear", `{}`},
		{[]string{"rules", "list"}, "rules.list", `{}`},
		{[]string{"auth", "status"}, "auth.status", `{}`},
		{[]string{"mail", "list", "--folder", "f", "--count", "4", "--skip", "2"}, "mail.list", `{"folder":"f","count":4,"skip":2}`},
		{[]string{"mail", "get", "m"}, "mail.get", `{"id":"m"}`},
		{[]string{"mail", "thread", "t", "--count", "3"}, "mail.thread", `{"id":"t","count":3}`},
		{[]string{"mail", "search", "--text", "x", "--from", "alice", "--subject", "s", "--after", "2026-01-01", "--before", "2026-02-01", "--folder", "f", "--count", "9"}, "mail.search", `{"text":"x","from":"alice","subject":"s","after":"2026-01-01","before":"2026-02-01","folder":"f","count":9}`},
		{[]string{"mail", "link", "m", "L1", "L2"}, "mail.link", `{"id":"m","links":["L1","L2"]}`},
		{[]string{"mail", "link", "m", "--all"}, "mail.link", `{"id":"m","all":true}`},
		{[]string{"mail", "folders"}, "mail.folders", `{}`},
		{[]string{"mail", "categories"}, "mail.categories", `{}`},
		{[]string{"mail", "archive", "a", "b"}, "mail.archive", `{"ids":["a","b"]}`},
		{[]string{"mail", "trash", "a"}, "mail.trash", `{"ids":["a"]}`},
		{[]string{"mail", "move", "f", "a", "b"}, "mail.move", `{"folder":"f","ids":["a","b"]}`},
		{[]string{"mail", "read", "a"}, "mail.read", `{"ids":["a"]}`},
		{[]string{"mail", "unread", "a"}, "mail.unread", `{"ids":["a"]}`},
		{[]string{"mail", "flag", "a", "--state", "complete", "b"}, "mail.flag", `{"ids":["a","b"],"state":"complete"}`},
		{[]string{"mail", "categorize", "a", "--add", "One", "--add", "Two", "--remove", "Old"}, "mail.categorize", `{"ids":["a"],"add":["One","Two"],"remove":["Old"]}`},
		{[]string{"mail", "organize", "--since", "1m", "--until", "1d", "--apply"}, "mail.organize", `{"since":"1m","until":"1d","apply":true}`},
		{[]string{"calendar", "list", "--from", "2026-03-07", "--to", "+2d"}, "calendar.list", `{"start":"2026-03-07T00:00:00-05:00","end":"2026-03-09T00:00:00-04:00"}`},
		{[]string{"calendar", "get", "e"}, "calendar.get", `{"id":"e"}`},
		{[]string{"draft", "new", "--to", "a@test", "--to", "b@test", "--subject", "", "--body", "draft-canary"}, "draft.new", `{"to":["a@test","b@test"],"subject":"","body":"draft-canary"}`},
		{[]string{"draft", "reply", "m", "--body", "reply-canary"}, "draft.reply", `{"id":"m","body":"reply-canary"}`},
		{[]string{"draft", "forward", "m", "--to", "a@test", "--to", "b@test"}, "draft.forward", `{"id":"m","to":["a@test","b@test"]}`},
		{[]string{"draft", "update", "d", "--body", ""}, "draft.update", `{"id":"d","body":""}`},
		{[]string{"mail", "send", "--to", "a@test", "--to", "b@test", "--subject", "", "--body", "send-canary"}, "mail.send", `{"to":["a@test","b@test"],"subject":"","body":"send-canary"}`},
		{[]string{"mail", "reply", "m", "--body", ""}, "mail.reply", `{"id":"m","body":""}`},
		{[]string{"mail", "forward", "m", "--to", "a@test", "--to", "b@test"}, "mail.forward", `{"id":"m","to":["a@test","b@test"]}`},
	}
	loc, e := time.LoadLocation("America/New_York")
	if e != nil {
		t.Fatal(e)
	}
	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			calls := 0
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var req struct {
					Operation string
					Account   string
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
				if req.Operation != tc.op || req.Account != "remote" || string(a) != string(b) {
					t.Errorf("request %s %s %s", req.Operation, req.Account, b)
				}
				io.WriteString(w, `{"ok":true,"result":"日本"}`)
			}))
			defer h.Close()
			var out, errout bytes.Buffer
			code := cli.Run(context.Background(), append([]string{"--server", h.URL, "--account", "remote"}, tc.args...), &out, &errout, cli.Options{Now: func() time.Time { return time.Date(2026, 3, 31, 12, 0, 0, 0, loc) }, LookupEnv: func(k string) string {
				if k != "AGENTMAILGUARD_SERVER" && stateful(k) {
					t.Errorf("read local environment %s", k)
				}
				return ""
			}})
			if code != 0 || calls != 1 || errout.Len() != 0 || !strings.Contains(out.String(), "日本") {
				t.Errorf("exit %d calls %d out %s stderr %s", code, calls, out.String(), errout.String())
			}
		})
	}
}

type failedOutput struct{ writes int }

func (w *failedOutput) Write([]byte) (int, error) {
	w.writes++
	return 0, errors.New("private-body-writer-error-canary")
}

func TestOutputFailureIsSafeAndDoesNotRepeatOperation(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"json", []string{"mail", "archive", "source-id"}},
		{"pretty", []string{"mail", "get", "source-id", "--pretty"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				io.WriteString(w, `{"id":"source-id","verdict":"green","body":"private-body-canary"}`)
			}))
			defer h.Close()
			out := &failedOutput{}
			var errout bytes.Buffer
			code := cli.Run(context.Background(), append([]string{"--server", h.URL}, tc.args...), out, &errout, cli.Options{LookupEnv: func(string) string { return "" }})
			if code != 1 || errout.String() != "error: output failed\n" || calls.Load() != 1 || out.writes != 1 {
				t.Fatalf("exit=%d stderr=%q HTTP attempts=%d output attempts=%d", code, errout.String(), calls.Load(), out.writes)
			}
		})
	}
}

func TestHelpOutputFailure(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"-h"}, {"mail"}, {"mail", "--help"}, {"mail", "-h"}, {"mail", "get", "--help"}, {"stats", "--help"}} {
		out := &failedOutput{}
		var errout bytes.Buffer
		code := cli.Run(context.Background(), args, out, &errout, cli.Options{LookupEnv: func(k string) string {
			if stateful(k) {
				t.Fatal("help accessed state")
			}
			return ""
		}})
		if code != 1 || errout.String() != "error: output failed\n" || out.writes != 1 {
			t.Errorf("args=%v exit=%d stderr=%q output attempts=%d", args, code, errout.String(), out.writes)
		}
	}
}

func TestExplicitBlankAccountRefusedBeforeHTTPOrLocalState(t *testing.T) {
	var calls atomic.Int32
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		io.WriteString(w, `{}`)
	}))
	defer h.Close()
	for _, account := range []string{"", " ", "\t\n", "\u2003"} {
		for _, command := range [][]string{{"mail", "trash", "source-id"}, {"auth", "login", "--device-code"}} {
			for _, global := range []bool{true, false} {
				args := append([]string{}, command...)
				if global {
					args = append([]string{"--account", account}, args...)
				} else {
					args = append(args, "--account", account)
				}
				var out, errout bytes.Buffer
				lookups := 0
				code := cli.Run(context.Background(), args, &out, &errout, cli.Options{LookupEnv: func(k string) string {
					if stateful(k) {
						lookups++
					}
					return h.URL
				}})
				if code != 1 || out.Len() != 0 || errout.String() != "error: account must not be blank\n" || calls.Load() != 0 || lookups != 0 {
					t.Errorf("command=%v global=%t exit=%d stderr=%q requests=%d state lookups=%d", command, global, code, errout.String(), calls.Load(), lookups)
				}
			}
		}
	}
}

func TestPrettyAliasesUseLastOption(t *testing.T) {
	for _, tc := range []struct {
		flags  []string
		pretty bool
	}{
		{[]string{"--no-pretty"}, false},
		{[]string{"--pretty", "--no-pretty"}, false},
		{[]string{"--no-pretty", "--pretty"}, true},
		{[]string{"--no-pretty=false"}, true},
		{[]string{"--no-pretty", "--pretty=false"}, false},
	} {
		var calls atomic.Int32
		h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			io.WriteString(w, `{"id":"source-id","verdict":"red"}`)
		}))
		var out, errout bytes.Buffer
		args := append([]string{"--server", h.URL, "mail", "get", "source-id"}, tc.flags...)
		code := cli.Run(context.Background(), args, &out, &errout, cli.Options{LookupEnv: func(string) string { return "" }})
		h.Close()
		if code != 0 || errout.Len() != 0 || calls.Load() != 1 || json.Valid(out.Bytes()) == tc.pretty || strings.Contains(out.String(), "Verdict:     red") != tc.pretty {
			t.Errorf("flags=%v exit=%d out=%q stderr=%q", tc.flags, code, out.String(), errout.String())
		}
	}
	for _, tc := range []struct {
		args []string
		want string
	}{{[]string{"mail", "get", "--help"}, "--no-pretty"}, {[]string{"auth", "login", "--help"}, "--no-device-code"}} {
		var out, errout bytes.Buffer
		if code := cli.Run(context.Background(), tc.args, &out, &errout, cli.Options{}); code != 0 || !strings.Contains(out.String(), tc.want) {
			t.Fatalf("negative option %s absent from %v help", tc.want, tc.args)
		}
	}
}

func TestUsageAndHelpNeedNoServerOrState(t *testing.T) {
	for _, args := range [][]string{{"mail", "send", "x"}, {"draft", "update", "x"}, {"mail", "flag", "x", "--state", "urgent"}, {"mail", "get", "x", "y"}, {"stats", "--days", "0"}, {"mail", "link", "x"}, {"mail", "link", "x", "L1", "--all"}, {"mail", "get", "x", "--secret", "canary"}, {"calendar", "list", "--from", "tomorrow"}, {"mail", "organize", "--since", "0d"}, {"mail", "get", "x", "--home", "canary"},
		{"mail", "send", "--subject", "s", "--body", "b"}, {"mail", "send", "--to", "a", "--body", "b"}, {"mail", "send", "--to", "a", "--subject", "s"},
		{"mail", "reply", "x"}, {"mail", "reply", "--body", "b"}, {"mail", "reply", "x", "y", "--body", "b"}, {"mail", "forward", "x"}, {"mail", "forward", "--to", "a"}} {
		var out, errout bytes.Buffer
		code := cli.Run(context.Background(), args, &out, &errout, cli.Options{})
		if code != 2 || out.Len() != 0 || strings.Contains(errout.String(), "canary") {
			t.Errorf("%v exit %d stdout %s stderr %s", args, code, out.String(), errout.String())
		}
	}
	for _, tc := range []struct {
		args []string
		want []string
	}{{nil, []string{"serve", "mail", "calendar", "draft"}}, {[]string{"mail"}, []string{"organize", "categorize", "link"}}} {
		var out, errout bytes.Buffer
		args := append([]string{}, tc.args...)
		code := cli.Run(context.Background(), append(args, "--help"), &out, &errout, cli.Options{LookupEnv: func(k string) string {
			if stateful(k) {
				t.Fatal("help accessed state")
			}
			return ""
		}})
		for _, want := range tc.want {
			if code != 0 || !strings.Contains(out.String(), want) {
				t.Fatalf("help for %v is missing %s", tc.args, want)
			}
		}
	}
}

func TestOptionsFormattingIsRedacted(t *testing.T) {
	lookup := func(string) string { return "SYNTHETIC-PASSWORD" }
	opts := cli.Options{LookupEnv: lookup, Server: server.Options{LookupEnv: lookup}}
	for _, value := range []any{opts, &opts} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			if printed := fmt.Sprintf(verb, value); printed != "CLIOptions{redacted}" {
				t.Errorf("%s bypasses redaction for %T: %s", verb, value, printed)
			}
		}
	}
}
