package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/cli"
	"github.com/ivanrvpereira/agentmailguard-go/internal/views"
)

// received builds a timestamp the local zone renders as the given wall-clock time,
// so the expected cell text stays a fixed literal on any machine.
func received(t *testing.T, year int, month time.Month, day, hour, minute int) string {
	t.Helper()
	return time.Date(year, month, day, hour, minute, 0, 0, time.Local).Format(time.RFC3339)
}

// sgr matches a color or decoration sequence, so a test can compare styled output with plain output.
var sgr = regexp.MustCompile("\x1b\\[[0-9;]*m")

// summaryList is one response per verdict class: green and yellow carry a sender name and a subject,
// red and sealed carry the address only, and error carries neither.
func summaryList(t *testing.T) string {
	t.Helper()
	return `[{"id":"green-id","verdict":"green","received_at":"` + received(t, 2026, 3, 7, 9, 30) + `","auth":{},"is_read":false,"flag":"flagged","categories":[],` +
		`"sender":{"name":"Alice Example","address":"alice@test"},"subject":"Quarterly numbers"},` +
		`{"id":"yellow-id","verdict":"yellow","received_at":"` + received(t, 2026, 3, 7, 8, 15) + `","auth":{},"is_read":true,"flag":"none","categories":[],` +
		`"sender":{"name":"Carol Example","address":"carol@test"},"subject":"Invoice","reason":"injection"},` +
		`{"id":"red-id","verdict":"red","received_at":"` + received(t, 2026, 3, 6, 18, 5) + `","auth":{},"is_read":true,"flag":"none","categories":[],` +
		`"sender":{"address":"bob@test"},"reason":"injection"},` +
		`{"id":"sealed-id","verdict":"sealed","received_at":"` + received(t, 2026, 3, 6, 11, 45) + `","auth":{},"is_read":true,"flag":"none","categories":[],` +
		`"sender":{"address":"dave@test"},"reason":"account_security"},` +
		`{"id":"error-id","verdict":"error","received_at":"` + received(t, 2026, 3, 5, 7, 0) + `","auth":{},"is_read":true,"flag":"none","categories":[],"error":"Error"}]`
}

// runPretty runs one command against a fake server that answers with body.
func runPretty(t *testing.T, body string, env map[string]string, args ...string) (int, string, string) {
	t.Helper()
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, body) }))
	defer h.Close()
	var out, errout bytes.Buffer
	code := cli.Run(context.Background(), append([]string{"--server", h.URL}, args...), &out, &errout,
		cli.Options{LookupEnv: func(k string) string { return env[k] }})
	return code, out.String(), errout.String()
}

func TestMailListAndSearchPrettyRenderAPlainTable(t *testing.T) {
	for _, args := range [][]string{{"mail", "list", "--pretty"}, {"mail", "search", "--text", "x", "--pretty"}} {
		code, out, errout := runPretty(t, summaryList(t), nil, args...)
		if code != 0 || errout != "" {
			t.Fatalf("%v: exit=%d stderr=%q", args, code, errout)
		}
		for _, want := range []string{
			"Received", "Verdict", "Flags", "From", "Subject", "ID",
			"2026-03-07 09:30", "2026-03-06 18:05", "2026-03-05 07:00",
			"green-id", "yellow-id", "red-id", "sealed-id", "error-id",
			"green", "yellow", "red", "sealed", "error",
			"Alice Example", "Quarterly numbers", "bob@test", "dave@test",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%v: table is missing %q:\n%s", args, want, out)
			}
		}
		if strings.Contains(out, "\x1b") {
			t.Errorf("%v: escape sequence on a plain writer: %q", args, out)
		}
		if strings.Contains(out, "alice@test") {
			t.Errorf("%v: sender name should replace the address: %s", args, out)
		}
		// A row shows only what its verdict view exposes: no subject for red or sealed, neither cell for error.
		for _, line := range strings.Split(out, "\n") {
			for _, id := range []string{"red-id", "sealed-id", "error-id"} {
				if strings.Contains(line, id) && strings.Contains(line, "Quarterly") {
					t.Errorf("%v: %s row leaked a subject: %q", args, id, line)
				}
			}
			if strings.Contains(line, "error-id") && strings.Contains(line, "@") {
				t.Errorf("%v: error row leaked a sender: %q", args, line)
			}
		}
	}
}

