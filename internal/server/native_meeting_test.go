package server_test

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
)

// meetingGraph is a stateful fake of the Graph event endpoints. Created events keep every posted field.
type meetingGraph struct {
	mu        sync.Mutex
	events    map[string]map[string]any
	writes    []externalWrite
	schedules []map[string]any
	created   int
}

func (g *meetingGraph) handle(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id, action, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/me/events/"), "/")
	// getSchedule is a read, so it is recorded apart from the writes below.
	if r.Method == "POST" && r.URL.Path == "/me/calendar/getSchedule" {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		g.schedules = append(g.schedules, body)
		response(w, map[string]any{"value": []any{
			map[string]any{"scheduleId": "work@example.test", "availabilityView": "2002", "scheduleItems": []any{map[string]any{"subject": "private-busy-subject", "location": "private-busy-room"}}},
			map[string]any{"scheduleId": "FIRST@example.test", "availabilityView": "1003"},
			map[string]any{"scheduleId": "second@example.test", "availabilityView": "0202"},
			map[string]any{"scheduleId": "hidden@example.test", "error": map[string]any{"message": "private-error-text", "responseCode": "ErrorAccessDenied"}},
			map[string]any{"scheduleId": "private-stranger@example.test", "availabilityView": "0000"},
		}})
		return
	}
	if r.Method == "GET" {
		if r.URL.Path == "/me/outlook/masterCategories" {
			response(w, map[string]any{"value": []any{}})
			return
		}
		if e, ok := g.events[id]; ok && strings.HasPrefix(r.URL.Path, "/me/events/") && action == "" {
			response(w, e)
			return
		}
		w.WriteHeader(404)
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	g.writes = append(g.writes, externalWrite{r.URL.Path, r.Method, body})
	switch {
	case r.Method == "POST" && r.URL.Path == "/me/events":
		g.created++
		id = "created-" + strconv.Itoa(g.created)
		e := map[string]any{"id": id, "isOrganizer": true, "organizer": map[string]any{"emailAddress": map[string]any{"address": "work@example.test"}}}
		maps.Copy(e, body)
		if body["isOnlineMeeting"] == true {
			e["onlineMeeting"] = map[string]any{"joinUrl": "https://teams.example.test/join/" + id}
		}
		g.events[id] = e
		response(w, map[string]any{"id": id})
	case r.Method == "PATCH" && g.events[id] != nil && action == "":
		maps.Copy(g.events[id], body)
		if body["isOnlineMeeting"] == true {
			g.events[id]["onlineMeeting"] = map[string]any{"joinUrl": "https://teams.example.test/join/" + id}
		}
		response(w, g.events[id])
	case r.Method == "POST" && action == "cancel" && g.events[id] != nil:
		g.events[id]["isCancelled"] = true
		w.WriteHeader(202)
	case r.Method == "POST" && g.events[id] != nil:
		w.WriteHeader(202)
	default:
		w.WriteHeader(404)
	}
}

func syntheticEvent(id string, organizer bool, extra map[string]any) map[string]any {
	e := map[string]any{"id": id, "subject": "private-subject-" + id, "isOrganizer": organizer, "organizer": map[string]any{"emailAddress": map[string]any{"address": "organizer@example.test"}},
		"start": map[string]string{"dateTime": "2026-10-12T09:00:00", "timeZone": "UTC"}, "end": map[string]string{"dateTime": "2026-10-12T10:00:00", "timeZone": "UTC"},
		"body": map[string]string{"contentType": "text", "content": "private-body-" + id}, "attendees": []any{map[string]any{"emailAddress": map[string]any{"address": "work@example.test"}, "type": "required"}}}
	maps.Copy(e, extra)
	return e
}

// The compiled client only knows the daemon URL and its own time zone; it never sees configuration or credentials.
func TestNativeBinaryMeetingWorkflow(t *testing.T) {
	g := &meetingGraph{events: map[string]map[string]any{"recurring-1": syntheticEvent("recurring-1", true, map[string]any{"type": "occurrence"})}}
	for _, r := range []string{"accept", "tentative", "decline"} {
		g.events["attendee-"+r] = syntheticEvent("attendee-"+r, false, nil)
	}
	google := &meetingGoogle{events: map[string]map[string]any{
		"g-invited":   googleEvent("g-invited", false, nil),
		"g-recurring": googleEvent("g-recurring", true, map[string]any{"recurrence": []string{"RRULE:FREQ=WEEKLY"}}),
	}}
	api := newNativeAPI()
	// The M365 write grant is the login of work only, so other proves the missing-scope refusal.
	logins := map[string]credentials.OAuth{
		"m365/" + workEmail:   m365Login(m365Grant + " Calendars.ReadWrite"),
		"m365/" + otherEmail:  m365Login(m365Grant),
		"gmail/" + gmailEmail: gmailLogin(gmailWriteGrant),
	}
	// Lisbon leaves summer time on 2026-10-25, between the invite and the update below.
	d, run := startNative(t, buildNative(t), nativeFakes{
		logins: logins, config: calendarAllow, jev: api, graph: g.handle, google: google.handle,
		now: time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC), clientEnv: []string{"TZ=Europe/Lisbon"},
	})
	// write runs one successful command and returns its stdout and the Graph writes it caused.
	write := func(args ...string) (string, []externalWrite) {
		t.Helper()
		g.mu.Lock()
		before := len(g.writes)
		g.mu.Unlock()
		out, errout, code := run(args...)
		if code != 0 || errout != "" {
			t.Fatalf("command %v code %d stderr %s", args, code, errout)
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		return out, append([]externalWrite{}, g.writes[before:]...)
	}
	exact := func(args []string, writes []externalWrite, method, path string, body map[string]any) {
		t.Helper()
		if len(writes) != 1 || writes[0].method != method || writes[0].path != path || !reflect.DeepEqual(writes[0].body, body) {
			t.Fatalf("command %v: Graph writes %#v, want one %s %s %#v", args, writes, method, path, body)
		}
	}
	view := func(id string) map[string]any {
		t.Helper()
		out, _ := write("--account", "work", "calendar", "get", id)
		var v map[string]any
		if json.Unmarshal([]byte(out), &v) != nil || v["verdict"] != "green" {
			t.Fatalf("calendar get %s: %s", id, out)
		}
		return v
	}
	utc := func(stamp string) map[string]any { return map[string]any{"dateTime": stamp, "timeZone": "UTC"} }
	attendee := func(address, kind string) map[string]any {
		return map[string]any{"emailAddress": map[string]any{"address": address}, "type": kind}
	}

	// Availability is a read. The only slot that every participant shows as free is 09:00 Lisbon time.
	// The hidden participant has no data and stays unknown, never free.
	out, writes := write("--account", "work", "calendar", "availability", "--to", "work@example.test", "--to", "first@example.test", "--to", "second@example.test", "--to", "hidden@example.test", "--start", "2026-10-09 08:00", "--end", "2026-10-09 10:00")
	var free struct {
		Start, End string
		Interval   int `json:"interval_minutes"`
		Schedules  []struct {
			Address string
			Slots   []string
		}
	}
	if len(writes) != 0 || strings.Contains(out, "private-") || json.Unmarshal([]byte(out), &free) != nil || free.Start != "2026-10-09T08:00:00+01:00" || free.End != "2026-10-09T10:00:00+01:00" || free.Interval != 30 || len(free.Schedules) != 4 {
		t.Fatalf("availability output %q, Graph writes %d", out, len(writes))
	}
	slots := map[string][]string{}
	for _, schedule := range free.Schedules {
		slots[schedule.Address] = schedule.Slots
	}
	if !reflect.DeepEqual(slots, map[string][]string{"work@example.test": {"busy", "free", "free", "busy"}, "first@example.test": {"tentative", "free", "free", "out_of_office"}, "second@example.test": {"free", "busy", "free", "busy"}, "hidden@example.test": {"unknown", "unknown", "unknown", "unknown"}}) {
		t.Fatalf("availability slots %v", slots)
	}
	if !reflect.DeepEqual(g.schedules, []map[string]any{{"schedules": []any{"work@example.test", "first@example.test", "second@example.test", "hidden@example.test"}, "startTime": utc("2026-10-09T07:00:00"), "endTime": utc("2026-10-09T09:00:00"), "availabilityViewInterval": float64(30)}}) || len(api.jevCalls) != 0 {
		t.Fatalf("getSchedule requests %v, Jev calls %v", g.schedules, api.jevCalls)
	}

	args := []string{"--account", "work", "calendar", "invite", "--to", "first@example.test", "--to", "second@example.test", "--subject", "private-meeting-subject", "--body", "private-meeting-body\n<b>agenda</b> & notes", "--location", "private-room", "--start", "2026-10-09 09:00", "--duration", "30m"}
	out, writes = write(args...)
	exact(args, writes, "POST", "/me/events", map[string]any{"subject": "private-meeting-subject", "body": map[string]any{"contentType": "html", "content": "private-meeting-body<br>&lt;b&gt;agenda&lt;/b&gt; &amp; notes"}, "location": map[string]any{"displayName": "private-room"},
		"start": utc("2026-10-09T08:00:00"), "end": utc("2026-10-09T08:30:00"), "attendees": []any{attendee("first@example.test", "required"), attendee("second@example.test", "required")}, "isOnlineMeeting": true, "onlineMeetingProvider": "teamsForBusiness"})
	if strings.TrimSpace(out) != "{\n  \"id\": \"created-1\"\n}" {
		t.Fatalf("invite output %q", out)
	}
	args = []string{"--account", "work", "calendar", "invite", "--to", "first@example.test", "--subject", "private-plain-subject", "--body", "private-plain-body", "--start", "2026-10-09 11:00", "--end", "2026-10-09 11:15", "--no-online"}
	_, writes = write(args...)
	exact(args, writes, "POST", "/me/events", map[string]any{"subject": "private-plain-subject", "body": map[string]any{"contentType": "html", "content": "private-plain-body"}, "location": map[string]any{"displayName": ""},
		"start": utc("2026-10-09T10:00:00"), "end": utc("2026-10-09T10:15:00"), "attendees": []any{attendee("first@example.test", "required")}, "isOnlineMeeting": false})
	if v := view("created-2"); v["join_url"] != nil {
		t.Fatalf("opted-out invite has a meeting link: %v", v)
	}

	// Adding Teams later keeps the same event and sends only the online meeting fields, so Graph keeps the body, times, and attendees.
	args = []string{"--account", "work", "calendar", "update", "created-2", "--online"}
	_, writes = write(args...)
	exact(args, writes, "PATCH", "/me/events/created-2", map[string]any{"isOnlineMeeting": true, "onlineMeetingProvider": "teamsForBusiness"})
	v := view("created-2")
	if v["id"] != "created-2" || v["subject"] != "private-plain-subject" || v["start"] != "2026-10-09T10:00:00+00:00" || v["end"] != "2026-10-09T10:15:00+00:00" || v["join_url"] != "https://teams.example.test/join/created-2" || !strings.Contains(v["body"].(string), "private-plain-body") || len(v["attendees"].([]any)) != 1 || v["attendees"].([]any)[0].(map[string]any)["address"] != "first@example.test" {
		t.Fatalf("event after adding Teams %v", v)
	}

	v = view("created-1")
	if v["subject"] != "private-meeting-subject" || v["start"] != "2026-10-09T08:00:00+00:00" || v["end"] != "2026-10-09T08:30:00+00:00" || v["join_url"] != "https://teams.example.test/join/created-1" || !strings.Contains(v["body"].(string), "private-meeting-body") {
		t.Fatalf("created event view %v", v)
	}

	// The organizer marks one attendee optional in Outlook; the update must keep that type.
	g.mu.Lock()
	g.events["created-1"]["attendees"] = []any{attendee("first@example.test", "required"), attendee("second@example.test", "optional")}
	g.mu.Unlock()
	args = []string{"--account", "work", "calendar", "update", "created-1", "--subject", "private-new-subject", "--start", "2026-10-30 10:00", "--add-attendee", "third@example.test", "--remove-attendee", "first@example.test"}
	out, writes = write(args...)
	exact(args, writes, "PATCH", "/me/events/created-1", map[string]any{"subject": "private-new-subject", "start": utc("2026-10-30T10:00:00"), "end": utc("2026-10-30T10:30:00"),
		"attendees": []any{attendee("second@example.test", "optional"), attendee("third@example.test", "required")}})
	if strings.TrimSpace(out) != "{\n  \"id\": \"created-1\",\n  \"ok\": true\n}" {
		t.Fatalf("update output %q", out)
	}
	v = view("created-1")
	var addresses []string
	for _, a := range v["attendees"].([]any) {
		addresses = append(addresses, a.(map[string]any)["address"].(string))
	}
	if v["subject"] != "private-new-subject" || v["start"] != "2026-10-30T10:00:00+00:00" || v["end"] != "2026-10-30T10:30:00+00:00" || v["join_url"] != "https://teams.example.test/join/created-1" || !strings.Contains(v["body"].(string), "private-meeting-body") || !reflect.DeepEqual(addresses, []string{"second@example.test", "third@example.test"}) {
		t.Fatalf("updated event view %v", v)
	}

	// Refusals reach no Graph or Gmail write and keep the fixed client messages.
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--account", "gmail", "calendar", "availability", "--to", "first@example.test", "--start", "2026-10-09 09:00", "--end", "2026-10-09 18:00"}, "calendar availability is not supported for this account"},
		{[]string{"--account", "other", "calendar", "cancel", "created-1"}, "missing_scopes"},
		{[]string{"--account", "other", "calendar", "respond", "attendee-accept", "--response", "accept"}, "missing_scopes"},
		{[]string{"--account", "work", "calendar", "update", "recurring-1", "--subject", "private-recurring-subject"}, "calendar write refused: recurring events are not supported"},
		{[]string{"--account", "work", "calendar", "cancel", "recurring-1"}, "calendar write refused: recurring events are not supported"},
		{[]string{"--account", "work", "calendar", "update", "attendee-accept", "--subject", "private-attendee-subject"}, "calendar write refused: only the organizer can update or cancel"},
		{[]string{"--account", "work", "calendar", "cancel", "attendee-accept"}, "calendar write refused: only the organizer can update or cancel"},
		{[]string{"--account", "work", "calendar", "respond", "created-1", "--response", "decline"}, "calendar response refused: organizer cannot respond"},
	} {
		g.mu.Lock()
		before := len(g.writes)
		g.mu.Unlock()
		out, errout, code := run(tc.args...)
		g.mu.Lock()
		after := len(g.writes)
		g.mu.Unlock()
		if code != 1 || out != "" || errout != "error: "+tc.want+"\n" || after != before {
			t.Fatalf("command %v: exit=%d stdout=%q stderr=%q Graph writes=%d", tc.args, code, out, errout, after-before)
		}
	}
	if len(google.recorded()) != 0 {
		t.Fatal("refusal reached Google")
	}

	args = []string{"--account", "work", "calendar", "cancel", "created-1", "--comment", "private-cancel-comment"}
	_, writes = write(args...)
	exact(args, writes, "POST", "/me/events/created-1/cancel", map[string]any{"comment": "private-cancel-comment"})
	out, errout, code := run("--account", "work", "calendar", "cancel", "created-1")
	if code != 1 || out != "" || errout != "error: calendar write refused: event is cancelled\n" {
		t.Fatalf("second cancel: exit=%d stdout=%q stderr=%q", code, out, errout)
	}
	for response, endpoint := range map[string]string{"accept": "accept", "tentative": "tentativelyAccept", "decline": "decline"} {
		args = []string{"--account", "work", "calendar", "respond", "attendee-" + response, "--response", response, "--comment", "private-rsvp-comment"}
		_, writes = write(args...)
		exact(args, writes, "POST", "/me/events/attendee-"+response+"/"+endpoint, map[string]any{"comment": "private-rsvp-comment", "sendResponse": true})
	}
	g.mu.Lock()
	total := len(g.writes)
	g.mu.Unlock()
	if total != 8 {
		t.Fatalf("Graph writes=%d, want 2 invites, 2 updates, 1 cancel, 3 responses", total)
	}

	verifyNativeGmailMeetings(t, google, run)

	log, err := os.ReadFile(filepath.Join(d.home, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// M365 and Gmail together.
	for operation, count := range map[string]int{"calendar_invite": 4, "calendar_update": 5, "calendar_cancel": 2, "calendar_respond": 4} {
		if n := bytes.Count(log, []byte(`"operation":"`+operation+`"`)); n != count {
			t.Errorf("audit has %d %s records, want %d", n, operation, count)
		}
	}
	for _, canary := range []string{"private-", "example.test", "screening-key", "availability"} {
		if bytes.Contains(log, []byte(canary)) {
			t.Fatalf("audit contains %s", canary)
		}
	}
}

// verifyNativeGmailMeetings runs the meeting commands on the Gmail account through the compiled client.
// Every write notifies all guests, and the organizer's writes keep guests from inviting others.
func verifyNativeGmailMeetings(t *testing.T, google *meetingGoogle, run func(args ...string) (string, string, int)) {
	t.Helper()
	write := func(args ...string) (string, []googleWrite) {
		t.Helper()
		before := len(google.recorded())
		out, errout, code := run(args...)
		if code != 0 || errout != "" {
			t.Fatalf("command %v code %d stderr %s", args, code, errout)
		}
		return out, google.recorded()[before:]
	}
	notify := url.Values{"sendUpdates": {"all"}}
	withMeet := url.Values{"sendUpdates": {"all"}, "conferenceDataVersion": {"1"}}
	// exact checks the one write; a Meet create request has a random ID, which must be present and is then ignored.
	exact := func(args []string, writes []googleWrite, method, path string, query url.Values, body map[string]any) {
		t.Helper()
		if len(writes) == 1 && writes[0].Body != nil {
			if conference, ok := writes[0].Body["conferenceData"].(map[string]any); ok {
				request, _ := conference["createRequest"].(map[string]any)
				if id, _ := request["requestId"].(string); id == "" {
					t.Fatalf("command %v: Meet request without an ID: %v", args, conference)
				}
				delete(request, "requestId")
			}
		}
		if len(writes) != 1 || writes[0].Method != method || writes[0].Path != path || !reflect.DeepEqual(writes[0].Query, query) || !reflect.DeepEqual(writes[0].Body, body) {
			t.Fatalf("command %v: Google writes %#v, want one %s %s %v %#v", args, writes, method, path, query, body)
		}
	}
	meet := map[string]any{"createRequest": map[string]any{"conferenceSolutionKey": map[string]any{"type": "hangoutsMeet"}}}
	utc := func(stamp string) map[string]any { return map[string]any{"dateTime": stamp} }
	view := func(id string) map[string]any {
		t.Helper()
		out, _ := write("--account", "gmail", "calendar", "get", id)
		var v map[string]any
		if json.Unmarshal([]byte(out), &v) != nil || v["verdict"] != "green" {
			t.Fatalf("calendar get %s: %s", id, out)
		}
		return v
	}

	// An invite has a Google Meet link by default.
	args := []string{"--account", "gmail", "calendar", "invite", "--to", "first@example.test", "--to", "second@example.test", "--subject", "private-gmail-subject", "--body", "private-gmail-body", "--location", "private-gmail-room", "--start", "2026-10-09 09:00", "--duration", "30m"}
	out, writes := write(args...)
	exact(args, writes, "POST", "/calendar/events", withMeet, map[string]any{"summary": "private-gmail-subject", "description": "private-gmail-body", "location": "private-gmail-room", "start": utc("2026-10-09T08:00:00Z"), "end": utc("2026-10-09T08:30:00Z"),
		"attendees": []any{map[string]any{"email": "first@example.test"}, map[string]any{"email": "second@example.test"}}, "guestsCanInviteOthers": false, "conferenceData": meet})
	if strings.TrimSpace(out) != "{\n  \"id\": \"g-created-1\"\n}" {
		t.Fatalf("invite output %q", out)
	}
	if v := view("g-created-1"); v["join_url"] != "https://meet.example.test/g-created-1" {
		t.Fatalf("invite has no Meet link: %v", v)
	}
	args = []string{"--account", "gmail", "calendar", "invite", "--to", "first@example.test", "--subject", "private-gmail-plain", "--start", "2026-10-09 11:00", "--end", "2026-10-09 11:15", "--no-online"}
	_, writes = write(args...)
	exact(args, writes, "POST", "/calendar/events", notify, map[string]any{"summary": "private-gmail-plain", "description": "", "location": "", "start": utc("2026-10-09T10:00:00Z"), "end": utc("2026-10-09T10:15:00Z"),
		"attendees": []any{map[string]any{"email": "first@example.test"}}, "guestsCanInviteOthers": false})
	if v := view("g-created-2"); v["join_url"] != nil {
		t.Fatalf("opted-out invite has a meeting link: %v", v)
	}

	// --online adds a Meet link to the same event once; a second --online leaves the conference as it is.
	args = []string{"--account", "gmail", "calendar", "update", "g-created-2", "--online"}
	_, writes = write(args...)
	exact(args, writes, "PATCH", "/calendar/events/g-created-2", withMeet, map[string]any{"guestsCanInviteOthers": false, "conferenceData": meet})
	if v := view("g-created-2"); v["join_url"] != "https://meet.example.test/g-created-2" || v["subject"] != "private-gmail-plain" {
		t.Fatalf("event after adding Meet %v", v)
	}
	_, writes = write(args...)
	exact(args, writes, "PATCH", "/calendar/events/g-created-2", notify, map[string]any{"guestsCanInviteOthers": false})

	// The attendee list goes as a whole: kept entries, with their responses, are unchanged.
	google.mu.Lock()
	google.events["g-created-1"]["attendees"] = []any{map[string]any{"email": "first@example.test", "responseStatus": "accepted"}, map[string]any{"email": "second@example.test", "optional": true, "responseStatus": "tentative"}}
	google.mu.Unlock()
	args = []string{"--account", "gmail", "calendar", "update", "g-created-1", "--subject", "private-gmail-new", "--start", "2026-10-30 10:00", "--add-attendee", "third@example.test", "--remove-attendee", "first@example.test"}
	_, writes = write(args...)
	exact(args, writes, "PATCH", "/calendar/events/g-created-1", notify, map[string]any{"summary": "private-gmail-new", "start": utc("2026-10-30T10:00:00Z"), "end": utc("2026-10-30T10:30:00Z"),
		"attendees": []any{map[string]any{"email": "second@example.test", "optional": true, "responseStatus": "tentative"}, map[string]any{"email": "third@example.test"}}, "guestsCanInviteOthers": false})

	// A response changes only the account's own attendee entry.
	args = []string{"--account", "gmail", "calendar", "respond", "g-invited", "--response", "tentative", "--comment", "private-gmail-rsvp"}
	_, writes = write(args...)
	exact(args, writes, "PATCH", "/calendar/events/g-invited", notify, map[string]any{"attendees": []any{map[string]any{"email": "olga@example.test", "organizer": true, "responseStatus": "accepted"}, map[string]any{"email": "work@example.test", "self": true, "responseStatus": "tentative", "comment": "private-gmail-rsvp"}}})

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--account", "gmail", "calendar", "cancel", "g-created-1", "--comment", "private-gmail-cancel"}, "calendar cancel refused: comments are not supported for this account"},
		{[]string{"--account", "gmail", "calendar", "update", "g-invited", "--subject", "private-gmail-subject"}, "calendar write refused: only the organizer can update or cancel"},
		{[]string{"--account", "gmail", "calendar", "respond", "g-created-1", "--response", "accept"}, "calendar response refused: organizer cannot respond"},
		{[]string{"--account", "gmail", "calendar", "cancel", "g-recurring"}, "calendar write refused: recurring events are not supported"},
	} {
		before := len(google.recorded())
		out, errout, code := run(tc.args...)
		if code != 1 || out != "" || errout != "error: "+tc.want+"\n" || len(google.recorded()) != before {
			t.Fatalf("command %v: exit=%d stdout=%q stderr=%q", tc.args, code, out, errout)
		}
	}

	// Cancel deletes with notifications; Google keeps the event as cancelled, so a second cancel is refused.
	args = []string{"--account", "gmail", "calendar", "cancel", "g-created-1"}
	_, writes = write(args...)
	if len(writes) != 1 || writes[0].Method != "DELETE" || writes[0].Path != "/calendar/events/g-created-1" || !reflect.DeepEqual(writes[0].Query, notify) {
		t.Fatalf("cancel writes %#v", writes)
	}
	out, errout, code := run(args...)
	if code != 1 || out != "" || errout != "error: calendar write refused: event is cancelled\n" {
		t.Fatalf("second cancel: exit=%d stdout=%q stderr=%q", code, out, errout)
	}
	if n := len(google.recorded()); n != 7 {
		t.Fatalf("Google writes=%d, want 2 invites, 3 updates, 1 response, 1 cancel", n)
	}
}
