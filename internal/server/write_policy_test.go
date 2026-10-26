package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
)

// policyDaemon is a graphDaemon with the write grant whose provider counts every request; GET requests return the
// green message m1 for any message, event e2 as an attendee event, and any other ID as the organizer event e1.
// Other requests succeed.
func policyDaemon(t *testing.T, policy string) (*daemon, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var reads, writes atomic.Int32
	message := greenMessage(t, "m1")
	d := graphDaemon(t, policy, writeGrant, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			reads.Add(1)
			if strings.HasPrefix(r.URL.Path, "/me/messages/") {
				io.WriteString(w, message)
			} else if strings.HasSuffix(r.URL.Path, "/events/e2") {
				io.WriteString(w, strings.Replace(attendeeEvent, `"id":"e1"`, `"id":"e2"`, 1))
			} else {
				io.WriteString(w, organizerEvent)
			}
			return
		}
		writes.Add(1)
		io.WriteString(w, `{"id":"moved"}`)
	}), newNativeAPI())
	return d, &reads, &writes
}

func TestPolicyDenyIsAPublicRefusalBeforeProviderAccess(t *testing.T) {
	d, reads, writes := policyDaemon(t, "[policy]\nmail_draft='deny'\n")
	for _, tc := range []struct{ op, params, key string }{
		{"calendar.invite", validCalendarParams["calendar.invite"], "calendar_invite"},
		{"calendar.update", validCalendarParams["calendar.update"], "calendar_update"},
		{"calendar.cancel", validCalendarParams["calendar.cancel"], "calendar_cancel"},
		{"calendar.respond", validCalendarParams["calendar.respond"], "calendar_respond"},
		{"draft.new", `{"to":["a@example.test"],"subject":"CANARY","body":"CANARY"}`, "mail_draft"},
	} {
		status, body := accountRPC(t, d.s, tc.op, "work", tc.params)
		if status != 500 || body != `{"error":"refused by policy: `+tc.key+`"}` {
			t.Errorf("%s: %d %s", tc.op, status, body)
		}
	}
	if reads.Load() != 0 || writes.Load() != 0 {
		t.Fatalf("provider accessed: reads=%d writes=%d", reads.Load(), writes.Load())
	}
	data, err := os.ReadFile(filepath.Join(d.home, "audit.jsonl"))
	if err != nil || strings.Contains(string(data), "CANARY") || strings.Count(string(data), `"decision":"deny"`) != 5 {
		t.Fatalf("audit=%s %v", data, err)
	}
}

var approvalID = regexp.MustCompile(`^\{"status":"pending","approval_id":"([0-9a-f]{32})","url":"http://localhost:8788/a/[0-9a-f]{32}"\}$`)

// The url names the approval console page of the item: the default listen address, or [console].listen.
func TestPendingResultLinksToTheApprovalConsole(t *testing.T) {
	for _, tc := range []struct{ name, table, base string }{
		{"default", "", "http://localhost:8788/a/"},
		{"configured", "[console]\nlisten='100.64.0.1:9000'\n", "http://100.64.0.1:9000/a/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _, _ := policyDaemon(t, "[policy]\nmail_draft='approve'\n"+tc.table)
			status, body := accountRPC(t, d.s, "draft.new", "work", `{"to":["a@example.test"],"subject":"s","body":"b"}`)
			var got map[string]string
			if status != 200 || json.Unmarshal([]byte(body), &got) != nil || len(got) != 3 || got["status"] != "pending" || len(got["approval_id"]) != 32 || got["url"] != tc.base+got["approval_id"] {
				t.Fatalf("%d %s", status, body)
			}
		})
	}
}

