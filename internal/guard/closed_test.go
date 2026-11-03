package guard_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

func TestClosedFoldersRefuseBeforeReadingMessages(t *testing.T) {
	b := &mailbox{messages: messages("m1")}
	g, _, _ := setup(t, b, &screener{verdict: model.Green})
	for _, tc := range []struct {
		folder string
		want   error
	}{{"trash", guard.FolderTrash}, {"spam", guard.FolderSpam}} {
		if _, err := g.List(t.Context(), tc.folder, model.PageRequest{}); err != tc.want {
			t.Fatalf("list %s = %v", tc.folder, err)
		}
		if _, err := g.Search(t.Context(), model.SearchQuery{Text: new("x"), Folder: &tc.folder}, model.PageRequest{}); err != tc.want {
			t.Fatalf("search %s = %v", tc.folder, err)
		}
	}
	if len(b.pages) != 0 || len(b.gets) != 0 {
		t.Fatalf("closed folder read messages: pages=%v gets=%v", b.pages, b.gets)
	}
}

func TestClosedMessageRefusesCachedGetAndReplySource(t *testing.T) {
	m := message("m1")
	m.Folders = []string{"trash"}
	w := &writer{mailbox: &mailbox{messages: []model.Message{m}}}
	s := &screener{verdict: model.Green}
	g, c, path := setup(t, w, s)
	if err := c.Put(t.Context(), "work", cache.Row{MessageID: "m1", ReceivedAt: now, ScreenedAt: now, Verdict: model.Green, Score: new(0.0)}); err != nil {
		t.Fatal(err)
	}
	if got, err := g.Get(t.Context(), "m1"); err != guard.MessageTrash || got.ID != "" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, _, err := g.DraftReply(t.Context(), "m1", model.Reply{Body: "hello"}); err != guard.MessageTrash {
		t.Fatalf("reply = %v", err)
	}
	if s.calls != 0 || len(w.calls) != 0 || len(allAuditRecords(t, path)) != 0 {
		t.Fatalf("closed source reached screening, policy or provider: screens=%d writes=%v records=%v", s.calls, w.calls, allAuditRecords(t, path))
	}
}

type unreadableWriter struct{ *writer }

func (w *unreadableWriter) Get(ctx context.Context, ids []string, body bool) ([]model.Message, error) {
	if len(ids) == 1 && ids[0] == "unreadable" {
		return nil, model.MailboxError("NotFoundError")
	}
	return w.writer.Get(ctx, ids, body)
}

func TestClosedMutationBatchKeepsEachResultAndAudit(t *testing.T) {
	for _, approved := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "approved"}[approved], func(t *testing.T) {
			open, closed := message("open"), message("closed")
			closed.Folders = []string{"trash"}
			w := &unreadableWriter{&writer{mailbox: &mailbox{messages: []model.Message{open, closed}}}}
			g, _, path := setup(t, w, &screener{verdict: model.Green})
			ctx := t.Context()
			if approved {
				ctx = guard.WithApproval(ctx)
			}
			got, err := g.Trash(ctx, []string{"open", "closed", "unreadable"})
			want := []model.MutationResult{{ID: "open", OK: true}, {ID: "closed", Error: new(string(guard.MessageTrash))}, {ID: "unreadable", Error: new("MailboxError")}}
			if err != nil || !reflect.DeepEqual(got, want) || !reflect.DeepEqual(w.trashed, []string{"open"}) {
				t.Fatalf("batch=%+v err=%v provider=%v", got, err, w.trashed)
			}
			records := auditRecords(t, path)
			if len(records) != 1 || !reflect.DeepEqual(records[0]["ids"], []any{"open", "closed", "unreadable"}) {
				t.Fatalf("audit=%v", records)
			}
			results := records[0]["results"].([]any)
			if len(results) != 3 || results[0].(map[string]any)["ok"] != true || results[1].(map[string]any)["error"] != string(guard.MessageTrash) || results[2].(map[string]any)["error"] != "MailboxError" {
				t.Fatalf("audit results=%v", results)
			}
			for _, r := range policyRecords(t, path) {
				if r["id"] != "open" {
					t.Fatalf("closed or unreadable ID reached policy: %v", r)
				}
			}
		})
	}
}
