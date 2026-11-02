package server_test

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// batchForm is the body of a batch form with the IDs, the decision, and the extra fields.
func batchForm(decision string, ids []string, extra ...string) string {
	parts := []string{"decision=" + decision}
	for _, id := range ids {
		parts = append(parts, "id="+id)
	}
	return strings.Join(append(parts, extra...), "&")
}

// approvedOrder are the IDs of the approved records of the audit log, in order.
func (f *consoleFixture) approvedOrder(t *testing.T) []string {
	t.Helper()
	_, records := auditOf(t, f.d.home)
	out := []string{}
	for _, r := range records {
		if r["event"] == "approval" && r["state"] == "approved" {
			out = append(out, r["approval_id"].(string))
		}
	}
	return out
}

// Batch approve runs the selected pending actions once each, one after another in list order: Messages, Meetings, then
// Mailbox operations, newest first. It skips the Messages and Meetings actions that need review, never a Mailbox
// operation, even of a red or sealed message, and reports an action that is not pending, or unknown, as already
// handled. The list then shows the outcome with its filters, and the decided actions under Recents. A second submit
// of the same batch runs nothing again.
func TestConsoleBatchApproveSkipsActionsThatNeedReview(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	at := func(score float64, op, params string) string {
		t.Helper()
		f.setScore(score)
		f.advance(time.Minute)
		return pend(t, f.d.s, op, params)[0]
	}
	draft := `{"to":["a@example.test"],"subject":"s","body":"b"}`
	older := at(.1, "draft.new", draft)
	red := at(.1, "mail.trash", `{"ids":["r"]}`)
	sealed := at(.1, "mail.trash", `{"ids":["s"]}`)
	gone := at(.1, "mail.trash", `{"ids":["gone"]}`)
	respond := at(.1, "calendar.respond", `{"id":"e","response":"decline","comment":"c"}`)
	invite := at(.62, "calendar.invite", `{"to":["a@example.test"],"subject":"s","body":"b","start":"2026-04-01T09:00:00Z","end":"2026-04-01T10:00:00Z"}`)
	risk := at(.62, "mail.send", draft)
	failedCheck := at(2, "draft.new", draft)
	newer := at(.1, "draft.new", draft)
	handled := at(.1, "draft.new", draft)
	if err := f.d.s.Deny(context.Background(), handled); err != nil {
		t.Fatal(err)
	}
	unknown := "0123456789abcdef0123456789abcdef"
	ids := []string{red, sealed, gone, respond, invite, older, risk, failedCheck, newer, handled, unknown, newer}
	w := f.post(t, "/batch", batchForm("approve", ids, "return="+url.QueryEscape("/?account=work&select=messages")), nil)
	if want := "/?account=work&approved=5&failed=1&handled=2&review=3"; w.Code != 303 || w.Header().Get("Location") != want {
		t.Fatalf("batch approve: %d %q %s; want %q", w.Code, w.Header().Get("Location"), w.Body, want)
	}
	if got, want := f.approvedOrder(t), []string{newer, older, respond, gone, sealed, red}; !slices.Equal(got, want) {
		t.Fatalf("approved order=%q\nwant %q", got, want)
	}
	for id, want := range map[string][]string{
		newer: {"created", "approved", "done"}, respond: {"created", "approved", "done"}, red: {"created", "approved", "done"},
		sealed: {"created", "approved", "done"}, gone: {"created", "approved", "failed"},
		risk: {"created"}, failedCheck: {"created"}, invite: {"created"}, handled: {"created", "denied"},
	} {
		if got := f.approvalStates(t, id); !slices.Equal(got, want) {
			t.Errorf("records of %s=%q; want %q", id, got, want)
		}
	}
	page := f.get(t, w.Header().Get("Location"), nil).Body.String()
	if want := "5 approved, 3 skipped (needs review), 1 failed, 2 already handled"; !strings.Contains(pageText(page), want) {
		t.Errorf("list misses the notice %q", want)
	}
	for _, id := range []string{newer, older, respond, red, gone} {
		if !slices.Contains(recentLinks(page, groupOf(page, id)), id) {
			t.Errorf("%s not under Recents", id)
		}
	}
	writes := f.writes()
	w = f.post(t, "/batch", batchForm("approve", ids), nil)
	if want := "/?handled=8&review=3"; w.Code != 303 || w.Header().Get("Location") != want || f.writes() != writes || len(f.approvedOrder(t)) != 6 {
		t.Fatalf("second submit: %d %q writes=%d/%d approved=%d", w.Code, w.Header().Get("Location"), f.writes(), writes, len(f.approvedOrder(t)))
	}
}

