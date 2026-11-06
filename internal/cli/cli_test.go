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
		{[]string{"rules", "list"}, "rules.list", `{}`},
		{[]string{"account", "list"}, "account.list", `{}`},
		{[]string{"account", "status"}, "account.status", `{}`},
		{[]string{"approvals", "status", "p"}, "approvals.status", `{"id":"p"}`},
		{[]string{"mail", "list"}, "mail.list", `{"folder":"inbox"}`},
		{[]string{"mail", "list", "--folder", "f", "--count", "4", "--page", "next-4"}, "mail.list", `{"folder":"f","count":4,"page":"next-4"}`},
		{[]string{"mail", "get", "m"}, "mail.get", `{"id":"m"}`},
		{[]string{"mail", "thread", "t"}, "mail.thread", `{"id":"t"}`},
		{[]string{"mail", "search", "--text", "x"}, "mail.search", `{"text":"x"}`},
		{[]string{"mail", "search", "--text", "x", "--from", "alice", "--to", "bob", "--subject", "s", "--after", "2026-01-01", "--before", "2026-02-01", "--folder", "f", "--count", "9", "--page", "next-9"}, "mail.search", `{"text":"x","from":"alice","to":"bob","subject":"s","after":"2026-01-01T00:00:00-05:00","before":"2026-02-02T00:00:00-05:00","folder":"f","count":9,"page":"next-9"}`},
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
		{[]string{"mail", "archive", "--thread", "t"}, "mail.archive", `{"thread":"t"}`},
		{[]string{"mail", "trash", "--thread", "t", "--in", "f"}, "mail.trash", `{"thread":"t","in":"f"}`},
		{[]string{"mail", "spam", "--thread", "t", "--in", "f"}, "mail.spam", `{"thread":"t","in":"f"}`},
		{[]string{"mail", "move", "f", "--thread", "t", "--in", "Label_2"}, "mail.move", `{"folder":"f","thread":"t","in":"Label_2"}`},
		{[]string{"mail", "read", "--thread", "t"}, "mail.read", `{"thread":"t"}`},
		{[]string{"mail", "unread", "--in", "inbox", "--thread", "t"}, "mail.unread", `{"thread":"t","in":"inbox"}`},
		{[]string{"mail", "categorize", "--thread", "t", "--add", "One"}, "mail.categorize", `{"thread":"t","add":["One"]}`},
		{[]string{"mail", "organize", "--since", "1m", "--until", "1d", "--apply"}, "mail.organize", `{"since":"2026-02-28T00:00:00-05:00","until":"2026-03-31T00:00:00-04:00","apply":true}`},
		{[]string{"mail", "organize"}, "mail.organize", `{"since":"2026-03-24T00:00:00-04:00"}`},
		{[]string{"calendar", "list", "--from", "2026-03-07", "--to", "+1d"}, "calendar.list", `{"start":"2026-03-07T00:00:00-05:00","end":"2026-03-09T00:00:00-04:00"}`},
		{[]string{"calendar", "list"}, "calendar.list", `{"start":"2026-03-31T00:00:00-04:00","end":"2026-04-08T00:00:00-04:00"}`},
		{[]string{"calendar", "list", "--from", "2026-03-07", "--to", "2026-03-07"}, "calendar.list", `{"start":"2026-03-07T00:00:00-05:00","end":"2026-03-08T00:00:00-05:00"}`},
		{[]string{"calendar", "get", "e"}, "calendar.get", `{"id":"e"}`},
		{[]string{"calendar", "search", "budget plan"}, "calendar.search", `{"text":"budget plan"}`},
		{[]string{"calendar", "search", "budget plan", "--page", "25"}, "calendar.search", `{"text":"budget plan","page":"25"}`},
		{[]string{"draft", "new", "--to", "a@test", "--to", "b@test", "--subject", "", "--body", "draft-canary"}, "draft.new", `{"to":["a@test","b@test"],"cc":[],"bcc":[],"subject":"","body":"draft-canary","html":false}`},
		{[]string{"draft", "new"}, "draft.new", `{"to":[],"cc":[],"bcc":[],"subject":"","body":"","html":false}`},
		{[]string{"draft", "reply", "m", "--body", "reply-canary"}, "draft.reply", `{"id":"m","body":"reply-canary","html":false,"all":false,"cc":[],"bcc":[]}`},
		{[]string{"draft", "reply", "m", "--all", "--cc", "c@test", "--cc", "d@test", "--bcc", "e@test", "--body", "<p>reply-canary</p>", "--html"}, "draft.reply", `{"id":"m","body":"<p>reply-canary</p>","html":true,"all":true,"cc":["c@test","d@test"],"bcc":["e@test"]}`},
		{[]string{"draft", "forward", "m", "--to", "a@test", "--to", "b@test"}, "draft.forward", `{"id":"m","to":["a@test","b@test"],"cc":[],"bcc":[],"body":"","html":false}`},
		{[]string{"draft", "forward", "m", "--body", "forward-canary"}, "draft.forward", `{"id":"m","to":[],"cc":[],"bcc":[],"body":"forward-canary","html":false}`},
		{[]string{"draft", "update", "d", "--body", ""}, "draft.update", `{"id":"d","body":""}`},
		{[]string{"draft", "update", "d", "--to", "a@test", "--to", "b@test", "--cc", "", "--subject", "s", "--body", "<p>update-canary</p>", "--html"}, "draft.update", `{"id":"d","to":["a@test","b@test"],"cc":[],"subject":"s","body":"<p>update-canary</p>","html":true}`},
		{[]string{"draft", "update", "d", "--bcc", "e@test"}, "draft.update", `{"id":"d","bcc":["e@test"]}`},
		{[]string{"draft", "delete", "d"}, "draft.delete", `{"id":"d"}`},
		{[]string{"draft", "send", "d"}, "draft.send", `{"id":"d"}`},
		{[]string{"mail", "send", "--to", "a@test", "--to", "b@test", "--subject", "", "--body", "send-canary"}, "mail.send", `{"to":["a@test","b@test"],"cc":[],"bcc":[],"subject":"","body":"send-canary","html":false}`},
		{[]string{"mail", "send", "--cc", "c@test", "--bcc", "d@test", "--bcc", "e@test", "--body", "<p>send-canary</p>", "--html"}, "mail.send", `{"to":[],"cc":["c@test"],"bcc":["d@test","e@test"],"subject":"","body":"<p>send-canary</p>","html":true}`},
		{[]string{"mail", "reply", "m"}, "mail.reply", `{"id":"m","body":"","html":false,"all":false,"cc":[],"bcc":[]}`},
		{[]string{"mail", "reply", "m", "--all", "--bcc", "e@test", "--body", "reply-canary"}, "mail.reply", `{"id":"m","body":"reply-canary","html":false,"all":true,"cc":[],"bcc":["e@test"]}`},
		{[]string{"mail", "forward", "m", "--to", "a@test", "--to", "b@test"}, "mail.forward", `{"id":"m","to":["a@test","b@test"],"cc":[],"bcc":[],"body":"","html":false}`},
		{[]string{"mail", "forward", "m", "--to", "a@test", "--cc", "c@test", "--bcc", "d@test", "--bcc", "e@test", "--body", "<p>forward-canary</p>", "--html"}, "mail.forward", `{"id":"m","to":["a@test"],"cc":["c@test"],"bcc":["d@test","e@test"],"body":"<p>forward-canary</p>","html":true}`},
		{[]string{"mail", "forward", "m", "--bcc", "d@test"}, "mail.forward", `{"id":"m","to":[],"cc":[],"bcc":["d@test"],"body":"","html":false}`},
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
			code := cli.Run(context.Background(), append([]string{"--host", h.URL, "--account", "remote"}, tc.args...), &out, &errout, cli.Options{Now: func() time.Time { return time.Date(2026, 3, 31, 12, 0, 0, 0, loc) }, LookupEnv: func(k string) string {
				if stateful(k) {
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
			code := cli.Run(context.Background(), append([]string{"-H", h.URL}, tc.args...), out, &errout, cli.Options{LookupEnv: func(string) string { return "" }})
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
		for _, command := range [][]string{{"mail", "trash", "source-id"}, {"account", "status"}} {
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
		args := append([]string{"-H", h.URL, "mail", "get", "source-id"}, tc.flags...)
		code := cli.Run(context.Background(), args, &out, &errout, cli.Options{LookupEnv: func(string) string { return "" }})
		h.Close()
		if code != 0 || errout.Len() != 0 || calls.Load() != 1 || json.Valid(out.Bytes()) == tc.pretty || strings.Contains(out.String(), "Verdict:     red") != tc.pretty {
			t.Errorf("flags=%v exit=%d out=%q stderr=%q", tc.flags, code, out.String(), errout.String())
		}
	}
	var out, errout bytes.Buffer
	if code := cli.Run(context.Background(), []string{"mail", "get", "--help"}, &out, &errout, cli.Options{}); code != 0 || !strings.Contains(out.String(), "--no-pretty") {
		t.Fatal("negative option --no-pretty absent from mail get help")
	}
}

func TestUsageAndHelpNeedNoServerOrState(t *testing.T) {
	for _, args := range [][]string{{"mail", "send", "x"}, {"draft", "update", "x"}, {"draft", "update", "x", "--html"}, {"draft", "delete"}, {"draft", "delete", "x", "y"}, {"draft", "send"}, {"draft", "send", "x", "y"}, {"draft", "send", "x", "--body", "b"}, {"draft", "update", "x", "--subject", "s", "--html"}, {"mail", "flag", "x", "--state", "urgent"}, {"mail", "get", "x", "y"}, {"stats", "--days", "0"}, {"mail", "link", "x"}, {"mail", "link", "x", "L1", "--all"}, {"mail", "get", "x", "--secret", "canary"}, {"calendar", "list", "--from", "tomorrow"}, {"calendar", "list", "--from", "2026-03-07", "--to", "2026-03-06"}, {"calendar", "list", "--text", "x"}, {"calendar", "list", "--organizer", "x"}, {"calendar", "list", "--attendee", "x"}, {"calendar", "search"}, {"calendar", "search", "a", "b"}, {"calendar", "search", "a", "--from", "today"}, {"mail", "thread", "t", "--count", "3"}, {"mail", "list", "--skip", "2"}, {"mail", "thread", "t", "--page", "2"}, {"calendar", "search", "a", "--count", "3"}, {"mail", "organize", "--since", "0d"}, {"mail", "get", "x", "--home", "canary"},
		{"mail", "send", "--subject", "s", "--body", "b"}, {"mail", "send", "--html"}, {"draft", "new", "--html=maybe"},
		{"mail", "archive"}, {"mail", "archive", "a", "--thread", "t"}, {"mail", "trash", "--in", "inbox"}, {"mail", "read", "a", "--in", "inbox"}, {"mail", "unread"},
		{"mail", "move", "f"}, {"mail", "move", "f", "a", "--thread", "t"}, {"mail", "move", "--thread", "t"}, {"mail", "move", "f", "--in", "inbox", "a"},
		{"mail", "categorize", "--add", "One"}, {"mail", "categorize", "a", "--thread", "t", "--add", "One"}, {"mail", "flag", "--thread", "t", "--state", "flagged"}, {"mail", "flag", "a", "--state", "flagged", "--in", "inbox"},
		{"mail", "reply", "--body", "b"}, {"draft", "reply", "x", "--all=maybe"}, {"mail", "reply", "x", "y", "--body", "b"}, {"mail", "forward", "x"}, {"mail", "forward", "--to", "a"}, {"mail", "forward", "x", "--body", "b", "--html"}, {"draft", "forward", "x", "--html=maybe"}} {
		var out, errout bytes.Buffer
		code := cli.Run(context.Background(), args, &out, &errout, cli.Options{})
		if code != 2 || out.Len() != 0 || strings.Contains(errout.String(), "canary") {
			t.Errorf("%v exit %d stdout %s stderr %s", args, code, out.String(), errout.String())
		}
	}
	for _, tc := range []struct {
		args []string
		want []string
	}{{nil, []string{"account", "mail", "calendar", "draft", "AMG_HOST"}}, {[]string{"mail"}, []string{"organize", "categorize", "link"}},
		{[]string{"mail", "archive"}, []string{"--thread", "--in", "Inbox"}}, {[]string{"mail", "trash"}, []string{"--thread", "--in"}}, {[]string{"mail", "move"}, []string{"--thread", "--in"}},
		{[]string{"mail", "read"}, []string{"--thread", "--in"}}, {[]string{"mail", "unread"}, []string{"--thread", "--in"}}, {[]string{"mail", "categorize"}, []string{"--thread", "--in"}}} {
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
	opts := cli.Options{LookupEnv: lookup}
	for _, value := range []any{opts, &opts} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
			if printed := fmt.Sprintf(verb, value); printed != "CLIOptions{redacted}" {
				t.Errorf("%s bypasses redaction for %T: %s", verb, value, printed)
			}
		}
	}
}

// Date options are client days: the first day starts at its local midnight and the last day ends at the next
// local midnight. Relative organize units count back from today. Where a midnight falls in a DST gap
// (America/Santiago on 2024-09-08), the day starts at its first instant, 01:00.
func TestDateOptionsResolveToLocalMidnightsOnTheClientClock(t *testing.T) {
	zone := func(name string) *time.Location {
		loc, e := time.LoadLocation(name)
		if e != nil {
			t.Fatal(e)
		}
		return loc
	}
	lisbon, newYork, santiago := zone("Europe/Lisbon"), zone("America/New_York"), zone("America/Santiago")
	utcMinus7 := time.FixedZone("UTC-7", -7*60*60)
	for _, tc := range []struct {
		now    time.Time
		args   []string
		params string
	}{
		{time.Date(2024, 3, 31, 23, 0, 0, 0, utcMinus7), []string{"mail", "organize", "--since", "1m"}, `{"since":"2024-02-29T00:00:00-07:00"}`},
		{time.Date(2023, 3, 31, 12, 0, 0, 0, lisbon), []string{"mail", "organize", "--since", "1m"}, `{"since":"2023-02-28T00:00:00Z"}`},
		{time.Date(2026, 3, 8, 21, 0, 0, 0, newYork), []string{"mail", "organize", "--since", "1d"}, `{"since":"2026-03-07T00:00:00-05:00"}`},
		{time.Date(2026, 3, 31, 12, 0, 0, 0, lisbon), []string{"mail", "organize", "--since", "1w", "--until", "1d"}, `{"since":"2026-03-24T00:00:00Z","until":"2026-03-31T00:00:00+01:00"}`},
		{time.Date(2026, 3, 31, 12, 0, 0, 0, lisbon), []string{"mail", "organize", "--since", "2026-03-31", "--until", "2026-03-31"}, `{"since":"2026-03-31T00:00:00+01:00","until":"2026-04-01T00:00:00+01:00"}`},
		{time.Date(2024, 9, 8, 12, 0, 0, 0, santiago), []string{"mail", "organize", "--since", "1d", "--until", "2024-09-07"}, `{"since":"2024-09-07T00:00:00-04:00","until":"2024-09-08T01:00:00-03:00"}`},
		{time.Date(2024, 9, 10, 12, 0, 0, 0, santiago), []string{"mail", "organize", "--since", "2d", "--until", "2024-09-08"}, `{"since":"2024-09-08T01:00:00-03:00","until":"2024-09-09T00:00:00-03:00"}`},
		{time.Date(2024, 10, 8, 12, 0, 0, 0, santiago), []string{"mail", "organize", "--since", "1m"}, `{"since":"2024-09-08T01:00:00-03:00"}`},
		{time.Date(2024, 9, 10, 12, 0, 0, 0, santiago), []string{"mail", "search", "--after", "2024-09-08", "--before", "2024-09-08"}, `{"after":"2024-09-08T01:00:00-03:00","before":"2024-09-09T00:00:00-03:00"}`},
		{time.Date(2024, 9, 10, 12, 0, 0, 0, santiago), []string{"mail", "search", "--after", "2024-09-07", "--before", "2024-09-07"}, `{"after":"2024-09-07T00:00:00-04:00","before":"2024-09-08T01:00:00-03:00"}`},
		{time.Date(2024, 9, 1, 12, 0, 0, 0, santiago), []string{"calendar", "list"}, `{"end":"2024-09-09T00:00:00-03:00","start":"2024-09-01T00:00:00-04:00"}`},
		{time.Date(2024, 9, 8, 12, 0, 0, 0, santiago), []string{"calendar", "list", "--from", "today", "--to", "+1d"}, `{"end":"2024-09-10T00:00:00-03:00","start":"2024-09-08T01:00:00-03:00"}`},
		{time.Date(2024, 9, 1, 12, 0, 0, 0, santiago), []string{"calendar", "list", "--from", "2024-09-06", "--to", "2024-09-07"}, `{"end":"2024-09-08T01:00:00-03:00","start":"2024-09-06T00:00:00-04:00"}`},
	} {
		var got string
		h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct{ Params json.RawMessage }
			json.NewDecoder(r.Body).Decode(&req)
			got = string(req.Params)
			io.WriteString(w, `{}`)
		}))
		var out, errout bytes.Buffer
		code := cli.Run(context.Background(), append([]string{"-H", h.URL}, tc.args...), &out, &errout, cli.Options{Now: func() time.Time { return tc.now }, LookupEnv: func(string) string { return "" }})
		h.Close()
		if code != 0 || got != tc.params {
			t.Errorf("%v at %v: exit %d params %s want %s", tc.args, tc.now, code, got, tc.params)
		}
	}
	for _, args := range [][]string{{"mail", "organize", "--since", "999999999999999999999m"}, {"mail", "organize", "--since", "0d"}, {"mail", "organize", "--since", "7d", "--until", "2026-02-30"},
		{"mail", "search", "--after", "2026-03-07", "--before", "2026-03-06"}} {
		var out, errout bytes.Buffer
		if code := cli.Run(context.Background(), append([]string{"-H", "http://127.0.0.1:1"}, args...), &out, &errout, cli.Options{LookupEnv: func(string) string { return "" }}); code != 2 {
			t.Errorf("%v: exit %d stderr %s", args, code, errout.String())
		}
	}
}

