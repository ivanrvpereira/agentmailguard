package server_test

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/credentials"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

type googleWrite struct {
	Method, Path string
	Query        url.Values
	Body         map[string]any
}

// meetingGoogle is a stateful fake of the Google profile, labels, and primary calendar. Created events keep
// every posted field; a conference create request becomes a Meet link; a delete marks the event cancelled.
type meetingGoogle struct {
	mu       sync.Mutex
	events   map[string]map[string]any
	writes   []googleWrite
	created  int
	requests atomic.Int32
}

func (g *meetingGoogle) handle(w http.ResponseWriter, r *http.Request) {
	g.requests.Add(1)
	g.mu.Lock()
	defer g.mu.Unlock()
	switch r.URL.Path {
	case "/gmail/profile":
		response(w, map[string]string{"emailAddress": gmailEmail})
		return
	case "/gmail/labels":
		response(w, map[string]any{"labels": []any{}})
		return
	}
	id, _ := strings.CutPrefix(r.URL.Path, "/calendar/events/")
	if r.Method == "GET" {
		if e, ok := g.events[id]; ok {
			response(w, e)
			return
		}
		w.WriteHeader(404)
		return
	}
	write := googleWrite{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query()}
	_ = json.NewDecoder(r.Body).Decode(&write.Body)
	g.writes = append(g.writes, write)
	apply := func(e map[string]any) {
		maps.Copy(e, write.Body)
		if conference, ok := write.Body["conferenceData"].(map[string]any); ok && conference["createRequest"] != nil && write.Query.Get("conferenceDataVersion") == "1" {
			e["conferenceData"] = map[string]any{"entryPoints": []any{map[string]any{"entryPointType": "video", "uri": "https://meet.example.test/" + e["id"].(string)}}}
		}
	}
	switch {
	case r.Method == "POST" && r.URL.Path == "/calendar/events":
		g.created++
		e := map[string]any{"id": "g-created-" + strconv.Itoa(g.created), "status": "confirmed", "organizer": map[string]any{"email": "gmail@example.test", "self": true}}
		apply(e)
		g.events[e["id"].(string)] = e
		response(w, e)
	case r.Method == "PATCH" && g.events[id] != nil:
		apply(g.events[id])
		response(w, g.events[id])
	case r.Method == "DELETE" && g.events[id] != nil:
		g.events[id]["status"] = "cancelled"
		w.WriteHeader(204)
	default:
		w.WriteHeader(404)
	}
}

func (g *meetingGoogle) recorded() []googleWrite {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]googleWrite{}, g.writes...)
}

func googleEvent(id string, organizer bool, extra map[string]any) map[string]any {
	e := map[string]any{"id": id, "summary": "CANARY raw " + id, "description": "CANARY raw body", "start": map[string]string{"dateTime": "2026-10-09T09:00:00Z"}, "end": map[string]string{"dateTime": "2026-10-09T10:00:00Z"},
		"organizer": map[string]any{"email": "olga@example.test"}, "attendees": []any{map[string]any{"email": "olga@example.test", "organizer": true, "responseStatus": "accepted"}, map[string]any{"email": "work@example.test", "self": true, "responseStatus": "needsAction"}}}
	if organizer {
		e["organizer"] = map[string]any{"email": "work@example.test", "self": true}
		e["attendees"] = []any{map[string]any{"email": "keep@example.test", "responseStatus": "accepted"}, map[string]any{"email": "work@example.test", "self": true, "organizer": true, "responseStatus": "accepted"}}
	}
	maps.Copy(e, extra)
	return e
}

// gmailDaemon starts a daemon with the Gmail account gmail, logged in with scopes, and config appended to its
// configuration. The returned fake serves the Google APIs.
func gmailDaemon(t *testing.T, scopes, config string) (*daemon, *meetingGoogle) {
	t.Helper()
	google := &meetingGoogle{events: map[string]map[string]any{"org": googleEvent("org", true, nil), "att": googleEvent("att", false, nil)}}
	api := httptest.NewServer(http.HandlerFunc(google.handle))
	t.Cleanup(api.Close)
	d := startDaemon(t, daemonSetup{config: gmailConfig + config, record: record(map[string]credentials.OAuth{"gmail/" + gmailEmail: gmailLogin(scopes)}), jev: newNativeAPI(),
		options: server.Options{Gmail: googleOptions(api.URL), Now: func() time.Time { return time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC) }}})
	return d, google
}

// googleOptions point the Gmail, profile, calendar, and token endpoints at url.
func googleOptions(url string) gmail.Options {
	return gmail.Options{ProfileURL: url + "/gmail/profile", GmailURL: url + "/gmail", CalendarURL: url + "/calendar", TokenURL: url + "/token"}
}

