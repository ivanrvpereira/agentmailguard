package server_test

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/server"
)

// everyWriteWaits holds every write for approval, also of sealed messages.
const everyWriteWaits = "[policy]\nmail_send='approve'\nmail_draft='approve'\nmail_trash='approve'\ncalendar_invite='approve'\ncalendar_update='approve'\ncalendar_cancel='approve'\ncalendar_respond='approve'\n[policy.sealed]\nmail_trash='approve'\n"

// waitingOps are the operations that can wait for approval, with the list group and the action label that the list
// shows, and params that the console fixture holds. Each draft operation names its own draft, so that none withdraws
// the waiting send.
var waitingOps = []struct{ op, group, label, params string }{
	{"mail.send", "messages", "Send", `{"to":["a@example.test"],"subject":"s","body":"b"}`},
	{"mail.reply", "messages", "Reply", `{"id":"g","body":"b"}`},
	{"mail.forward", "messages", "Forward", `{"id":"g","to":["a@example.test"]}`},
	{"draft.new", "messages", "New draft", `{"to":["a@example.test"],"subject":"s","body":"b"}`},
	{"draft.reply", "messages", "Draft reply", `{"id":"g","body":"b"}`},
	{"draft.forward", "messages", "Draft forward", `{"id":"g","to":["a@example.test"]}`},
	{"draft.update", "messages", "Edit draft", `{"id":"draft-2","body":"b"}`},
	{"draft.send", "messages", "Send draft", `{"id":"draft-1"}`},
	{"draft.delete", "messages", "Delete draft", `{"id":"draft-3"}`},
	{"calendar.invite", "meetings", "Invite", `{"to":["a@example.test"],"subject":"s","body":"b","start":"2026-04-01T09:00:00Z","end":"2026-04-01T10:00:00Z"}`},
	{"calendar.update", "meetings", "Update", `{"id":"o","subject":"s"}`},
	{"calendar.cancel", "meetings", "Cancel", `{"id":"o","comment":"c"}`},
	{"calendar.respond", "meetings", "Respond", `{"id":"e","response":"decline","comment":"c"}`},
	{"mail.trash", "mailbox", "Trash", `{"ids":["g"]}`},
}

// neverWaitingOps are the other API operations: reads, and writes that no policy key holds.
var neverWaitingOps = []string{
	"account.list", "account.status", "approvals.status", "calendar.availability", "calendar.get", "calendar.list", "calendar.search",
	"mail.archive", "mail.categories", "mail.categorize", "mail.flag", "mail.folders", "mail.get", "mail.link", "mail.list", "mail.move",
	"mail.organize", "mail.read", "mail.search", "mail.thread", "mail.unread", "rules.list", "stats",
}

// groupOf is the data-group of the list table that links to the pending action, or empty when no table does.
func groupOf(page, id string) string {
	at := strings.Index(page, `href="/a/`+id+`"`)
	if at < 0 {
		return ""
	}
	start := strings.LastIndex(page[:at], `data-group="`)
	if start < 0 {
		return ""
	}
	group, _, _ := strings.Cut(page[start+len(`data-group="`):], `"`)
	return group
}

// rowOf is the list row that links to the pending action, from <li to </li>, or empty.
func rowOf(page, id string) string {
	at := strings.Index(page, `href="/a/`+id+`"`)
	if at < 0 {
		return ""
	}
	start := strings.LastIndex(page[:at], "<li")
	end := strings.Index(page[at:], "</li>")
	if start < 0 || end < 0 {
		return ""
	}
	return page[start : at+end]
}

// Every operation that can wait for approval has a list group: Messages, Meetings, then Mailbox operations. An API
// operation that is in neither list fails here until it is placed, and a waiting one also needs its group.
func TestConsoleListGroupsEveryOperationThatCanWait(t *testing.T) {
	placed := slices.Clone(neverWaitingOps)
	for _, w := range waitingOps {
		placed = append(placed, w.op)
	}
	slices.Sort(placed)
	if got := server.Operations(); !slices.Equal(got, placed) {
		t.Fatalf("operations=%q\nplaced=%q", got, placed)
	}
	f := consoleDaemon(t, everyWriteWaits, true)
	ids := map[string]string{}
	for _, w := range waitingOps {
		ids[w.op] = pend(t, f.d.s, w.op, w.params)[0]
	}
	page := f.get(t, "/", nil).Body.String()
	for _, w := range waitingOps {
		if got := groupOf(page, ids[w.op]); got != w.group {
			t.Errorf("%s: group %q; want %q", w.op, got, w.group)
		}
		if row := rowOf(page, ids[w.op]); !strings.Contains(row, `<span class="op">`+w.label+`</span>`) {
			t.Errorf("%s: row misses the label %q: %s", w.op, w.label, row)
		}
	}
	messages, meetings, mailbox := strings.Index(page, `data-group="messages"`), strings.Index(page, `data-group="meetings"`), strings.Index(page, `data-group="mailbox"`)
	if messages < 0 || messages > meetings || meetings > mailbox {
		t.Fatalf("table order: messages=%d meetings=%d mailbox=%d", messages, meetings, mailbox)
	}
	for _, title := range []string{">Messages <", ">Meetings <", ">Mailbox operations <"} {
		if !strings.Contains(page, title) {
			t.Errorf("page misses the title %q", title)
		}
	}
}

// setScore makes the fake Jev answer each action check with score; a value outside 0..1 makes the check fail.
func (f *consoleFixture) setScore(score float64) {
	f.api.mu.Lock()
	f.api.actionScore = score
	f.api.mu.Unlock()
}

