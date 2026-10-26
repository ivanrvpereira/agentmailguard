package server_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestMailSearchByRecipientMapsToAndNamesEveryTermInTheRefusal(t *testing.T) {
	home := fakeHome(t, "")
	if _, err := fakeDB(t, home).Exec(`INSERT INTO messages (account, id, sender_address, to_addresses, subject, received_at) VALUES
		('demo', 'to-ana', 'sender@example.test', '["ana@example.test"]', 'private-subject', '2026-10-06T08:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	s, _ := fakeServer(t, home)
	status, body := accountRPC(t, s, "mail.search", "demo", `{"to":"ana@example.test"}`)
	var page struct{ Messages []struct{ ID string } }
	if err := json.Unmarshal([]byte(body), &page); status != 200 || err != nil || !reflect.DeepEqual(page.Messages, []struct{ ID string }{{"to-ana"}}) {
		t.Fatalf("to search: %d %s", status, body)
	}
	for params, want := range map[string]string{
		`{"to":"","folder":"inbox"}`:                 `{"error":"search needs at least one of text, from, to, subject, after, before"}`,
		`{"to":"` + strings.Repeat("a", 4097) + `"}`: `{"error":"invalid request"}`,
	} {
		if status, body := accountRPC(t, s, "mail.search", "demo", params); status == 200 || body != want {
			t.Errorf("%.40s: %d %s", params, status, body)
		}
	}
}

func TestMailSearchTakesExactRFC3339BoundsAndRefusesDays(t *testing.T) {
	home := fakeHome(t, "")
	if _, err := fakeDB(t, home).Exec(`INSERT INTO messages (account, id, sender_address, subject, received_at) VALUES
		('demo', 'at-eight', 'sender@example.test', 'private-subject', '2026-10-06T08:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	s, _ := fakeServer(t, home)
	status, body := accountRPC(t, s, "mail.search", "demo", `{"after":"2026-10-06T09:00:00+01:00","before":"2026-10-06T08:00:01Z"}`)
	var page struct{ Messages []struct{ ID string } }
	if err := json.Unmarshal([]byte(body), &page); status != 200 || err != nil || !reflect.DeepEqual(page.Messages, []struct{ ID string }{{"at-eight"}}) {
		t.Fatalf("exact bounds: %d %s", status, body)
	}
	for _, params := range []string{`{"after":"2026-10-06"}`, `{"before":"2026-10-07"}`, `{"after":"2026-10-06 08:00"}`} {
		if status, body := accountRPC(t, s, "mail.search", "demo", params); status != 400 || body != `{"error":"invalid request"}` {
			t.Errorf("%s: %d %s", params, status, body)
		}
	}
}