// Each verdict carries its own meaning in color: safe, caution, refused, account security, failure.
func TestMailListPrettyColorsEachVerdictWhenColorIsForced(t *testing.T) {
	plain := func() string {
		code, out, errout := runPretty(t, summaryList(t), nil, "mail", "list", "--pretty")
		if code != 0 || errout != "" {
			t.Fatalf("exit=%d stderr=%q", code, errout)
		}
		return out
	}()
	code, out, errout := runPretty(t, summaryList(t), map[string]string{"CLICOLOR_FORCE": "1"}, "mail", "list", "--pretty")
	if code != 0 || errout != "" {
		t.Fatalf("exit=%d stderr=%q", code, errout)
	}
	// 32 green, 33 yellow, 31 red, 35 magenta: the meaning of a row must not depend on the column order.
	for _, want := range []string{"\x1b[32mgreen", "\x1b[33myellow", "\x1b[31mred", "\x1b[35msealed", "\x1b[31merror"} {
		if !strings.Contains(out, want) {
			t.Errorf("forced color output is missing %q:\n%q", want, out)
		}
	}
	if sgr.ReplaceAllString(out, "") != plain {
		t.Errorf("color changed the table:\n%q\n%q", out, plain)
	}
}

func TestMailListWithoutPrettyStaysIndentedJSON(t *testing.T) {
	body := summaryList(t)
	var want bytes.Buffer
	if e := json.Indent(&want, []byte(body), "", "  "); e != nil {
		t.Fatal(e)
	}
	for _, env := range []map[string]string{nil, {"CLICOLOR_FORCE": "1"}} {
		code, out, errout := runPretty(t, body, env, "mail", "list")
		if code != 0 || errout != "" {
			t.Fatalf("exit=%d stderr=%q", code, errout)
		}
		if out != want.String()+"\n" || !json.Valid([]byte(out)) || strings.Contains(out, "\x1b") {
			t.Errorf("env=%v JSON output changed:\n%s", env, out)
		}
	}
}

func TestMailListPrettyRefusesAResponseThatIsNotASummaryList(t *testing.T) {
	for _, body := range []string{`{"ok":true}`, `[{"id":5}]`, `"text"`} {
		code, out, errout := runPretty(t, body, nil, "mail", "list", "--pretty")
		if code != 1 || out != "" || errout != "error: invalid server response\n" {
			t.Errorf("body=%s exit=%d stdout=%q stderr=%q", body, code, out, errout)
		}
	}
}

func TestMailGetPrettyKeepsTheTextViewAndColorsOnlyWhenForced(t *testing.T) {
	body := `{"id":"green-id","verdict":"green","received_at":"` + received(t, 2026, 3, 7, 9, 30) + `","auth":{},"is_read":false,"flag":"none","categories":[],` +
		`"sender":{"name":"Alice Example","address":"alice@test"},"subject":"Quarterly numbers","body":"private-body-canary"}`
	var v views.MailView
	if e := json.Unmarshal([]byte(body), &v); e != nil {
		t.Fatal(e)
	}
	code, out, errout := runPretty(t, body, nil, "mail", "get", "green-id", "--pretty")
	if code != 0 || errout != "" || out != views.ToText(v)+"\n" {
		t.Fatalf("plain pretty text changed: exit=%d stderr=%q\n%q", code, errout, out)
	}
	code, forced, errout := runPretty(t, body, map[string]string{"CLICOLOR_FORCE": "1"}, "mail", "get", "green-id", "--pretty")
	if code != 0 || errout != "" || !strings.Contains(forced, "\x1b[") {
		t.Fatalf("forced pretty text has no color: exit=%d stderr=%q\n%q", code, errout, forced)
	}
	for _, want := range []string{"Verdict:", "\x1b[32mgreen", "Alice Example", "private-body-canary"} {
		if !strings.Contains(forced, want) {
			t.Errorf("forced pretty text is missing %q:\n%q", want, forced)
		}
	}
	if sgr.ReplaceAllString(forced, "") != out {
		t.Errorf("color changed the text view:\n%q\n%q", forced, out)
	}
}

