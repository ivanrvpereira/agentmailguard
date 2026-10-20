package cache_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

var ctx = context.Background()
var received = time.Date(2026, 9, 25, 8, 30, 0, 0, time.UTC)

//go:fix inline
func ptr[T any](v T) *T { return new(v) }
func row(id string) cache.Row {
	return cache.Row{MessageID: id, ReceivedAt: received, Score: new(.05), Verdict: model.Green, Auth: model.AuthResults{SPF: new("pass"), DKIM: new("pass"), DMARC: new("fail"), CompAuth: new("fail"), CompAuthReason: new("000")}, ScreenedAt: received.Add(30 * time.Minute)}
}
func open(t *testing.T) (*cache.Cache, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state", "cache.db")
	c, err := cache.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	return c, path
}
func TestCancelledCacheIOLeavesStoredStateUnchanged(t *testing.T) {
	c, _ := open(t)
	original := row("m1")
	if err := c.Put(ctx, "work", original); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	changed := original
	changed.Verdict = model.Red
	for _, operation := range []func() error{
		func() error { return c.Put(cancelled, "work", changed) },
		func() error { _, err := c.Get(cancelled, "work", []string{"m1"}); return err },
		func() error { return c.SaveOrganization(cancelled, "work", "m1", model.Organization{}) },
		func() error { return c.MarkOrganizationApplied(cancelled, "work", "m1", received) },
		func() error { return c.MarkSecurityApplied(cancelled, "work", "m1", model.Red) },
		func() error { _, err := c.Stats(cancelled, 30, received); return err },
		func() error { _, err := c.Clear(cancelled); return err },
	} {
		if err := operation(); !errors.Is(err, context.Canceled) {
			t.Errorf("expected cancellation, got %v", err)
		}
	}
	got, err := c.Get(ctx, "work", []string{"m1"})
	if err != nil || !reflect.DeepEqual(got["m1"], original) {
		t.Fatalf("cancelled I/O changed state: %+v %v", got, err)
	}
}

func TestInvalidScoresAndAnswersNeverReplaceSavedState(t *testing.T) {
	c, path := open(t)
	original := row("m1")
	if err := c.Put(ctx, "work", original); err != nil {
		t.Fatal(err)
	}
	for _, score := range []float64{-.1, 1.5, math.NaN(), math.Inf(1)} {
		bad := original
		bad.Score = &score
		if err := c.Put(ctx, "work", bad); err == nil {
			t.Errorf("invalid score accepted: %v", score)
		}
	}
	bad := original
	bad.Verdict = "purple"
	if err := c.Put(ctx, "work", bad); err == nil {
		t.Fatal("invalid verdict accepted")
	}
	for _, answer := range []model.Organization{
		{Categories: map[string]float64{"urgent": math.NaN()}},
		{Categories: map[string]float64{"RULE INSTRUCTION CANARY": .9}},
		{Folder: &model.FolderAnswer{Choice: "stay", Confidence: 1, Probabilities: map[string]float64{"stay": .5}}},
	} {
		if err := c.SaveOrganization(ctx, "work", "m1", answer); err == nil {
			t.Fatal("invalid answers accepted")
		}
	}
	got, err := c.Get(ctx, "work", []string{"m1"})
	if err != nil || !reflect.DeepEqual(got["m1"], original) {
		t.Errorf("saved state changed: %+v %v", got, err)
	}
	bytes, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(bytes), "CANARY") {
		t.Fatal("content persisted")
	}
}

func TestCachePreservesRendererAndAdapterErrorCodes(t *testing.T) {
	c, _ := open(t)
	// Unqualified type names include json.SyntaxError, url.Error, time.ParseError,
	// base64.CorruptInputError, and net.OpError. These are codes, not Error() text.
	for _, code := range []string{
		"render ValueError", "render Exception", "render SyntaxError", "render Error",
		"render ParseError", "render CorruptInputError", "SyntaxError", "Error",
		"Graph error OpError", "Gmail error Error", "Calendar error SyntaxError",
		"Graph error 404 ErrorItemNotFound", "Gmail error 403 rateLimitExceeded", "TypeSafeAPIError 429",
	} {
		t.Run(code, func(t *testing.T) {
			r := row("m1")
			r.Verdict, r.Score, r.Error = model.Error, nil, &code
			if err := c.Put(ctx, "work", r); err != nil {
				t.Fatal(err)
			}
			got, err := c.Get(ctx, "work", []string{"m1"})
			if err != nil || !reflect.DeepEqual(got["m1"], r) {
				t.Fatalf("error code changed: %v %v", got, err)
			}
		})
	}
	for _, raw := range []string{"render ValueError: RAW CONTENT CANARY", "render RAW CONTENT CANARY", "render failed", "Graph error 404 RAW CONTENT CANARY", "failed: RAW CONTENT CANARY"} {
		r := row("m1")
		r.Verdict, r.Score, r.Error = model.Error, nil, &raw
		if err := c.Put(ctx, "work", r); err == nil {
			t.Error("raw error message accepted")
		}
	}
}

