package e2e_test

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// verdict is the verdict field of a message view.
func verdict(t *testing.T, view string) string {
	t.Helper()
	return decode[struct{ Verdict string }](t, view).Verdict
}

// mailPage is the result of mail list and mail search.
type mailPage struct {
	Messages []struct{ ID, Verdict string }
	NextPage *string `json:"next_page"`
}

// An agent reads a folder page by page: it repeats the same options with --page until no next_page comes.
func TestListPagesThroughTheFolderWithNextPage(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	all := decode[mailPage](t, s.ok("mail", "list"))
	if len(all.Messages) != 6 || all.NextPage != nil {
		t.Fatalf("one default page of six: %+v", all)
	}
	seen := []string{}
	args := []string{"mail", "list", "--count", "4"}
	for range 3 {
		p := decode[mailPage](t, s.ok(args...))
		for _, m := range p.Messages {
			seen = append(seen, m.ID)
		}
		if p.NextPage == nil {
			break
		}
		args = []string{"mail", "list", "--count", "4", "--page", *p.NextPage}
	}
	want := []string{}
	for _, m := range all.Messages {
		want = append(want, m.ID)
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("pages gave %v, want %v", seen, want)
	}
	if out := s.ok("mail", "list", "--count", "4", "--pretty"); !strings.HasSuffix(out, "\nnext_page: 4\n") {
		t.Fatalf("pretty page without its next_page line:\n%s", out)
	}
	s.refused("invalid request", "mail", "list", "--page", "not a page")
}

func TestTriageHidesUnsafeMailAndFailsClosed(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	verdicts := map[string]string{}
	for _, m := range decode[mailPage](t, s.ok("mail", "list")).Messages {
		verdicts[m.ID] = m.Verdict
	}
	want := map[string]string{"lunch": "green", "invoice": "green", "kickoff": "green", "phish": "red", "code": "sealed", "broken": "error"}
	if !reflect.DeepEqual(verdicts, want) {
		t.Fatalf("verdicts=%v want %v", verdicts, want)
	}
	if out := s.ok("mail", "get", "lunch"); !strings.Contains(out, "canary-body-lunch") {
		t.Fatalf("green body missing: %s", out)
	}
	for id, want := range map[string]string{"phish": "red", "code": "sealed", "broken": "error"} {
		out := s.ok("mail", "get", id)
		if verdict(t, out) != want || strings.Contains(out, "canary-body") || strings.Contains(out, "482913") {
			t.Fatalf("%s: unsafe body shown or wrong verdict: %s", id, out)
		}
	}
	// A failed screening is not remembered: once the engine answers, the next read screens the message again.
	s.exec(`UPDATE messages SET body = 'canary-body-broken: fixed' WHERE id = 'broken'`)
	if out := s.ok("mail", "get", "broken"); verdict(t, out) != "green" || !strings.Contains(out, "canary-body-broken: fixed") {
		t.Fatalf("error not screened again: %s", out)
	}
}

func TestSendWaitsForApprovalAndRunsOnlyOnce(t *testing.T) {
	t.Parallel()
	s := start(t, "[policy]\nmail_send='approve'\n")
	sent := func() []string {
		return s.column(`SELECT subject FROM messages WHERE account = 'demo@fake.invalid' AND folder = 'sent' ORDER BY subject`)
	}
	send := func(subject string) pending {
		return s.held("mail", "send", "--to", "ana@example.test", "--subject", subject, "--body", "canary-body-send")
	}

	approved := send("canary-subject-approved")
	if got := sent(); len(got) != 0 {
		t.Fatalf("pending send ran: %v", got)
	}
	s.approve(approved.ApprovalID)
	if got := sent(); !reflect.DeepEqual(got, []string{"canary-subject-approved"}) || s.state(approved.ApprovalID) != "done" {
		t.Fatalf("approved send: sent=%v state=%s", got, s.state(approved.ApprovalID))
	}
	if code := s.decide(approved.ApprovalID, "approve"); code != http.StatusConflict {
		t.Fatalf("second approve: HTTP %d", code)
	}

	denied := send("canary-subject-denied")
	if code := s.decide(denied.ApprovalID, "deny"); code != http.StatusSeeOther {
		t.Fatalf("deny: HTTP %d", code)
	}
	if got := sent(); !reflect.DeepEqual(got, []string{"canary-subject-approved"}) || s.state(denied.ApprovalID) != "denied" {
		t.Fatalf("after deny: sent=%v state=%s", got, s.state(denied.ApprovalID))
	}
}

func TestDefaultPolicyRefusesSendsAndMeetings(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	before := s.dump()
	s.refused("refused by policy: mail_send", "mail", "send", "--to", "ana@example.test", "--subject", "canary-subject-refused", "--body", "canary-body-refused")
	s.refused("refused by policy: calendar_invite", "calendar", "invite", "--to", "ana@example.test", "--subject", "canary-subject-refused", "--start", "2030-01-02 09:00", "--duration", "30m", "--no-online")
	// A refused write changes no row at the provider: no sent mail, draft, event, attendee, folder, or category.
	s.assertTables(before)
}

func TestScreeningFlagMakesAnAllowedWriteWaitForApproval(t *testing.T) {
	t.Parallel()
	s := start(t, "[policy]\nmail_send='allow'\nmail_draft='allow'\n")
	if out := s.ok("mail", "send", "--to", "ana@example.test", "--subject", "canary-subject-plain", "--body", "canary-body-plain: see you at noon"); !decode[struct{ OK bool }](t, out).OK {
		t.Fatalf("plain send: %s", out)
	}
	flagged := s.held("mail", "send", "--to", "ana@example.test", "--subject", "canary-subject-flagged", "--body", "canary-body-flagged: the new password is hunter2")
	draft := s.held("draft", "new", "--to", "ana@example.test", "--subject", "canary-subject-draft", "--body", "canary-body-draft: my password is hunter2")
	sent := func() []string {
		return s.column(`SELECT subject FROM messages WHERE folder = 'sent' ORDER BY subject`)
	}
	if got := sent(); !reflect.DeepEqual(got, []string{"canary-subject-plain"}) {
		t.Fatalf("flagged send ran: %v", got)
	}
	if got := s.column(`SELECT subject FROM messages WHERE folder = 'drafts'`); len(got) != 0 {
		t.Fatalf("flagged draft ran: %v", got)
	}
	// The user decides: an approved run is not checked again.
	s.approve(flagged.ApprovalID)
	s.approve(draft.ApprovalID)
	if got := sent(); !reflect.DeepEqual(got, []string{"canary-subject-flagged", "canary-subject-plain"}) {
		t.Fatalf("approved flagged send: %v", got)
	}
	if got := s.column(`SELECT subject FROM messages WHERE folder = 'drafts'`); !reflect.DeepEqual(got, []string{"canary-subject-draft"}) {
		t.Fatalf("approved flagged draft: %v", got)
	}
}

