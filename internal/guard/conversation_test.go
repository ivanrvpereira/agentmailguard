package guard_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// conversationWriter chooses m2 and m1 (newest first) for conversation t1 in the Inbox, and m3 in folder f2.
func conversationWriter() *writer {
	return &writer{
		mailbox: &mailbox{messages: messages("m1", "m2", "m3")},
		chosen:  map[string][]string{"t1/inbox": {"m2", "m1"}, "t1/f2": {"m3"}},
	}
}

func TestConversationActionsChangeTheMessagesInTheFolderAndRecordTheThread(t *testing.T) {
	w := conversationWriter()
	g, _, path := setup(t, w, &screener{verdict: model.Green})
	ctx := context.Background()
	calls := []struct {
		op  string
		run func() ([]model.MutationResult, error)
	}{
		{"archive", func() ([]model.MutationResult, error) { return g.ArchiveConversation(ctx, "t1", "inbox") }},
		{"trash", func() ([]model.MutationResult, error) { return g.TrashConversation(ctx, "t1", "inbox") }},
		{"move", func() ([]model.MutationResult, error) { return g.MoveConversation(ctx, "t1", "inbox", "f1") }},
		{"read", func() ([]model.MutationResult, error) { return g.MarkReadConversation(ctx, "t1", "inbox", true) }},
		{"unread", func() ([]model.MutationResult, error) { return g.MarkReadConversation(ctx, "t1", "inbox", false) }},
		{"categorize", func() ([]model.MutationResult, error) {
			return g.CategorizeConversation(ctx, "t1", "inbox", []string{"Work"}, nil)
		}},
	}
	for _, c := range calls {
		got, err := c.run()
		if want := []model.MutationResult{{ID: "m2", OK: true}, {ID: "m1", OK: true}}; err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: results=%+v err=%v", c.op, got, err)
		}
	}
	if want := []string{"archive", "trash", "move", "read", "unread", "categorize"}; !reflect.DeepEqual(w.calls, want) {
		t.Fatalf("writes=%v", w.calls)
	}
	records := auditRecords(t, path)
	if len(records) != len(calls) {
		t.Fatalf("records=%v", records)
	}
	for i, r := range records {
		if r["operation"] != calls[i].op || r["thread"] != "t1" || !reflect.DeepEqual(r["ids"], []any{"m2", "m1"}) {
			t.Fatalf("record %d=%v", i, r)
		}
	}
	// A move from another folder takes the messages from that folder; a move by ID takes them from the Inbox.
	if got, err := g.MoveConversation(ctx, "t1", "f2", "f1"); err != nil || !reflect.DeepEqual(got, []model.MutationResult{{ID: "m3", OK: true}}) {
		t.Fatalf("move from f2: results=%+v err=%v", got, err)
	}
	if _, err := g.Move(ctx, []string{"m1"}, "f1"); err != nil {
		t.Fatal(err)
	}
	if want := []string{"inbox", "f2", "inbox"}; !reflect.DeepEqual(w.moveSources, want) {
		t.Fatalf("move sources=%v", w.moveSources)
	}
	if r := auditRecords(t, path); r[len(r)-1]["thread"] != nil {
		t.Fatalf("move by ID recorded a thread: %v", r[len(r)-1])
	}
}

func TestConversationActionWithNoMessagesInTheFolderChangesNothing(t *testing.T) {
	w := conversationWriter()
	g, _, path := setup(t, w, &screener{verdict: model.Green})
	got, err := g.ArchiveConversation(context.Background(), "t1", "f1")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("results=%#v err=%v", got, err)
	}
	if len(w.calls) != 0 || len(allAuditRecords(t, path)) != 0 {
		t.Fatalf("writes=%v records=%v", w.calls, allAuditRecords(t, path))
	}
}

