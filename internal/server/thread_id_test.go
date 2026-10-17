package server_test

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

// threadIDs maps the id of each message in a mail.list or mail.search result, or of one mail.get result, to its
// verdict and thread_id. A missing thread_id is "absent".
func threadIDs(t *testing.T, body string) map[string][2]string {
	t.Helper()
	var items []map[string]any
	if json.Unmarshal([]byte(body), &items) != nil {
		var one map[string]any
		if err := json.Unmarshal([]byte(body), &one); err != nil {
			t.Fatalf("body %s: %v", body, err)
		}
		items = []map[string]any{one}
	}
	out := map[string][2]string{}
	for _, item := range items {
		thread, ok := item["thread_id"].(string)
		if !ok {
			thread = "absent"
		}
		out[item["id"].(string)] = [2]string{item["verdict"].(string), thread}
	}
	return out
}

func TestMailReadsShowTheThreadIDForEveryVerdict(t *testing.T) {
	home := fakeHome(t, "")
	if _, err := fakeDB(t, home).Exec(`INSERT INTO messages (account, id, sender_address, subject, received_at, thread_id) VALUES
		('demo', 'y', 'sender@example.test', 'private-subject-y', '2026-10-06T08:00:00Z', 'thread-1'),
		('demo', 's', 'sender@example.test', 'private-subject-s', '2026-10-06T07:00:00Z', 'thread-1'),
		('demo', 'r', 'sender@example.test', 'private-subject-r', '2026-10-06T06:00:00Z', 'thread-2'),
		('demo', 'down', 'sender@example.test', 'private-subject-down', '2026-10-06T05:00:00Z', 'thread-2')`); err != nil {
		t.Fatal(err)
	}
	s, _ := fakeServer(t, home)
	want := map[string][2]string{"g": {"green", "g"}, "y": {"yellow", "thread-1"}, "s": {"sealed", "thread-1"}, "r": {"red", "thread-2"}, "down": {"error", "thread-2"}}
	for _, call := range []struct{ op, params string }{{"mail.list", `{}`}, {"mail.search", `{"text":"private"}`}} {
		status, body := accountRPC(t, s, call.op, "demo", call.params)
		if got := threadIDs(t, body); status != 200 || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %d %v", call.op, status, got)
		}
	}
	for id, w := range want {
		status, body := accountRPC(t, s, "mail.get", "demo", `{"id":"`+id+`"}`)
		if got := threadIDs(t, body); status != 200 || !reflect.DeepEqual(got, map[string][2]string{id: w}) {
			t.Errorf("mail.get %s: %d %v", id, status, got)
		}
	}
}

func TestMailReadsOmitTheThreadIDThatTheProviderDoesNotGive(t *testing.T) {
	s, _ := graphServer(t, "", http.HandlerFunc(newNativeAPI().graph))
	status, body := accountRPC(t, s, "mail.list", "work", `{}`)
	want := map[string][2]string{"g": {"green", "absent"}, "y": {"yellow", "absent"}, "s": {"sealed", "absent"}, "r": {"red", "absent"}}
	if got := threadIDs(t, body); status != 200 || !reflect.DeepEqual(got, want) {
		t.Fatalf("mail.list: %d %v", status, got)
	}
}