// newDraft is the result of draft new.
type newDraft struct {
	ID       string   `json:"id"`
	ThreadID string   `json:"thread_id"`
	To       []string `json:"to"`
	CC       []string `json:"cc"`
	BCC      []string `json:"bcc"`
	Subject  string   `json:"subject"`
}

// A new draft takes repeated To, CC, and BCC recipients and an HTML body, and every field is optional. The
// result names the draft and its recipients, and mail get shows the BCC of the draft like its To and CC.
func TestDraftNewTakesEveryRecipientListAndAnHTMLBody(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	draft := decode[newDraft](t, s.ok("draft", "new", "--to", "ana@example.test", "--to", "bob@example.test", "--cc", "cy@example.test", "--cc", "di@example.test",
		"--bcc", "ed@example.test", "--bcc", "flo@example.test", "--subject", "canary-subject-html", "--body", "<p>canary-body-html</p>", "--html"))
	want := newDraft{draft.ID, draft.ID, []string{"ana@example.test", "bob@example.test"}, []string{"cy@example.test", "di@example.test"}, []string{"ed@example.test", "flo@example.test"}, "canary-subject-html"}
	if draft.ID == "" || !reflect.DeepEqual(draft, want) {
		t.Fatalf("draft=%+v", draft)
	}
	stored := s.column(`SELECT folder || ' ' || is_draft || ' ' || to_addresses || ' ' || cc_addresses || ' ' || bcc_addresses || ' ' || content_type || ' ' || body FROM messages WHERE id = ?`, draft.ID)
	if !reflect.DeepEqual(stored, []string{`drafts 1 ["ana@example.test","bob@example.test"] ["cy@example.test","di@example.test"] ["ed@example.test","flo@example.test"] html <p>canary-body-html</p>`}) {
		t.Fatalf("stored draft: %v", stored)
	}
	view := decode[struct {
		Verdict string
		BCC     []struct{ Address string } `json:"bcc"`
	}](t, s.ok("mail", "get", draft.ID))
	if view.Verdict != "green" || fmt.Sprint(view.BCC) != "[{ed@example.test} {flo@example.test}]" {
		t.Fatalf("mail get: %+v", view)
	}

	// Without options, the draft is empty plain text, as the apps allow.
	empty := decode[newDraft](t, s.ok("draft", "new"))
	if empty.ID == "" || !reflect.DeepEqual(empty, newDraft{empty.ID, empty.ID, []string{}, []string{}, []string{}, ""}) {
		t.Fatalf("empty draft=%+v", empty)
	}
	plain := decode[newDraft](t, s.ok("draft", "new", "--body", "canary-body-line-one\ncanary-body-line-two")).ID
	if got := s.column(`SELECT content_type || ' ' || body FROM messages WHERE id = ?`, plain); !reflect.DeepEqual(got, []string{"text canary-body-line-one\ncanary-body-line-two"}) {
		t.Fatalf("plain draft: %q", got)
	}
}

// A send needs a recipient in To, CC, or BCC; one with only BCC recipients goes out. A send without any
// recipient is invalid usage and reaches no policy decision.
func TestSendNeedsARecipientAndCanGoOnlyToBcc(t *testing.T) {
	t.Parallel()
	s := start(t, sendAllowed)
	before := s.dump()
	if out, errout, code := s.amg("mail", "send", "--subject", "canary-subject-nobody", "--body", "canary-body-nobody"); code != 2 || out != "" || errout == "" {
		t.Fatalf("send without a recipient: exit %d stdout %q stderr %q", code, out, errout)
	}
	s.assertTables(before)
	if got := s.auditEvents("policy"); len(got) != 0 {
		t.Fatalf("policy records of an invalid send: %v", got)
	}
	if out := s.ok("mail", "send", "--bcc", "ana@example.test", "--subject", "canary-subject-bcc", "--body", "canary-body-bcc"); !decode[struct{ OK bool }](t, out).OK {
		t.Fatalf("bcc send: %s", out)
	}
	if got := s.column(`SELECT to_addresses || ' ' || cc_addresses || ' ' || bcc_addresses || ' ' || content_type FROM messages WHERE folder = 'sent'`); !reflect.DeepEqual(got, []string{`[] [] ["ana@example.test"] text`}) {
		t.Fatalf("sent: %v", got)
	}
}

const sendAllowed = "[policy]\nmail_send='allow'\n"

// The approval console shows each recipient list of a waiting send, marks the BCC recipients as hidden, and
// shows an HTML body as its source text. The approved run sends the stored lists and the HTML body unchanged.
func TestApprovalPageShowsToCcAndMarkedBcc(t *testing.T) {
	t.Parallel()
	s := start(t, "[policy]\nmail_send='approve'\n")
	p := s.held("mail", "send", "--to", "ana@example.test", "--cc", "cy@example.test", "--bcc", "ed@example.test", "--subject", "canary-subject-console", "--body", "<b>canary-body-console</b>", "--html")
	_, list := s.consoleGet(consolePassword, "/")
	if !strings.Contains(list, "ana@example.test; cc: cy@example.test; bcc (hidden): ed@example.test") {
		t.Fatalf("list misses the recipients:\n%s", list)
	}
	_, item := s.consoleGet(consolePassword, "/a/"+p.ApprovalID)
	for _, want := range []string{"<dt>to</dt><dd><pre>ana@example.test</pre></dd>", "<dt>cc</dt><dd><pre>cy@example.test</pre></dd>", "<dt>bcc (hidden recipients)</dt><dd><pre>ed@example.test</pre></dd>",
		"<dt>body (HTML source, not rendered)</dt><dd><pre>&lt;b&gt;canary-body-console&lt;/b&gt;</pre></dd>"} {
		if !strings.Contains(item, want) {
			t.Errorf("item page misses %q:\n%s", want, item)
		}
	}
	s.approve(p.ApprovalID)
	if got := s.column(`SELECT to_addresses || ' ' || cc_addresses || ' ' || bcc_addresses || ' ' || content_type || ' ' || body FROM messages WHERE folder = 'sent'`); !reflect.DeepEqual(got, []string{`["ana@example.test"] ["cy@example.test"] ["ed@example.test"] html <b>canary-body-console</b>`}) {
		t.Fatalf("sent: %v", got)
	}
}

// seedTeam adds the message team: from ana, to the account and bob, copied to cy, the account in capitals, and bob
// again in capitals, with the Reply-To that the fake provider does not have, so a reply goes to the sender.
func (s *server) seedTeam() {
	s.t.Helper()
	s.exec(`INSERT INTO messages (account, id, sender_name, sender_address, to_addresses, cc_addresses, subject, body) VALUES
		('demo@fake.invalid', 'team', 'canary-name-ana', 'ana@example.test', '["demo@fake.invalid", "bob@example.test"]', '["cy@example.test", "DEMO@fake.invalid", "BOB@example.test"]', 'canary-subject-team', 'canary-body-team: a <b>plan</b>')`)
}

