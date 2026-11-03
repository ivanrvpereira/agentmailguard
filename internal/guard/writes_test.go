package guard_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

type writer struct {
	*mailbox
	calls            []string
	trashed          []string
	spammed          []string
	flags            []model.FlagState
	updates          []draftUpdate
	draftReads       int
	draftIDs         []string            // the IDs that ReadDraft got, in order
	beforeWrite      func()              // runs at the start of DraftUpdate, DraftDelete, and DraftSend
	notDraft         bool                // ReadDraft gives a message that is not a draft
	draftErr         error               // the ReadDraft error
	draft            *model.DraftResult  // the DraftNew result; nil gives ID d1
	recipientsErr    error               // the ReplyRecipients error; nil gives the recipients of replyRecipients
	chosen           map[string][]string // the ThreadMessagesIn IDs by "thread/folder"; a missing key gives none
	chooseErr        error               // the ThreadMessagesIn error
	choices          []string            // the "thread/folder" of each ThreadMessagesIn call, in order
	afterChoose      func()              // runs at the end of ThreadMessagesIn
	moveSources      []string            // the source folder of each Move call, in order
	err              error
	entered, release chan struct{}
}

// Get supplies metadata for the drafts that ReadDraft supplies below, as well as the seeded messages.
func (w *writer) Get(ctx context.Context, ids []string, body bool) ([]model.Message, error) {
	if !body && len(ids) == 1 && (ids[0] == "d1" || ids[0] == "draft-source") {
		return []model.Message{{ID: ids[0], Folders: []string{"drafts"}, IsDraft: true}}, nil
	}
	return w.mailbox.Get(ctx, ids, body)
}

