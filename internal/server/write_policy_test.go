package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

// policyServer is a graphServer whose provider counts every request; GET requests return event e2 as an attendee event and any other ID as the
// organizer event e1. Other requests succeed.
func policyServer(t *testing.T, policy string) (*server.Server, string, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var reads, writes atomic.Int32
	s, home := graphServer(t, policy, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			reads.Add(1)
			if strings.HasSuffix(r.URL.Path, "/events/e2") {
				io.WriteString(w, strings.Replace(attendeeEvent, `"id":"e1"`, `"id":"e2"`, 1))
			} else {
				io.WriteString(w, organizerEvent)
			}
			return
		}
		writes.Add(1)
		io.WriteString(w, `{"id":"moved"}`)
	}))
	return s, home, &reads, &writes
}

// graphServer serves the work and other M365 accounts with the given policy tables and graph as the Graph API.
// Its Jev is a fake that does not flag actions.
func graphServer(t *testing.T, policy string, graph http.Handler) (*server.Server, string) {
	t.Helper()
	return graphJevServer(t, policy, graph, newNativeAPI())
}

// graphJevServer is a graphServer whose Jev is the fake Jev of jev.
func graphJevServer(t *testing.T, policy string, graph http.Handler, jev *nativeAPI) (*server.Server, string) {
	t.Helper()
	home := t.TempDir()
	text := "[accounts.work]\nprovider='m365'\nclient_id='synthetic'\ntenant_id='synthetic'\n[accounts.other]\nprovider='m365'\nclient_id='other'\ntenant_id='synthetic'\n" + policy
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(graph)
	t.Cleanup(api.Close)
	s, err := server.New(context.Background(), home, server.Options{Credentials: &meetingStore{scopes: "Mail.ReadWrite Mail.Send Calendars.ReadWrite"}, Screener: jev.screener(t), M365: m365.Options{BaseURL: api.URL, AuthorityURL: api.URL}, Now: func() time.Time { return time.Date(2026, 10, 7, 0, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, home
}

func accountRPC(t *testing.T, s *server.Server, op, account, params string) (int, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/call", strings.NewReader(`{"operation":"`+op+`","account":"`+account+`","params":`+params+`}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w.Code, strings.TrimSpace(w.Body.String())
}

// storedArgs reads a stored action through the cache API.
func storedArgs(t *testing.T, home, id string) cache.Pending {
	t.Helper()
	c, err := cache.Open(context.Background(), filepath.Join(home, "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	item, err := c.GetPending(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func sameJSON(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		t.Fatalf("invalid JSON: %s %s", a, b)
	}
	return reflect.DeepEqual(x, y)
}

func TestPolicyDenyIsAPublicRefusalBeforeProviderAccess(t *testing.T) {
	s, home, reads, writes := policyServer(t, "[policy]\nmail_draft='deny'\n")
	for _, tc := range []struct{ op, params, key string }{
		{"calendar.invite", validCalendarParams["calendar.invite"], "calendar_invite"},
		{"calendar.update", validCalendarParams["calendar.update"], "calendar_update"},
		{"calendar.cancel", validCalendarParams["calendar.cancel"], "calendar_cancel"},
		{"calendar.respond", validCalendarParams["calendar.respond"], "calendar_respond"},
		{"draft.new", `{"to":["a@example.test"],"subject":"CANARY","body":"CANARY"}`, "mail_draft"},
	} {
		status, body := accountRPC(t, s, tc.op, "work", tc.params)
		if status != 500 || body != `{"error":"refused by policy: `+tc.key+`"}` {
			t.Errorf("%s: %d %s", tc.op, status, body)
		}
	}
	if reads.Load() != 0 || writes.Load() != 0 {
		t.Fatalf("provider accessed: reads=%d writes=%d", reads.Load(), writes.Load())
	}
	data, err := os.ReadFile(filepath.Join(home, "audit.jsonl"))
	if err != nil || strings.Contains(string(data), "CANARY") || strings.Count(string(data), `"decision":"deny"`) != 5 {
		t.Fatalf("audit=%s %v", data, err)
	}
}

var approvalID = regexp.MustCompile(`^\{"status":"pending","approval_id":"([0-9a-f]{32})","url":"http://localhost:8788/a/[0-9a-f]{32}"\}$`)

// The url names the approval console page of the item: the default listen address, or [approvals].listen.
func TestPendingResultLinksToTheApprovalConsole(t *testing.T) {
	for _, tc := range []struct{ name, table, base string }{
		{"default", "", "http://localhost:8788/a/"},
		{"configured", "[approvals]\nlisten='100.64.0.1:9000'\n", "http://100.64.0.1:9000/a/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _ := policyServer(t, "[policy]\nmail_draft='approve'\n"+tc.table)
			status, body := accountRPC(t, s, "draft.new", "work", `{"to":["a@example.test"],"subject":"s","body":"b"}`)
			var got map[string]string
			if status != 200 || json.Unmarshal([]byte(body), &got) != nil || len(got) != 3 || got["status"] != "pending" || len(got["approval_id"]) != 32 || got["url"] != tc.base+got["approval_id"] {
				t.Fatalf("%d %s", status, body)
			}
		})
	}
}

// The stored args equal the request as JSON, so the strict API parser accepts them and gives the same action.
func TestPolicyApproveReturnsPendingAndStoresTheRequest(t *testing.T) {
	s, home, reads, writes := policyServer(t, "[policy]\nmail_draft='approve'\ncalendar_invite='approve'\ncalendar_update='approve'\ncalendar_cancel='approve'\ncalendar_respond='approve'\n[policy.sealed]\nmail_draft='approve'\n")
	for _, tc := range []struct{ op, params string }{
		{"draft.new", `{"to":["CANARY@example.test"],"subject":"CANARY subject","body":"CANARY body"}`},
		{"draft.update", `{"id":"d1","body":""}`},
		{"draft.reply", `{"id":"m1","body":"CANARY reply"}`},
		{"draft.forward", `{"id":"m1","to":["a@example.test","b@example.test"]}`},
		{"calendar.update", `{"id":"e1","subject":"CANARY replacement","location":"","start":"2026-10-09T12:00:00+01:00","duration":"30m0s","add":["new@example.test"],"remove":["remove@example.test"],"teams":true}`},
		{"calendar.update", `{"id":"e1","end":"2026-10-09T11:30:00+01:00"}`},
		{"calendar.invite", `{"to":["a@example.test"],"subject":"","body":"","start":"2026-10-09T09:00:00Z","end":"2026-10-09T10:00:00Z","location":"Room","teams":false}`},
		{"calendar.cancel", `{"id":"e1","comment":"CANARY comment"}`},
		{"calendar.respond", `{"id":"e2","response":"tentative","comment":"CANARY comment"}`},
	} {
		status, body := accountRPC(t, s, tc.op, "work", tc.params)
		match := approvalID.FindStringSubmatch(body)
		if status != 200 || match == nil {
			t.Fatalf("%s: %d %s", tc.op, status, body)
		}
		item := storedArgs(t, home, match[1])
		if item.Account != "work" || item.Op != tc.op || item.State != cache.StatePending || !sameJSON(t, string(item.Args), tc.params) {
			t.Fatalf("%s stored %+v args=%s", tc.op, item, item.Args)
		}
	}
	if writes.Load() != 0 {
		t.Fatalf("approve wrote to the provider %d times", writes.Load())
	}
	if reads.Load() != 5 {
		t.Fatalf("reads=%d; want one event check per update, cancel, and respond, and one read of the forwarded message for the Jev check", reads.Load())
	}
}

func TestTrashPolicyResultsKeepOneEntryPerID(t *testing.T) {
	s, home, reads, writes := policyServer(t, "[policy.sealed]\nmail_trash='allow'\n[accounts.other.policy]\nmail_trash='approve'\n")
	status, body := accountRPC(t, s, "mail.trash", "work", `{"ids":["m1"]}`)
	if status != 200 || body != `[{"id":"m1","ok":true,"error":null}]` || writes.Load() != 1 {
		t.Fatalf("work: %d %s writes=%d", status, body, writes.Load())
	}
	status, body = accountRPC(t, s, "mail.trash", "other", `{"ids":["m1","m2"]}`)
	var got []map[string]any
	if status != 200 || json.Unmarshal([]byte(body), &got) != nil || len(got) != 2 {
		t.Fatalf("other: %d %s", status, body)
	}
	for i, id := range []string{"m1", "m2"} {
		approval, _ := got[i]["approval_id"].(string)
		if got[i]["id"] != id || got[i]["ok"] != false || got[i]["error"] != "pending" || approval == "" || len(got[i]) != 4 {
			t.Fatalf("result %d=%v", i, got[i])
		}
		if item := storedArgs(t, home, approval); item.Account != "other" || item.Op != "mail.trash" || string(item.Args) != `{"ids":["`+id+`"]}` {
			t.Fatalf("stored %+v args=%s", item, item.Args)
		}
	}
	if writes.Load() != 1 || reads.Load() != 0 {
		t.Fatalf("provider accessed for pending items: reads=%d writes=%d", reads.Load(), writes.Load())
	}
}

func TestAccountPolicyOverrideAppliesToThatAccountOnly(t *testing.T) {
	s, _, _, writes := policyServer(t, "[policy]\ncalendar_cancel='allow'\n[accounts.other.policy]\ncalendar_cancel='deny'\n")
	if status, body := accountRPC(t, s, "calendar.cancel", "other", `{"id":"e1"}`); status != 500 || body != `{"error":"refused by policy: calendar_cancel"}` || writes.Load() != 0 {
		t.Fatalf("other: %d %s writes=%d", status, body, writes.Load())
	}
	if status, body := accountRPC(t, s, "calendar.cancel", "work", `{"id":"e1"}`); status != 200 || body != `{"id":"e1","ok":true}` || writes.Load() != 1 {
		t.Fatalf("work: %d %s writes=%d", status, body, writes.Load())
	}
}
