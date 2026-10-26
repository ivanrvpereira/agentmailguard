package guard_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/audit"
	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

type calendarWriter struct {
	*calendar
	invites              []model.NewEvent
	changes              []model.EventChange
	ids                  []string
	comments             []string
	responses            []model.EventResponse
	err, getErr          error
	returnID             *string
	afterGet, afterWrite func()
}

func (w *calendarWriter) Get(ctx context.Context, id string) (model.Event, error) {
	e, err := w.calendar.Get(ctx, id)
	if w.afterGet != nil {
		w.afterGet()
	}
	if w.getErr != nil {
		return model.Event{}, w.getErr
	}
	return e, err
}
func (w *calendarWriter) Invite(_ context.Context, e model.NewEvent) (string, error) {
	w.invites = append(w.invites, e)
	if w.afterWrite != nil {
		w.afterWrite()
	}
	if w.returnID != nil {
		return *w.returnID, w.err
	}
	return "created", w.err
}
func (w *calendarWriter) Update(_ context.Context, id string, c model.EventChange) error {
	w.ids = append(w.ids, id)
	w.changes = append(w.changes, c)
	if w.afterWrite != nil {
		w.afterWrite()
	}
	return w.err
}
func (w *calendarWriter) Cancel(_ context.Context, id, comment string) error {
	w.ids = append(w.ids, id)
	w.comments = append(w.comments, comment)
	if w.afterWrite != nil {
		w.afterWrite()
	}
	return w.err
}
func (w *calendarWriter) Respond(_ context.Context, id string, r model.EventResponse) error {
	w.ids = append(w.ids, id)
	w.responses = append(w.responses, r)
	if w.afterWrite != nil {
		w.afterWrite()
	}
	return w.err
}

// calendarGuard returns a guard over mail b and calendar cal, its screener, and its audit path.
func calendarGuard(t *testing.T, b model.Mailbox, cal model.Calendar) (*guard.Guard, *eventScreener, string) {
	t.Helper()
	s := &eventScreener{}
	_, cache, path := setup(t, b, s)
	return guard.New("work", b, cal, s, cache, audit.New(path), guard.Options{Policy: allowAll}), s, path
}
func meetingWriter() *calendarWriter {
	return &calendarWriter{calendar: &calendar{event: model.Event{ID: "e1", Start: now, End: now.Add(time.Hour), IsOrganizer: true, Subject: "CANARY", Body: new("CANARY")}}}
}
func assertCalendarAudit(t *testing.T, path, operation, id string) {
	t.Helper()
	records := auditRecords(t, path)
	if len(records) != 1 {
		t.Fatalf("audit=%v", records)
	}
	if records[0]["operation"] != operation || !reflect.DeepEqual(records[0]["ids"], []any{id}) {
		t.Fatalf("audit=%v", records[0])
	}
}

