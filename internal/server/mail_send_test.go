package server_test

import (
	"context"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// sendParams are valid params of the send operations, the first three in the shape of the matching draft
// operations, and the paths of the Graph writes that each makes. A reply or forward is a draft (approvalGraph names it
// w1), its PATCH, and its send; a draft send is the send of the draft d1.
var sendParams = []struct {
	op, params string
	paths      []string
}{
	{"mail.send", `{"to":["CANARY@example.test"],"subject":"CANARY subject","body":"CANARY body"}`, []string{"/me/sendMail"}},
	{"mail.reply", `{"id":"m1","body":"CANARY reply"}`, []string{"/me/messages/m1/createReply", "/me/messages/w1", "/me/messages/w1/send"}},
	{"mail.forward", `{"id":"m1","to":["a@example.test","b@example.test"],"body":"CANARY note"}`, []string{"/me/messages/m1/createForward", "/me/messages/w1", "/me/messages/w1/send"}},
	{"draft.send", `{"id":"d1"}`, []string{"/me/messages/d1/send"}},
}

// sendRead is the Graph JSON that a send of sendParams reads: the forward's Jev check reads the forwarded message,
// which the fake Jev screens green, and a draft send reads the draft d1.
func sendRead(t *testing.T, op string) string {
	if op == "draft.send" {
		return draftJSON
	}
	return greenMessage(t, "m1")
}

// writePaths are the paths of the recorded writes.
func writePaths(writes []meetingRequest) []string {
	out := []string{}
	for _, w := range writes {
		out = append(out, w.Path)
	}
	return out
}

func TestMailSendDefaultPolicyRefusesAfterSourceMetadata(t *testing.T) {
	d, reads, writes := policyDaemon(t, "")
	for _, tc := range sendParams {
		if status, body := accountRPC(t, d.s, tc.op, "work", tc.params); status != 500 || body != `{"error":"refused by policy: mail_send"}` {
			t.Errorf("%s: %d %s", tc.op, status, body)
		}
	}
	if reads.Load() != 15 || writes.Load() != 0 {
		t.Fatalf("expected only closed-folder and source metadata reads: reads=%d writes=%d", reads.Load(), writes.Load())
	}
	text, records := auditOf(t, d.home)
	if strings.Contains(text, "CANARY") || strings.Contains(text, "example.test") || len(records) != len(sendParams) {
		t.Fatalf("audit=%s", text)
	}
}

// Gmail has no extra grant for sending: source metadata is checked before the policy.
func TestGmailMailSendFollowsThePolicyWithoutAGrantCheck(t *testing.T) {
	d, google := gmailDaemon(t, gmailReadGrant, "")
	before := google.requests.Load()
	for _, tc := range sendParams {
		if status, body := accountRPC(t, d.s, tc.op, "", tc.params); status != 500 || body != `{"error":"refused by policy: mail_send"}` {
			t.Errorf("%s: %d %s", tc.op, status, body)
		}
	}
	if n := google.requests.Load() - before; n != 6 {
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
			event := sendRead(t, tc.op)
			graph.event.Store(&event)
			status, body := accountRPC(t, d.s, tc.op, "work", tc.params)
			if status != 200 || body != `{"ok":true}` {
				t.Fatalf("%d %s", status, body)
			}
			if got := graph.recorded(); !reflect.DeepEqual(writePaths(got), tc.paths) || got[len(got)-1].Method != "POST" {
				t.Fatalf("provider writes=%+v", got)
			}
			assertAudited(t, d.home, strings.Replace(tc.op, ".", "_", 1))
		})
	}
}

