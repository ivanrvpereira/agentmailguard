package guard_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/rules"
)

type pendingRedScreen struct {
	model.Screener
	entered, release chan struct{}
	noopID           string
	verdict          model.Verdict
}

func (s *pendingRedScreen) Screen(ctx context.Context, _ map[string]any) (model.Screening, error) {
	close(s.entered)
	select {
	case <-s.release:
		return *answer(model.Red).Screening, nil
	case <-ctx.Done():
		return model.Screening{}, ctx.Err()
	}
}
func (s *pendingRedScreen) Classify(_ context.Context, state map[string]any, _ bool, questions *model.OrganizationQuestions) (model.Classification, error) {
	verdict := s.verdict
	if verdict == "" {
		verdict = model.Green
	}
	result := answer(verdict)
	if len(questions.Categories) == 0 {
		result.Organization.Categories = map[string]float64{}
	}
	if state["subject"] == s.noopID {
		result.Organization.Folder = &model.FolderAnswer{Choice: "stay", Confidence: .9, Probabilities: map[string]float64{"route": .1, "stay": .9}}
	}
	return result, nil
}

type validationPauseBox struct {
	*organizerBox
	folderCalls         atomic.Int32
	validating, release chan struct{}
	moved               []string
}

func (b *validationPauseBox) Move(ctx context.Context, ids []string, dest string) ([]model.MutationResult, error) {
	b.moved = append(b.moved, ids...)
	return b.writer.Move(ctx, ids, dest)
}

