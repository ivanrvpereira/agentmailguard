package guard_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
	"github.com/ivanrvpereira/agentmailguard-go/internal/rules"
)

type outcomeBox struct {
	*organizerBox
	categoryResults []model.MutationResult
	categoryError   error
	afterWrite      func()
	afterArchive    func()
}

func (b *outcomeBox) Archive(ctx context.Context, ids []string) ([]model.MutationResult, error) {
	results, err := b.writer.Archive(ctx, ids)
	if b.afterArchive != nil {
		b.afterArchive()
	}
	return results, err
}

func (b *outcomeBox) Categorize(ctx context.Context, ids, add, remove []string) ([]model.MutationResult, error) {
	b.calls = append(b.calls, "categorize")
	if b.afterWrite != nil {
		b.afterWrite()
	}
	if b.categoryError != nil {
		return nil, b.categoryError
	}
	if b.categoryResults != nil {
		return b.categoryResults, nil
	}
	out := []model.MutationResult{}
	for _, id := range ids {
		out = append(out, model.MutationResult{ID: id, OK: true})
	}
	return out, nil
}
func TestOrganizerNormalizesUnsolicitedMissingAndDuplicateResults(t *testing.T) {
	base, s := organizerSetup(t)
	base.messages = nil
	base.scan.Messages = nil
	for _, id := range []string{"missing", "duplicate", "good"} {
		m := message(id)
		base.messages = append(base.messages, m)
		base.scan.Messages = append(base.scan.Messages, model.InboxEntry{ID: id, ReceivedAt: m.ReceivedAt})
	}
	b := &outcomeBox{organizerBox: base, categoryResults: []model.MutationResult{{ID: "CANARY unsolicited", OK: true}, {ID: "duplicate", Error: ptr("CANARY raw")}, {ID: "duplicate", OK: true}, {ID: "good", OK: true}}}
	g, _, path := setup(t, b, s)
	report, err := g.Organize(context.Background(), now.Add(-7*24*time.Hour), orgRules(), now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts["failed"][model.Green] != 2 || report.Counts["applied"][model.Green] != 1 {
		t.Fatalf("wrong failure counts: %+v", report.Counts)
	}
	for _, line := range report.Planned {
		want := "blocked by category"
		if line.ID == "good" {
			want = "ok"
		}
		if line.Move == nil || line.Move.Result == nil || *line.Move.Result != want {
			t.Fatalf("incorrect move result for %s", line.ID)
		}
	}
	records := auditRecords(t, path)
	if len(records) != 2 || !reflect.DeepEqual(records[1]["ids"], []any{"good"}) {
		t.Fatalf("bad IDs were moved: %+v", records)
	}
	results := records[0]["results"].([]any)
	for i, id := range []string{"missing", "duplicate", "good"} {
		got := results[i].(map[string]any)
		if got["id"] != id || got["ok"] != (id == "good") {
			t.Fatalf("not normalized: %+v", results)
		}
	}
	data, _ := json.Marshal(report)
	if strings.Contains(string(data), "CANARY") {
		t.Fatal("provider text in report")
	}
}
func TestOrganizerWholeCallFailuresAuditEveryRequestedIDAndContinueBatches(t *testing.T) {
	base, s := organizerSetup(t)
	base.messages = nil
	base.scan.Messages = nil
	for i := range 51 {
		id := fmt.Sprintf("m%02d", i)
		m := message(id)
		base.messages = append(base.messages, m)
		base.scan.Messages = append(base.scan.Messages, model.InboxEntry{ID: id, ReceivedAt: m.ReceivedAt})
	}
	b := &outcomeBox{organizerBox: base, categoryError: model.MailboxError("CANARY provider body")}
	g, _, path := setup(t, b, s)
	report, err := g.Organize(context.Background(), now.Add(-7*24*time.Hour), orgRules(), now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	records := auditRecords(t, path)
	if len(records) != 2 || len(records[0]["ids"].([]any)) != 50 || len(records[1]["ids"].([]any)) != 1 || report.Counts["failed"][model.Green] != 51 {
		t.Fatalf("batches failed or retried incorrectly: records=%d counts=%v", len(records), report.Counts)
	}
	for _, record := range records {
		for _, raw := range record["results"].([]any) {
			r := raw.(map[string]any)
			if r["ok"] != false || r["error"] != "MailboxError" {
				t.Fatalf("unsafe failure audit: %+v", r)
			}
		}
	}
	b.categoryError = nil
	report, err = g.Organize(context.Background(), now.Add(-7*24*time.Hour), orgRules(), now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts["cached"][model.Green] != 51 || report.Counts["applied"][model.Green] != 51 || len(s.asked) != 51 {
		t.Fatal("failed actions did not retry with cached answers")
	}
}
func TestOrganizerFiltersHalfOpenScanAndPreservesFractionalAge(t *testing.T) {
	b, s := organizerSetup(t)
	b.messages = nil
	b.scan.Messages = nil
	since := now.Add(-72 * time.Hour)
	until := now.Add(-time.Hour)
	for _, entry := range []model.InboxEntry{{ID: "young", ReceivedAt: now.Add(-48*time.Hour + 500*time.Millisecond)}, {ID: "scan-bad", ReceivedAt: since}, {ID: "early", ReceivedAt: since.Add(-time.Nanosecond)}, {ID: "edge", ReceivedAt: until}, {ID: "young", ReceivedAt: now.Add(-48*time.Hour + 500*time.Millisecond)}} {
		m := message(entry.ID)
		m.ReceivedAt = entry.ReceivedAt
		b.messages = append(b.messages, m)
		b.scan.Messages = append(b.scan.Messages, entry)
	}
	// The provider scan can repeat an ID; body reads still return exactly one message per ID.
	b.messages = b.messages[:4]
	b.scan.Failures = []model.ScanFailure{{ID: ptr("scan-bad"), Error: "Graph error InvalidMetadata"}, {ID: ptr("scan-bad"), Error: "Graph error InvalidMetadata"}, {Error: "CANARY bad page"}}
	r := orgRules()
	r.Rules[0].MinAgeHours = 48
	g, _, path := setup(t, b, s)
	ctx := context.Background()
	preview, err := g.Organize(ctx, since, r, now, false, &until)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Planned) != 2 || len(s.asked) != 2 {
		t.Fatalf("out-of-window or duplicate classification: %+v", preview)
	}
	report, err := g.Organize(ctx, since, r, now, true, &until)
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts["cached"][model.Green] != 2 || report.Counts["failed"][model.Green] != 1 || report.Counts["skipped"][model.Green] != 1 || *report.Planned[0].Move.After != "2026-09-28T15:30:00.500000Z" || *report.Planned[0].Move.Result != "too young" || *report.Planned[1].Move.Result != "scan failed" {
		t.Fatalf("scan/age safety failed: %+v", report)
	}
	records := auditRecords(t, path)
	if len(records) != 1 || !reflect.DeepEqual(records[0]["ids"], []any{"young"}) {
		t.Fatalf("scan failure mutated: %+v", records)
	}
	report, err = g.Organize(ctx, since, r, now.Add(500*time.Millisecond), true, &until)
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts["applied"][model.Green] != 1 || *report.Planned[0].Move.Result != "ok" {
		t.Fatal("move did not occur exactly at fractional boundary")
	}
}
func TestOrganizerNoRulesMissingTargetsAndNoopCompletion(t *testing.T) {
	b, s := organizerSetup(t)
	g, _, path := setup(t, b, s)
	ctx := context.Background()
	since := now.Add(-7 * 24 * time.Hour)
	report, err := g.Organize(ctx, since, rules.Rules{}, now, true, nil)
	if err != nil || len(report.Planned) != 0 || len(s.asked) != 0 || len(b.gets) != 0 {
		t.Fatalf("no-rule work: %+v %v", report, err)
	}
	until := since
	if _, err := g.Organize(ctx, since, orgRules(), now, true, &until); err == nil {
		t.Fatal("reversed interval allowed")
	}
	r := orgRules()
	r.Rules[0].Target = "missing-folder"
	r.Rules[1].Target = "missing-category"
	report, err = g.Organize(ctx, since, r, now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Warnings) != 2 || report.Counts["skipped"][model.Green] != 1 || report.Counts["failed"][model.Green] != 0 || len(b.calls) != 0 || len(auditRecords(t, path)) != 0 {
		t.Fatalf("missing targets counted as failures or wrote: %+v", report)
	}
	// Removed/changed-kind rules cannot act on the old answers; an empty current plan completes green mail.
	r = rules.Rules{Rules: []rules.Rule{{Name: "route", Kind: rules.Category, Target: "Work", Instructions: "Changed kind"}}}
	report, err = g.Organize(ctx, since, r, now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Planned) != 0 || report.Counts["applied"][model.Green] != 1 || len(b.calls) != 0 {
		t.Fatalf("green no-op did not complete: %+v", report)
	}
}
func TestOrganizationMarkerFailureIsReportedSafely(t *testing.T) {
	base, s := organizerSetup(t)
	b := &outcomeBox{organizerBox: base}
	g, c, _ := setup(t, b, s)
	b.afterArchive = func() { _ = c.Close() }
	report, err := g.Organize(context.Background(), now.Add(-7*24*time.Hour), orgRules(), now, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts["failed"][model.Green] != 1 || report.Counts["applied"][model.Green] != 0 || len(report.Errors) != 1 || report.Errors[0].Error != "cache Error" {
		t.Fatalf("cache failure not reported: %+v", report)
	}
}
func TestSuccessfulFinalWritesKeepCompletionAfterCancellation(t *testing.T) {
	for _, securityTag := range []bool{false, true} {
		t.Run(fmt.Sprint("securityTag=", securityTag), func(t *testing.T) {
			base, s := organizerSetup(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			b := &outcomeBox{organizerBox: base}
			want := 2
			if securityTag {
				s.result = answer(model.Yellow)
				b.afterWrite = cancel
				want = 1
			} else {
				b.afterArchive = cancel
			}
			g, _, path := setup(t, b, s)
			since := now.Add(-7 * 24 * time.Hour)
			if _, err := g.Organize(ctx, since, orgRules(), now, true, nil); !errors.Is(err, context.Canceled) {
				t.Fatalf("final successful write cancellation: %v", err)
			}
			b.afterWrite, b.afterArchive = nil, nil
			if _, err := g.Organize(context.Background(), since, orgRules(), now, true, nil); err != nil {
				t.Fatal(err)
			}
			if len(b.calls) != want || len(auditRecords(t, path)) != want {
				t.Fatalf("confirmed successful writes repeated: operations=%v audit=%d", b.calls, len(auditRecords(t, path)))
			}
		})
	}
}

func TestOrganizerCancellationAuditsCompletedWriteButNoUnattemptedMove(t *testing.T) {
	base, s := organizerSetup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := &outcomeBox{organizerBox: base, afterWrite: cancel}
	g, _, path := setup(t, b, s)
	_, err := g.Organize(ctx, now.Add(-7*24*time.Hour), orgRules(), now, true, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("organizer cancellation=%v", err)
	}
	records := auditRecords(t, path)
	if len(records) != 1 || records[0]["operation"] != "categorize" || !reflect.DeepEqual(b.calls, []string{"categorize"}) {
		t.Fatalf("cancelled organizer audited an unattempted move: records=%d calls=%v", len(records), b.calls)
	}
}

func TestOrganizerApplicationGateCancelsConcurrentRun(t *testing.T) {
	b, s := organizerSetup(t)
	b.entered = make(chan struct{})
	b.release = make(chan struct{})
	g, _, _ := setup(t, b, s)
	done := make(chan error, 1)
	since := now.Add(-7 * 24 * time.Hour)
	go func() { _, err := g.Organize(context.Background(), since, orgRules(), now, true, nil); done <- err }()
	<-b.entered
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := g.Organize(ctx, since, orgRules(), now, true, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent organize did not wait/cancel: %v", err)
	}
	close(b.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.calls, []string{"categorize", "archive"}) || len(s.asked) != 1 {
		t.Fatalf("concurrent organize repeated actions: %v", b.calls)
	}
}
