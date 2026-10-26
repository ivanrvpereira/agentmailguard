package gmail_test

import (
	"context"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func calendarEvent() map[string]any {
	return map[string]any{"id": "ev", "summary": "Agenda", "description": "line<br>two", "start": map[string]string{"dateTime": "2026-09-02T10:00:00.001+01:00"}, "end": map[string]string{"dateTime": "2026-09-02T11:00:00+01:00"}, "recurringEventId": "series", "visibility": "confidential", "transparency": "transparent", "status": "cancelled", "organizer": map[string]string{"displayName": "Olga", "email": "olga@example.test"}, "attendees": []any{map[string]string{"email": "bob@example.test", "responseStatus": "needsAction"}}, "attachments": []any{map[string]string{"title": "agenda.pdf"}}, "conferenceData": map[string]any{"entryPoints": []any{map[string]string{"entryPointType": "phone", "uri": "tel:123"}, map[string]string{"entryPointType": "video", "uri": "https://meet.example.test/x"}}}}
}
func TestCalendarExpandedPagesAndMapping(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("calendar write")
		}
		if r.URL.Path == "/events" {
			q := r.URL.Query()
			if q.Get("timeMin") != "2026-09-01T00:00:00Z" || q.Get("timeMax") != "2026-09-08T00:00:00Z" || q.Get("singleEvents") != "true" || q.Get("orderBy") != "startTime" || q.Get("maxResults") != "250" {
				t.Error("calendar query")
			}
			if q.Get("pageToken") == "" {
				respond(w, map[string]any{"items": []any{calendarEvent()}, "nextPageToken": "next"})
				return
			}
			respond(w, map[string]any{"items": []any{map[string]any{"id": "day", "start": map[string]string{"date": "2026-09-02"}, "end": map[string]string{"date": "2026-09-03"}, "recurrence": []string{"RRULE:FREQ=WEEKLY"}}}})
			return
		}
		if r.URL.EscapedPath() != "/events/ev%2F..%2Fx%3Fy" {
			t.Error("calendar path escaping")
		}
		v := calendarEvent()
		v["id"] = "ev/../x?y"
		respond(w, v)
	}))
	defer server.Close()
	c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), CalendarURL: server.URL})
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	events, err := c.Calendar().List(context.Background(), start, start.Add(7*24*time.Hour))
	if err != nil || len(events) != 2 {
		t.Fatal(events, err)
	}
	e := events[0]
	if !e.Start.Equal(time.Date(2026, 9, 2, 9, 0, 0, 1000000, time.UTC)) || !e.IsPrivate || !e.IsCancelled || e.ShowAs != "free" || *e.ContentType != model.HTML || *e.Body != "line<br>two" || *e.Recurrence != "recurring" || *e.JoinURL != "https://meet.example.test/x" || e.Attendees[0].Response != "needsAction" || e.Attachments[0] != "agenda.pdf" || e.Organizer.Name != "Olga" {
		t.Fatalf("event mapping: %+v", e)
	}
	e = events[1]
	if !e.IsAllDay || *e.Body != "" || *e.ContentType != model.Text || *e.Recurrence != "weekly" || e.End.Sub(e.Start) != 24*time.Hour {
		t.Fatal("all-day mapping")
	}
	if _, err = c.Calendar().Get(context.Background(), "ev/../x?y"); err != nil {
		t.Fatal(err)
	}
}
func TestCalendarMalformedAndCyclicPagesFail(t *testing.T) {
	for _, mode := range []string{"cycle", "null", "naive"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch mode {
				case "cycle":
					respond(w, map[string]any{"items": []any{}, "nextPageToken": "same"})
				case "null":
					respond(w, map[string]any{"items": nil})
				case "naive":
					e := calendarEvent()
					e["start"] = map[string]string{"dateTime": "2026-09-02T10:00:00"}
					respond(w, map[string]any{"items": []any{e}})
				}
			}))
			defer server.Close()
			c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), CalendarURL: server.URL})
			if _, err := c.Calendar().List(context.Background(), time.Now(), time.Now().Add(time.Hour)); err == nil {
				t.Fatal("invalid calendar accepted")
			}
		})
	}
}
func TestCalendarSearchSendsOnlyTheTextAsQAndReadsOnePage(t *testing.T) {
	var queries []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		queries = append(queries, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
		respond(w, map[string]any{"items": []any{calendarEvent(), map[string]any{"id": "day", "start": map[string]string{"date": "2026-09-02"}, "end": map[string]string{"date": "2026-09-03"}}}, "nextPageToken": "next"})
	}))
	defer server.Close()
	c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), CalendarURL: server.URL}).Calendar()
	events, err := c.Search(context.Background(), "budget & plan")
	if err != nil || len(events) != 2 || events[0].ID != "ev" || events[0].Organizer.Name != "Olga" || events[1].ID != "day" || !events[1].IsAllDay {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if len(queries) != 1 || queries[0] != "GET /events?q=budget+%26+plan" {
		t.Fatalf("queries=%q", queries)
	}
}
func TestCalendarSearchSkipsDeletedRecurringInstances(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// With singleEvents and showDeleted false, Google lists a deleted instance of a recurring event as a
		// cancelled entry without start or end.
		respond(w, map[string]any{"items": []any{calendarEvent(), map[string]any{"id": "series_20260903", "status": "cancelled", "recurringEventId": "series", "originalStartTime": map[string]string{"dateTime": "2026-09-03T10:00:00Z"}}}})
	}))
	defer server.Close()
	c := gmail.New(account, validStore(), gmail.Options{HTTPClient: server.Client(), CalendarURL: server.URL}).Calendar()
	events, err := c.Search(context.Background(), "agenda")
	if err != nil || len(events) != 1 || events[0].ID != "ev" {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}