func (b *validationPauseBox) Folders(ctx context.Context) ([]model.Folder, error) {
	if b.folderCalls.Add(1) == 2 {
		close(b.validating)
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return []model.Folder{{ID: "f1", Name: "Finance"}}, nil
}

type categoryPauseBox struct {
	*organizerBox
	categoryCalls       atomic.Int32
	validating, release chan struct{}
}

func (b *categoryPauseBox) Categories(ctx context.Context) ([]string, error) {
	if b.categoryCalls.Add(1) == 2 {
		close(b.validating)
		select {
		case <-b.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return b.organizerBox.Categories(ctx)
}

func TestConcurrentRedScreenBlocksStaleBusinessCategoryAndSecurityTag(t *testing.T) {
	for _, original := range []model.Verdict{model.Green, model.Yellow} {
		t.Run(string(original), func(t *testing.T) {
			base, _ := organizerSetup(t)
			b := &categoryPauseBox{organizerBox: base, validating: make(chan struct{}), release: make(chan struct{})}
			s := &pendingRedScreen{entered: make(chan struct{}), release: make(chan struct{}), verdict: original}
			g, c, _ := setup(t, b, s)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			getDone := make(chan error, 1)
			go func() { _, err := g.Get(ctx, "m1"); getDone <- err }()
			receive(t, s.entered)
			done := make(chan error, 1)
			go func() { _, err := g.Organize(ctx, now.Add(-7*24*time.Hour), orgRules(), now, true, nil); done <- err }()
			receive(t, b.validating)
			close(s.release)
			if err := receive(t, getDone); err != nil {
				t.Fatal(err)
			}
			close(b.release)
			if err := receive(t, done); err != nil {
				t.Fatal(err)
			}
			if len(b.calls) != 0 {
				t.Fatalf("stale %s authorized category/tag: %v", original, b.calls)
			}
			rows, err := c.Get(ctx, "work", []string{"m1"})
			if err != nil || rows["m1"].SecurityAppliedVerdict != nil || rows["m1"].OrganizationAppliedAt != nil {
				t.Fatal("refused action marked as successful")
			}
		})
	}
}

func TestSecurityPersistenceWaitsForAuthorizedWriteAndCanCancel(t *testing.T) {
	for _, cancelRead := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRead), func(t *testing.T) {
			b, _ := organizerSetup(t)
			b.entered, b.release = make(chan struct{}), make(chan struct{})
			s := &pendingRedScreen{entered: make(chan struct{}), release: make(chan struct{})}
			g, c, _ := setup(t, b, s)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			readCtx, cancelGet := context.WithCancel(ctx)
			defer cancelGet()
			getDone := make(chan error, 1)
			go func() { _, err := g.Get(readCtx, "m1"); getDone <- err }()
			receive(t, s.entered)
			done := make(chan error, 1)
			go func() { _, err := g.Organize(ctx, now.Add(-7*24*time.Hour), orgRules(), now, true, nil); done <- err }()
			receive(t, b.entered)
			close(s.release)
			if cancelRead {
				cancelGet()
				if err := receive(t, getDone); !errors.Is(err, context.Canceled) {
					t.Fatalf("queued security persistence did not cancel: %v", err)
				}
			} else {
				select {
				case err := <-getDone:
					t.Fatalf("security persisted inside authorized write: %v", err)
				case <-time.After(30 * time.Millisecond):
				}
			}
			rows, err := c.Get(ctx, "work", []string{"m1"})
			if err != nil || rows["m1"].Verdict != model.Green {
				t.Fatal("verdict changed during authorized provider write")
			}
			close(b.release)
			if err := receive(t, done); err != nil {
				t.Fatal(err)
			}
			if !cancelRead {
				if err := receive(t, getDone); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestConcurrentRedScreenCannotCompleteStaleGreenNoop(t *testing.T) {
	base, _ := organizerSetup(t)
	base.categories = []string{"Work"}
	base.messages = append(base.messages, message("m2"))
	base.scan.Messages = append(base.scan.Messages, model.InboxEntry{ID: "m2", ReceivedAt: now})
	b := &validationPauseBox{organizerBox: base, validating: make(chan struct{}), release: make(chan struct{})}
	s := &pendingRedScreen{entered: make(chan struct{}), release: make(chan struct{}), noopID: "m1"}
	g, c, _ := setup(t, b, s)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	getDone := make(chan error, 1)
	go func() { _, err := g.Get(ctx, "m1"); getDone <- err }()
	receive(t, s.entered)
	r := rules.Rules{Rules: []rules.Rule{{Name: "route", Kind: rules.Folder, Target: "f1", Instructions: "Route"}}}
	organizeDone := make(chan error, 1)
	go func() { _, err := g.Organize(ctx, now.Add(-7*24*time.Hour), r, now, true, nil); organizeDone <- err }()
	receive(t, b.validating)
	close(s.release)
	if err := receive(t, getDone); err != nil {
		t.Fatal(err)
	}
	close(b.release)
	if err := receive(t, organizeDone); err != nil {
		t.Fatal(err)
	}
	rows, err := c.Get(ctx, "work", []string{"m1", "m2"})
	if err != nil || rows["m1"].OrganizationAppliedAt != nil || rows["m2"].OrganizationAppliedAt == nil {
		t.Fatal("stale green noop completed or valid sibling lost completion")
	}
}

func TestFinalAuthorizationRevalidatesCacheAndPreservesValidSibling(t *testing.T) {
	base, s := organizerSetup(t)
	base.messages = append(base.messages, message("m2"))
	base.scan.Messages = append(base.scan.Messages, model.InboxEntry{ID: "m2", ReceivedAt: now})
	s.result.Organization.Categories = map[string]float64{}
	b := &validationPauseBox{organizerBox: base, validating: make(chan struct{}), release: make(chan struct{})}
	g, c, path := setup(t, b, s)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r := rules.Rules{Rules: []rules.Rule{{Name: "route", Kind: rules.Folder, Target: "f1", Instructions: "Route"}}}
	done := make(chan error, 1)
	go func() { _, err := g.Organize(ctx, now.Add(-7*24*time.Hour), r, now, true, nil); done <- err }()
	receive(t, b.validating)
	if err := c.Put(ctx, "work", cache.Row{MessageID: "m1", ReceivedAt: now, ScreenedAt: now, Verdict: model.Green, Score: new(.9)}); err != nil {
		t.Fatal(err)
	}
	close(b.release)
	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b.moved, []string{"m2"}) {
		t.Fatalf("final authorization did not isolate invalid cached green: moved=%v", b.moved)
	}
	records := auditRecords(t, path)
	if len(records) != 1 {
		t.Fatalf("audit records=%d", len(records))
	}
	results := records[0]["results"].([]any)
	if len(results) != 2 || results[0].(map[string]any)["id"] != "m1" || results[0].(map[string]any)["ok"] != false || results[1].(map[string]any)["id"] != "m2" || results[1].(map[string]any)["ok"] != true {
		t.Fatal("final authorization lost ordered per-ID outcomes")
	}
	rows, err := c.Get(ctx, "work", []string{"m1", "m2"})
	if err != nil || rows["m1"].OrganizationAppliedAt != nil || rows["m2"].OrganizationAppliedAt == nil {
		t.Fatal("final authorization lost independent completion")
	}
}

func TestConcurrentRedScreenDuringValidationBlocksAutomaticMove(t *testing.T) {
	base, _ := organizerSetup(t)
	base.categories = []string{"Work"}
	b := &validationPauseBox{organizerBox: base, validating: make(chan struct{}), release: make(chan struct{})}
	s := &pendingRedScreen{entered: make(chan struct{}), release: make(chan struct{})}
	g, _, path := setup(t, b, s)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	getDone := make(chan error, 1)
	go func() {
		v, err := g.Get(ctx, "m1")
		if err == nil && v.Verdict != model.Red {
			err = errors.New("expected red read")
		}
		getDone <- err
	}()
	receive(t, s.entered)
	r := rules.Rules{Rules: []rules.Rule{{Name: "route", Kind: rules.Folder, Target: "f1", Instructions: "Route"}}}
	organizeDone := make(chan error, 1)
	go func() { _, err := g.Organize(ctx, now.Add(-7*24*time.Hour), r, now, true, nil); organizeDone <- err }()
	receive(t, b.validating)
	close(s.release)
	if err := receive(t, getDone); err != nil {
		t.Fatal(err)
	}
	close(b.release)
	if err := receive(t, organizeDone); err != nil {
		t.Fatal(err)
	}
	if len(b.calls) != 0 {
		t.Fatalf("concurrent red verdict authorized automatic mutation: %v", b.calls)
	}
	for _, record := range auditRecords(t, path) {
		for _, raw := range record["results"].([]any) {
			if raw.(map[string]any)["ok"] == true {
				t.Fatal("denied move audited as successful")
			}
		}
	}
	// Manual operations remain verdict-independent.
	if _, err := g.Move(ctx, []string{"m1"}, "f1"); err != nil {
		t.Fatal(err)
	}
	if len(b.calls) != 1 || b.calls[0] != "move" {
		t.Fatal("manual red move refused")
	}
}
