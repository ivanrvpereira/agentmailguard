package guard_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// threadBox answers Thread with response and older, and serves every other read from the embedded mailbox.
type threadBox struct {
	*mailbox
	response []model.Message
	older    bool
	calls    int
}

func (b *threadBox) Thread(context.Context, string, int) ([]model.Message, bool, error) {
	b.calls++
	return b.response, b.older, nil
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

func TestThreadScreensEachMessageAsListDoesAndShowsItsFolders(t *testing.T) {
	full := []model.Message{threadMessage("m1", 3, "sent"), threadMessage("m2", 2, "trash")}
	b := &threadBox{mailbox: &mailbox{messages: full}, response: metadata(full...), older: true}
	s := &screener{verdict: model.Red}
	g, _, _ := setup(t, b, s)
	ctx := t.Context()
	got, err := g.Thread(ctx, "t1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got.ThreadID != "t1" || got.NewestReceivedAt != "2026-09-28T13:30:00+00:00" || !got.Older || len(got.Messages) != 2 || s.calls != 2 {
		t.Fatalf("thread=%+v screenings=%d", got, s.calls)
	}
	if !reflect.DeepEqual(got.Messages[0].Folders, []string{"sent"}) || !reflect.DeepEqual(got.Messages[1].Folders, []string{"trash"}) {
		t.Fatalf("folders=%v %v", got.Messages[0].Folders, got.Messages[1].Folders)
	}
	for _, m := range got.Messages {
		if m.Verdict != model.Red || m.Subject != nil || m.Sender == nil || m.Sender.Name != nil || m.To != nil || m.Attachments != nil {
			t.Fatalf("red message exposed hidden fields: %+v", m)
		}
	}
	list, err := g.List(ctx, "inbox", 25, 0)
	if err != nil || len(list) != 2 || !reflect.DeepEqual(list[0], got.Messages[0]) || !reflect.DeepEqual(list[1], got.Messages[1]) {
		t.Fatalf("list=%+v thread=%+v err=%v", list, got.Messages, err)
	}
}

func TestThreadFailsClosedOnAResultThatBreaksTheContract(t *testing.T) {
	m1, m2, m3 := threadMessage("m1", 3, "inbox"), threadMessage("m2", 2, "inbox"), threadMessage("m3", 1, "inbox")
	unnamed, other := m2, m2
	unnamed.ID, other.ThreadID = "", "t2"
	for name, response := range map[string][]model.Message{
		"no messages":     nil,
		"empty ID":        {m1, unnamed},
		"duplicate ID":    {m1, m1},
		"more than count": {m1, m2, m3},
		"other thread":    {m1, other},
		"newest first":    {m2, m1},
	} {
		t.Run(name, func(t *testing.T) {
			b := &threadBox{mailbox: &mailbox{messages: []model.Message{m1, m2, m3}}, response: metadata(response...)}
			s := &screener{verdict: model.Green}
			g, _, _ := setup(t, b, s)
			got, err := g.Thread(t.Context(), "t1", 2)
			if err != guard.GuardError("ValueError") || got.Messages != nil || s.calls != 0 {
				t.Fatalf("thread=%+v err=%v screenings=%d", got, err, s.calls)
			}
		})
	}
}

func TestThreadRefusesABadCountOrAnEmptyThreadBeforeTheMailbox(t *testing.T) {
	b := &threadBox{mailbox: &mailbox{}}
	g, _, _ := setup(t, b, &screener{verdict: model.Green})
	for _, tc := range []struct {
		thread string
		count  int
		want   guard.GuardError
	}{
		{"t1", 0, "count must be between 1 and 100"},
		{"t1", 101, "count must be between 1 and 100"},
		{"", 25, "give a thread ID"},
	} {
		if _, err := g.Thread(t.Context(), tc.thread, tc.count); err != tc.want {
			t.Errorf("%q %d: err=%v", tc.thread, tc.count, err)
		}
	}
	if b.calls != 0 {
		t.Fatalf("mailbox called %d times", b.calls)
	}
}
