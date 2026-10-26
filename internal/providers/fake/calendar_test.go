package fake_test

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/fake"
)

// calendar seeds the demo account with meetings that it organizes (mine, solo), a meeting that Ana
// organizes (ana), a cancelled meeting (gone), a meeting outside the week (later), and a meeting of
// another account.
func calendar(t *testing.T) (model.Calendar, string) {
	t.Helper()
	home := t.TempDir()
	seed(t, home, `INSERT INTO events (account, id, subject, body, content_type, location, start_at, end_at, organizer_name, organizer_address, join_url, is_private, show_as, categories, attachments) VALUES
		('demo', 'mine', 'Planning', '<p>Agenda</p>', 'html', 'Room 1', '2026-04-02T09:00:00Z', '2026-04-02T10:00:00Z', 'Demo', 'DEMO@fake.test', 'https://meet.example.test/x', 1, 'tentative', '["Work"]', '["agenda.pdf"]')`,
		`INSERT INTO events (account, id, subject, start_at, end_at) VALUES ('demo', 'solo', 'Focus', '2026-04-01T08:00:00Z', '2026-04-01T09:00:00Z'), ('demo', 'later', 'Later', '2026-04-20T08:00:00Z', '2026-04-20T09:00:00Z'), ('other', 'theirs', 'Theirs', '2026-04-02T08:00:00Z', '2026-04-02T09:00:00Z')`,
		`INSERT INTO events (account, id, subject, start_at, end_at, organizer_name, organizer_address, recurrence) VALUES ('demo', 'ana', 'Lunch', '2026-04-03T13:00:00+02:00', '2026-04-03T14:00:00+02:00', 'Ana', 'ana@example.test', 'weekly')`,
		`INSERT INTO events (account, id, subject, start_at, end_at, is_cancelled, cancel_comment) VALUES ('demo', 'gone', 'Old', '2026-04-04T08:00:00Z', '2026-04-04T09:00:00Z', 1, 'Sorry')`,
		`INSERT INTO attendees (account, event_id, address, name, response) VALUES ('demo', 'mine', 'ana@example.test', 'Ana', 'accepted'), ('demo', 'mine', 'bob@example.test', '', 'none'), ('demo', 'ana', 'demo@fake.test', '', 'tentativelyAccepted')`)
	return fake.New("demo", home, func() time.Time { return now }).Calendar(), home
}

func eventIDs(events []model.Event) []string {
	out := []string{}
	for _, e := range events {
		out = append(out, e.ID)
	}
	return out
}

func TestCalendarListsEventsInTheRangeByStartAndGetsOne(t *testing.T) {
	c, _ := calendar(t)
	ctx := context.Background()
	start, end := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 4, 8, 0, 0, 0, 0, time.UTC)
	got, err := c.List(ctx, start, end)
	if err != nil || !reflect.DeepEqual(eventIDs(got), []string{"solo", "mine", "ana", "gone"}) {
		t.Fatalf("ids=%v err=%v", eventIDs(got), err)
	}
	// An event that ends at the range start, or starts at the range end, is outside the range.
	if got, err := c.List(ctx, time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC), time.Date(2026, 4, 2, 9, 0, 0, 0, time.UTC)); err != nil || len(got) != 0 {
		t.Fatalf("edges=%v err=%v", eventIDs(got), err)
	}
	if _, err := c.List(ctx, end, start); err == nil {
		t.Fatal("empty range accepted")
	}
	body, kind, join := "<p>Agenda</p>", model.HTML, "https://meet.example.test/x"
	want := model.Event{ID: "mine", Subject: "Planning", Start: time.Date(2026, 4, 2, 9, 0, 0, 0, time.UTC), End: time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC), Location: "Room 1", Organizer: model.Address{Name: "Demo", Address: "DEMO@fake.test"}, IsOrganizer: true,
		Attendees: []model.Attendee{{Address: model.Address{Name: "Ana", Address: "ana@example.test"}, Response: "accepted"}, {Address: model.Address{Address: "bob@example.test"}, Response: "none"}},
		JoinURL:   &join, Attachments: []string{"agenda.pdf"}, IsPrivate: true, Categories: []string{"Work"}, ShowAs: "tentative", Body: &body, ContentType: &kind}
	if !reflect.DeepEqual(got[1], want) {
		t.Fatalf("listed=%+v", got[1])
	}
	if one, err := c.Get(ctx, "mine"); err != nil || !reflect.DeepEqual(one, want) {
		t.Fatalf("get=%+v err=%v", one, err)
	}
	text, plain := "", model.Text
	solo, err := c.Get(ctx, "solo")
	if want := (model.Event{ID: "solo", Subject: "Focus", Start: time.Date(2026, 4, 1, 8, 0, 0, 0, time.UTC), End: time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC), Organizer: model.Address{Address: "demo@fake.test"}, IsOrganizer: true, Attendees: []model.Attendee{}, Attachments: []string{}, Categories: []string{}, ShowAs: "busy", Body: &text, ContentType: &plain}); err != nil || !reflect.DeepEqual(solo, want) {
		t.Fatalf("defaults=%+v err=%v", solo, err)
	}
	ana, err := c.Get(ctx, "ana")
	if err != nil || ana.IsOrganizer || ana.Organizer.Address != "ana@example.test" || ana.Recurrence == nil || *ana.Recurrence != "weekly" || !ana.Start.Equal(time.Date(2026, 4, 3, 11, 0, 0, 0, time.UTC)) {
		t.Fatalf("ana=%+v err=%v", ana, err)
	}
	if gone, err := c.Get(ctx, "gone"); err != nil || !gone.IsCancelled {
		t.Fatalf("gone=%+v err=%v", gone, err)
	}
	for _, id := range []string{"missing", "theirs"} {
		if _, err := c.Get(ctx, id); err == nil {
			t.Errorf("get %s accepted", id)
		}
	}
}

