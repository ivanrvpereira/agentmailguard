package guard_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/audit"
	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

var now = time.Date(2026, 9, 28, 15, 30, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }
func message(id string) model.Message {
	return model.Message{ID: id, Subject: id, Sender: model.Address{Name: "DISPLAYNAME", Address: "alice@example.com"}, ReceivedAt: now.Add(-24 * time.Hour), Body: ptr("BODYTEXT https://example.com/report"), ContentType: ptr(model.Text), Auth: &model.AuthResults{SPF: ptr("pass")}, Flag: model.NoFlag, Categories: []string{"Work", "Planted"}}
}

type fetch struct {
	ids  []string
	body bool
}
type mailbox struct {
	model.Mailbox
	mu           sync.Mutex
	messages     []model.Message
	gets         []fetch
	listedBodies bool
}

func (b *mailbox) Categories(context.Context) ([]string, error) { return []string{"Work"}, nil }
func (b *mailbox) Search(ctx context.Context, _ model.SearchQuery, count int, body bool) ([]model.Message, error) {
	return b.List(ctx, "inbox", count, 0, body)
}
func (b *mailbox) List(_ context.Context, _ string, _, _ int, _ bool) ([]model.Message, error) {
	out := append([]model.Message{}, b.messages...)
	if !b.listedBodies {
		for i := range out {
			out[i].Body = nil
			out[i].Auth = nil
		}
	}
	return out, nil
}
func (b *mailbox) Get(_ context.Context, ids []string, body bool) ([]model.Message, error) {
	b.mu.Lock()
	b.gets = append(b.gets, fetch{append([]string{}, ids...), body})
	b.mu.Unlock()
	out := []model.Message{}
	for _, id := range ids {
		for _, m := range b.messages {
			if m.ID == id {
				if !body {
					m.Body = nil
					m.Auth = nil
				}
				out = append(out, m)
			}
		}
	}
	return out, nil
}

type screener struct {
	model.Screener
	mu      sync.Mutex
	calls   int
	verdict model.Verdict
}

func (s *screener) Screen(context.Context, map[string]any) (model.Screening, error) {
	s.mu.Lock()
	s.calls++
	s.mu.Unlock()
	score := 0.0
	if s.verdict == model.Red {
		score = .9
	}
	return model.Screening{Verdict: s.verdict, Score: &score}, nil
}

// CheckAction never flags, so writes behave as before the Jev action check.
func (s *screener) CheckAction(context.Context, string, string, map[string]any) (model.ActionCheck, error) {
	return model.ActionScore(0), nil
}
func setup(t *testing.T, b model.Mailbox, s model.Screener) (*guard.Guard, *cache.Cache, string) {
	t.Helper()
	dir := t.TempDir()
	c, err := cache.Open(context.Background(), filepath.Join(dir, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	path := filepath.Join(dir, "audit.jsonl")
	return guard.New("work", b, nil, s, c, audit.New(path), guard.Options{Now: func() time.Time { return now }, Policy: allowAll}), c, path
}
func TestLinksResolveSafeURLsAndRefuseSecretsIndependently(t *testing.T) {
	m := message("m1")
	m.Body = ptr("https://example.com/report https://example.com/reset?token=secret")
	b := &mailbox{messages: []model.Message{m}, listedBodies: true}
	s := &screener{verdict: model.Green}
	g, _, _ := setup(t, b, s)
	got, err := g.Links(context.Background(), "m1", []string{"L1", "L2", "L9"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].URL == nil || *got[0].URL != "https://example.com/report" || got[1].Error == nil || *got[1].Error != "the URL likely carries a secret (sign-in, reset, token)" || got[2].Error == nil || *got[2].Error != "no such link in this message" {
		t.Fatalf("incorrect per-link result: %+v", got)
	}
}

func TestCachedRedReadsNeverFetchOrExposeBody(t *testing.T) {
	b := &mailbox{messages: []model.Message{message("m1")}}
	s := &screener{verdict: model.Red}
	g, _, _ := setup(t, b, s)
	first, err := g.List(context.Background(), "inbox", 25, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 1 || first[0].Verdict != model.Red || first[0].Subject != nil || first[0].Sender.Name != nil {
		t.Fatalf("red summary exposed fields: %+v", first)
	}
	_, err = g.List(context.Background(), "inbox", 25, 0)
	if err != nil {
		t.Fatal(err)
	}
	v, err := g.Get(context.Background(), "m1")
	if err != nil {
		t.Fatal(err)
	}
	if v.Body != nil || v.Subject != nil || len(v.Categories) != 1 || v.Categories[0] != "Work" {
		t.Fatalf("unsafe red view: %+v", v)
	}
	if s.calls != 1 || len(b.gets) != 2 || !b.gets[0].body || b.gets[1].body {
		t.Fatalf("cached hidden read requested body/screening: screens=%d gets=%+v", s.calls, b.gets)
	}
}
