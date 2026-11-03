package guard_test

import (
	"reflect"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

func TestSpamDecidesEachIDAndRefusesClosedMessagesFirst(t *testing.T) {
	for _, base := range []config.Decision{config.Allow, config.Approve, config.Deny} {
		t.Run(string(base), func(t *testing.T) {
			closed := message("closed")
			closed.Folders = []string{"trash"}
			w := &writer{mailbox: &mailbox{messages: append(messages("m1", "m2", "sealed", "error", "red"), closed)}}
			policy := config.DefaultPolicy
			policy.Base.MailSpam = base
			g, c, path := policyGuard(t, w, nil, &subjectScreener{verdicts: map[string]model.Verdict{"sealed": model.Sealed, "error": model.Error, "red": model.Red}}, policy)
			ids := []string{"m1", "m2", "sealed", "error", "red", "closed"}
			got, err := g.Spam(t.Context(), ids)
			if err != nil || len(got) != len(ids) {
				t.Fatalf("spam=%+v %v", got, err)
			}
			seen := map[string]bool{}
			for i, id := range ids {
				r := got[i]
				switch {
				case id == "closed":
					if !reflect.DeepEqual(r, model.MutationResult{ID: id, Error: new(string(guard.MessageTrash))}) {
						t.Fatalf("closed=%+v", r)
					}
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
				if r["event"] == "policy" && (r["key"] != "mail_spam" || r["id"] == "closed") {
					t.Fatalf("policy=%v", r)
				}
			}
		})
	}
}