// A reply all draft goes to the sender and copies everyone on the original except the account, each address once
// ignoring case, plus the agent's CC and BCC. An HTML reply keeps the quoted original below the agent's HTML, and
// the reply stays in the conversation. The result of a green source names the draft, its recipients, and subject.
func TestReplyAllDraftAddsTheAgentCcAndBccAndQuotesBelowHTML(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	s.seedTeam()
	draft := decode[newDraft](t, s.ok("draft", "reply", "team", "--all", "--cc", "di@example.test", "--cc", "CY@example.test", "--bcc", "ed@example.test", "--body", "<p>canary-body-reply-all</p>", "--html"))
	want := newDraft{draft.ID, "team", []string{"ana@example.test"}, []string{"bob@example.test", "cy@example.test", "di@example.test"}, []string{"ed@example.test"}, "Re: canary-subject-team"}
	if draft.ID == "" || !reflect.DeepEqual(draft, want) {
		t.Fatalf("draft=%+v", draft)
	}
	stored := s.column(`SELECT folder || ' ' || thread_id || ' ' || to_addresses || ' ' || cc_addresses || ' ' || bcc_addresses || ' ' || content_type || ' ' || body FROM messages WHERE id = ?`, draft.ID)
	if !reflect.DeepEqual(stored, []string{`drafts team ["ana@example.test"] ["bob@example.test","cy@example.test","di@example.test"] ["ed@example.test"] html <p>canary-body-reply-all</p><blockquote>canary-body-team: a &lt;b&gt;plan&lt;/b&gt;</blockquote>`}) {
		t.Fatalf("stored draft: %v", stored)
	}
	// Without --all, the reply goes to the sender only, with the agent's CC.
	single := decode[newDraft](t, s.ok("draft", "reply", "team", "--cc", "di@example.test"))
	if !reflect.DeepEqual([][]string{single.To, single.CC, single.BCC}, [][]string{{"ana@example.test"}, {"di@example.test"}, {}}) {
		t.Fatalf("reply=%+v", single)
	}
}

// The draft result of a reply to a red message names only the draft: its recipients and subject come from a message
// whose headers are hidden.
func TestReplyDraftOfARedMessageGivesOnlyItsIDs(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	out := s.ok("draft", "reply", "phish", "--all", "--body", "canary-body-reply-red")
	got := decode[map[string]any](t, out)
	id, _ := got["id"].(string)
	if want := map[string]any{"id": id, "thread_id": "phish"}; id == "" || !reflect.DeepEqual(got, want) {
		t.Fatalf("draft=%s", out)
	}
}

// mail reply --all sends at once under allow, to the recipients that reply all picks plus the agent's CC and BCC.
func TestMailReplyAllSendsToEveryoneAndTheAgentCopies(t *testing.T) {
	t.Parallel()
	s := start(t, sendAllowed)
	s.seedTeam()
	if out := s.ok("mail", "reply", "team", "--all", "--cc", "di@example.test", "--bcc", "ed@example.test", "--body", "canary-body-reply-sent"); !decode[struct{ OK bool }](t, out).OK {
		t.Fatalf("reply: %s", out)
	}
	if got := s.column(`SELECT thread_id || ' ' || to_addresses || ' ' || cc_addresses || ' ' || bcc_addresses || ' ' || content_type || ' ' || body FROM messages WHERE folder = 'sent'`); !reflect.DeepEqual(got, []string{`team ["ana@example.test"] ["bob@example.test","cy@example.test","di@example.test"] ["ed@example.test"] text canary-body-reply-sent`}) {
		t.Fatalf("sent: %v", got)
	}
}

// The approval page of a waiting reply shows the recipients worked out from the original, with the agent's CC
// added and its BCC marked as hidden. The approved run sends to them. An approved reply draft stores only its IDs.
func TestApprovalPageShowsTheReplyRecipients(t *testing.T) {
	t.Parallel()
	s := start(t, "[policy]\nmail_send='approve'\nmail_draft='approve'\n")
	s.seedTeam()
	p := s.held("mail", "reply", "team", "--all", "--cc", "di@example.test", "--bcc", "ed@example.test", "--body", "canary-body-reply-held")
	_, item := s.consoleGet(consolePassword, "/a/"+p.ApprovalID)
	for _, want := range []string{"<h2>Reply recipients</h2>", "<dt>to</dt><dd><pre>ana@example.test</pre></dd>",
		"<dt>cc</dt><dd><pre>bob@example.test</pre><pre>cy@example.test</pre><pre>di@example.test</pre></dd>", "<dt>bcc (hidden recipients)</dt><dd><pre>ed@example.test</pre></dd>"} {
		if !strings.Contains(item, want) {
			t.Errorf("item page misses %q:\n%s", want, item)
		}
	}
	s.approve(p.ApprovalID)
	if got := s.column(`SELECT to_addresses || ' ' || cc_addresses || ' ' || bcc_addresses FROM messages WHERE folder = 'sent'`); !reflect.DeepEqual(got, []string{`["ana@example.test"] ["bob@example.test","cy@example.test","di@example.test"] ["ed@example.test"]`}) {
		t.Fatalf("sent: %v", got)
	}
	d := s.held("draft", "reply", "team", "--all", "--body", "canary-body-reply-draft")
	s.approve(d.ApprovalID)
	status := decode[struct {
		State  string
		Result map[string]any
	}](t, s.ok("approvals", "status", d.ApprovalID))
	if id, _ := status.Result["id"].(string); status.State != "done" || id == "" || !reflect.DeepEqual(status.Result, map[string]any{"id": id, "thread_id": "team"}) {
		t.Fatalf("approved draft: %+v", status)
	}
	// When the original can no longer be read, the page says so instead of showing no recipients.
	gone := s.held("mail", "reply", "team", "--body", "canary-body-reply-gone")
	s.exec(`UPDATE messages SET account = 'someone-else' WHERE id = 'team'`)
	if _, item := s.consoleGet(consolePassword, "/a/"+gone.ApprovalID); !strings.Contains(item, "<p>Reply recipients could not be read.</p>") || strings.Contains(item, "<dt>to</dt><dd><pre>ana@example.test") {
		t.Fatalf("item page of an unreadable original:\n%s", item)
	}
}

// seedReport adds the message report: from bob to the account, with an HTML-like body and two attachments.
func (s *server) seedReport() {
	s.t.Helper()
	s.exec(`INSERT INTO messages (account, id, sender_name, sender_address, to_addresses, subject, body, attachments) VALUES
		('demo@fake.invalid', 'report', 'canary-name-bob', 'bob@example.test', '["demo@fake.invalid"]', 'canary-subject-report', 'canary-body-report: a <b>plan</b>', '["canary-file-plan.pdf","canary-file-notes.txt"]')`)
}

