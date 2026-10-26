package server_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// addresses returns n distinct addresses on a reserved domain, as a JSON array.
func addresses(n int) string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("p%d@example.test", i)
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// The server sets no limit of its own on message IDs, recipients, or attendees: the provider refuses what it does
// not accept. The fake provider accepts any number.
func TestListsHaveNoLimitOfOurOwn(t *testing.T) {
	home := fakeHome(t, "[policy]\nmail_send='allow'\nmail_draft='allow'\ncalendar_invite='allow'\ncalendar_update='allow'\n")
	if _, err := fakeDB(t, home).Exec(`INSERT INTO events (account, id, subject, start_at, end_at) VALUES ('demo', 'e1', 'Planning', '2026-10-09T09:00:00Z', '2026-10-09T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	s, _ := fakeServer(t, home)
	ids := make([]string, 51)
	for i := range ids {
		ids[i] = "g"
		if i > 0 {
			ids[i] = fmt.Sprintf("missing-%d", i)
		}
	}
	idList, _ := json.Marshal(ids)
	var results []struct{ ID string }
	if status, body := accountRPC(t, s, "mail.read", "demo", `{"ids":`+string(idList)+`}`); status != 200 || json.Unmarshal([]byte(body), &results) != nil || len(results) != 51 {
		t.Errorf("read 51 IDs: %d %.200s", status, body)
	}
	many := addresses(101)
	for _, c := range []struct{ op, params string }{
		{"mail.send", `{"to":` + many + `,"subject":"Hello","body":"hi"}`},
		{"draft.new", `{"to":` + many + `,"subject":"Hello","body":"hi"}`},
		{"mail.forward", `{"id":"g","to":` + many + `}`},
		{"draft.forward", `{"id":"g","to":` + many + `}`},
		{"calendar.invite", `{"to":` + many + `,"optional":` + strings.Replace(many, "@", "-optional@", -1) + `,"subject":"Planning","body":"","start":"2026-10-10T09:00:00Z","end":"2026-10-10T10:00:00Z"}`},
		{"calendar.update", `{"id":"e1","add":` + many + `}`},
	} {
		if status, body := accountRPC(t, s, c.op, "demo", c.params); status != 200 {
			t.Errorf("%s with 101 addresses: %d %s", c.op, status, body)
		}
	}
	// Microsoft Graph calendarView needs a start and an end but sets no maximum range.
	if status, body := accountRPC(t, s, "calendar.list", "demo", `{"start":"2026-01-01T00:00:00Z","end":"2027-01-01T00:00:00Z"}`); status != 200 || !strings.Contains(body, `"id":"e1"`) {
		t.Errorf("calendar list over a year: %d %.200s", status, body)
	}
	// Gmail messages.modify takes at most 100 labels to add and 100 to remove, and refuses more itself; Graph
	// documents no limit on message categories.
	names := make([]string, 101)
	for i := range names {
		names[i] = fmt.Sprintf("c%d", i)
		if _, err := fakeDB(t, home).Exec(`INSERT INTO categories (account, name) VALUES ('demo', ?)`, names[i]); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := json.Marshal(names)
	for _, field := range []string{"add", "remove"} {
		if status, body := accountRPC(t, s, "mail.categorize", "demo", `{"ids":["g"],"`+field+`":`+string(list)+`}`); status != 200 || !strings.Contains(body, `"ok":true`) {
			t.Errorf("categorize --%s with 101 categories: %d %.200s", field, status, body)
		}
	}
	if status, body := accountRPC(t, s, "mail.read", "demo", `{"ids":[]}`); status != 400 {
		t.Errorf("read without IDs: %d %s", status, body)
	}
}
