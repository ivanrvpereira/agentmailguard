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
