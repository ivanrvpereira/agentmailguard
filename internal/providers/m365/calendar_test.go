package m365_test

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
)

func event() map[string]any {
	return map[string]any{"id": "event", "subject": "Planning", "start": map[string]string{"dateTime": "2026-09-26T09:00:00.0000000", "timeZone": "UTC"}, "end": map[string]string{"dateTime": "2026-09-26T09:30:00.0000000", "timeZone": "UTC"}, "isAllDay": true, "type": "occurrence", "location": map[string]string{"displayName": "Room 1"}, "organizer": map[string]any{"emailAddress": map[string]string{"name": "Olga", "address": "olga@example.test"}}, "attendees": []any{map[string]any{"emailAddress": map[string]string{"address": "bob@example.test"}, "status": map[string]string{"response": "accepted"}}, map[string]any{"emailAddress": map[string]string{"address": "carol@example.test"}}}, "onlineMeeting": map[string]string{"joinUrl": "https://teams.example.test/join"}, "sensitivity": "private", "categories": []string{"Work"}, "showAs": "busy", "isCancelled": true, "body": map[string]string{"contentType": "html", "content": "<p>agenda</p>"}, "attachments": []any{map[string]string{"name": "agenda.pdf"}}}
}
func TestCalendarMapsAllFieldsAndPagesInUTC(t *testing.T) {
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || !strings.Contains(r.Header.Get("Prefer"), `outlook.timezone="UTC"`) {
			t.Error("calendar not read-only UTC")
		}
		if strings.Contains(r.URL.Path, "/events/") {
			if r.URL.EscapedPath() != "/v1.0/me/events/e%2F1" {
				t.Error("event ID not escaped")
			}
			jsonResponse(w, event())
			return
		}
		q := r.URL.Query()
		if q.Get("page") == "2" {
			e := event()
			e["id"] = "second"
			delete(e, "body")
			jsonResponse(w, map[string]any{"value": []any{e}})
			return
		}
		if q.Get("startDateTime") != "2026-09-21T00:00:00Z" || q.Get("endDateTime") != "2026-09-22T00:00:00Z" || q.Get("$expand") != "attachments($select=name)" {
			t.Errorf("calendar query %v", q)
		}
		jsonResponse(w, map[string]any{"value": []any{event()}, "@odata.nextLink": "http://" + r.Host + "/v1.0/me/calendarView?page=2"})
	})
	ctx := context.Background()
	cal := c.Calendar()
	events, err := cal.List(ctx, now, now.Add(24*time.Hour))
	if err != nil || len(events) != 2 || events[1].Body != nil {
		t.Fatalf("events %v %v", events, err)
	}
	ev, err := cal.Get(ctx, "e/1")
	if err != nil {
		t.Fatal(err)
	}
	recurrence, join, body := "recurring", "https://teams.example.test/join", "<p>agenda</p>"
	typ := model.HTML
	expected := model.Event{ID: "event", Subject: "Planning", Start: time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC), End: time.Date(2026, 9, 26, 9, 30, 0, 0, time.UTC), IsAllDay: true, Recurrence: &recurrence, Location: "Room 1", Organizer: model.Address{Name: "Olga", Address: "olga@example.test"}, Attendees: []model.Attendee{{Address: model.Address{Address: "bob@example.test"}, Response: "accepted"}, {Address: model.Address{Address: "carol@example.test"}, Response: "none"}}, JoinURL: &join, Attachments: []string{"agenda.pdf"}, IsPrivate: true, Categories: []string{"Work"}, ShowAs: "busy", IsCancelled: true, Body: &body, ContentType: &typ}
	if !reflect.DeepEqual(ev, expected) || !reflect.DeepEqual(events[0], expected) {
		t.Fatalf("event mapping %+v", ev)
	}
}
func TestCalendarRejectsNonUTCorInvalidDates(t *testing.T) {
	for _, start := range []any{nil, map[string]string{"dateTime": "2026-09-21T10:00:00", "timeZone": "W. Europe Standard Time"}, map[string]string{"dateTime": "SECRET", "timeZone": "UTC"}, map[string]string{"dateTime": "2026-09-21T10:00:00+01:00", "timeZone": "UTC"}} {
		c := graphClient(t, func(w http.ResponseWriter, r *http.Request) { e := event(); e["start"] = start; jsonResponse(w, e) })
		if _, err := c.Calendar().Get(context.Background(), "e"); err == nil || err.Error() != "Graph error InvalidMetadata" {
			t.Fatalf("invalid date accepted %v", err)
		}
	}
}
