package guard_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

func (w *writer) SendNew(context.Context, model.NewDraft) error {
	_, err := w.write("mail_send", nil)
	return err
}
func (w *writer) SendReply(context.Context, string, model.Reply) error {
	_, err := w.write("mail_reply", nil)
	return err
}
func (w *writer) SendForward(context.Context, string, model.Forward) error {
	_, err := w.write("mail_forward", nil)
	return err
}

// sendCalls are the send operations: three with the same agent text, and the send of the draft d1, whose fields
// come from ReadDraft (draftContent).
var sendCalls = []struct {
	op   string
	call func(context.Context, *guard.Guard) error
}{
	{"mail.send", func(ctx context.Context, g *guard.Guard) error {
		return g.SendNew(ctx, model.NewDraft{To: []string{"CANARY@example.test"}, Subject: "CANARY subject", Body: "CANARY body"})
	}},
	{"mail.reply", func(ctx context.Context, g *guard.Guard) error {
		return g.SendReply(ctx, "m1", model.Reply{Body: "CANARY reply"})
	}},
	{"mail.forward", func(ctx context.Context, g *guard.Guard) error {
		return g.SendForward(ctx, "m1", model.Forward{Body: "CANARY note", To: []string{"CANARY@example.test"}})
	}},
	{"draft.send", func(ctx context.Context, g *guard.Guard) error { return g.DraftSend(ctx, "d1") }},
}

// draftFields are the stored and checked fields of draftContent.
const draftFields = `{"to":["CANARY-to@example.test"],"cc":[],"bcc":["CANARY-bcc@example.test"],"subject":"CANARY subject","body":"<p>CANARY body</p>","html":true}`

func TestDefaultPolicyRefusesEverySendAfterSourceMetadata(t *testing.T) {
	for _, tc := range sendCalls {
		t.Run(tc.op, func(t *testing.T) {
			b := &mailbox{messages: messages("m1")}
			w := &writer{mailbox: b}
			s := scoring(0)
			g, _, path := policyGuard(t, w, nil, s, defaultPolicy)
			if err := tc.call(context.Background(), g); err == nil || err.Error() != "refused by policy: mail_send" {
				t.Fatalf("send=%v", err)
			}
			if len(w.calls) != 0 || slices.ContainsFunc(b.gets, func(f fetch) bool { return f.body }) || w.draftReads != 0 || len(s.checked()) != 0 || s.count() != 0 {
				t.Fatalf("provider or Jev accessed: writes=%v fetches=%v draft reads=%d checks=%v", w.calls, b.gets, w.draftReads, s.checked())
			}
			records := policyRecords(t, path)
			if len(records) != 1 || records[0]["operation"] != tc.op || records[0]["key"] != "mail_send" || records[0]["decision"] != "deny" || len(auditRecords(t, path)) != 0 {
				t.Fatalf("records=%v", allAuditRecords(t, path))
			}
		})
	}
}

func TestAllowedSendRunsOnceWithAContentFreeRecord(t *testing.T) {
	w := &writer{mailbox: &mailbox{messages: messages("m1")}}
	s := scoring(0.01)
	g, _, path := policyGuard(t, w, nil, s, allowAll)
	ctx := context.Background()
	for _, tc := range sendCalls {
		if err := tc.call(ctx, g); err != nil {
			t.Fatalf("%s=%v", tc.op, err)
		}
	}
	if want := []string{"mail_send", "mail_reply", "mail_forward", "draft_send"}; !reflect.DeepEqual(w.calls, want) {
		t.Fatalf("provider writes=%v", w.calls)
	}
	records := auditRecords(t, path)
	want := []struct {
		operation string
		ids       []any
	}{{"mail_send", []any{}}, {"mail_reply", []any{"m1"}}, {"mail_forward", []any{"m1"}}, {"draft_send", []any{"d1"}}}
	if len(records) != len(want) {
		t.Fatalf("records=%v", records)
	}
	for i, w := range want {
		r := records[i]
		results := []any{}
		for _, id := range w.ids {
			results = append(results, map[string]any{"id": id, "ok": true, "error": nil})
		}
		if r["operation"] != w.operation || !reflect.DeepEqual(r["ids"], w.ids) || !reflect.DeepEqual(r["results"], results) || r["draft_id"] != nil {
			t.Fatalf("record %d=%v", i, r)
		}
	}
	for _, r := range policyRecords(t, path) {
		if r["key"] != "mail_send" || r["decision"] != "allow" {
			t.Fatalf("policy record=%v", r)
		}
	}
}

