package gmail_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
)

// Only Google's self flags establish the account's role; an organizer address that looks like the user's does not.
func TestCalendarEventRoleComesFromSelfFlags(t *testing.T) {
	for _, tc := range []struct {
		name      string
		organizer map[string]any
		want      bool
	}{
		{"organizer self", map[string]any{"email": "synthetic@example.test", "self": true}, true},
		{"organizer not self", map[string]any{"email": "synthetic@example.test"}, false},
		{"organizer self false", map[string]any{"email": "olga@example.test", "self": false}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				e := calendarEvent()
				e["organizer"] = tc.organizer
				respond(w, e)
			}))
			defer server.Close()
			c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), CalendarURL: server.URL})
			e, err := c.Calendar().Get(context.Background(), "ev")
			if err != nil || e.IsOrganizer != tc.want {
				t.Fatalf("IsOrganizer=%v err=%v, want %v", e.IsOrganizer, err, tc.want)
			}
		})
	}
}

type calendarRequest struct {
	Method, Path string
	Query        url.Values
	Body         map[string]any
}

// calendarAPI is a fake Google Calendar: GET returns event, and every other request is recorded and
// answered with reply (204 with no body when reply is nil).
type calendarAPI struct {
	mu     sync.Mutex
	event  map[string]any
	reply  map[string]any
	status int
	writes []calendarRequest
}

func (a *calendarAPI) handle(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		defer a.mu.Unlock()
		if r.Method == "GET" {
			respond(w, a.event)
			return
		}
		req := calendarRequest{Method: r.Method, Path: r.URL.EscapedPath(), Query: r.URL.Query()}
		if r.Method != "DELETE" {
			if err := json.NewDecoder(r.Body).Decode(&req.Body); err != nil {
				t.Errorf("%s body: %v", r.Method, err)
			}
		}
		a.writes = append(a.writes, req)
		if a.status != 0 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(a.status)
			_, _ = w.Write([]byte(`{"error":{"errors":[{"reason":"rateLimitExceeded"}]}}`))
			return
		}
		if a.reply == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		respond(w, a.reply)
	}
}

// only returns the single recorded write.
func (a *calendarAPI) only(t *testing.T) calendarRequest {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.writes) != 1 {
		t.Fatalf("calendar writes=%d, want 1: %+v", len(a.writes), a.writes)
	}
	return a.writes[0]
}

func calendarWriter(t *testing.T, a *calendarAPI) model.CalendarWriter {
	t.Helper()
	server := httptest.NewServer(a.handle(t))
	t.Cleanup(server.Close)
	c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), CalendarURL: server.URL, Sleep: func(context.Context, time.Duration) error { return nil }})
	w, ok := c.Calendar().(model.CalendarWriter)
	if !ok {
		t.Fatal("Gmail calendar is not a CalendarWriter")
	}
	return w
}

