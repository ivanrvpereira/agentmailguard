package guard_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/audit"
	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/rules"
)

// defaultPolicy is the shipped default from the plan: mail allowed, calendar denied, sealed trash denied.
var defaultPolicy = config.Policy{
	Base: config.Decisions{
		MailTrash: config.Allow, MailDraft: config.Allow, MailSend: config.Deny,
		CalendarInvite: config.Deny, CalendarUpdate: config.Deny, CalendarCancel: config.Deny, CalendarRespond: config.Deny,
	},
	Sealed: config.SealedDecisions{MailTrash: config.Deny},
}

// allowAll allows every write. A reply or forward still looks up its source, which must not be sealed.
var allowAll = config.Policy{
	Base: config.Decisions{
		MailTrash: config.Allow, MailDraft: config.Allow, MailSend: config.Allow,
		CalendarInvite: config.Allow, CalendarUpdate: config.Allow, CalendarCancel: config.Allow, CalendarRespond: config.Allow,
	},
	Sealed: config.SealedDecisions{MailTrash: config.Allow},
}

// policyGuard builds a guard over mail b and calendar cal with the given policy.
func policyGuard(t *testing.T, b model.Mailbox, cal model.Calendar, s model.Screener, policy config.Policy) (*guard.Guard, *cache.Cache, string) {
	t.Helper()
	dir := t.TempDir()
	c, err := cache.Open(context.Background(), filepath.Join(dir, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	path := filepath.Join(dir, "audit.jsonl")
	return guard.New("work", b, cal, s, c, audit.New(path), guard.Options{Now: func() time.Time { return now }, Policy: policy}), c, path
}

func TestEmptyPolicyDeniesEveryPolicyWrite(t *testing.T) {
	w := &writer{mailbox: &mailbox{messages: []model.Message{message("m1")}}}
	cal := meetingWriter()
	s := &screener{verdict: model.Green}
	g, _, _ := policyGuard(t, w, cal, s, config.Policy{})
	ctx := context.Background()
	got, err := g.Trash(ctx, []string{"m1"})
	if err != nil || !reflect.DeepEqual(got, []model.MutationResult{{ID: "m1", Error: new("refused by policy: mail_trash")}}) {
		t.Fatalf("trash=%+v %v", got, err)
	}
	if _, err := g.DraftNew(ctx, model.NewDraft{To: []string{"a@example.test"}}); err == nil || err.Error() != "refused by policy: mail_draft" {
		t.Fatalf("draft=%v", err)
	}
	if err := callCalendarWrite(ctx, g, "update"); err == nil || err.Error() != "refused by policy: calendar_update" {
		t.Fatalf("calendar=%v", err)
	}
	if len(w.calls) != 0 || cal.calls != 0 || len(cal.ids) != 0 {
		t.Fatalf("refused writes reached the provider: mail=%v calendar reads=%d writes=%v", w.calls, cal.calls, cal.ids)
	}
	if _, err := g.Archive(ctx, []string{"m1"}); err != nil || !reflect.DeepEqual(w.calls, []string{"archive"}) {
		t.Fatalf("organize action without a key was refused: %v %v", err, w.calls)
	}
}

// subjectScreener gives each message the verdict for its subject, with a score that fits it, green otherwise. message(id) uses the ID as subject.
type subjectScreener struct {
	model.Screener
	mu       sync.Mutex
	verdicts map[string]model.Verdict
	screened []string
}

func (s *subjectScreener) Screen(_ context.Context, state map[string]any) (model.Screening, error) {
	subject, _ := state["subject"].(string)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.screened = append(s.screened, subject)
	switch v := s.verdicts[subject]; v {
	case model.Error:
		return model.Screening{Verdict: model.Error, Error: new("Error")}, nil
	case "":
		return model.Screening{Verdict: model.Green, Score: new(0.0)}, nil
	case model.Yellow:
		return model.Screening{Verdict: v, Score: new(0.35)}, nil
	case model.Red:
		return model.Screening{Verdict: v, Score: new(0.9)}, nil
	default:
		return model.Screening{Verdict: v, Score: new(0.0)}, nil
	}
}

// CheckAction never flags, so writes behave as before the Jev action check.
func (s *subjectScreener) CheckAction(context.Context, string, string, map[string]any) (model.ActionCheck, error) {
	return model.ActionScore(0), nil
}
func (s *subjectScreener) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.screened)
}

func messages(ids ...string) []model.Message {
	out := []model.Message{}
	for _, id := range ids {
		out = append(out, message(id))
	}
	return out
}
func refusal(id, key string) model.MutationResult {
	return model.MutationResult{ID: id, Error: new("refused by policy: " + key)}
}