func TestInvalidMailSendParamsAreRefusedBeforeAccountAccess(t *testing.T) {
	d, reads, writes := policyDaemon(t, sendAllow)
	for op, params := range map[string][]string{
		"mail.send":    {`{}`, `{"to":[],"cc":[],"bcc":[],"subject":"s","body":"b"}`, `{"bcc":[""]}`, `{"to":["a@example.test"],"html":"yes"}`, `{"to":["a@example.test"],"from":"x"}`, `{"to":["a@example.test"],"cc":null}`},
		"mail.reply":   {`{}`, `{"body":"b"}`, `{"id":"","body":"b"}`, `{"id":"m1","body":"b","to":["x"]}`, `{"id":"m1","all":"yes"}`, `{"id":"m1","cc":[""]}`, `{"id":"m1","bcc":null}`},
		"draft.send":   {`{}`, `{"id":""}`, `{"id":"d1","body":"b"}`, `{"id":"d1","to":["a@example.test"]}`},
		"mail.forward": {`{}`, `{"to":["a@example.test"]}`, `{"id":"m1"}`, `{"id":"m1","to":[],"cc":[],"bcc":[],"body":"b"}`, `{"id":"m1","to":["a@example.test"],"subject":"s"}`, `{"id":"m1","cc":[""]}`, `{"id":"m1","bcc":null}`, `{"id":"m1","to":["a@example.test"],"html":"yes"}`},
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
			event := sendRead(t, tc.op)
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
			if got := graph.recorded(); !reflect.DeepEqual(writePaths(got), tc.paths) {
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
// serves no message, and a failed verdict lookup counts as sealed. A draft send has no sealed check.
func TestMailSendFromASealedSourceIsAlwaysRefused(t *testing.T) {
	var writes atomic.Int32
	d := graphDaemon(t, sendAllow, writeGrant, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if binFolders(w, r) {
			return
		}
		if r.Method == "GET" && !strings.Contains(r.URL.Query().Get("$select"), ",body") {
			response(w, map[string]any{"id": "m1", "parentFolderId": "inbox"})
			return
		}
		if r.Method != "GET" {
			writes.Add(1)
		}
		w.WriteHeader(404)
		io.WriteString(w, `{}`)
	}), newNativeAPI())
	for _, tc := range sendParams[1:3] {
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

// draftGraph is a fake Graph API whose sendMail answers 202 and whose new draft is a message with a hostile subject
// and recipient address. It records the path of each request.
func draftGraph(paths *[]string, mu *sync.Mutex) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*paths = append(*paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.URL.Path == "/me/sendMail" {
			w.WriteHeader(202)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"d1","conversationId":"conv","subject":"CANARY\u200b see https://evil.example.test/x","toRecipients":[{"emailAddress":{"address":"a@example.test\u0007"}}],"ccRecipients":[],"bccRecipients":[{"emailAddress":{"address":"b@example.test"}}]}`)
	}
}

// draft.new and mail.send take every param as optional, a send needs only a recipient, and the draft result
// comes from the provider's answer with every text field cleaned and the IDs as given.
func TestNewMessageParamsReachTheProviderAndTheDraftResultIsCleaned(t *testing.T) {
	var paths []string
	var mu sync.Mutex
	d := graphDaemon(t, "[policy]\nmail_send='allow'\nmail_draft='allow'\n", writeGrant, draftGraph(&paths, &mu), newNativeAPI())
	if status, body := accountRPC(t, d.s, "draft.new", "work", `{"to":["a@example.test"],"bcc":["b@example.test"],"body":"<p>x</p>","html":true}`); status != 200 || body != `{"id":"d1","thread_id":"conv","to":["a@example.test"],"cc":[],"bcc":["b@example.test"],"subject":"CANARY see [link removed]"}` {
		t.Fatalf("draft.new: %d %s", status, body)
	}
	if status, body := accountRPC(t, d.s, "mail.send", "work", `{"bcc":["b@example.test"]}`); status != 200 || body != `{"ok":true}` {
		t.Fatalf("mail.send: %d %s", status, body)
	}
	if want := []string{"POST /me/messages", "POST /me/sendMail"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("provider requests=%v", paths)
	}
}

// The stored result of an approved draft.new keeps only the draft and thread IDs: the subject and the
// recipients are mail content, which the pending result never holds.
func TestApprovedDraftStoresOnlyItsIDs(t *testing.T) {
	var paths []string
	var mu sync.Mutex
	d := graphDaemon(t, "[policy]\nmail_draft='approve'\n", writeGrant, draftGraph(&paths, &mu), newNativeAPI())
	id := pend(t, d.s, "draft.new", `{"to":["a@example.test"],"cc":["c@example.test"],"bcc":["b@example.test"],"subject":"CANARY subject","body":"CANARY body"}`)[0]
	if err := d.s.Approve(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	assertState(t, d.s, id, `{"state":"done","result":{"id":"d1","thread_id":"conv"}}`)
}

// draft.update gives the draft's IDs from the provider's answer, "ok": true as before it gave the IDs, and only the
// fields that the request gave, each text field cleaned; an empty list stays empty, and fields that were not given
// are not read back.
func TestDraftUpdateResultHasOnlyTheGivenFieldsCleaned(t *testing.T) {
	var paths []string
	d := graphDaemon(t, "[policy]\nmail_draft='allow'\n", writeGrant, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if binFolders(w, r) {
			return
		}
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"d1","parentFolderId":"drafts","conversationId":"conv","isDraft":true,"subject":"CANARY old","toRecipients":[{"emailAddress":{"address":"old@example.test"}}],"ccRecipients":[],"bccRecipients":[],"body":{"contentType":"text","content":"CANARY"}}`)
	}), newNativeAPI())
	status, body := accountRPC(t, d.s, "draft.update", "work", `{"id":"d1","to":["a@example.test\u0007"],"cc":[],"subject":"CANARY\u200b see https://evil.example.test/x"}`)
	if want := `{"id":"d1","thread_id":"conv","ok":true,"to":["a@example.test"],"cc":[],"subject":"CANARY see [link removed]"}`; status != 200 || body != want {
		t.Fatalf("draft.update: %d %s\nwant %s", status, body, want)
	}
	if want := []string{"GET /me/messages/d1", "GET /me/messages/d1", "GET /me/messages/d1", "PATCH /me/messages/d1"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("provider requests=%v", paths)
	}
}

// A draft delete or send of a message that is not a draft gives the agent its fixed refusal and writes nothing.
func TestDraftDeleteAndSendOfAMessageThatIsNotADraftAreRefusedPublicly(t *testing.T) {
	d, graph := approvalDaemon(t, "[policy]\nmail_draft='allow'\nmail_send='allow'\n")
	event := greenMessage(t, "m1")
	graph.event.Store(&event)
	for op, want := range map[string]string{"draft.delete": "draft delete refused: not a draft", "draft.send": "draft send refused: not a draft"} {
		if status, body := accountRPC(t, d.s, op, "work", `{"id":"m1"}`); status != 500 || body != `{"error":"`+want+`"}` {
			t.Errorf("%s: %d %s", op, status, body)
		}
	}
	if got := graph.recorded(); len(got) != 0 {
		t.Fatalf("provider writes=%+v", got)
	}
}
