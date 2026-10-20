package guard_test

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/rules"
)

func checkStats(t *testing.T, got, want guard.Stats) {
	t.Helper()
	if got != want {
		t.Fatalf("stats = %+v, want %+v", got, want)
	}
}

func TestOrganizeReportKeyOrderAndEmptyStats(t *testing.T) {
	b, s := organizerSetup(t)
	g, _, _ := setup(t, b, s)
	since := now.Add(-7 * 24 * time.Hour)
	until := now.Add(time.Hour)
	for _, tc := range []struct {
		until *time.Time
		keys  []string
	}{
		{nil, []string{"planned", "errors", "warnings", "since", "stats"}},
		{&until, []string{"planned", "errors", "warnings", "since", "until", "stats"}},
	} {
		for _, r := range []rules.Rules{{}, orgRules()} {
			report, err := g.Organize(context.Background(), since, r, now, false, tc.until)
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(report)
			if err != nil {
				t.Fatal(err)
			}
			d := json.NewDecoder(strings.NewReader(string(data)))
			keys := []string{}
			if _, err := d.Token(); err != nil {
				t.Fatal(err)
			}
			for d.More() {
				key, _ := d.Token()
				keys = append(keys, key.(string))
				var skip json.RawMessage
				if err := d.Decode(&skip); err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(keys, tc.keys) {
				t.Fatalf("key order = %v, want %v", keys, tc.keys)
			}
			if len(r.Rules) == 0 && !strings.HasSuffix(string(data), `"stats":{"messages":0,"verdicts":{},"applied":0,"tagged":0,"skipped":0,"failed":0}}`) {
				t.Fatalf("no-work stats: %s", data)
			}
		}
	}
	report, err := g.Organize(context.Background(), since, orgRules(), now, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(report.Stats)
	if string(data) != `{"messages":1,"verdicts":{"green":1},"applied":0,"tagged":0,"skipped":0,"failed":0}` {
		t.Fatalf("stats JSON: %s", data)
	}
	data, _ = json.Marshal(guard.Verdicts{Green: 11, Yellow: 2, Red: 1, Error: 1})
	if string(data) != `{"green":11,"yellow":2,"red":1,"error":1}` {
		t.Fatalf("verdict order or zero counts: %s", data)
	}
}

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
	s.result.Error = new("invalid answer folder")
	g, _, path := setup(t, b, s)
	ctx := context.Background()
	since := now.Add(-7 * 24 * time.Hour)
	report, err := g.Organize(ctx, since, orgRules(), now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkStats(t, report.Stats, guard.Stats{Messages: 1, Verdicts: guard.Verdicts{Yellow: 1}, Tagged: 1, Failed: 1})
	if len(report.Planned) != 0 || !reflect.DeepEqual(b.calls, []string{"categorize"}) {
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
	checkStats(t, report.Stats, guard.Stats{Messages: 1, Verdicts: guard.Verdicts{Yellow: 1}, Skipped: 1})
	if len(b.calls) != 1 {
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
	checkStats(t, report.Stats, guard.Stats{Messages: 1, Verdicts: guard.Verdicts{Yellow: 1}, Skipped: 1})
	if len(b.gets) != 0 || len(s.asked) != 2 {
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
	checkStats(t, report.Stats, guard.Stats{Messages: 1, Verdicts: guard.Verdicts{Green: 1}, Applied: 1})
	if !reflect.DeepEqual(b.calls, []string{"categorize", "archive"}) {
		t.Fatalf("actions not applied categories-first: calls=%v", b.calls)
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
	checkStats(t, report.Stats, guard.Stats{Messages: 1, Verdicts: guard.Verdicts{Green: 1}, Skipped: 1})
	if len(report.Planned) != 0 || len(b.calls) != 2 || len(b.gets) != 0 || len(s.asked) != 1 {
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
	checkStats(t, report.Stats, guard.Stats{Messages: 1, Verdicts: guard.Verdicts{Green: 1}})
	if len(s.asked) != 1 || len(b.calls) != 0 || len(auditRecords(t, path)) != 0 {
		t.Fatalf("preview classification or writes incorrect: %+v calls=%v", report, b.calls)
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
	checkStats(t, again.Stats, guard.Stats{Messages: 1, Verdicts: guard.Verdicts{Green: 1}})
	if len(b.gets) != 0 || len(s.asked) != 1 {
		t.Fatal("preview did not reuse cached classification")
	}
}

func TestRuleMoveIntoTrashFolderWritesNothing(t *testing.T) {
	b, s := organizerSetup(t)
	s.result.Organization.Categories = map[string]float64{}
	g, c, path := setup(t, b, s)
	r := rules.Rules{Rules: []rules.Rule{{Name: "route", Kind: rules.Folder, Target: "del", Instructions: "Route"}}}
	report, err := g.Organize(context.Background(), now.Add(-7*24*time.Hour), r, now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Planned) != 1 || report.Planned[0].Move == nil || report.Planned[0].Move.Result == nil || *report.Planned[0].Move.Result != "GuardError" {
		t.Fatalf("rule move outcome: %+v", report.Planned)
	}
	if len(b.calls) != 0 {
		t.Fatalf("rule move into trash folder wrote: %v", b.calls)
	}
	records := auditRecords(t, path)
	if len(records) != 1 || records[0]["operation"] != "move" || records[0]["rule"] != "route" {
		t.Fatalf("refused rule move audit: %+v", records)
	}
	result := records[0]["results"].([]any)[0].(map[string]any)
	if result["id"] != "m1" || result["ok"] != false || result["error"] != "GuardError" {
		t.Fatalf("refused rule move audit result: %+v", result)
	}
	rows, err := c.Get(context.Background(), "work", []string{"m1"})
	if err != nil || rows["m1"].OrganizationAppliedAt != nil {
		t.Fatal("refused rule move marked organization applied")
	}
}
