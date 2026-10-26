package server_test

import (
	"context"
	"encoding/json"
	"errors"
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

	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

// approvalGraph is a fake Graph API. A GET returns the current event. Every other request is a write:
// it is recorded and answered with {"id":"w1"}, a reply draft also with Graph's quoted body, or with failStatus
// when that is set.
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
	if strings.HasSuffix(r.URL.Path, "/createReply") || strings.HasSuffix(r.URL.Path, "/createReplyAll") {
		io.WriteString(w, `{"id":"w1","body":{"contentType":"html","content":"<html><body>quoted</body></html>"}}`)
		return
	}
	io.WriteString(w, `{"id":"w1"}`)
}

func (g *approvalGraph) recorded() []meetingRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]meetingRequest{}, g.writes...)
}

// approvalDaemon is a graphDaemon with the write grant whose Graph API returns the organizer event e1 to every GET.
func approvalDaemon(t *testing.T, policy string) (*daemon, *approvalGraph) {
	t.Helper()
	graph := &approvalGraph{}
	event := organizerEvent
	graph.event.Store(&event)
	return graphDaemon(t, policy, writeGrant, graph, newNativeAPI()), graph
}

var approvalIDs = regexp.MustCompile(`"approval_id":"([0-9a-f]{32})"`)

// pend makes a call of the default account that the policy holds and returns the approval ID of each held action.
func pend(t *testing.T, s *server.Server, op, params string) []string {
	t.Helper()
	status, body := accountRPC(t, s, op, "", params)
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

// trashApprove holds every trash. The sealed decision is approve too: the fake API serves no messages, so
// each message counts as sealed, and an approved run must skip the sealed decision to run.
const trashApprove = "[policy]\nmail_trash='approve'\n[policy.sealed]\nmail_trash='approve'\n"
const allowAll = "[policy]\nmail_draft='allow'\ncalendar_invite='allow'\ncalendar_update='allow'\ncalendar_cancel='allow'\ncalendar_respond='allow'\n"
const approveAll = "[policy]\nmail_draft='approve'\ncalendar_invite='approve'\ncalendar_update='approve'\ncalendar_cancel='approve'\ncalendar_respond='approve'\n"

// approvalStatus asks for the state of an item. The call names an account that does not exist: the
// operation is not account-scoped and ignores it.
func approvalStatus(t *testing.T, s *server.Server, id string) (int, string) {
	t.Helper()
	return accountRPC(t, s, "approvals.status", "nobody", `{"id":"`+id+`"}`)
}

// assertState checks the approvals.status answer of an item: its state, and the stored result of a done item.
func assertState(t *testing.T, s *server.Server, id, want string) {
	t.Helper()
	if status, body := approvalStatus(t, s, id); status != 200 || !sameJSON(t, body, want) {
		t.Fatalf("item %s: %d %s; want %s", id, status, body, want)
	}
}

func TestApproveTrashMovesOnlyThatMessageAndStoresTheResult(t *testing.T) {
	d, graph := approvalDaemon(t, trashApprove)
	ids := pend(t, d.s, "mail.trash", `{"ids":["m1","m2"]}`)
	if err := d.s.Approve(context.Background(), ids[0]); err != nil {
		t.Fatal(err)
	}
	want := meetingRequest{"POST", "/me/messages/m1/move", map[string]any{"destinationId": "deleteditems"}}
	if got := graph.recorded(); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
		t.Fatalf("provider writes=%+v", got)
	}
	assertState(t, d.s, ids[0], `{"state":"done","result":[{"id":"m1","ok":true,"error":null}]}`)
	assertState(t, d.s, ids[1], `{"state":"pending"}`)
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

// The same call under an allow policy gives the expected provider writes and result: one write, and for a reply
// draft the createReply and its PATCH. A stored draft result keeps only the draft IDs (stored).
func TestApprovedActionMakesTheSameWriteAsTheAllowedCall(t *testing.T) {
	for _, tc := range []struct {
		name, op, params, event, stored string
		writes                          int
	}{
		{"draft.new", "draft.new", `{"to":["CANARY@example.test"],"subject":"CANARY subject","body":"CANARY body"}`, organizerEvent, `{"id":"w1"}`, 1},
		// A reply or forward runs only when its source is not sealed, and the allowed forward only when Jev can read
		// the forwarded text, so the Graph API serves a green message.
		{"draft.reply", "draft.reply", `{"id":"m1","body":"CANARY reply","all":true,"cc":["c@example.test"],"bcc":["d@example.test"],"html":true}`, greenMessage(t, "m1"), `{"id":"w1"}`, 2},
		{"draft.forward", "draft.forward", `{"id":"m1","to":["a@example.test","b@example.test"]}`, greenMessage(t, "m1"), "", 1},
		{"calendar.invite", "calendar.invite", `{"to":["a@example.test"],"subject":"CANARY","body":"CANARY body","start":"2026-10-09T09:00:00Z","end":"2026-10-09T10:00:00Z","location":"Room","online":false}`, organizerEvent, "", 1},
		{"calendar.update duration and attendees", "calendar.update", `{"id":"e1","subject":"CANARY replacement","location":"","start":"2026-10-09T12:00:00+01:00","duration":"30m","add":["new@example.test"],"remove":["remove@example.test"],"online":true}`, organizerEvent, "", 1},
		{"calendar.update start keeps duration", "calendar.update", `{"id":"e1","start":"2026-10-09T12:00:00+01:00"}`, organizerEvent, "", 1},
		{"calendar.update end", "calendar.update", `{"id":"e1","end":"2026-10-09T11:30:00+01:00"}`, organizerEvent, "", 1},
		{"calendar.update html body", "calendar.update", `{"id":"e1","body":"<b>CANARY</b> body","html":true}`, organizerEvent, "", 1},
		{"calendar.cancel", "calendar.cancel", `{"id":"e1","comment":"CANARY comment"}`, organizerEvent, "", 1},
		{"calendar.respond", "calendar.respond", `{"id":"e1","response":"tentative","comment":"CANARY comment"}`, attendeeEvent, "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			allowed, allowedGraph := approvalDaemon(t, allowAll)
			allowedGraph.event.Store(&tc.event)
			status, want := accountRPC(t, allowed.s, tc.op, "work", tc.params)
			if status != 200 || len(allowedGraph.recorded()) != tc.writes {
				t.Fatalf("allowed call: %d %s writes=%+v", status, want, allowedGraph.recorded())
			}
			d, graph := approvalDaemon(t, approveAll)
			graph.event.Store(&tc.event)
			id := pend(t, d.s, tc.op, tc.params)[0]
			if err := d.s.Approve(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if got := graph.recorded(); len(got) != tc.writes || !reflect.DeepEqual(got, allowedGraph.recorded()) {
				t.Fatalf("approved writes=%+v; allowed=%+v", got, allowedGraph.recorded())
			}
			if tc.stored != "" {
				want = tc.stored
			}
			assertState(t, d.s, id, `{"state":"done","result":`+want+`}`)
		})
	}
}

// An approved run adds the approved and done records and its normal operation record, and no policy
// record or new pending item. Records hold IDs, codes, and the account key only.
func TestApprovedRunAuditsTheApprovalAndNoNewDecision(t *testing.T) {
	for _, tc := range []struct{ policy, op, params, operation string }{
		{approveAll, "draft.new", `{"to":["CANARY@example.test"],"subject":"CANARY subject","body":"CANARY body"}`, "draft_new"},
		{approveAll, "calendar.update", `{"id":"e1","subject":"CANARY","add":["new@example.test"]}`, "calendar_update"},
		{trashApprove, "mail.trash", `{"ids":["m1"]}`, "trash"},
	} {
		t.Run(tc.op, func(t *testing.T) {
			d, _ := approvalDaemon(t, tc.policy)
			id := pend(t, d.s, tc.op, tc.params)[0]
			_, before := auditOf(t, d.home)
			if err := d.s.Approve(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			text, after := auditOf(t, d.home)
			added := after[len(before):]
			if len(added) != 3 || strings.Contains(text, "CANARY") || strings.Contains(text, "example.test") {
				t.Fatalf("records added=%v audit=%s", added, text)
			}
			want := []map[string]any{
				{"event": "approval", "account": workEmail, "operation": tc.op, "state": "approved", "approval_id": id},
				{"account": workEmail, "operation": tc.operation},
				{"event": "approval", "account": workEmail, "operation": tc.op, "state": "done", "approval_id": id},
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
	d, _ := approvalDaemon(t, trashApprove)
	for name, call := range map[string]func(context.Context, string) error{"approve": d.s.Approve, "deny": d.s.Deny} {
		if err := call(context.Background(), "0123456789abcdef0123456789abcdef"); err != server.ErrApprovalNotFound {
			t.Fatalf("%s=%v", name, err)
		}
	}
}

func TestDenyStopsTheActionForGood(t *testing.T) {
	d, graph := approvalDaemon(t, approveAll)
	id := pend(t, d.s, "draft.new", `{"to":["CANARY@example.test"],"subject":"CANARY","body":"CANARY"}`)[0]
	if err := d.s.Deny(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := d.s.Approve(context.Background(), id); err != server.ErrAlreadyHandled {
		t.Fatalf("approve after deny=%v", err)
	}
	if err := d.s.Deny(context.Background(), id); err != server.ErrAlreadyHandled {
		t.Fatalf("second deny=%v", err)
	}
	if got := len(graph.recorded()); got != 0 {
		t.Fatalf("provider writes=%d", got)
	}
	assertState(t, d.s, id, `{"state":"denied"}`)
	text, records := auditOf(t, d.home)
	last := records[len(records)-1]
	if strings.Contains(text, "CANARY") || len(records) != 3 || last["event"] != "approval" || last["state"] != "denied" || last["operation"] != "draft.new" || last["account"] != workEmail || last["approval_id"] != id {
		t.Fatalf("audit=%s", text)
	}
}

func TestConcurrentApprovalsRunTheActionOnce(t *testing.T) {
	d, graph := approvalDaemon(t, trashApprove)
	id := pend(t, d.s, "mail.trash", `{"ids":["m1"]}`)[0]
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { errs <- d.s.Approve(context.Background(), id) })
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
	if won != 1 || len(graph.recorded()) != 1 {
		t.Fatalf("won=%d writes=%d", won, len(graph.recorded()))
	}
	assertState(t, d.s, id, `{"state":"done","result":[{"id":"m1","ok":true,"error":null}]}`)
}

// A run that fails returns a safe code, is stored and audited as failed, and never runs again.
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
			d, graph := approvalDaemon(t, tc.policy)
			id := pend(t, d.s, tc.op, tc.params)[0]
			tc.breakIt(graph)
			if err := d.s.Approve(context.Background(), id); err == nil || err.Error() != tc.code {
				t.Fatalf("approve=%v; want %s", err, tc.code)
			}
			if err := d.s.Approve(context.Background(), id); err != server.ErrAlreadyHandled {
				t.Fatalf("second approve=%v", err)
			}
			if got := len(graph.recorded()); got != tc.writes {
				t.Fatalf("provider writes=%d; want %d", got, tc.writes)
			}
			assertState(t, d.s, id, `{"state":"failed"}`)
			text, records := auditOf(t, d.home)
			last := records[len(records)-1]
			if strings.Contains(text, "CANARY") || last["event"] != "approval" || last["state"] != "failed" || last["approval_id"] != id || strings.Count(text, `"state":"failed"`) != 1 {
				t.Fatalf("audit=%s", text)
			}
		})
	}
}

// An action waits under the account key. When a reload removes its account, approving it runs nothing and fails with
// a fixed code; the action never runs on another account.
func TestApprovalOfARemovedAccountFailsWithoutAWrite(t *testing.T) {
	d, graph := approvalDaemon(t, trashApprove)
	id := pend(t, d.s, "mail.trash", `{"ids":["m1"]}`)[0]
	d.writeConfig(t, "default_account = 'other'\n"+otherTable+trashApprove)
	d.reload(t)
	if err := d.s.Approve(context.Background(), id); err == nil || err.Error() != "UnknownAccountError" {
		t.Fatalf("approve=%v", err)
	}
	if len(graph.recorded()) != 0 {
		t.Fatalf("provider writes=%v", graph.recorded())
	}
	assertState(t, d.s, id, `{"state":"failed"}`)
}

// blockedApproval starts an approval of a trash item whose provider write blocks until its request ends,
// and returns once the write has reached the provider.
func blockedApproval(t *testing.T, ctx context.Context) (*daemon, string, chan error) {
	t.Helper()
	d, graph := approvalDaemon(t, trashApprove)
	id := pend(t, d.s, "mail.trash", `{"ids":["m1"]}`)[0]
	graph.block = make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- d.s.Approve(ctx, id) }()
	select {
	case <-graph.block:
	case <-time.After(5 * time.Second):
		t.Fatal("write did not reach the provider")
	}
	return d, id, done
}

// The ended request is a failed provider request, which the trash result reports with its fixed code.
func TestApprovalFinishesWhenTheCallerGoesAway(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	d, id, done := blockedApproval(t, ctx)
	cancel()
	if err := <-done; err == nil || err.Error() != "Graph error RequestFailed" {
		t.Fatalf("approve=%v", err)
	}
	assertState(t, d.s, id, `{"state":"failed"}`)
	if _, records := auditOf(t, d.home); records[len(records)-1]["state"] != "failed" {
		t.Fatalf("last record=%v", records[len(records)-1])
	}
}

// Approve returns after its work ends, so only the audit record of the failed run shows that Close waited: the
// closed daemon answers no status call.
func TestCloseWaitsForARunningApproval(t *testing.T) {
	d, id, done := blockedApproval(t, context.Background())
	if err := d.s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, records := auditOf(t, d.home); records[len(records)-1]["state"] != "failed" || records[len(records)-1]["approval_id"] != id {
		t.Fatalf("last record=%v", records[len(records)-1])
	}
	if err := <-done; err == nil || err.Error() != "Graph error RequestFailed" {
		t.Fatalf("approve=%v", err)
	}
}

func TestApproveAndDenyAfterCloseAreRefused(t *testing.T) {
	d, graph := approvalDaemon(t, trashApprove)
	ids := pend(t, d.s, "mail.trash", `{"ids":["m1","m2"]}`)
	d.s.Close()
	if err := d.s.Approve(context.Background(), ids[0]); err != server.ErrUnavailable {
		t.Fatalf("approve=%v", err)
	}
	if err := d.s.Deny(context.Background(), ids[1]); err != server.ErrUnavailable {
		t.Fatalf("deny=%v", err)
	}
	if len(graph.recorded()) != 0 {
		t.Fatal("provider write after close")
	}
	// A restart on the same home finds both items still waiting.
	restarted := startDaemon(t, daemonSetup{home: d.home, config: workConfig + trashApprove + fakeScreen, record: record(nil)})
	for _, id := range ids {
		assertState(t, restarted.s, id, `{"state":"pending"}`)
	}
}

// Without its approved record the action does not run, and it can never run later.
func TestApprovalDoesNotRunWithoutItsAuditRecord(t *testing.T) {
	d, graph := approvalDaemon(t, trashApprove)
	id := pend(t, d.s, "mail.trash", `{"ids":["m1"]}`)[0]
	// A folder in place of the log makes every write fail, also for root, which ignores file permissions.
	log := filepath.Join(d.home, "audit.jsonl")
	if err := errors.Join(os.Remove(log), os.Mkdir(log, 0700)); err != nil {
		t.Fatal(err)
	}
	if err := d.s.Approve(context.Background(), id); err == nil || err.Error() != "AuditError" {
		t.Fatalf("approve=%v", err)
	}
	if len(graph.recorded()) != 0 {
		t.Fatal("provider write without an audit record")
	}
	assertState(t, d.s, id, `{"state":"failed"}`)
}

// The status shows the state of an item and, once done, its stored result. It never shows the stored
// arguments, the account, or a failure code.
func TestApprovalStatusFollowsTheItemWithoutItsArguments(t *testing.T) {
	d, graph := approvalDaemon(t, "[policy]\nmail_draft='approve'\nmail_trash='approve'\n[policy.sealed]\nmail_trash='approve'\n")
	draft := `{"to":["CANARY@example.test"],"subject":"CANARY subject","body":"CANARY body"}`
	approved, denied := pend(t, d.s, "draft.new", draft)[0], pend(t, d.s, "draft.new", draft)[0]
	failed := pend(t, d.s, "mail.trash", `{"ids":["m2"]}`)[0]
	for _, id := range []string{approved, denied, failed} {
		if status, body := approvalStatus(t, d.s, id); status != 200 || body != `{"state":"pending"}` {
			t.Fatalf("pending %s: %d %s", id, status, body)
		}
	}
	if err := d.s.Approve(context.Background(), approved); err != nil {
		t.Fatal(err)
	}
	if err := d.s.Deny(context.Background(), denied); err != nil {
		t.Fatal(err)
	}
	graph.failStatus.Store(403)
	if err := d.s.Approve(context.Background(), failed); err == nil {
		t.Fatal("approve of a failing write succeeded")
	}
	for _, tc := range []struct{ id, want string }{
		{approved, `{"state":"done","result":{"id":"w1"}}`},
		{denied, `{"state":"denied"}`},
		{failed, `{"state":"failed"}`},
	} {
		if status, body := approvalStatus(t, d.s, tc.id); status != 200 || body != tc.want {
			t.Errorf("%s: %d %s; want %s", tc.id, status, body, tc.want)
		}
	}
}

func TestApprovalStatusRefusesUnknownAndMalformedIDs(t *testing.T) {
	d, _ := approvalDaemon(t, trashApprove)
	if status, body := approvalStatus(t, d.s, "0123456789abcdef0123456789abcdef"); status != 404 || body != `{"error":"approval not found"}` {
		t.Fatalf("unknown: %d %s", status, body)
	}
	id := pend(t, d.s, "mail.trash", `{"ids":["m1"]}`)[0]
	for _, params := range []string{
		`{"id":"` + strings.ToUpper(id) + `"}`,
		`{"id":"` + id[1:] + `"}`,
		`{"id":"` + id + `0"}`,
		`{"id":"` + id[2:] + `zz"}`,
		`{"id":1}`,
		`{}`,
		`{"id":"` + id + `","account":"work"}`,
	} {
		if status, body := accountRPC(t, d.s, "approvals.status", "", params); status != 400 || body != `{"error":"invalid request"}` {
			t.Errorf("%s: %d %s", params, status, body)
		}
	}
}
