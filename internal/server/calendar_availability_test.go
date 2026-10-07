package server_test

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

const availabilityParams = `{"to":["Me@example.test","colleague@example.test"],"start":"2026-10-09T09:00:00+01:00","end":"2026-10-09T10:00:00+01:00"}`

func TestCalendarHTTPAvailabilityReturnsOnlyRequestedSlotMetadata(t *testing.T) {
	handler, request := recordingGraph(t, "", `{"value":[
		{"scheduleId":"me@example.test","availabilityView":"02","scheduleItems":[{"subject":"CANARY subject","location":"CANARY room","status":"busy"}],"workingHours":{"timeZone":{"name":"CANARY zone"}}},
		{"scheduleId":"colleague@example.test","availabilityView":"00","error":{"message":"CANARY error","responseCode":"ErrorAccessDenied"}},
		{"scheduleId":"CANARY@example.test","availabilityView":"00"}]}`)
	s, _, home := calendarServer(t, "User.Read Calendars.ReadBasic", handler)
	status, body := meetingRPC(t, s, "calendar.availability", availabilityParams)
	want := `{"start":"2026-10-09T09:00:00+01:00","end":"2026-10-09T10:00:00+01:00","interval_minutes":30,"schedules":[{"address":"Me@example.test","slots":["free","busy"]},{"address":"colleague@example.test","slots":["unknown","unknown"]}]}`
	if status != 200 || body != want {
		t.Fatalf("response=%d %s", status, body)
	}
	got := request()
	wantBody := map[string]any{"schedules": []any{"Me@example.test", "colleague@example.test"}, "startTime": map[string]any{"dateTime": "2026-10-09T08:00:00", "timeZone": "UTC"}, "endTime": map[string]any{"dateTime": "2026-10-09T09:00:00", "timeZone": "UTC"}, "availabilityViewInterval": float64(30)}
	if got.Method != "POST" || got.Path != "/me/calendar/getSchedule" || !reflect.DeepEqual(got.Body, wantBody) {
		t.Fatalf("provider request=%+v", got)
	}
	if _, err := os.Stat(filepath.Join(home, "audit.jsonl")); !os.IsNotExist(err) {
		t.Fatal("availability wrote audit")
	}
}

func TestCalendarHTTPAvailabilityNeedsAnM365CalendarReadGrant(t *testing.T) {
	for _, tc := range []struct{ provider, scopes, want string }{
		{"m365", "User.Read calendars.readbasic", ""},
		{"m365", "User.Read Calendars.Read", ""},
		{"m365", "User.Read CALENDARS.READWRITE", ""},
		{"m365", "User.Read Mail.ReadWrite role:Calendars.Read Calendars.Read.Shared Calendars.ReadBasic.All", "missing_scopes"},
		{"gmail", "Calendars.Read", "calendar availability is not supported for this account"},
	} {
		t.Run(tc.provider+"/"+tc.scopes, func(t *testing.T) {
			var requests atomic.Int32
			s, creds, home := calendarServer(t, tc.scopes, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				io.WriteString(w, `{"value":[{"scheduleId":"me@example.test","availabilityView":"00"},{"scheduleId":"colleague@example.test","availabilityView":"00"}]}`)
			})
			if tc.provider == "gmail" {
				useGmail(t, home)
			}
			status, body := meetingRPC(t, s, "calendar.availability", availabilityParams)
			if tc.want == "" {
				if status != 200 || !strings.Contains(body, `"slots":["free","free"]`) || requests.Load() != 1 {
					t.Fatalf("response=%d %s requests=%d", status, body, requests.Load())
				}
				return
			}
			if status != 500 || body != `{"error":"`+tc.want+`"}` || requests.Load() != 0 {
				t.Fatalf("response=%d %s requests=%d", status, body, requests.Load())
			}
			if tc.provider == "gmail" && creds.loads.Load() != 0 {
				t.Fatal("Gmail accessed credentials")
			}
		})
	}
}