var gmailMeetingParams = map[string]string{
	"calendar.invite":  `{"to":["a@example.test"],"subject":"CANARY","body":"CANARY body","start":"2026-10-09T09:00:00Z","end":"2026-10-09T10:00:00Z","online":false}`,
	"calendar.update":  `{"id":"org","subject":"CANARY new"}`,
	"calendar.cancel":  `{"id":"org"}`,
	"calendar.respond": `{"id":"att","response":"decline","comment":"CANARY comment"}`,
}

// Logins made before calendar writes keep reading, but cannot write until the user logs in again.
func TestGmailMeetingHTTPNeedsTheWriteGrant(t *testing.T) {
	for op, params := range gmailMeetingParams {
		t.Run(op, func(t *testing.T) {
			d, google := gmailDaemon(t, gmailReadGrant, allowAll)
			status, body := accountRPC(t, d.s, op, "gmail", params)
			if status != 500 || body != `{"error":"missing_scopes"}` || len(google.recorded()) != 0 {
				t.Fatalf("response=%d %s writes=%+v", status, body, google.recorded())
			}
			assertNoAudit(t, d.home)
		})
	}
	d, _ := gmailDaemon(t, gmailReadGrant, "")
	if status, body := accountRPC(t, d.s, "calendar.get", "gmail", `{"id":"org"}`); status != 200 || !strings.Contains(body, `"id":"org"`) {
		t.Fatalf("read with the old grant: %d %s", status, body)
	}
}

// Google has no cancellation message. A comment is refused before any provider access.
func TestGmailMeetingHTTPCancelRefusesAComment(t *testing.T) {
	d, google := gmailDaemon(t, gmailWriteGrant, allowAll)
	before := google.requests.Load()
	status, body := accountRPC(t, d.s, "calendar.cancel", "gmail", `{"id":"org","comment":"CANARY comment"}`)
	if status != 500 || body != `{"error":"calendar cancel refused: comments are not supported for this account"}` || google.requests.Load() != before {
		t.Fatalf("response=%d %s requests=%d", status, body, google.requests.Load()-before)
	}
	assertNoAudit(t, d.home)
}

// Google Calendar always notifies the organizer of a response. no_notify is refused before any provider access.
func TestGmailMeetingHTTPRespondRefusesNoNotify(t *testing.T) {
	d, google := gmailDaemon(t, gmailWriteGrant, allowAll)
	before := google.requests.Load()
	status, body := accountRPC(t, d.s, "calendar.respond", "gmail", `{"id":"att","response":"accept","no_notify":true}`)
	if status != 500 || body != `{"error":"calendar response refused: responses without notification are not supported for this account"}` || google.requests.Load() != before {
		t.Fatalf("response=%d %s requests=%d", status, body, google.requests.Load()-before)
	}
	assertNoAudit(t, d.home)
}

// The calendar policy keys apply to Gmail as they do to M365: deny is the default, and an approved action
// makes the same write as the allowed call.
func TestGmailMeetingPolicyDeniesApprovesAndAllows(t *testing.T) {
	for op, params := range gmailMeetingParams {
		t.Run(op, func(t *testing.T) {
			denied, deniedGoogle := gmailDaemon(t, gmailWriteGrant, "")
			status, body := accountRPC(t, denied.s, op, "gmail", params)
			if status != 500 || body != `{"error":"refused by policy: `+strings.Replace(op, ".", "_", 1)+`"}` || len(deniedGoogle.recorded()) != 0 {
				t.Fatalf("default policy: %d %s writes=%+v", status, body, deniedGoogle.recorded())
			}
			allowed, allowedGoogle := gmailDaemon(t, gmailWriteGrant, allowAll)
			status, want := accountRPC(t, allowed.s, op, "gmail", params)
			if status != 200 || len(allowedGoogle.recorded()) != 1 {
				t.Fatalf("allowed call: %d %s writes=%+v", status, want, allowedGoogle.recorded())
			}
			assertAudited(t, allowed.home, strings.Replace(op, ".", "_", 1))
			d, google := gmailDaemon(t, gmailWriteGrant, approveAll)
			id := pend(t, d.s, op, params)[0]
			if len(google.recorded()) != 0 {
				t.Fatal("held action reached Google")
			}
			if err := d.s.Approve(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if got := google.recorded(); len(got) != 1 || !reflect.DeepEqual(got[0], allowedGoogle.recorded()[0]) {
				t.Fatalf("approved writes=%+v; allowed=%+v", got, allowedGoogle.recorded())
			}
			assertState(t, d.s, id, `{"state":"done","result":`+want+`}`)
		})
	}
}
