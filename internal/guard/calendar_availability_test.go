package guard_test

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/audit"
	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

type availabilityCalendar struct {
	calendar
	schedules []model.Schedule
	err       error
	queries   [][]string
}

func (c *availabilityCalendar) Availability(_ context.Context, addresses []string, _, _ time.Time) ([]model.Schedule, error) {
	c.queries = append(c.queries, addresses)
	return c.schedules, c.err
}

func availabilityGuard(t *testing.T, cal model.Calendar) (*guard.Guard, *eventScreener, string) {
	t.Helper()
	b := &mailbox{}
	s := &eventScreener{}
	_, cache, path := setup(t, b, s)
	return guard.New("work", b, cal, s, cache, audit.New(path), guard.Options{}), s, path
}

func slots(values ...model.AvailabilityStatus) []model.AvailabilityStatus { return values }

func TestCalendarAvailabilityReturnsOnlyRequestedParticipantsWithProvenStatuses(t *testing.T) {
	cal := &availabilityCalendar{schedules: []model.Schedule{
		{Address: "stranger@example.test", Slots: slots("free", "free")},
		{Address: "b@example.test", Slots: slots("busy", "tentative")},
		{Address: "A@EXAMPLE.TEST", Slots: slots("free", "out_of_office")},
		{Address: "short@example.test", Slots: slots("free")},
		{Address: "text@example.test", Slots: slots("free", "CANARY")},
		{Address: "twice@example.test", Slots: slots("free", "free")},
		{Address: "twice@example.test", Slots: slots("free", "free")},
	}}
	g, screener, path := availabilityGuard(t, cal)
	requested := []string{"a@example.test", "B@example.test", "missing@example.test", "short@example.test", "text@example.test", "twice@example.test"}
	got, err := g.CalendarAvailability(context.Background(), requested, now, now.Add(time.Hour))
	unknown := slots("unknown", "unknown")
	want := []model.Schedule{
		{Address: "a@example.test", Slots: slots("free", "out_of_office")},
		{Address: "B@example.test", Slots: slots("busy", "tentative")},
		{Address: "missing@example.test", Slots: unknown},
		{Address: "short@example.test", Slots: unknown},
		{Address: "text@example.test", Slots: unknown},
		{Address: "twice@example.test", Slots: unknown},
	}
	if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(cal.queries, [][]string{requested}) {
		t.Fatalf("availability=%v err=%v queries=%v", got, err, cal.queries)
	}
	if len(screener.states) != 0 || cal.calls != 0 {
		t.Fatalf("availability screened %d states and read %d events", len(screener.states), cal.calls)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("availability wrote audit")
	}
}

func TestCalendarAvailabilityRefusesBeforeProviderAccess(t *testing.T) {
	g, _, _ := availabilityGuard(t, &calendar{})
	if got, err := g.CalendarAvailability(context.Background(), []string{"a@example.test"}, now, now.Add(time.Hour)); got != nil || err == nil || err.Error() != "calendar availability is not supported for this account" {
		t.Fatalf("read-only calendar: availability=%v err=%v", got, err)
	}
	cal := &availabilityCalendar{}
	g, _, _ = availabilityGuard(t, cal)
	many := make([]string, 21)
	for i := range many {
		many[i] = strings.Repeat("a", i+1) + "@example.test"
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, tc := range map[string]struct {
		ctx        context.Context
		addresses  []string
		start, end time.Time
		want       string
	}{
		"no addresses":      {context.Background(), nil, now, now.Add(time.Hour), "invalid request"},
		"21 addresses":      {context.Background(), many, now, now.Add(time.Hour), "invalid request"},
		"blank address":     {context.Background(), []string{"a@example.test", " "}, now, now.Add(time.Hour), "invalid request"},
		"duplicate address": {context.Background(), []string{"a@example.test", "A@Example.Test"}, now, now.Add(time.Hour), "invalid request"},
		"empty range":       {context.Background(), []string{"a@example.test"}, now, now, "invalid request"},
		"reversed range":    {context.Background(), []string{"a@example.test"}, now, now.Add(-time.Hour), "invalid request"},
		"62 days":           {context.Background(), []string{"a@example.test"}, now, now.Add(62 * 24 * time.Hour), "invalid request"},
		"year zero":         {context.Background(), []string{"a@example.test"}, time.Date(0, 12, 31, 23, 0, 0, 0, time.UTC), time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC), "invalid request"},
		"canceled":          {canceled, []string{"a@example.test"}, now, now.Add(time.Hour), "context canceled"},
	} {
		if got, err := g.CalendarAvailability(tc.ctx, tc.addresses, tc.start, tc.end); got != nil || err == nil || err.Error() != tc.want {
			t.Errorf("%s: availability=%v err=%v", name, got, err)
		}
	}
	if len(cal.queries) != 0 {
		t.Fatalf("refused queries reached the provider: %v", cal.queries)
	}
	got, err := g.CalendarAvailability(context.Background(), many[:20], now, now.Add(62*24*time.Hour-time.Minute))
	if err != nil || len(got) != 20 || len(got[0].Slots) != 2976 {
		t.Fatalf("largest query: %d schedules err=%v", len(got), err)
	}
}

func TestCalendarAvailabilityProviderFailuresStaySafe(t *testing.T) {
	cal := &availabilityCalendar{err: model.MailboxError("CANARY raw provider text")}
	g, _, _ := availabilityGuard(t, cal)
	if got, err := g.CalendarAvailability(context.Background(), []string{"a@example.test"}, now, now.Add(time.Hour)); got != nil || err == nil || strings.Contains(err.Error(), "CANARY") {
		t.Fatalf("availability=%v err=%v", got, err)
	}
}
