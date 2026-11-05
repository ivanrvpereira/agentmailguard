package guard_test

import (
	"reflect"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

func TestSpamDecidesEachID(t *testing.T) {
	for _, base := range []config.Decision{config.Allow, config.Approve, config.Deny} {
		t.Run(string(base), func(t *testing.T) {
			w := &writer{mailbox: &mailbox{messages: messages("m1", "m2", "sealed", "error", "red")}}
			policy := config.DefaultPolicy
			policy.Base.MailSpam = base
			g, c, path := policyGuard(t, w, nil, &subjectScreener{verdicts: map[string]model.Verdict{"sealed": model.Sealed, "error": model.Error, "red": model.Red}}, policy)
			ids := []string{"m1", "m2", "sealed", "error", "red"}
			got, err := g.Spam(t.Context(), ids)
			if err != nil || len(got) != len(ids) {
				t.Fatalf("spam=%+v %v", got, err)
			}
			seen := map[string]bool{}
			for i, id := range ids {
				r := got[i]
				switch {
				case base == config.Deny || id == "sealed" || id == "error":
					if !reflect.DeepEqual(r, refusal(id, "mail_spam")) {
						t.Fatalf("refusal=%+v", r)
					}
				case base == config.Allow:
					if !reflect.DeepEqual(r, model.MutationResult{ID: id, OK: true}) {
						t.Fatalf("allowed=%+v", r)
					}
				default:
					if r.ID != id || r.OK || r.Error == nil || *r.Error != "pending" || r.ApprovalID == nil || seen[*r.ApprovalID] {
						t.Fatalf("pending=%+v", r)
					}
					seen[*r.ApprovalID] = true
					item, err := c.GetPending(t.Context(), *r.ApprovalID)
					if err != nil || item.Op != "mail.spam" || string(item.Args) != `{"ids":["`+id+`"]}` || item.State != cache.StatePending {
						t.Fatalf("pending item=%+v args=%s err=%v", item, item.Args, err)
					}
				}
			}
			want := []string(nil)
			if base == config.Allow {
				want = []string{"m1", "m2", "red"}
			}
			if !reflect.DeepEqual(w.spammed, want) {
				t.Fatalf("provider=%v want=%v", w.spammed, want)
			}
			for _, r := range policyRecords(t, path) {
				if r["event"] == "policy" && r["key"] != "mail_spam" {
					t.Fatalf("policy=%v", r)
				}
			}
		})
	}
}

// A repeated ID is written once and every position gets the provider's result, so a spam that moved m1 never
// reports it failed.
func TestRepeatedIDIsWrittenOnceAndReportedAtEveryPosition(t *testing.T) {
	w := &writer{mailbox: &mailbox{messages: messages("m1", "m2")}}
	g, _, path := setup(t, w, &screener{verdict: model.Green})
	got, err := g.Spam(t.Context(), []string{"m1", "m2", "m1"})
	want := []model.MutationResult{{ID: "m1", OK: true}, {ID: "m2", OK: true}, {ID: "m1", OK: true}}
	if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(w.spammed, []string{"m1", "m2"}) {
		t.Fatalf("results=%+v err=%v spammed=%v", got, err, w.spammed)
	}
	if records := auditRecords(t, path); len(records) != 1 || len(records[0]["results"].([]any)) != 3 {
		t.Fatalf("records=%v", records)
	}
}

// A conversation spam moves the messages of the conversation in its folder to spam, not trash, and its record is a spam.
func TestSpamConversationCallsSpam(t *testing.T) {
	w := conversationWriter()
	g, _, path := setup(t, w, &screener{verdict: model.Green})
	got, err := g.SpamConversation(t.Context(), "t1", "inbox")
	if want := []model.MutationResult{{ID: "m2", OK: true}, {ID: "m1", OK: true}}; err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("results=%+v err=%v", got, err)
	}
	if !reflect.DeepEqual(w.calls, []string{"spam"}) || !reflect.DeepEqual(w.spammed, []string{"m2", "m1"}) || w.trashed != nil {
		t.Fatalf("writes=%v spammed=%v trashed=%v", w.calls, w.spammed, w.trashed)
	}
	if records := auditRecords(t, path); len(records) != 1 || records[0]["operation"] != "spam" || records[0]["thread"] != "t1" {
		t.Fatalf("records=%v", records)
	}
}
