package server_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/screening"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

// meetingNativeStore grants the M365 write scope to "work" only, so "other" proves the missing-scope refusal.
type meetingNativeStore struct{ nativeStore }

func (s *meetingNativeStore) LoadOAuth(ctx context.Context, p, a string) (credentials.OAuth, error) {
	if p != "m365" {
		return s.nativeStore.LoadOAuth(ctx, p, a)
	}
	scopes := "User.Read Mail.ReadWrite Calendars.Read Calendars.ReadWrite MailboxSettings.Read"
	if a == "other" {
		scopes = "User.Read Mail.ReadWrite Calendars.Read MailboxSettings.Read"
	}
	claims, _ := json.Marshal(map[string]string{"scp": scopes})
	return credentials.OAuth{AccessToken: "x." + base64.RawURLEncoding.EncodeToString(claims) + ".x", ExpiresAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), Username: "work@example.test"}, nil
}

// meetingGraph is a stateful fake of the Graph event endpoints. Created events keep every posted field.
type meetingGraph struct {
	mu      sync.Mutex
	events  map[string]map[string]any
	writes  []externalWrite
	created int
}

func (g *meetingGraph) handle(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	id, action, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/me/events/"), "/")
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
		for k, v := range body {
			e[k] = v
		}
		if body["isOnlineMeeting"] == true {
			e["onlineMeeting"] = map[string]any{"joinUrl": "https://teams.example.test/join/" + id}
		}
		g.events[id] = e
		response(w, map[string]any{"id": id})
	case r.Method == "PATCH" && g.events[id] != nil && action == "":
		for k, v := range body {
			g.events[id][k] = v
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
	for k, v := range extra {
		e[k] = v
	}
	return e
}

// The compiled client only knows the server URL and its own time zone; it never sees configuration or credentials.
func TestNativeBinaryMeetingWorkflow(t *testing.T) {
	binary := buildNative(t)
	home := t.TempDir()
	configFile(t, home)
	f, err := os.OpenFile(filepath.Join(home, "config.toml"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("[accounts.gmail]\nprovider='gmail'\nclient_id='synthetic'\nclient_secret='private-config-canary'\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	g := &meetingGraph{events: map[string]map[string]any{"recurring-1": syntheticEvent("recurring-1", true, map[string]any{"type": "occurrence"})}}
	for _, r := range []string{"accept", "tentative", "decline"} {
		g.events["attendee-"+r] = syntheticEvent("attendee-"+r, false, nil)
	}
	graph := httptest.NewServer(http.HandlerFunc(g.handle))
	defer graph.Close()
	var googleRequests atomic.Int32
	google := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { googleRequests.Add(1); w.WriteHeader(500) }))
	defer google.Close()
	api := newNativeAPI()
	jev := httptest.NewServer(http.HandlerFunc(api.jev))
	defer jev.Close()
	creds := &meetingNativeStore{}
	screen := screening.New(screening.Options{Endpoint: jev.URL, Credentials: creds, LookupEnv: func(string) string { return "synthetic-key" }})
	s, err := server.New(context.Background(), home, server.Options{Credentials: creds, Screener: screen, M365: m365.Options{BaseURL: graph.URL}, Gmail: gmail.Options{GmailURL: google.URL + "/gmail", ProfileURL: google.URL + "/gmail/profile", CalendarURL: google.URL + "/calendar"}, Now: func() time.Time { return time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := httptest.NewServer(s)
	defer h.Close()
	clientHome := t.TempDir()
	run := func(args ...string) (string, string, int) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, append([]string{"--server", h.URL}, args...)...)
		// Lisbon leaves summer time on 2026-10-25, between the invite and the update below.
		cmd.Env = []string{"HOME=" + clientHome, "AGENTMAILGUARD_HOME=" + filepath.Join(clientHome, "does-not-exist"), "TZ=Europe/Lisbon"}
		var out, errout bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errout
		code := 0
		if e := cmd.Run(); e != nil {
			code = -1
			if ex, ok := e.(*exec.ExitError); ok {
				code = ex.ExitCode()
			}
		}
		return out.String(), errout.String(), code
	}
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

	args := []string{"--account", "work", "calendar", "invite", "--to", "first@example.test", "--to", "second@example.test", "--subject", "private-meeting-subject", "--body", "private-meeting-body", "--location", "private-room", "--start", "2026-10-09 09:00", "--duration", "30m", "--teams"}
	out, writes := write(args...)
	exact(args, writes, "POST", "/me/events", map[string]any{"subject": "private-meeting-subject", "body": map[string]any{"contentType": "text", "content": "private-meeting-body"}, "location": map[string]any{"displayName": "private-room"},
		"start": utc("2026-10-09T08:00:00"), "end": utc("2026-10-09T08:30:00"), "attendees": []any{attendee("first@example.test", "required"), attendee("second@example.test", "required")}, "isOnlineMeeting": true, "onlineMeetingProvider": "teamsForBusiness"})
	if strings.TrimSpace(out) != "{\n  \"id\": \"created-1\"\n}" {
		t.Fatalf("invite output %q", out)
	}
	args = []string{"--account", "work", "calendar", "invite", "--to", "first@example.test", "--subject", "private-plain-subject", "--start", "2026-10-09 11:00", "--end", "2026-10-09 11:15"}
	_, writes = write(args...)
	exact(args, writes, "POST", "/me/events", map[string]any{"subject": "private-plain-subject", "body": map[string]any{"contentType": "text", "content": ""}, "location": map[string]any{"displayName": ""},
		"start": utc("2026-10-09T10:00:00"), "end": utc("2026-10-09T10:15:00"), "attendees": []any{attendee("first@example.test", "required")}})

	v := view("created-1")
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
		{[]string{"--account", "gmail", "calendar", "invite", "--to", "first@example.test", "--subject", "private-gmail-subject", "--start", "2026-10-09 09:00", "--duration", "30m"}, "calendar writes are not supported for this account"},
		{[]string{"--account", "gmail", "calendar", "respond", "attendee-accept", "--response", "accept"}, "calendar writes are not supported for this account"},
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
	if googleRequests.Load() != 0 {
		t.Fatal("Gmail refusal reached the provider")
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
	if total != 7 {
		t.Fatalf("Graph writes=%d, want 2 invites, 1 update, 1 cancel, 3 responses", total)
	}

	log, err := os.ReadFile(filepath.Join(home, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for operation, count := range map[string]int{"calendar_invite": 2, "calendar_update": 1, "calendar_cancel": 1, "calendar_respond": 3} {
		if n := bytes.Count(log, []byte(`"operation":"`+operation+`"`)); n != count {
			t.Errorf("audit has %d %s records, want %d", n, operation, count)
		}
	}
	for _, canary := range []string{"private-", "example.test", "synthetic-key"} {
		if bytes.Contains(log, []byte(canary)) {
			t.Fatalf("audit contains %s", canary)
		}
	}
	if entries, err := os.ReadDir(clientHome); err != nil || len(entries) != 0 {
		t.Fatal("remote client created local state")
	}
}
