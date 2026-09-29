package model_test

import (
	"math"
	"testing"

	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
)

func TestOrganizationValidity(t *testing.T) {
	for _, tt := range []struct {
		name  string
		org   model.Organization
		valid bool
	}{
		{"empty completed answer", model.Organization{}, true},
		{"low confidence is not a cutoff", model.Organization{Folder: &model.FolderAnswer{Choice: "finance", Confidence: .1, Probabilities: map[string]float64{"finance": .34, "other": .33, "stay": .33}}}, true},
		{"rounded total", model.Organization{Folder: &model.FolderAnswer{Choice: "stay", Confidence: 1, Probabilities: map[string]float64{"stay": .98}}}, true},
		{"unknown choice", model.Organization{Folder: &model.FolderAnswer{Choice: "missing", Confidence: 1, Probabilities: map[string]float64{"stay": 1}}}, false},
		{"bad total", model.Organization{Folder: &model.FolderAnswer{Choice: "stay", Confidence: 1, Probabilities: map[string]float64{"stay": .9}}}, false},
		{"bad confidence", model.Organization{Folder: &model.FolderAnswer{Choice: "stay", Confidence: math.NaN(), Probabilities: map[string]float64{"stay": 1}}}, false},
		{"bad category", model.Organization{Categories: map[string]float64{"urgent": math.Inf(1)}}, false},
		{"instructions are not rule names", model.Organization{Categories: map[string]float64{"Secret instructions!": .9}}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.org.Validate(); (err == nil) != tt.valid {
				t.Fatalf("valid=%v, error=%v", tt.valid, err)
			}
		})
	}
}

func TestDecisionPrecedenceAndInvalidScores(t *testing.T) {
	for _, tt := range []struct {
		injection, security float64
		want                model.Verdict
	}{
		{0, 0, model.Green}, {0.2999, 0, model.Green}, {0.30, 0, model.Yellow},
		{0.3999, 0, model.Yellow}, {0.40, 1, model.Red}, {0.30, 0.80, model.Sealed},
		{0, 0.7999, model.Green}, {math.NaN(), 0, model.Error}, {0, math.Inf(1), model.Error}, {-0.1, 0, model.Error}, {1.1, 0, model.Error},
	} {
		if got := model.Decide(tt.injection, tt.security); got != tt.want {
			t.Errorf("Decide(%v, %v) = %v, want %v", tt.injection, tt.security, got, tt.want)
		}
	}
}