func writer(t *testing.T, c model.Calendar) model.CalendarWriter {
	t.Helper()
	w, ok := c.(model.CalendarWriter)
	if !ok {
		t.Fatal("the fake calendar does not write")
	}
	return w
}

func TestInviteStoresAnEventThatTheAccountOrganizes(t *testing.T) {
	c, _ := calendar(t)
	ctx := context.Background()
	start, end := time.Date(2026, 4, 6, 9, 0, 0, 0, time.FixedZone("x", 3600)), time.Date(2026, 4, 6, 9, 30, 0, 0, time.FixedZone("x", 3600))
	id, err := writer(t, c).Invite(ctx, model.NewEvent{Subject: "Review", Body: "Bring <notes>\nand coffee", Location: "Room 2", Start: start, End: end, Attendees: []string{"ana@example.test", "bob@example.test"}, Online: true})
	if err != nil || len(id) < 10 || id[:5] != "fake-" {
		t.Fatalf("id=%q err=%v", id, err)
	}
	got, err := c.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	body, kind, join := "Bring &lt;notes&gt;<br>and coffee", model.HTML, "https://meet.fake.test/"+id
	want := model.Event{ID: id, Subject: "Review", Start: time.Date(2026, 4, 6, 8, 0, 0, 0, time.UTC), End: time.Date(2026, 4, 6, 8, 30, 0, 0, time.UTC), Location: "Room 2", Organizer: model.Address{Address: "demo@fake.test"}, IsOrganizer: true,
		Attendees: []model.Attendee{{Address: model.Address{Address: "ana@example.test"}, Response: "none"}, {Address: model.Address{Address: "bob@example.test"}, Response: "none"}},
		JoinURL:   &join, Attachments: []string{}, Categories: []string{}, ShowAs: "busy", Body: &body, ContentType: &kind}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("event=%+v", got)
	}
	offline, err := writer(t, c).Invite(ctx, model.NewEvent{Subject: "Call", Start: start, End: end, Attendees: []string{"ana@example.test"}})
	if err != nil || offline == id {
		t.Fatalf("id=%q err=%v", offline, err)
	}
	if got, err := c.Get(ctx, offline); err != nil || got.JoinURL != nil {
		t.Fatalf("offline=%+v err=%v", got, err)
	}
	if listed, err := c.List(ctx, start, end); err != nil || len(listed) != 2 {
		t.Fatalf("listed=%v err=%v", eventIDs(listed), err)
	}
}

