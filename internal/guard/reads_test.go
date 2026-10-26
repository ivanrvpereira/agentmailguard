package guard_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/views"
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
			if got, err := summaries(g.List(ctx, "inbox", model.PageRequest{})); err == nil || len(got) != 0 || s.calls != 0 {
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
	for _, result := range []model.Screening{{}, {Verdict: model.Green}, {Verdict: model.Green, Score: new(.9)}, {Verdict: model.Error, Error: new("CANARY raw")}} {
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
			s.result = model.Screening{Verdict: model.Green, Score: new(0.0)}
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
func TestSearchByRecipientAloneIsASearchTerm(t *testing.T) {
	g, _, _ := setup(t, &mailbox{messages: []model.Message{message("m1")}}, &screener{verdict: model.Green})
	got, err := summaries(g.Search(t.Context(), model.SearchQuery{To: ptr("ana@example.test")}, model.PageRequest{}))
	if err != nil || len(got) != 1 {
		t.Fatalf("to-only search=%v err=%v", got, err)
	}
	_, err = summaries(g.Search(t.Context(), model.SearchQuery{To: ptr(""), Folder: ptr("inbox")}, model.PageRequest{}))
	if err == nil || err.Error() != "search needs at least one of text, from, to, subject, after, before" {
		t.Fatalf("search without a term returned %v", err)
	}
}

// The guard re-checks the page request before the adapter, and treats the adapter's next-page marker as provider
// data: a marker that model.ValidPage refuses fails the whole page.
func TestListAndSearchCheckThePageRequestAndTheNextMarker(t *testing.T) {
	b := &mailbox{messages: []model.Message{message("m1")}}
	g, _, _ := setup(t, b, &screener{verdict: model.Green})
	ctx := t.Context()
	for name, call := range map[string]func(model.PageRequest) (views.MailPage, error){
		"List": func(p model.PageRequest) (views.MailPage, error) { return g.List(ctx, "inbox", p) },
		"Search": func(p model.PageRequest) (views.MailPage, error) {
			return g.Search(ctx, model.SearchQuery{Text: new("x")}, p)
		},
	} {
		long := strings.Repeat("a", model.MaxPage)
		b.next = long
		asked := model.PageRequest{Size: 5, Page: "https://graph.example.test/v1.0/me/messages?$skip=5"}
		got, err := call(asked)
		if err != nil || len(got.Messages) != 1 || got.NextPage != long || b.pages[len(b.pages)-1] != asked {
			t.Fatalf("%s: next=%.20q err=%v pages=%v", name, got.NextPage, err, b.pages)
		}
		before := len(b.pages)
		for _, p := range []model.PageRequest{{Size: -1}, {Page: "a b"}, {Page: "é"}, {Page: long + "a"}} {
			if got, err := call(p); err != guard.GuardError("invalid request") || got.Messages != nil {
				t.Errorf("%s %+.20v accepted: %d messages, %v", name, p, len(got.Messages), err)
			}
		}
		if len(b.pages) != before {
			t.Fatalf("%s: an invalid page request reached the mailbox", name)
		}
		for _, next := range []string{"a b", "a\x1b[31m", "\u00e9", "a\nb", long + "a"} {
			b.next = next
			if got, err := call(model.PageRequest{}); err != guard.GuardError("ValueError") || got.Messages != nil || got.NextPage != "" {
				t.Errorf("%s next %.20q accepted: %d messages, next %.20q, %v", name, next, len(got.Messages), got.NextPage, err)
			}
		}
	}
}
func TestListReusesFetchedEmptyBodiesAndValidatesReadRequests(t *testing.T) {
	m := message("empty")
	m.Body = new("")
	b := &mailbox{messages: []model.Message{m}, listedBodies: true}
	s := &screener{verdict: model.Green}
	g, _, _ := setup(t, b, s)
	ctx := context.Background()
	got, err := summaries(g.Search(ctx, model.SearchQuery{Text: new("x")}, model.PageRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Verdict != model.Green || len(b.gets) != 0 || s.calls != 1 {
		t.Fatal("fetched empty list body was fetched again or not screened")
	}
	if _, err := summaries(g.Search(ctx, model.SearchQuery{Folder: new("inbox")}, model.PageRequest{})); err == nil {
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