func TestApproveStoresTheSendInItsAPIShape(t *testing.T) {
	policy := defaultPolicy
	policy.Base.MailSend = config.Approve
	params := map[string]string{
		"mail.send":    `{"to":["CANARY@example.test"],"cc":[],"bcc":[],"subject":"CANARY subject","body":"CANARY body","html":false}`,
		"mail.reply":   `{"id":"m1","body":"CANARY reply","html":false,"all":false,"cc":[],"bcc":[]}`,
		"mail.forward": `{"id":"m1","to":["CANARY@example.test"],"cc":[],"bcc":[],"body":"CANARY note","html":false}`,
		"draft.send":   `{"id":"d1"}`,
	}
	// A draft send stores the draft fields that Jev checked beside its args; other sends store none.
	drafts := map[string]string{"draft.send": draftFields}
	for _, tc := range sendCalls {
		t.Run(tc.op, func(t *testing.T) {
			w := &writer{mailbox: &mailbox{messages: messages("m1")}}
			g, c, _ := policyGuard(t, w, nil, scoring(0), policy)
			err := tc.call(context.Background(), g)
			var pending guard.PendingError
			if !errors.As(err, &pending) {
				t.Fatalf("send=%v; want pending", err)
			}
			item, err := c.GetPending(context.Background(), pending.ApprovalID)
			if err != nil || item.Op != tc.op || item.State != cache.StatePending || string(item.Args) != params[tc.op] || !sameJSON(item.Draft, drafts[tc.op]) {
				t.Fatalf("stored %+v args=%s draft=%s %v", item, item.Args, item.Draft, err)
			}
			if len(w.calls) != 0 {
				t.Fatalf("provider writes=%v", w.calls)
			}
		})
	}
}

func TestSendFromASealedSourceIsAlwaysRefused(t *testing.T) {
	policy := defaultPolicy
	policy.Base.MailSend = config.Allow
	// A draft send acts on a draft and has no sealed check.
	for _, tc := range sendCalls[1:3] {
		t.Run(tc.op, func(t *testing.T) {
			w := &writer{mailbox: &mailbox{messages: messages("m1")}}
			s := &actionScreener{subjectScreener: &subjectScreener{verdicts: map[string]model.Verdict{"m1": model.Sealed}}, check: model.ActionScore(0)}
			g, _, _ := policyGuard(t, w, nil, s, policy)
			if err := tc.call(context.Background(), g); err == nil || err.Error() != "refused by policy: mail_send" {
				t.Fatalf("send=%v", err)
			}
			if len(w.calls) != 0 {
				t.Fatalf("provider writes=%v", w.calls)
			}
		})
	}
	w := &writer{mailbox: &mailbox{messages: messages("m1")}}
	s := &actionScreener{subjectScreener: &subjectScreener{verdicts: map[string]model.Verdict{"m1": model.Sealed}}, check: model.ActionScore(0)}
	g, _, _ := policyGuard(t, w, nil, s, policy)
	if err := sendCalls[0].call(context.Background(), g); err != nil || s.count() != 0 {
		t.Fatalf("new send has no source: %v screenings=%d", err, s.count())
	}
}

func TestJevFlagHoldsAnAllowedSend(t *testing.T) {
	wants := map[string]map[string]any{
		"mail.send":    {"to": []any{"CANARY@example.test"}, "cc": []any{}, "bcc": []any{}, "subject": "CANARY subject", "body": "CANARY body", "html": false},
		"mail.reply":   {"body": "CANARY reply", "html": false, "all": false, "cc": []any{}, "bcc": []any{}, "reply_recipients": map[string]any{"to": []string{"ana@example.test"}, "cc": []string{}}},
		"mail.forward": {"to": []any{"CANARY@example.test"}, "cc": []any{}, "bcc": []any{}, "body": "CANARY note", "html": false, "forwarded_message": map[string]any{"subject": "m1", "body": "BODYTEXT [L1]"}},
		"draft.send":   {"to": []any{"CANARY-to@example.test"}, "cc": []any{}, "bcc": []any{"CANARY-bcc@example.test"}, "subject": "CANARY subject", "body": "<p>CANARY body</p>", "html": true},
	}
	for _, tc := range sendCalls {
		t.Run(tc.op, func(t *testing.T) {
			w := &writer{mailbox: &mailbox{messages: messages("m1")}}
			s := scoring(0.99)
			g, c, _ := policyGuard(t, w, nil, s, allowAll)
			err := tc.call(context.Background(), g)
			var pending guard.PendingError
			if !errors.As(err, &pending) {
				t.Fatalf("send=%v; want pending", err)
			}
			want := actionCall{tc.op, "sends an email to the listed recipients", wants[tc.op]}
			if got := s.checked(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
				t.Fatalf("checks=%+v\nwant   %+v", got, want)
			}
			if item, err := c.GetPending(context.Background(), pending.ApprovalID); err != nil || item.Jev == nil || !item.Jev.Flag {
				t.Fatalf("stored %+v %v", item, err)
			}
			if len(w.calls) != 0 {
				t.Fatalf("provider writes=%v", w.calls)
			}
		})
	}
}

