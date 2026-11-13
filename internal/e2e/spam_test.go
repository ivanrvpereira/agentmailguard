package e2e_test

import (
	"slices"
	"strings"
	"testing"
)

// Trash and spam stay readable and restorable; mail goes into them only through mail trash and mail spam.
func TestTrashAndSpamAreReadableAndOnlyTrashAndSpamMoveMailIntoThem(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	listed := func(folder string) []string {
		ids := []string{}
		for _, m := range decode[mailPage](t, s.ok("mail", "list", "--folder", folder)).Messages {
			ids = append(ids, m.ID)
		}
		return ids
	}
	s.ok("mail", "trash", "lunch")
	if got := listed("trash"); !slices.Contains(got, "lunch") {
		t.Fatalf("trash=%v", got)
	}
	if out := s.ok("mail", "get", "lunch"); !strings.Contains(out, "canary-body-lunch") {
		t.Fatalf("get from the trash: %s", out)
	}
	got := decode[[]result](t, s.ok("mail", "move", "projects", "lunch"))
	if len(got) != 1 || !got[0].OK || s.value("folder", "lunch") != "projects" {
		t.Fatalf("restore=%+v folder=%s", got, s.value("folder", "lunch"))
	}
	got = decode[[]result](t, s.ok("mail", "move", "Inbox", "lunch"))
	if len(got) != 1 || !got[0].OK || s.value("folder", "lunch") != "inbox" {
		t.Fatalf("move to Inbox=%+v folder=%s", got, s.value("folder", "lunch"))
	}
	s.refused("move refused: use mail.trash", "mail", "move", "trash", "invoice")
	s.refused("move refused: use mail.spam", "mail", "move", "spam", "invoice")
	if s.value("folder", "invoice") != "inbox" {
		t.Fatal("refused destination changed the message")
	}
	got = decode[[]result](t, s.ok("mail", "spam", "invoice"))
	if len(got) != 1 || got[0].ID != "invoice" || !got[0].OK || s.value("folder", "invoice") != "spam" {
		t.Fatalf("spam=%+v folder=%s", got, s.value("folder", "invoice"))
	}
	if got := listed("spam"); !slices.Contains(got, "invoice") {
		t.Fatalf("spam=%v", got)
	}
}

func TestSpamApprovalActsOnlyOnItsMessage(t *testing.T) {
	t.Parallel()
	s := start(t, "[policy]\nmail_spam='approve'\n")
	got := decode[[]result](t, s.ok("mail", "spam", "lunch", "invoice"))
	if len(got) != 2 {
		t.Fatalf("spam=%+v", got)
	}
	for _, r := range got {
		if r.OK || r.Error != "pending" || r.ApprovalID == "" || s.value("folder", r.ID) != "inbox" {
			t.Fatalf("pending=%+v", r)
		}
	}
	if got[0].ApprovalID == got[1].ApprovalID {
		t.Fatal("messages share an approval")
	}
	s.approve(got[0].ApprovalID)
	if s.state(got[0].ApprovalID) != "done" || s.value("folder", "lunch") != "spam" || s.value("folder", "invoice") != "inbox" || s.state(got[1].ApprovalID) != "pending" {
		t.Fatal("approval did not act only on its message")
	}
}