// Google renders the description as HTML, so the plain-text body goes escaped and keeps its line breaks.
func TestCalendarInvitePostsTheEventWithNotifications(t *testing.T) {
	for _, online := range []bool{false, true} {
		t.Run("online="+map[bool]string{false: "false", true: "true"}[online], func(t *testing.T) {
			api := &calendarAPI{reply: map[string]any{"id": "created"}}
			w := calendarWriter(t, api)
			start := time.Date(2026, 10, 9, 10, 0, 0, 0, time.FixedZone("Lisbon", 3600))
			id, err := w.Invite(context.Background(), model.NewEvent{Subject: "Plan", Body: "Agenda\r\n<a href=\"https://evil.example.test\">one</a>", Location: "Room 1", Start: start, End: start.Add(30 * time.Minute), Attendees: []string{"a@example.test", "b@example.test"}, Online: online})
			if err != nil || id != "created" {
				t.Fatalf("invite id=%q err=%v", id, err)
			}
			got := api.only(t)
			wantQuery := url.Values{"sendUpdates": {"all"}}
			want := map[string]any{"summary": "Plan", "description": "Agenda<br>&lt;a href=&#34;https://evil.example.test&#34;&gt;one&lt;/a&gt;", "location": "Room 1",
				"start": map[string]any{"dateTime": "2026-10-09T09:00:00Z"}, "end": map[string]any{"dateTime": "2026-10-09T09:30:00Z"},
				"attendees":             []any{map[string]any{"email": "a@example.test"}, map[string]any{"email": "b@example.test"}},
				"guestsCanInviteOthers": false}
			if online {
				wantQuery.Set("conferenceDataVersion", "1")
				request, _ := got.Body["conferenceData"].(map[string]any)["createRequest"].(map[string]any)
				if id, _ := request["requestId"].(string); len(id) < 16 {
					t.Fatalf("conference request ID %v", request["requestId"])
				}
				want["conferenceData"] = map[string]any{"createRequest": map[string]any{"requestId": request["requestId"], "conferenceSolutionKey": map[string]any{"type": "hangoutsMeet"}}}
			}
			if got.Method != "POST" || got.Path != "/events" || !reflect.DeepEqual(got.Query, wantQuery) || !reflect.DeepEqual(got.Body, want) {
				t.Fatalf("invite request %+v", got)
			}
		})
	}
}

func TestCalendarUpdatePatchesOnlyTheChangedFields(t *testing.T) {
	api := &calendarAPI{reply: map[string]any{"id": "ev"}, event: calendarEvent()}
	w := calendarWriter(t, api)
	subject, location := "New plan", ""
	start := time.Date(2026, 10, 9, 10, 0, 0, 0, time.FixedZone("Lisbon", 3600))
	end := start.Add(45 * time.Minute)
	if err := w.Update(context.Background(), "ev/1", model.EventChange{Subject: &subject, Location: &location, Start: &start, End: &end}); err != nil {
		t.Fatal(err)
	}
	got := api.only(t)
	want := map[string]any{"summary": "New plan", "location": "", "start": map[string]any{"dateTime": "2026-10-09T09:00:00Z"}, "end": map[string]any{"dateTime": "2026-10-09T09:45:00Z"}, "guestsCanInviteOthers": false}
	if got.Method != "PATCH" || got.Path != "/events/ev%2F1" || !reflect.DeepEqual(got.Query, url.Values{"sendUpdates": {"all"}}) || !reflect.DeepEqual(got.Body, want) {
		t.Fatalf("update request %+v", got)
	}
}

// PATCH replaces the attendee list as a whole, so the update sends every attendee it keeps, unchanged.
func TestCalendarUpdateAttendeesKeepsTheOthersUnchanged(t *testing.T) {
	keep := map[string]any{"email": "keep@example.test", "displayName": "Keep", "optional": true, "responseStatus": "accepted", "comment": "see you"}
	me := map[string]any{"email": "synthetic@example.test", "self": true, "organizer": true, "responseStatus": "accepted"}
	event := calendarEvent()
	event["attendees"] = []any{keep, map[string]any{"email": "Remove@example.test", "responseStatus": "declined"}, me}
	api := &calendarAPI{reply: map[string]any{"id": "ev"}, event: event}
	w := calendarWriter(t, api)
	if err := w.Update(context.Background(), "ev", model.EventChange{AddAttendees: []string{"new@example.test", "KEEP@example.test"}, RemoveAttendees: []string{"remove@example.test"}}); err != nil {
		t.Fatal(err)
	}
	got := api.only(t)
	want := map[string]any{"attendees": []any{keep, me, map[string]any{"email": "new@example.test"}}, "guestsCanInviteOthers": false}
	if got.Method != "PATCH" || got.Path != "/events/ev" || !reflect.DeepEqual(got.Query, url.Values{"sendUpdates": {"all"}}) || !reflect.DeepEqual(got.Body, want) {
		t.Fatalf("update request %+v", got)
	}
}