func TestApprovedSendRunsWithoutANewDecision(t *testing.T) {
	for _, tc := range sendCalls {
		t.Run(tc.op, func(t *testing.T) {
			w := &writer{mailbox: &mailbox{messages: messages("m1")}}
			s := scoring(0.99)
			g, _, path := policyGuard(t, w, nil, s, defaultPolicy)
			if err := tc.call(guard.WithApproval(context.Background()), g); err != nil {
				t.Fatalf("send=%v", err)
			}
			if len(w.calls) != 1 || len(s.checked()) != 0 || len(policyRecords(t, path)) != 0 || len(auditRecords(t, path)) != 1 {
				t.Fatalf("writes=%v checks=%v records=%v", w.calls, s.checked(), allAuditRecords(t, path))
			}
		})
	}
}

func TestFailedSendIsNotAudited(t *testing.T) {
	w := &writer{mailbox: &mailbox{messages: messages("m1")}, err: errors.New("CANARY provider text")}
	g, _, path := policyGuard(t, w, nil, scoring(0), allowAll)
	for _, tc := range sendCalls {
		if err := tc.call(context.Background(), g); err == nil || err.Error() == "CANARY provider text" {
			t.Fatalf("%s=%v", tc.op, err)
		}
	}
	if len(auditRecords(t, path)) != 0 {
		t.Fatal("failed send has an operation record")
	}
}

// The guard re-checks that a send or forward has a recipient: one without To, CC, or BCC is an invalid request that
// reaches no policy decision, no Jev check, and no provider.
func TestSendWithoutARecipientIsInvalid(t *testing.T) {
	w := &writer{mailbox: &mailbox{}}
	s := scoring(0)
	g, _, path := policyGuard(t, w, nil, s, allowAll)
	if err := g.SendNew(context.Background(), model.NewDraft{Subject: "CANARY subject", Body: "CANARY body"}); err == nil || err.Error() != "invalid request" {
		t.Fatalf("send=%v", err)
	}
	if err := g.SendForward(context.Background(), "m1", model.Forward{Body: "CANARY note", HTML: true}); err == nil || err.Error() != "invalid request" {
		t.Fatalf("forward=%v", err)
	}
	if len(w.calls) != 0 || len(s.checked()) != 0 || len(allAuditRecords(t, path)) != 0 {
		t.Fatalf("writes=%v checks=%v records=%v", w.calls, s.checked(), allAuditRecords(t, path))
	}
}

// DraftNew returns the draft that the provider made; a result without an ID fails closed.
func TestDraftNewReturnsTheProviderDraftAndRefusesOneWithoutID(t *testing.T) {
	made := model.DraftResult{ID: "d9", ThreadID: "t9", To: []string{"a@example.test"}, CC: []string{}, BCC: []string{"b@example.test"}, Subject: "Hi"}
	w := &writer{mailbox: &mailbox{}, draft: &made}
	g, _, _ := policyGuard(t, w, nil, scoring(0), allowAll)
	if got, err := g.DraftNew(context.Background(), model.NewDraft{To: []string{"a@example.test"}}); err != nil || !reflect.DeepEqual(got, made) {
		t.Fatalf("draft=%+v %v", got, err)
	}
	w.draft = &model.DraftResult{ThreadID: "t9"}
	if _, err := g.DraftNew(context.Background(), model.NewDraft{To: []string{"a@example.test"}}); err == nil || err.Error() != "ValueError" {
		t.Fatalf("draft without ID: %v", err)
	}
}

