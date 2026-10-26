package server_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

// page is the result of mail.list, mail.search, and calendar.search.
type page struct {
	Messages []struct{ ID, Verdict string } `json:"messages"`
	Events   []struct{ ID, Verdict string } `json:"events"`
	Next     *string                        `json:"next_page"`
}

// rpcPage calls op on the demo account and returns the page and the raw body.
func rpcPage(t *testing.T, s *server.Server, op, params string) (page, string) {
	t.Helper()
	status, body := accountRPC(t, s, op, "demo", params)
	var p page
	if status != 200 || json.Unmarshal([]byte(body), &p) != nil {
		t.Fatalf("%s %s: %d %.300s", op, params, status, body)
	}
	return p, body
}

// The fake provider pages like Graph: 10 items by default, and next_page names the next page until the last one.
func TestMailListAndSearchReturnOnePageWithTheNextPageMarker(t *testing.T) {
	home := fakeHome(t)
	db := fakeDB(t, home)
	for i := range 11 {
		if _, err := db.Exec(`INSERT INTO messages (account, id, sender_address, subject, body, received_at, spf, dkim, dmarc) VALUES (?, ?, 'sender@example.test', 'invoice', 'body', ?, 'pass', 'pass', 'pass')`, demoEmail, fmt.Sprintf("m%02d", i), fmt.Sprintf("2026-10-05T%02d:00:00Z", i)); err != nil {
			t.Fatal(err)
		}
	}
	s := fakeDaemon(t, home, "").s
	for _, c := range []struct{ op, params string }{{"mail.list", `{`}, {"mail.search", `{"text":"sender@example.test",`}} {
		first, _ := rpcPage(t, s, c.op, c.params+`"folder":"inbox"}`)
		if len(first.Messages) != 10 || first.Next == nil || *first.Next != "10" {
			t.Fatalf("%s first page: %+v", c.op, first)
		}
		last, body := rpcPage(t, s, c.op, c.params+`"folder":"inbox","page":"10"}`)
		if len(last.Messages) != 2 || last.Next != nil || strings.Contains(body, "next_page") {
			t.Fatalf("%s last page: %s", c.op, body)
		}
		counted, _ := rpcPage(t, s, c.op, c.params+`"count":3}`)
		if len(counted.Messages) != 3 || counted.Next == nil || *counted.Next != "3" {
			t.Fatalf("%s count 3: %+v", c.op, counted)
		}
	}
}

func TestCalendarSearchReturnsOnePageWithTheNextPageMarker(t *testing.T) {
	home := fakeHome(t)
	db := fakeDB(t, home)
	for i := range 12 {
		if _, err := db.Exec(`INSERT INTO events (account, id, subject, start_at, end_at) VALUES (?, ?, 'Planning', ?, ?)`, demoEmail, fmt.Sprintf("e%02d", i), fmt.Sprintf("2026-10-09T%02d:00:00Z", i), fmt.Sprintf("2026-10-09T%02d:30:00Z", i)); err != nil {
			t.Fatal(err)
		}
	}
	s := fakeDaemon(t, home, "").s
	first, _ := rpcPage(t, s, "calendar.search", `{"text":"planning"}`)
	if len(first.Events) != 10 || first.Next == nil || *first.Next != "10" {
		t.Fatalf("first page: %+v", first)
	}
	if last, body := rpcPage(t, s, "calendar.search", `{"text":"planning","page":"10"}`); len(last.Events) != 2 || strings.Contains(body, "next_page") {
		t.Fatalf("last page: %s", body)
	}
}

// A count goes to the provider as given: the fake refuses more than 1,000, as Graph does, and that refusal is a
// provider error. A negative count, skip, and a page that is not a bounded marker are invalid requests.
func TestListAndSearchSendTheCountAsGivenAndRefuseInvalidPages(t *testing.T) {
	s := fakeDaemon(t, fakeHome(t), "").s
	for _, c := range []struct{ op, params string }{{"mail.list", `{"count":1001}`}, {"mail.search", `{"text":"g","count":1001}`}} {
		if status, body := accountRPC(t, s, c.op, "demo", c.params); status != 500 || body != `{"error":"operation failed"}` {
			t.Errorf("%s count 1001: %d %s", c.op, status, body)
		}
	}
	for _, c := range []struct{ op, params string }{
		{"mail.list", `{"count":-1}`},
		{"mail.list", `{"skip":1}`},
		{"mail.list", `{"page":10}`},
		{"mail.list", `{"page":"` + strings.Repeat("1", 16<<10+1) + `"}`},
		{"mail.list", `{"page":"1 0"}`},
		{"mail.search", `{"text":"g","count":-1}`},
		{"mail.search", `{"text":"g","page":"\u00e9"}`},
		{"calendar.search", `{"text":"g","page":"` + strings.Repeat("1", 16<<10+1) + `"}`},
		{"calendar.search", `{"text":"g","count":1}`},
	} {
		if status, body := accountRPC(t, s, c.op, "demo", c.params); status != 400 || body != `{"error":"invalid request"}` {
			t.Errorf("%s %.60s: %d %s", c.op, c.params, status, body)
		}
	}
}
