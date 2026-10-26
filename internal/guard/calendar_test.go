package guard_test

import (
	"context"
	"strings"
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
func (c *calendar) Search(context.Context, string, string) (model.EventPage, error) {
	c.calls++
	return model.EventPage{Events: []model.Event{c.event}}, nil
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
	if _, err := g.CalendarList(context.Background(), now, now.AddDate(10, 0, 0)); err != nil {
		t.Fatal(err)
	}
	for _, end := range []time.Time{now, now.Add(-time.Hour)} {
		if _, err := g.CalendarList(context.Background(), now, end); err == nil {
			t.Fatal("invalid calendar range accepted")
		}
	}
}

// searchCalendar answers Search with found and next, and records the pages it was asked for.
type searchCalendar struct {
	model.Calendar
	found []model.Event
	next  string
	pages []string
}

func (c *searchCalendar) Search(_ context.Context, _, page string) (model.EventPage, error) {
	c.pages = append(c.pages, page)
	return model.EventPage{Events: c.found, Next: c.next}, nil
}

func TestCalendarSearchScreensEachFoundEventInOrder(t *testing.T) {
	text := model.Text
	found := []model.Event{
		{ID: "b", Subject: "hidden subject", Start: now, End: now.Add(time.Hour), Body: ptr("notes"), ContentType: &text},
		{ID: "a", Subject: "Budget", Start: now, End: now.Add(time.Hour), Body: ptr("agenda"), ContentType: &text},
	}
	s := &subjectScreener{verdicts: map[string]model.Verdict{"hidden subject": model.Error}}
	g, _, _ := policyGuard(t, &mailbox{}, &searchCalendar{found: found}, s, allowAll)
	page, err := g.CalendarSearch(t.Context(), "budget", "")
	got := page.Events
	if err != nil || len(got) != 2 || got[0].ID != "b" || got[1].ID != "a" || len(s.screened) != 2 {
		t.Fatalf("got=%+v err=%v screened=%v", got, err, s.screened)
	}
	if got[0].Verdict != model.Error || got[0].Subject != nil || got[0].Body != nil {
		t.Fatalf("error event exposed hidden fields: %+v", got[0])
	}
	if got[1].Verdict != model.Green || got[1].Subject == nil || *got[1].Subject != "Budget" || got[1].Body == nil {
		t.Fatalf("green event: %+v", got[1])
	}
}

func TestCalendarSearchFailsClosedOnAResultThatBreaksTheContract(t *testing.T) {
	e := model.Event{ID: "a", Start: now, End: now.Add(time.Hour), Body: ptr(""), ContentType: ptr(model.Text)}
	unnamed := e
	unnamed.ID = ""
	for name, found := range map[string][]model.Event{"empty ID": {unnamed}, "duplicate ID": {e, e}} {
		t.Run(name, func(t *testing.T) {
			s := &subjectScreener{}
			g, _, _ := policyGuard(t, &mailbox{}, &searchCalendar{found: found}, s, allowAll)
			if got, err := g.CalendarSearch(t.Context(), "budget", ""); err != guard.GuardError("ValueError") || got.Events != nil || len(s.screened) != 0 {
				t.Fatalf("got=%+v err=%v screened=%v", got, err, s.screened)
			}
		})
	}
}

func TestCalendarSearchRefusesEmptyTextBeforeTheCalendar(t *testing.T) {
	cal := &searchCalendar{}
	g, _, _ := policyGuard(t, &mailbox{}, cal, &subjectScreener{}, allowAll)
	if _, err := g.CalendarSearch(t.Context(), "", ""); err != guard.GuardError("calendar search needs text") || len(cal.pages) != 0 {
		t.Fatalf("err=%v pages=%v", err, cal.pages)
	}
}

// The guard re-checks the page before the calendar and checks the next-page marker as List does.
func TestCalendarSearchChecksThePageAndTheNextMarker(t *testing.T) {
	e := model.Event{ID: "a", Start: now, End: now.Add(time.Hour), Body: ptr(""), ContentType: ptr(model.Text)}
	cal := &searchCalendar{found: []model.Event{e}, next: "20"}
	s := &subjectScreener{}
	g, _, _ := policyGuard(t, &mailbox{}, cal, s, allowAll)
	if got, err := g.CalendarSearch(t.Context(), "budget", "10"); err != nil || len(got.Events) != 1 || got.NextPage != "20" || cal.pages[0] != "10" {
		t.Fatalf("got=%+v err=%v pages=%v", got, err, cal.pages)
	}
	for _, page := range []string{"1 0", "\u00e9", strings.Repeat("1", model.MaxPage+1)} {
		if got, err := g.CalendarSearch(t.Context(), "budget", page); err != guard.GuardError("invalid request") || got.Events != nil || len(cal.pages) != 1 {
			t.Errorf("page %.20q: %d events, %v, pages=%d", page, len(got.Events), err, len(cal.pages))
		}
	}
	for _, next := range []string{"2 0", "\x1b[31m", strings.Repeat("1", model.MaxPage+1)} {
		cal.next = next
		if got, err := g.CalendarSearch(t.Context(), "budget", ""); err != guard.GuardError("ValueError") || got.Events != nil || got.NextPage != "" {
			t.Errorf("next %.20q: %d events, next %.20q, %v", next, len(got.Events), got.NextPage, err)
		}
	}
	if len(s.screened) != 1 {
		t.Fatalf("an event of a refused page was screened: %v", s.screened)
	}
}