// A held action waits as pending and reaches the provider only for reads: the event checks, the verdict lookup of
// the reply source, and the reply recipients and the forwarded message of the Jev check. TestApprovedActionMakesTheSameWriteAsTheAllowedCall runs the stored requests.
func TestPolicyApproveHoldsTheActionWithoutAProviderWrite(t *testing.T) {
	d, reads, writes := policyDaemon(t, "[policy]\nmail_draft='approve'\ncalendar_invite='approve'\ncalendar_update='approve'\ncalendar_cancel='approve'\ncalendar_respond='approve'\n")
	for _, tc := range []struct{ op, params string }{
		{"draft.new", `{"to":["CANARY@example.test"],"subject":"CANARY subject","body":"CANARY body"}`},
		{"draft.update", `{"id":"d1","body":""}`},
		{"draft.reply", `{"id":"m1","body":"CANARY reply"}`},
		{"draft.forward", `{"id":"m1","to":["a@example.test","b@example.test"]}`},
		{"calendar.update", `{"id":"e1","subject":"CANARY replacement","location":"","start":"2026-10-09T12:00:00+01:00","duration":"30m0s","add":["new@example.test"],"remove":["remove@example.test"],"online":true}`},
		{"calendar.update", `{"id":"e1","end":"2026-10-09T11:30:00+01:00"}`},
		{"calendar.invite", `{"to":["a@example.test"],"subject":"","body":"","start":"2026-10-09T09:00:00Z","end":"2026-10-09T10:00:00Z","location":"Room","online":false}`},
		{"calendar.cancel", `{"id":"e1","comment":"CANARY comment"}`},
		{"calendar.respond", `{"id":"e2","response":"tentative","comment":"CANARY comment"}`},
	} {
		status, body := accountRPC(t, d.s, tc.op, "work", tc.params)
		match := approvalID.FindStringSubmatch(body)
		if status != 200 || match == nil {
			t.Fatalf("%s: %d %s", tc.op, status, body)
		}
		if status, body := approvalStatus(t, d.s, match[1]); status != 200 || body != `{"state":"pending"}` {
			t.Fatalf("%s status: %d %s", tc.op, status, body)
		}
	}
	if writes.Load() != 0 {
		t.Fatalf("approve wrote to the provider %d times", writes.Load())
	}
	if reads.Load() != 7 {
		t.Fatalf("reads=%d; want one event check per update, cancel, and respond, one screening of the reply source, and one read of the reply recipients and one of the forwarded message for the Jev check", reads.Load())
	}
}

func TestTrashPolicyResultsKeepOneEntryPerID(t *testing.T) {
	d, reads, writes := policyDaemon(t, "[policy.sealed]\nmail_trash='allow'\n[accounts.other.policy]\nmail_trash='approve'\n")
	status, body := accountRPC(t, d.s, "mail.trash", "work", `{"ids":["m1"]}`)
	if status != 200 || body != `[{"id":"m1","ok":true,"error":null}]` || writes.Load() != 1 {
		t.Fatalf("work: %d %s writes=%d", status, body, writes.Load())
	}
	status, body = accountRPC(t, d.s, "mail.trash", "other", `{"ids":["m1","m2"]}`)
	var got []map[string]any
	if status != 200 || json.Unmarshal([]byte(body), &got) != nil || len(got) != 2 {
		t.Fatalf("other: %d %s", status, body)
	}
	for i, id := range []string{"m1", "m2"} {
		approval, _ := got[i]["approval_id"].(string)
		if got[i]["id"] != id || got[i]["ok"] != false || got[i]["error"] != "pending" || approval == "" || len(got[i]) != 4 {
			t.Fatalf("result %d=%v", i, got[i])
		}
		if status, body := approvalStatus(t, d.s, approval); status != 200 || body != `{"state":"pending"}` {
			t.Fatalf("item %d: %d %s", i, status, body)
		}
	}
	if writes.Load() != 1 || reads.Load() != 0 {
		t.Fatalf("provider accessed for pending items: reads=%d writes=%d", reads.Load(), writes.Load())
	}
}

func TestAccountPolicyOverrideAppliesToThatAccountOnly(t *testing.T) {
	d, _, writes := policyDaemon(t, "[policy]\ncalendar_cancel='allow'\n[accounts.other.policy]\ncalendar_cancel='deny'\n")
	if status, body := accountRPC(t, d.s, "calendar.cancel", "other", `{"id":"e1"}`); status != 500 || body != `{"error":"refused by policy: calendar_cancel"}` || writes.Load() != 0 {
		t.Fatalf("other: %d %s writes=%d", status, body, writes.Load())
	}
	if status, body := accountRPC(t, d.s, "calendar.cancel", "work", `{"id":"e1"}`); status != 200 || body != `{"id":"e1","ok":true}` || writes.Load() != 1 {
		t.Fatalf("work: %d %s writes=%d", status, body, writes.Load())
	}
}