func TestFailedConversationReadChangesNothing(t *testing.T) {
	w := conversationWriter()
	w.chooseErr = model.MailboxError("NotFoundError")
	g, _, path := setup(t, w, &screener{verdict: model.Green})
	// The error is the one of an unknown message ID: the safe code of the provider error.
	if got, err := g.TrashConversation(context.Background(), "gone", "inbox"); err != guard.GuardError("MailboxError") || got != nil {
		t.Fatalf("results=%+v err=%v", got, err)
	}
	if len(w.calls) != 0 || len(allAuditRecords(t, path)) != 0 {
		t.Fatalf("writes=%v records=%v", w.calls, allAuditRecords(t, path))
	}
}

// The refusals of a run on message IDs apply also when the folder holds no message of the conversation (f1).
func TestConversationActionsKeepTheFixedRefusals(t *testing.T) {
	w := conversationWriter()
	g, _, path := setup(t, w, &screener{verdict: model.Green})
	ctx := context.Background()
	for _, in := range []string{"inbox", "f1"} {
		for want, run := range map[string]func() ([]model.MutationResult, error){
			"move refused: use mail.trash": func() ([]model.MutationResult, error) { return g.MoveConversation(ctx, "t1", in, "del") },
			"move refused: the destination must be a folder ID from `folders`": func() ([]model.MutationResult, error) {
				return g.MoveConversation(ctx, "t1", in, "nowhere")
			},
			"categorize refused: not in the category list": func() ([]model.MutationResult, error) {
				return g.CategorizeConversation(ctx, "t1", in, []string{"Planted"}, nil)
			},
			"categorize needs at least one category to add or remove": func() ([]model.MutationResult, error) {
				return g.CategorizeConversation(ctx, "t1", in, nil, nil)
			},
		} {
			if got, err := run(); err != guard.GuardError(want) || got != nil {
				t.Fatalf("in %s, want %q: results=%+v err=%v", in, want, got, err)
			}
		}
	}
	if len(w.calls) != 0 || len(allAuditRecords(t, path)) != 0 {
		t.Fatalf("writes=%v records=%v", w.calls, allAuditRecords(t, path))
	}
}

func TestConversationActionNeedsAThreadAndAFolder(t *testing.T) {
	w := conversationWriter()
	g, _, path := setup(t, w, &screener{verdict: model.Green})
	for _, target := range [][2]string{{"", "inbox"}, {"t1", ""}} {
		if got, err := g.ArchiveConversation(context.Background(), target[0], target[1]); err != guard.GuardError("give a thread ID and a folder ID") || got != nil {
			t.Fatalf("%q: results=%+v err=%v", target, got, err)
		}
	}
	if len(w.choices) != 0 || len(w.calls) != 0 || len(allAuditRecords(t, path)) != 0 {
		t.Fatalf("choices=%v writes=%v records=%v", w.choices, w.calls, allAuditRecords(t, path))
	}
}

// The provider's IDs pass the same checks as an agent's: an empty or repeated ID fails the whole action.
func TestConversationWithAnInvalidChoiceChangesNothing(t *testing.T) {
	for name, ids := range map[string][]string{"empty": {"m1", ""}, "repeated": {"m1", "m1"}} {
		w := conversationWriter()
		w.chosen["t1/inbox"] = ids
		g, _, path := setup(t, w, &screener{verdict: model.Green})
		if got, err := g.ArchiveConversation(context.Background(), "t1", "inbox"); err == nil || got != nil {
			t.Fatalf("%s: results=%+v err=%v", name, got, err)
		}
		if len(w.calls) != 0 || len(allAuditRecords(t, path)) != 0 {
			t.Fatalf("%s: writes=%v", name, w.calls)
		}
	}
}

