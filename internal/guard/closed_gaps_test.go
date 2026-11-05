package guard_test

import (
	"reflect"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// A failed, empty, or invalid closed-folder lookup refuses every read and change before any message read.
func TestClosedFolderLookupFailsClosedBeforeAnyMessageRead(t *testing.T) {
	for name, lookup := range map[string]func() (map[string]model.Bin, error){
		"error":       func() (map[string]model.Bin, error) { return nil, model.MailboxError("Graph error 500") },
		"empty":       func() (map[string]model.Bin, error) { return map[string]model.Bin{}, nil },
		"invalid bin": func() (map[string]model.Bin, error) { return map[string]model.Bin{"trash": "archive"}, nil },
		"empty ID":    func() (map[string]model.Bin, error) { return map[string]model.Bin{"": model.Trash}, nil },
	} {
		t.Run(name, func(t *testing.T) {
			w := &writer{mailbox: &mailbox{messages: messages("m1"), closed: lookup}}
			s := &screener{verdict: model.Green}
			g, _, path := setup(t, w, s)
			ctx := t.Context()
			for op, call := range map[string]func() error{
				"list": func() error { _, err := g.List(ctx, "inbox", model.PageRequest{}); return err },
				"search": func() error {
					_, err := g.Search(ctx, model.SearchQuery{Text: new("x"), Folder: new("inbox")}, model.PageRequest{})
					return err
				},
				"get":   func() error { _, err := g.Get(ctx, "m1"); return err },
				"trash": func() error { _, err := g.Trash(ctx, []string{"m1"}); return err },
				"move":  func() error { _, err := g.Move(ctx, []string{"m1"}, "f1"); return err },
			} {
				if err := call(); err == nil {
					t.Errorf("%s ran", op)
				}
			}
			if len(w.gets) != 0 || len(w.pages) != 0 || s.calls != 0 || len(w.calls) != 0 {
				t.Fatalf("reads=%v pages=%v screens=%d writes=%v", w.gets, w.pages, s.calls, w.calls)
			}
			if records := allAuditRecords(t, path); len(records) != 0 {
				t.Fatalf("records=%v", records)
			}
		})
	}
}

// A read that fetches the body a second time checks the second copy again: the message may have moved to a closed
// folder between the two reads. A cached hidden verdict makes the first read metadata only; BCC makes it rescreen.
func TestBodyRefetchChecksTheClosedFoldersAgain(t *testing.T) {
	m := message("m1")
	m.BCC = []model.Address{{Address: "bcc@example.test"}}
	b := &mailbox{messages: []model.Message{m}, moved: map[string]string{"m1": "trash"}}
	s := &screener{verdict: model.Green}
	g, c, _ := setup(t, b, s)
	if err := c.Put(t.Context(), "work", cache.Row{MessageID: "m1", ReceivedAt: now, ScreenedAt: now, Verdict: model.Red, Score: new(0.9)}); err != nil {
		t.Fatal(err)
	}
	if v, err := g.Get(t.Context(), "m1"); err != guard.MessageTrash || v.ID != "" || s.calls != 0 || len(b.gets) != 2 || b.gets[0].body {
		t.Fatalf("get=%+v err=%v screens=%d reads=%v", v, err, s.calls, b.gets)
	}
}

// A page message whose body fetch shows it in a closed folder is left out unscreened; the page keeps its marker.
func TestPageBodyFetchLeavesOutAMessageThatMovedToAClosedFolder(t *testing.T) {
	b := &mailbox{messages: messages("open", "moved"), moved: map[string]string{"moved": "spam"}, next: "n2"}
	s := &subjectScreener{}
	g, _, _ := setup(t, b, s)
	got, err := g.List(t.Context(), "inbox", model.PageRequest{})
	if err != nil || len(got.Messages) != 1 || got.Messages[0].ID != "open" || got.NextPage != "n2" || !reflect.DeepEqual(s.screened, []string{"open"}) {
		t.Fatalf("page=%+v err=%v screened=%v", got, err, s.screened)
	}
}

// A search without a folder leaves out the closed messages of its page and keeps the next-page marker, also when no
// message is left.
func TestSearchWithoutAFolderLeavesOutClosedMessages(t *testing.T) {
	inTrash, inSpam := message("binned"), message("junk")
	inTrash.Folders, inSpam.Folders = []string{"trash"}, []string{"spam"}
	for name, tc := range map[string]struct {
		messages []model.Message
		want     []string
	}{
		"mixed":       {append(messages("open"), inTrash, inSpam), []string{"open"}},
		"only closed": {[]model.Message{inTrash, inSpam}, []string{}},
	} {
		t.Run(name, func(t *testing.T) {
			s := &subjectScreener{}
			g, _, _ := setup(t, &mailbox{messages: tc.messages, next: "n2"}, s)
			got, err := g.Search(t.Context(), model.SearchQuery{Text: new("x")}, model.PageRequest{})
			ids := []string{}
			for _, m := range got.Messages {
				ids = append(ids, m.ID)
			}
			if err != nil || got.Messages == nil || !reflect.DeepEqual(ids, tc.want) || got.NextPage != "n2" || s.count() != len(tc.want) {
				t.Fatalf("page=%+v err=%v screened=%v", got, err, s.screened)
			}
		})
	}
}

// The organizer refuses a message that moved to a closed folder after the scan: the fetch shows it in trash, so it
// gets the code closed, without its cached row, and its body never reaches the screener. One closed-folder lookup
// serves the whole call.
func TestClassifyRefusesAMessageThatMovedToAClosedFolder(t *testing.T) {
	binned := message("binned")
	binned.Folders = []string{"trash"}
	b := &mailbox{messages: append(messages("m1"), binned)}
	s := &classifier{screener: &screener{verdict: model.Green}, result: answer(model.Green)}
	g, c, _ := setup(t, b, s)
	if err := c.Put(t.Context(), "work", cache.Row{MessageID: "binned", ReceivedAt: now, ScreenedAt: now, Verdict: model.Error, Error: new("Error")}); err != nil {
		t.Fatal(err)
	}
	got, err := g.Classify(t.Context(), []string{"m1", "binned"}, orgRules())
	if err != nil || len(got) != 2 || got[0].Error != nil || !got[0].Classified {
		t.Fatalf("classify=%+v err=%v", got, err)
	}
	if !reflect.DeepEqual(got[1], guard.Classified{ID: "binned", Error: new("closed")}) {
		t.Fatalf("closed=%+v", got[1])
	}
	if len(s.asked) != 1 || s.calls != 0 || b.lookups != 1 {
		t.Fatalf("screener asked=%v screens=%d lookups=%d", s.asked, s.calls, b.lookups)
	}
}

// A draft write, reply, or forward with a closed source first withdraws the waiting send of the draft where it
// applies, then gives the closed refusal: no policy record, no Jev check, no draft read, and no provider write.
func TestComposeWithAClosedSourceRefusesBeforePolicyJevAndWrite(t *testing.T) {
	for _, tc := range []struct {
		name     string
		withdraw bool
		call     func(*guard.Guard, string) error
	}{
		{"draft update", true, func(g *guard.Guard, id string) error {
			_, err := g.DraftUpdate(t.Context(), id, model.DraftChange{Subject: new("CANARY")})
			return err
		}},
		{"draft delete", true, func(g *guard.Guard, id string) error { return g.DraftDelete(t.Context(), id) }},
		{"draft send", true, func(g *guard.Guard, id string) error { return g.DraftSend(t.Context(), id) }},
		{"draft reply", false, func(g *guard.Guard, id string) error {
			_, _, err := g.DraftReply(t.Context(), id, model.Reply{Body: "CANARY"})
			return err
		}},
		{"draft forward", false, func(g *guard.Guard, id string) error {
			_, _, err := g.DraftForward(t.Context(), id, model.Forward{To: []string{"a@example.test"}})
			return err
		}},
		{"mail reply", false, func(g *guard.Guard, id string) error {
			return g.SendReply(t.Context(), id, model.Reply{Body: "CANARY"})
		}},
		{"mail forward", false, func(g *guard.Guard, id string) error {
			return g.SendForward(t.Context(), id, model.Forward{To: []string{"a@example.test"}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := message("binned")
			source.Folders, source.IsDraft = []string{"spam"}, tc.withdraw
			w := &writer{mailbox: &mailbox{messages: []model.Message{source}}}
			s := scoring(0.08)
			g, c, path := policyGuard(t, w, nil, s, allowAll)
			old := waitingSend(t, c, "binned")
			if err := tc.call(g, "binned"); err != guard.MessageSpam {
				t.Fatalf("err=%v", err)
			}
			want := []string{}
			if tc.withdraw {
				want = []string{"approval draft.send withdrawn " + old}
			} else if stateOf(t, c, old) != cache.StatePending {
				t.Fatal("a reply or forward withdrew a draft send")
			}
			if events := decisionEvents(t, path); !reflect.DeepEqual(events, want) {
				t.Fatalf("records=%v want=%v", events, want)
			}
			if len(s.checked()) != 0 || w.draftReads != 0 || len(w.calls) != 0 || len(auditRecords(t, path)) != 0 {
				t.Fatalf("jev=%v draft reads=%d writes=%v", s.checked(), w.draftReads, w.calls)
			}
		})
	}
}

// ReplyRecipients refuses a closed message without asking the provider.
func TestReplyRecipientsRefusesAClosedMessage(t *testing.T) {
	m := message("binned")
	m.Folders = []string{"trash"}
	w := &writer{mailbox: &mailbox{messages: []model.Message{m}}}
	g, _, _ := setup(t, w, &screener{verdict: model.Green})
	if got, err := g.ReplyRecipients(t.Context(), "binned", true); err != guard.MessageTrash || !reflect.DeepEqual(got, model.Recipients{}) || w.recipientReads != 0 {
		t.Fatalf("recipients=%+v err=%v reads=%d", got, err, w.recipientReads)
	}
}

// One request walks the closed folders once: a trash batch of uncached messages, whose sealed lookups read and
// screen each message, a move with its destination check, and a reply all reuse the lookup of their request.
func TestOneRequestLooksUpTheClosedFoldersOnce(t *testing.T) {
	w := &writer{mailbox: &mailbox{messages: messages("m1", "m2", "m3")}}
	g, _, _ := policyGuard(t, w, nil, &screener{verdict: model.Green}, defaultPolicy)
	ctx := t.Context()
	for name, run := range map[string]func() error{
		"trash": func() error { _, err := g.Trash(ctx, []string{"m1", "m2", "m3"}); return err },
		"move":  func() error { _, err := g.Move(ctx, []string{"m1", "m2"}, "f1"); return err },
		"reply": func() error { _, _, err := g.DraftReply(ctx, "m3", model.Reply{}); return err },
	} {
		w.lookups = 0
		if err := run(); err != nil || w.lookups != 1 {
			t.Errorf("%s: lookups=%d err=%v", name, w.lookups, err)
		}
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

// A conversation spam moves the open messages of the conversation to spam, not trash, and its record is a spam.
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
