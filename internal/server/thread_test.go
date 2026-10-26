package server_test

import (
	"testing"
)

func TestMailThreadReturnsEveryMessageOfTheConversationNewestFirstWithTheirFolders(t *testing.T) {
	home := fakeHome(t, "")
	if _, err := fakeDB(t, home).Exec(`INSERT INTO messages (account, id, folder, sender_address, subject, received_at, thread_id) VALUES
		('demo', 'r', 'inbox', 'sender@example.test', 'private-subject-r', '2026-10-06T06:00:00Z', 'conv'),
		('demo', 's', 'trash', 'sender@example.test', 'private-subject-s', '2026-10-06T07:00:00Z', 'conv'),
		('demo', 'y', 'sent', 'sender@example.test', 'private-subject-y', '2026-10-06T08:00:00Z', 'conv')`); err != nil {
		t.Fatal(err)
	}
	s, _ := fakeServer(t, home)
	auth := `"auth":{"spf":null,"dkim":null,"dmarc":null,"compauth":null,"compauth_reason":null}`
	status, body := accountRPC(t, s, "mail.thread", "demo", `{"id":"conv"}`)
	if status != 200 || !sameJSON(t, body, `[`+
		`{"id":"y","thread_id":"conv","verdict":"yellow","received_at":"2026-10-06T08:00:00+00:00",`+auth+`,"is_read":false,"flag":"none","categories":[],"folders":["sent"],"sender":{"name":"","address":"sender@example.test"},"subject":"private-subject-y","to":[],"cc":[],"attachments":[],"reason":"injection"},`+
		`{"id":"s","thread_id":"conv","verdict":"sealed","received_at":"2026-10-06T07:00:00+00:00",`+auth+`,"is_read":false,"flag":"none","categories":[],"folders":["trash"],"sender":{"address":"sender@example.test"},"reason":"account_security"},`+
		`{"id":"r","thread_id":"conv","verdict":"red","received_at":"2026-10-06T06:00:00+00:00",`+auth+`,"is_read":false,"flag":"none","categories":[],"folders":["inbox"],"sender":{"address":"sender@example.test"},"reason":"injection"}]`) {
		t.Fatalf("thread: %d %s", status, body)
	}
}

func TestMailThreadRefusesAMissingIDOrAnyOtherParameter(t *testing.T) {
	s, _ := fakeServer(t, fakeHome(t, ""))
	for _, params := range []string{`{}`, `{"id":""}`, `{"id":"conv","count":25}`, `{"id":"conv","folder":"inbox"}`} {
		if status, body := accountRPC(t, s, "mail.thread", "demo", params); status != 400 || body != `{"error":"invalid request"}` {
			t.Errorf("%s: %d %s", params, status, body)
		}
	}
	if status, body := accountRPC(t, s, "mail.thread", "demo", `{"id":"conv"}`); status != 500 || body != `{"error":"operation failed"}` {
		t.Errorf("unknown thread: %d %s", status, body)
	}
}
