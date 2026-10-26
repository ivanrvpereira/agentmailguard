package m365_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

func event() map[string]any {
	return map[string]any{"id": "event", "subject": "Planning", "start": map[string]string{"dateTime": "2026-09-26T09:00:00.0000000", "timeZone": "UTC"}, "end": map[string]string{"dateTime": "2026-09-26T09:30:00.0000000", "timeZone": "UTC"}, "isAllDay": true, "type": "occurrence", "location": map[string]string{"displayName": "Room 1"}, "organizer": map[string]any{"emailAddress": map[string]string{"name": "Olga", "address": "olga@example.test"}}, "isOrganizer": false, "attendees": []any{map[string]any{"emailAddress": map[string]string{"address": "bob@example.test"}, "status": map[string]string{"response": "accepted"}}, map[string]any{"emailAddress": map[string]string{"address": "carol@example.test"}}}, "onlineMeeting": map[string]string{"joinUrl": "https://teams.example.test/join"}, "sensitivity": "private", "categories": []string{"Work"}, "showAs": "busy", "isCancelled": true, "body": map[string]string{"contentType": "html", "content": "<p>agenda</p>"}, "attachments": []any{map[string]string{"name": "agenda.pdf"}}}
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
func TestCalendarOrganizerMetadata(t *testing.T) {
	for _, organizer := range []bool{true, false} {
		c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
			if !strings.Contains(r.URL.Query().Get("$select"), ",isOrganizer,") {
				t.Error("organizer authority not selected")
			}
			e := event()
			e["isOrganizer"] = organizer
			jsonResponse(w, e)
		})
		e, err := c.Calendar().Get(context.Background(), "event")
		if err != nil || e.IsOrganizer != organizer {
			t.Fatalf("organizer = %v, error = %v", e.IsOrganizer, err)
		}
	}
}

func TestCalendarRejectsMissingOrMalformedOrganizerMetadata(t *testing.T) {
	for name, value := range map[string]any{"missing": nil, "null": nil, "string": "false", "number": 0} {
		t.Run(name, func(t *testing.T) {
			c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
				e := event()
				e["isOrganizer"] = value
				if name == "missing" {
					delete(e, "isOrganizer")
				}
				if strings.Contains(r.URL.Path, "/events/") {
					jsonResponse(w, e)
					return
				}
				jsonResponse(w, map[string]any{"value": []any{e}})
			})
			if _, err := c.Calendar().Get(context.Background(), "event"); err == nil || err.Error() != "Graph error InvalidMetadata" {
				t.Fatalf("get accepted unproven organizer role: %v", err)
			}
			if _, err := c.Calendar().List(context.Background(), now, now.Add(time.Hour)); err == nil || err.Error() != "Graph error InvalidMetadata" {
				t.Fatalf("list accepted unproven organizer role: %v", err)
			}
		})
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

func TestCalendarSearchPostsTheSearchQueryAndReadsEachHit(t *testing.T) {
	var requests []string
	var mu sync.Mutex
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.EscapedPath()+" "+string(body))
		mu.Unlock()
		switch {
		case r.Method == "POST" && r.URL.Path == "/v1.0/search/query":
			var got map[string]any
			text := map[bool]string{true: "nothing", false: "budget plan"}[strings.Contains(string(body), "nothing")]
			if json.Unmarshal(body, &got) != nil || !reflect.DeepEqual(got, map[string]any{"requests": []any{map[string]any{"entityTypes": []any{"event"}, "query": map[string]any{"queryString": text}}}}) {
				t.Errorf("search body %s", body)
			}
			if text == "nothing" {
				jsonResponse(w, map[string]any{"value": []any{map[string]any{"searchTerms": []string{"nothing"}, "hitsContainers": []any{map[string]any{"total": 0, "moreResultsAvailable": false}}}}})
				return
			}
			jsonResponse(w, map[string]any{"value": []any{map[string]any{"hitsContainers": []any{map[string]any{"hits": []any{
				map[string]any{"hitId": "e/2", "rank": 1, "resource": map[string]any{"subject": "partial"}},
				map[string]any{"hitId": "e1", "rank": 2, "resource": map[string]any{"subject": "partial"}},
			}}}}}})
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, "/v1.0/me/events/"):
			if r.URL.Query().Get("$select") == "" || !strings.Contains(r.Header.Get("Prefer"), `outlook.timezone="UTC"`) {
				t.Errorf("event read %s", r.URL)
			}
			e := event()
			e["id"] = strings.TrimPrefix(r.URL.Path, "/v1.0/me/events/")
			jsonResponse(w, e)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
	})
	cal := c.Calendar()
	events, err := cal.Search(t.Context(), "budget plan")
	if err != nil || len(events) != 2 || events[0].ID != "e/2" || events[1].ID != "e1" || events[0].Subject != "Planning" || events[0].Body == nil {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	slices.Sort(requests)
	if !slices.Contains(requests, "GET /v1.0/me/events/e%2F2 ") || !slices.Contains(requests, "GET /v1.0/me/events/e1 ") || len(requests) != 3 {
		t.Fatalf("requests=%q", requests)
	}
	requests = nil
	if events, err := cal.Search(t.Context(), "nothing"); err != nil || len(events) != 0 || len(requests) != 1 {
		t.Fatalf("no hits: events=%+v err=%v requests=%q", events, err, requests)
	}
}

func TestCalendarSearchFailsOnAHitWithoutID(t *testing.T) {
	c := graphClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("read after a bad hit: %s %s", r.Method, r.URL)
		}
		jsonResponse(w, map[string]any{"value": []any{map[string]any{"hitsContainers": []any{map[string]any{"hits": []any{map[string]any{"rank": 1}}}}}}})
	})
	if events, err := c.Calendar().Search(t.Context(), "budget"); err == nil || events != nil {
		t.Fatalf("events=%+v err=%v", events, err)
	}
}