// Waiting actions are newest first in their table, with the age, the alias, and the account key in its title. A
// handled one moves under Recents.
func TestConsoleListShowsWaitingActionsNewestFirstThenRecents(t *testing.T) {
	f := consoleDaemon(t, draftApprove, true)
	add := func(name string) string {
		t.Helper()
		id := pend(t, f.d.s, "draft.new", `{"to":["`+name+`@example.test"],"subject":"`+name+`-subject","body":"`+name+`-body"}`)[0]
		f.advance(time.Minute)
		return id
	}
	older, denied, newer := add("older"), add("denied"), add("newer")
	if err := f.d.s.Deny(context.Background(), denied); err != nil {
		t.Fatal(err)
	}
	w := f.get(t, "/", nil)
	page := w.Body.String()
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("%d %s", w.Code, page)
	}
	recents := strings.Index(page, `<details class="recent">`)
	positions := []int{}
	for _, id := range []string{newer, older, denied} {
		positions = append(positions, strings.Index(page, `href="/a/`+id+`"`))
	}
	if positions[0] < 0 || positions[0] > positions[1] || positions[1] > recents || recents > positions[2] {
		t.Fatalf("order newer, older, Recents, denied: positions=%v recents=%d page=%s", positions, recents, page)
	}
	for id, want := range map[string]string{newer: ">1m<", older: ">3m<", denied: ">0s<"} {
		if row := rowOf(page, id); !strings.Contains(row, want) || !strings.Contains(row, `<span class="acct" title="`+workEmail+`">work</span>`) {
			t.Errorf("row misses %q or the account: %s", want, row)
		}
	}
	if row := rowOf(page, denied); !strings.Contains(row, "denied@example.test · denied-subject") || !strings.Contains(row, ">Denied<") {
		t.Errorf("Recents row misses the summary or the outcome: %s", row)
	}
	onlyConsoleLinks(t, page)
}

// A Messages row shows the recipients as today (reply marker, cc, bcc marked hidden), long addresses cut in the middle
// of the local part with the full text in the title, the subject, and a one-line preview of the body. A draft send
// shows the stored draft fields.
func TestConsoleListShowsMessageRows(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	long := pend(t, f.d.s, "mail.send", `{"to":["accounts-payable-notifications@vendor-billing.example.com","b@example.test"],"subject":"Invoice","body":"\n\n  First   line\nsecond line"}`)[0]
	all := pend(t, f.d.s, "draft.reply", `{"id":"g","body":"b","all":true,"cc":["c@example.test"],"bcc":["d@example.test"]}`)[0]
	reply := pend(t, f.d.s, "mail.reply", `{"id":"g","body":"b"}`)[0]
	empty := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"","body":"b"}`)[0]
	send := pend(t, f.d.s, "draft.send", `{"id":"draft-1"}`)[0]
	page := f.get(t, "/", nil).Body.String()
	for id, wants := range map[string][]string{
		long: {`<span class="to" title="accounts-payable-notifications@vendor-billing.example.com, b@example.test">accounts…@vendor-billing.example.com, b@example.test</span>`,
			`<span class="subj">Invoice</span><span class="snip">First line</span>`},
		all:   {`<span class="to" title="reply all; cc: c@example.test; bcc (hidden): d@example.test"><span class="ph">reply all</span>; cc: c@example.test; bcc (hidden): d@example.test</span>`},
		reply: {`<span class="to" title="reply"><span class="ph">reply</span></span>`},
		empty: {`<span class="subj ph">No subject</span>`},
		send:  {`<span class="op">Send draft</span>`, `<span class="subj">private-subject-draft-1</span><span class="snip">private-body-draft-1 https://example.test/report</span>`},
	} {
		row := rowOf(page, id)
		for _, want := range wants {
			if !strings.Contains(row, want) {
				t.Errorf("row misses %q: %s", want, row)
			}
		}
	}
}

// The preview of an HTML body is its text: tags and comments gone, the content of style, script, and head dropped,
// entities decoded, and whitespace collapsed. The template escapes it like any text.
func TestConsoleListPreviewsTheTextOfAnHTMLBody(t *testing.T) {
	f := consoleDaemon(t, draftApprove, true)
	body := `<!DOCTYPE html><HTML><Head><title>CANARY-title</title><style>p{color:red} CANARY-style</style></head><body><!-- CANARY-comment --><p>Hello   team,</p>` +
		`<SCRIPT type='x'>CANARY-script</script ><p title="a>b">Shipped the <b>console</b> &amp; fixed &lt;bugs&gt;.</p></body></html>`
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	id := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","html":true,"body":`+string(b)+`}`)[0]
	row := rowOf(f.get(t, "/", nil).Body.String(), id)
	summary, _, _ := strings.Cut(row, "</summary>")
	if want := `<span class="snip">Hello team, Shipped the console &amp; fixed &lt;bugs&gt;.</span>`; !strings.Contains(summary, want) {
		t.Fatalf("summary misses %q: %s", want, summary)
	}
	if strings.Contains(summary, "CANARY") || strings.Contains(summary, "DOCTYPE") {
		t.Fatalf("preview keeps markup or dropped content: %s", summary)
	}
}

