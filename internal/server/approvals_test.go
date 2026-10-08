package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

// approvalGraph is a fake Graph API. A GET returns the current event. Every other request is a write:
// it is recorded and answered with {"id":"w1"}, or with failStatus when that is set.
type approvalGraph struct {
	mu         sync.Mutex
	writes     []meetingRequest
	event      atomic.Pointer[string]
	failStatus atomic.Int32
	// block, when set, receives each write and the write then waits for the request to end.
	block chan struct{}
}

func (g *approvalGraph) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if r.Method == "GET" {
		io.WriteString(w, *g.event.Load())
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	g.mu.Lock()
	g.writes = append(g.writes, meetingRequest{r.Method, r.URL.Path, body})
	g.mu.Unlock()
	if g.block != nil {
		g.block <- struct{}{}
		<-r.Context().Done()
		return
	}
	if status := g.failStatus.Load(); status != 0 {
		w.WriteHeader(int(status))
		io.WriteString(w, `{"error":{"code":"CANARY"}}`)
		return
	}
	io.WriteString(w, `{"id":"w1"}`)
}

func (g *approvalGraph) recorded() []meetingRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]meetingRequest{}, g.writes...)
}

// approvalServer is a graphServer whose Graph API returns the organizer event e1 to every GET.
func approvalServer(t *testing.T, policy string) (*server.Server, string, *approvalGraph) {
	t.Helper()
	graph := &approvalGraph{}
	event := organizerEvent
	graph.event.Store(&event)
	s, home := graphServer(t, policy, graph)
	return s, home, graph
}

var approvalIDs = regexp.MustCompile(`"approval_id":"([0-9a-f]{32})"`)

// pend makes a call of the work account that the policy holds and returns the approval ID of each held action.
func pend(t *testing.T, s *server.Server, op, params string) []string {
	t.Helper()
	status, body := accountRPC(t, s, op, "work", params)
	matches := approvalIDs.FindAllStringSubmatch(body, -1)
	if status != 200 || matches == nil {
		t.Fatalf("%s: %d %s", op, status, body)
	}
	ids := []string{}
	for _, m := range matches {
		ids = append(ids, m[1])
	}
	return ids
}

// auditOf returns the audit log of home as text and as records.
func auditOf(t *testing.T, home string) (string, []map[string]any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		out = append(out, record)
	}
	return string(data), out
}

// trashApprove holds every trash. The sealed decision is approve too: the fake API serves no messages, so
// each message counts as sealed, and an approved run must skip the sealed decision to run.
const trashApprove = "[policy]\nmail_trash='approve'\n[policy.sealed]\nmail_trash='approve'\n"
const allowAll = "[policy]\nmail_draft='allow'\ncalendar_invite='allow'\ncalendar_update='allow'\ncalendar_cancel='allow'\ncalendar_respond='allow'\n[policy.sealed]\nmail_draft='allow'\n"
const approveAll = "[policy]\nmail_draft='approve'\ncalendar_invite='approve'\ncalendar_update='approve'\ncalendar_cancel='approve'\ncalendar_respond='approve'\n[policy.sealed]\nmail_draft='approve'\n"

func TestApproveTrashMovesOnlyThatMessageAndStoresTheResult(t *testing.T) {
	s, home, graph := approvalServer(t, trashApprove)
	ids := pend(t, s, "mail.trash", `{"ids":["m1","m2"]}`)
	if err := s.Approve(context.Background(), ids[0]); err != nil {
		t.Fatal(err)
	}
	want := meetingRequest{"POST", "/me/messages/m1/move", map[string]any{"destinationId": "deleteditems"}}
	if got := graph.recorded(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("provider writes=%+v", got)
	}
	item := storedArgs(t, home, ids[0])
	if item.State != cache.StateDone || item.Result == nil || !sameJSON(t, *item.Result, `[{"id":"m1","ok":true,"error":null}]`) {
		t.Fatalf("approved item %+v result=%v", item, item.Result)
	}
	if other := storedArgs(t, home, ids[1]); other.State != cache.StatePending {
		t.Fatalf("other item state=%s", other.State)
	}
}

