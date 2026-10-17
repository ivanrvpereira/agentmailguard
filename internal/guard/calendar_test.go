package guard_test

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/audit"
	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

type calendar struct {
	event model.Event
	calls int
}

func (c *calendar) Get(context.Context, string) (model.Event, error) { c.calls++; return c.event, nil }
func (c *calendar) List(context.Context, time.Time, time.Time) ([]model.Event, error) {
	c.calls++
	return []model.Event{c.event}, nil
}

type eventScreener struct {
	model.Screener
	states []map[string]any
}

func (s *eventScreener) Screen(_ context.Context, state map[string]any) (model.Screening, error) {
	s.states = append(s.states, state)
	return model.Screening{Verdict: model.Green, Score: new(0.0)}, nil
}

// CheckAction never flags, so writes behave as before the Jev action check.
func (s *eventScreener) CheckAction(context.Context, string, string, map[string]any) (model.ActionCheck, error) {
	return model.ActionScore(0), nil
}
func TestEventMissingBodyFailsClosedAndDoesNotBecomeCached(t *testing.T) {
	b := &mailbox{}
	s := &eventScreener{}
	_, c, path := setup(t, b, s)
	cal := &calendar{event: model.Event{ID: "e1", Subject: "meeting", Start: now, End: now.Add(time.Hour)}}
	g := guard.New("work", b, cal, s, c, audit.New(path), guard.Options{Now: func() time.Time { return now }})
	ctx := context.Background()
	v, err := g.CalendarGet(ctx, "e1")
	if err != nil || v.Verdict != model.Error || v.Subject != nil || v.Body != nil || len(s.states) != 0 {
		t.Fatalf("missing event body did not fail before Jev: %+v %v", v, err)
	}
	cal.event.Body = new("BODYTEXT")
	cal.event.ContentType = ptr(model.Text)
	for range 2 {
		v, err = g.CalendarGet(ctx, "e1")
		if err != nil || v.Verdict != model.Green || v.Body == nil {
			t.Fatal("valid event failed")
		}
	}
	if len(s.states) != 2 {
		t.Fatal("event was cached")
	}
	cal.event.ID = "other"
	if _, err := g.CalendarGet(ctx, "e1"); err == nil {
		t.Fatal("unexpected fetched event accepted")
	}
}

func TestPrivateEventsScreenEveryReadWithoutBody(t *testing.T) {
	b := &mailbox{}
	s := &eventScreener{}
	_, c, path := setup(t, b, s)
	cal := &calendar{event: model.Event{ID: "e1", Subject: "meeting", Start: now, End: now.Add(time.Hour), IsPrivate: true, Body: new("SECRET"), ContentType: ptr(model.HTML)}}
	g := guard.New("work", b, cal, s, c, audit.New(path), guard.Options{Now: func() time.Time { return now }})
	for range 2 {
		v, err := g.CalendarGet(context.Background(), "e1")
		if err != nil {
			t.Fatal(err)
		}
		if v.Verdict != model.Green || v.Body != nil || v.Subject == nil {
			t.Fatalf("private event is not screened metadata-only: %+v", v)
		}
	}
	if len(s.states) != 2 {
		t.Fatalf("event reads screened %d times, want 2", len(s.states))
	}
	for _, state := range s.states {
		if _, ok := state["body"]; ok {
			t.Fatal("private event body reached screener")
		}
	}
	if _, err := g.CalendarList(context.Background(), now, now.Add(90*24*time.Hour+time.Hour), model.EventFilter{}); err != nil {
		t.Fatal(err)
	}
	for _, end := range []time.Time{now, now.Add(-time.Hour), now.Add(90*24*time.Hour + time.Hour + time.Nanosecond)} {
		if _, err := g.CalendarList(context.Background(), now, end, model.EventFilter{}); err == nil {
			t.Fatal("invalid calendar range accepted")
		}
	}
}

// eventList is a calendar that lists its events in order.
type eventList []model.Event

func (l eventList) List(context.Context, time.Time, time.Time) ([]model.Event, error) { return l, nil }
func (l eventList) Get(context.Context, string) (model.Event, error) {
	return model.Event{}, model.MailboxError("NotFoundError")
}

func TestCalendarListFilterScreensAndReturnsOnlyMatchingEventsInOrder(t *testing.T) {
	text, html := model.Text, model.HTML
	events := eventList{
		{ID: "subject", Subject: "Budget review", Start: now, End: now.Add(time.Hour), Organizer: model.Address{Name: "Ana Lima", Address: "a1@example.test"}, Attendees: []model.Attendee{{Address: model.Address{Name: "Bob", Address: "b1@example.test"}}}, Body: ptr("notes"), ContentType: &text},
		{ID: "location", Subject: "Sync", Location: "Budget room", Start: now, End: now.Add(time.Hour), Organizer: model.Address{Address: "ana@example.test"}, Attendees: []model.Attendee{{Address: model.Address{Address: "carla@example.test"}}}, Body: ptr(""), ContentType: &text},
		{ID: "body", Subject: "Plan", Start: now, End: now.Add(time.Hour), Organizer: model.Address{Name: "Olga", Address: "olga@example.test"}, Attendees: []model.Attendee{{Address: model.Address{Address: "bobby@example.test"}}}, Body: ptr("<p>The BUDGET plan</p>"), ContentType: &html},
		{ID: "private", Subject: "Private", Start: now, End: now.Add(time.Hour), Organizer: model.Address{Address: "olga@example.test"}, IsPrivate: true, Body: ptr("budget"), ContentType: &text},
	}
	subjects := map[string]string{"Budget review": "subject", "Sync": "location", "Plan": "body", "Private": "private"}
	all := []string{"subject", "location", "body", "private"}
	for _, tc := range []struct {
		name   string
		filter model.EventFilter
		want   []string
	}{
		{"no filter", model.EventFilter{}, all},
		{"empty fields", model.EventFilter{Text: ptr(""), Organizer: ptr(""), Attendee: ptr("")}, all},
		{"text in subject, location, or body, never a private body", model.EventFilter{Text: ptr("budget")}, []string{"subject", "location", "body"}},
		{"organizer name or address", model.EventFilter{Organizer: ptr("ANA")}, []string{"subject", "location"}},
		{"attendee name or address", model.EventFilter{Attendee: ptr("bob")}, []string{"subject", "body"}},
		{"every field must match", model.EventFilter{Text: ptr("budget"), Organizer: ptr("olga")}, []string{"body"}},
		{"no match", model.EventFilter{Attendee: ptr("nobody")}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &subjectScreener{}
			g, _, _ := policyGuard(t, &mailbox{}, events, s, allowAll)
			got, err := g.CalendarList(t.Context(), now, now.Add(time.Hour), tc.filter)
			ids := []string{}
			for _, v := range got {
				ids = append(ids, v.ID)
			}
			// Screening runs in parallel, so its order is not fixed.
			screened := []string{}
			for _, subject := range s.screened {
				screened = append(screened, subjects[subject])
			}
			if err != nil || !reflect.DeepEqual(ids, tc.want) || !reflect.DeepEqual(slices.Sorted(slices.Values(screened)), slices.Sorted(slices.Values(tc.want))) {
				t.Fatalf("listed=%v screened=%v err=%v, want %v", ids, screened, err, tc.want)
			}
		})
	}
}