// Styling the prefix may add color, never a word: removing the sequences gives the plain line back.
func TestErrorPrefixKeepsItsWordsOnEveryWriter(t *testing.T) {
	const want = "error: invalid server URL\n"
	run := func(env map[string]string) string {
		var out, errout bytes.Buffer
		code := cli.Run(context.Background(), []string{"--server", "not-a-url", "mail", "list"}, &out, &errout,
			cli.Options{LookupEnv: func(k string) string { return env[k] }})
		if code != 1 || out.Len() != 0 {
			t.Fatalf("env=%v exit=%d stdout=%q", env, code, out.String())
		}
		return errout.String()
	}
	if plain := run(nil); plain != want {
		t.Errorf("plain writer received %q", plain)
	}
	forced := run(map[string]string{"CLICOLOR_FORCE": "1"})
	if !strings.Contains(forced, "\x1b[") {
		t.Errorf("forced color produced no escape sequence: %q", forced)
	}
	if sgr.ReplaceAllString(forced, "") != want {
		t.Errorf("color changed the message: %q", forced)
	}
	if off := run(map[string]string{"CLICOLOR_FORCE": "1", "NO_COLOR": "1"}); off != want {
		t.Errorf("NO_COLOR did not turn color off: %q", off)
	}
}

// A server response is not trusted at the terminal: control sequences inside a subject or a body must not
// reach a colored terminal, even though the server already strips them, because a remote listener is unprotected.
func TestPrettyOutputDropsControlSequencesFromServerText(t *testing.T) {
	env := map[string]string{"CLICOLOR_FORCE": "1", "TERM": "xterm-256color"}
	hostile := `Hi \u001b[31mfake\u001b[0m \u001b]8;;http://x\u0007link\u001b]8;;\u0007 back\rspace\u0008`
	list := `[{"id":"h-id","verdict":"green","received_at":"` + received(t, 2026, 3, 7, 9, 30) + `","auth":{},"is_read":false,"flag":"none","categories":[],` +
		`"sender":{"name":"` + hostile + `","address":"alice@test"},"subject":"` + hostile + `"}]`
	get := `{"id":"h-id","verdict":"green","received_at":"` + received(t, 2026, 3, 7, 9, 30) + `","auth":{},"is_read":false,"flag":"none","categories":[],` +
		`"sender":{"name":"Alice","address":"alice@test"},"subject":"` + hostile + `","body":"line one\n` + hostile + `\nline three"}`
	for _, tc := range []struct {
		body string
		args []string
	}{{list, []string{"mail", "list", "--pretty"}}, {get, []string{"mail", "get", "h-id", "--pretty"}}} {
		code, out, errout := runPretty(t, tc.body, env, tc.args...)
		if code != 0 || errout != "" {
			t.Fatalf("%v: exit=%d stderr=%q", tc.args, code, errout)
		}
		plain := sgr.ReplaceAllString(out, "")
		for _, bad := range []string{"\x1b", "\r", "\x08", "\x07"} {
			if strings.Contains(plain, bad) {
				t.Errorf("%v: server control byte %q reached the terminal: %q", tc.args, bad, plain)
			}
		}
		for _, want := range []string{"fake", "link", "back", "space"} {
			if !strings.Contains(plain, want) {
				t.Errorf("%v: visible text %q was lost: %q", tc.args, want, plain)
			}
		}
	}
	// Newlines inside a green body still separate lines.
	_, out, _ := runPretty(t, get, env, "mail", "get", "h-id", "--pretty")
	if !strings.Contains(out, "line one\n") || !strings.Contains(out, "\nline three") {
		t.Errorf("body lines were joined: %q", out)
	}
}