// greenMessage is the Graph JSON of message id with a body, which the fake Jev of nativeAPI screens green.
func greenMessage(t *testing.T, id string) string {
	t.Helper()
	data, err := json.Marshal(messageJSON(id, true, nil))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The same call under an allow policy gives the expected provider write and result.
func TestApprovedActionMakesTheSameWriteAsTheAllowedCall(t *testing.T) {
	for _, tc := range []struct{ name, op, params, event string }{
		{"draft.new", "draft.new", `{"to":["CANARY@example.test"],"subject":"CANARY subject","body":"CANARY body"}`, organizerEvent},
		{"draft.reply", "draft.reply", `{"id":"m1","body":"CANARY reply"}`, organizerEvent},
		// The allowed forward runs only when Jev can read the forwarded text, so the Graph API serves a green message.
		{"draft.forward", "draft.forward", `{"id":"m1","to":["a@example.test","b@example.test"]}`, greenMessage(t, "m1")},
		{"calendar.invite", "calendar.invite", `{"to":["a@example.test"],"subject":"CANARY","body":"CANARY body","start":"2026-10-09T09:00:00Z","end":"2026-10-09T10:00:00Z","location":"Room","teams":false}`, organizerEvent},
		{"calendar.update duration and attendees", "calendar.update", `{"id":"e1","subject":"CANARY replacement","location":"","start":"2026-10-09T12:00:00+01:00","duration":"30m","add":["new@example.test"],"remove":["remove@example.test"],"teams":true}`, organizerEvent},
		{"calendar.update start keeps duration", "calendar.update", `{"id":"e1","start":"2026-10-09T12:00:00+01:00"}`, organizerEvent},
		{"calendar.update end", "calendar.update", `{"id":"e1","end":"2026-10-09T11:30:00+01:00"}`, organizerEvent},
		{"calendar.cancel", "calendar.cancel", `{"id":"e1","comment":"CANARY comment"}`, organizerEvent},
		{"calendar.respond", "calendar.respond", `{"id":"e1","response":"tentative","comment":"CANARY comment"}`, attendeeEvent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allowed, _, allowedGraph := approvalServer(t, allowAll)
			allowedGraph.event.Store(&tc.event)
			status, want := accountRPC(t, allowed, tc.op, "work", tc.params)
			if status != 200 || len(allowedGraph.recorded()) != 1 {
				t.Fatalf("allowed call: %d %s writes=%+v", status, want, allowedGraph.recorded())
			}
			s, home, graph := approvalServer(t, approveAll)
			graph.event.Store(&tc.event)
			id := pend(t, s, tc.op, tc.params)[0]
			if err := s.Approve(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if got := graph.recorded(); len(got) != 1 || !reflect.DeepEqual(got[0], allowedGraph.recorded()[0]) {
				t.Fatalf("approved writes=%+v; allowed=%+v", got, allowedGraph.recorded())
			}
			if item := storedArgs(t, home, id); item.State != cache.StateDone || item.Result == nil || !sameJSON(t, *item.Result, want) {
				t.Fatalf("item %+v result=%v; want %s", item, item.Result, want)
			}
		})
	}
}

// An approved run adds the approved and done records and its normal operation record, and no policy
// record or new pending item. Records hold IDs and codes only.
func TestApprovedRunAuditsTheApprovalAndNoNewDecision(t *testing.T) {
	for _, tc := range []struct{ policy, op, params, operation string }{
		{approveAll, "draft.new", `{"to":["CANARY@example.test"],"subject":"CANARY subject","body":"CANARY body"}`, "draft_new"},
		{approveAll, "calendar.update", `{"id":"e1","subject":"CANARY","add":["new@example.test"]}`, "calendar_update"},
		{trashApprove, "mail.trash", `{"ids":["m1"]}`, "trash"},
	} {
		t.Run(tc.op, func(t *testing.T) {
			s, home, _ := approvalServer(t, tc.policy)
			id := pend(t, s, tc.op, tc.params)[0]
			_, before := auditOf(t, home)
			if err := s.Approve(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			text, after := auditOf(t, home)
			added := after[len(before):]
			if len(added) != 3 || strings.Contains(text, "CANARY") || strings.Contains(text, "example.test") {
				t.Fatalf("records added=%v audit=%s", added, text)
			}
			want := []map[string]any{
				{"event": "approval", "account": "work", "operation": tc.op, "state": "approved", "approval_id": id},
				{"account": "work", "operation": tc.operation},
				{"event": "approval", "account": "work", "operation": tc.op, "state": "done", "approval_id": id},
			}
			for i, w := range want {
				for k, v := range w {
					if added[i][k] != v {
						t.Fatalf("record %d=%v; want %s=%v", i, added[i], k, v)
					}
				}
			}
			if _, ok := added[1]["event"]; ok {
				t.Fatalf("record 1=%v is not an operation record", added[1])
			}
		})
	}
}

func TestApproveAndDenyRefuseAnUnknownID(t *testing.T) {
	s, _, _ := approvalServer(t, trashApprove)
	for name, call := range map[string]func(context.Context, string) error{"approve": s.Approve, "deny": s.Deny} {
		if err := call(context.Background(), "0123456789abcdef0123456789abcdef"); err != server.ErrApprovalNotFound {
			t.Fatalf("%s=%v", name, err)
		}
	}
}

func TestDenyStopsTheActionForGood(t *testing.T) {
	s, home, graph := approvalServer(t, approveAll)
	id := pend(t, s, "draft.new", `{"to":["CANARY@example.test"],"subject":"CANARY","body":"CANARY"}`)[0]
	if err := s.Deny(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(context.Background(), id); err != server.ErrAlreadyHandled {
		t.Fatalf("approve after deny=%v", err)
	}
	if err := s.Deny(context.Background(), id); err != server.ErrAlreadyHandled {
		t.Fatalf("second deny=%v", err)
	}
	if got := len(graph.recorded()); got != 0 {
		t.Fatalf("provider writes=%d", got)
	}
	if item := storedArgs(t, home, id); item.State != cache.StateDenied || item.Result != nil {
		t.Fatalf("item %+v", item)
	}
	text, records := auditOf(t, home)
	last := records[len(records)-1]
	if strings.Contains(text, "CANARY") || len(records) != 3 || last["event"] != "approval" || last["state"] != "denied" || last["operation"] != "draft.new" || last["account"] != "work" || last["approval_id"] != id {
		t.Fatalf("audit=%s", text)
	}
}

func TestConcurrentApprovalsRunTheActionOnce(t *testing.T) {
	s, home, graph := approvalServer(t, trashApprove)
	id := pend(t, s, "mail.trash", `{"ids":["m1"]}`)[0]
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { errs <- s.Approve(context.Background(), id) })
	}
	wg.Wait()
	close(errs)
	won := 0
	for err := range errs {
		if err == nil {
			won++
		} else if err != server.ErrAlreadyHandled {
			t.Fatal(err)
		}
	}
	if won != 1 || len(graph.recorded()) != 1 || storedArgs(t, home, id).State != cache.StateDone {
		t.Fatalf("won=%d writes=%d", won, len(graph.recorded()))
	}
}

// A run that fails stores a safe code, is audited as failed, and never runs again.
func TestFailedApprovalStoresASafeCodeAndDoesNotRetry(t *testing.T) {
	cancelled := strings.Replace(organizerEvent, `"isOrganizer":true`, `"isOrganizer":true,"isCancelled":true`, 1)
	for _, tc := range []struct {
		name, policy, op, params string
		breakIt                  func(*approvalGraph)
		code                     string
		writes                   int
	}{
		{"trash provider error", trashApprove, "mail.trash", `{"ids":["m1"]}`, func(g *approvalGraph) { g.failStatus.Store(403) }, "Graph error 403", 1},
		{"draft provider error", approveAll, "draft.new", `{"to":["CANARY@example.test"],"subject":"CANARY","body":"CANARY"}`, func(g *approvalGraph) { g.failStatus.Store(403) }, "MailboxError", 1},
		{"event now cancelled", approveAll, "calendar.update", `{"id":"e1","duration":"30m"}`, func(g *approvalGraph) { g.event.Store(&cancelled) }, "GuardError", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, home, graph := approvalServer(t, tc.policy)
			id := pend(t, s, tc.op, tc.params)[0]
			tc.breakIt(graph)
			if err := s.Approve(context.Background(), id); err == nil || err.Error() != tc.code {
				t.Fatalf("approve=%v; want %s", err, tc.code)
			}
			if err := s.Approve(context.Background(), id); err != server.ErrAlreadyHandled {
				t.Fatalf("second approve=%v", err)
			}
			if got := len(graph.recorded()); got != tc.writes {
				t.Fatalf("provider writes=%d; want %d", got, tc.writes)
			}
			if item := storedArgs(t, home, id); item.State != cache.StateFailed || item.Result == nil || *item.Result != tc.code {
				t.Fatalf("item %+v result=%v", item, item.Result)
			}
			text, records := auditOf(t, home)
			last := records[len(records)-1]
			if strings.Contains(text, "CANARY") || last["event"] != "approval" || last["state"] != "failed" || last["approval_id"] != id || strings.Count(text, `"state":"failed"`) != 1 {
				t.Fatalf("audit=%s", text)
			}
		})
	}
}