// A forward draft needs no recipient: it carries the agent's note, the original attachments, and the conversation of
// the original. The result of a green source names the draft, its recipients, and subject.
func TestDraftForwardCarriesTheNoteAndTheAttachmentsWithoutARecipient(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	s.seedReport()
	draft := decode[newDraft](t, s.ok("draft", "forward", "report", "--body", "canary-body-note\nline two"))
	if want := (newDraft{draft.ID, "report", []string{}, []string{}, []string{}, "Fw: canary-subject-report"}); draft.ID == "" || !reflect.DeepEqual(draft, want) {
		t.Fatalf("draft=%+v", draft)
	}
	stored := s.column(`SELECT folder || ' ' || thread_id || ' ' || to_addresses || ' ' || cc_addresses || ' ' || bcc_addresses || ' ' || attachments || ' ' || content_type || ' ' || body FROM messages WHERE id = ?`, draft.ID)
	if !reflect.DeepEqual(stored, []string{`drafts report [] [] [] ["canary-file-plan.pdf","canary-file-notes.txt"] text canary-body-note` + "\nline two"}) {
		t.Fatalf("stored draft: %q", stored)
	}
}

// mail forward needs a recipient: without one it is invalid usage and reaches no policy decision. With To, CC, and
// BCC and an HTML note, it sends at once under allow, with the original below the note and its attachments.
func TestMailForwardSendsToEveryListWithTheOriginalBelowTheHTMLNote(t *testing.T) {
	t.Parallel()
	s := start(t, sendAllowed)
	s.seedReport()
	before := s.dump()
	if out, errout, code := s.amg("mail", "forward", "report", "--body", "canary-body-nobody"); code != 2 || out != "" || errout == "" {
		t.Fatalf("forward without a recipient: exit %d stdout %q stderr %q", code, out, errout)
	}
	s.assertTables(before)
	if got := s.auditEvents("policy"); len(got) != 0 {
		t.Fatalf("policy records of an invalid forward: %v", got)
	}
	if out := s.ok("mail", "forward", "report", "--to", "ana@example.test", "--cc", "cy@example.test", "--bcc", "ed@example.test", "--body", "<p>canary-body-note</p>", "--html"); !decode[struct{ OK bool }](t, out).OK {
		t.Fatalf("forward: %s", out)
	}
	if got := s.column(`SELECT thread_id || ' ' || to_addresses || ' ' || cc_addresses || ' ' || bcc_addresses || ' ' || attachments || ' ' || content_type || ' ' || body FROM messages WHERE folder = 'sent'`); !reflect.DeepEqual(got, []string{
		`report ["ana@example.test"] ["cy@example.test"] ["ed@example.test"] ["canary-file-plan.pdf","canary-file-notes.txt"] html <p>canary-body-note</p><blockquote>canary-body-report: a &lt;b&gt;plan&lt;/b&gt;</blockquote>`}) {
		t.Fatalf("sent: %v", got)
	}
}

// The approval page of a waiting forward shows its To and CC and marks its BCC as hidden; the approved run sends
// to them. A forward draft of a red message waits, because Jev cannot read the forwarded text, and its approved run
// stores only the draft IDs.
func TestApprovalPageShowsTheForwardRecipients(t *testing.T) {
	t.Parallel()
	s := start(t, "[policy]\nmail_send='approve'\n")
	s.seedReport()
	p := s.held("mail", "forward", "report", "--to", "ana@example.test", "--cc", "cy@example.test", "--bcc", "ed@example.test", "--body", "canary-body-forward-held")
	if _, list := s.consoleGet(consolePassword, "/"); !strings.Contains(list, "ana@example.test; cc: cy@example.test; bcc (hidden): ed@example.test") {
		t.Fatalf("list misses the recipients:\n%s", list)
	}
	_, item := s.consoleGet(consolePassword, "/a/"+p.ApprovalID)
	for _, want := range []string{"<dt>to</dt><dd><pre>ana@example.test</pre></dd>", "<dt>cc</dt><dd><pre>cy@example.test</pre></dd>", "<dt>bcc (hidden recipients)</dt><dd><pre>ed@example.test</pre></dd>"} {
		if !strings.Contains(item, want) {
			t.Errorf("item page misses %q:\n%s", want, item)
		}
	}
	s.approve(p.ApprovalID)
	if got := s.column(`SELECT to_addresses || ' ' || cc_addresses || ' ' || bcc_addresses || ' ' || attachments || ' ' || body FROM messages WHERE folder = 'sent'`); !reflect.DeepEqual(got, []string{
		`["ana@example.test"] ["cy@example.test"] ["ed@example.test"] ["canary-file-plan.pdf","canary-file-notes.txt"] canary-body-forward-held`}) {
		t.Fatalf("sent: %v", got)
	}
	red := s.held("draft", "forward", "phish", "--to", "ana@example.test", "--body", "canary-body-forward-red")
	s.approve(red.ApprovalID)
	status := decode[struct {
		State  string
		Result map[string]any
	}](t, s.ok("approvals", "status", red.ApprovalID))
	if id, _ := status.Result["id"].(string); status.State != "done" || id == "" || !reflect.DeepEqual(status.Result, map[string]any{"id": id, "thread_id": "phish"}) {
		t.Fatalf("approved red forward draft: %+v", status)
	}
}

// draft update changes only the fields that it is given: a list replaces the old one, and an empty value clears it.
// The result gives the draft's IDs and the changed fields, and the returned ID reads the updated draft.
func TestDraftUpdateChangesOnlyTheGivenFields(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	id := decode[newDraft](t, s.ok("draft", "new", "--to", "ana@example.test", "--cc", "cy@example.test", "--bcc", "ed@example.test", "--subject", "canary-subject-old", "--body", "canary-body-old")).ID
	stored := func() string {
		return strings.Join(s.column(`SELECT folder || ' ' || is_draft || ' ' || to_addresses || ' ' || cc_addresses || ' ' || bcc_addresses || ' ' || subject || ' ' || content_type || ' ' || body FROM messages WHERE id = ?`, id), "\n")
	}
	for _, tc := range []struct {
		args   []string
		result map[string]any
		row    string
	}{
		{[]string{"--subject", "canary-subject-new"}, map[string]any{"subject": "canary-subject-new"},
			`drafts 1 ["ana@example.test"] ["cy@example.test"] ["ed@example.test"] canary-subject-new text canary-body-old`},
		{[]string{"--to", "bob@example.test", "--to", "di@example.test"}, map[string]any{"to": []any{"bob@example.test", "di@example.test"}},
			`drafts 1 ["bob@example.test","di@example.test"] ["cy@example.test"] ["ed@example.test"] canary-subject-new text canary-body-old`},
		{[]string{"--cc", ""}, map[string]any{"cc": []any{}},
			`drafts 1 ["bob@example.test","di@example.test"] [] ["ed@example.test"] canary-subject-new text canary-body-old`},
		{[]string{"--body", "<p>canary-body-html</p>", "--html"}, map[string]any{},
			`drafts 1 ["bob@example.test","di@example.test"] [] ["ed@example.test"] canary-subject-new html <p>canary-body-html</p>`},
		{[]string{"--bcc", "flo@example.test", "--body", "canary-body-plain"}, map[string]any{"bcc": []any{"flo@example.test"}},
			`drafts 1 ["bob@example.test","di@example.test"] [] ["flo@example.test"] canary-subject-new text canary-body-plain`},
	} {
		got := decode[map[string]any](t, s.ok(append([]string{"draft", "update", id}, tc.args...)...))
		tc.result["id"], tc.result["thread_id"] = id, id
		if !reflect.DeepEqual(got, tc.result) {
			t.Fatalf("update %v: result %v, want %v", tc.args, got, tc.result)
		}
		if row := stored(); row != tc.row {
			t.Fatalf("update %v: stored %q, want %q", tc.args, row, tc.row)
		}
		id = got["id"].(string)
	}
	view := decode[struct {
		Verdict string
		Subject string
		Body    string
	}](t, s.ok("mail", "get", id))
	if view.Verdict != "green" || view.Subject != "canary-subject-new" || !strings.Contains(view.Body, "canary-body-plain") {
		t.Fatalf("mail get of the updated draft: %+v", view)
	}
}