func TestCalendarUpdateRequiresProviderAuthorityAndPreservesSelectedFields(t *testing.T) {
	for _, tc := range []struct {
		name, refusal string
		mutate        func(*model.Event)
	}{
		{"organizer", "", func(*model.Event) {}},
		{"spoofed address", "calendar write refused: only the organizer can update or cancel", func(e *model.Event) { e.IsOrganizer = false; e.Organizer = model.Address{Address: "work@example.test"} }},
		{"recurring", "calendar write refused: recurring events are not supported", func(e *model.Event) { e.Recurrence = new("CANARY") }},
		{"cancelled", "calendar write refused: event is cancelled", func(e *model.Event) { e.IsCancelled = true }},
		{"wrong id", "ValueError", func(e *model.Event) { e.ID = "unexpected" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := meetingWriter()
			tc.mutate(&w.event)
			g, _, path := calendarGuard(t, &mailbox{}, w)
			change := model.EventChange{Subject: new("CANARY new subject"), Location: new("")}
			err := g.CalendarUpdate(context.Background(), "e1", change, 0)
			if tc.refusal != "" {
				if err == nil || err.Error() != tc.refusal || len(w.changes) != 0 || len(auditRecords(t, path)) != 0 {
					t.Fatalf("refusal=%v changes=%v", err, w.changes)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(w.changes, []model.EventChange{change}) || !reflect.DeepEqual(w.ids, []string{"e1"}) {
				t.Fatalf("update=%v changes=%v ids=%v", err, w.changes, w.ids)
			}
			assertCalendarAudit(t, path, "calendar_update", "e1")
		})
	}
}

func TestCalendarUpdateResolvesOnlyRequestedTimeChanges(t *testing.T) {
	start := now.Add(2 * time.Hour)
	end := now.Add(3 * time.Hour)
	half := 30 * time.Minute
	for _, tc := range []struct {
		name     string
		change   model.EventChange
		duration time.Duration
		want     model.EventChange
	}{
		{"start preserves hour", model.EventChange{Start: &start}, 0, model.EventChange{Start: &start, End: &end}},
		{"duration uses old start", model.EventChange{}, half, model.EventChange{End: new(now.Add(30 * time.Minute))}},
		{"start and duration", model.EventChange{Start: &start}, half, model.EventChange{Start: &start, End: new(now.Add(150 * time.Minute))}},
		{"end only", model.EventChange{End: &end}, 0, model.EventChange{End: &end}},
		{"explicit range", model.EventChange{Start: &start, End: &end}, 0, model.EventChange{Start: &start, End: &end}},
		{"attendees only", model.EventChange{AddAttendees: []string{"CANARY@example.test"}}, 0, model.EventChange{AddAttendees: []string{"CANARY@example.test"}}},
		{"online only", model.EventChange{Online: true}, 0, model.EventChange{Online: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := meetingWriter()
			g, _, path := calendarGuard(t, &mailbox{}, w)
			if err := g.CalendarUpdate(context.Background(), "e1", tc.change, tc.duration); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(w.changes, []model.EventChange{tc.want}) {
				t.Fatalf("changes=%+v want=%+v", w.changes, tc.want)
			}
			assertCalendarAudit(t, path, "calendar_update", "e1")
		})
	}
}

func TestCalendarWritesRejectInvalidRangesAndChanges(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name             string
		change           model.EventChange
		duration         time.Duration
		oldStart, oldEnd *time.Time
	}{
		{name: "empty"},
		{name: "empty lists", change: model.EventChange{AddAttendees: []string{}, RemoveAttendees: []string{}}},
		{name: "negative duration", duration: -time.Hour},
		{name: "negative duration with change", change: model.EventChange{Subject: new("CANARY")}, duration: -time.Hour},
		{name: "end duration", change: model.EventChange{End: new(now.Add(time.Hour))}, duration: time.Hour},
		{name: "early end", change: model.EventChange{End: new(now)}},
		{name: "reversed range", change: model.EventChange{Start: new(now.Add(time.Hour)), End: new(now)}},
		{name: "year zero", change: model.EventChange{Start: new(time.Date(0, 1, 1, 0, 0, 0, 0, time.UTC))}},
		{name: "overflow year", change: model.EventChange{Start: new(time.Date(9999, 12, 31, 23, 45, 0, 0, time.UTC))}},
		{name: "UTC overflow", change: model.EventChange{End: new(time.Date(9999, 12, 31, 23, 45, 0, 0, time.FixedZone("west", -3600)))}},
		{name: "bad old duration", change: model.EventChange{Start: new(now)}, oldEnd: new(now.Add(-time.Hour))},
		{name: "duration saturation", change: model.EventChange{Start: new(now)}, oldStart: new(time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)), oldEnd: new(time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC))},
		{name: "overlapping attendee identities", change: model.EventChange{AddAttendees: []string{"CANARY@example.test"}, RemoveAttendees: []string{"canary@EXAMPLE.test"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := meetingWriter()
			if tc.oldStart != nil {
				w.event.Start = *tc.oldStart
			}
			if tc.oldEnd != nil {
				w.event.End = *tc.oldEnd
			}
			g, _, path := calendarGuard(t, &mailbox{}, w)
			err := g.CalendarUpdate(ctx, "e1", tc.change, tc.duration)
			if err == nil || err.Error() != "invalid request" || len(w.changes) != 0 || len(auditRecords(t, path)) != 0 {
				t.Fatalf("error=%v writes=%v", err, w.changes)
			}
		})
	}
	for _, end := range []time.Time{now, now.Add(-time.Hour), time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)} {
		w := meetingWriter()
		g, _, _ := calendarGuard(t, &mailbox{}, w)
		_, err := g.CalendarInvite(ctx, model.NewEvent{Start: now, End: end, Attendees: []string{"a@example.test"}})
		if err == nil || err.Error() != "invalid request" || len(w.invites) != 0 {
			t.Fatalf("invalid invite: %v", err)
		}
	}
}