func TestCalendarHTTPAvailabilityRejectsInvalidInputBeforeAccountAccess(t *testing.T) {
	var requests atomic.Int32
	s, creds, home := calendarServer(t, "Calendars.Read", func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) })
	if err := os.Remove(filepath.Join(home, "config.toml")); err != nil {
		t.Fatal(err)
	}
	with := func(old, replacement string) string { return strings.Replace(availabilityParams, old, replacement, 1) }
	addresses := make([]string, 21)
	for i := range addresses {
		addresses[i] = `"a` + strings.Repeat("a", i) + `@example.test"`
	}
	for _, params := range []string{
		`{}`, `null`, `[]`,
		`{"start":"2026-10-09T09:00:00Z","end":"2026-10-09T10:00:00Z"}`,
		with(`"to":["Me@example.test","colleague@example.test"]`, `"to":[]`),
		with(`"to":["Me@example.test","colleague@example.test"]`, `"to":null`),
		with(`"to":["Me@example.test","colleague@example.test"]`, `"to":"Me@example.test"`),
		with(`"to":["Me@example.test","colleague@example.test"]`, `"to":[`+strings.Join(addresses, ",")+`]`),
		with(`"colleague@example.test"`, `""`),
		with(`"colleague@example.test"`, `" \t"`),
		with(`"colleague@example.test"`, `"`+strings.Repeat("a", 4097)+`"`),
		with(`"colleague@example.test"`, `"ME@EXAMPLE.TEST"`),
		with(`"colleague@example.test"`, `7`),
		with(`"start":"2026-10-09T09:00:00+01:00",`, ``),
		with(`"2026-10-09T09:00:00+01:00"`, `null`),
		with(`"2026-10-09T09:00:00+01:00"`, `"2026-10-09 09:00"`),
		with(`"2026-10-09T10:00:00+01:00"`, `"2026-10-09T09:00:00+01:00"`),
		with(`"2026-10-09T10:00:00+01:00"`, `"2026-10-09T07:59:59Z"`),
		with(`"2026-10-09T10:00:00+01:00"`, `"2026-12-10T08:00:00Z"`),
		with(`"2026-10-09T09:00:00+01:00"`, `"0000-12-31T23:00:00Z"`),
		strings.TrimSuffix(availabilityParams, "}") + `,"interval":15}`,
		strings.TrimSuffix(availabilityParams, "}") + `,"duration":"30m"}`,
		strings.TrimSuffix(availabilityParams, "}") + `,"subject":null}`,
		with(`"start"`, `"start":"2026-10-09T08:00:00Z","start"`),
	} {
		status, body := meetingRPC(t, s, "calendar.availability", params)
		if status != 400 || body != `{"error":"invalid request"}` {
			t.Errorf("%.160s: %d %s", params, status, body)
		}
	}
	if requests.Load() != 0 || creds.loads.Load() != 0 {
		t.Fatal("invalid input accessed provider")
	}
}

func TestCalendarHTTPAvailabilityGraphFailuresStaySafe(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"forbidden": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(403)
			io.WriteString(w, `{"error":{"code":"CANARY","message":"CANARY raw error"}}`)
		},
		"malformed": func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"value":"CANARY"}`) },
	} {
		t.Run(name, func(t *testing.T) {
			s, _, _ := calendarServer(t, "Calendars.Read", handler)
			status, body := meetingRPC(t, s, "calendar.availability", availabilityParams)
			if status != 500 || body != `{"error":"operation failed"}` {
				t.Fatalf("response=%d %s", status, body)
			}
		})
	}
}

func TestCalendarHTTPAvailabilityEchoesTheQueriedAnchors(t *testing.T) {
	s, _, _ := calendarServer(t, "Calendars.Read", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"value":[{"scheduleId":"me@example.test","availabilityView":"00"}]}`)
	})
	status, body := meetingRPC(t, s, "calendar.availability", `{"to":["me@example.test"],"start":"2026-10-09T09:00:00.25+01:00","end":"2026-10-09T09:45:00.25+01:00"}`)
	if want := `{"start":"2026-10-09T09:00:00.25+01:00","end":"2026-10-09T09:45:00.25+01:00","interval_minutes":30,"schedules":[{"address":"me@example.test","slots":["free","free"]}]}`; status != 200 || body != want {
		t.Fatalf("response=%d %s", status, body)
	}
}