func TestCalendarUpdateAddsMeetOnlyWhenTheEventHasNoConference(t *testing.T) {
	for _, tc := range []struct {
		name  string
		event func(map[string]any)
		meet  bool
	}{
		{"no conference", func(e map[string]any) { delete(e, "conferenceData") }, true},
		{"conference data", func(map[string]any) {}, false},
		{"hangout link only", func(e map[string]any) { delete(e, "conferenceData"); e["hangoutLink"] = "https://meet.example.test/x" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := calendarEvent()
			tc.event(event)
			api := &calendarAPI{reply: map[string]any{"id": "ev"}, event: event}
			w := calendarWriter(t, api)
			if err := w.Update(context.Background(), "ev", model.EventChange{Online: true}); err != nil {
				t.Fatal(err)
			}
			got := api.only(t)
			want := map[string]any{"guestsCanInviteOthers": false}
			wantQuery := url.Values{"sendUpdates": {"all"}}
			if tc.meet {
				wantQuery.Set("conferenceDataVersion", "1")
				request, _ := got.Body["conferenceData"].(map[string]any)["createRequest"].(map[string]any)
				if id, _ := request["requestId"].(string); len(id) < 16 {
					t.Fatalf("conference request ID %v", request["requestId"])
				}
				want["conferenceData"] = map[string]any{"createRequest": map[string]any{"requestId": request["requestId"], "conferenceSolutionKey": map[string]any{"type": "hangoutsMeet"}}}
			}
			if got.Method != "PATCH" || !reflect.DeepEqual(got.Query, wantQuery) || !reflect.DeepEqual(got.Body, want) {
				t.Fatalf("update request %+v", got)
			}
		})
	}
}

func TestCalendarUpdateRefusesUnresolvedOrUnreadableInputWithoutWriting(t *testing.T) {
	start := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		attendees any
		change    model.EventChange
	}{
		{"start without end", nil, model.EventChange{Start: &start}},
		{"attendee without email", []any{map[string]any{"responseStatus": "accepted"}}, model.EventChange{AddAttendees: []string{"a@example.test"}}},
		{"attendees not a list", "a@example.test", model.EventChange{RemoveAttendees: []string{"a@example.test"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := calendarEvent()
			event["attendees"] = tc.attendees
			api := &calendarAPI{reply: map[string]any{"id": "ev"}, event: event}
			if err := calendarWriter(t, api).Update(context.Background(), "ev", tc.change); err == nil || len(api.writes) != 0 {
				t.Fatalf("err=%v writes=%d", err, len(api.writes))
			}
		})
	}
}

// A response is the account's own attendee entry. Every other entry goes back exactly as Google sent it.
func TestCalendarRespondChangesOnlyTheSelfEntry(t *testing.T) {
	organizer := map[string]any{"email": "olga@example.test", "organizer": true, "responseStatus": "accepted"}
	other := map[string]any{"email": "bob@example.test", "responseStatus": "tentative", "comment": "maybe"}
	for _, tc := range []struct {
		response model.MeetingResponse
		comment  string
		want     map[string]any
	}{
		{model.ResponseAccept, "", map[string]any{"email": "synthetic@example.test", "self": true, "responseStatus": "accepted"}},
		{model.ResponseTentative, "CANARY comment", map[string]any{"email": "synthetic@example.test", "self": true, "responseStatus": "tentative", "comment": "CANARY comment"}},
		{model.ResponseDecline, "", map[string]any{"email": "synthetic@example.test", "self": true, "responseStatus": "declined"}},
	} {
		t.Run(string(tc.response), func(t *testing.T) {
			event := calendarEvent()
			// An earlier comment does not travel with a new response.
			event["attendees"] = []any{organizer, map[string]any{"email": "synthetic@example.test", "self": true, "responseStatus": "needsAction", "comment": "old"}, other}
			api := &calendarAPI{reply: map[string]any{"id": "ev"}, event: event}
			if err := calendarWriter(t, api).Respond(context.Background(), "ev", model.EventResponse{Response: tc.response, Comment: tc.comment}); err != nil {
				t.Fatal(err)
			}
			got := api.only(t)
			want := map[string]any{"attendees": []any{organizer, tc.want, other}}
			if got.Method != "PATCH" || got.Path != "/events/ev" || !reflect.DeepEqual(got.Query, url.Values{"sendUpdates": {"all"}}) || !reflect.DeepEqual(got.Body, want) {
				t.Fatalf("respond request %+v", got)
			}
		})
	}
}

