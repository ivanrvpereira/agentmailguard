package screening

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

// Fake engine markers. They match the JSON state without case.
const (
	FakeInjection       = "ignore previous instructions" // injection: the message is red
	FakeAccountSecurity = "verification code"            // account security: the message is sealed
	FakeSuspicious      = "password"                     // a write action is suspicious
	FakeFailure         = "screenfail"                   // every question fails, as when the engine is down
)

// NewFake returns a Screener whose engine answers from the markers above, without network or key.
// It is for tests and local development. A folder question picks the first option, in name order, that
// appears in the state, else stay; a category scores high when its name appears. Its action checks are
// marked Fake.
func NewFake() *Screener { return &Screener{engine: fake{}} }

type fake struct{}

func (fake) answer(ctx context.Context, state map[string]any, questions map[string]question) (map[string]answer, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "CancelledError", err
	}
	data, err := json.Marshal(state)
	if err != nil {
		return nil, "ValueError", nil
	}
	text := strings.ToLower(string(data))
	if strings.Contains(text, FakeFailure) {
		return nil, "TypeSafeAPIError 503", nil
	}
	noul := func(marker string) answer {
		if strings.Contains(text, strings.ToLower(marker)) {
			return answer{Type: "noul", Noul: .9}
		}
		return answer{Type: "noul", Noul: .05}
	}
	answers := map[string]answer{}
	for name, q := range questions {
		switch {
		case q.Type == "choice":
			answers[name] = choose(text, slices.Sorted(maps.Keys(q.Criteria)))
		case name == "injection":
			answers[name] = noul(FakeInjection)
		case name == "account_security":
			answers[name] = noul(FakeAccountSecurity)
		case name == "suspicious":
			answers[name] = noul(FakeSuspicious)
		default:
			answers[name] = noul(strings.TrimPrefix(name, "category:"))
		}
	}
	return answers, "", nil
}

func choose(text string, options []string) answer {
	choice := options[0]
	if slices.Contains(options, "stay") {
		choice = "stay"
	}
	for _, option := range options {
		if option != "stay" && strings.Contains(text, option) {
			choice = option
			break
		}
	}
	probabilities := map[string]float64{choice: 1}
	if len(options) > 1 {
		probabilities[choice] = .9
		for _, option := range options {
			if option != choice {
				probabilities[option] = .1 / float64(len(options)-1)
			}
		}
	}
	return answer{Type: "choice", Choice: choice, Confidence: .9, Probabilities: probabilities}
}