func TestCalendarCancelAndRespondRequireCorrectRoleAndAuditPayloads(t *testing.T) {
	for _, action := range []string{"cancel", "accept", "tentative", "decline", "invalid"} {
		for _, state := range []string{"organizer", "attendee", "recurring", "cancelled", "wrong id"} {
			t.Run(action+"/"+state, func(t *testing.T) {
				w := meetingWriter()
				w.event.IsOrganizer = state == "organizer"
				switch state {
				case "recurring":
					w.event.Recurrence = new("CANARY")
				case "cancelled":
					w.event.IsCancelled = true
				case "wrong id":
					w.event.ID = "other"
				}
				g, _, path := calendarGuard(t, &mailbox{}, w)
				ctx := context.Background()
				var err error
				if action == "cancel" {
					err = g.CalendarCancel(ctx, "e1", "CANARY comment")
				} else {
					err = g.CalendarRespond(ctx, "e1", model.EventResponse{Response: model.MeetingResponse(action), Comment: "CANARY comment"})
				}
				allowed := action == "cancel" && state == "organizer" || action != "cancel" && action != "invalid" && state == "attendee"
				if !allowed {
					if err == nil || len(w.ids) != 0 || len(auditRecords(t, path)) != 0 {
						t.Fatalf("refused action wrote: %v %v", err, w.ids)
					}
					if action != "cancel" && action != "invalid" && state == "organizer" && err.Error() != "calendar response refused: organizer cannot respond" {
						t.Fatal(err)
					}
					if action == "invalid" && err.Error() != "invalid request" {
						t.Fatal(err)
					}
					return
				}
				if err != nil || !reflect.DeepEqual(w.ids, []string{"e1"}) {
					t.Fatalf("action=%v ids=%v", err, w.ids)
				}
				op := "calendar_respond"
				if action == "cancel" {
					op = "calendar_cancel"
					if !reflect.DeepEqual(w.comments, []string{"CANARY comment"}) {
						t.Fatal("lost comment")
					}
				} else if !reflect.DeepEqual(w.responses, []model.EventResponse{{Response: model.MeetingResponse(action), Comment: "CANARY comment"}}) {
					t.Fatal("lost response")
				}
				assertCalendarAudit(t, path, op, "e1")
			})
		}
	}
}

func callCalendarWrite(ctx context.Context, g *guard.Guard, op string) error {
	switch op {
	case "invite":
		_, err := g.CalendarInvite(ctx, model.NewEvent{Start: now, End: now.Add(time.Hour), Attendees: []string{"CANARY@example.test"}})
		return err
	case "update":
		return g.CalendarUpdate(ctx, "e1", model.EventChange{Subject: new("CANARY")}, 0)
	case "cancel":
		return g.CalendarCancel(ctx, "e1", "CANARY")
	default:
		return g.CalendarRespond(ctx, "e1", model.EventResponse{Response: "accept", Comment: "CANARY"})
	}
}
func TestCalendarWriteFailureAndCancellationBoundaries(t *testing.T) {
	for _, op := range []string{"invite", "update", "cancel", "respond"} {
		for _, mode := range []string{"readonly", "cancel before", "cancel after fetch", "cancel after success", "provider failure", "fetch failure", "audit failure"} {
			if op == "invite" && (mode == "cancel after fetch" || mode == "fetch failure") {
				continue
			}
			t.Run(op+"/"+mode, func(t *testing.T) {
				w := meetingWriter()
				w.event.IsOrganizer = op != "respond"
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var cal model.Calendar = w
				want := ""
				written, canceled := false, false
				switch mode {
				case "readonly":
					cal = w.calendar
					want = "calendar writes are not supported for this account"
				case "cancel before":
					cancel()
					canceled = true
				case "cancel after fetch":
					w.afterGet = cancel
					canceled = true
				case "cancel after success":
					w.afterWrite = cancel
					written = true
				case "provider failure":
					w.err = errors.New("CANARY")
					want = "Error"
				case "fetch failure":
					w.getErr = guard.GuardError("calendar write refused: event is cancelled CANARY")
					want = "GuardError"
				case "audit failure":
					want = "AuditError"
					written = true
				}
				g, _, path := calendarGuard(t, &mailbox{}, cal)
				if mode == "audit failure" {
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				}
				err := callCalendarWrite(ctx, g, op)
				if canceled && !errors.Is(err, context.Canceled) || !canceled && (want == "" && err != nil || want != "" && (err == nil || err.Error() != want)) {
					t.Fatalf("error=%v want=%q canceled=%t", err, want, canceled)
				}
				attempts := len(w.invites) + len(w.ids)
				if written || mode == "provider failure" {
					if attempts != 1 {
						t.Fatalf("write attempts=%d", attempts)
					}
				} else if attempts != 0 {
					t.Fatalf("forbidden writes=%d", attempts)
				}
				if mode == "readonly" && w.calls != 0 {
					t.Fatal("read-only provider accessed")
				}
				if mode != "audit failure" {
					records := auditRecords(t, path)
					if written && len(records) != 1 || !written && len(records) != 0 {
						t.Fatalf("audit=%v", records)
					}
				}
			})
		}
	}
	w := meetingWriter()
	w.returnID = new("")
	g, _, path := calendarGuard(t, &mailbox{}, w)
	if err := callCalendarWrite(context.Background(), g, "invite"); err == nil || err.Error() != "ValueError" || len(auditRecords(t, path)) != 0 {
		t.Fatalf("empty created ID=%v", err)
	}
}

