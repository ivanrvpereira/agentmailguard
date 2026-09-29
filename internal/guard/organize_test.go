package guard_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
)

type organizerBox struct {
	*writer
	scan       model.InboxScan
	categories []string
}

func (b *organizerBox) ScanInbox(context.Context, time.Time, *time.Time) (model.InboxScan, error) {
	return b.scan, nil
}
func (b *organizerBox) Categories(context.Context) ([]string, error) {
	return append([]string{}, b.categories...), nil
}
func organizerSetup(t *testing.T) (*organizerBox, *classifier) {
	t.Helper()
	m := message("m1")
	return &organizerBox{writer: &writer{mailbox: &mailbox{messages: []model.Message{m}}}, scan: model.InboxScan{Messages: []model.InboxEntry{{ID: "m1", ReceivedAt: m.ReceivedAt}}}, categories: []string{"Work", "Yellow", "Red", "Sealed"}}, &classifier{screener: &screener{verdict: model.Green}, result: answer(model.Green)}
}
func TestSecurityTagsAreIndependentAndTrustSuccessfulMarker(t *testing.T) {
	b, s := organizerSetup(t)
	s.result = answer(model.Yellow)
	s.result.Organization = nil
	s.result.Error = ptr("invalid answer folder")
	g, _, path := setup(t, b, s)
	ctx := context.Background()
	since := now.Add(-7 * 24 * time.Hour)
	report, err := g.Organize(ctx, since, orgRules(), now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts["tagged"][model.Yellow] != 1 || report.Counts["failed"][model.Yellow] != 1 || len(report.Planned) != 0 || !reflect.DeepEqual(b.calls, []string{"categorize"}) {
		t.Fatalf("organization failure prevented independent tag: %+v calls=%v", report, b.calls)
	}
	records := auditRecords(t, path)
	if len(records) != 1 || records[0]["rule"] != "security:yellow" || !reflect.DeepEqual(records[0]["add"], []any{"Yellow"}) || !reflect.DeepEqual(records[0]["remove"], []any{"Red", "Sealed"}) {
		t.Fatalf("wrong security audit: %+v", records)
	}
	s.result = answer(model.Yellow)
	b.gets = nil
	report, err = g.Organize(ctx, since, orgRules(), now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts["tagged"][model.Yellow] != 0 || report.Counts["skipped"][model.Yellow] != 1 || len(b.calls) != 1 {
		t.Fatalf("tag marker not trusted: %+v", report)
	}
	for _, get := range b.gets {
		if !get.body {
			t.Fatal("marked tag fetched metadata again")
		}
	}
	b.gets = nil
	report, err = g.Organize(ctx, since, orgRules(), now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.gets) != 0 || report.Counts["cached"][model.Yellow] != 1 {
		t.Fatalf("security marker incorrectly completed organization: %+v", report)
	}
}

func TestOrganizeApplyCategoriesBeforeMovesAndCompletesOnce(t *testing.T) {
	b, s := organizerSetup(t)
	g, _, path := setup(t, b, s)
	ctx := context.Background()
	since := now.Add(-7 * 24 * time.Hour)
	report, err := g.Organize(ctx, since, orgRules(), now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.calls, []string{"categorize", "archive"}) || report.Counts["applied"][model.Green] != 1 {
		t.Fatalf("actions not applied categories-first: calls=%v counts=%v", b.calls, report.Counts)
	}
	records := auditRecords(t, path)
	if len(records) != 2 || records[0]["rule"] != "work" || records[1]["rule"] != "route" {
		t.Fatalf("wrong action audit: %+v", records)
	}
	if report.Planned[0].CategoryResults == nil || (*report.Planned[0].CategoryResults)["Work"] != "ok" || report.Planned[0].Move.Result == nil || *report.Planned[0].Move.Result != "ok" {
		t.Fatalf("missing action results: %+v", report.Planned)
	}
	b.gets = nil
	report, err = g.Organize(ctx, since, orgRules(), now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts["skipped"][model.Green] != 1 || len(report.Planned) != 0 || len(b.calls) != 2 || len(b.gets) != 0 || len(s.asked) != 1 {
		t.Fatalf("completed item repeated: %+v calls=%v", report, b.calls)
	}
}

func TestOrganizePreviewUsesCurrentAnswersAndSafeReport(t *testing.T) {
	b, s := organizerSetup(t)
	g, _, path := setup(t, b, s)
	ctx := context.Background()
	since := now.Add(-7 * 24 * time.Hour)
	report, err := g.Organize(ctx, since, orgRules(), now, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Planned) != 1 || report.Planned[0].ID != "m1" || report.Planned[0].Move == nil || report.Planned[0].Move.To == nil || *report.Planned[0].Move.To != "archive" || !reflect.DeepEqual(report.Planned[0].Categories, []string{"Work"}) {
		t.Fatalf("missing green plan: %+v", report)
	}
	if report.Counts["screened"][model.Green] != 1 || report.Counts["classified"][model.Green] != 1 || len(b.calls) != 0 || len(auditRecords(t, path)) != 0 {
		t.Fatalf("preview counts or writes incorrect: %+v calls=%v", report, b.calls)
	}
	data, _ := json.Marshal(report)
	for _, forbidden := range []string{"BODYTEXT", "CANARY", "DISPLAYNAME", "probabilities", "confidence", "until", "category_results"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("unsafe or wrong preview JSON field %q", forbidden)
		}
	}
	b.gets = nil
	again, err := g.Organize(ctx, since, orgRules(), now, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.Counts["cached"][model.Green] != 1 || len(b.gets) != 0 || len(s.asked) != 1 {
		t.Fatal("preview did not reuse cached classification")
	}
}