// sameJSON reports whether stored is the JSON value of want, or is nil when want is empty.
func sameJSON(stored json.RawMessage, want string) bool {
	if want == "" {
		return stored == nil
	}
	var a, b any
	return json.Unmarshal(stored, &a) == nil && json.Unmarshal([]byte(want), &b) == nil && reflect.DeepEqual(a, b)
}

// waitingSend stores a waiting send of draft id in c, as an earlier draft send under approve did, and gives its ID.
func waitingSend(t *testing.T, c *cache.Cache, id string) string {
	t.Helper()
	approval, err := c.AddPending(context.Background(), "work", "draft.send", json.RawMessage(`{"id":"`+id+`"}`), json.RawMessage(draftFields), nil, now)
	if err != nil {
		t.Fatal(err)
	}
	return approval
}

// stateOf is the state of the stored action id.
func stateOf(t *testing.T, c *cache.Cache, id string) cache.PendingState {
	t.Helper()
	p, err := c.GetPending(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return p.State
}

// waitingIDs gives the IDs of the actions in c that wait for approval.
func waitingIDs(t *testing.T, c *cache.Cache) []string {
	t.Helper()
	items, err := c.ListPending(context.Background(), 0, "")
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, p := range items {
		out = append(out, p.ID)
	}
	return out
}

// decisionEvents gives the policy and approval records as "policy OPERATION DECISION ID APPROVAL" and "approval
// OPERATION STATE APPROVAL", in order. A field that a record does not have is <nil>.
func decisionEvents(t *testing.T, path string) []string {
	t.Helper()
	out := []string{}
	for _, r := range policyRecords(t, path) {
		if r["event"] == "policy" {
			out = append(out, fmt.Sprint("policy ", r["operation"], " ", r["decision"], " ", r["id"], " ", r["approval_id"]))
		} else {
			out = append(out, fmt.Sprint("approval ", r["operation"], " ", r["state"], " ", r["approval_id"]))
		}
	}
	return out
}

// A draft update that runs, directly or as an approved run, first withdraws the waiting send of its draft: at the
// time of the provider write, the old item is already withdrawn, so the console can no longer approve it. After the
// write the guard makes a new mail_send decision for the draft's new ID (Gmail gives a new ID), with a fresh read of
// the draft and a fresh Jev check, and without the approval mark of an approved update: allow and approve both give a
// new waiting send that never runs by itself. The result names it. Every record has IDs and fixed codes only.
func TestDraftUpdateWithdrawsTheWaitingSendBeforeTheWriteAndHoldsANewOne(t *testing.T) {
	for _, tc := range []struct {
		name     string
		send     config.Decision
		approved bool
	}{
		{"send allowed", config.Allow, false},
		{"send approved", config.Approve, false},
		{"approved update, send allowed", config.Allow, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &writer{mailbox: &mailbox{}}
			s := scoring(0.08)
			policy := allowAll
			policy.Base.MailSend = tc.send
			g, c, path := policyGuard(t, w, nil, s, policy)
			old := waitingSend(t, c, "d1")
			var atWrite cache.PendingState
			w.beforeWrite = func() { atWrite = stateOf(t, c, old) }
			ctx := context.Background()
			if tc.approved {
				ctx = guard.WithApproval(ctx)
			}
			got, err := g.DraftUpdate(ctx, "d1", model.DraftChange{Subject: new("CANARY changed")})
			if err != nil || got.ID != "d4" || got.ThreadID != "t4" || got.SendApprovalID == "" || got.SendApprovalID == old {
				t.Fatalf("update=%+v %v", got, err)
			}
			if atWrite != cache.StateWithdrawn || stateOf(t, c, old) != cache.StateWithdrawn {
				t.Fatalf("old send at the write=%s after=%s", atWrite, stateOf(t, c, old))
			}
			item, err := c.GetPending(context.Background(), got.SendApprovalID)
			if err != nil || item.Op != "draft.send" || item.State != cache.StatePending || string(item.Args) != `{"id":"d4"}` || !sameJSON(item.Draft, draftFields) || item.Jev == nil || item.Jev.Flag {
				t.Fatalf("new send=%+v args=%s draft=%s %v", item, item.Args, item.Draft, err)
			}
			if !reflect.DeepEqual(waitingIDs(t, c), []string{got.SendApprovalID}) || !reflect.DeepEqual(w.calls, []string{"draft_update"}) || !reflect.DeepEqual(w.draftIDs, []string{"d1", "d4"}) {
				t.Fatalf("waiting=%v writes=%v draft reads=%v", waitingIDs(t, c), w.calls, w.draftIDs)
			}
			checks := s.checked()
			if len(checks) == 0 || checks[len(checks)-1].operation != "draft.send" || !reflect.DeepEqual(checks[len(checks)-1].state, map[string]any{"to": []any{"CANARY-to@example.test"}, "cc": []any{},
				"bcc": []any{"CANARY-bcc@example.test"}, "subject": "CANARY subject", "body": "<p>CANARY body</p>", "html": true}) {
				t.Fatalf("checks=%+v", checks)
			}
			// The allow record of the update follows its write, as for every allowed write.
			want := []string{"approval draft.send withdrawn " + old, "policy draft.update allow d1 <nil>"}
			if tc.approved {
				want = want[:1]
			}
			want = append(want, "policy draft.send approve d4 "+got.SendApprovalID, "approval draft.send created "+got.SendApprovalID)
			if events := decisionEvents(t, path); !reflect.DeepEqual(events, want) {
				t.Fatalf("records=%v\nwant     %v", events, want)
			}
			if records := auditRecords(t, path); len(records) != 1 || records[0]["operation"] != "draft_update" {
				t.Fatalf("operation records=%v", records)
			}
		})
	}
}