func TestDaemonAddressFromHostOptionThenEnvironmentThenDefault(t *testing.T) {
	hit := func(name string) (*httptest.Server, *atomic.Int32) {
		var calls atomic.Int32
		h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			io.WriteString(w, `{}`)
		}))
		t.Cleanup(h.Close)
		return h, &calls
	}
	flag, flagCalls := hit("flag")
	env, envCalls := hit("env")
	for _, tc := range []struct {
		args        []string
		flags, envs int32
	}{
		{[]string{"-H", flag.URL, "stats"}, 1, 0},
		{[]string{"--host", flag.URL, "stats"}, 2, 0},
		{[]string{"stats"}, 2, 1},
	} {
		var out, errout bytes.Buffer
		code := cli.Run(context.Background(), tc.args, &out, &errout, cli.Options{LookupEnv: func(k string) string {
			if k == "AMG_HOST" {
				return env.URL
			}
			return ""
		}})
		if code != 0 || flagCalls.Load() != tc.flags || envCalls.Load() != tc.envs {
			t.Fatalf("%v: exit %d flag calls %d env calls %d stderr %s", tc.args, code, flagCalls.Load(), envCalls.Load(), errout.String())
		}
	}
}

// amg is the agent's tool: an allowlist for it must not allow any management command.
func TestAgentClientHasNoManagementCommands(t *testing.T) {
	for _, args := range [][]string{{"serve"}, {"reload"}, {"config"}, {"store"}, {"auth"}, {"cache"}, {"account", "add", "work"}, {"account", "login", "work"}, {"account", "remove", "work"}, {"mail", "get", "x", "--home", "/tmp"}} {
		var out, errout bytes.Buffer
		code := cli.Run(context.Background(), args, &out, &errout, cli.Options{LookupEnv: func(k string) string {
			if stateful(k) {
				t.Fatalf("%v read %s", args, k)
			}
			return ""
		}})
		if code != 2 {
			t.Errorf("%v: exit %d stdout %q stderr %q", args, code, out.String(), errout.String())
		}
	}
}