// A Meetings row shows the event, when, the people, and the change: an invitation from the stored request, the
// others from the request and the live read of the event.
func TestConsoleListShowsMeetingRows(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	invite := pend(t, f.d.s, "calendar.invite", `{"to":["required@example.test"],"optional":["optional@example.test"],"subject":"Budget sync","body":"Agenda","location":"Room 2","start":"2026-04-01T09:00:00Z","end":"2026-04-01T10:00:00Z"}`)[0]
	respond := pend(t, f.d.s, "calendar.respond", `{"id":"e","response":"decline","comment":"Travelling"}`)[0]
	cancel := pend(t, f.d.s, "calendar.cancel", `{"id":"o","comment":"No sync this week"}`)[0]
	update := pend(t, f.d.s, "calendar.update", `{"id":"o","start":"2026-04-01T11:00:00Z","add":["new@example.test"]}`)[0]
	trash := pend(t, f.d.s, "mail.trash", `{"ids":["g"]}`)[0]
	page := f.get(t, "/", nil).Body.String()
	for id, wants := range map[string][]string{
		invite:  {`<span class="ev">Budget sync</span>`, `<span class="when num">Wed 1 Apr, 09:00–10:00</span>`, `<span class="ppl">2 people</span>`, `<span class="k">New event</span><span class="snip">Room 2</span>`},
		respond: {`<span class="ev">private-subject-event</span>`, `<span class="ppl ph">Organizer</span>`, `<span class="k">Decline</span><span class="snip">Travelling</span>`},
		cancel:  {`<span class="ev">private-subject-event</span>`, `<span class="ppl">0 people</span>`, `<span class="k">Cancel event</span><span class="snip">No sync this week</span>`},
		update:  {`<span class="k">Start, &#43;1 person</span><span class="snip">Tue 31 Mar, 10:00 → Wed 1 Apr, 11:00</span>`},
		trash:   {`<span class="op">Trash</span>`, `<span class="from">sender@example.test</span>`, `<span class="recv num">Mon 30 Mar</span>`},
	} {
		row := rowOf(page, id)
		for _, want := range wants {
			if !strings.Contains(row, want) {
				t.Errorf("row misses %q: %s", want, row)
			}
		}
	}
}

// pageText is the text of a page: tags removed and whitespace collapsed.
func pageText(page string) string {
	return strings.Join(strings.Fields(tags.ReplaceAllString(page, "")), " ")
}

// neverOnPage are words of the old vocabulary and of the screening service that no console page shows.
var neverOnPage = regexp.MustCompile(`(?i)jev|flagged`)

// oldWord is the first word of neverOnPage on a page, or empty. The value of the "I reviewed it" box is a form
// field that the page does not show.
func oldWord(page string) string {
	return neverOnPage.FindString(strings.ReplaceAll(page, `name="confirm" value="flagged"`, ""))
}

// The Score is the action check: green with the value when checked, red with ▲ and Needs review for a risk or a
// failed check (its fixed code in the title), grey — when not checked. A Mailbox operations row of a message without a
// screening verdict is grey. The open row shows the value with its label.
func TestConsoleListShowsTheScoreOfTheActionCheck(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	f.setScore(0.06)
	checked := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	f.setScore(0.499)
	below := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	f.setScore(0.999)
	high := pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	f.setScore(0.58)
	risk := pend(t, f.d.s, "calendar.respond", `{"id":"e","response":"decline","comment":"c"}`)[0]
	f.setScore(2)
	failed := pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	unchecked := pend(t, f.d.s, "draft.delete", `{"id":"draft-1"}`)[0]
	trash := pend(t, f.d.s, "mail.trash", `{"ids":["gone"]}`)[0]
	page := f.get(t, "/", nil).Body.String()
	for id, wants := range map[string][]string{
		checked:   {`<li class="row ok">`, `<span class="score">0.06<span class="vh"> · Checked</span></span>`, `<dd>0.06 · Checked</dd>`},
		risk:      {`<li class="row risky">`, `<span class="score bad"><span class="mk" aria-hidden="true">▲</span>0.58<span class="vh"> · Needs review</span></span>`, `<dd class="bad">▲ 0.58 · Needs review</dd>`},
		failed:    {`<li class="row risky">`, `<span class="score bad" title="ValidationError"><span class="mk" aria-hidden="true">▲</span>error<span class="vh"> · Needs review</span></span>`, `<dd class="bad">▲ error · Needs review · ValidationError</dd>`},
		below:     {`<li class="row ok">`, `<span class="score">0.49<span class="vh"> · Checked</span></span>`, `<dd>0.49 · Checked</dd>`},
		high:      {`<li class="row risky">`, `<dd class="bad">▲ 0.99 · Needs review</dd>`},
		unchecked: {`<li class="row none">`, `<span class="score">—<span class="vh"> · Not checked</span></span>`, `<dd>— · Not checked</dd>`},
		trash:     {`<li class="row none">`, `<span class="score">—<span class="vh"> · Unknown</span></span>`},
	} {
		row := rowOf(page, id)
		for _, want := range wants {
			if !strings.Contains(row, want) {
				t.Errorf("row misses %q: %s", want, row)
			}
		}
	}
	if !strings.Contains(page, `<span class="sum num">7 waiting · <b>3 need review</b></span>`) {
		t.Errorf("header misses the counts: %s", page)
	}
	if want := "Red: needs review · Grey: not checked · Green: checked"; !strings.Contains(pageText(page), want) {
		t.Errorf("legend misses %q: %s", want, pageText(page))
	}
	if m := oldWord(page); m != "" {
		t.Errorf("page shows %q: %s", m, page)
	}
}

// A check by the fake engine carries a fake tag before the value, in the row and in the open row.
func TestConsoleListTagsTheFakeCheck(t *testing.T) {
	f := fakeConsole(t, fakeHome(t), fakeScreen+draftApproveOnly)
	clear := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"See you at noon"}`)[0]
	risk := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"Your new password is x"}`)[0]
	page := f.get(t, "/", nil).Body.String()
	for id, wants := range map[string][]string{
		clear: {`<span class="score"><span class="fake">fake</span>0.05<span class="vh">`, `<dd><span class="fake">fake</span>0.05 · Checked</dd>`},
		risk:  {`<span class="score bad"><span class="fake">fake</span><span class="mk" aria-hidden="true">▲</span>0.90<span class="vh">`, `<dd class="bad"><span class="fake">fake</span>▲ 0.90 · Needs review</dd>`},
	} {
		row := rowOf(page, id)
		for _, want := range wants {
			if !strings.Contains(row, want) {
				t.Errorf("row misses %q: %s", want, row)
			}
		}
	}
	if m := oldWord(page); m != "" {
		t.Errorf("page shows %q: %s", m, page)
	}
}

