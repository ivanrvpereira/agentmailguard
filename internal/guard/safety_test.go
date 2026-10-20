package guard_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/rules"
	"github.com/ivanrvpereira/agentmailguard/internal/views"
)

func TestInvalidCachedScreeningCannotAuthorizeViewsOrOrganization(t *testing.T) {
	for _, bad := range []struct {
		name    string
		verdict model.Verdict
		score   *float64
		err     *string
	}{
		{"missing score", model.Green, nil, nil},
		{"green red score", model.Green, new(.9), nil},
		{"yellow green score", model.Yellow, new(.1), nil},
		{"red green score", model.Red, new(.1), nil},
		{"sealed red score", model.Sealed, new(.9), nil},
		{"green error", model.Green, new(.1), new("ValidationError")},
	} {
		for _, surface := range []string{"Get", "Links", "List", "Search", "Classify", "Organize", "Noop"} {
			t.Run(bad.name+"/"+surface, func(t *testing.T) {
				base, s := organizerSetup(t)
				base.categories = []string{"Work"}
				s.verdict = model.Red
				s.result = answer(model.Red)
				g, c, path := setup(t, base, s)
				ctx := context.Background()
				if err := c.Put(ctx, "work", cache.Row{MessageID: "m1", ReceivedAt: now, ScreenedAt: now, Verdict: bad.verdict, Score: bad.score, Error: bad.err}); err != nil {
					t.Fatal(err)
				}
				if err := c.SaveOrganization(ctx, "work", "m1", *answer(model.Green).Organization); err != nil {
					t.Fatal(err)
				}
				if err := c.MarkSecurityApplied(ctx, "work", "m1", model.Yellow); err != nil {
					t.Fatal(err)
				}
				switch surface {
				case "Get":
					v, err := g.Get(ctx, "m1")
					if err != nil || v.Verdict != model.Red || v.Body != nil {
						t.Fatalf("invalid cache authorized view: verdict=%s body=%v err=%v", v.Verdict, v.Body != nil, err)
					}
				case "Links":
					v, err := g.Links(ctx, "m1", nil)
					if err == nil || len(v) != 0 {
						t.Fatal("invalid cache authorized links")
					}
				case "List", "Search":
					var v []views.MailSummary
					var err error
					if surface == "Search" {
						v, err = g.Search(ctx, model.SearchQuery{Text: new("synthetic")}, 1)
					} else {
						v, err = g.List(ctx, "inbox", 1, 0)
					}
					if err != nil || len(v) != 1 || v[0].Verdict != model.Red || v[0].Subject != nil {
						t.Fatalf("invalid cache authorized summary: %+v %v", v, err)
					}
				case "Classify":
					v, err := g.Classify(ctx, []string{"m1"}, orgRules())
					if err != nil || len(v) != 1 || !v[0].Screened || v[0].Classified || v[0].Row.Verdict != model.Red {
						t.Fatalf("invalid cache reused classification: %+v %v", v, err)
					}
				case "Organize", "Noop":
					r := orgRules()
					if surface == "Noop" {
						r = rules.Rules{Rules: []rules.Rule{{Name: "route", Kind: rules.Category, Target: "Work", Instructions: "changed kind"}}}
					}
					report, err := g.Organize(ctx, now.Add(-7*24*time.Hour), r, now, true, nil)
					if err != nil || len(report.Planned) != 0 || len(base.calls) != 0 {
						t.Fatalf("invalid cache authorized business actions/noop: %+v calls=%v error=%v", report, base.calls, err)
					}
					// Re-screened red; no Red category, so no tag and nothing applied.
					checkStats(t, report.Stats, guard.Stats{Messages: 1, Verdicts: guard.Verdicts{Red: 1}, Skipped: 1})
				}
				if s.calls+len(s.asked) != 1 {
					t.Fatalf("invalid screening was not retried exactly once: screen=%d classify=%d", s.calls, len(s.asked))
				}
				if len(s.asked) != 0 && s.asked[0] != (asked{true, false}) {
					t.Fatal("independent organization answers were re-asked")
				}
				rows, err := c.Get(ctx, "work", []string{"m1"})
				if err != nil || rows["m1"].Organization == nil || rows["m1"].SecurityAppliedVerdict == nil || *rows["m1"].SecurityAppliedVerdict != model.Yellow || rows["m1"].OrganizationAppliedAt != nil {
					t.Fatal("retry lost independent metadata or completed invalid work")
				}
				if len(auditRecords(t, path)) != 0 {
					t.Fatal("invalid screening caused a mutation audit")
				}
			})
		}
	}
}

func TestGuardFormattingRedactsPopulatedRealJev(t *testing.T) {
	s := newCoordinatedJev(t)
	g, _, _ := setup(t, concurrentMailbox{}, s)
	ctx := t.Context()
	done := make(chan error, 1)
	go func() { _, err := g.Get(ctx, "m00"); done <- err }()
	p := receive(t, s.entered)
	close(p.allow)
	if err := receive(t, done); err != nil {
		t.Fatal(err)
	}
	for _, value := range []any{g, *g} {
		for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q", "%x", "%X", "%d", "%f", "%t", "%c", "%U", "%20.5s"} {
			if fmt.Sprintf(verb, value) != "Guard{redacted}" {
				t.Errorf("Guard formatting is not redacted for %T %s", value, verb)
			}
		}
	}
}
