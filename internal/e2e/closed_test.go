package e2e_test

import (
	"reflect"
	"testing"
)

func TestClosedFoldersAreUnreadableAndCannotBeMovedIntoOrOutOf(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	// Cache a green view before the message is trashed.
	s.ok("mail", "get", "lunch")
	s.ok("mail", "trash", "lunch")
	s.refused("refused: folder is trash", "mail", "list", "--folder", "trash")
	s.refused("refused: message is in trash", "mail", "get", "lunch")
	got := decode[[]result](t, s.ok("mail", "move", "projects", "lunch"))
	if len(got) != 1 || got[0].OK || got[0].Error != "refused: message is in trash" || s.value("folder", "lunch") != "trash" {
		t.Fatalf("move out=%+v folder=%s", got, s.value("folder", "lunch"))
	}
	s.refused("move refused: use mail.trash", "mail", "move", "trash", "invoice")
	s.refused("move refused: use mail.spam", "mail", "move", "spam", "invoice")
	if s.value("folder", "invoice") != "inbox" {
		t.Fatal("refused destination changed the message")
	}
	if got := decode[[]map[string]any](t, s.ok("mail", "thread", "lunch")); !reflect.DeepEqual(got, []map[string]any{}) {
		t.Fatalf("all-closed thread=%v", got)
	}
}
