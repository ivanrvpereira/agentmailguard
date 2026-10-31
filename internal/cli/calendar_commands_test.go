package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/cli"
)

// meetingClient runs one command against a recording server. The client clock is in New York,
// so local input crosses the 2026-03-08 and 2026-11-01 DST changes.
func meetingClient(t *testing.T, args ...string) (code int, stdout, stderr string, requests []map[string]any) {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if json.NewDecoder(r.Body).Decode(&req) != nil {
			t.Error("invalid request")
		}
		requests = append(requests, req)
		io.WriteString(w, `{"id":"returned-event-id","ok":true}`)
	}))
	defer h.Close()
	var out, errout bytes.Buffer
	code = cli.Run(context.Background(), append([]string{"--host", h.URL, "--account", "work"}, args...), &out, &errout, cli.Options{
		Now: func() time.Time { return time.Date(2026, 3, 1, 12, 0, 0, 0, loc) },
		LookupEnv: func(k string) string {
			if k != "AMG_HOST" && stateful(k) {
				t.Errorf("read local environment %s", k)
			}
			return ""
		},
	})
	return code, out.String(), errout.String(), requests
}

func TestCalendarWriteCommandsSendExactRequests(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		op     string
		params string
	}{
		{"invite duration across spring DST", []string{"calendar", "invite", "--to", "a@example.test", "--subject", "Plan", "--start", "2026-03-08 01:30", "--duration", "1h"}, "calendar.invite",
			`{"to":["a@example.test"],"subject":"Plan","body":"","online":true,"start":"2026-03-08T01:30:00-05:00","end":"2026-03-08T03:30:00-04:00"}`},
		{"invite end with every option", []string{"calendar", "invite", "--to", "a@example.test", "--to", "b@example.test", "--subject", "", "--body", "Agenda", "--location", "Room 1", "--online", "--start", "2026-10-09 09:00", "--end", "2026-10-09 10:15"}, "calendar.invite",
			`{"to":["a@example.test","b@example.test"],"subject":"","body":"Agenda","location":"Room 1","online":true,"start":"2026-10-09T09:00:00-04:00","end":"2026-10-09T10:15:00-04:00"}`},
		{"invite explicit empty location without online meeting", []string{"calendar", "invite", "--to", "a@example.test", "--subject", "Plan", "--location", "", "--online=false", "--start", "2026-11-01 00:30", "--end", "2026-11-01 03:00"}, "calendar.invite",
			`{"to":["a@example.test"],"subject":"Plan","body":"","location":"","online":false,"start":"2026-11-01T00:30:00-04:00","end":"2026-11-01T03:00:00-05:00"}`},
		{"invite opts out of online meeting", []string{"calendar", "invite", "--to", "a@example.test", "--subject", "Plan", "--no-online", "--start", "2026-10-09 09:00", "--duration", "30m"}, "calendar.invite",
			`{"to":["a@example.test"],"subject":"Plan","body":"","online":false,"start":"2026-10-09T09:00:00-04:00","end":"2026-10-09T09:30:00-04:00"}`},
		{"invite last online option wins", []string{"calendar", "invite", "--to", "a@example.test", "--subject", "Plan", "--online", "--no-online", "--start", "2026-10-09 09:00", "--duration", "30m"}, "calendar.invite",
			`{"to":["a@example.test"],"subject":"Plan","body":"","online":false,"start":"2026-10-09T09:00:00-04:00","end":"2026-10-09T09:30:00-04:00"}`},
		{"invite online after opt-out", []string{"calendar", "invite", "--to", "a@example.test", "--subject", "Plan", "--no-online", "--online", "--start", "2026-10-09 09:00", "--duration", "30m"}, "calendar.invite",
			`{"to":["a@example.test"],"subject":"Plan","body":"","online":true,"start":"2026-10-09T09:00:00-04:00","end":"2026-10-09T09:30:00-04:00"}`},
		{"invite negated opt-out", []string{"calendar", "invite", "--to", "a@example.test", "--subject", "Plan", "--no-online=false", "--start", "2026-10-09 09:00", "--duration", "30m"}, "calendar.invite",
			`{"to":["a@example.test"],"subject":"Plan","body":"","online":true,"start":"2026-10-09T09:00:00-04:00","end":"2026-10-09T09:30:00-04:00"}`},
		{"invite optional attendees repeated", []string{"calendar", "invite", "--to", "a@example.test", "--optional", "b@example.test", "--optional", "c@example.test", "--subject", "Plan", "--start", "2026-10-09 09:00", "--duration", "30m"}, "calendar.invite",
			`{"to":["a@example.test"],"optional":["b@example.test","c@example.test"],"subject":"Plan","body":"","online":true,"start":"2026-10-09T09:00:00-04:00","end":"2026-10-09T09:30:00-04:00"}`},
		{"invite html body", []string{"calendar", "invite", "--to", "a@example.test", "--subject", "Plan", "--body", "<p>Agenda</p>", "--html", "--start", "2026-10-09 09:00", "--duration", "30m"}, "calendar.invite",
			`{"to":["a@example.test"],"subject":"Plan","body":"<p>Agenda</p>","html":true,"online":true,"start":"2026-10-09T09:00:00-04:00","end":"2026-10-09T09:30:00-04:00"}`},
		{"update subject only", []string{"calendar", "update", "e1", "--subject", "New"}, "calendar.update", `{"id":"e1","subject":"New"}`},
		{"update html body", []string{"calendar", "update", "e1", "--body", "<p>New</p>", "--html"}, "calendar.update", `{"id":"e1","body":"<p>New</p>","html":true}`},
		{"update body only", []string{"calendar", "update", "e1", "--body", "New agenda"}, "calendar.update", `{"id":"e1","body":"New agenda"}`},
		{"update explicit empty body", []string{"calendar", "update", "e1", "--subject", "New", "--body", ""}, "calendar.update", `{"id":"e1","subject":"New","body":""}`},
		{"update adds online meeting", []string{"calendar", "update", "e1", "--online"}, "calendar.update", `{"id":"e1","online":true}`},
		{"update explicit empty subject and location", []string{"calendar", "update", "e1", "--subject", "", "--location", ""}, "calendar.update", `{"id":"e1","subject":"","location":""}`},
		{"update start only keeps server duration", []string{"calendar", "update", "e1", "--start", "2026-03-08 03:00"}, "calendar.update", `{"id":"e1","start":"2026-03-08T03:00:00-04:00"}`},
		{"update duration only", []string{"calendar", "update", "e1", "--duration", "1h30m"}, "calendar.update", `{"id":"e1","duration":"1h30m0s"}`},
		{"update end only", []string{"calendar", "update", "e1", "--end", "2026-11-01 03:00"}, "calendar.update", `{"id":"e1","end":"2026-11-01T03:00:00-05:00"}`},
		{"update start and duration", []string{"calendar", "update", "e1", "--start", "2026-10-09 09:00", "--duration", "45m"}, "calendar.update", `{"id":"e1","start":"2026-10-09T09:00:00-04:00","duration":"45m0s"}`},
		{"update start and end", []string{"calendar", "update", "e1", "--start", "2026-12-01 09:00", "--end", "2026-12-01 10:00"}, "calendar.update", `{"id":"e1","start":"2026-12-01T09:00:00-05:00","end":"2026-12-01T10:00:00-05:00"}`},
		{"cancel without comment", []string{"calendar", "cancel", "e1"}, "calendar.cancel", `{"id":"e1"}`},
		{"cancel with comment", []string{"calendar", "cancel", "e1", "--comment", "Moved online"}, "calendar.cancel", `{"id":"e1","comment":"Moved online"}`},
		{"cancel explicit empty comment", []string{"calendar", "cancel", "e1", "--comment", ""}, "calendar.cancel", `{"id":"e1","comment":""}`},
		{"respond accept", []string{"calendar", "respond", "e1", "--response", "accept"}, "calendar.respond", `{"id":"e1","response":"accept"}`},
		{"respond tentative with comment", []string{"calendar", "respond", "e1", "--response", "tentative", "--comment", "Maybe"}, "calendar.respond", `{"id":"e1","response":"tentative","comment":"Maybe"}`},
		{"respond decline explicit empty comment", []string{"calendar", "respond", "e1", "--comment", "", "--response", "decline"}, "calendar.respond", `{"id":"e1","response":"decline","comment":""}`},
		{"respond without notification", []string{"calendar", "respond", "e1", "--response", "decline", "--no-notify"}, "calendar.respond", `{"id":"e1","response":"decline","no_notify":true}`},
		{"respond without notification and an explicit empty comment", []string{"calendar", "respond", "e1", "--response", "accept", "--no-notify", "--comment", ""}, "calendar.respond", `{"id":"e1","response":"accept","comment":"","no_notify":true}`},
		{"update attendees repeated", []string{"calendar", "update", "e1", "--add-attendee", "a@example.test", "--remove-attendee", "c@example.test", "--add-attendee", "b@example.test"}, "calendar.update", `{"id":"e1","add":["a@example.test","b@example.test"],"remove":["c@example.test"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errout, requests := meetingClient(t, tc.args...)
			if code != 0 || errout != "" || len(requests) != 1 || !strings.Contains(out, "returned-event-id") {
				t.Fatalf("exit=%d stdout=%q stderr=%q requests=%d", code, out, errout, len(requests))
			}
			var want map[string]any
			if err := json.Unmarshal([]byte(tc.params), &want); err != nil {
				t.Fatal(err)
			}
			got := requests[0]
			if got["operation"] != tc.op || got["account"] != "work" || !reflect.DeepEqual(got["params"], want) {
				t.Fatalf("request=%v", got)
			}
		})
	}
}

func TestCalendarWriteUsageErrorsNeedNoServerOrState(t *testing.T) {
	invite := []string{"calendar", "invite", "--to", "a@example.test", "--subject", "Plan"}
	for _, args := range [][]string{
		append(invite, "--start", "2026-10-09 09:00"),
		append(invite, "--start", "2026-10-09 09:00", "--end", "2026-10-09 10:00", "--duration", "1h"),
		append(invite, "--start", "2026-10-09 09:00", "--duration", "0s"),
		append(invite, "--start", "2026-10-09 09:00", "--duration", "-30m"),
		append(invite, "--start", "2026-10-09 09:00", "--duration", "1d"),
		append(invite, "--start", "2026-10-09 09:00", "--duration", "9999999999999h"),
		append(invite, "--start", "2026-10-09 09:00", "--end", "2026-10-09 09:00"),
		append(invite, "--start", "2026-10-09 09:00", "--end", "2026-10-09 08:59"),
		append(invite, "--start", "2026-03-08 02:30", "--duration", "1h"),
		append(invite, "--start", "2026-10-09 09:00", "--end", "2026-03-08 02:00"),
		append(invite, "--start", "2026-02-30 09:00", "--duration", "1h"),
		append(invite, "--start", "2026-10-09 24:00", "--duration", "1h"),
		append(invite, "--start", "2026-10-09 9:00", "--duration", "1h"),
		append(invite, "--start", "2026-10-09T09:00", "--duration", "1h"),
		append(invite, "--start", "2026-10-09 09:00:00", "--duration", "1h"),
		append(invite, "--start", "2026-10-09 09:00-04:00", "--duration", "1h"),
		append(invite, "--start", "0000-01-01 09:00", "--duration", "1h"),
		append(invite, "--start", "9999-12-31 23:30", "--duration", "1h"),
		append(invite, "--start", "2026-10-09 09:00", "--duration", "1h", "extra"),
		{"calendar", "invite", "--subject", "Plan", "--start", "2026-10-09 09:00", "--duration", "1h"},
		{"calendar", "invite", "--to", "a@example.test", "--start", "2026-10-09 09:00", "--duration", "1h"},
		{"calendar", "invite", "--to", "a@example.test", "--subject", "Plan", "--duration", "1h"},
		{"calendar", "update", "--subject", "New"},
		{"calendar", "update", "e1", "e2", "--subject", "New"},
		{"calendar", "update", "e1"},
		{"calendar", "update", "e1", "--online=false"},
		{"calendar", "update", "e1", "--subject", "New", "--online=false"},
		{"calendar", "update", "e1", "--no-online"},
		{"calendar", "update", "e1", "--subject", "New", "--html"},
		{"calendar", "update", "e1", "--teams"},
		append(invite, "--start", "2026-10-09 09:00", "--duration", "1h", "--no-teams"),
		{"calendar", "update", "e1", "--to", "a@example.test"},
		{"calendar", "update", "e1", "--end", "2026-10-09 10:00", "--duration", "1h"},
		{"calendar", "update", "e1", "--duration", "0s"},
		{"calendar", "update", "e1", "--duration", "-1h"},
		{"calendar", "update", "e1", "--duration", "soon"},
		{"calendar", "update", "e1", "--start", "2026-03-08 02:00"},
		{"calendar", "update", "e1", "--end", "2026-10-09"},
		{"calendar", "update", "e1", "--start", "2026-10-09 10:00", "--end", "2026-10-09 10:00"},
		{"calendar", "update", "e1", "--start", "2026-10-09 10:00", "--end", "2026-10-09 09:00"},
		{"calendar", "update", "e1", "--start", "9999-12-31 18:00", "--duration", "1h"},
		{"calendar", "cancel"},
		{"calendar", "cancel", "e1", "e2"},
		{"calendar", "cancel", "e1", "--body", "x"},
		{"calendar", "respond", "e1"},
		{"calendar", "respond", "--response", "accept"},
		{"calendar", "respond", "e1", "e2", "--response", "accept"},
		{"calendar", "respond", "e1", "--response", "Accept"},
		{"calendar", "respond", "e1", "--response", "tentativelyAccept"},
		{"calendar", "respond", "e1", "--response", ""},
		{"calendar", "respond", "e1", "--response", "accept", "--start", "2026-10-09 09:00"},
		{"calendar", "respond", "e1", "--response", "accept", "--send-response=false"},
		{"calendar", "respond", "e1", "--response", "accept", "--no-notify", "--comment", "See you"},
	} {
		code, out, errout, requests := meetingClient(t, args...)
		if code != 2 || out != "" || errout != "error: invalid command usage; use --help\n" || len(requests) != 0 {
			t.Errorf("%v: exit=%d stdout=%q stderr=%q requests=%d", args[1:], code, out, errout, len(requests))
		}
	}
}

func TestHelpStatesThatCalendarWritesSendNotifications(t *testing.T) {
	code, out, errout := runOffline(t, "--help")
	if code != 0 || errout != "" || strings.Contains(out, "No mail send") || !strings.Contains(out, "No permanent delete, except draft delete on Gmail") || !strings.Contains(out, "mail send, reply, forward, and draft send send email at once") || !strings.Contains(out, "send meeting notifications at once") {
		t.Fatalf("root help: exit=%d stdout=%q stderr=%q", code, out, errout)
	}
	// Meeting writes work on both providers.
	if strings.Contains(out, "M365 calendar invite") {
		t.Fatalf("root help limits meeting writes to M365: %q", out)
	}
	code, out, errout = runOffline(t, "calendar", "--help")
	for _, want := range []string{"invite", "update", "cancel", "respond", "meeting notifications"} {
		if code != 0 || errout != "" || !strings.Contains(out, want) || strings.Contains(out, "M365 meeting") {
			t.Fatalf("calendar help is missing %q or limits meetings to M365: exit=%d stdout=%q stderr=%q", want, code, out, errout)
		}
	}
	code, out, errout = runOffline(t, "calendar", "cancel", "--help")
	if code != 0 || errout != "" || !strings.Contains(out, "refused on Gmail") {
		t.Fatalf("cancel help must state the Gmail comment refusal: exit=%d stdout=%q stderr=%q", code, out, errout)
	}
	code, out, errout = runOffline(t, "calendar", "invite", "--help")
	if code != 0 || errout != "" || !strings.Contains(out, "--no-online") || !strings.Contains(out, "(default true)") || !strings.Contains(out, "Teams on Microsoft 365, Google Meet on Gmail") {
		t.Fatalf("invite help must show the online meeting default and its opt-out: exit=%d stdout=%q stderr=%q", code, out, errout)
	}
}

func TestCalendarAvailabilityCommandSendsLocalWindow(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		params string
	}{
		{"two participants", []string{"calendar", "availability", "--to", "me@example.test", "--to", "colleague@example.test", "--start", "2026-10-09 09:00", "--end", "2026-10-09 18:00"},
			`{"to":["me@example.test","colleague@example.test"],"start":"2026-10-09T09:00:00-04:00","end":"2026-10-09T18:00:00-04:00"}`},
		{"across fall DST", []string{"calendar", "availability", "--end", "2026-11-01 03:00", "--to", "a@example.test", "--start", "2026-10-31 23:00"},
			`{"to":["a@example.test"],"start":"2026-10-31T23:00:00-04:00","end":"2026-11-01T03:00:00-05:00"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errout, requests := meetingClient(t, tc.args...)
			if code != 0 || errout != "" || len(requests) != 1 || !strings.Contains(out, "returned-event-id") {
				t.Fatalf("exit=%d stdout=%q stderr=%q requests=%d", code, out, errout, len(requests))
			}
			var want map[string]any
			if err := json.Unmarshal([]byte(tc.params), &want); err != nil {
				t.Fatal(err)
			}
			if got := requests[0]; got["operation"] != "calendar.availability" || got["account"] != "work" || !reflect.DeepEqual(got["params"], want) {
				t.Fatalf("request=%v", got)
			}
		})
	}
}

