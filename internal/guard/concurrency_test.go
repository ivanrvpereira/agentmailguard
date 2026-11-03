package guard_test

import (
	"context"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

type concurrentMailbox struct{ model.Mailbox }

func (concurrentMailbox) ClosedFolders(ctx context.Context) (map[string]model.Bin, error) {
	return (&mailbox{}).ClosedFolders(ctx)
}

func (concurrentMailbox) Get(_ context.Context, ids []string, body bool) ([]model.Message, error) {
	out := []model.Message{}
	for _, id := range ids {
		m := message(id)
		if !body {
			m.Body = nil
		}
		out = append(out, m)
	}
	return out, nil
}
func (concurrentMailbox) Categories(context.Context) ([]string, error) { return []string{"Work"}, nil }

type concurrentScreener struct{}

func (concurrentScreener) Screen(context.Context, map[string]any) (model.Screening, error) {
	return *answer(model.Green).Screening, nil
}
func (concurrentScreener) CheckAction(context.Context, string, string, map[string]any) (model.ActionCheck, error) {
	return model.ActionScore(0), nil
}
func (concurrentScreener) Classify(_ context.Context, _ map[string]any, security bool, questions *model.OrganizationQuestions) (model.Classification, error) {
	r := answer(model.Green)
	if !security {
		r.Screening = nil
	}
	if questions == nil {
		r.Organization = nil
	}
	return r, nil
}
func TestConcurrentOrganizerApplyClassifiesInParallelButMutatesOnce(t *testing.T) {
	b, s := organizerSetup(t)
	b.messages, b.scan.Messages = nil, nil
	for _, id := range batchIDs() {
		b.messages = append(b.messages, message(id))
		b.scan.Messages = append(b.scan.Messages, model.InboxEntry{ID: id, ReceivedAt: now})
	}
	g, _, path := setup(t, b, s)
	ctx, cancel := context.WithTimeout(context.Background(), bound)
	defer cancel()
	start := make(chan struct{})
	done := make(chan error, 8)
	for range 8 {
		go func() {
			<-start
			_, err := g.Organize(ctx, now.Add(-7*24*time.Hour), orgRules(), now, true, nil)
			done <- err
		}()
	}
	close(start)
	for range 8 {
		if err := receive(t, done); err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(b.calls, []string{"categorize", "archive"}) || len(s.asked) != 40 {
		t.Fatalf("concurrent apply repeated classification/actions: calls=%v questions=%d", b.calls, len(s.asked))
	}
	records := auditRecords(t, path)
	if len(records) != 2 || records[0]["operation"] != "categorize" || records[1]["operation"] != "archive" || len(records[0]["ids"].([]any)) != 40 || len(records[1]["ids"].([]any)) != 40 {
		t.Fatalf("concurrent apply lost category-before-move or repeated audit: %v", records)
	}
}

func TestConcurrentReadsAndClassificationShareCacheSafely(t *testing.T) {
	g, _, _ := setup(t, concurrentMailbox{}, concurrentScreener{})
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := range 20 {
		wg.Go(func() {
			if i%2 == 0 {
				_, err := g.Get(ctx, "same")
				errs <- err
			} else {
				_, err := g.Classify(ctx, []string{"same"}, orgRules())
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	v, err := g.Get(ctx, "same")
	if err != nil || v.Verdict != model.Green || v.Body == nil {
		t.Fatalf("concurrent cache lost verdict: %+v %v", v, err)
	}
	classified, err := g.Classify(ctx, []string{"same"}, orgRules())
	if err != nil || len(classified) != 1 || classified[0].Screened || classified[0].Classified || classified[0].Row.Organization == nil {
		t.Fatalf("concurrent security update lost organization answers: %+v %v", classified, err)
	}
}