func TestUnvalidatedSecurityMetadataNeverReplacesCachedState(t *testing.T) {
	c, path := open(t)
	original := row("m1")
	if err := c.Put(ctx, "work", original); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*cache.Row){
		func(r *cache.Row) { r.Error = new("RAW CONTENT CANARY") },
		func(r *cache.Row) { r.Auth.SPF = new("RAW CONTENT CANARY") },
		func(r *cache.Row) { r.Auth.DKIM = new("RAW CONTENT CANARY") },
		func(r *cache.Row) { r.Auth.DMARC = new("RAW CONTENT CANARY") },
		func(r *cache.Row) { r.Auth.CompAuth = new("RAW CONTENT CANARY") },
		func(r *cache.Row) { r.Auth.CompAuthReason = new("RAW CONTENT CANARY") },
	} {
		bad := original
		change(&bad)
		if err := c.Put(ctx, "work", bad); err == nil {
			t.Error("unvalidated security metadata accepted")
		}
	}
	got, err := c.Get(ctx, "work", []string{"m1"})
	if err != nil || !reflect.DeepEqual(got["m1"], original) {
		t.Fatalf("rejected metadata changed cache: %v", err)
	}
	bytes, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(bytes), "CANARY") {
		t.Fatal("unvalidated metadata persisted")
	}
}

