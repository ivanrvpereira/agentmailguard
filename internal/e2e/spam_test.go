package e2e_test

import "testing"

func TestSpamMovesToClosedSpamFolder(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	got := decode[[]result](t, s.ok("mail", "spam", "lunch"))
	if len(got) != 1 || got[0].ID != "lunch" || !got[0].OK || s.value("folder", "lunch") != "spam" {
		t.Fatalf("spam=%+v folder=%s", got, s.value("folder", "lunch"))
	}
	s.refused("refused: message is in spam", "mail", "get", "lunch")
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
