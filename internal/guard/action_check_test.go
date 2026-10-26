package guard_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

type actionCall struct {
	operation, effect string
	state             map[string]any
}

// actionScreener is a fake Jev: it screens messages like subjectScreener and answers every action check with check.
type actionScreener struct {
	*subjectScreener
	mu    sync.Mutex
	check model.ActionCheck
	err   error
	calls []actionCall
}

func (s *actionScreener) CheckAction(_ context.Context, operation, effect string, state map[string]any) (model.ActionCheck, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, actionCall{operation, effect, state})
	return s.check, s.err
}
func (s *actionScreener) checked() []actionCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]actionCall{}, s.calls...)
}
func scoring(score float64) *actionScreener {
	return &actionScreener{subjectScreener: &subjectScreener{}, check: model.ActionScore(score)}
}

func TestJevSeesOnlyTheAgentFieldsOfEachAction(t *testing.T) {
	start := now.Add(24 * time.Hour)
	for _, tc := range []struct {
		name string
		call func(context.Context, *guard.Guard) error
		want actionCall
	}{
		{"draft.new", func(ctx context.Context, g *guard.Guard) error {
			_, err := g.DraftNew(ctx, model.NewDraft{To: []string{"a@example.test"}, Subject: "Lunch", Body: "See you"})
			return err
		}, actionCall{"draft.new", "creates a draft that the user may send later", map[string]any{"to": []any{"a@example.test"}, "subject": "Lunch", "body": "See you"}}},
		{"draft.reply", func(ctx context.Context, g *guard.Guard) error {
			_, err := g.DraftReply(ctx, "m1", "Thanks")
			return err
		},
			actionCall{"draft.reply", "creates a draft that the user may send later", map[string]any{"body": "Thanks"}}},
		{"draft.update", func(ctx context.Context, g *guard.Guard) error {
			_, err := g.DraftUpdate(ctx, "d1", "New body")
			return err
		},
			actionCall{"draft.update", "creates a draft that the user may send later", map[string]any{"body": "New body"}}},
		{"draft.forward", func(ctx context.Context, g *guard.Guard) error {
			_, err := g.DraftForward(ctx, "m1", []string{"b@example.test"})
			return err
		}, actionCall{"draft.forward", "creates a draft that the user may send later", map[string]any{"to": []any{"b@example.test"}, "forwarded_message": map[string]any{"subject": "m1", "body": "BODYTEXT [L1]"}}}},
		{"calendar.invite", func(ctx context.Context, g *guard.Guard) error {
			_, err := g.CalendarInvite(ctx, model.NewEvent{Subject: "Plan", Body: "Agenda", Location: "Room 1", Start: start, End: start.Add(time.Hour), Attendees: []string{"a@example.test"}, Online: true})
			return err
		}, actionCall{"calendar.invite", "sends meeting invitations to the attendees", map[string]any{"to": []any{"a@example.test"}, "subject": "Plan", "body": "Agenda", "location": "Room 1", "start": "2026-09-29T15:30:00Z", "end": "2026-09-29T16:30:00Z", "online": true}}},
		{"calendar.update", func(ctx context.Context, g *guard.Guard) error {
			return g.CalendarUpdate(ctx, "e1", model.EventChange{Subject: new("Moved"), Start: &start, AddAttendees: []string{"c@example.test"}}, 30*time.Minute)
		}, actionCall{"calendar.update", "changes a meeting and notifies the attendees", map[string]any{"subject": "Moved", "start": "2026-09-29T15:30:00Z", "duration": "30m0s", "add": []any{"c@example.test"}}}},
		{"calendar.respond", func(ctx context.Context, g *guard.Guard) error {
			return g.CalendarRespond(ctx, "e1", model.EventResponse{Response: model.ResponseDecline, Comment: "Running late"})
		}, actionCall{"calendar.respond", "sends a response to the meeting organizer", map[string]any{"response": "decline", "comment": "Running late"}}},
		{"calendar.cancel", func(ctx context.Context, g *guard.Guard) error { return g.CalendarCancel(ctx, "e1", "Sorry") },
			actionCall{"calendar.cancel", "cancels a meeting and notifies the attendees", map[string]any{"comment": "Sorry"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cal := meetingWriter()
			cal.event.IsOrganizer = tc.name != "calendar.respond"
			s := scoring(0.08)
			g, _, _ := policyGuard(t, &writer{mailbox: &mailbox{messages: messages("m1")}}, cal, s, allowAll)
			if err := tc.call(context.Background(), g); err != nil {
				t.Fatal(err)
			}
			if got := s.checked(); len(got) != 1 || !reflect.DeepEqual(got[0], tc.want) {
				t.Fatalf("checks=%+v\nwant   %+v", got, tc.want)
			}
		})
	}
}

func TestActionsWithoutAgentTextAreNotChecked(t *testing.T) {
	cal := meetingWriter()
	s := scoring(0.99)
	w := &writer{mailbox: &mailbox{messages: messages("m1")}}
	g, _, _ := policyGuard(t, w, cal, s, allowAll)
	ctx := context.Background()
	if got, err := g.Trash(ctx, []string{"m1"}); err != nil || !got[0].OK {
		t.Fatalf("trash=%+v %v", got, err)
	}
	if err := g.CalendarCancel(ctx, "e1", ""); err != nil {
		t.Fatalf("cancel=%v", err)
	}
	cal.event.IsOrganizer = false
	if err := g.CalendarRespond(ctx, "e1", model.EventResponse{Response: model.ResponseAccept}); err != nil {
		t.Fatalf("respond=%v", err)
	}
	if len(s.checked()) != 0 {
		t.Fatalf("checked=%+v", s.checked())
	}
}

func TestJevFlagTurnsAllowIntoPending(t *testing.T) {
	w := &writer{mailbox: &mailbox{}}
	g, c, path := policyGuard(t, w, nil, scoring(0.62), allowAll)
	ctx := context.Background()
	_, err := g.DraftNew(ctx, model.NewDraft{To: []string{"CANARY@example.test"}, Subject: "CANARY", Body: "CANARY"})
	var pending guard.PendingError
	if !errors.As(err, &pending) || len(w.calls) != 0 {
		t.Fatalf("draft=%v writes=%v", err, w.calls)
	}
	item, err := c.GetPending(ctx, pending.ApprovalID)
	if err != nil || !reflect.DeepEqual(item.Jev, &model.ActionCheck{Flag: true, Score: new(0.62)}) {
		t.Fatalf("stored jev=%+v %v", item.Jev, err)
	}
	records := policyRecords(t, path)
	if len(records) != 2 || records[0]["decision"] != "approve" || !reflect.DeepEqual(records[0]["reasons"], []any{"policy", "jev_flag"}) {
		t.Fatalf("records=%v", records)
	}
}

func TestJevScoreBelowThresholdRuns(t *testing.T) {
	w := &writer{mailbox: &mailbox{}}
	g, _, path := policyGuard(t, w, nil, scoring(0.49), allowAll)
	if _, err := g.DraftNew(context.Background(), model.NewDraft{To: []string{"a@example.test"}, Body: "ok"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(w.calls, []string{"draft_new"}) {
		t.Fatalf("writes=%v", w.calls)
	}
	if records := policyRecords(t, path); len(records) != 1 || records[0]["decision"] != "allow" || !reflect.DeepEqual(records[0]["reasons"], []any{"policy"}) {
		t.Fatalf("records=%v", records)
	}
}

func TestFailedJevCheckTurnsAllowIntoPending(t *testing.T) {
	for name, s := range map[string]*actionScreener{
		"failed check":  {subjectScreener: &subjectScreener{}, check: model.ActionFailure("TimeoutError")},
		"adapter error": {subjectScreener: &subjectScreener{}, err: errors.New("CANARY")},
		"invalid score": {subjectScreener: &subjectScreener{}, check: model.ActionCheck{Score: new(1.5)}},
		"zero result":   {subjectScreener: &subjectScreener{}},
	} {
		t.Run(name, func(t *testing.T) {
			cal := meetingWriter()
			g, c, path := policyGuard(t, &mailbox{}, cal, s, allowAll)
			ctx := context.Background()
			err := g.CalendarUpdate(ctx, "e1", model.EventChange{Subject: new("CANARY")}, 0)
			var pending guard.PendingError
			if !errors.As(err, &pending) || len(cal.ids) != 0 {
				t.Fatalf("update=%v writes=%v", err, cal.ids)
			}
			item, err := c.GetPending(ctx, pending.ApprovalID)
			if err != nil || item.Jev == nil || !item.Jev.Flag || item.Jev.Score != nil || item.Jev.Error == nil || model.ValidateFailureText(item.Jev.Error) != nil {
				t.Fatalf("stored jev=%+v %v", item.Jev, err)
			}
			if records := policyRecords(t, path); len(records) != 2 || !reflect.DeepEqual(records[0]["reasons"], []any{"policy", "jev_error"}) {
				t.Fatalf("records=%v", records)
			}
		})
	}
}

func TestDenyNeverAsksJev(t *testing.T) {
	s := scoring(0.08)
	cal := meetingWriter()
	g, _, _ := policyGuard(t, &writer{mailbox: &mailbox{messages: messages("m1")}}, cal, s, config.Policy{})
	ctx := context.Background()
	if _, err := g.DraftNew(ctx, model.NewDraft{Body: "x"}); err == nil || err.Error() != "refused by policy: mail_draft" {
		t.Fatalf("draft=%v", err)
	}
	if _, err := g.DraftForward(ctx, "m1", []string{"a@example.test"}); err == nil {
		t.Fatal("forward ran")
	}
	if err := callCalendarWrite(ctx, g, "invite"); err == nil {
		t.Fatal("invite ran")
	}
	if len(s.checked()) != 0 {
		t.Fatalf("checked=%+v", s.checked())
	}
}

func TestApproveAsksJevAndStaysPending(t *testing.T) {
	s := scoring(0.08)
	policy := allowAll
	policy.Base.MailDraft = config.Approve
	g, c, path := policyGuard(t, &writer{mailbox: &mailbox{}}, nil, s, policy)
	ctx := context.Background()
	_, err := g.DraftNew(ctx, model.NewDraft{Body: "x"})
	var pending guard.PendingError
	if !errors.As(err, &pending) || len(s.checked()) != 1 {
		t.Fatalf("draft=%v checks=%d", err, len(s.checked()))
	}
	item, err := c.GetPending(ctx, pending.ApprovalID)
	if err != nil || !reflect.DeepEqual(item.Jev, &model.ActionCheck{Flag: false, Score: new(0.08)}) {
		t.Fatalf("stored jev=%+v %v", item.Jev, err)
	}
	if records := policyRecords(t, path); !reflect.DeepEqual(records[0]["reasons"], []any{"policy"}) {
		t.Fatalf("records=%v", records)
	}
}

func TestApprovedRunIsNotCheckedAgain(t *testing.T) {
	s := scoring(0.99)
	w := &writer{mailbox: &mailbox{}}
	g, _, _ := policyGuard(t, w, nil, s, allowAll)
	if _, err := g.DraftNew(guard.WithApproval(context.Background()), model.NewDraft{Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if len(s.checked()) != 0 || !reflect.DeepEqual(w.calls, []string{"draft_new"}) {
		t.Fatalf("checks=%+v writes=%v", s.checked(), w.calls)
	}
}

func TestForwardWithoutSourceTextIsPending(t *testing.T) {
	for name, tc := range map[string]struct {
		box     *mailbox
		verdict model.Verdict
	}{
		"hidden source":  {&mailbox{messages: messages("m1")}, model.Red},
		"missing source": {&mailbox{}, ""},
	} {
		t.Run(name, func(t *testing.T) {
			s := scoring(0.08)
			s.verdicts = map[string]model.Verdict{"m1": tc.verdict}
			policy := allowAll // the sealed decision allows, so only the check holds the action back
			w := &writer{mailbox: tc.box}
			g, c, path := policyGuard(t, w, nil, s, policy)
			ctx := context.Background()
			_, err := g.DraftForward(ctx, "m1", []string{"a@example.test"})
			var pending guard.PendingError
			if !errors.As(err, &pending) || len(w.calls) != 0 || len(s.checked()) != 0 {
				t.Fatalf("forward=%v writes=%v checks=%+v", err, w.calls, s.checked())
			}
			item, err := c.GetPending(ctx, pending.ApprovalID)
			if err != nil || item.Jev == nil || item.Jev.Error == nil || item.Jev.Score != nil {
				t.Fatalf("stored jev=%+v %v", item.Jev, err)
			}
			if records := policyRecords(t, path); !reflect.DeepEqual(records[0]["reasons"], []any{"policy", "jev_error"}) {
				t.Fatalf("records=%v", records)
			}
		})
	}
}