// Batch deny denies every selected pending action, also those that need review, and reports the rest as already
// handled. Nothing runs.
func TestConsoleBatchDenyDeniesEverySelectedPendingAction(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	f.setScore(.62)
	risk := pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	f.setScore(.1)
	draft := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	trash := pend(t, f.d.s, "mail.trash", `{"ids":["r"]}`)[0]
	done := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	if err := f.d.s.Approve(context.Background(), done); err != nil {
		t.Fatal(err)
	}
	writes := f.writes()
	w := f.post(t, "/batch", batchForm("deny", []string{risk, draft, trash, done}), nil)
	if w.Code != 303 || w.Header().Get("Location") != "/?denied=3&handled=1" || f.writes() != writes {
		t.Fatalf("batch deny: %d %q writes=%d/%d", w.Code, w.Header().Get("Location"), f.writes(), writes)
	}
	for _, id := range []string{risk, draft, trash} {
		assertState(t, f.d.s, id, `{"state":"denied"}`)
		if got := f.approvalStates(t, id); !slices.Equal(got, []string{"created", "denied"}) {
			t.Errorf("records of %s=%q", id, got)
		}
	}
	if page := f.get(t, w.Header().Get("Location"), nil).Body.String(); !strings.Contains(pageText(page), "3 denied, 1 already handled") {
		t.Errorf("list misses the notice")
	}
}

// A batch needs the console origin, the form type, one known decision, 1 to 500 IDs of 32 lowercase hex characters,
// and only known fields within the size limit. Anything else runs nothing.
func TestConsoleBatchRefusesMalformedForms(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	id := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	many := []string{id}
	for i := range 500 {
		many = append(many, fmt.Sprintf("%032x", i))
	}
	body := func(s string) func(*http.Request) {
		return func(r *http.Request) { r.Body = io.NopCloser(strings.NewReader(s)) }
	}
	for name, tc := range map[string]struct {
		change func(*http.Request)
		code   int
	}{
		"no origin":      {func(r *http.Request) { r.Header.Del("Origin") }, 403},
		"null origin":    {func(r *http.Request) { r.Header.Set("Origin", "null") }, 403},
		"foreign origin": {func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }, 403},
		"json":           {func(r *http.Request) { r.Header.Set("Content-Type", "application/json") }, 400},
		"other charset": {func(r *http.Request) {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=latin1")
		}, 400},
		"501 IDs":          {body(batchForm("approve", many)), 400},
		"uppercase ID":     {body(batchForm("approve", []string{id, strings.ToUpper(id)})), 400},
		"short ID":         {body(batchForm("approve", []string{id, id[1:]})), 400},
		"no ID":            {body("decision=approve"), 400},
		"empty ID":         {body(batchForm("approve", []string{id, ""})), 400},
		"no decision":      {body("id=" + id), 400},
		"two decisions":    {body(batchForm("approve", []string{id}, "decision=deny")), 400},
		"unknown decision": {body(batchForm("maybe", []string{id})), 400},
		"confirm":          {body(batchForm("approve", []string{id}, "confirm=flagged")), 400},
		"unknown field":    {body(batchForm("approve", []string{id}, "x=1")), 400},
		"two returns":      {body(batchForm("approve", []string{id}, "return=%2F", "return=%2F")), 400},
		"too large":        {body(batchForm("approve", []string{id}) + strings.Repeat("&", 32<<10)), 400},
		"malformed":        {body(batchForm("approve", []string{id}) + "&%zz"), 400},
		"query instead": {func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(""))
			r.URL.RawQuery = batchForm("approve", []string{id})
		}, 400},
		"batch of an op ID": {func(r *http.Request) { r.URL.Path = "/batch/" + id }, 404},
	} {
		if w := f.post(t, "/batch", batchForm("approve", []string{id}), tc.change); w.Code != tc.code {
			t.Errorf("%s: %d %s; want %d", name, w.Code, w.Body, tc.code)
		}
	}
	if f.writes() != 0 || len(f.approvedOrder(t)) != 0 {
		t.Fatalf("refused batch ran: writes=%d approved=%q", f.writes(), f.approvedOrder(t))
	}
	assertState(t, f.d.s, id, `{"state":"pending"}`)
	if w := f.get(t, "/batch", nil); w.Code != 405 || w.Header().Get("Allow") != "POST" {
		t.Fatalf("GET /batch: %d %q", w.Code, w.Header().Get("Allow"))
	}
	if w := f.post(t, "/batch", batchForm("approve", many[:500]), nil); w.Code != 303 || w.Header().Get("Location") != "/?approved=1&handled=499" {
		t.Fatalf("500 IDs: %d %q", w.Code, w.Header().Get("Location"))
	}
}