func TestDefaultPolicyTrashesAndDraftsGreenMail(t *testing.T) {
	w := &writer{mailbox: &mailbox{messages: messages("m1", "m2")}}
	g, _, _ := policyGuard(t, w, nil, &subjectScreener{}, defaultPolicy)
	ctx := context.Background()
	got, err := g.Trash(ctx, []string{"m1"})
	if err != nil || !reflect.DeepEqual(got, []model.MutationResult{{ID: "m1", OK: true}}) {
		t.Fatalf("trash=%+v %v", got, err)
	}
	for _, call := range []func() (string, error){
		func() (string, error) {
			d, err := g.DraftNew(ctx, model.NewDraft{To: []string{"a@example.test"}})
			return d.ID, err
		},
		func() (string, error) {
			d, _, err := g.DraftReply(ctx, "m2", model.Reply{Body: "reply"})
			return d.ID, err
		},
		func() (string, error) {
			d, _, err := g.DraftForward(ctx, "m2", model.Forward{To: []string{"a@example.test"}})
			return d.ID, err
		},
		func() (string, error) {
			d, err := g.DraftUpdate(ctx, "d1", model.DraftChange{Body: new("body")})
			return d.ID, err
		},
		func() (string, error) { return "", g.DraftDelete(ctx, "d1") },
	} {
		if _, err := call(); err != nil {
			t.Fatal(err)
		}
	}
	if want := []string{"trash", "draft_new", "draft_reply", "draft_forward", "draft_update", "draft_delete"}; !reflect.DeepEqual(w.calls, want) {
		t.Fatalf("provider writes=%v want %v", w.calls, want)
	}
}

func TestDefaultSealedDecisionRefusesSealedAndErrorMessages(t *testing.T) {
	for _, verdict := range []model.Verdict{model.Sealed, model.Error} {
		t.Run(string(verdict), func(t *testing.T) {
			w := &writer{mailbox: &mailbox{messages: messages("m1")}}
			s := &subjectScreener{verdicts: map[string]model.Verdict{"m1": verdict}}
			g, _, _ := policyGuard(t, w, nil, s, defaultPolicy)
			ctx := context.Background()
			if _, err := g.Get(ctx, "m1"); err != nil {
				t.Fatal(err)
			}
			s.verdicts = nil // a second screening would now say green
			got, err := g.Trash(ctx, []string{"m1"})
			if err != nil || !reflect.DeepEqual(got, []model.MutationResult{refusal("m1", "mail_trash")}) {
				t.Fatalf("trash=%+v %v", got, err)
			}
			if _, _, err := g.DraftReply(ctx, "m1", model.Reply{Body: "reply"}); err == nil || err.Error() != "refused by policy: mail_draft" {
				t.Fatalf("reply=%v", err)
			}
			if _, _, err := g.DraftForward(ctx, "m1", model.Forward{To: []string{"a@example.test"}}); err == nil || err.Error() != "refused by policy: mail_draft" {
				t.Fatalf("forward=%v", err)
			}
			if len(w.calls) != 0 || s.count() != 1 {
				t.Fatalf("provider writes=%v screenings=%d; want none and only the first read", w.calls, s.count())
			}
		})
	}
}

// sourceCalls are the writes that reply to or forward the existing message m1, with their policy key.
var sourceCalls = []struct {
	op, key string
	call    func(context.Context, *guard.Guard) error
}{
	{"draft.reply", "mail_draft", func(ctx context.Context, g *guard.Guard) error {
		_, _, err := g.DraftReply(ctx, "m1", model.Reply{Body: "CANARY reply"})
		return err
	}},
	{"draft.forward", "mail_draft", func(ctx context.Context, g *guard.Guard) error {
		_, _, err := g.DraftForward(ctx, "m1", model.Forward{Body: "CANARY note", To: []string{"CANARY@example.test"}})
		return err
	}},
	{"mail.reply", "mail_send", func(ctx context.Context, g *guard.Guard) error {
		return g.SendReply(ctx, "m1", model.Reply{Body: "CANARY reply"})
	}},
	{"mail.forward", "mail_send", func(ctx context.Context, g *guard.Guard) error {
		return g.SendForward(ctx, "m1", model.Forward{Body: "CANARY note", To: []string{"CANARY@example.test"}})
	}},
}

// approveAll holds every write for approval.
var approveAll = config.Policy{
	Base: config.Decisions{
		MailTrash: config.Approve, MailDraft: config.Approve, MailSend: config.Approve,
		CalendarInvite: config.Approve, CalendarUpdate: config.Approve, CalendarCancel: config.Approve, CalendarRespond: config.Approve,
	},
	Sealed: config.SealedDecisions{MailTrash: config.Approve},
}