func TestUpdateChangesOnlyEventsThatTheAccountOrganizes(t *testing.T) {
	c, _ := calendar(t)
	ctx := context.Background()
	w := writer(t, c)
	subject, location := "Planning v2", ""
	start, end := time.Date(2026, 4, 2, 11, 0, 0, 0, time.UTC), time.Date(2026, 4, 2, 11, 45, 0, 0, time.UTC)
	if err := w.Update(ctx, "mine", model.EventChange{Subject: &subject, Location: &location, Start: &start, End: &end, AddAttendees: []string{"ANA@example.test", "carla@example.test"}, RemoveAttendees: []string{"BOB@example.test"}}); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx, "mine")
	wantAttendees := []model.Attendee{{Address: model.Address{Name: "Ana", Address: "ana@example.test"}, Response: "accepted"}, {Address: model.Address{Address: "carla@example.test"}, Response: "none"}}
	if err != nil || got.Subject != subject || got.Location != "" || !got.Start.Equal(start) || !got.End.Equal(end) || !reflect.DeepEqual(got.Attendees, wantAttendees) || *got.Body != "<p>Agenda</p>" || *got.JoinURL != "https://meet.example.test/x" {
		t.Fatalf("event=%+v err=%v", got, err)
	}
	// Online adds a link only to an event without one.
	for _, id := range []string{"mine", "solo"} {
		if err := w.Update(ctx, id, model.EventChange{Online: true}); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := c.Get(ctx, "mine"); err != nil || *got.JoinURL != "https://meet.example.test/x" || got.Subject != subject {
		t.Fatalf("mine=%+v err=%v", got, err)
	}
	if got, err := c.Get(ctx, "solo"); err != nil || got.JoinURL == nil || *got.JoinURL != "https://meet.fake.test/solo" || got.Subject != "Focus" {
		t.Fatalf("solo=%+v err=%v", got, err)
	}
	for _, id := range []string{"ana", "gone", "missing", "theirs"} {
		if err := w.Update(ctx, id, model.EventChange{Subject: &subject}); err == nil {
			t.Errorf("update of %s accepted", id)
		}
	}
	if got, err := c.Get(ctx, "ana"); err != nil || got.Subject != "Lunch" {
		t.Fatalf("ana=%+v err=%v", got, err)
	}
}

func TestCancelMarksTheEventCancelledAndKeepsItsRows(t *testing.T) {
	c, home := calendar(t)
	ctx := context.Background()
	w := writer(t, c)
	if err := w.Cancel(ctx, "mine", "Moved to next week"); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx, "mine")
	if err != nil || !got.IsCancelled || got.Subject != "Planning" || len(got.Attendees) != 2 {
		t.Fatalf("event=%+v err=%v", got, err)
	}
	var comment string
	if err := open(t, home).QueryRow(`SELECT cancel_comment FROM events WHERE account = 'demo' AND id = 'mine'`).Scan(&comment); err != nil || comment != "Moved to next week" {
		t.Fatalf("comment=%q err=%v", comment, err)
	}
	for _, id := range []string{"mine", "ana", "missing", "theirs"} {
		if err := w.Cancel(ctx, id, ""); err == nil {
			t.Errorf("cancel of %s accepted", id)
		}
	}
	if got, err := c.Get(ctx, "ana"); err != nil || got.IsCancelled {
		t.Fatalf("ana=%+v err=%v", got, err)
	}
}

