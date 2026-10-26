package guard_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// threadBox answers Thread with response, and serves every other read from the embedded mailbox.
type threadBox struct {
	*mailbox
	response []model.Message
	calls    int
}

func (b *threadBox) Thread(context.Context, string) ([]model.Message, error) {
	b.calls++
	return b.response, nil
}

// threadMessage is message id in conversation t1, received hours before now, in folder.
func threadMessage(id string, hours int, folder string) model.Message {
	m := message(id)
	m.ThreadID, m.ReceivedAt, m.Folders = "t1", now.Add(-time.Duration(hours)*time.Hour), []string{folder}
	return m
}

// metadata drops the bodies and authentication results, as a Thread result has none.
func metadata(messages ...model.Message) []model.Message {
	out := []model.Message{}
	for _, m := range messages {
		m.Body, m.Auth = nil, nil
		out = append(out, m)
	}
	return out
}

func TestThreadScreensEachMessageAsListDoes(t *testing.T) {
	full := []model.Message{threadMessage("m2", 2, "trash"), threadMessage("m1", 3, "sent")}
	b := &threadBox{mailbox: &mailbox{messages: full}, response: metadata(full...)}
	s := &screener{verdict: model.Red}
	g, _, _ := setup(t, b, s)
	ctx := t.Context()
	got, err := g.Thread(ctx, "t1")
	if err != nil || len(got) != 2 || got[0].ID != "m2" || got[1].ID != "m1" || s.calls != 2 {
		t.Fatalf("thread=%+v err=%v screenings=%d", got, err, s.calls)
	}
	for _, m := range got {
		if m.Verdict != model.Red || m.Subject != nil || m.Sender == nil || m.Sender.Name != nil || m.To != nil || m.Attachments != nil {
			t.Fatalf("red message exposed hidden fields: %+v", m)
		}
	}
	list, err := g.List(ctx, "inbox", 25, 0)
	if err != nil || !reflect.DeepEqual(list, got) {
		t.Fatalf("list=%+v thread=%+v err=%v", list, got, err)
	}
}

func TestThreadFailsClosedOnAResultThatBreaksTheContract(t *testing.T) {
	m1, m2, m3 := threadMessage("m1", 3, "inbox"), threadMessage("m2", 2, "inbox"), threadMessage("m3", 1, "inbox")
	unnamed, other := m2, m2
	unnamed.ID, other.ThreadID = "", "t2"
	for name, response := range map[string][]model.Message{
		"no messages":  nil,
		"empty ID":     {unnamed, m1},
		"duplicate ID": {m1, m1},
		"other thread": {other, m1},
		"oldest first": {m2, m3},
	} {
		t.Run(name, func(t *testing.T) {
			b := &threadBox{mailbox: &mailbox{messages: []model.Message{m1, m2, m3}}, response: metadata(response...)}
			s := &screener{verdict: model.Green}
			g, _, _ := setup(t, b, s)
			got, err := g.Thread(t.Context(), "t1")
			if err != guard.GuardError("ValueError") || got != nil || s.calls != 0 {
				t.Fatalf("thread=%+v err=%v screenings=%d", got, err, s.calls)
			}
		})
	}
}

func TestThreadRefusesAnEmptyThreadBeforeTheMailbox(t *testing.T) {
	b := &threadBox{mailbox: &mailbox{}}
	g, _, _ := setup(t, b, &screener{verdict: model.Green})
	if _, err := g.Thread(t.Context(), ""); err != guard.GuardError("give a thread ID") {
		t.Errorf("err=%v", err)
	}
	if b.calls != 0 {
		t.Fatalf("mailbox called %d times", b.calls)
	}
}