// A red source refuses with the reason red; sealed, error, and unreadable sources with the reason sealed. The
// source is screened in the decision, because it has no cached verdict.
func TestReplyAndForwardOfARedOrSealedSourceAreAlwaysRefused(t *testing.T) {
	sources := map[string]struct {
		messages []model.Message
		verdict  model.Verdict
		reason   string
	}{
		"red":              {messages("m1"), model.Red, "red"},
		"sealed":           {messages("m1"), model.Sealed, "sealed"},
		"screening failed": {messages("m1"), model.Error, "sealed"},
		"unreadable":       {nil, "", "sealed"},
	}
	for name, policy := range map[string]config.Policy{"allow": allowAll, "approve": approveAll} {
		for source, src := range sources {
			for _, tc := range sourceCalls {
				t.Run(name+"/"+source+"/"+tc.op, func(t *testing.T) {
					w := &writer{mailbox: &mailbox{messages: src.messages}}
					s := &actionScreener{subjectScreener: &subjectScreener{verdicts: map[string]model.Verdict{"m1": src.verdict}}, check: model.ActionScore(0)}
					g, c, path := policyGuard(t, w, nil, s, policy)
					ctx := context.Background()
					if err := tc.call(ctx, g); err == nil || err.Error() != "refused by policy: "+tc.key {
						t.Fatalf("error=%v", err)
					}
					if len(w.calls) != 0 || len(s.checked()) != 0 {
						t.Fatalf("provider writes=%v checks=%v", w.calls, s.checked())
					}
					records := policyRecords(t, path)
					for _, r := range records {
						delete(r, "time")
					}
					want := []map[string]any{{"account": "work", "event": "policy", "operation": tc.op, "key": tc.key, "decision": "deny", "reasons": []any{"policy", src.reason}, "id": "m1"}}
					if !reflect.DeepEqual(records, want) || len(auditRecords(t, path)) != 0 {
						t.Fatalf("records=%v\nwant   %v", allAuditRecords(t, path), want)
					}
					if items, err := c.ListPending(ctx, 10); err != nil || len(items) != 0 {
						t.Fatalf("pending items=%+v %v", items, err)
					}
				})
			}
		}
	}
}

// The forwarded text of a yellow message is not shown, so the Jev check of its forward fails and holds an allowed
// forward; the policy record then has the reasons policy and jev_error, and never sealed or red.
func TestReplyAndForwardOfAGreenOrYellowSourceKeepTheBaseDecision(t *testing.T) {
	for _, verdict := range []model.Verdict{model.Green, model.Yellow} {
		for _, tc := range sourceCalls {
			for name, policy := range map[string]config.Policy{"allow": allowAll, "approve": approveAll} {
				t.Run(string(verdict)+"/"+tc.op+"/"+name, func(t *testing.T) {
					hidden := verdict != model.Green && strings.HasSuffix(tc.op, ".forward")
					decision, reasons := name, []any{"policy"}
					if hidden {
						decision, reasons = "approve", []any{"policy", "jev_error"}
					}
					w := &writer{mailbox: &mailbox{messages: messages("m1")}}
					s := &actionScreener{subjectScreener: &subjectScreener{verdicts: map[string]model.Verdict{"m1": verdict}}, check: model.ActionScore(0)}
					g, _, path := policyGuard(t, w, nil, s, policy)
					err := tc.call(context.Background(), g)
					var pending guard.PendingError
					if decision == "allow" && (err != nil || len(w.calls) != 1) || decision == "approve" && (!errors.As(err, &pending) || len(w.calls) != 0) {
						t.Fatalf("error=%v writes=%v; want %s", err, w.calls, decision)
					}
					records := policyRecords(t, path)
					if len(records) == 0 || records[0]["decision"] != decision || !reflect.DeepEqual(records[0]["reasons"], reasons) {
						t.Fatalf("records=%v; want decision %s reasons %v", records, decision, reasons)
					}
				})
			}
		}
	}
}

func TestMissingVerdictIsScreenedBeforeTheDecision(t *testing.T) {
	for _, tc := range []struct {
		verdict model.Verdict
		want    model.MutationResult
	}{
		{model.Sealed, refusal("m1", "mail_trash")},
		{model.Green, model.MutationResult{ID: "m1", OK: true}},
	} {
		t.Run(string(tc.verdict), func(t *testing.T) {
			w := &writer{mailbox: &mailbox{messages: messages("m1")}}
			s := &subjectScreener{verdicts: map[string]model.Verdict{"m1": tc.verdict}}
			g, _, _ := policyGuard(t, w, nil, s, defaultPolicy)
			got, err := g.Trash(context.Background(), []string{"m1"})
			if err != nil || !reflect.DeepEqual(got, []model.MutationResult{tc.want}) {
				t.Fatalf("trash=%+v %v", got, err)
			}
			if !reflect.DeepEqual(s.screened, []string{"m1"}) {
				t.Fatalf("screened=%v; want m1 once", s.screened)
			}
		})
	}
	w := &writer{mailbox: &mailbox{messages: messages("m1")}}
	s := &subjectScreener{verdicts: map[string]model.Verdict{"m1": model.Sealed}}
	g, _, _ := policyGuard(t, w, nil, s, defaultPolicy)
	if _, _, err := g.DraftReply(context.Background(), "m1", model.Reply{Body: "reply"}); err == nil || err.Error() != "refused by policy: mail_draft" || s.count() != 1 || len(w.calls) != 0 {
		t.Fatalf("reply to unscreened sealed source: %v screenings=%d writes=%v", err, s.count(), w.calls)
	}
}