// Under a mail_send deny, an update still withdraws the waiting send but makes no new one, also as an approved run:
// the new decision never inherits the approval of the update. The deny has its policy record.
func TestDraftUpdateUnderASendDenyWithdrawsAndMakesNoNewSend(t *testing.T) {
	for _, approved := range []bool{false, true} {
		w := &writer{mailbox: &mailbox{}}
		s := scoring(0.08)
		policy := allowAll
		policy.Base.MailSend = config.Deny
		g, c, path := policyGuard(t, w, nil, s, policy)
		old := waitingSend(t, c, "d1")
		ctx := context.Background()
		if approved {
			ctx = guard.WithApproval(ctx)
		}
		got, err := g.DraftUpdate(ctx, "d1", model.DraftChange{Subject: new("CANARY changed")})
		if err != nil || got != (guard.UpdatedDraft{ID: "d4", ThreadID: "t4"}) {
			t.Fatalf("approved=%v update=%+v %v", approved, got, err)
		}
		if stateOf(t, c, old) != cache.StateWithdrawn || len(waitingIDs(t, c)) != 0 || !reflect.DeepEqual(w.draftIDs, []string{"d1"}) {
			t.Fatalf("approved=%v old=%s waiting=%v draft reads=%v", approved, stateOf(t, c, old), waitingIDs(t, c), w.draftIDs)
		}
		for _, check := range s.checked() {
			if check.operation == "draft.send" {
				t.Fatalf("approved=%v: denied send was checked", approved)
			}
		}
		if events := decisionEvents(t, path); events[len(events)-1] != "policy draft.send deny d4 <nil>" || !slices.Contains(events, "approval draft.send withdrawn "+old) {
			t.Fatalf("approved=%v records=%v", approved, events)
		}
	}
}

// Several waiting sends of the draft are all withdrawn, and the update makes exactly one new one. An update of a
// draft without a waiting send makes none.
func TestDraftUpdateMakesAtMostOneNewSend(t *testing.T) {
	w := &writer{mailbox: &mailbox{}}
	g, c, _ := policyGuard(t, w, nil, scoring(0.08), allowAll)
	first, second := waitingSend(t, c, "d1"), waitingSend(t, c, "d1")
	got, err := g.DraftUpdate(context.Background(), "d1", model.DraftChange{Subject: new("CANARY changed")})
	if err != nil || stateOf(t, c, first) != cache.StateWithdrawn || stateOf(t, c, second) != cache.StateWithdrawn || !reflect.DeepEqual(waitingIDs(t, c), []string{got.SendApprovalID}) {
		t.Fatalf("update=%+v %v waiting=%v", got, err, waitingIDs(t, c))
	}
	other := &writer{mailbox: &mailbox{}}
	g, c, path := policyGuard(t, other, nil, scoring(0.08), allowAll)
	if got, err := g.DraftUpdate(context.Background(), "d1", model.DraftChange{Subject: new("CANARY changed")}); err != nil || got.SendApprovalID != "" {
		t.Fatalf("update without a waiting send=%+v %v", got, err)
	}
	if len(waitingIDs(t, c)) != 0 || !reflect.DeepEqual(other.draftIDs, []string{"d1"}) || !reflect.DeepEqual(decisionEvents(t, path), []string{"policy draft.update allow d1 <nil>"}) {
		t.Fatalf("waiting=%v draft reads=%v records=%v", waitingIDs(t, c), other.draftIDs, decisionEvents(t, path))
	}
}