func TestCalendarAvailabilityUsageErrorsNeedNoServer(t *testing.T) {
	window := []string{"--start", "2026-10-09 09:00", "--end", "2026-10-09 18:00"}
	query := func(extra ...string) []string {
		return append(append([]string{"calendar", "availability", "--to", "a@example.test"}, window...), extra...)
	}
	many := []string{"calendar", "availability", "--start", "2026-10-09 09:00", "--end", "2026-10-09 18:00"}
	for i := range 21 {
		many = append(many, "--to", strings.Repeat("a", i+1)+"@example.test")
	}
	for _, args := range [][]string{
		append([]string{"calendar", "availability"}, window...),
		{"calendar", "availability", "--to", "a@example.test", "--start", "2026-10-09 09:00"},
		{"calendar", "availability", "--to", "a@example.test", "--end", "2026-10-09 18:00"},
		many,
		query("--to", ""),
		query("--to", " "),
		query("--to", "A@Example.test"),
		query("extra"),
		query("--duration", "30m"),
		query("--interval", "15"),
		query("--subject", "Plan"),
		{"calendar", "availability", "--to", "a@example.test", "--start", "2026-10-09 09:00", "--end", "2026-10-09 09:00"},
		{"calendar", "availability", "--to", "a@example.test", "--start", "2026-10-09 09:00", "--end", "2026-10-09 08:30"},
		{"calendar", "availability", "--to", "a@example.test", "--start", "2026-10-09 09:00", "--end", "2026-12-10 09:00"},
		{"calendar", "availability", "--to", "a@example.test", "--start", "2026-03-08 02:30", "--end", "2026-03-08 04:00"},
		{"calendar", "availability", "--to", "a@example.test", "--start", "2026-10-09T09:00", "--end", "2026-10-09 18:00"},
		{"calendar", "availability", "--to", "a@example.test", "--start", "2026-10-09 09:00", "--end", "2026-10-09"},
		{"calendar", "availability", "--to", "a@example.test", "--start", "0000-12-31 23:00", "--end", "0001-01-01 01:00"},
	} {
		code, out, errout, requests := meetingClient(t, args...)
		if code != 2 || out != "" || errout != "error: invalid command usage; use --help\n" || len(requests) != 0 {
			t.Errorf("%v: exit=%d stdout=%q stderr=%q requests=%d", args[1:], code, out, errout, len(requests))
		}
	}
}

func TestHelpSeparatesReadOnlyAvailabilityFromNotifications(t *testing.T) {
	code, out, errout := runOffline(t, "calendar", "--help")
	if code != 0 || errout != "" || !strings.Contains(out, "availability") || !strings.Contains(out, "read-only, sends nothing") {
		t.Fatalf("calendar help: exit=%d stdout=%q stderr=%q", code, out, errout)
	}
	code, out, errout = runOffline(t, "calendar", "availability", "--help")
	for _, want := range []string{"--to", "--start", "--end", "30-minute", "read-only"} {
		if code != 0 || errout != "" || !strings.Contains(out, want) {
			t.Fatalf("availability help is missing %q: exit=%d stdout=%q stderr=%q", want, code, out, errout)
		}
	}
}