// blockedApproval starts an approval of a trash item whose provider write blocks until its request ends,
// and returns once the write has reached the provider.
func blockedApproval(t *testing.T, ctx context.Context) (*server.Server, string, string, chan error) {
	t.Helper()
	s, home, graph := approvalServer(t, trashApprove)
	id := pend(t, s, "mail.trash", `{"ids":["m1"]}`)[0]
	graph.block = make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- s.Approve(ctx, id) }()
	select {
	case <-graph.block:
	case <-time.After(5 * time.Second):
		t.Fatal("write did not reach the provider")
	}
	return s, home, id, done
}

// The ended request is a failed provider request, which the trash result reports with its fixed code.
func TestApprovalFinishesWhenTheCallerGoesAway(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	_, home, id, done := blockedApproval(t, ctx)
	cancel()
	if err := <-done; err == nil || err.Error() != "Graph error RequestFailed" {
		t.Fatalf("approve=%v", err)
	}
	if item := storedArgs(t, home, id); item.State != cache.StateFailed || item.Result == nil || *item.Result != "Graph error RequestFailed" {
		t.Fatalf("item %+v result=%v", item, item.Result)
	}
	if _, records := auditOf(t, home); records[len(records)-1]["state"] != "failed" {
		t.Fatalf("last record=%v", records[len(records)-1])
	}
}