// The list shows the notice of a batch only from valid counts, as a fixed sentence without the parts that are zero.
// An invalid count gives the unfiltered page without a notice.
func TestConsoleListShowsTheBatchNotice(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)
	for query, want := range map[string]string{
		"?approved=3&review=1&failed=1&handled=2": "3 approved, 1 skipped (needs review), 1 failed, 2 already handled",
		"?denied=4&handled=1":                     "4 denied, 1 already handled",
		"?account=work&approved=500":              "500 approved",
		"?handled=1&approved=0":                   "1 already handled",
	} {
		if text := pageText(f.get(t, "/"+query, nil).Body.String()); !strings.Contains(text, want) {
			t.Errorf("%q: list misses %q", query, want)
		}
	}
	for _, query := range []string{"?approved=501", "?approved=-1", "?approved=01", "?approved=%2B1", "?approved=1e2", "?approved=x", "?approved=", "?approved=1&approved=1", "?approved=0", "?approved=1&CANARY=1", "?approved=1&account=CANARY"} {
		page := f.get(t, "/"+query, nil).Body.String()
		if strings.Contains(page, `class="notice"`) || strings.Contains(pageText(page), "1 approved") || strings.Contains(page, "CANARY") {
			t.Errorf("%q: notice shown", query)
		}
	}
}

// rowBoxes are the batch checkboxes of a page, by ID: true when ticked.
func rowBoxes(page string) map[string]bool {
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`<input class="sel" type="checkbox" form="batch" name="id" value="([^"]*)"( data-review)?( checked)?>`).FindAllStringSubmatch(page, -1) {
		out[m[1]] = m[3] != ""
	}
	return out
}