// draft update works on reply and forward drafts too: --body replaces the whole body, quoted original included,
// and the draft keeps its conversation, its recipients, and the forwarded attachments.
func TestDraftUpdateChangesReplyAndForwardDrafts(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	s.seedTeam()
	s.seedReport()
	reply := decode[newDraft](t, s.ok("draft", "reply", "team", "--body", "canary-body-reply")).ID
	if got := decode[map[string]any](t, s.ok("draft", "update", reply, "--body", "canary-body-reply-new")); !reflect.DeepEqual(got, map[string]any{"id": reply, "thread_id": "team"}) {
		t.Fatalf("reply update: %v", got)
	}
	forward := decode[newDraft](t, s.ok("draft", "forward", "report", "--body", "canary-body-note")).ID
	if got := decode[map[string]any](t, s.ok("draft", "update", forward, "--to", "ana@example.test")); !reflect.DeepEqual(got, map[string]any{"id": forward, "thread_id": "report", "to": []any{"ana@example.test"}}) {
		t.Fatalf("forward update: %v", got)
	}
	got := s.column(`SELECT thread_id || ' ' || to_addresses || ' ' || subject || ' ' || attachments || ' ' || body FROM messages WHERE id IN (?, ?) ORDER BY thread_id DESC`, reply, forward)
	if want := []string{`team ["ana@example.test"] Re: canary-subject-team [] canary-body-reply-new`,
		`report ["ana@example.test"] Fw: canary-subject-report ["canary-file-plan.pdf","canary-file-notes.txt"] canary-body-note`}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stored drafts: %q", got)
	}
}