func TestStatsWindowSharesAndClear(t *testing.T) {
	c, _ := open(t)
	verdicts := []model.Verdict{model.Green, model.Green, model.Green, model.Green, model.Green, model.Yellow, model.Red, model.Error, model.Sealed}
	for i, verdict := range verdicts {
		r := row(fmt.Sprintf("m%d", i))
		r.Verdict = verdict
		if err := c.Put(ctx, "work", r); err != nil {
			t.Fatal(err)
		}
	}
	clean := row("clean")
	clean.Auth = model.AuthResults{DMARC: new("pass")}
	old := row("old")
	old.ReceivedAt = received.Add(-40 * 24 * time.Hour)
	sealed := row("sealed")
	sealed.Verdict = model.Sealed
	sealed.Auth = model.AuthResults{}
	for _, item := range []struct {
		account string
		row     cache.Row
	}{{"work", clean}, {"work", old}, {"home", sealed}} {
		if err := c.Put(ctx, item.account, item.row); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := c.Stats(ctx, 30, received.Add(time.Hour))
	want := cache.AccountStats{Total: 10, Counts: map[model.Verdict]int{model.Green: 6, model.Yellow: 1, model.Sealed: 1, model.Red: 1, model.Error: 1}, Shares: map[model.Verdict]float64{model.Green: .6, model.Yellow: .1, model.Sealed: .1, model.Red: .1, model.Error: .1}, GreenShareNonSealed: new(.667), DMARCFail: 9, CompAuthFail: 9}
	if err != nil || !reflect.DeepEqual(stats["work"], want) || stats["home"].GreenShareNonSealed != nil {
		t.Fatalf("stats=%+v error=%v", stats, err)
	}
	if err := c.SaveOrganization(ctx, "work", "clean", model.Organization{}); err != nil {
		t.Fatal(err)
	}
	if err := c.MarkOrganizationApplied(ctx, "work", "clean", received); err != nil {
		t.Fatal(err)
	}
	if err := c.MarkSecurityApplied(ctx, "work", "clean", model.Yellow); err != nil {
		t.Fatal(err)
	}
	count, err := c.Clear(ctx)
	if err != nil || count != 12 {
		t.Fatalf("clear=%d error=%v", count, err)
	}
	stats, err = c.Stats(ctx, 60, received)
	if err != nil || len(stats) != 0 {
		t.Fatalf("not cleared: %v %v", stats, err)
	}
	if err := c.Put(ctx, "work", clean); err != nil {
		t.Fatal(err)
	}
	rows, err := c.Get(ctx, "work", []string{"clean"})
	if err != nil || !reflect.DeepEqual(rows["clean"], clean) {
		t.Fatalf("markers survived clear: %+v %v", rows, err)
	}
}

func TestRescreenPreservesAnswersAndIndependentMarkers(t *testing.T) {
	c, _ := open(t)
	original := row("m1")
	for _, account := range []string{"work", "home"} {
		if err := c.Put(ctx, account, original); err != nil {
			t.Fatal(err)
		}
	}
	answers := model.Organization{Folder: &model.FolderAnswer{Choice: "finance", Confidence: .82, Probabilities: map[string]float64{"finance": .82, "stay": .18}}, Categories: map[string]float64{"urgent": .91}}
	at := received.Add(24 * time.Hour)
	for _, err := range []error{c.SaveOrganization(ctx, "work", "m1", answers), c.MarkOrganizationApplied(ctx, "work", "m1", at), c.MarkSecurityApplied(ctx, "work", "m1", model.Yellow)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	changed := row("m1")
	changed.Verdict, changed.Score = model.Red, new(.9)
	if err := c.Put(ctx, "work", changed); err != nil {
		t.Fatal(err)
	}
	changed.Organization, changed.OrganizationAppliedAt, changed.SecurityAppliedVerdict = &answers, &at, ptr(model.Yellow)
	got, err := c.Get(ctx, "work", []string{"m1"})
	if err != nil || !reflect.DeepEqual(got["m1"], changed) {
		t.Fatalf("organizer state lost: got=%+v error=%v", got, err)
	}
	other, err := c.Get(ctx, "home", []string{"m1"})
	if err != nil || !reflect.DeepEqual(other["m1"], original) {
		t.Fatalf("account leaked: %v, %v", other, err)
	}
	for _, err := range []error{c.SaveOrganization(ctx, "other", "m1", answers), c.MarkOrganizationApplied(ctx, "other", "m1", at), c.MarkSecurityApplied(ctx, "other", "m1", model.Red), c.MarkSecurityApplied(ctx, "work", "m1", model.Green)} {
		if err == nil {
			t.Fatal("invalid update accepted")
		}
	}
}

func TestConcurrentCacheAccessPreservesIndependentState(t *testing.T) {
	first, path := open(t)
	second, err := cache.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	original := row("m1")
	for _, account := range []string{"work", "home"} {
		if err := first.Put(ctx, account, original); err != nil {
			t.Fatal(err)
		}
	}
	answers := model.Organization{Categories: map[string]float64{"urgent": .9}}
	var workers sync.WaitGroup
	for i := range 24 {
		workers.Go(func() {
			c := []*cache.Cache{first, second}[i%2]
			for _, err := range []error{
				c.Put(ctx, "work", original),
				c.SaveOrganization(ctx, "work", "m1", answers),
				c.MarkOrganizationApplied(ctx, "work", "m1", received),
				c.MarkSecurityApplied(ctx, "work", "m1", model.Yellow),
			} {
				if err != nil {
					t.Error(err)
				}
			}
			if _, err := c.Get(ctx, "work", []string{"m1"}); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	want := original
	want.Organization = &answers
	want.OrganizationAppliedAt = &received
	want.SecurityAppliedVerdict = ptr(model.Yellow)
	for account, expected := range map[string]cache.Row{"work": want, "home": original} {
		got, err := first.Get(ctx, account, []string{"m1"})
		if err != nil || !reflect.DeepEqual(got["m1"], expected) {
			t.Fatalf("concurrent access lost or crossed account state: %s %v", account, err)
		}
	}
}

func TestCacheRoundTripIsolationAndPrivateFiles(t *testing.T) {
	c, path := open(t)
	a, b := row("m1"), row("m1")
	b.Verdict, b.Score, b.Error = model.Error, nil, new("TimeoutError")
	if err := c.Put(ctx, "work", a); err != nil {
		t.Fatal(err)
	}
	if err := c.Put(ctx, "home", b); err != nil {
		t.Fatal(err)
	}
	for account, want := range map[string]cache.Row{"work": a, "home": b} {
		got, err := c.Get(ctx, account, []string{"m1", "missing"})
		if err != nil || !reflect.DeepEqual(got, map[string]cache.Row{"m1": want}) {
			t.Fatalf("got=%+v error=%v", got, err)
		}
	}
	for _, ids := range [][]string{nil, {"missing"}} {
		got, err := c.Get(ctx, "work", ids)
		if err != nil || len(got) != 0 {
			t.Fatalf("miss=%v error=%v", got, err)
		}
	}
	for name, mode := range map[string]os.FileMode{path: 0600, filepath.Dir(path): 0700} {
		info, err := os.Stat(name)
		if err != nil || info.Mode().Perm() != mode {
			t.Fatalf("private permissions not enforced for %s", name)
		}
	}
}