// A draft update or delete that does not run now still withdraws the waiting send of its draft and makes no new one:
// one that waits for approval itself, one that the policy denies, and one of a message that is not a draft. Nothing is
// written, and the withdrawal has its approval record.
func TestDraftChangeThatDoesNotRunStillWithdrawsTheWaitingSend(t *testing.T) {
	for _, tc := range []struct {
		name  string
		draft config.Decision
		w     *writer
	}{
		{"held", config.Approve, &writer{}},
		{"denied", config.Deny, &writer{}},
		{"not a draft", config.Allow, &writer{notDraft: true}},
	} {
		for _, op := range []struct {
			name string
			call func(*guard.Guard) error
		}{
			{"update", func(g *guard.Guard) error {
				_, err := g.DraftUpdate(context.Background(), "d1", model.DraftChange{Subject: new("CANARY changed")})
				return err
			}},
			{"delete", func(g *guard.Guard) error { return g.DraftDelete(context.Background(), "d1") }},
		} {
			t.Run(tc.name+" "+op.name, func(t *testing.T) {
				w := *tc.w
				w.mailbox = &mailbox{}
				policy := allowAll
				policy.Base.MailDraft = tc.draft
				g, c, path := policyGuard(t, &w, nil, scoring(0.08), policy)
				old := waitingSend(t, c, "d1")
				err := op.call(g)
				if err == nil {
					t.Fatal("change ran")
				}
				// Only a held change waits: its own item.
				want := []string{}
				pending, held := errors.AsType[guard.PendingError](err)
				if held != (tc.draft == config.Approve) {
					t.Fatalf("err=%v; held=%v, want held only under approve", err, held)
				}
				if held {
					want = []string{pending.ApprovalID}
				}
				if stateOf(t, c, old) != cache.StateWithdrawn || !reflect.DeepEqual(waitingIDs(t, c), want) || len(w.calls) != 0 {
					t.Fatalf("old send=%s waiting=%v writes=%v", stateOf(t, c, old), waitingIDs(t, c), w.calls)
				}
				if events := decisionEvents(t, path); len(events) == 0 || events[0] != "approval draft.send withdrawn "+old {
					t.Fatalf("records=%v", events)
				}
			})
		}
	}
}

// A failed provider write returns its safe code. The send that the change withdrew before the write stays withdrawn,
// and no new send is made.
func TestFailedDraftChangeKeepsTheSendWithdrawnAndMakesNoNewOne(t *testing.T) {
	for _, op := range []struct {
		name string
		call func(*guard.Guard) error
	}{
		{"update", func(g *guard.Guard) error {
			_, err := g.DraftUpdate(context.Background(), "d1", model.DraftChange{Subject: new("CANARY changed")})
			return err
		}},
		{"delete", func(g *guard.Guard) error { return g.DraftDelete(context.Background(), "d1") }},
	} {
		t.Run(op.name, func(t *testing.T) {
			w := &writer{mailbox: &mailbox{}, err: model.MailboxError("CANARY provider text")}
			s := scoring(0.08)
			g, c, path := policyGuard(t, w, nil, s, allowAll)
			old := waitingSend(t, c, "d1")
			if err := op.call(g); err == nil || err.Error() != "MailboxError" {
				t.Fatalf("change=%v", err)
			}
			if stateOf(t, c, old) != cache.StateWithdrawn || len(waitingIDs(t, c)) != 0 || !reflect.DeepEqual(w.draftIDs, []string{"d1"}) || len(auditRecords(t, path)) != 0 {
				t.Fatalf("old=%s waiting=%v draft reads=%v records=%v", stateOf(t, c, old), waitingIDs(t, c), w.draftIDs, allAuditRecords(t, path))
			}
			for _, check := range s.checked() {
				if check.operation == "draft.send" {
					t.Fatal("failed change checked a new send")
				}
			}
		})
	}
}

