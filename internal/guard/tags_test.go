package guard_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

type metadataBox struct{ *organizerBox }

func (b *metadataBox) Get(ctx context.Context, ids []string, body bool) ([]model.Message, error) {
	if !body {
		switch ids[0] {
		case "gone":
			return nil, model.MailboxError("CANARY raw")
		case "swap":
			return []model.Message{message("other")}, nil
		case "draft":
			m := message("draft")
			m.IsDraft = true
			return []model.Message{m}, nil
		}
	}
	return b.mailbox.Get(ctx, ids, body)
}
func TestTagMetadataFailuresAndScanFailuresNeverMutateThoseIDs(t *testing.T) {
	base, s := organizerSetup(t)
	s.result = answer(model.Red)
	base.messages = nil
	base.scan.Messages = nil
	for _, id := range []string{"gone", "swap", "draft", "scan-bad", "good", "already"} {
		m := message(id)
		if id == "already" {
			m.Categories = []string{"Red", "Work"}
		}
		base.messages = append(base.messages, m)
		base.scan.Messages = append(base.scan.Messages, model.InboxEntry{ID: id, ReceivedAt: m.ReceivedAt})
	}
	base.scan.Failures = []model.ScanFailure{{ID: new("scan-bad"), Error: "Graph error InvalidMetadata"}}
	b := &metadataBox{base}
	g, _, path := setup(t, b, s)
	report, err := g.Organize(context.Background(), now.Add(-7*24*time.Hour), orgRules(), now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Failed: gone, swap, draft, scan-bad. Tagged: good. Skipped: good and already.
	checkStats(t, report.Stats, guard.Stats{Messages: 6, Verdicts: guard.Verdicts{Red: 6}, Tagged: 1, Skipped: 2, Failed: 4})
	if len(report.Planned) != 0 {
		t.Fatalf("red mail planned: %+v", report)
	}
	want := []string{"Graph error InvalidMetadata", "tag fetch MailboxError", "tag fetch ValueError", "tag draft"}
	for i, code := range want {
		if len(report.Errors) <= i || report.Errors[i].Error != code {
			t.Fatalf("wrong tag failures: %+v", report.Errors)
		}
	}
	records := auditRecords(t, path)
	if len(records) != 1 || !reflect.DeepEqual(records[0]["ids"], []any{"good"}) {
		t.Fatalf("bad ID or correct tag mutated: %+v", records)
	}
	base.gets = nil
	report, err = g.Organize(context.Background(), now.Add(-7*24*time.Hour), orgRules(), now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkStats(t, report.Stats, guard.Stats{Messages: 6, Verdicts: guard.Verdicts{Red: 6}, Skipped: 2, Failed: 4})
	if len(auditRecords(t, path)) != 1 {
		t.Fatal("successful/already-correct tags written twice")
	}
	for _, get := range base.gets {
		if reflect.DeepEqual(get.ids, []string{"already"}) || reflect.DeepEqual(get.ids, []string{"good"}) {
			t.Fatal("successful security marker fetched again")
		}
	}
}
func TestCompletedGreenCanBeRescreenedAndRetaggedWithoutReclassification(t *testing.T) {
	b, s := organizerSetup(t)
	g, _, path := setup(t, b, s)
	ctx := context.Background()
	since := now.Add(-7 * 24 * time.Hour)
	if _, err := g.Organize(ctx, since, orgRules(), now, true, nil); err != nil {
		t.Fatal(err)
	}
	b.messages[0].ContentType = nil
	v, err := g.Get(ctx, "m1")
	if err != nil || v.Verdict != model.Error {
		t.Fatalf("render failure: %+v %v", v, err)
	}
	b.messages[0].ContentType = ptr(model.Text)
	s.verdict = model.Red
	v, err = g.Get(ctx, "m1")
	if err != nil || v.Verdict != model.Red {
		t.Fatalf("re-screen: %+v %v", v, err)
	}
	b.gets = nil
	report, err := g.Organize(ctx, since, orgRules(), now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkStats(t, report.Stats, guard.Stats{Messages: 1, Verdicts: guard.Verdicts{Red: 1}, Tagged: 1, Skipped: 1})
	if len(report.Planned) != 0 || len(s.asked) != 1 {
		t.Fatalf("business completion suppressed retag or reclassified: %+v", report)
	}
	records := auditRecords(t, path)
	if len(records) != 3 || records[2]["rule"] != "security:red" {
		t.Fatalf("unexpected retag audit: %+v", records)
	}
	b.gets = nil
	report, err = g.Organize(ctx, since, orgRules(), now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkStats(t, report.Stats, guard.Stats{Messages: 1, Verdicts: guard.Verdicts{Red: 1}, Skipped: 1})
	if len(b.gets) != 0 || len(auditRecords(t, path)) != 3 {
		t.Fatal("retag repeat was not complete")
	}
}
func TestMissingSecurityCategoryWarnsOnlyWhenNeeded(t *testing.T) {
	b, s := organizerSetup(t)
	s.result = answer(model.Sealed)
	b.categories = []string{"Work", "sealed", " Sealed "}
	g, _, path := setup(t, b, s)
	since := now.Add(-7 * 24 * time.Hour)
	ctx := context.Background()
	report, err := g.Organize(ctx, since, orgRules(), now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkStats(t, report.Stats, guard.Stats{Messages: 1, Verdicts: guard.Verdicts{Sealed: 1}, Skipped: 1})
	if len(report.Warnings) != 1 || report.Warnings[0].Rule != "security:sealed" || len(auditRecords(t, path)) != 0 {
		t.Fatalf("wrong missing tag warning: %+v", report)
	}
	b.categories = append(b.categories, "Sealed")
	report, err = g.Organize(ctx, since, orgRules(), now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkStats(t, report.Stats, guard.Stats{Messages: 1, Verdicts: guard.Verdicts{Sealed: 1}, Tagged: 1, Skipped: 1})
	if len(report.Warnings) != 0 {
		t.Fatalf("new existing tag not applied: %+v", report)
	}
	b.categories = []string{"Work"}
	report, err = g.Organize(ctx, since, orgRules(), now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Warnings) != 0 {
		t.Fatal("successful marker warned after tag removed")
	}
}