func TestCloseWaitsForARunningApproval(t *testing.T) {
	s, home, id, done := blockedApproval(t, context.Background())
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	// Approve returns after its work ends, so only the stored state shows that Close waited.
	if item := storedArgs(t, home, id); item.State != cache.StateFailed {
		t.Fatalf("state=%s", item.State)
	}
	if err := <-done; err == nil || err.Error() != "Graph error RequestFailed" {
		t.Fatalf("approve=%v", err)
	}
}

func TestApproveAndDenyAfterCloseAreRefused(t *testing.T) {
	s, home, graph := approvalServer(t, trashApprove)
	ids := pend(t, s, "mail.trash", `{"ids":["m1","m2"]}`)
	s.Close()
	if err := s.Approve(context.Background(), ids[0]); err != server.ErrUnavailable {
		t.Fatalf("approve=%v", err)
	}
	if err := s.Deny(context.Background(), ids[1]); err != server.ErrUnavailable {
		t.Fatalf("deny=%v", err)
	}
	for _, id := range ids {
		if item := storedArgs(t, home, id); item.State != cache.StatePending {
			t.Fatalf("state=%s", item.State)
		}
	}
	if len(graph.recorded()) != 0 {
		t.Fatal("provider write after close")
	}
}

// Without its approved record the action does not run, and it can never run later.
func TestApprovalDoesNotRunWithoutItsAuditRecord(t *testing.T) {
	s, home, graph := approvalServer(t, trashApprove)
	id := pend(t, s, "mail.trash", `{"ids":["m1"]}`)[0]
	if err := os.Chmod(filepath.Join(home, "audit.jsonl"), 0400); err != nil {
		t.Fatal(err)
	}
	if err := s.Approve(context.Background(), id); err == nil {
		t.Fatal("approve succeeded")
	}
	if len(graph.recorded()) != 0 {
		t.Fatal("provider write without an audit record")
	}
	if item := storedArgs(t, home, id); item.State != cache.StateFailed || item.Result == nil || *item.Result != "AuditError" {
		t.Fatalf("item %+v result=%v", item, item.Result)
	}
}