func TestCalendarWritesShareMutationGateWithMail(t *testing.T) {
	for _, op := range []string{"invite", "update", "cancel", "respond"} {
		t.Run(op, func(t *testing.T) {
			w := &writer{mailbox: &mailbox{}, entered: make(chan struct{}), release: make(chan struct{})}
			cal := meetingWriter()
			cal.event.IsOrganizer = op != "respond"
			g, _, _ := calendarGuard(t, w, cal)
			done := make(chan error, 1)
			go func() { _, err := g.Archive(context.Background(), []string{"m1"}); done <- err }()
			<-w.entered
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			err := callCalendarWrite(ctx, g, op)
			close(w.release)
			if e := <-done; e != nil {
				t.Fatal(e)
			}
			if !errors.Is(err, context.DeadlineExceeded) || len(cal.invites)+len(cal.ids)+cal.calls != 0 {
				t.Fatalf("queued operation escaped gate: %v", err)
			}
		})
	}
}

func TestCalendarWritesHoldMutationGateDuringFetchAndWrite(t *testing.T) {
	for _, op := range []string{"invite", "update", "cancel", "respond"} {
		for _, phase := range []string{"fetch", "write"} {
			if op == "invite" && phase == "fetch" {
				continue
			}
			t.Run(op+"/"+phase, func(t *testing.T) {
				mail := &writer{mailbox: &mailbox{}}
				cal := meetingWriter()
				cal.event.IsOrganizer = op != "respond"
				g, _, path := calendarGuard(t, mail, cal)
				checked := false
				check := func() {
					checked = true
					ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
					defer cancel()
					if _, err := g.Archive(ctx, []string{"m1"}); !errors.Is(err, context.DeadlineExceeded) {
						t.Errorf("mail mutation entered during calendar %s: %v", phase, err)
					}
				}
				if phase == "fetch" {
					cal.afterGet = check
				} else {
					cal.afterWrite = check
				}
				if err := callCalendarWrite(context.Background(), g, op); err != nil {
					t.Fatal(err)
				}
				if !checked || len(mail.calls) != 0 || len(cal.invites)+len(cal.ids) != 1 || len(auditRecords(t, path)) != 1 {
					t.Fatal("calendar write did not exclude the competing mail mutation")
				}
			})
		}
	}
}

func TestCalendarInvitePreservesPayloadAndAuditsWithoutReading(t *testing.T) {
	w := meetingWriter()
	g, _, path := calendarGuard(t, &mailbox{}, w)
	event := model.NewEvent{Subject: "CANARY subject", Body: "CANARY body", Location: "CANARY location", Attendees: []string{"CANARY@example.test"}, Optional: []string{"optional@example.test"}, Start: now, End: now.Add(9 * time.Hour), Online: true}
	id, err := g.CalendarInvite(context.Background(), event)
	if err != nil || id != "created" || !reflect.DeepEqual(w.invites, []model.NewEvent{event}) || w.calls != 0 {
		t.Fatalf("invite = %q %v, payloads=%v reads=%d", id, err, w.invites, w.calls)
	}
	assertCalendarAudit(t, path, "calendar_invite", "created")
}

// storedArgs decodes the request arguments of a pending item.
func storedArgs(t *testing.T, item cache.Pending) map[string]any {
	t.Helper()
	var args map[string]any
	if err := json.Unmarshal(item.Args, &args); err != nil {
		t.Fatal(err)
	}
	return args
}