// filterCounts are the label and count of each filter link of a page, in page order.
var filterCount = regexp.MustCompile(`<a href="([^"]*)"(?: title="[^"]*")?( aria-current="true")?>([^<]*)<span class="c num">(\d+)</span></a>`)

func filterCounts(page string) []string {
	out := []string{}
	for _, m := range filterCount.FindAllStringSubmatch(page, -1) {
		current := ""
		if m[2] != "" {
			current = "*"
		}
		out = append(out, current+m[3]+" "+m[4]+" "+m[1])
	}
	return out
}

// The Account (accounts in alias order) and Show filters combine; each option counts the waiting actions that it would show, under the other
// filter. The header counts the waiting actions shown, and Needs review only Messages and Meetings. Recents follow
// the Account filter, not the Show filter. A table with nothing to show is hidden.
func TestConsoleListFiltersByAccountAndReview(t *testing.T) {
	f := gmailConsole(t, everyWriteWaits)
	draft := func(alias string, score float64) string {
		t.Helper()
		f.setScore(score)
		return pendOn(t, f.d.s, "draft.new", alias, `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	}
	workRisk, workClear, gmailClear, gmailRisk := draft("work", .62), draft("work", .1), draft("gmail", .1), draft("gmail", .62)
	trash := pend(t, f.d.s, "mail.trash", `{"ids":["g"]}`)[0]
	gmailDenied := draft("gmail", .1)
	if err := f.d.s.Deny(context.Background(), gmailDenied); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query, header string
		counts        []string
		shown, hidden []string
	}{
		{"", "5 waiting · <b>2 need review</b>",
			[]string{"*All 5 /", "gmail 2 /?account=gmail", "work 3 /?account=work", "*All 5 /", "Needs review 2 /?status=review", "Others 3 /?status=others"},
			[]string{workRisk, workClear, gmailClear, gmailRisk, trash, gmailDenied}, nil},
		{"?account=gmail", "2 waiting · <b>1 need review</b>",
			[]string{"All 5 /", "*gmail 2 /?account=gmail", "work 3 /?account=work", "*All 2 /?account=gmail", "Needs review 1 /?account=gmail&amp;status=review", "Others 1 /?account=gmail&amp;status=others"},
			[]string{gmailClear, gmailRisk, gmailDenied}, []string{workRisk, workClear, trash}},
		{"?status=review", "2 waiting · <b>2 need review</b>",
			[]string{"*All 2 /?status=review", "gmail 1 /?account=gmail&amp;status=review", "work 1 /?account=work&amp;status=review", "All 5 /", "*Needs review 2 /?status=review", "Others 3 /?status=others"},
			[]string{workRisk, gmailRisk, gmailDenied}, []string{workClear, gmailClear, trash}},
		{"?account=work&status=others", "2 waiting · 0 need review",
			[]string{"All 3 /?status=others", "gmail 1 /?account=gmail&amp;status=others", "*work 2 /?account=work&amp;status=others", "All 3 /?account=work", "Needs review 1 /?account=work&amp;status=review", "*Others 2 /?account=work&amp;status=others"},
			[]string{workClear, trash}, []string{workRisk, gmailClear, gmailRisk, gmailDenied}},
	} {
		w := f.get(t, "/"+tc.query, nil)
		page := w.Body.String()
		if w.Code != 200 || !strings.Contains(page, `<span class="sum num">`+tc.header+`</span>`) {
			t.Errorf("%q: %d header misses %q: %s", tc.query, w.Code, tc.header, page)
		}
		if got := filterCounts(page); !slices.Equal(got, tc.counts) {
			t.Errorf("%q: filters=%q\nwant %q", tc.query, got, tc.counts)
		}
		for _, id := range tc.shown {
			if groupOf(page, id) == "" {
				t.Errorf("%q: %s not shown", tc.query, id)
			}
		}
		for _, id := range tc.hidden {
			if strings.Contains(page, id) {
				t.Errorf("%q: %s shown", tc.query, id)
			}
		}
		if hasMailbox := strings.Contains(page, `data-group="mailbox"`); hasMailbox != slices.Contains(tc.shown, trash) {
			t.Errorf("%q: Mailbox operations table shown=%v", tc.query, hasMailbox)
		}
		onlyConsoleLinks(t, page)
	}
	if row := rowOf(f.get(t, "/", nil).Body.String(), gmailClear); !strings.Contains(row, `<span class="acct" title="`+gmailEmail+`">gmail</span>`) {
		t.Errorf("row misses the alias: %s", row)
	}
}

// A filter that is not valid, in any way, gives the unfiltered page; the page never repeats the input.
func TestConsoleListIgnoresAnInvalidFilter(t *testing.T) {
	f := gmailConsole(t, everyWriteWaits)
	f.setScore(.62)
	pendOn(t, f.d.s, "draft.new", "gmail", `{"to":["a@example.test"],"subject":"s","body":"b"}`)
	f.setScore(.1)
	pendOn(t, f.d.s, "draft.new", "work", `{"to":["a@example.test"],"subject":"s","body":"b"}`)
	for _, query := range []string{
		"?account=CANARY", "?account=Gmail", "?status=CANARY", "?status=Review", "?CANARY=1", "?account=gmail&CANARY=1",
		"?status=review&status=others", "?account=gmail&account=work", "?account=%zzCANARY", "?account=%3Cscript%3ECANARY",
		"?status=review;account=gmail", "?select=messages", "?account=", "?status=",
	} {
		w := f.get(t, "/"+query, nil)
		page := w.Body.String()
		if w.Code != 200 || !strings.Contains(page, `<span class="sum num">2 waiting · <b>1 need review</b></span>`) || strings.Contains(page, "CANARY") {
			t.Errorf("%q: %d %s", query, w.Code, page)
		}
		if !slices.Contains(filterCounts(page), "*All 2 /") {
			t.Errorf("%q: filters=%q", query, filterCounts(page))
		}
	}
}

// recentLinks are the action pages that the Recents of a table link to, in page order.
func recentLinks(page, group string) []string {
	start := strings.Index(page, `data-group="`+group+`"`)
	if start < 0 {
		return nil
	}
	section, _, _ := strings.Cut(page[start:], "</section>")
	_, recents, ok := strings.Cut(section, `<details class="recent">`)
	if !ok {
		return nil
	}
	out := []string{}
	for _, m := range regexp.MustCompile(`<a class="line" href="/a/([0-9a-f]{32})">`).FindAllStringSubmatch(recents, -1) {
		out = append(out, m[1])
	}
	return out
}

// Each table ends with a closed Recents list of its own 10 most recently handled actions, newest first, each linking
// to its action page with its outcome: done, denied, failed with its code, or withdrawn.
func TestConsoleListShowsTheLastTenHandledActionsOfEachTable(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	var denied []string
	for range 11 {
		id := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
		f.advance(time.Minute)
		if err := f.d.s.Deny(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		denied = append(denied, id)
	}
	done := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	f.advance(time.Minute)
	if err := f.d.s.Approve(context.Background(), done); err != nil {
		t.Fatal(err)
	}
	f.advance(time.Minute)
	withdrawn := pend(t, f.d.s, "draft.send", `{"id":"draft-5"}`)[0]
	pend(t, f.d.s, "draft.delete", `{"id":"draft-5"}`)
	f.advance(time.Minute)
	failed := pend(t, f.d.s, "mail.trash", `{"ids":["gone"]}`)[0]
	if err := f.d.s.Approve(context.Background(), failed); err == nil {
		t.Fatal("trash of a missing message ran")
	}
	page := f.get(t, "/", nil).Body.String()
	want := append([]string{withdrawn, done}, reversed(denied)[:8]...)
	if got := recentLinks(page, "messages"); !slices.Equal(got, want) {
		t.Errorf("Messages Recents=%v\nwant %v", got, want)
	}
	if got := recentLinks(page, "mailbox"); !slices.Equal(got, []string{failed}) {
		t.Errorf("Mailbox operations Recents=%v", got)
	}
	if recentLinks(page, "meetings") != nil || strings.Contains(page, `data-group="meetings"`) {
		t.Errorf("empty Meetings table shown: %s", page)
	}
	if !strings.Contains(page, `<summary>Recents <span class="n num">10</span></summary>`) || strings.Contains(page, `<details class="recent" open`) {
		t.Errorf("Messages Recents not closed with its count: %s", page)
	}
	for id, outcome := range map[string]string{withdrawn: `"st">Withdrawn<`, done: `"st">Done<`, denied[10]: `"st">Denied<`, failed: `"st failed">Failed: Graph error 404<`} {
		if row := rowOf(page, id); !strings.Contains(row, outcome) {
			t.Errorf("Recents row misses %q: %s", outcome, row)
		}
	}
}

// Under the Account filter, Recents are the account's own last handled actions, even when other accounts handled
// more actions since.
func TestConsoleListShowsTheRecentsOfTheFilteredAccount(t *testing.T) {
	f := gmailConsole(t, everyWriteWaits)
	deny := func(alias string) string {
		t.Helper()
		id := pendOn(t, f.d.s, "draft.new", alias, `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
		f.advance(time.Minute)
		if err := f.d.s.Deny(context.Background(), id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	var gmail []string
	for range 3 {
		gmail = append(gmail, deny("gmail"))
	}
	for range 10 {
		deny("work")
	}
	page := f.get(t, "/?account=gmail", nil).Body.String()
	if got := recentLinks(page, "messages"); !slices.Equal(got, reversed(gmail)) {
		t.Fatalf("gmail Recents=%v\nwant %v", got, reversed(gmail))
	}
}

func reversed(ids []string) []string {
	out := slices.Clone(ids)
	slices.Reverse(out)
	return out
}

// Opening a row shows the account key, the score with its label, every request field with bcc marked hidden and an
// empty one as a grey dash, the full body or meeting text as plain text under a fixed caption, and a link to the
// action page.
func TestConsoleListOpenRowShowsEveryRequestFieldAndTheBody(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	send := pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"cc":[],"bcc":["d@example.test"],"subject":"Plan","body":"Hi,\n\nsecond paragraph"}`)[0]
	html := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","html":true,"body":"<p>Hi &amp; bye</p>"}`)[0]
	page := f.get(t, "/", nil).Body.String()
	row := rowOf(page, send)
	for _, want := range []string{
		"<dt>Account</dt><dd>" + workEmail + "</dd>",
		"<dt>Score</dt><dd>0.10 · Checked</dd>",
		`<dt>To</dt><dd><span class="ln">a@example.test</span></dd>`,
		`<dt>Cc</dt><dd class="ph">—</dd>`,
		`<dt>Bcc (hidden recipients)</dt><dd><span class="ln">d@example.test</span></dd>`,
		`<dt>Subject</dt><dd><span class="ln">Plan</span></dd>`,
		"<figcaption>Body, plain text as the agent wrote it</figcaption><pre>Hi,\n\nsecond paragraph</pre>",
		`<a href="/a/` + send + `">Open the action page</a>`,
	} {
		if !strings.Contains(row, want) {
			t.Errorf("open row misses %q: %s", want, row)
		}
	}
	if want := "<figcaption>Body, HTML source as the agent wrote it, not rendered</figcaption><pre>&lt;p&gt;Hi &amp;amp; bye&lt;/p&gt;</pre>"; !strings.Contains(rowOf(page, html), want) || strings.Contains(page, "<iframe") {
		t.Errorf("open row misses %q or has a frame: %s", want, rowOf(page, html))
	}
	respond := pend(t, f.d.s, "calendar.respond", `{"id":"e","response":"decline","comment":"Travelling"}`)[0]
	cancel := pend(t, f.d.s, "calendar.cancel", `{"id":"o","comment":"No sync"}`)[0]
	invite := pend(t, f.d.s, "calendar.invite", `{"to":["a@example.test"],"subject":"s","body":"Agenda","start":"2026-04-01T09:00:00Z","end":"2026-04-01T10:00:00Z"}`)[0]
	update := pend(t, f.d.s, "calendar.update", `{"id":"o","body":"<b>New</b>","html":true}`)[0]
	page = f.get(t, "/", nil).Body.String()
	for id, want := range map[string]string{
		respond: "<figcaption>Comment to the organizer, plain text as the agent wrote it</figcaption><pre>Travelling</pre>",
		cancel:  "<figcaption>Comment to the attendees, plain text as the agent wrote it</figcaption><pre>No sync</pre>",
		invite:  "<figcaption>Invitation text, plain text as the agent wrote it</figcaption><pre>Agenda</pre>",
		update:  "<figcaption>Invitation text, HTML source as the agent wrote it, not rendered</figcaption><pre>&lt;b&gt;New&lt;/b&gt;</pre>",
	} {
		if row := rowOf(page, id); !strings.Contains(row, want) || strings.Contains(row, "<dt>Comment</dt>") {
			t.Errorf("open row misses %q or lists the comment as a field: %s", want, row)
		}
	}
}

// hostile is agent text that is markup or template syntax if it is not escaped.
const hostile = `<img src=x onerror=alert(1)> {{7*7}} ${7*7}`

// Agent text renders as literal text in every column, the open row, titles, and Recents; the page has no script, no
// event handler attribute, and links only to the console.
func TestConsoleListShowsHostileAgentTextAsLiteralText(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	h, err := json.Marshal(hostile)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{
		pend(t, f.d.s, "mail.send", `{"to":[`+string(h)+`],"cc":[`+string(h)+`],"subject":`+string(h)+`,"body":`+string(h)+`}`)[0],
		pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":`+string(h)+`,"html":true,"body":`+string(h)+`}`)[0],
		pend(t, f.d.s, "calendar.invite", `{"to":[`+string(h)+`],"subject":`+string(h)+`,"body":`+string(h)+`,"location":`+string(h)+`,"start":"2026-04-01T09:00:00Z","end":"2026-04-01T10:00:00Z"}`)[0],
		pend(t, f.d.s, "calendar.respond", `{"id":"e","response":"decline","comment":`+string(h)+`}`)[0],
		pend(t, f.d.s, "calendar.update", `{"id":"o","subject":`+string(h)+`,"location":`+string(h)+`}`)[0],
	}
	recent := pend(t, f.d.s, "mail.send", `{"to":[`+string(h)+`],"subject":`+string(h)+`,"body":"b"}`)[0]
	if err := f.d.s.Deny(context.Background(), recent); err != nil {
		t.Fatal(err)
	}
	page := f.get(t, "/", nil).Body.String()
	if strings.Contains(page, "<img") {
		t.Fatalf("hostile text is markup: %s", page)
	}
	escaped := `&lt;img src=x onerror=alert(1)&gt; {{7*7}} ${7*7}`
	for _, id := range append(ids, recent) {
		row := rowOf(page, id)
		summary, open, _ := strings.Cut(row, "</summary>")
		if !strings.Contains(summary, escaped) || id != recent && !strings.Contains(open, escaped) {
			t.Errorf("row misses the literal text: %s", row)
		}
	}
	for id := range rowBoxes(page) {
		if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(id) {
			t.Errorf("checkbox value %q is not an ID", id)
		}
	}
	if !strings.Contains(page, `<span class="vh">Select Send: `+escaped+`</span>`) {
		t.Errorf("checkbox label misses the literal text")
	}
	for _, want := range []string{`<span class="to" title="` + escaped + `; cc: ` + escaped + `">`, `<span class="ev">` + escaped + `</span>`, `<span class="snip">` + escaped + `</span>`} {
		if !strings.Contains(page, want) {
			t.Errorf("page misses %q", want)
		}
	}
	onlyConsoleLinks(t, page)
}

// liveReads are the messages and events that the fake Graph API served by ID since the last reset, with the number
// of requests of each.
func (f *consoleFixture) liveReads(reset bool) map[string]int {
	f.api.mu.Lock()
	defer f.api.mu.Unlock()
	out := map[string]int{}
	for path, n := range f.api.gets {
		if id, ok := strings.CutPrefix(path, "/me/messages/"); ok {
			out["message "+id] += n
		} else if id, ok := strings.CutPrefix(path, "/me/events/"); ok {
			out["event "+id] += n
		}
	}
	if reset {
		f.api.gets = map[string]int{}
	}
	return out
}

// hang makes the fake Graph API answer the requests of path only when the client gives up.
func (f *consoleFixture) hang(path string) {
	f.api.mu.Lock()
	f.api.hang[path] = true
	f.api.mu.Unlock()
}

// The list reads the event of a meeting update, cancel, or response and the message of a trash live, through the
// guarded read; Messages rows and invitations never read. The rows show the details, never a body.
func TestConsoleListReadsOnlyMeetingAndMailboxRowsLive(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	for _, w := range waitingOps {
		if w.group == "messages" || w.op == "calendar.invite" {
			pend(t, f.d.s, w.op, w.params)
		}
	}
	f.liveReads(true)
	if page := f.get(t, "/", nil).Body.String(); !strings.Contains(page, `data-group="messages"`) {
		t.Fatalf("no Messages table: %s", page)
	}
	if reads := f.liveReads(true); len(reads) != 0 {
		t.Fatalf("Messages rows and an invitation read %v", reads)
	}
	update := pend(t, f.d.s, "calendar.update", `{"id":"o","subject":"s"}`)[0]
	cancel := pend(t, f.d.s, "calendar.cancel", `{"id":"o","comment":"c"}`)[0]
	respond := pend(t, f.d.s, "calendar.respond", `{"id":"e","response":"decline","comment":"c"}`)[0]
	trash := pend(t, f.d.s, "mail.trash", `{"ids":["y"]}`)[0]
	f.liveReads(true)
	page := f.get(t, "/", nil).Body.String()
	reads := f.liveReads(false)
	if len(reads) != 3 || reads["event o"] == 0 || reads["event e"] == 0 || reads["message y"] == 0 {
		t.Fatalf("live reads %v; want the events o and e and the message y", reads)
	}
	for id, wants := range map[string][]string{
		update:  {`<span class="ev">private-subject-event</span>`, `<span class="when num">Tue 31 Mar, 10:00–11:00</span>`, `<span class="ppl">0 people</span>`},
		cancel:  {`<span class="ev">private-subject-event</span>`, `<span class="k">Cancel event</span><span class="snip">c</span>`},
		respond: {`<span class="ev">private-subject-event</span>`, `<span class="ppl ph">Organizer</span>`, `<dt>Event</dt><dd>private-subject-event <span class="src">· from the calendar</span></dd>`, `<dt>When</dt><dd>Tue 31 Mar 2026, 10:00–11:00</dd>`},
		trash: {`<span class="text"><span class="from">sender@example.test</span><span class="subj">private-subject-y</span></span>`, `<span class="recv num">Mon 30 Mar</span>`,
			`<dt>Sender</dt><dd>private-name &lt;sender@example.test&gt;</dd>`, `<dt>Subject</dt><dd>private-subject-y</dd>`, `<dt>Received</dt><dd>Mon 30 Mar 2026, 10:00</dd>`},
	} {
		row := rowOf(page, id)
		for _, want := range wants {
			if !strings.Contains(row, want) {
				t.Errorf("row misses %q: %s", want, row)
			}
		}
	}
	if strings.Contains(page, "private-body-y") || strings.Contains(page, "private-body-event") || strings.Contains(page, "not available") {
		t.Errorf("page shows a body or a missing read: %s", page)
	}
	if text, _ := auditOf(t, f.d.home); strings.Contains(text, "private-") || strings.Contains(text, "sender@") {
		t.Errorf("audit holds read details: %s", text)
	}
}

// A red, sealed, or unscreened message shows only what its guarded view shows: the sender address at most, and
// "Subject hidden (sealed)". The Score is the message's screening verdict from the cache, and none needs review.
func TestConsoleListHidesTheSubjectOfRedAndSealedMessages(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	ids := map[string]string{}
	for _, m := range []string{"g", "y", "r", "s", "down"} {
		ids[m] = pend(t, f.d.s, "mail.trash", `{"ids":["`+m+`"]}`)[0]
	}
	page := f.get(t, "/", nil).Body.String()
	hidden := `<span class="text"><span class="from">sender@example.test</span><span class="ph">Subject hidden (sealed)</span></span>`
	for m, wants := range map[string][]string{
		"g":    {`<li class="row ok">`, `<span class="subj">private-subject-g</span>`, `<span class="score">0.10<span class="vh"> · Green</span></span>`, `<dd>0.10 · Green</dd>`},
		"y":    {`<li class="row warn">`, `<span class="score">0.35<span class="vh"> · Yellow</span></span>`, `<dd>0.35 · Yellow</dd>`},
		"r":    {`<li class="row risky">`, hidden, `<dd class="bad">▲ 0.90 · Red</dd>`, `<dt>Subject</dt><dd class="ph">Subject hidden (sealed)</dd>`, `<dt>Sender</dt><dd>sender@example.test</dd>`},
		"s":    {`<li class="row risky">`, hidden, `<span class="score bad"><span class="mk" aria-hidden="true">▲</span>sealed<span class="vh"> · Sealed</span></span>`, `<dd class="bad">▲ sealed · Sealed</dd>`},
		"down": {`<li class="row risky">`, `<span class="text"><span class="ph">Subject hidden (sealed)</span></span>`, `<dd class="bad">▲ error · Error · `},
	} {
		row := rowOf(page, ids[m])
		for _, want := range wants {
			if !strings.Contains(row, want) {
				t.Errorf("%s: row misses %q: %s", m, want, row)
			}
		}
		if m != "g" && m != "y" && (strings.Contains(row, "private-") || strings.Contains(row, "subj")) {
			t.Errorf("%s: row shows hidden text: %s", m, row)
		}
	}
	if !strings.Contains(page, `<span class="sum num">5 waiting · 0 need review</span>`) {
		t.Errorf("Mailbox operations rows need review: %s", page)
	}
	// A handled trash keeps the cached verdict of its message under Recents, without a read.
	if err := f.d.s.Deny(context.Background(), ids["r"]); err != nil {
		t.Fatal(err)
	}
	f.liveReads(true)
	page = f.get(t, "/", nil).Body.String()
	if row := rowOf(page, ids["r"]); !strings.Contains(row, `<span class="score bad"><span class="mk" aria-hidden="true">▲</span>0.90<span class="vh"> · Red</span></span>`) || !strings.Contains(row, ">Denied<") {
		t.Errorf("Recents row misses the cached verdict: %s", row)
	}
	if reads := f.liveReads(false); reads["message r"] != 0 {
		t.Errorf("Recents row read its message: %v", reads)
	}
}

// A read that fails or does not finish by the page deadline leaves its row "not available", and the page still
// renders in time. The Score of a trash comes from the cache, so it shows even when the live read fails; a message
// that was never screened is grey.
func TestConsoleListShowsRowsWhoseReadFailsOrIsLate(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	if status, body := accountRPC(t, f.d.s, "mail.get", "", `{"id":"y"}`); status != 200 {
		t.Fatalf("agent read: %d %s", status, body)
	}
	cached := pend(t, f.d.s, "mail.trash", `{"ids":["y"]}`)[0]
	gone := pend(t, f.d.s, "mail.trash", `{"ids":["gone"]}`)[0]
	slow := pend(t, f.d.s, "calendar.update", `{"id":"o","start":"2026-03-31T11:00:00Z"}`)[0]
	f.hang("/me/messages/y")
	f.hang("/me/events/o")
	deadline := 300 * time.Millisecond
	t.Cleanup(server.SetListReadDeadline(deadline))
	start := time.Now()
	w := f.get(t, "/", nil)
	if elapsed := time.Since(start); elapsed > deadline+time.Second {
		t.Fatalf("page took %v", elapsed)
	}
	page := w.Body.String()
	for id, wants := range map[string][]string{
		cached: {`<li class="row warn">`, `<span class="text"><span class="from ph">Message not available</span></span>`, `<span class="recv num">—</span>`, `<span class="score">0.35<span class="vh"> · Yellow</span></span>`},
		gone:   {`<li class="row none">`, `<span class="from ph">Message not available</span>`, `<span class="score">—<span class="vh"> · Unknown</span></span>`},
		slow:   {`<span class="ev ph">Event not available</span>`, `<span class="k">Start → Tue 31 Mar, 11:00</span>`},
	} {
		row := rowOf(page, id)
		for _, want := range wants {
			if !strings.Contains(row, want) {
				t.Errorf("row misses %q: %s", want, row)
			}
		}
	}
	if w.Code != 200 || !strings.Contains(page, `<span class="sum num">3 waiting · 0 need review</span>`) {
		t.Fatalf("%d %s", w.Code, page)
	}
}

// A page reads at most 50 events and messages, those of the rows it shows first; the other rows are not available.
// The deadline is long, so that every one of the 50 first screenings finishes.
func TestConsoleListReadsAtMostFiftyRows(t *testing.T) {
	t.Cleanup(server.SetListReadDeadline(time.Minute))
	f := consoleDaemon(t, everyWriteWaits, true)
	var ids []string
	for i := range 52 {
		ids = append(ids, pend(t, f.d.s, "mail.trash", `{"ids":["m`+strconv.Itoa(i)+`"]}`)[0])
		f.advance(time.Second)
	}
	f.liveReads(true)
	page := f.get(t, "/", nil).Body.String()
	if reads := f.liveReads(false); len(reads) != 50 || reads["message m0"] != 0 || reads["message m1"] != 0 || reads["message m51"] == 0 {
		t.Fatalf("%d reads; want the 50 newest: %v", len(reads), reads)
	}
	for i, id := range ids {
		if missing := strings.Contains(rowOf(page, id), "Message not available"); missing != (i < 2) {
			t.Errorf("m%d: not available=%v", i, missing)
		}
	}
}

// At most 4 reads of one account run at a time. The deadline is long, so that every slowed read finishes.
func TestConsoleListReadsFourMessagesOfAnAccountAtATime(t *testing.T) {
	t.Cleanup(server.SetListReadDeadline(time.Minute))
	f := consoleDaemon(t, everyWriteWaits, true)
	for i := range 12 {
		pend(t, f.d.s, "mail.trash", `{"ids":["m`+strconv.Itoa(i)+`"]}`)
	}
	f.api.mu.Lock()
	f.api.screenDelay, f.api.maxScreening = 50*time.Millisecond, 0
	f.api.mu.Unlock()
	page := f.get(t, "/", nil).Body.String()
	f.api.mu.Lock()
	most := f.api.maxScreening
	f.api.mu.Unlock()
	if most > 4 || most == 0 || strings.Contains(page, "not available") {
		t.Fatalf("%d reads at a time: %s", most, page)
	}
}

// A meeting update shows its time change from the event's current time to the new one.
func TestConsoleListShowsTheTimeChangeOfAMeetingUpdate(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	sameDay := pend(t, f.d.s, "calendar.update", `{"id":"o","start":"2026-03-31T11:00:00Z","end":"2026-03-31T12:00:00Z","location":"Room 2"}`)[0]
	otherDay := pend(t, f.d.s, "calendar.update", `{"id":"o","start":"2026-04-01T12:00:00+02:00"}`)[0]
	page := f.get(t, "/", nil).Body.String()
	for id, want := range map[string]string{
		sameDay:  `<span class="k">Start, End, Location</span><span class="snip">10:00 → 11:00 · 11:00 → 12:00 · Room 2</span>`,
		otherDay: `<span class="k">Start</span><span class="snip">Tue 31 Mar, 12:00 → Wed 1 Apr, 12:00</span>`,
	} {
		if row := rowOf(page, id); !strings.Contains(row, want) {
			t.Errorf("row misses %q: %s", want, row)
		}
	}
}