func TestCalendarRespondNeedsExactlyOneSelfEntry(t *testing.T) {
	for name, list := range map[string][]any{
		"no attendees": nil,
		"no self":      {map[string]any{"email": "synthetic@example.test", "responseStatus": "needsAction"}},
		"two self":     {map[string]any{"email": "a@example.test", "self": true}, map[string]any{"email": "b@example.test", "self": true}},
	} {
		t.Run(name, func(t *testing.T) {
			event := calendarEvent()
			delete(event, "attendees")
			if list != nil {
				event["attendees"] = list
			}
			api := &calendarAPI{reply: map[string]any{"id": "ev"}, event: event}
			if err := calendarWriter(t, api).Respond(context.Background(), "ev", model.EventResponse{Response: model.ResponseAccept}); err == nil || len(api.writes) != 0 {
				t.Fatalf("err=%v writes=%d", err, len(api.writes))
			}
		})
	}
}

// Cancel deletes the organizer's event with notifications. Google keeps a deleted event as cancelled on the
// organizer's calendar, where it can be restored, so this is not a permanent delete.
func TestCalendarCancelDeletesWithNotifications(t *testing.T) {
	api := &calendarAPI{event: calendarEvent()}
	if err := calendarWriter(t, api).Cancel(context.Background(), "ev/1", ""); err != nil {
		t.Fatal(err)
	}
	got := api.only(t)
	if got.Method != "DELETE" || got.Path != "/events/ev%2F1" || !reflect.DeepEqual(got.Query, url.Values{"sendUpdates": {"all"}}) {
		t.Fatalf("cancel request %+v", got)
	}
}

// Google has no cancellation message, so a comment is refused rather than dropped.
func TestCalendarCancelRefusesACommentWithoutRequests(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests++ }))
	defer server.Close()
	c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), CalendarURL: server.URL})
	if err := c.Calendar().(model.CalendarWriter).Cancel(context.Background(), "ev", "CANARY comment"); err == nil || requests != 0 {
		t.Fatalf("err=%v requests=%d", err, requests)
	}
}

// A write that may have reached Google is never sent again: a duplicate invitation is worse than a failure.
func TestCalendarWritesAreNotRetried(t *testing.T) {
	start := time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	subject := "New"
	for _, status := range []int{http.StatusTooManyRequests, http.StatusServiceUnavailable, http.StatusInternalServerError} {
		event := calendarEvent()
		event["attendees"] = []any{map[string]any{"email": "synthetic@example.test", "self": true}}
		api := &calendarAPI{event: event, status: status}
		w := calendarWriter(t, api)
		ctx := context.Background()
		for name, write := range map[string]func() error{
			"invite": func() error {
				_, err := w.Invite(ctx, model.NewEvent{Subject: "s", Start: start, End: start.Add(time.Hour), Attendees: []string{"a@example.test"}, Online: true})
				return err
			},
			"update":  func() error { return w.Update(ctx, "ev", model.EventChange{Subject: &subject, Online: true}) },
			"respond": func() error { return w.Respond(ctx, "ev", model.EventResponse{Response: model.ResponseAccept}) },
			"cancel":  func() error { return w.Cancel(ctx, "ev", "") },
		} {
			api.mu.Lock()
			before := len(api.writes)
			api.mu.Unlock()
			if err := write(); err == nil {
				t.Fatalf("status %d: %s succeeded", status, name)
			}
			if n := len(api.writes) - before; n != 1 {
				t.Fatalf("status %d: %d requests for one %s", status, n, name)
			}
		}
	}
}