func TestCLIExitSemanticsAndDaemonErrors(t *testing.T) {
	for _, tc := range []struct {
		args       []string
		status     int
		body       string
		exit       int
		wantOutput bool
	}{
		{[]string{"mail", "archive", "a", "b"}, 200, `[{"id":"a","ok":true,"error":null},{"id":"b","ok":false,"error":"failed"}]`, 0, true},
		{[]string{"account", "status"}, 200, `{"account":"work","email":"work@example.com","ok":false,"logged_in":false}`, 1, true},
		{[]string{"mail", "get", "x"}, 500, `{"error":"private-daemon-canary"}`, 1, false},
		{[]string{"mail", "organize", "--since", "2026-01-01"}, 400, `{"error":"invalid request"}`, 2, false},
		{[]string{"mail", "list", "--count", "-1"}, 400, `{"error":"invalid request"}`, 1, false},
	} {
		h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var req struct{ Account string }
			json.NewDecoder(r.Body).Decode(&req)
			if req.Account != "" {
				t.Error("default account must remain daemon-selected")
			}
			w.WriteHeader(tc.status)
			io.WriteString(w, tc.body)
		}))
		var out, errout bytes.Buffer
		code := cli.Run(context.Background(), tc.args, &out, &errout, cli.Options{LookupEnv: func(k string) string {
			if k == "AMG_HOST" {
				return h.URL
			}
			return ""
		}})
		h.Close()
		if code != tc.exit || (out.Len() > 0) != tc.wantOutput || strings.Contains(errout.String(), "canary") {
			t.Errorf("%v: exit %d output %s stderr %s", tc.args, code, out.String(), errout.String())
		}
	}
}