// approvalStatus asks for the state of an item. The call names an account that does not exist: the
// operation is not account-scoped and ignores it.
func approvalStatus(t *testing.T, s *server.Server, id string) (int, string) {
	t.Helper()
	return accountRPC(t, s, "approvals.status", "nobody", `{"id":"`+id+`"}`)
}

// The status shows the state of an item and, once done, its stored result. It never shows the stored
// arguments, the account, or a failure code.
func TestApprovalStatusFollowsTheItemWithoutItsArguments(t *testing.T) {
	s, _, graph := approvalServer(t, "[policy]\nmail_draft='approve'\nmail_trash='approve'\n[policy.sealed]\nmail_trash='approve'\n")
	draft := `{"to":["CANARY@example.test"],"subject":"CANARY subject","body":"CANARY body"}`
	approved, denied := pend(t, s, "draft.new", draft)[0], pend(t, s, "draft.new", draft)[0]
	failed := pend(t, s, "mail.trash", `{"ids":["m2"]}`)[0]
	for _, id := range []string{approved, denied, failed} {
		if status, body := approvalStatus(t, s, id); status != 200 || body != `{"state":"pending"}` {
			t.Fatalf("pending %s: %d %s", id, status, body)
		}
	}
	if err := s.Approve(context.Background(), approved); err != nil {
		t.Fatal(err)
	}
	if err := s.Deny(context.Background(), denied); err != nil {
		t.Fatal(err)
	}
	graph.failStatus.Store(403)
	if err := s.Approve(context.Background(), failed); err == nil {
		t.Fatal("approve of a failing write succeeded")
	}
	for _, tc := range []struct{ id, want string }{
		{approved, `{"state":"done","result":{"id":"w1"}}`},
		{denied, `{"state":"denied"}`},
		{failed, `{"state":"failed"}`},
	} {
		if status, body := approvalStatus(t, s, tc.id); status != 200 || body != tc.want {
			t.Errorf("%s: %d %s; want %s", tc.id, status, body, tc.want)
		}
	}
}

func TestApprovalStatusRefusesUnknownAndMalformedIDs(t *testing.T) {
	s, _, _ := approvalServer(t, trashApprove)
	if status, body := approvalStatus(t, s, "0123456789abcdef0123456789abcdef"); status != 404 || body != `{"error":"approval not found"}` {
		t.Fatalf("unknown: %d %s", status, body)
	}
	id := pend(t, s, "mail.trash", `{"ids":["m1"]}`)[0]
	for _, params := range []string{
		`{"id":"` + strings.ToUpper(id) + `"}`,
		`{"id":"` + id[1:] + `"}`,
		`{"id":"` + id + `0"}`,
		`{"id":"` + id[2:] + `zz"}`,
		`{"id":1}`,
		`{}`,
		`{"id":"` + id + `","account":"work"}`,
	} {
		if status, body := meetingRPC(t, s, "approvals.status", params); status != 400 || body != `{"error":"invalid request"}` {
			t.Errorf("%s: %d %s", params, status, body)
		}
	}
}
