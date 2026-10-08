package server_test

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
)

// sendParams are valid params of the three send operations, in the shape of the matching draft operations.
var sendParams = []struct{ op, params, path string }{
	{"mail.send", `{"to":["CANARY@example.test"],"subject":"CANARY subject","body":"CANARY body"}`, "/me/sendMail"},
	{"mail.reply", `{"id":"m1","body":"CANARY reply"}`, "/me/messages/m1/reply"},
	{"mail.forward", `{"id":"m1","to":["a@example.test","b@example.test"]}`, "/me/messages/m1/forward"},
}

func TestMailSendDefaultPolicyRefusesBeforeProviderAccess(t *testing.T) {
	s, home, reads, writes := policyServer(t, "")
	for _, tc := range sendParams {
		if status, body := accountRPC(t, s, tc.op, "work", tc.params); status != 500 || body != `{"error":"refused by policy: mail_send"}` {
			t.Errorf("%s: %d %s", tc.op, status, body)
		}
	}
	if reads.Load() != 0 || writes.Load() != 0 {
		t.Fatalf("provider accessed: reads=%d writes=%d", reads.Load(), writes.Load())
	}
	text, records := auditOf(t, home)
	if strings.Contains(text, "CANARY") || strings.Contains(text, "example.test") || len(records) != 3 {
		t.Fatalf("audit=%s", text)
	}
}

// Gmail has no extra grant for sending: the policy decides before any credential access.
func TestGmailMailSendFollowsThePolicyWithoutAGrantCheck(t *testing.T) {
	s, creds, home := calendarServer(t, "User.Read", func(w http.ResponseWriter, r *http.Request) { t.Error("Graph request") })
	useGmail(t, home)
	for _, tc := range sendParams {
		if status, body := meetingRPC(t, s, tc.op, tc.params); status != 500 || body != `{"error":"refused by policy: mail_send"}` {
			t.Errorf("%s: %d %s", tc.op, status, body)
		}
	}
	if creds.loads.Load() != 0 {
		t.Fatal("refused send accessed credentials")
	}
}

// The grant check comes before the policy, as for calendar writes: without Mail.Send nothing is decided,
// read, or written, also when the policy allows the send.
func TestM365MailSendWithoutTheSendGrantIsMissingScopes(t *testing.T) {
	for _, policy := range []string{"", "[policy]\nmail_send='allow'\n[policy.sealed]\nmail_send='allow'\n"} {
		var requests atomic.Int32
		s, _, home := calendarServer(t, "User.Read Mail.ReadWrite Calendars.Read Calendars.ReadWrite Mail.Send.Shared role:Mail.Send", func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(202) })
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte("[accounts.work]\nprovider='m365'\nclient_id='synthetic'\ntenant_id='synthetic'\n"+policy), 0600); err != nil {
			t.Fatal(err)
		}
		for _, tc := range sendParams {
			if status, body := meetingRPC(t, s, tc.op, tc.params); status != 500 || body != `{"error":"missing_scopes"}` {
				t.Errorf("%q %s: %d %s", policy, tc.op, status, body)
			}
		}
		if requests.Load() != 0 {
			t.Fatalf("provider requests=%d", requests.Load())
		}
		if _, err := os.Stat(filepath.Join(home, "audit.jsonl")); !os.IsNotExist(err) {
			t.Fatal("refusal wrote audit")
		}
	}
}

const sendAllow = "[policy]\nmail_send='allow'\n"

func TestAllowedMailSendPostsOnceAndReturnsOK(t *testing.T) {
	for _, tc := range sendParams {
		t.Run(tc.op, func(t *testing.T) {
			graph := &approvalGraph{}
			// The forward's Jev check reads the forwarded message, which the fake Jev screens green.
			event := greenMessage(t, "m1")
			graph.event.Store(&event)
			s, home := graphServer(t, sendAllow, graph)
			status, body := accountRPC(t, s, tc.op, "work", tc.params)
			if status != 200 || body != `{"ok":true}` {
				t.Fatalf("%d %s", status, body)
			}
			if got := graph.recorded(); len(got) != 1 || got[0].Method != "POST" || got[0].Path != tc.path {
				t.Fatalf("provider writes=%+v", got)
			}
			assertAudited(t, home, strings.Replace(tc.op, ".", "_", 1))
		})
	}
}

