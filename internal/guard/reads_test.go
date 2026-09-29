package guard_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
)

type screeningResult struct {
	model.Screener
	result model.Screening
	err    error
	calls  int
}

func (s *screeningResult) Screen(context.Context, map[string]any) (model.Screening, error) {
	s.calls++
	return s.result, s.err
}

type responseBox struct {
	*mailbox
	response []model.Message
}

func (b *responseBox) Get(context.Context, []string, bool) ([]model.Message, error) {
	return b.response, nil
}
func TestUnexpectedFetchedIDsNeverReachScreeningOrViews(t *testing.T) {
	for _, responses := range [][]model.Message{nil, {message("other")}, {message("m1"), message("m1")}} {
		t.Run("malformed", func(t *testing.T) {
			b := &responseBox{mailbox: &mailbox{messages: []model.Message{message("m1")}}, response: responses}
			s := &screener{verdict: model.Green}
			g, _, _ := setup(t, b, s)
			ctx := context.Background()
			v, err := g.Get(ctx, "m1")
			if err == nil || v.Subject != nil || v.Body != nil || s.calls != 0 {
				t.Fatalf("malformed fetched response exposed fields or screened: %+v %v", v, err)
			}
			if got, err := g.List(ctx, "inbox", 1, 0); err == nil || len(got) != 0 || s.calls != 0 {
				t.Fatal("malformed list body response accepted")
			}
			b.response = []model.Message{message("m1")}
			v, err = g.Get(ctx, "m1")
			if err != nil || v.Verdict != model.Green || s.calls != 1 {
				t.Fatal("malformed response polluted cache")
			}
		})
	}
}

func TestFailClosedScreeningAndErrorRetryThroughReads(t *testing.T) {
	for _, result := range []model.Screening{{}, {Verdict: model.Green}, {Verdict: model.Green, Score: ptr(.9)}, {Verdict: model.Error, Error: ptr("CANARY raw")}} {
		t.Run(string(result.Verdict), func(t *testing.T) {
			b := &mailbox{messages: []model.Message{message("m1")}}
			s := &screeningResult{result: result}
			g, _, _ := setup(t, b, s)
			ctx := context.Background()
			v, err := g.Get(ctx, "m1")
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(v)
			if v.Verdict != model.Error || v.Body != nil || v.Subject != nil || v.Sender != nil || strings.Contains(string(data), "CANARY") {
				t.Fatalf("failed screening exposed fields: %s", data)
			}
			s.result = model.Screening{Verdict: model.Green, Score: ptr(0.0)}
			v, err = g.Get(ctx, "m1")
			if err != nil {
				t.Fatal(err)
			}
			if v.Verdict != model.Green || s.calls != 2 {
				t.Fatal("error row was not retried")
			}
			b.messages[0].ContentType = nil
			v, err = g.Get(ctx, "m1")
			if err != nil {
				t.Fatal(err)
			}
			if v.Verdict != model.Error || v.Body != nil || s.calls != 2 {
				t.Fatal("cached green render failure did not fail closed before screening")
			}
		})
	}
}
func TestDraftIdentityOverridesPreviouslyCachedVerdict(t *testing.T) {
	for _, verdict := range []model.Verdict{model.Green, model.Red} {
		t.Run(string(verdict), func(t *testing.T) {
			b := &mailbox{messages: []model.Message{message("m1")}}
			s := &screener{verdict: verdict}
			g, _, _ := setup(t, b, s)
			ctx := context.Background()
			if _, err := g.Get(ctx, "m1"); err != nil {
				t.Fatal(err)
			}
			b.messages[0].IsDraft = true
			s.verdict = model.Red
			for range 2 {
				v, err := g.Get(ctx, "m1")
				if err != nil {
					t.Fatal(err)
				}
				if v.Verdict != model.Red || v.Body != nil {
					t.Fatal("draft reused cached green")
				}
			}
			if s.calls != 3 {
				t.Fatalf("draft was not screened every read: %d", s.calls)
			}
			b.messages[0].IsDraft = false
			s.verdict = model.Yellow
			v, err := g.Get(ctx, "m1")
			if err != nil {
				t.Fatal(err)
			}
			if v.Verdict != verdict || s.calls != 3 {
				t.Fatal("draft screening overwrote persistent verdict")
			}
		})
	}
}
func TestListReusesFetchedEmptyBodiesAndValidatesReadRequests(t *testing.T) {
	m := message("empty")
	m.Body = ptr("")
	b := &mailbox{messages: []model.Message{m}, listedBodies: true}
	s := &screener{verdict: model.Green}
	g, _, _ := setup(t, b, s)
	ctx := context.Background()
	got, err := g.Search(ctx, model.SearchQuery{Text: ptr("x")}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Verdict != model.Green || len(b.gets) != 0 || s.calls != 1 {
		t.Fatal("fetched empty list body was fetched again or not screened")
	}
	for _, n := range []int{0, 101} {
		if _, err := g.List(ctx, "inbox", n, 0); err == nil {
			t.Fatal("bad list count allowed")
		}
		if _, err := g.Search(ctx, model.SearchQuery{Text: ptr("x")}, n); err == nil {
			t.Fatal("bad search count allowed")
		}
	}
	if _, err := g.List(ctx, "inbox", 1, -1); err == nil {
		t.Fatal("negative skip allowed")
	}
	if _, err := g.Search(ctx, model.SearchQuery{Folder: ptr("inbox")}, 1); err == nil {
		t.Fatal("folder-only search allowed")
	}
	b.messages[0].Body = nil
	if _, err := g.Get(ctx, "empty"); err == nil {
		t.Fatal("missing fetched body accepted as empty")
	}
	ctx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := g.Get(ctx, "empty"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read returned %v", err)
	}
}
