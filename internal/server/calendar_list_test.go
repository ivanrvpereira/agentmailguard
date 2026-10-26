package server_test

import (
	"encoding/json"
	"strings"
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

func TestCalendarSearchReturnsTheScreenedEventsThatTheProviderFinds(t *testing.T) {
	home := fakeHome(t, "")
	if _, err := fakeDB(t, home).Exec(`INSERT INTO events (account, id, subject, start_at, end_at, organizer_address) VALUES
		('demo', 'budget', 'Budget', '2026-10-09T09:00:00Z', '2026-10-09T10:00:00Z', 'ana@example.test'),
		('demo', 'lunch', 'Lunch', '2026-10-09T12:00:00Z', '2026-10-09T13:00:00Z', 'olga@example.test')`); err != nil {
		t.Fatal(err)
	}
	s, _ := fakeServer(t, home)
	status, body := accountRPC(t, s, "calendar.search", "demo", `{"text":"olga"}`)
	var items []struct{ ID, Verdict string }
	if err := json.Unmarshal([]byte(body), &items); status != 200 || err != nil || len(items) != 1 || items[0].ID != "lunch" || items[0].Verdict != "green" {
		t.Fatalf("search: %d %s", status, body)
	}
	for _, params := range []string{`{}`, `{"text":""}`, `{"text":7}`, `{"text":"` + strings.Repeat("a", 4097) + `"}`, `{"text":"olga","start":"2026-10-09T00:00:00Z"}`} {
		if status, body := accountRPC(t, s, "calendar.search", "demo", params); status != 400 || body != `{"error":"invalid request"}` {
			t.Errorf("%.40s: %d %s", params, status, body)
		}
	}
}