func TestRespondRecordsTheAccountsOwnResponseOnOthersEvents(t *testing.T) {
	c, home := calendar(t)
	ctx := context.Background()
	w := writer(t, c)
	seed(t, home, `INSERT INTO identities (account, address, name) VALUES ('demo', 'Demo@Fake.Test', 'Demo User')`,
		`INSERT INTO events (account, id, subject, start_at, end_at, organizer_address) VALUES ('demo', 'bob', 'Sync', '2026-04-05T08:00:00Z', '2026-04-05T09:00:00Z', 'bob@example.test')`,
		`INSERT INTO events (account, id, subject, start_at, end_at, organizer_address, is_cancelled) VALUES ('demo', 'dead', 'Dead', '2026-04-05T08:00:00Z', '2026-04-05T09:00:00Z', 'bob@example.test', 1)`)
	if err := w.Respond(ctx, "ana", model.EventResponse{Response: model.ResponseDecline, Comment: "Out that day"}); err != nil {
		t.Fatal(err)
	}
	if err := w.Respond(ctx, "bob", model.EventResponse{Response: model.ResponseTentative}); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Get(ctx, "ana"); err != nil || !reflect.DeepEqual(got.Attendees, []model.Attendee{{Address: model.Address{Address: "demo@fake.test"}, Response: "declined"}}) {
		t.Fatalf("ana=%+v err=%v", got, err)
	}
	if got, err := c.Get(ctx, "bob"); err != nil || !reflect.DeepEqual(got.Attendees, []model.Attendee{{Address: model.Address{Name: "Demo User", Address: "Demo@Fake.Test"}, Response: "tentativelyAccepted"}}) {
		t.Fatalf("bob=%+v err=%v", got, err)
	}
	if err := w.Respond(ctx, "bob", model.EventResponse{Response: model.ResponseAccept, Comment: "See you"}); err != nil {
		t.Fatal(err)
	}
	rows, err := open(t, home).Query(`SELECT event_id, response, coalesce(comment, '-') FROM attendees WHERE account = 'demo' AND event_id IN ('ana', 'bob') ORDER BY event_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := [][3]string{}
	for rows.Next() {
		var r [3]string
		if err := rows.Scan(&r[0], &r[1], &r[2]); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if want := [][3]string{{"ana", "declined", "Out that day"}, {"bob", "accepted", "See you"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rows=%v", got)
	}
	for _, id := range []string{"mine", "solo", "dead", "missing", "theirs"} {
		if err := w.Respond(ctx, id, model.EventResponse{Response: model.ResponseAccept}); err == nil {
			t.Errorf("response to %s accepted", id)
		}
	}
}

func TestAvailabilityComesOnlyFromFreeBusyRows(t *testing.T) {
	c, home := calendar(t)
	ctx := context.Background()
	seed(t, home, `INSERT INTO free_busy (account, address, start_at, end_at, status) VALUES
		('demo', 'ana@example.test', '2026-04-02T09:00:00Z', '2026-04-02T10:00:00Z', 'busy'),
		('demo', 'ana@example.test', '2026-04-02T11:15:00+02:00', '2026-04-02T11:45:00+02:00', 'tentative'),
		('demo', 'ana@example.test', '2026-04-02T10:15:00Z', '2026-04-02T10:20:00Z', 'tentative'),
		('demo', 'ana@example.test', '2026-04-02T10:00:00Z', '2026-04-02T11:15:00Z', 'free'),
		('demo', 'ana@example.test', '2026-04-02T10:45:00Z', '2026-04-02T11:00:00Z', 'out_of_office'),
		('demo', 'bob@example.test', '2026-04-03T09:00:00Z', '2026-04-03T10:00:00Z', 'busy'),
		('demo', 'bob@example.test', '2026-04-02T11:15:00Z', '2026-04-02T11:30:00Z', 'busy'),
		('other', 'bob@example.test', '2026-04-02T09:00:00Z', '2026-04-02T12:00:00Z', 'busy'),
		('demo', 'carla@example.test', '2026-04-02T09:00:00Z', '2026-04-02T10:00:00Z', 'busy'),
		('demo', 'carla@example.test', 'soon', '2026-04-02T10:00:00Z', 'busy'),
		('demo', 'dave@example.test', '2026-04-02T10:00:00Z', '2026-04-02T09:00:00Z', 'busy')`)
	reader, ok := c.(model.CalendarAvailability)
	if !ok {
		t.Fatal("the fake calendar has no availability")
	}
	// Five slots: 09:00, 09:30, 10:00, 10:30, and 11:00 until 11:15. demo@fake.test has the event mine at
	// 09:00 but no free_busy rows. bob is busy only after the end. carla has a row that cannot be read, and
	// dave has a row that ends before it starts.
	start, end := time.Date(2026, 4, 2, 9, 0, 0, 0, time.UTC), time.Date(2026, 4, 2, 11, 15, 0, 0, time.UTC)
	got, err := reader.Availability(ctx, []string{"Ana@Example.test", "nobody@example.test", "bob@example.test", "demo@fake.test", "carla@example.test", "dave@example.test"}, start, end)
	unknown := []model.AvailabilityStatus{"unknown", "unknown", "unknown", "unknown", "unknown"}
	want := []model.Schedule{
		{Address: "Ana@Example.test", Slots: []model.AvailabilityStatus{"busy", "busy", "tentative", "out_of_office", "free"}},
		{Address: "nobody@example.test", Slots: unknown},
		{Address: "bob@example.test", Slots: []model.AvailabilityStatus{"free", "free", "free", "free", "free"}},
		{Address: "demo@fake.test", Slots: unknown},
		{Address: "carla@example.test", Slots: unknown},
		{Address: "dave@example.test", Slots: unknown},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("schedules=%v err=%v", got, err)
	}
	if _, err := reader.Availability(ctx, []string{"ana@example.test", "ANA@example.test"}, start, end); err == nil {
		t.Fatal("duplicate addresses accepted")
	}
}

// optionalFlags maps each attendee address of event id to its optional column.
func optionalFlags(t *testing.T, home, id string) map[string]bool {
	t.Helper()
	rows, err := open(t, home).Query(`SELECT address, optional FROM attendees WHERE account = 'demo' AND event_id = ?`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var address string
		var optional bool
		if err := rows.Scan(&address, &optional); err != nil {
			t.Fatal(err)
		}
		out[address] = optional
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestInviteStoresOptionalAttendeesAsOptional(t *testing.T) {
	c, home := calendar(t)
	start := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	id, err := writer(t, c).Invite(t.Context(), model.NewEvent{Subject: "Review", Start: start, End: start.Add(time.Hour), Attendees: []string{"ana@example.test"}, Optional: []string{"bob@example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := optionalFlags(t, home, id); !reflect.DeepEqual(got, map[string]bool{"ana@example.test": false, "bob@example.test": true}) {
		t.Fatalf("optional=%v", got)
	}
	if got := optionalFlags(t, home, "mine"); !reflect.DeepEqual(got, map[string]bool{"ana@example.test": false, "bob@example.test": false}) {
		t.Fatalf("seeded optional=%v", got)
	}
}

// A fake-provider.db made before attendees had optional gets the column on the first open.
func TestDatabaseFromBeforeOptionalAttendeesOpensAndKeepsItsAttendees(t *testing.T) {
	home := t.TempDir()
	schema, err := os.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	before := strings.Replace(string(schema), "\toptional INTEGER NOT NULL DEFAULT 0 CHECK (optional IN (0, 1)),\n", "", 1)
	if before == string(schema) {
		t.Fatal("schema.sql has no optional column")
	}
	db := open(t, home)
	for _, s := range []string{before,
		`INSERT INTO events (account, id, subject, start_at, end_at) VALUES ('demo', 'old', 'Old', '2026-04-02T09:00:00Z', '2026-04-02T10:00:00Z')`,
		`INSERT INTO attendees (account, event_id, address) VALUES ('demo', 'old', 'ana@example.test')`} {
		if _, err := db.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	c := fake.New("demo", home, func() time.Time { return now }).Calendar()
	ctx := t.Context()
	if got, err := c.Get(ctx, "old"); err != nil || len(got.Attendees) != 1 {
		t.Fatalf("old=%+v err=%v", got, err)
	}
	start := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	id, err := writer(t, c).Invite(ctx, model.NewEvent{Subject: "Review", Start: start, End: start.Add(time.Hour), Optional: []string{"bob@example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := optionalFlags(t, home, "old"); !reflect.DeepEqual(got, map[string]bool{"ana@example.test": false}) {
		t.Fatalf("old optional=%v", got)
	}
	if got := optionalFlags(t, home, id); !reflect.DeepEqual(got, map[string]bool{"bob@example.test": true}) {
		t.Fatalf("new optional=%v", got)
	}
}

// A new body is stored as escaped HTML, as an invitation body is, and replaces the old one.
func TestUpdateStoresANewBodyAsEscapedHTML(t *testing.T) {
	c, _ := calendar(t)
	ctx := t.Context()
	body := "Bring <notes>\nand coffee"
	if err := writer(t, c).Update(ctx, "solo", model.EventChange{Body: &body}); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(ctx, "solo")
	if err != nil || *got.Body != "Bring &lt;notes&gt;<br>and coffee" || *got.ContentType != model.HTML || got.Subject != "Focus" {
		t.Fatalf("solo=%+v err=%v", got, err)
	}
}

func TestCalendarSearchMatchesEventTextAndPeopleByStart(t *testing.T) {
	c, _ := calendar(t)
	ctx := context.Background()
	for text, want := range map[string][]string{
		"planning":         {"mine"},        // subject, ignoring case
		"agenda":           {"mine"},        // body
		"room 1":           {"mine"},        // location
		"ana":              {"mine", "ana"}, // attendee name, organizer name and address
		"BOB@example.test": {"mine"},        // attendee address
		"o":                {"solo", "mine", "ana", "gone", "later"},
		"theirs":           {}, // another account's event
		"nothing":          {},
	} {
		got, err := c.Search(ctx, text)
		if err != nil || !reflect.DeepEqual(eventIDs(got), want) {
			t.Errorf("%q: ids=%v err=%v", text, eventIDs(got), err)
		}
	}
	got, err := c.Search(ctx, "planning")
	if one, _ := c.Get(ctx, "mine"); err != nil || len(got) != 1 || !reflect.DeepEqual(got[0], one) {
		t.Fatalf("search=%+v err=%v", got, err)
	}
}