// A draft delete withdraws the waiting send of its draft before the provider write and makes no new send.
func TestDraftDeleteWithdrawsTheWaitingSend(t *testing.T) {
	w := &writer{mailbox: &mailbox{}}
	g, c, path := policyGuard(t, w, nil, scoring(0.08), allowAll)
	old := waitingSend(t, c, "d1")
	var atWrite cache.PendingState
	w.beforeWrite = func() { atWrite = stateOf(t, c, old) }
	if err := g.DraftDelete(context.Background(), "d1"); err != nil {
		t.Fatal(err)
	}
	if atWrite != cache.StateWithdrawn || len(waitingIDs(t, c)) != 0 || !reflect.DeepEqual(w.calls, []string{"draft_delete"}) || !reflect.DeepEqual(w.draftIDs, []string{"d1"}) {
		t.Fatalf("old at the write=%s waiting=%v writes=%v draft reads=%v", atWrite, waitingIDs(t, c), w.calls, w.draftIDs)
	}
	if events := decisionEvents(t, path); !reflect.DeepEqual(events, []string{"approval draft.send withdrawn " + old, "policy draft.delete allow d1 <nil>"}) {
		t.Fatalf("records=%v", events)
	}
}

// A second draft send of a draft with a waiting send withdraws the old one first, so one draft has at most one
// waiting send: under approve the new send waits in its place, under allow the old one is withdrawn before the send.
func TestSecondDraftSendReplacesTheWaitingSend(t *testing.T) {
	for _, send := range []config.Decision{config.Approve, config.Allow} {
		t.Run(string(send), func(t *testing.T) {
			w := &writer{mailbox: &mailbox{}}
			policy := allowAll
			policy.Base.MailSend = send
			g, c, path := policyGuard(t, w, nil, scoring(0.08), policy)
			old := waitingSend(t, c, "d1")
			var atWrite cache.PendingState
			w.beforeWrite = func() { atWrite = stateOf(t, c, old) }
			err := g.DraftSend(context.Background(), "d1")
			pending, held := errors.AsType[guard.PendingError](err)
			if stateOf(t, c, old) != cache.StateWithdrawn {
				t.Fatalf("old send=%s", stateOf(t, c, old))
			}
			switch send {
			case config.Approve:
				if !held || !reflect.DeepEqual(waitingIDs(t, c), []string{pending.ApprovalID}) || len(w.calls) != 0 {
					t.Fatalf("send=%v waiting=%v writes=%v", err, waitingIDs(t, c), w.calls)
				}
				if events := decisionEvents(t, path); !reflect.DeepEqual(events, []string{"approval draft.send withdrawn " + old, "policy draft.send approve d1 " + pending.ApprovalID, "approval draft.send created " + pending.ApprovalID}) {
					t.Fatalf("records=%v", events)
				}
			default:
				if err != nil || atWrite != cache.StateWithdrawn || len(waitingIDs(t, c)) != 0 || !reflect.DeepEqual(w.calls, []string{"draft_send"}) {
					t.Fatalf("send=%v old at the write=%s waiting=%v writes=%v", err, atWrite, waitingIDs(t, c), w.calls)
				}
			}
		})
	}
}

// A second draft send withdraws the waiting send of its draft also when it is refused itself: under a mail_send deny,
// before any draft read, and for a message that is no longer a draft. Withdrawing only removes: nothing new waits,
// nothing is written, and the withdrawal has its approval record.
func TestRefusedDraftSendStillWithdrawsTheWaitingSend(t *testing.T) {
	for _, tc := range []struct {
		name, want string
		send       config.Decision
		w          *writer
	}{
		{"denied", "refused by policy: mail_send", config.Deny, &writer{}},
		{"no longer a draft", "draft send refused: not a draft", config.Approve, &writer{notDraft: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.w.mailbox = &mailbox{}
			policy := allowAll
			policy.Base.MailSend = tc.send
			g, c, path := policyGuard(t, tc.w, nil, scoring(0.08), policy)
			old := waitingSend(t, c, "d1")
			if err := g.DraftSend(context.Background(), "d1"); err == nil || err.Error() != tc.want {
				t.Fatalf("send=%v", err)
			}
			if stateOf(t, c, old) != cache.StateWithdrawn || len(waitingIDs(t, c)) != 0 || len(tc.w.calls) != 0 {
				t.Fatalf("old=%s waiting=%v writes=%v", stateOf(t, c, old), waitingIDs(t, c), tc.w.calls)
			}
			if events := decisionEvents(t, path); len(events) == 0 || events[0] != "approval draft.send withdrawn "+old {
				t.Fatalf("records=%v", events)
			}
		})
	}
}
