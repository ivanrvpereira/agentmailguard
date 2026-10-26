package server_test

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCalendarListRefusesTextOrganizerAndAttendeeFilters(t *testing.T) {
	d := fakeDaemon(t, fakeHome(t), "")
	window := `"start":"2026-10-09T00:00:00Z","end":"2026-10-10T00:00:00Z"`
	if status, body := accountRPC(t, d.s, "calendar.list", "demo", `{`+window+`}`); status != 200 {
		t.Fatalf("plain list: %d %s", status, body)
	}
	for _, field := range []string{"text", "organizer", "attendee"} {
		if status, body := accountRPC(t, d.s, "calendar.list", "demo", `{`+window+`,"`+field+`":"budget"}`); status != 400 || body != `{"error":"invalid request"}` {
			t.Errorf("%s: %d %s", field, status, body)
		}
	}
}

func TestCalendarSearchReturnsTheScreenedEventsThatTheProviderFinds(t *testing.T) {
	home := fakeHome(t)
	fakeSQL(t, home, `INSERT INTO events (account, id, subject, start_at, end_at, organizer_address) VALUES
		(?1, 'budget', 'Budget', '2026-10-09T09:00:00Z', '2026-10-09T10:00:00Z', 'ana@example.test'),
		(?1, 'lunch', 'Lunch', '2026-10-09T12:00:00Z', '2026-10-09T13:00:00Z', 'olga@example.test')`)
	d := fakeDaemon(t, home, "")
	status, body := accountRPC(t, d.s, "calendar.search", "demo", `{"text":"olga"}`)
	var found page
	if err := json.Unmarshal([]byte(body), &found); status != 200 || err != nil || len(found.Events) != 1 || found.Events[0].ID != "lunch" || found.Events[0].Verdict != "green" {
		t.Fatalf("search: %d %s", status, body)
	}
	for _, params := range []string{`{}`, `{"text":""}`, `{"text":7}`, `{"text":"` + strings.Repeat("a", 4097) + `"}`, `{"text":"olga","start":"2026-10-09T00:00:00Z"}`} {
		if status, body := accountRPC(t, d.s, "calendar.search", "demo", params); status != 400 || body != `{"error":"invalid request"}` {
			t.Errorf("%.40s: %d %s", params, status, body)
		}
	}
}
