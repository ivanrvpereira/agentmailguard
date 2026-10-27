package guard_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
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

func TestDefaultPolicyRefusesEverySendBeforeProviderAccess(t *testing.T) {
	for _, tc := range sendCalls {
		t.Run(tc.op, func(t *testing.T) {
			b := &mailbox{messages: messages("m1")}
			w := &writer{mailbox: b}
			s := scoring(0)
			g, _, path := policyGuard(t, w, nil, s, defaultPolicy)
			if err := tc.call(context.Background(), g); err == nil || err.Error() != "refused by policy: mail_send" {
				t.Fatalf("send=%v", err)
			}
			if len(w.calls) != 0 || len(b.gets) != 0 || w.draftReads != 0 || len(s.checked()) != 0 || s.count() != 0 {
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