func (w *writer) write(op string, ids []string) ([]model.MutationResult, error) {
	w.calls = append(w.calls, op)
	if w.err != nil {
		return nil, w.err
	}
	out := []model.MutationResult{}
	for _, id := range ids {
		out = append(out, model.MutationResult{ID: id, OK: true})
	}
	return out, nil
}
func (w *writer) Archive(ctx context.Context, ids []string) ([]model.MutationResult, error) {
	if w.entered != nil {
		close(w.entered)
		select {
		case <-w.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return w.write("archive", ids)
}
func (w *writer) Trash(_ context.Context, ids []string) ([]model.MutationResult, error) {
	w.trashed = append(w.trashed, ids...)
	return w.write("trash", ids)
}
func (w *writer) Spam(_ context.Context, ids []string) ([]model.MutationResult, error) {
	w.spammed = append(w.spammed, ids...)
	return w.write("spam", ids)
}
func (w *writer) Move(_ context.Context, ids []string, from, _ string) ([]model.MutationResult, error) {
	w.moveSources = append(w.moveSources, from)
	return w.write("move", ids)
}
func (w *writer) ThreadMessagesIn(_ context.Context, thread, folder string) ([]string, error) {
	w.choices = append(w.choices, thread+"/"+folder)
	if w.afterChoose != nil {
		defer w.afterChoose()
	}
	if w.chooseErr != nil {
		return nil, w.chooseErr
	}
	ids := w.chosen[thread+"/"+folder]
	if ids == nil {
		ids = []string{}
	}
	return ids, nil
}
func (w *writer) MarkRead(_ context.Context, ids []string, read bool) ([]model.MutationResult, error) {
	if read {
		return w.write("read", ids)
	}
	return w.write("unread", ids)
}
func (w *writer) Flag(_ context.Context, ids []string, state model.FlagState) ([]model.MutationResult, error) {
	w.flags = append(w.flags, state)
	return w.write("flag", ids)
}
func (w *writer) Categorize(_ context.Context, ids, _, _ []string) ([]model.MutationResult, error) {
	return w.write("categorize", ids)
}
func (w *writer) Folders(context.Context) ([]model.Folder, error) {
	return []model.Folder{{ID: "f1", Name: "Finance"}, {ID: "del", Name: "Deleted Items"}}, nil
}
func (w *writer) DraftNew(context.Context, model.NewDraft) (model.DraftResult, error) {
	_, err := w.write("draft_new", nil)
	if w.draft != nil {
		return *w.draft, err
	}
	return model.DraftResult{ID: "d1"}, err
}
func (w *writer) DraftReply(context.Context, string, model.Reply) (model.DraftResult, error) {
	_, err := w.write("draft_reply", nil)
	return model.DraftResult{ID: "d2"}, err
}

// replyRecipients are the reply recipients that writer gives: the sender, and with all also bo@example.test, so
// that a test sees which all flag was asked for.
func replyRecipients(all bool) model.Recipients {
	r := model.Recipients{To: []string{"ana@example.test"}, CC: []string{}, BCC: []string{}}
	if all {
		r.CC = []string{"bo@example.test"}
	}
	return r
}
func (w *writer) ReplyRecipients(_ context.Context, _ string, all bool) (model.Recipients, error) {
	if w.recipientsErr != nil {
		return model.Recipients{}, w.recipientsErr
	}
	return replyRecipients(all), nil
}
func (w *writer) DraftForward(context.Context, string, model.Forward) (model.DraftResult, error) {
	_, err := w.write("draft_forward", nil)
	return model.DraftResult{ID: "d3"}, err
}

// draftUpdate is one DraftUpdate call that writer got.
type draftUpdate struct {
	id     string
	change model.DraftChange
}

// draftContent is what ReadDraft gives for a draft.
var draftContent = model.DraftContent{IsDraft: true, To: []string{"CANARY-to@example.test"}, CC: []string{}, BCC: []string{"CANARY-bcc@example.test"}, Subject: "CANARY subject", Body: "<p>CANARY body</p>", HTML: true}

func (w *writer) ReadDraft(_ context.Context, id string) (model.DraftContent, error) {
	w.draftReads++
	w.draftIDs = append(w.draftIDs, id)
	if w.draftErr != nil {
		return model.DraftContent{}, w.draftErr
	}
	if w.notDraft {
		return model.DraftContent{}, nil
	}
	return draftContent, nil
}
func (w *writer) DraftDelete(context.Context, string) error {
	if w.beforeWrite != nil {
		w.beforeWrite()
	}
	_, err := w.write("draft_delete", nil)
	return err
}
func (w *writer) DraftSend(context.Context, string) error {
	if w.beforeWrite != nil {
		w.beforeWrite()
	}
	_, err := w.write("draft_send", nil)
	return err
}
func (w *writer) DraftUpdate(_ context.Context, id string, change model.DraftChange) (string, string, error) {
	if w.beforeWrite != nil {
		w.beforeWrite()
	}
	w.updates = append(w.updates, draftUpdate{id, change})
	_, err := w.write("draft_update", nil)
	return "d4", "t4", err
}

// auditRecords returns the per-operation records. Policy and approval records have an "event" field;
// policyRecords returns those.
func auditRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, r := range allAuditRecords(t, path) {
		if _, ok := r["event"]; !ok {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
func policyRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	out := []map[string]any{}
	for _, r := range allAuditRecords(t, path) {
		if _, ok := r["event"]; ok {
			out = append(out, r)
		}
	}
	return out
}
func allAuditRecords(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "CANARY") {
		t.Fatal("content leaked to audit")
	}
	out := []map[string]any{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		var r map[string]any
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

type cancelingFolders struct {
	*writer
	cancel context.CancelFunc
}

func (w *cancelingFolders) Folders(context.Context) ([]model.Folder, error) {
	w.cancel()
	return []model.Folder{{ID: "f1", Name: "Finance"}}, nil
}
func TestCancellationDuringDestinationValidationDoesNotAttemptWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &cancelingFolders{writer: &writer{mailbox: &mailbox{messages: messages("m1", "m2")}}, cancel: cancel}
	g, _, path := setup(t, w, &screener{verdict: model.Green})
	_, err := g.Move(ctx, []string{"m1"}, "f1")
	if !errors.Is(err, context.Canceled) || len(w.calls) != 0 || len(auditRecords(t, path)) != 0 {
		t.Fatalf("cancelled validation attempted mutation: error=%v calls=%v", err, w.calls)
	}
}

func TestMutationGateCancellationDoesNotAttemptQueuedDraft(t *testing.T) {
	w := &writer{mailbox: &mailbox{messages: messages("m1", "m2")}, entered: make(chan struct{}), release: make(chan struct{})}
	g, _, path := setup(t, w, &screener{verdict: model.Green})
	done := make(chan error, 1)
	go func() { _, err := g.Archive(context.Background(), []string{"m1"}); done <- err }()
	<-w.entered
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := g.DraftNew(ctx, model.NewDraft{Body: "CANARY"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued draft did not cancel: %v", err)
	}
	close(w.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(w.calls, []string{"archive"}) || len(auditRecords(t, path)) != 1 {
		t.Fatalf("cancelled draft reached provider: %v", w.calls)
	}
}

func TestEveryManualRouteAndDraftHasContentFreeAudit(t *testing.T) {
	w := &writer{mailbox: &mailbox{messages: messages("m1")}}
	s := &screener{verdict: model.Green}
	g, _, path := setup(t, w, s)
	ctx := context.Background()
	// The forward's Jev check reads its source; a cached green verdict needs no new screening.
	if _, err := g.Get(ctx, "m1"); err != nil {
		t.Fatal(err)
	}
	ids := []string{"m1"}
	calls := []func() error{
		func() error { _, e := g.Trash(ctx, ids); return e }, func() error { _, e := g.Move(ctx, ids, "f1"); return e }, func() error { _, e := g.MarkRead(ctx, ids, true); return e }, func() error { _, e := g.MarkRead(ctx, ids, false); return e }, func() error { _, e := g.Flag(ctx, ids, model.Complete); return e }, func() error { _, e := g.Categorize(ctx, ids, []string{"Work"}, nil); return e },
		func() error {
			_, e := g.DraftNew(ctx, model.NewDraft{To: []string{"CANARY@example.com"}, Subject: "CANARY", Body: "CANARY"})
			return e
		}, func() error { _, _, e := g.DraftReply(ctx, "m1", model.Reply{Body: "CANARY"}); return e }, func() error {
			_, _, e := g.DraftForward(ctx, "m1", model.Forward{Body: "CANARY", To: []string{"CANARY@example.com"}})
			return e
		}, func() error {
			_, e := g.DraftUpdate(ctx, "d1", model.DraftChange{Subject: new("CANARY"), Body: new("CANARY")})
			return e
		}, func() error { return g.DraftDelete(ctx, "d1") }, func() error { return g.DraftSend(ctx, "d1") },
	}
	for _, call := range calls {
		if err := call(); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"trash", "move", "read", "unread", "flag", "categorize", "draft_new", "draft_reply", "draft_forward", "draft_update", "draft_delete", "draft_send"}
	if !reflect.DeepEqual(w.calls, want) {
		t.Fatalf("wrong operations: %v", w.calls)
	}
	records := auditRecords(t, path)
	if len(records) != len(want) {
		t.Fatalf("audit records=%d", len(records))
	}
	allowed := map[string]bool{"time": true, "account": true, "operation": true, "rule": true, "ids": true, "results": true, "folder": true, "flag": true, "add": true, "remove": true, "draft_id": true}
	for i, r := range records {
		for key := range r {
			if !allowed[key] {
				t.Fatalf("unexpected audit field %s", key)
			}
		}
		if r["operation"] != want[i] || r["account"] != "work" || r["rule"] != nil {
			t.Fatalf("audit fields: %+v", r)
		}
	}
	if records[9]["draft_id"] != "d4" || !reflect.DeepEqual(records[9]["ids"], []any{"d1"}) {
		t.Fatal("draft update lost source/result IDs")
	}
	// A draft delete or send records only the draft's ID.
	for _, r := range records[10:] {
		if r["draft_id"] != nil || !reflect.DeepEqual(r["ids"], []any{"d1"}) {
			t.Fatalf("draft delete or send record=%v", r)
		}
	}
	if s.calls != 1 {
		t.Fatal("writes screened content")
	}
	before := len(w.calls)
	refusals := []func() error{func() error { _, e := g.Archive(ctx, nil); return e }, func() error { _, e := g.Trash(ctx, []string{}); return e }, func() error { _, e := g.Move(ctx, ids, "archive"); return e }, func() error { _, e := g.Flag(ctx, ids, "urgent"); return e }, func() error { _, e := g.Categorize(ctx, ids, []string{"Planted"}, nil); return e }, func() error { _, e := g.Categorize(ctx, ids, nil, nil); return e }}
	for _, call := range refusals {
		if call() == nil {
			t.Fatal("invalid mutation allowed")
		}
	}
	if len(w.calls) != before || len(auditRecords(t, path)) != before {
		t.Fatal("refusal attempted or audited mutation")
	}
}

func TestManualFlagAndDraftUpdatePreserveRequestedPayloads(t *testing.T) {
	w := &writer{mailbox: &mailbox{messages: messages("m1", "m2")}}
	g, _, path := setup(t, w, &screener{verdict: model.Red})
	ctx := context.Background()
	for _, state := range []model.FlagState{model.Flagged, model.Complete, model.NoFlag} {
		if _, err := g.Flag(ctx, []string{"m1"}, state); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(w.flags, []model.FlagState{model.Flagged, model.Complete, model.NoFlag}) {
		t.Errorf("provider flag states=%v; want flagged, complete, none", w.flags)
	}
	change := model.DraftChange{CC: new([]string{}), Body: new("CANARY draft update é <b>requested body</b>"), HTML: true}
	if got, err := g.DraftUpdate(ctx, "draft-source", change); err != nil || got != (guard.UpdatedDraft{ID: "d4", ThreadID: "t4"}) {
		t.Fatalf("draft update result: %+v %v", got, err)
	}
	if !reflect.DeepEqual(w.updates, []draftUpdate{{"draft-source", change}}) {
		t.Errorf("provider got %+v, not the exact draft and change", w.updates)
	}
	if len(auditRecords(t, path)) != 4 {
		t.Fatal("payload mutations missing safe audit")
	}
}

func TestManualRefusalsKeepFixedReasonsWithoutWriting(t *testing.T) {
	w := &writer{mailbox: &mailbox{messages: messages("m1", "m2")}}
	g, _, path := setup(t, w, &screener{verdict: model.Green})
	ctx := context.Background()
	ids := []string{"m1"}
	for _, tc := range []struct {
		name, want string
		call       func() error
	}{
		{"move", "move refused: the destination must be a folder ID from `folders`", func() error { _, err := g.Move(ctx, ids, "CANARY destination"); return err }},
		{"trash folder", "move refused: use mail.trash", func() error { _, err := g.Move(ctx, ids, "del"); return err }},
		{"flag", "flag state must be one of flagged, complete, none", func() error { _, err := g.Flag(ctx, ids, "CANARY flag"); return err }},
		{"empty categories", "categorize needs at least one category to add or remove", func() error { _, err := g.Categorize(ctx, ids, nil, nil); return err }},
		{"unknown category", "categorize refused: not in the category list", func() error { _, err := g.Categorize(ctx, ids, []string{"CANARY category"}, nil); return err }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err == nil || err.Error() != tc.want {
				t.Fatalf("refusal = %v; want %q", err, tc.want)
			}
		})
	}
	if len(w.calls) != 0 || len(auditRecords(t, path)) != 0 {
		t.Fatal("refusal attempted or audited a write")
	}
}

type disappearingCategories struct {
	*organizerBox
	lookups int
}

func (b *disappearingCategories) Categories(context.Context) ([]string, error) {
	b.lookups++
	if b.lookups == 1 {
		return []string{"Work"}, nil
	}
	return nil, nil
}

func TestOrganizerRefusalsAndExternalErrorsKeepFixedAuditCodes(t *testing.T) {
	for _, external := range []bool{false, true} {
		t.Run(fmt.Sprint(external), func(t *testing.T) {
			base, s := organizerSetup(t)
			var b model.Mailbox = &disappearingCategories{organizerBox: base}
			if external {
				b = &outcomeBox{organizerBox: base, categoryError: guard.GuardError("CANARY provider content")}
			}
			g, _, path := setup(t, b, s)
			report, err := g.Organize(context.Background(), now.Add(-7*24*time.Hour), orgRules(), now, true, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Planned) != 1 || report.Planned[0].CategoryResults == nil || (*report.Planned[0].CategoryResults)["Work"] != "GuardError" || *report.Planned[0].Move.Result != "blocked by category" {
				t.Fatalf("unsafe organizer outcome: %+v", report)
			}
			records := auditRecords(t, path)
			if len(records) != 1 || records[0]["results"].([]any)[0].(map[string]any)["error"] != "GuardError" {
				t.Fatalf("organizer refusal not normalized: %+v", records)
			}
			data, _ := json.Marshal(report)
			if strings.Contains(string(data), "CANARY") || strings.Contains(string(data), "categorize refused") {
				t.Fatal("public prose leaked into organizer failure codes")
			}
			if !external && len(base.calls) != 0 {
				t.Fatal("refused validation wrote to provider")
			}
		})
	}
}

func TestExternalGuardErrorsCannotSmuggleRefusalText(t *testing.T) {
	w := &writer{mailbox: &mailbox{messages: messages("m1", "m2")}}
	g, _, path := setup(t, w, &screener{verdict: model.Green})
	for _, text := range []string{"CANARY provider body", "categorize refused: not in the category list CANARY"} {
		w.err = guard.GuardError(text)
		if _, err := g.Archive(context.Background(), []string{"m1"}); err == nil || err.Error() != "GuardError" {
			t.Fatalf("external GuardError not redacted: %v", err)
		}
	}
	if len(w.calls) != 2 || len(auditRecords(t, path)) != 0 {
		t.Fatal("failed manual writes retried or audited")
	}
}

func TestManualMutationsDoNotScreenAndAuditOnlySafeResults(t *testing.T) {
	w := &writer{mailbox: &mailbox{messages: messages("m1", "m2")}}
	s := &screener{verdict: model.Red}
	g, _, path := setup(t, w, s)
	ctx := context.Background()
	got, err := g.Archive(ctx, []string{"m1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].OK {
		t.Fatalf("archive did not return successful requested ID: %+v", got)
	}
	records := auditRecords(t, path)
	if len(records) != 1 || records[0]["operation"] != "archive" || records[0]["rule"] != nil || !reflect.DeepEqual(records[0]["ids"], []any{"m1"}) {
		t.Fatalf("invalid audit schema: %+v", records)
	}
	w.err = errors.New("CANARY body")
	if _, err := g.Archive(ctx, []string{"m2"}); err == nil || strings.Contains(err.Error(), "CANARY") {
		t.Fatalf("unsafe whole-call error: %v", err)
	}
	if len(auditRecords(t, path)) != 1 {
		t.Fatal("ordinary failed call wrote audit")
	}
	if s.calls != 0 {
		t.Fatal("manual mutation screened mail")
	}
}