// Every waiting row, not Recents, has a checkbox of the batch form. Each table's header box is a link that ticks the
// rows of that table and keeps the filters; on that page the link is ticked and leads back, and Clear is a link
// back. The selection never narrows the page, and an unknown group gives the unfiltered page.
func TestConsoleListTicksTheRowsOfASelectedTable(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	send := pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	respond := pend(t, f.d.s, "calendar.respond", `{"id":"e","response":"decline","comment":"c"}`)[0]
	cancel := pend(t, f.d.s, "calendar.cancel", `{"id":"o","comment":"c"}`)[0]
	trash := pend(t, f.d.s, "mail.trash", `{"ids":["g"]}`)[0]
	denied := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	if err := f.d.s.Deny(context.Background(), denied); err != nil {
		t.Fatal(err)
	}
	none := map[string]bool{send: false, respond: false, cancel: false, trash: false}
	for query, want := range map[string]map[string]bool{
		"":                              none,
		"?select=CANARY":                none,
		"?account=work&select=meetings": {send: false, respond: true, cancel: true, trash: false},
		"?select=mailbox":               {send: false, respond: false, cancel: false, trash: true},
	} {
		page := f.get(t, "/"+query, nil).Body.String()
		if got := rowBoxes(page); !maps.Equal(got, want) {
			t.Errorf("%q: boxes=%v; want %v", query, got, want)
		}
		if !strings.Contains(page, `<form id="batch" method="post" action="/batch" aria-label="Selected actions">`) || strings.Contains(page, "CANARY") {
			t.Errorf("%q: no batch form, or the page repeats the query", query)
		}
	}
	page := f.get(t, "/?account=work&select=meetings", nil).Body.String()
	for _, want := range []string{
		`<a class="all on" href="/?account=work" title="Clear the selection in Meetings">`,
		`<a class="all" href="/?account=work&amp;select=messages" title="Select all in Messages">`,
		`<a class="all" href="/?account=work&amp;select=mailbox" title="Select all in Mailbox operations">`,
		`<a class="btn quiet" href="/?account=work">Clear</a>`,
		`<input type="hidden" name="return" value="/?account=work">`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page misses %q", want)
		}
	}
	if strings.Contains(page, `name="return" value="/?account=work&amp;select`) || strings.Contains(page, `type="reset"`) {
		t.Errorf("a return keeps select, or Clear resets to the ticked page")
	}
	page = f.get(t, "/?status=others", nil).Body.String()
	for _, want := range []string{`<a class="all" href="/?select=meetings&amp;status=others" title="Select all in Meetings">`, `<button class="btn quiet" type="reset">Clear</button>`} {
		if !strings.Contains(page, want) {
			t.Errorf("page misses %q", want)
		}
	}
	onlyConsoleLinks(t, page)
}

