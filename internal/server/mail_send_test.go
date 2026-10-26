package server_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// sendParams are valid params of the three send operations, in the shape of the matching draft operations.
var sendParams = []struct{ op, params, path string }{
	{"mail.send", `{"to":["CANARY@example.test"],"subject":"CANARY subject","body":"CANARY body"}`, "/me/sendMail"},
	{"mail.reply", `{"id":"m1","body":"CANARY reply"}`, "/me/messages/m1/reply"},
	{"mail.forward", `{"id":"m1","to":["a@example.test","b@example.test"]}`, "/me/messages/m1/forward"},
}

func TestMailSendDefaultPolicyRefusesBeforeProviderAccess(t *testing.T) {
	d, reads, writes := policyDaemon(t, "")
	for _, tc := range sendParams {
		if status, body := accountRPC(t, d.s, tc.op, "work", tc.params); status != 500 || body != `{"error":"refused by policy: mail_send"}` {
			t.Errorf("%s: %d %s", tc.op, status, body)
		}
	}
	if reads.Load() != 0 || writes.Load() != 0 {
		t.Fatalf("provider accessed: reads=%d writes=%d", reads.Load(), writes.Load())
	}
	text, records := auditOf(t, d.home)
	if strings.Contains(text, "CANARY") || strings.Contains(text, "example.test") || len(records) != 3 {
		t.Fatalf("audit=%s", text)
	}
}

// Gmail has no extra grant for sending: the policy decides before any provider access.
func TestGmailMailSendFollowsThePolicyWithoutAGrantCheck(t *testing.T) {
	d, google := gmailDaemon(t, gmailReadGrant, "")
	before := google.requests.Load()
	for _, tc := range sendParams {
		if status, body := accountRPC(t, d.s, tc.op, "", tc.params); status != 500 || body != `{"error":"refused by policy: mail_send"}` {
			t.Errorf("%s: %d %s", tc.op, status, body)
		}
	}
	if n := google.requests.Load() - before; n != 0 {
		t.Fatalf("refused send made %d Google requests", n)
	}
}

// The grant check comes before the policy, as for calendar writes: without Mail.Send nothing is decided,
// read, or written, also when the policy allows the send.
func TestM365MailSendWithoutTheSendGrantIsMissingScopes(t *testing.T) {
	for _, policy := range []string{"", "[policy]\nmail_send='allow'\n"} {
		var requests atomic.Int32
		d := graphDaemon(t, policy, "User.Read Mail.ReadWrite Calendars.Read Calendars.ReadWrite Mail.Send.Shared role:Mail.Send", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(202) }), newNativeAPI())
		for _, tc := range sendParams {
			if status, body := accountRPC(t, d.s, tc.op, "", tc.params); status != 500 || body != `{"error":"missing_scopes"}` {
				t.Errorf("%q %s: %d %s", policy, tc.op, status, body)
			}
		}
		if requests.Load() != 0 {
			t.Fatalf("provider requests=%d", requests.Load())
		}
		assertNoAudit(t, d.home)
	}
}

const sendAllow = "[policy]\nmail_send='allow'\n"

func TestAllowedMailSendPostsOnceAndReturnsOK(t *testing.T) {
	for _, tc := range sendParams {
		t.Run(tc.op, func(t *testing.T) {
			d, graph := approvalDaemon(t, sendAllow)
			// The forward's Jev check reads the forwarded message, which the fake Jev screens green.
			event := greenMessage(t, "m1")
			graph.event.Store(&event)
			status, body := accountRPC(t, d.s, tc.op, "work", tc.params)
			if status != 200 || body != `{"ok":true}` {
				t.Fatalf("%d %s", status, body)
			}
			if got := graph.recorded(); len(got) != 1 || got[0].Method != "POST" || got[0].Path != tc.path {
				t.Fatalf("provider writes=%+v", got)
			}
			assertAudited(t, d.home, strings.Replace(tc.op, ".", "_", 1))
		})
	}
}

func TestInvalidMailSendParamsAreRefusedBeforeAccountAccess(t *testing.T) {
	d, reads, writes := policyDaemon(t, sendAllow)
	for op, params := range map[string][]string{
		"mail.send":    {`{}`, `{"to":[],"subject":"s","body":"b"}`, `{"to":["a@example.test"],"body":"b"}`, `{"to":["a@example.test"],"subject":"s"}`, `{"to":["a@example.test"],"subject":"s","body":"b","cc":["x"]}`},
		"mail.reply":   {`{}`, `{"id":"m1"}`, `{"id":"","body":"b"}`, `{"id":"m1","body":"b","to":["x"]}`},
		"mail.forward": {`{}`, `{"id":"m1"}`, `{"id":"m1","to":[]}`, `{"id":"m1","to":["a"],"body":"b"}`},
	} {
		for _, p := range params {
			if status, body := accountRPC(t, d.s, op, "work", p); status != 400 || body != `{"error":"invalid request"}` {
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
			d, graph := approvalDaemon(t, "[policy]\nmail_send='approve'\n")
			event := greenMessage(t, "m1")
			graph.event.Store(&event)
			id := pend(t, d.s, tc.op, tc.params)[0]
			if len(graph.recorded()) != 0 {
				t.Fatalf("held send reached the provider: %+v", graph.recorded())
			}
			if err := d.s.Approve(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if err := d.s.Approve(context.Background(), id); err == nil {
				t.Fatal("second approve ran")
			}
			if got := graph.recorded(); len(got) != 1 || got[0].Path != tc.path {
				t.Fatalf("provider writes=%+v", got)
			}
			assertState(t, d.s, id, `{"state":"done","result":{"ok":true}}`)
			text, _ := auditOf(t, d.home)
			if strings.Contains(text, "CANARY") || strings.Contains(text, "example.test") {
				t.Fatalf("audit=%s", text)
			}
		})
	}
}

// A reply or forward from a sealed source is always refused, also when mail_send allows it. The fake Graph API
// serves no message, and a failed verdict lookup counts as sealed.
func TestMailSendFromASealedSourceIsAlwaysRefused(t *testing.T) {
	var writes atomic.Int32
	d := graphDaemon(t, sendAllow, writeGrant, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			writes.Add(1)
		}
		w.WriteHeader(404)
		io.WriteString(w, `{}`)
	}), newNativeAPI())
	for _, tc := range sendParams[1:] {
		if status, body := accountRPC(t, d.s, tc.op, "work", tc.params); status != 500 || body != `{"error":"refused by policy: mail_send"}` {
			t.Errorf("%s: %d %s", tc.op, status, body)
		}
	}
	if writes.Load() != 0 {
		t.Fatalf("writes=%d", writes.Load())
	}
}

func TestConsoleShowsTheSourceOfAReplyOrForwardSend(t *testing.T) {
	f := consoleDaemon(t, "[policy]\nmail_send='approve'\n", true)
	for _, tc := range []struct{ op, params string }{
		{"mail.reply", `{"id":"g","body":"reply text"}`},
		{"mail.forward", `{"id":"g","to":["a@example.test"]}`},
	} {
		id := pend(t, f.d.s, tc.op, tc.params)[0]
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
	if f.writes() != 0 {
		t.Fatalf("provider writes=%d", f.writes())
	}
}