// draft update without a field, or with --html but no --body, is invalid usage and reaches no policy decision. An
// update of a message that is not a draft is refused with a fixed text before any write.
func TestDraftUpdateRefusesNoFieldAndAMessageThatIsNotADraft(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	id := decode[newDraft](t, s.ok("draft", "new", "--subject", "canary-subject-draft")).ID
	before := s.dump()
	audit := func() string {
		data, err := os.ReadFile(filepath.Join(s.home, "audit.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	records := audit()
	for _, args := range [][]string{{"draft", "update", id}, {"draft", "update", id, "--html"}, {"draft", "update", id, "--subject", "canary-subject-x", "--html"}} {
		if out, errout, code := s.amg(args...); code != 2 || out != "" || errout == "" {
			t.Fatalf("amg %v: exit %d stdout %q stderr %q", args, code, out, errout)
		}
	}
	s.refused("draft update refused: not a draft", "draft", "update", "lunch", "--subject", "canary-subject-x", "--body", "canary-body-x")
	s.assertTables(before)
	if audit() != records {
		t.Fatal("refused updates wrote audit records")
	}
}

// A draft that the user wrote in the app can be updated too. An update that waits for approval stores only the fields
// that it was given; the approval page shows them, and the approved run applies them and stores only the draft IDs.
func TestApprovedDraftUpdateAppliesOnlyTheGivenFields(t *testing.T) {
	t.Parallel()
	s := start(t, "[policy]\nmail_draft='approve'\n")
	id := "users-draft"
	s.exec(`INSERT INTO messages (account, id, folder, to_addresses, cc_addresses, subject, body, is_draft, thread_id) VALUES
		('demo@fake.invalid', ?, 'drafts', '["ana@example.test"]', '["cy@example.test"]', 'canary-subject-old', 'canary-body-old', 1, 'conversation')`, id)
	p := s.held("draft", "update", id, "--cc", "", "--subject", "canary-subject-new")
	if got := s.column(`SELECT cc_addresses || ' ' || subject FROM messages WHERE id = ?`, id); !reflect.DeepEqual(got, []string{`["cy@example.test"] canary-subject-old`}) {
		t.Fatalf("pending update ran: %v", got)
	}
	_, item := s.consoleGet(consolePassword, "/a/"+p.ApprovalID)
	for _, want := range []string{"<dt>cc</dt><dd></dd>", "<dt>subject</dt><dd><pre>canary-subject-new</pre></dd>"} {
		if !strings.Contains(item, want) {
			t.Errorf("item page misses %q:\n%s", want, item)
		}
	}
	if strings.Contains(item, "<dt>to</dt>") || strings.Contains(item, "<dt>body</dt>") {
		t.Errorf("item page shows a field that was not given:\n%s", item)
	}
	s.approve(p.ApprovalID)
	if got := s.column(`SELECT to_addresses || ' ' || cc_addresses || ' ' || subject || ' ' || body FROM messages WHERE id = ?`, id); !reflect.DeepEqual(got, []string{`["ana@example.test"] [] canary-subject-new canary-body-old`}) {
		t.Fatalf("approved update: %v", got)
	}
	status := decode[struct {
		State  string
		Result map[string]any
	}](t, s.ok("approvals", "status", p.ApprovalID))
	if status.State != "done" || !reflect.DeepEqual(status.Result, map[string]any{"id": id, "thread_id": "conversation"}) {
		t.Fatalf("approved draft update: %+v", status)
	}
}

// draft delete removes the draft under the default policy, with an operation record of its ID only. A received
// message is never deleted: draft delete refuses it with a fixed text and changes nothing.
func TestDraftDeleteRemovesTheDraftAndNeverOtherMail(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	id := decode[newDraft](t, s.ok("draft", "new", "--to", "ana@example.test", "--subject", "canary-subject-delete", "--body", "canary-body-delete")).ID
	if out := s.ok("draft", "delete", id); !decode[struct{ OK bool }](t, out).OK {
		t.Fatalf("draft delete: %s", out)
	}
	if got := s.column(`SELECT id FROM messages WHERE id = ?`, id); len(got) != 0 {
		t.Fatalf("deleted draft remains: %v", got)
	}
	if got := s.operations(); !reflect.DeepEqual(got, []string{"draft_new [" + id + "]", "draft_delete [" + id + "]"}) {
		t.Fatalf("operation records=%v", got)
	}
	before := s.dump()
	for _, received := range []string{"lunch", "phish"} {
		s.refused("draft delete refused: not a draft", "draft", "delete", received)
	}
	s.refused("refused by policy: mail_send", "draft", "send", "lunch")
	s.assertTables(before)
	if got := s.operations(); len(got) != 2 {
		t.Fatalf("refused deletes wrote operation records: %v", got)
	}
}

// draft send under allow sends the draft as it is: it leaves drafts and lands in sent with every recipient list, and
// the result is ok. A received message is refused with a fixed text, and under the default policy (deny) no draft is
// sent.
func TestDraftSendSendsTheDraftAndOnlyADraft(t *testing.T) {
	t.Parallel()
	s := start(t, sendAllowed)
	id := decode[newDraft](t, s.ok("draft", "new", "--to", "ana@example.test", "--cc", "cy@example.test", "--bcc", "ed@example.test", "--subject", "canary-subject-send", "--body", "<p>canary-body-send</p>", "--html")).ID
	before := s.dump()
	s.refused("draft send refused: not a draft", "draft", "send", "lunch")
	s.assertTables(before)
	if out := s.ok("draft", "send", id); !decode[struct{ OK bool }](t, out).OK {
		t.Fatalf("draft send: %s", out)
	}
	if got := s.column(`SELECT folder || ' ' || is_draft || ' ' || to_addresses || ' ' || cc_addresses || ' ' || bcc_addresses || ' ' || subject || ' ' || content_type || ' ' || body FROM messages WHERE id = ?`, id); !reflect.DeepEqual(got,
		[]string{`sent 0 ["ana@example.test"] ["cy@example.test"] ["ed@example.test"] canary-subject-send html <p>canary-body-send</p>`}) {
		t.Fatalf("sent draft: %q", got)
	}
	if got := s.operations(); !reflect.DeepEqual(got, []string{"draft_new [" + id + "]", "draft_send [" + id + "]"}) {
		t.Fatalf("operation records=%v", got)
	}

	denied := start(t, "")
	draft := decode[newDraft](t, denied.ok("draft", "new", "--to", "ana@example.test", "--subject", "canary-subject-denied")).ID
	before = denied.dump()
	denied.refused("refused by policy: mail_send", "draft", "send", draft)
	denied.assertTables(before)
}

// A draft send that waits for approval stores the draft fields that Jev checked: the approval list and page show its
// recipients, bcc marked as hidden, its subject, and its HTML body as source text. The approved run sends the draft
// as it is then, here after an update of its subject. An approved send whose draft is gone fails with a safe code and
// sends nothing.
func TestApprovedDraftSendShowsTheCheckedDraftAndSendsItOnce(t *testing.T) {
	t.Parallel()
	s := start(t, "[policy]\nmail_send='approve'\n")
	id := decode[newDraft](t, s.ok("draft", "new", "--to", "ana@example.test", "--cc", "cy@example.test", "--bcc", "ed@example.test", "--subject", "canary-subject-checked", "--body", "<b>canary-body-checked</b>", "--html")).ID
	p := s.held("draft", "send", id)
	if got := s.column(`SELECT folder FROM messages WHERE id = ?`, id); !reflect.DeepEqual(got, []string{"drafts"}) {
		t.Fatalf("pending send ran: %v", got)
	}
	_, list := s.consoleGet(consolePassword, "/")
	if !strings.Contains(list, "ana@example.test; cc: cy@example.test; bcc (hidden): ed@example.test") || !strings.Contains(list, "canary-subject-checked") {
		t.Fatalf("list misses the draft fields:\n%s", list)
	}
	_, item := s.consoleGet(consolePassword, "/a/"+p.ApprovalID)
	for _, want := range []string{"<dt>id</dt><dd><pre>" + id + "</pre></dd>", "<h2>Draft</h2>", "<dt>to</dt><dd><pre>ana@example.test</pre></dd>", "<dt>cc</dt><dd><pre>cy@example.test</pre></dd>",
		"<dt>bcc (hidden recipients)</dt><dd><pre>ed@example.test</pre></dd>", "<dt>subject</dt><dd><pre>canary-subject-checked</pre></dd>",
		"<dt>body (HTML source, not rendered)</dt><dd><pre>&lt;b&gt;canary-body-checked&lt;/b&gt;</pre></dd>"} {
		if !strings.Contains(item, want) {
			t.Errorf("item page misses %q:\n%s", want, item)
		}
	}
	s.ok("draft", "update", id, "--subject", "canary-subject-changed")
	s.approve(p.ApprovalID)
	if got := s.column(`SELECT folder || ' ' || is_draft || ' ' || subject FROM messages WHERE id = ?`, id); !reflect.DeepEqual(got, []string{"sent 0 canary-subject-changed"}) {
		t.Fatalf("approved send: %v", got)
	}
	status := decode[struct {
		State  string
		Result map[string]any
	}](t, s.ok("approvals", "status", p.ApprovalID))
	if status.State != "done" || !reflect.DeepEqual(status.Result, map[string]any{"ok": true}) {
		t.Fatalf("approved draft send: %+v", status)
	}

	gone := decode[newDraft](t, s.ok("draft", "new", "--to", "ana@example.test", "--subject", "canary-subject-gone")).ID
	p = s.held("draft", "send", gone)
	s.ok("draft", "delete", gone)
	sent := s.column(`SELECT id FROM messages WHERE folder = 'sent'`)
	s.approve(p.ApprovalID)
	if state := s.state(p.ApprovalID); state != "failed" {
		t.Fatalf("approved send of a deleted draft: %s", state)
	}
	if got := s.column(`SELECT id FROM messages WHERE folder = 'sent'`); !reflect.DeepEqual(got, sent) {
		t.Fatalf("approved send of a deleted draft sent %v", got)
	}
	if _, item := s.consoleGet(consolePassword, "/a/"+p.ApprovalID); !strings.Contains(item, "<dt>Error</dt><dd><pre>MailboxError</pre></dd>") {
		t.Fatalf("failed item page:\n%s", item)
	}
}

// A reply or forward of a sealed or error message is refused even when the policy allows drafts and sends.
func TestRepliesAndForwardsOfASealedMessageAreAlwaysRefused(t *testing.T) {
	t.Parallel()
	s := start(t, "[policy]\nmail_send='allow'\nmail_draft='allow'\n")
	before := s.dump()
	want := []map[string]any{}
	for _, id := range []string{"code", "broken"} {
		for _, tc := range []struct {
			op, key string
			args    []string
		}{
			{"mail.reply", "mail_send", []string{"mail", "reply", id, "--body", "canary-body-reply"}},
			{"mail.forward", "mail_send", []string{"mail", "forward", id, "--to", "ana@example.test"}},
			{"draft.reply", "mail_draft", []string{"draft", "reply", id, "--body", "canary-body-reply"}},
			{"draft.forward", "mail_draft", []string{"draft", "forward", id, "--to", "ana@example.test"}},
		} {
			s.refused("refused by policy: "+tc.key, tc.args...)
			want = append(want, map[string]any{"event": "policy", "account": "demo@fake.invalid", "operation": tc.op, "key": tc.key, "decision": "deny", "reasons": []any{"policy", "sealed"}, "id": id})
		}
	}
	// A refused write changes no row at the provider: no sent mail and no draft.
	s.assertTables(before)
	if got := s.auditEvents("policy"); !reflect.DeepEqual(got, want) {
		t.Fatalf("policy records=%v\nwant %v", got, want)
	}
}

// Only replies, forwards, and trash of a sealed message have a sealed check; the other mail writes act on it as on any message.
func TestSealedMailCanStillBeOrganizedByHand(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	want := s.dump()
	for _, message := range want["messages"] {
		if message["id"] == "code" {
			message["folder"], message["is_read"], message["flag"], message["categories"] = "archive", int64(1), "flagged", `["Sealed"]`
		}
	}
	for _, args := range [][]string{
		{"mail", "unread", "code"}, {"mail", "read", "code"}, {"mail", "flag", "code", "--state", "flagged"},
		{"mail", "categorize", "code", "--add", "Sealed"}, {"mail", "move", "projects", "code"}, {"mail", "archive", "code"},
	} {
		if got := decode[[]result](t, s.ok(args...)); len(got) != 1 || !got[0].OK {
			t.Fatalf("amg %v: %+v", args, got)
		}
	}
	s.assertTables(want)
}

// result is one entry of a mail mutation result.
type result struct {
	ID         string `json:"id"`
	OK         bool   `json:"ok"`
	Error      string `json:"error"`
	ApprovalID string `json:"approval_id"`
}

func TestTrashArchiveAndMoveNeverDeleteMail(t *testing.T) {
	t.Parallel()
	s := start(t, "[policy.sealed]\nmail_trash='approve'\n")
	// At the end, only the folders of these messages have changed. No message is deleted, and no other
	// column or row changes: not read state, flag, categories, subject, or body.
	want := s.dump()
	moved := map[string]string{"lunch": "trash", "invoice": "archive", "kickoff": "projects", "code": "trash"}
	for _, message := range want["messages"] {
		if folder, ok := moved[message["id"].(string)]; ok {
			message["folder"] = folder
		}
	}
	for _, args := range [][]string{{"mail", "trash", "lunch"}, {"mail", "archive", "invoice"}, {"mail", "move", "projects", "kickoff"}} {
		if got := decode[[]result](t, s.ok(args...)); len(got) != 1 || !got[0].OK {
			t.Fatalf("amg %v: %+v", args, got)
		}
	}
	// Only mail trash puts mail in the trash, so the write policy decides every trash.
	for _, folder := range []string{"trash", "old-trash"} {
		s.refused("move refused: use mail.trash", "mail", "move", folder, "phish")
	}
	// A sealed message needs approval to go to the trash.
	got := decode[[]result](t, s.ok("mail", "trash", "code"))
	if len(got) != 1 || got[0].OK || got[0].Error != "pending" || got[0].ApprovalID == "" || s.value("folder", "code") != "inbox" {
		t.Fatalf("sealed trash: %+v", got)
	}
	s.approve(got[0].ApprovalID)
	s.assertTables(want)
}

func TestOrganizePreviewWritesNothingAndApplyRunsOnce(t *testing.T) {
	t.Parallel()
	s := start(t, "")
	rules := "[accounts.\"demo@fake.invalid\".projects]\nfolder = 'projects'\ninstructions = 'canary-rule: mail about projects.'\n[accounts.\"demo@fake.invalid\".invoices]\ncategory = 'Invoices'\ninstructions = 'canary-rule: mail about invoices.'\n"
	if err := os.WriteFile(filepath.Join(s.home, "rules.toml"), []byte(rules), 0600); err != nil {
		t.Fatal(err)
	}
	// A hand edit takes effect at the reload.
	if got := s.reload(); got != "reload ok" {
		t.Fatalf("reload: %s\n%s", got, s.logText())
	}
	want := s.dump()
	if preview := s.ok("--timeout", "5m", "mail", "organize", "--since", "1d"); !strings.Contains(preview, `"rule": "projects"`) {
		t.Fatalf("preview: %s", preview)
	}
	// A preview changes no row at the provider: no folder, category, read state, or flag.
	s.assertTables(want)

	// Apply moves kickoff by its folder rule, gives invoice its rule category, and tags the red and the sealed
	// message. No other row or column changes; the message whose screening failed stays as it was.
	s.ok("--timeout", "5m", "mail", "organize", "--since", "1d", "--apply")
	for _, message := range want["messages"] {
		switch message["id"] {
		case "kickoff":
			message["folder"] = "projects"
		case "invoice":
			message["categories"] = `["Invoices"]`
		case "phish":
			message["categories"] = `["Red"]`
		case "code":
			message["categories"] = `["Sealed"]`
		}
	}
	s.assertTables(want)
	if again := s.ok("--timeout", "5m", "mail", "organize", "--since", "1d", "--apply"); !strings.Contains(again, `"planned": []`) {
		t.Fatalf("second apply: %s", again)
	}
	s.assertTables(want)
}

func TestMeetingsFromAvailabilityToCancel(t *testing.T) {
	t.Parallel()
	s := start(t, "[policy]\ncalendar_invite='approve'\ncalendar_respond='allow'\ncalendar_cancel='allow'\n")
	day := time.Now().UTC().AddDate(0, 0, 2).Format("2006-01-02")
	s.exec(`INSERT INTO free_busy (account, address, start_at, end_at, status) VALUES ('demo@fake.invalid', 'ana@example.test', ?, ?, 'busy')`, day+"T09:00:00Z", day+"T09:30:00Z")
	s.exec(`INSERT INTO events (account, id, subject, start_at, end_at, organizer_name, organizer_address) VALUES ('demo@fake.invalid', 'review', 'canary-subject-review', ?, ?, 'canary-name-boss', 'boss@example.test')`, day+"T14:00:00Z", day+"T15:00:00Z")
	s.exec(`INSERT INTO attendees (account, event_id, address, name, response) VALUES ('demo@fake.invalid', 'review', 'boss@example.test', 'canary-name-boss', 'organizer'), ('demo@fake.invalid', 'review', 'demo@fake.invalid', 'canary-name-me', 'none')`)

	availability := decode[struct {
		Schedules []struct {
			Address string
			Slots   []string
		}
	}](t, s.ok("calendar", "availability", "--to", "ana@example.test", "--to", "bob@example.test", "--start", day+" 09:00", "--end", day+" 10:00"))
	if fmt.Sprint(availability.Schedules) != "[{ana@example.test [busy free]} {bob@example.test [unknown unknown]}]" {
		t.Fatalf("availability: %+v", availability.Schedules)
	}

	invite := s.held("calendar", "invite", "--to", "ana@example.test", "--subject", "canary-subject-meeting", "--start", day+" 09:30", "--duration", "30m", "--no-online")
	if got := s.column(`SELECT id FROM events WHERE subject = 'canary-subject-meeting'`); len(got) != 0 {
		t.Fatalf("pending invite ran: %v", got)
	}
	s.approve(invite.ApprovalID)
	ids := s.column(`SELECT id FROM events WHERE subject = 'canary-subject-meeting' AND start_at = ?`, day+"T09:30:00Z")
	if len(ids) != 1 || !reflect.DeepEqual(s.column(`SELECT address FROM attendees WHERE event_id = ?`, ids[0]), []string{"ana@example.test"}) {
		t.Fatalf("approved invite: events %v", ids)
	}

	s.ok("calendar", "respond", "review", "--response", "accept", "--comment", "canary-comment-respond")
	if got := s.column(`SELECT response FROM attendees WHERE event_id = 'review' AND address = 'demo@fake.invalid'`); !reflect.DeepEqual(got, []string{"accepted"}) {
		t.Fatalf("response: %v", got)
	}

	s.ok("calendar", "cancel", ids[0], "--comment", "canary-comment-cancel")
	if got := s.column(`SELECT is_cancelled || ' ' || cancel_comment FROM events WHERE id = ?`, ids[0]); !reflect.DeepEqual(got, []string{"1 canary-comment-cancel"}) {
		t.Fatalf("cancel: %v", got)
	}
}

// calendar invite and calendar update take an HTML body with --html, and the event keeps it as is; without --html
// the body is escaped plain text. --html without --body is invalid usage and changes nothing.
func TestMeetingBodiesAreHTMLOnlyWithTheHTMLOption(t *testing.T) {
	t.Parallel()
	s := start(t, "[policy]\ncalendar_invite='allow'\ncalendar_update='allow'\n")
	id := decode[struct{ ID string }](t, s.ok("calendar", "invite", "--to", "ana@example.test", "--subject", "canary-subject-html-meeting", "--body", "<p>canary-body-agenda</p>", "--html",
		"--start", "2030-01-02 09:00", "--duration", "30m", "--no-online")).ID
	body := func(want string) {
		t.Helper()
		if got := s.column(`SELECT content_type || ' ' || body FROM events WHERE id = ?`, id); !reflect.DeepEqual(got, []string{want}) {
			t.Fatalf("event body %q, want %q", got, want)
		}
	}
	body("html <p>canary-body-agenda</p>")
	s.ok("calendar", "update", id, "--body", "<b>canary-body-update</b>\nnext", "--html")
	body("html <b>canary-body-update</b>\nnext")
	s.ok("calendar", "update", id, "--body", "<b>canary-body-plain</b>\nnext")
	body("html &lt;b&gt;canary-body-plain&lt;/b&gt;<br>next")

	before := s.dump()
	if out, errout, code := s.amg("calendar", "update", id, "--subject", "canary-subject-no-body", "--html"); code != 2 || out != "" || errout == "" {
		t.Fatalf("--html without --body: exit %d stdout %q stderr %q", code, out, errout)
	}
	s.assertTables(before)
}

// An invitation that waits for approval shows its HTML body as source text on the approval page, and the
// approved run keeps the HTML as is.
func TestApprovalPageShowsAnHTMLMeetingBodyAsSource(t *testing.T) {
	t.Parallel()
	s := start(t, "[policy]\ncalendar_invite='approve'\n")
	p := s.held("calendar", "invite", "--to", "ana@example.test", "--subject", "canary-subject-html-console", "--body", "<i>canary-body-console</i>", "--html",
		"--start", "2030-01-02 09:00", "--duration", "30m", "--no-online")
	_, item := s.consoleGet(consolePassword, "/a/"+p.ApprovalID)
	if want := "<dt>body (HTML source, not rendered)</dt><dd><pre>&lt;i&gt;canary-body-console&lt;/i&gt;</pre></dd>"; !strings.Contains(item, want) {
		t.Fatalf("item page misses %q:\n%s", want, item)
	}
	s.approve(p.ApprovalID)
	if got := s.column(`SELECT content_type || ' ' || body FROM events WHERE subject = 'canary-subject-html-console'`); !reflect.DeepEqual(got, []string{"html <i>canary-body-console</i>"}) {
		t.Fatalf("approved invite body %q", got)
	}
}

func TestPendingItemsAndVerdictsSurviveARestart(t *testing.T) {
	t.Parallel()
	s := start(t, "[policy]\nmail_send='approve'\n")
	if out := s.ok("mail", "get", "phish"); verdict(t, out) != "red" {
		t.Fatalf("phish: %s", out)
	}
	p := s.held("mail", "send", "--to", "ana@example.test", "--subject", "canary-subject-restart", "--body", "canary-body-restart")
	s.restart()
	// The cached verdict holds even if the engine would now fail.
	s.exec(`UPDATE messages SET body = body || ' SCREENFAIL' WHERE id = 'phish'`)
	if out := s.ok("mail", "get", "phish"); verdict(t, out) != "red" {
		t.Fatalf("phish after restart: %s", out)
	}
	if state := s.state(p.ApprovalID); state != "pending" {
		t.Fatalf("pending item after restart: %s", state)
	}
	s.approve(p.ApprovalID)
	if got := s.column(`SELECT subject FROM messages WHERE folder = 'sent'`); !reflect.DeepEqual(got, []string{"canary-subject-restart"}) {
		t.Fatalf("sent after restart: %v", got)
	}
}

func TestThreadShowsTheWholeConversationScreenedWithItsFolders(t *testing.T) {
	t.Parallel()
	s := start(t, "[policy]\nmail_draft='allow'\n")
	s.exec(`UPDATE messages SET thread_id = 'lunch', folder = 'trash', received_at = '2020-01-01T08:00:00Z' WHERE id = 'phish'`)
	s.exec(`UPDATE messages SET received_at = '2020-01-01T09:00:00Z' WHERE id = 'lunch'`)
	s.exec(`INSERT INTO messages (account, id, folder, sender_name, sender_address, to_addresses, subject, body, received_at, thread_id) VALUES
		('demo@fake.invalid', 'lunch-reply', 'sent', 'canary-name-me', 'demo@fake.invalid', '["ana@example.test"]', 'canary-subject-lunch-reply', 'canary-body-lunch-reply: Friday works.', '2020-01-01T10:00:00Z', 'lunch')`)
	draft := decode[struct{ ID string }](t, s.ok("draft", "reply", "lunch", "--body", "canary-body-draft: see you at noon")).ID

	type message struct {
		ID, Verdict string
		Subject     *string
		Folders     []string
	}
	subject := func(s string) *string { return &s }
	got := decode[[]message](t, s.ok("mail", "thread", "lunch"))
	want := []message{
		{draft, "green", subject("Re: canary-subject-lunch"), []string{"drafts"}},
		{"lunch-reply", "green", subject("canary-subject-lunch-reply"), []string{"sent"}},
		{"lunch", "green", subject("canary-subject-lunch"), []string{"inbox"}},
		{"phish", "red", nil, []string{"trash"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("thread=%+v", got)
	}
	s.refused("operation failed", "mail", "thread", "nowhere")
}