// Optional attendees receive the invitation, so Jev and the approval see them beside the required ones.
func TestCalendarInviteOptionalAttendeesReachJevAndTheStoredAction(t *testing.T) {
	s := scoring(0.08)
	policy := allowAll
	policy.Base.CalendarInvite = config.Approve
	w := meetingWriter()
	g, c, _ := policyGuard(t, &mailbox{}, w, s, policy)
	_, err := g.CalendarInvite(t.Context(), model.NewEvent{Subject: "Plan", Start: now, End: now.Add(time.Hour), Attendees: []string{"a@example.test"}, Optional: []string{"b@example.test"}})
	var pending guard.PendingError
	if !errors.As(err, &pending) || len(w.invites) != 0 {
		t.Fatalf("invite=%v writes=%v", err, w.invites)
	}
	if checks := s.checked(); len(checks) != 1 || !reflect.DeepEqual(checks[0].state["optional"], []any{"b@example.test"}) {
		t.Fatalf("checks=%+v", checks)
	}
	item, err := c.GetPending(t.Context(), pending.ApprovalID)
	if err != nil || !reflect.DeepEqual(storedArgs(t, item)["optional"], []any{"b@example.test"}) {
		t.Fatalf("stored=%s err=%v", item.Args, err)
	}
}

func TestCalendarInviteRefusesAnAddressThatIsRequiredAndOptional(t *testing.T) {
	w := meetingWriter()
	g, _, path := calendarGuard(t, &mailbox{}, w)
	_, err := g.CalendarInvite(t.Context(), model.NewEvent{Start: now, End: now.Add(time.Hour), Attendees: []string{"Ana@example.test"}, Optional: []string{"bob@example.test", "ana@EXAMPLE.test"}})
	if err == nil || err.Error() != "invalid request" || len(w.invites) != 0 || len(auditRecords(t, path)) != 0 {
		t.Fatalf("invite=%v writes=%v", err, w.invites)
	}
}

// A body-only update is a change. Jev and the approval see the new body.
func TestCalendarUpdateBodyReachesJevAndTheStoredAction(t *testing.T) {
	s := scoring(0.08)
	policy := allowAll
	policy.Base.CalendarUpdate = config.Approve
	w := meetingWriter()
	g, c, _ := policyGuard(t, &mailbox{}, w, s, policy)
	err := g.CalendarUpdate(t.Context(), "e1", model.EventChange{Body: ptr("New agenda")}, 0)
	var pending guard.PendingError
	if !errors.As(err, &pending) || len(w.changes) != 0 {
		t.Fatalf("update=%v writes=%v", err, w.changes)
	}
	if checks := s.checked(); len(checks) != 1 || !reflect.DeepEqual(checks[0].state, map[string]any{"body": "New agenda"}) {
		t.Fatalf("checks=%+v", checks)
	}
	item, err := c.GetPending(t.Context(), pending.ApprovalID)
	if err != nil || !reflect.DeepEqual(storedArgs(t, item), map[string]any{"id": "e1", "body": "New agenda"}) {
		t.Fatalf("stored=%s err=%v", item.Args, err)
	}
}

// A response without notification carries no agent text, so Jev does not check it; the approval sees no_notify.
func TestCalendarRespondNoNotifyReachesTheStoredAction(t *testing.T) {
	s := scoring(0.08)
	policy := allowAll
	policy.Base.CalendarRespond = config.Approve
	w := meetingWriter()
	w.event.IsOrganizer = false
	g, c, _ := policyGuard(t, &mailbox{}, w, s, policy)
	err := g.CalendarRespond(t.Context(), "e1", model.EventResponse{Response: model.ResponseDecline, NoNotify: true})
	var pending guard.PendingError
	if !errors.As(err, &pending) || len(w.responses) != 0 || len(s.checked()) != 0 {
		t.Fatalf("respond=%v writes=%v checks=%+v", err, w.responses, s.checked())
	}
	item, err := c.GetPending(t.Context(), pending.ApprovalID)
	if err != nil || !reflect.DeepEqual(storedArgs(t, item), map[string]any{"id": "e1", "response": "decline", "comment": "", "no_notify": true}) {
		t.Fatalf("stored=%s err=%v", item.Args, err)
	}
}

// A comment would reach nobody without a notification.
func TestCalendarRespondRefusesACommentWithoutNotification(t *testing.T) {
	w := meetingWriter()
	w.event.IsOrganizer = false
	g, _, path := calendarGuard(t, &mailbox{}, w)
	err := g.CalendarRespond(t.Context(), "e1", model.EventResponse{Response: model.ResponseAccept, Comment: "CANARY", NoNotify: true})
	if err == nil || err.Error() != "invalid request" || len(w.responses) != 0 || len(auditRecords(t, path)) != 0 {
		t.Fatalf("respond=%v writes=%v", err, w.responses)
	}
	if err := g.CalendarRespond(t.Context(), "e1", model.EventResponse{Response: model.ResponseAccept, NoNotify: true}); err != nil || !reflect.DeepEqual(w.responses, []model.EventResponse{{Response: model.ResponseAccept, NoNotify: true}}) {
		t.Fatalf("respond=%v writes=%v", err, w.responses)
	}
}
