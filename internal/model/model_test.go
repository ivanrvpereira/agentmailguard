package model_test

import (
	"math"
	"reflect"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
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

// ReplyRecipients picks the recipients as Gmail on the web does: Reply-To or From, the original To for a sent
// message, and with all also the original To and CC, without the own addresses and without duplicates ignoring case.
func TestReplyRecipientsFollowTheGmailWebRule(t *testing.T) {
	own := []string{"me@example.test", "Alias@Example.test"}
	for _, tt := range []struct {
		name string
		src  model.ReplySource
		all  bool
		want model.Recipients
	}{
		{"reply goes to the sender", model.ReplySource{From: []string{"ana@example.test"}, To: []string{"me@example.test", "bo@example.test"}, CC: []string{"cy@example.test"}}, false,
			model.Recipients{To: []string{"ana@example.test"}, CC: []string{}, BCC: []string{}}},
		{"reply goes to Reply-To", model.ReplySource{From: []string{"ana@example.test"}, ReplyTo: []string{"list@example.test"}, To: []string{"me@example.test"}}, false,
			model.Recipients{To: []string{"list@example.test"}, CC: []string{}, BCC: []string{}}},
		{"reply to a sent message goes to its To", model.ReplySource{From: []string{"me@example.test"}, ReplyTo: []string{"ignored@example.test"}, To: []string{"bo@example.test", "dan@example.test"}, CC: []string{"cy@example.test"}, Sent: true}, false,
			model.Recipients{To: []string{"bo@example.test", "dan@example.test"}, CC: []string{}, BCC: []string{}}},
		{"reply all copies the original To and CC without own addresses and duplicates", model.ReplySource{From: []string{"ana@example.test"}, To: []string{"ME@example.test", "bo@example.test", "Ana@Example.test"}, CC: []string{"alias@example.test", "BO@example.test", "cy@example.test"}}, true,
			model.Recipients{To: []string{"ana@example.test"}, CC: []string{"bo@example.test", "cy@example.test"}, BCC: []string{}}},
		{"reply all removes the own address from Reply-To and a CC address that is in To", model.ReplySource{From: []string{"ana@example.test"}, ReplyTo: []string{"list@example.test", "me@example.test"}, To: []string{"list@example.test", "me@example.test"}}, true,
			model.Recipients{To: []string{"list@example.test"}, CC: []string{}, BCC: []string{}}},
		{"reply all to a sent message goes to the original To and copies the original CC", model.ReplySource{From: []string{"alias@example.test"}, To: []string{"bo@example.test", "alias@example.test"}, CC: []string{"cy@example.test", "Bo@example.test"}, Sent: true}, true,
			model.Recipients{To: []string{"bo@example.test"}, CC: []string{"cy@example.test"}, BCC: []string{}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := model.ReplyRecipients(tt.src, tt.all, own); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// With adds the agent's CC and BCC to the reply recipients; an address already there, ignoring case, is not added
// again, and none is removed.
func TestReplyRecipientsWithTheAgentCCAndBCC(t *testing.T) {
	r := model.Recipients{To: []string{"ana@example.test"}, CC: []string{"bo@example.test"}, BCC: []string{}}
	got := r.With([]string{"BO@example.test", "cy@example.test", "cy@example.test"}, []string{"ana@example.test", "dan@example.test", "Cy@example.test", "DAN@example.test"})
	want := model.Recipients{To: []string{"ana@example.test"}, CC: []string{"bo@example.test", "cy@example.test"}, BCC: []string{"dan@example.test"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if got := (model.Recipients{}).With(nil, nil); !reflect.DeepEqual(got, model.Recipients{To: []string{}, CC: []string{}, BCC: []string{}}) {
		t.Fatalf("empty: %+v", got)
	}
}

func TestParseFolderNameAcceptsTheCommonNamesInAnyCase(t *testing.T) {
	for s, want := range map[string]model.FolderName{"inbox": model.Inbox, "INBOX": model.Inbox, "Trash": model.Trash, "SPAM": model.Spam, "spam": model.Spam} {
		if got, ok := model.ParseFolderName(s); !ok || got != want {
			t.Fatalf("%s: got %q %v", s, got, ok)
		}
	}
	for _, s := range []string{"", "sent", "archive", "junkemail", "deleteditems", "TRASH ", "Label_1"} {
		if got, ok := model.ParseFolderName(s); ok || got != "" {
			t.Fatalf("%s: got %q %v", s, got, ok)
		}
	}
}
