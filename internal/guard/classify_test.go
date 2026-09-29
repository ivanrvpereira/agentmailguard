package guard_test

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
	"github.com/ivanrvpereira/agentmailguard-go/internal/rules"
)

type asked struct {
	security     bool
	organization bool
}
type classifier struct {
	*screener
	asked  []asked
	result model.Classification
	err    error
}

func (s *classifier) Classify(_ context.Context, _ map[string]any, security bool, questions *model.OrganizationQuestions) (model.Classification, error) {
	s.mu.Lock()
	s.asked = append(s.asked, asked{security, questions != nil})
	s.mu.Unlock()
	if s.err != nil {
		return model.Classification{}, s.err
	}
	r := s.result
	if !security {
		r.Screening = nil
	}
	if questions == nil {
		r.Organization = nil
	}
	return r, nil
}
func orgRules() rules.Rules {
	return rules.Rules{Rules: []rules.Rule{{Name: "route", Kind: rules.Folder, Target: "archive", Instructions: "CANARY instructions"}, {Name: "work", Kind: rules.Category, Target: "Work", Instructions: "Work?"}}}
}
func answer(v model.Verdict) model.Classification {
	score := 0.0
	if v == model.Red {
		score = .9
	}
	if v == model.Yellow {
		score = .35
	}
	return model.Classification{Screening: &model.Screening{Verdict: v, Score: &score}, Organization: &model.Organization{Folder: &model.FolderAnswer{Choice: "route", Confidence: .1, Probabilities: map[string]float64{"route": .6, "stay": .4}}, Categories: map[string]float64{"work": .9}}}
}
func TestInvalidOrganizationCategoryDoesNotEraseValidSecurity(t *testing.T) {
	b := &mailbox{messages: []model.Message{message("m1")}}
	s := &classifier{screener: &screener{verdict: model.Green}, result: answer(model.Green)}
	s.result.Organization.Categories["work"] = math.NaN()
	g, _, _ := setup(t, b, s)
	got, err := g.Classify(context.Background(), []string{"m1"}, orgRules())
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Error == nil {
		t.Fatal("invalid category accepted")
	}
	if *got[0].Error != "invalid answer category" || got[0].Row.Verdict != model.Green || got[0].Classified || got[0].Row.Organization != nil {
		t.Fatalf("invalid organization handling: error=%q classified=%v verdict=%v", *got[0].Error, got[0].Classified, got[0].Row.Verdict)
	}
}

func TestClassifySecurityFailuresPreserveIndependentAnswers(t *testing.T) {
	b := &mailbox{messages: []model.Message{message("m1")}}
	s := &classifier{screener: &screener{verdict: model.Green}, result: answer(model.Green)}
	s.result.Screening = nil
	g, _, _ := setup(t, b, s)
	ctx := context.Background()
	got, err := g.Classify(ctx, []string{"m1"}, orgRules())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Classified || got[0].Row.Verdict != model.Error || got[0].Row.Organization == nil {
		t.Fatalf("missing security lost organization or went green: %+v", got)
	}
	s.result = answer(model.Green)
	s.err = errors.New("CANARY raw response")
	got, err = g.Classify(ctx, []string{"m1"}, orgRules())
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Row.Verdict != model.Error || got[0].Row.Organization == nil || *got[0].Error != "Error" || s.asked[1] != (asked{true, false}) {
		t.Fatalf("retry did not preserve independent organization: %+v", got)
	}
	s.err = nil
	got, err = g.Classify(ctx, []string{"m1"}, orgRules())
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Row.Verdict != model.Green || got[0].Classified || s.asked[2] != (asked{true, false}) {
		t.Fatal("security recovery reclassified saved answers")
	}
}

func TestClassifyOrganizationOnlyFailureFailsClosedThenRecovers(t *testing.T) {
	b := &mailbox{messages: []model.Message{message("m1")}}
	s := &classifier{screener: &screener{verdict: model.Green}, result: answer(model.Green), err: errors.New("CANARY")}
	g, _, _ := setup(t, b, s)
	ctx := context.Background()
	if _, err := g.Get(ctx, "m1"); err != nil {
		t.Fatal(err)
	}
	got, err := g.Classify(ctx, []string{"m1"}, orgRules())
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Screened || got[0].Row.Verdict != model.Error || got[0].Row.Organization != nil || s.asked[0] != (asked{false, true}) {
		t.Fatalf("organization request failure retained green: %+v", got)
	}
	s.err = nil
	got, err = g.Classify(ctx, []string{"m1"}, orgRules())
	if err != nil {
		t.Fatal(err)
	}
	if !got[0].Screened || !got[0].Classified || got[0].Row.Verdict != model.Green || s.asked[1] != (asked{true, true}) {
		t.Fatal("failed combined state not retried")
	}
}

func TestClassifyRenderFailureAndDraftDoNotStopSibling(t *testing.T) {
	bad := message("bad")
	bad.Body = nil
	draft := message("draft")
	draft.IsDraft = true
	b := &mailbox{messages: []model.Message{bad, draft, message("good")}}
	s := &classifier{screener: &screener{verdict: model.Green}, result: answer(model.Green)}
	g, _, _ := setup(t, b, s)
	got, err := g.Classify(context.Background(), []string{"bad", "draft", "good"}, orgRules())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].Error == nil || *got[0].Error != "render ValueError" || got[0].Row.Verdict != model.Error || got[1].Error == nil || *got[1].Error != "draft" || got[1].Row != nil || !got[2].Classified || len(s.asked) != 1 {
		t.Fatalf("failures not isolated: %+v", got)
	}
}

func TestClassificationFetchFailureIsolatedWithSafeCode(t *testing.T) {
	b := &mailbox{messages: []model.Message{message("good")}}
	s := &classifier{screener: &screener{verdict: model.Green}, result: answer(model.Green)}
	g, _, _ := setup(t, b, s)
	got, err := g.Classify(context.Background(), []string{"gone", "good"}, orgRules())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Error == nil || *got[0].Error != "fetch ValueError" || got[0].Screened || !got[1].Classified {
		t.Fatalf("fetch failure not safely isolated: %+v first error=%q", got, *got[0].Error)
	}
}

func TestClassifyOnlyMissingQuestionsAndNeverExposesAnswers(t *testing.T) {
	b := &mailbox{messages: []model.Message{message("m1")}}
	s := &classifier{screener: &screener{verdict: model.Red}, result: answer(model.Red)}
	g, _, _ := setup(t, b, s)
	ctx := context.Background()
	if _, err := g.Get(ctx, "m1"); err != nil {
		t.Fatal(err)
	}
	results, err := g.Classify(ctx, []string{"m1"}, orgRules())
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Screened || !results[0].Classified || results[0].Row == nil || results[0].Row.Verdict != model.Red || results[0].Row.Organization == nil {
		t.Fatalf("classification did not preserve cached red and save answers: %+v", results)
	}
	if !reflect.DeepEqual(s.asked, []asked{{false, true}}) {
		t.Fatalf("wrong questions: %+v", s.asked)
	}
	gets := len(b.gets)
	results, err = g.Classify(ctx, []string{"m1"}, orgRules())
	if err != nil {
		t.Fatal(err)
	}
	if len(b.gets) != gets || len(s.asked) != 1 || results[0].Classified {
		t.Fatal("complete classification read or asked again")
	}
	v, err := g.Get(ctx, "m1")
	if err != nil {
		t.Fatal(err)
	}
	if v.Body != nil || v.Subject != nil {
		t.Fatal("classification exposed hidden fields")
	}
}
