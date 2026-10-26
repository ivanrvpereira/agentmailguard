package server_test

import (
	"testing"
)

func TestCalendarListRefusesTextOrganizerAndAttendeeFilters(t *testing.T) {
	s, _ := fakeServer(t, fakeHome(t, ""))
	window := `"start":"2026-10-09T00:00:00Z","end":"2026-10-10T00:00:00Z"`
	if status, body := accountRPC(t, s, "calendar.list", "demo", `{`+window+`}`); status != 200 {
		t.Fatalf("plain list: %d %s", status, body)
	}
	for _, field := range []string{"text", "organizer", "attendee"} {
		if status, body := accountRPC(t, s, "calendar.list", "demo", `{`+window+`,"`+field+`":"budget"}`); status != 400 || body != `{"error":"invalid request"}` {
			t.Errorf("%s: %d %s", field, status, body)
		}
	}
}