func TestInvalidMailSendParamsAreRefusedBeforeAccountAccess(t *testing.T) {
	s, _, reads, writes := policyServer(t, sendAllow)
	for op, params := range map[string][]string{
		"mail.send":    {`{}`, `{"to":[],"subject":"s","body":"b"}`, `{"to":["a@example.test"],"body":"b"}`, `{"to":["a@example.test"],"subject":"s"}`, `{"to":["a@example.test"],"subject":"s","body":"b","cc":["x"]}`},
		"mail.reply":   {`{}`, `{"id":"m1"}`, `{"id":"","body":"b"}`, `{"id":"m1","body":"b","to":["x"]}`},
		"mail.forward": {`{}`, `{"id":"m1"}`, `{"id":"m1","to":[]}`, `{"id":"m1","to":["a"],"body":"b"}`},
	} {
		for _, p := range params {
			if status, body := accountRPC(t, s, op, "work", p); status != 400 || body != `{"error":"invalid request"}` {
				t.Errorf("%s %s: %d %s", op, p, status, body)
			}
		}
	}
	if reads.Load() != 0 || writes.Load() != 0 {
		t.Fatalf("provider accessed: reads=%d writes=%d", reads.Load(), writes.Load())
	}
}

func TestApprovedMailSendSendsOnceAndStoresOK(t *testing.T) {
	for _, tc := range sendParams {
		t.Run(tc.op, func(t *testing.T) {
			s, home, graph := approvalServer(t, "[policy]\nmail_send='approve'\n")
			event := greenMessage(t, "m1")
			graph.event.Store(&event)
			id := pend(t, s, tc.op, tc.params)[0]
			if item := storedArgs(t, home, id); item.Op != tc.op || !sameJSON(t, string(item.Args), tc.params) || len(graph.recorded()) != 0 {
				t.Fatalf("stored %+v args=%s writes=%d", item, item.Args, len(graph.recorded()))
			}
			if err := s.Approve(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if err := s.Approve(context.Background(), id); err == nil {
				t.Fatal("second approve ran")
			}
			if got := graph.recorded(); len(got) != 1 || got[0].Path != tc.path {
				t.Fatalf("provider writes=%+v", got)
			}
			if item := storedArgs(t, home, id); item.State != cache.StateDone || item.Result == nil || *item.Result != `{"ok":true}` {
				t.Fatalf("item %+v result=%v", item, item.Result)
			}
			text, _ := auditOf(t, home)
			if strings.Contains(text, "CANARY") || strings.Contains(text, "example.test") {
				t.Fatalf("audit=%s", text)
			}
		})
	}
}

// A reply or forward from a sealed source is refused by the default sealed decision. The fake Graph API
// serves no message, and a failed verdict lookup counts as sealed.
func TestMailSendFromASealedSourceIsRefusedByDefault(t *testing.T) {
	var writes atomic.Int32
	s, _ := graphServer(t, sendAllow, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writes.Add(1)
		}
		w.WriteHeader(404)
		io.WriteString(w, `{}`)
	}))
	for _, tc := range sendParams[1:] {
		if status, body := accountRPC(t, s, tc.op, "work", tc.params); status != 500 || body != `{"error":"refused by policy: mail_send"}` {
			t.Errorf("%s: %d %s", tc.op, status, body)
		}
	}
	if writes.Load() != 0 {
		t.Fatalf("writes=%d", writes.Load())
	}
}

func TestConsoleShowsTheSourceOfAReplyOrForwardSend(t *testing.T) {
	f := consoleServer(t, "[policy]\nmail_send='approve'\n[policy.sealed]\nmail_send='approve'\n", true)
	for _, tc := range []struct{ op, params string }{
		{"mail.reply", `{"id":"g","body":"reply text"}`},
		{"mail.forward", `{"id":"g","to":["a@example.test"]}`},
	} {
		id := pend(t, f.s, tc.op, tc.params)[0]
		w := f.get(t, "/a/"+id, nil)
		page := w.Body.String()
		for _, want := range []string{tc.op, "private-subject-g", "sender@example.test", "green"} {
			if w.Code != 200 || !strings.Contains(page, want) {
				t.Errorf("%s page misses %q", tc.op, want)
			}
		}
		if strings.Contains(page, "private-body-g") {
			t.Errorf("%s page shows the message body", tc.op)
		}
	}
	if len(f.api.writes) != 0 {
		t.Fatalf("provider writes=%v", f.api.writes)
	}
}