// A row offers Approve and Deny, which return to the list with its filters. A row that needs review offers Review, a
// link to its action page, and Deny; its open row and its action page approve only with the "I reviewed it" box,
// which the browser requires and Deny skips. A Mailbox operation never needs review.
func TestConsoleListRowsOfferTheirDecisions(t *testing.T) {
	f := gmailConsole(t, everyWriteWaits)
	f.setScore(.1)
	clear := pendOn(t, f.d.s, "draft.new", "gmail", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	f.setScore(.62)
	risk := pend(t, f.d.s, "mail.send", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
	trash := pend(t, f.d.s, "mail.trash", `{"ids":["r"]}`)[0]
	page := f.get(t, "/?status=others", nil).Body.String()
	page += f.get(t, "/?status=review", nil).Body.String()
	acts := func(id string) string {
		row := rowOf(page, id)
		_, form, _ := strings.Cut(row, `<form class="acts" method="post" action="/a/`+id+`">`)
		return form
	}
	for _, id := range []string{clear, trash} {
		form, row := acts(id), rowOf(page, id)
		if !strings.Contains(form, `<button class="btn go" type="submit" name="decision" value="approve">Approve`) || !strings.Contains(form, `value="deny">Deny`) ||
			!strings.Contains(form, `<input type="hidden" name="return" value="/?status=others">`) || strings.Contains(row, `class="ack"`) || strings.Contains(row, "data-review") {
			t.Errorf("row of %s: %s", id, row)
		}
	}
	form, row := acts(risk), rowOf(page, risk)
	for _, want := range []string{`<a class="btn" href="/a/` + risk + `">Review`, `value="deny">Deny`, `<input type="hidden" name="return" value="/?status=review">`} {
		if !strings.Contains(form, want) {
			t.Errorf("row actions miss %q: %s", want, form)
		}
	}
	if strings.Contains(form, `value="approve"`) || !strings.Contains(row, "data-review") {
		t.Errorf("row actions of a row that needs review: %s", row)
	}
	_, ack, _ := strings.Cut(row, `<form class="ack" method="post" action="/a/`+risk+`">`)
	for _, want := range []string{`<input type="hidden" name="return" value="/?status=review">`, `<input type="checkbox" name="confirm" value="flagged" required> I reviewed it`, `value="approve">Approve`} {
		if !strings.Contains(ack, want) {
			t.Errorf("open row misses %q: %s", want, row)
		}
	}
	action := f.get(t, "/a/"+risk, nil).Body.String()
	for _, want := range []string{`<input type="checkbox" name="confirm" value="flagged" required> I reviewed it`, `name="decision" value="deny" formnovalidate>Deny`} {
		if !strings.Contains(action, want) {
			t.Errorf("action page misses %q", want)
		}
	}
	if action := f.get(t, "/a/"+clear, nil).Body.String(); strings.Contains(action, `name="confirm"`) || strings.Contains(action, "formnovalidate") || strings.Contains(action, `name="return"`) {
		t.Errorf("action page of a checked action asks for review or returns to the list")
	}
	if w := f.post(t, "/a/"+clear, "decision=approve&return=%2F%3Fstatus%3Dothers", nil); w.Code != 303 || w.Header().Get("Location") != "/?status=others" {
		t.Fatalf("row approve: %d %q", w.Code, w.Header().Get("Location"))
	}
	if got := rowBoxes(f.get(t, "/", nil).Body.String()); !maps.Equal(got, map[string]bool{risk: false, trash: false}) {
		t.Fatalf("boxes after the approve=%v", got)
	}
}

// A batch decides a selected pending action even when more than 500 others wait, so that the list does not show it.
func TestConsoleBatchDecidesActionsBeyondTheListedOnes(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	draft := `{"to":["a@example.test"],"subject":"s","body":"b"}`
	approved, denied := pend(t, f.d.s, "draft.new", draft)[0], pend(t, f.d.s, "draft.new", draft)[0]
	for i := range 500 {
		f.advance(time.Second)
		pend(t, f.d.s, "draft.delete", `{"id":"draft-`+strconv.Itoa(i+10)+`"}`)
	}
	if strings.Contains(f.get(t, "/", nil).Body.String(), approved) {
		t.Fatal("the list shows the oldest action")
	}
	if w := f.post(t, "/batch", batchForm("approve", []string{approved}), nil); w.Code != 303 || w.Header().Get("Location") != "/?approved=1" {
		t.Fatalf("batch approve: %d %q", w.Code, w.Header().Get("Location"))
	}
	if w := f.post(t, "/batch", batchForm("deny", []string{denied}), nil); w.Code != 303 || w.Header().Get("Location") != "/?denied=1" {
		t.Fatalf("batch deny: %d %q", w.Code, w.Header().Get("Location"))
	}
	assertState(t, f.d.s, approved, `{"state":"done","result":{"id":"draft-1"}}`)
	assertState(t, f.d.s, denied, `{"state":"denied"}`)
}

// A batch returns to the list with the filters of a valid return field, rebuilt as a decision does, and to the
// unfiltered list for any other return field.
func TestConsoleBatchReturnsToTheListWithItsFilters(t *testing.T) {
	f := consoleDaemon(t, everyWriteWaits, true)
	deny := func(ret string) string {
		t.Helper()
		id := pend(t, f.d.s, "draft.new", `{"to":["a@example.test"],"subject":"s","body":"b"}`)[0]
		w := f.post(t, "/batch", batchForm("deny", []string{id}, "return="+url.QueryEscape(ret)), nil)
		if w.Code != 303 || strings.Contains(w.Body.String(), "evil") {
			t.Fatalf("return %q: %d %s", ret, w.Code, w.Body)
		}
		return w.Header().Get("Location")
	}
	for ret, want := range map[string]string{
		"/?status=others&account=work":   "/?account=work&denied=1&status=others",
		"/?account=work&select=messages": "/?account=work&denied=1",
		"/?approved=3":                   "/?denied=1",
	} {
		if got := deny(ret); got != want {
			t.Errorf("return %q: Location %q; want %q", ret, got, want)
		}
	}
	for _, ret := range hostileReturns {
		if got := deny(ret); got != "/?denied=1" {
			t.Errorf("return %q: Location %q; want the list", ret, got)
		}
	}
}