func TestConversationTrashDecidesAndHoldsEachMessageOnItsOwn(t *testing.T) {
	w := conversationWriter()
	w.chosen["t1/inbox"] = []string{"m3", "m2", "m1"}
	policy := defaultPolicy
	policy.Base.MailTrash = config.Approve
	s := &subjectScreener{verdicts: map[string]model.Verdict{"m2": model.Sealed}}
	g, c, path := policyGuard(t, w, nil, s, policy)
	ctx := context.Background()
	got, err := g.TrashConversation(ctx, "t1", "inbox")
	if err != nil || len(got) != 3 {
		t.Fatalf("results=%+v err=%v", got, err)
	}
	if !reflect.DeepEqual(got[1], refusal("m2", "mail_trash")) {
		t.Fatalf("sealed m2=%+v", got[1])
	}
	if len(w.calls) != 0 {
		t.Fatalf("provider writes=%v", w.calls)
	}
	for _, i := range []int{0, 2} {
		r := got[i]
		if r.Error == nil || *r.Error != "pending" || r.ApprovalID == nil {
			t.Fatalf("result %d=%+v", i, r)
		}
		item, err := c.GetPending(ctx, *r.ApprovalID)
		if err != nil || item.Op != "mail.trash" || string(item.Args) != `{"ids":["`+r.ID+`"]}` || item.State != cache.StatePending {
			t.Fatalf("stored %+v args=%s err=%v", item, item.Args, err)
		}
	}
	decisions := 0
	for _, r := range policyRecords(t, path) {
		if r["event"] == "policy" {
			decisions++
			if r["thread"] != nil {
				t.Fatalf("policy record has a thread: %v", r)
			}
		}
	}
	if decisions != 3 {
		t.Fatalf("policy records=%d, want one per message", decisions)
	}
	if r := auditRecords(t, path); len(r) != 1 || r[0]["thread"] != "t1" || !reflect.DeepEqual(r[0]["ids"], []any{"m3", "m2", "m1"}) {
		t.Fatalf("operation records=%v", r)
	}
	// An approved run trashes the message of its approval only, by ID.
	if _, err := g.Trash(guard.WithApproval(ctx), []string{"m3"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(w.trashed, []string{"m3"}) {
		t.Fatalf("trashed=%v", w.trashed)
	}
	if r := auditRecords(t, path); r[len(r)-1]["thread"] != nil {
		t.Fatalf("approved run recorded a thread: %v", r[len(r)-1])
	}
}

func TestConversationActionCancelledBeforeTheLockReadsNothing(t *testing.T) {
	w := conversationWriter()
	g, _, _ := setup(t, w, &screener{verdict: model.Green})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.ArchiveConversation(ctx, "t1", "inbox"); !errors.Is(err, context.Canceled) || len(w.choices) != 0 {
		t.Fatalf("err=%v choices=%v", err, w.choices)
	}
}

// A cancel during the read of the conversation ends the action with the cancel, also when nothing is to change.
func TestConversationActionCancelledDuringTheReadChangesNothing(t *testing.T) {
	for _, in := range []string{"inbox", "f1"} {
		w := conversationWriter()
		g, _, path := setup(t, w, &screener{verdict: model.Green})
		ctx, cancel := context.WithCancel(context.Background())
		w.afterChoose = cancel
		if got, err := g.ArchiveConversation(ctx, "t1", in); !errors.Is(err, context.Canceled) || got != nil {
			t.Fatalf("in %s: results=%+v err=%v", in, got, err)
		}
		if len(w.calls) != 0 || len(allAuditRecords(t, path)) != 0 {
			t.Fatalf("in %s: writes=%v records=%v", in, w.calls, allAuditRecords(t, path))
		}
	}
}

// The provider chooses the messages under the write lock: while another write holds it, nothing is read.
func TestConversationChoiceWaitsForTheWriteLock(t *testing.T) {
	w := conversationWriter()
	w.entered, w.release = make(chan struct{}), make(chan struct{})
	g, _, _ := setup(t, w, &screener{verdict: model.Green})
	done := make(chan error, 1)
	go func() { _, err := g.Archive(context.Background(), []string{"m1"}); done <- err }()
	<-w.entered
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := g.TrashConversation(ctx, "t1", "inbox"); !errors.Is(err, context.DeadlineExceeded) || len(w.choices) != 0 {
		t.Fatalf("err=%v choices=%v", err, w.choices)
	}
	close(w.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
