package server_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCalendarListMapsEachFilterAndRefusesLongTerms(t *testing.T) {
	home := fakeHome(t, "")
	for _, statement := range []string{`INSERT INTO events (account, id, subject, start_at, end_at, organizer_address) VALUES
		('demo', 'budget', 'Budget', '2026-10-09T09:00:00Z', '2026-10-09T10:00:00Z', 'ana@example.test'),
		('demo', 'lunch', 'Lunch', '2026-10-09T12:00:00Z', '2026-10-09T13:00:00Z', 'olga@example.test')`,
		`INSERT INTO attendees (account, event_id, address) VALUES ('demo', 'budget', 'bob@example.test'), ('demo', 'lunch', 'carla@example.test')`} {
		if _, err := fakeDB(t, home).Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := fakeServer(t, home)
	window := `"start":"2026-10-09T00:00:00Z","end":"2026-10-10T00:00:00Z"`
	for filter, want := range map[string][]string{
		``:                             {"budget", "lunch"},
		`,"text":"budget"`:             {"budget"},
		`,"organizer":"olga"`:          {"lunch"},
		`,"attendee":"carla"`:          {"lunch"},
		`,"attendee":""`:               {"budget", "lunch"},
		`,"text":"x","attendee":"bob"`: {},
	} {
		status, body := accountRPC(t, s, "calendar.list", "demo", `{`+window+filter+`}`)
		var items []struct{ ID string }
		if err := json.Unmarshal([]byte(body), &items); status != 200 || err != nil {
			t.Fatalf("%s: %d %s", filter, status, body)
		}
		ids := []string{}
		for _, item := range items {
			ids = append(ids, item.ID)
		}
		if !reflect.DeepEqual(ids, want) {
			t.Errorf("%s: ids=%v want %v", filter, ids, want)
		}
	}
	for _, field := range []string{"text", "organizer", "attendee"} {
		for _, value := range []string{`"` + strings.Repeat("a", 4097) + `"`, `7`, `null`} {
			if status, body := accountRPC(t, s, "calendar.list", "demo", `{`+window+`,"`+field+`":`+value+`}`); status != 400 || body != `{"error":"invalid request"}` {
				t.Errorf("%s=%.20s: %d %s", field, value, status, body)
			}
		}
	}
}