// The sealed trash decision is stricter here, so the message is screened, but a red message is not sealed: its
// trash keeps the base decision, and a held trash has only the reason policy.
func TestTrashOfARedMessageKeepsTheBaseDecision(t *testing.T) {
	for _, base := range []config.Decision{config.Allow, config.Approve} {
		t.Run(string(base), func(t *testing.T) {
			w := &writer{mailbox: &mailbox{messages: messages("m1")}}
			s := &subjectScreener{verdicts: map[string]model.Verdict{"m1": model.Red}}
			policy := defaultPolicy
			policy.Base.MailTrash = base
			g, _, path := policyGuard(t, w, nil, s, policy)
			got, err := g.Trash(context.Background(), []string{"m1"})
			if err != nil || len(got) != 1 || s.count() != 1 {
				t.Fatalf("trash=%+v %v screenings=%d", got, err, s.count())
			}
			want := []string{"trash"}
			if base == config.Approve {
				want = nil
			}
			if !reflect.DeepEqual(w.calls, want) {
				t.Fatalf("trash=%+v writes=%v; want %v", got[0], w.calls, want)
			}
			records := policyRecords(t, path)
			if len(records) == 0 || records[0]["event"] != "policy" || records[0]["decision"] != string(base) || !reflect.DeepEqual(records[0]["reasons"], []any{"policy"}) {
				t.Fatalf("records=%v; want decision %s reasons [policy]", records, base)
			}
		})
	}
}

func TestFailedVerdictLookupCountsAsSealed(t *testing.T) {
	w := &writer{mailbox: &mailbox{}} // the message cannot be fetched
	g, _, _ := policyGuard(t, w, nil, &subjectScreener{}, defaultPolicy)
	got, err := g.Trash(context.Background(), []string{"m1"})
	if err != nil || !reflect.DeepEqual(got, []model.MutationResult{refusal("m1", "mail_trash")}) || len(w.calls) != 0 {
		t.Fatalf("trash=%+v %v writes=%v", got, err, w.calls)
	}
}

func TestSealedDecisionThatIsNotStricterSkipsTheLookup(t *testing.T) {
	sealedAllow := defaultPolicy
	sealedAllow.Sealed.MailTrash = config.Allow
	bothDeny := defaultPolicy
	bothDeny.Base.MailTrash, bothDeny.Base.MailDraft = config.Deny, config.Deny
	for name, tc := range map[string]struct {
		policy config.Policy
		trash  model.MutationResult
		draft  string // the refusal of a reply; empty skips the reply, which looks up its source unless denied
	}{
		"sealed allow": {sealedAllow, model.MutationResult{ID: "m1", OK: true}, ""},
		"both deny":    {bothDeny, refusal("m1", "mail_trash"), "refused by policy: mail_draft"},
	} {
		t.Run(name, func(t *testing.T) {
			b := &mailbox{messages: messages("m1")}
			w := &writer{mailbox: b}
			s := &subjectScreener{verdicts: map[string]model.Verdict{"m1": model.Sealed}}
			g, _, _ := policyGuard(t, w, nil, s, tc.policy)
			ctx := context.Background()
			got, err := g.Trash(ctx, []string{"m1"})
			if err != nil || !reflect.DeepEqual(got, []model.MutationResult{tc.trash}) {
				t.Fatalf("trash=%+v %v", got, err)
			}
			if tc.draft != "" {
				if _, _, err := g.DraftReply(ctx, "m1", model.Reply{Body: "reply"}); err == nil || err.Error() != tc.draft {
					t.Fatalf("reply=%v want %q", err, tc.draft)
				}
			}
			if s.count() != 0 || len(b.gets) != 0 {
				t.Fatalf("looked up the verdict: screenings=%d fetches=%v", s.count(), b.gets)
			}
		})
	}
}

func TestTrashDecidesEachIDOnItsOwn(t *testing.T) {
	ids := []string{"m0", "m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8", "m9"}
	w := &writer{mailbox: &mailbox{messages: messages(ids...)}}
	s := &subjectScreener{verdicts: map[string]model.Verdict{"m3": model.Sealed}}
	g, _, _ := policyGuard(t, w, nil, s, defaultPolicy)
	got, err := g.Trash(context.Background(), ids)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.MutationResult{}
	for _, id := range ids {
		if id == "m3" {
			want = append(want, refusal(id, "mail_trash"))
		} else {
			want = append(want, model.MutationResult{ID: id, OK: true})
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("results=%+v", got)
	}
	if !reflect.DeepEqual(w.trashed, []string{"m0", "m1", "m2", "m4", "m5", "m6", "m7", "m8", "m9"}) {
		t.Fatalf("provider trashed %v; want the 9 green messages", w.trashed)
	}
}

func TestTrashDenyRefusesEveryIDWithoutProviderAccess(t *testing.T) {
	ids := []string{"m0", "m1", "m2", "m3", "m4", "m5", "m6", "m7", "m8", "m9"}
	b := &mailbox{messages: messages(ids...)}
	w := &writer{mailbox: b}
	policy := defaultPolicy
	policy.Base.MailTrash = config.Deny
	g, _, _ := policyGuard(t, w, nil, &subjectScreener{}, policy)
	got, err := g.Trash(context.Background(), ids)
	if err != nil || len(got) != 10 {
		t.Fatalf("trash=%+v %v", got, err)
	}
	for i, r := range got {
		if !reflect.DeepEqual(r, refusal(ids[i], "mail_trash")) {
			t.Fatalf("result %d=%+v", i, r)
		}
	}
	if len(w.calls) != 0 || len(b.gets) != 0 {
		t.Fatalf("provider accessed: writes=%v fetches=%v", w.calls, b.gets)
	}
}

func TestTrashApproveStoresOnePendingItemPerID(t *testing.T) {
	w := &writer{mailbox: &mailbox{messages: messages("m1", "m2")}}
	policy := defaultPolicy
	policy.Base.MailTrash = config.Approve
	g, c, _ := policyGuard(t, w, nil, &subjectScreener{}, policy)
	ctx := context.Background()
	got, err := g.Trash(ctx, []string{"m1", "m2"})
	if err != nil || len(got) != 2 {
		t.Fatalf("trash=%+v %v", got, err)
	}
	if len(w.calls) != 0 {
		t.Fatalf("provider writes=%v", w.calls)
	}
	seen := map[string]bool{}
	for i, id := range []string{"m1", "m2"} {
		r := got[i]
		if r.ID != id || r.OK || r.Error == nil || *r.Error != "pending" || r.ApprovalID == nil || seen[*r.ApprovalID] {
			t.Fatalf("result %d=%+v", i, r)
		}
		seen[*r.ApprovalID] = true
		item, err := c.GetPending(ctx, *r.ApprovalID)
		if err != nil {
			t.Fatal(err)
		}
		if item.Account != "work" || item.Op != "mail.trash" || string(item.Args) != `{"ids":["`+id+`"]}` || item.State != cache.StatePending {
			t.Fatalf("stored %+v args=%s", item, item.Args)
		}
	}
}

func TestTrashRepeatedIDGivesOnePendingItem(t *testing.T) {
	w := &writer{mailbox: &mailbox{messages: messages("m1")}}
	policy := defaultPolicy
	policy.Base.MailTrash = config.Approve
	g, _, path := policyGuard(t, w, nil, &subjectScreener{}, policy)
	got, err := g.Trash(context.Background(), []string{"m1", "m1"})
	if err != nil || len(got) != 2 || got[0].ApprovalID == nil || !reflect.DeepEqual(got[0], got[1]) {
		t.Fatalf("trash=%+v %v", got, err)
	}
	created := 0
	for _, r := range policyRecords(t, path) {
		if r["event"] == "approval" {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("created %d pending items; want 1", created)
	}
}

func TestTrashProviderFailureKeepsThePolicyResults(t *testing.T) {
	w := &writer{mailbox: &mailbox{messages: messages("m1", "m2")}, err: errors.New("CANARY")}
	s := &subjectScreener{verdicts: map[string]model.Verdict{"m2": model.Sealed}}
	policy := defaultPolicy
	policy.Sealed.MailTrash = config.Approve
	g, _, path := policyGuard(t, w, nil, s, policy)
	got, err := g.Trash(context.Background(), []string{"m1", "m2"})
	if err != nil || len(got) != 2 || got[0].ID != "m1" || got[0].OK || got[0].Error == nil || *got[0].Error == "CANARY" || got[1].ApprovalID == nil {
		t.Fatalf("trash=%+v %v", got, err)
	}
	if records := auditRecords(t, path); len(records) != 1 || !reflect.DeepEqual(records[0]["ids"], []any{"m1", "m2"}) {
		t.Fatalf("audit=%v", records)
	}
}

func TestCalendarDefaultDenyRefusesBeforeProviderAccess(t *testing.T) {
	for _, op := range []string{"invite", "update", "cancel", "respond"} {
		t.Run(op, func(t *testing.T) {
			cal := meetingWriter()
			cal.event.IsOrganizer = op != "respond"
			g, _, path := policyGuard(t, &mailbox{}, cal, &subjectScreener{}, defaultPolicy)
			err := callCalendarWrite(context.Background(), g, op)
			if err == nil || err.Error() != "refused by policy: calendar_"+op {
				t.Fatalf("error=%v", err)
			}
			if cal.calls != 0 || len(cal.invites)+len(cal.ids) != 0 || len(auditRecords(t, path)) != 0 {
				t.Fatalf("provider accessed: reads=%d writes=%d", cal.calls, len(cal.invites)+len(cal.ids))
			}
		})
	}
}

func TestCalendarApproveStoresOnlyAWritableAction(t *testing.T) {
	approve := defaultPolicy
	approve.Base.CalendarInvite, approve.Base.CalendarUpdate, approve.Base.CalendarCancel, approve.Base.CalendarRespond = config.Approve, config.Approve, config.Approve, config.Approve
	for _, op := range []string{"update", "cancel", "respond"} {
		t.Run(op+"/recurring", func(t *testing.T) {
			cal := meetingWriter()
			cal.event.IsOrganizer = op != "respond"
			cal.event.Recurrence = new("weekly")
			g, _, path := policyGuard(t, &mailbox{}, cal, &subjectScreener{}, approve)
			err := callCalendarWrite(context.Background(), g, op)
			if err == nil || err.Error() != "calendar write refused: recurring events are not supported" {
				t.Fatalf("error=%v", err)
			}
			if len(cal.ids) != 0 || len(policyRecords(t, path)) != 0 {
				t.Fatalf("refused action was written or queued: writes=%v records=%v", cal.ids, policyRecords(t, path))
			}
		})
		t.Run(op+"/writable", func(t *testing.T) {
			cal := meetingWriter()
			cal.event.IsOrganizer = op != "respond"
			g, c, _ := policyGuard(t, &mailbox{}, cal, &subjectScreener{}, approve)
			err := callCalendarWrite(context.Background(), g, op)
			var pending guard.PendingError
			if !errors.As(err, &pending) {
				t.Fatalf("error=%v; want pending", err)
			}
			item, err := c.GetPending(context.Background(), pending.ApprovalID)
			if err != nil || item.Op != "calendar."+op || item.State != cache.StatePending {
				t.Fatalf("stored %+v %v", item, err)
			}
			if cal.calls != 1 || len(cal.ids) != 0 {
				t.Fatalf("want one event check and no write: reads=%d writes=%v", cal.calls, cal.ids)
			}
		})
	}
}

func TestRuleTrashFollowsThePolicy(t *testing.T) {
	trashRule := rules.Rules{Rules: []rules.Rule{{Name: "old", Kind: rules.Folder, Target: "trash", Instructions: "Old"}}}
	for _, tc := range []struct {
		decision config.Decision
		outcome  string
	}{
		{config.Deny, "refused by policy: mail_trash"},
		{config.Approve, "pending"},
	} {
		t.Run(string(tc.decision), func(t *testing.T) {
			b, s := organizerSetup(t)
			s.result.Organization.Folder.Choice = "old"
			s.result.Organization.Folder.Probabilities = map[string]float64{"old": .6, "stay": .4}
			s.result.Organization.Categories = map[string]float64{}
			policy := defaultPolicy
			policy.Base.MailTrash = tc.decision
			g, c, path := policyGuard(t, b, nil, s, policy)
			ctx := context.Background()
			approvals := []string{}
			for run := range 2 {
				report, err := g.Organize(ctx, now.Add(-7*24*time.Hour), trashRule, now, true, nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(report.Planned) != 1 || report.Planned[0].Move == nil || report.Planned[0].Move.Result == nil || *report.Planned[0].Move.Result != tc.outcome {
					t.Fatalf("run %d outcome: %+v", run, report.Planned)
				}
				if report.Stats.Applied != 0 || report.Stats.Failed != 1 {
					t.Fatalf("run %d stats: %+v", run, report.Stats)
				}
			}
			if len(b.calls) != 0 {
				t.Fatalf("rule trash wrote: %v", b.calls)
			}
			for _, r := range policyRecords(t, path) {
				if r["event"] == "approval" {
					approvals = append(approvals, r["approval_id"].(string))
				}
			}
			if tc.decision == config.Deny {
				if len(approvals) != 0 {
					t.Fatalf("denied rule write stored items: %v", approvals)
				}
				return
			}
			if len(approvals) != 2 || approvals[0] == approvals[1] {
				t.Fatalf("want one new pending item per run: %v", approvals)
			}
			for _, id := range approvals {
				item, err := c.GetPending(ctx, id)
				if err != nil || item.Op != "mail.trash" || string(item.Args) != `{"ids":["m1"]}` {
					t.Fatalf("stored %+v %v", item, err)
				}
			}
		})
	}
}

func TestPolicyAuditRecordsHoldIDsAndCodesOnly(t *testing.T) {
	w := &writer{mailbox: &mailbox{messages: messages("m1", "m2", "m3")}}
	s := &subjectScreener{verdicts: map[string]model.Verdict{"m2": model.Sealed}}
	policy := defaultPolicy
	policy.Base.MailTrash, policy.Sealed.MailTrash = config.Allow, config.Approve
	policy.Base.MailDraft = config.Approve
	g, _, path := policyGuard(t, w, nil, s, policy)
	ctx := context.Background()
	got, err := g.Trash(ctx, []string{"m1", "m2"})
	if err != nil || len(got) != 2 || got[1].ApprovalID == nil {
		t.Fatalf("trash=%+v %v", got, err)
	}
	trashApproval := *got[1].ApprovalID
	_, err = g.DraftNew(ctx, model.NewDraft{To: []string{"CANARY@example.test"}, Subject: "CANARY", Body: "CANARY"})
	var pending guard.PendingError
	if !errors.As(err, &pending) {
		t.Fatalf("draft=%v", err)
	}
	policy.Base.MailDraft = config.Deny
	g, _, path2 := policyGuard(t, w, nil, s, policy)
	if _, _, err := g.DraftReply(ctx, "m3", model.Reply{Body: "CANARY"}); err == nil {
		t.Fatal("denied reply ran")
	}
	strip := func(records []map[string]any) []map[string]any {
		for _, r := range records {
			if _, err := time.Parse(time.RFC3339, r["time"].(string)); err != nil {
				t.Fatalf("time=%v", r["time"])
			}
			delete(r, "time")
		}
		return records
	}
	want := []map[string]any{
		{"account": "work", "event": "policy", "operation": "mail.trash", "key": "mail_trash", "decision": "approve", "reasons": []any{"policy", "sealed"}, "id": "m2", "approval_id": trashApproval},
		{"account": "work", "event": "approval", "operation": "mail.trash", "state": "created", "approval_id": trashApproval},
		{"account": "work", "event": "policy", "operation": "mail.trash", "key": "mail_trash", "decision": "allow", "reasons": []any{"policy"}, "id": "m1"},
		{"account": "work", "event": "policy", "operation": "draft.new", "key": "mail_draft", "decision": "approve", "reasons": []any{"policy"}, "approval_id": pending.ApprovalID},
		{"account": "work", "event": "approval", "operation": "draft.new", "state": "created", "approval_id": pending.ApprovalID},
	}
	if got := strip(policyRecords(t, path)); !reflect.DeepEqual(got, want) {
		t.Fatalf("records=%v\nwant   %v", got, want)
	}
	denied := []map[string]any{{"account": "work", "event": "policy", "operation": "draft.reply", "key": "mail_draft", "decision": "deny", "reasons": []any{"policy"}, "id": "m3"}}
	if got := strip(policyRecords(t, path2)); !reflect.DeepEqual(got, denied) {
		t.Fatalf("records=%v\nwant   %v", got, denied)
	}
	trash := auditRecords(t, path)
	results := []any{map[string]any{"id": "m1", "ok": true, "error": nil}, map[string]any{"id": "m2", "ok": false, "error": "pending"}}
	if len(trash) != 1 || trash[0]["operation"] != "trash" || !reflect.DeepEqual(trash[0]["ids"], []any{"m1", "m2"}) || !reflect.DeepEqual(trash[0]["results"], results) {
		t.Fatalf("trash record=%v", trash)
	}
}

func TestPolicyAuditFailureFailsClosed(t *testing.T) {
	for _, decision := range []config.Decision{config.Deny, config.Approve} {
		t.Run(string(decision), func(t *testing.T) {
			w := &writer{mailbox: &mailbox{messages: messages("m1")}}
			policy := defaultPolicy
			policy.Base.MailTrash, policy.Base.MailDraft = decision, decision
			g, _, path := policyGuard(t, w, nil, &subjectScreener{}, policy)
			if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if _, err := g.Trash(ctx, []string{"m1"}); err == nil || err.Error() != "AuditError" {
				t.Fatalf("trash=%v", err)
			}
			if _, err := g.DraftNew(ctx, model.NewDraft{To: []string{"a@example.test"}}); err == nil || err.Error() != "AuditError" {
				t.Fatalf("draft=%v", err)
			}
			if len(w.calls) != 0 {
				t.Fatalf("provider writes=%v", w.calls)
			}
		})
	}
}

// A draft update reads its message unless the policy denies it, and refuses one that is not a draft with the fixed
// public text before the Jev check, any pending item, any record, and any write. A failed read fails closed with a
// safe code. A change without a field, or with HTML but no body, is refused before the read.
func TestDraftUpdateRefusesAMessageThatIsNotADraftBeforeAnyWrite(t *testing.T) {
	body := model.DraftChange{Body: new("CANARY body")}
	for _, tc := range []struct {
		name     string
		decision config.Decision
		w        *writer
		change   model.DraftChange
		want     string
		reads    int
	}{
		{"not a draft", config.Allow, &writer{notDraft: true}, body, "draft update refused: not a draft", 1},
		{"not a draft under approve", config.Approve, &writer{notDraft: true}, body, "draft update refused: not a draft", 1},
		{"failed read", config.Allow, &writer{draftErr: model.MailboxError("CANARY provider text")}, body, "MailboxError", 1},
		{"no field", config.Allow, &writer{}, model.DraftChange{}, "invalid request", 0},
		{"html without body", config.Allow, &writer{}, model.DraftChange{Subject: new("x"), HTML: true}, "invalid request", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.w.mailbox = &mailbox{}
			s := scoring(0.08)
			policy := allowAll
			policy.Base.MailDraft = tc.decision
			g, c, path := policyGuard(t, tc.w, nil, s, policy)
			ctx := context.Background()
			if _, err := g.DraftUpdate(ctx, "m1", tc.change); err == nil || err.Error() != tc.want {
				t.Fatalf("err=%v want %q", err, tc.want)
			}
			if tc.w.draftReads != tc.reads || len(tc.w.calls) != 0 || len(s.checked()) != 0 {
				t.Fatalf("reads=%d writes=%v checks=%v", tc.w.draftReads, tc.w.calls, s.checked())
			}
			if items, err := c.ListPending(ctx, 10); err != nil || len(items) != 0 {
				t.Fatalf("pending=%v %v", items, err)
			}
			if got := allAuditRecords(t, path); len(got) != 0 {
				t.Fatalf("records=%v", got)
			}
		})
	}
	// A denied update reads nothing.
	w := &writer{mailbox: &mailbox{}, notDraft: true}
	g, _, _ := policyGuard(t, w, nil, scoring(0.08), config.Policy{})
	if _, err := g.DraftUpdate(context.Background(), "m1", body); err == nil || err.Error() != "refused by policy: mail_draft" || w.draftReads != 0 {
		t.Fatalf("denied update: %v reads=%d", err, w.draftReads)
	}
}

// A draft delete (mail_draft) and a draft send (mail_send) read their message unless the policy denies them, and
// refuse one that is not a draft with their fixed public text before the Jev check, any pending item, any record, and
// any write. A failed read fails closed with a safe code. An approved run reads the draft again, so an approved send
// of a draft that is gone or is no longer a draft sends nothing.
func TestDraftDeleteAndSendRefuseAMessageThatIsNotADraftBeforeAnyWrite(t *testing.T) {
	for _, op := range []struct {
		name, refusal, denied string
		call                  func(context.Context, *guard.Guard) error
		set                   func(*config.Policy, config.Decision)
	}{
		{"delete", "draft delete refused: not a draft", "refused by policy: mail_draft", func(ctx context.Context, g *guard.Guard) error { return g.DraftDelete(ctx, "m1") },
			func(p *config.Policy, d config.Decision) { p.Base.MailDraft = d }},
		{"send", "draft send refused: not a draft", "refused by policy: mail_send", func(ctx context.Context, g *guard.Guard) error { return g.DraftSend(ctx, "m1") },
			func(p *config.Policy, d config.Decision) { p.Base.MailSend = d }},
	} {
		for _, tc := range []struct {
			name     string
			decision config.Decision
			approved bool
			w        *writer
			want     string
		}{
			{"not a draft", config.Allow, false, &writer{notDraft: true}, op.refusal},
			{"not a draft under approve", config.Approve, false, &writer{notDraft: true}, op.refusal},
			{"failed read", config.Allow, false, &writer{draftErr: model.MailboxError("CANARY provider text")}, "MailboxError"},
			{"approved run of a message that is no longer a draft", config.Deny, true, &writer{notDraft: true}, op.refusal},
			{"approved run of a draft that is gone", config.Deny, true, &writer{draftErr: model.MailboxError("NotFoundError")}, "MailboxError"},
		} {
			t.Run(op.name+" "+tc.name, func(t *testing.T) {
				tc.w.mailbox = &mailbox{}
				s := scoring(0.08)
				policy := allowAll
				op.set(&policy, tc.decision)
				g, c, path := policyGuard(t, tc.w, nil, s, policy)
				ctx := context.Background()
				if tc.approved {
					ctx = guard.WithApproval(ctx)
				}
				if err := op.call(ctx, g); err == nil || err.Error() != tc.want {
					t.Fatalf("err=%v want %q", err, tc.want)
				}
				if tc.w.draftReads != 1 || len(tc.w.calls) != 0 || len(s.checked()) != 0 {
					t.Fatalf("reads=%d writes=%v checks=%v", tc.w.draftReads, tc.w.calls, s.checked())
				}
				if items, err := c.ListPending(ctx, 10); err != nil || len(items) != 0 {
					t.Fatalf("pending=%v %v", items, err)
				}
				if got := allAuditRecords(t, path); len(got) != 0 {
					t.Fatalf("records=%v", got)
				}
			})
		}
		t.Run(op.name+" denied", func(t *testing.T) {
			w := &writer{mailbox: &mailbox{}, notDraft: true}
			g, _, _ := policyGuard(t, w, nil, scoring(0.08), config.Policy{})
			if err := op.call(context.Background(), g); err == nil || err.Error() != op.denied || w.draftReads != 0 || len(w.calls) != 0 {
				t.Fatalf("denied: %v reads=%d writes=%v", err, w.draftReads, w.calls)
			}
		})
	}
}
