package server

import (
	"context"
	"html"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/views"
)

// listGroup is one table of the list page. Key names it in URLs and in the data-group attribute. Scored is true when
// its actions get an action check, so that its rows show a Score.
type listGroup struct {
	Key, Title, Hint, Class string
	Scored                  bool
}

const (
	groupMessages = "messages"
	groupMeetings = "meetings"
	groupMailbox  = "mailbox"
)

// listGroups are the list tables in page order.
var listGroups = []listGroup{
	{groupMessages, "Messages", "Sends go out when approved. Drafts are only saved or deleted.", "t-mail", true},
	{groupMeetings, "Meetings", "Approving notifies the attendees or the organizer.", "t-meet", true},
	{groupMailbox, "Mailbox operations", "Changes to existing messages. Trash is a move, never a permanent delete. No score, and the Show filter does not apply.", "t-box", false},
}

// scored reports whether the list group with the key shows a Score. An unknown group shows it, so that a risk found
// by the check is never hidden.
func scored(group string) bool {
	i := slices.IndexFunc(listGroups, func(g listGroup) bool { return g.Key == group })
	return i < 0 || listGroups[i].Scored
}

// listedOps maps each API operation that can wait for approval to its list group and its action label. The list
// shows no pending action of another operation. TestConsoleListGroupsEveryOperationThatCanWait checks that every
// such operation is here.
var listedOps = map[string]struct{ group, label string }{
	"mail.send":        {groupMessages, "Send"},
	"mail.reply":       {groupMessages, "Reply"},
	"mail.forward":     {groupMessages, "Forward"},
	"draft.new":        {groupMessages, "New draft"},
	"draft.reply":      {groupMessages, "Draft reply"},
	"draft.forward":    {groupMessages, "Draft forward"},
	"draft.update":     {groupMessages, "Edit draft"},
	"draft.send":       {groupMessages, "Send draft"},
	"draft.delete":     {groupMessages, "Delete draft"},
	"calendar.invite":  {groupMeetings, "Invite"},
	"calendar.update":  {groupMeetings, "Update"},
	"calendar.cancel":  {groupMeetings, "Cancel"},
	"calendar.respond": {groupMeetings, "Respond"},
	"mail.trash":       {groupMailbox, "Trash"},
}

// recentsListed is the number of handled actions that each table lists under Recents.
const recentsListed = 10

// Show filter values.
const (
	showReview = "review"
	showOthers = "others"
)

// listQuery is the validated query of the list page; zero shows everything. The filter is alias, with key its
// account key, and show. sel is the group whose rows the page renders ticked, and notice the outcome of a batch;
// neither is a filter, and the filter links and the return of a decision leave them out.
type listQuery struct {
	alias, key, show, sel string
	notice                batchCounts
}

// batchCounts are the outcome of a batch decision.
type batchCounts struct{ approved, denied, review, failed, handled int }

type noticePart struct {
	key, label string
	count      func(*batchCounts) *int
}

// noticeParts are the parts of a batch notice in notice order: the list page query key that carries the count, from
// 0 to maxBatchIDs, the words after the count, and the count.
var noticeParts = []noticePart{
	{"approved", "approved", func(n *batchCounts) *int { return &n.approved }},
	{"denied", "denied", func(n *batchCounts) *int { return &n.denied }},
	{"review", "skipped (needs review)", func(n *batchCounts) *int { return &n.review }},
	{"failed", "failed", func(n *batchCounts) *int { return &n.failed }},
	{"handled", "already handled", func(n *batchCounts) *int { return &n.handled }},
}

// text is the notice of a batch, with the parts that are not zero, as in "3 approved, 1 skipped (needs review),
// 1 failed, 2 already handled"; empty when every part is zero.
func (n batchCounts) text() string {
	var parts []string
	for _, p := range noticeParts {
		if c := *p.count(&n); c > 0 {
			parts = append(parts, strconv.Itoa(c)+" "+p.label)
		}
	}
	return strings.Join(parts, ", ")
}

// parseListQuery validates the list query against the loaded configuration. ok is false for a key that is none of
// account, status, select, and the keys of noticeParts, a repeated key, or an unknown value; the list page then shows the
// unfiltered page.
func parseListQuery(raw string, cfg config.Config) (q listQuery, ok bool) {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return listQuery{}, false
	}
	for key, v := range values {
		if len(v) != 1 {
			return listQuery{}, false
		}
		if i := slices.IndexFunc(noticeParts, func(p noticePart) bool { return p.key == key }); i >= 0 {
			n, err := strconv.Atoi(v[0])
			if err != nil || n < 0 || n > maxBatchIDs || strconv.Itoa(n) != v[0] {
				return listQuery{}, false
			}
			*noticeParts[i].count(&q.notice) = n
			continue
		}
		switch key {
		case "account":
			a, ok := cfg.Account(v[0])
			if !ok {
				return listQuery{}, false
			}
			q.alias, q.key = a.Alias, a.Key()
		case "status":
			if v[0] != showReview && v[0] != showOthers {
				return listQuery{}, false
			}
			q.show = v[0]
		case "select":
			if !slices.ContainsFunc(listGroups, func(g listGroup) bool { return g.Key == v[0] }) {
				return listQuery{}, false
			}
			q.sel = v[0]
		default:
			return listQuery{}, false
		}
	}
	return q, true
}

// maxReturnBytes bounds the return field of a decision form.
const maxReturnBytes = 512

// returnQuery validates the return field of a decision form: "/" or "/?" and a valid list query, at most
// maxReturnBytes, without "//", a backslash, or a control character. ok is false for any other value, which the
// decision ignores. The redirect is the url of the filter, never the field.
func returnQuery(v string, cfg config.Config) (listQuery, bool) {
	if len(v) > maxReturnBytes || strings.Contains(v, "//") || strings.ContainsFunc(v, func(r rune) bool { return r == '\\' || unicode.IsControl(r) }) {
		return listQuery{}, false
	}
	if v == "/" {
		return listQuery{}, true
	}
	raw, ok := strings.CutPrefix(v, "/?")
	if !ok {
		return listQuery{}, false
	}
	return parseListQuery(raw, cfg)
}

// values are the filter parameters of the query.
func (q listQuery) values() url.Values {
	v := url.Values{}
	if q.alias != "" {
		v.Set("account", q.alias)
	}
	if q.show != "" {
		v.Set("status", q.show)
	}
	return v
}

// url is the list page with the filter, its parameters in a fixed order.
func (q listQuery) url() string { return listURL(q.values()) }

// listURL is the list page with the parameters v.
func listURL(v url.Values) string {
	if len(v) == 0 {
		return "/"
	}
	return "/?" + v.Encode()
}

// matches reports whether a waiting row matches the filter, which the filter links count. A Mailbox operations row
// never needs review, so it matches Others, never Needs review.
func (q listQuery) matches(r listRow) bool {
	return (q.key == "" || r.Account == q.key) && (q.show == "" || (q.show == showReview) == r.Review)
}

// shows reports whether the page shows a waiting row: the Show filter does not apply to Mailbox operations, which
// have no status.
func (q listQuery) shows(r listRow) bool {
	if r.Group == groupMailbox {
		q.show = ""
	}
	return q.matches(r)
}

// refreshChoice is an interval of the Auto-refresh menu: Value is its ?refresh= and cookie value, Short its label in
// the menu button, Long its label in the menu. Seconds zero is off.
type refreshChoice struct {
	Value, Short, Long string
	Seconds            int
}

// refreshChoices are the Auto-refresh intervals in menu order; the first is the default. It is a variable only so
// that tests can change its cost through export_test.go.
var refreshChoices = []refreshChoice{
	{"5m", "5 min", "5 minutes", 300},
	{"10m", "10 min", "10 minutes", 600},
	{"30m", "30 min", "30 minutes", 1800},
	{"1h", "1 hour", "1 hour", 3600},
	{"off", "Off", "Off", 0},
}

// refreshCookie keeps the Auto-refresh choice. The name is the console's own: cookies are not kept per port, so
// another web page on the same host could otherwise set it.
const refreshCookie = "amg_console_refresh"

// requestRefresh is the choice of the refresh cookie of the request; a missing or unknown value is the default.
func requestRefresh(r *http.Request) refreshChoice {
	if c, err := r.Cookie(refreshCookie); err == nil {
		if i := slices.IndexFunc(refreshChoices, func(ch refreshChoice) bool { return ch.Value == c.Value }); i >= 0 {
			return refreshChoices[i]
		}
	}
	return refreshChoices[0]
}

// chooseRefresh keeps a valid ?refresh= choice in the refresh cookie for a year, then redirects to the list with the
// filter of the rest of the query, rebuilt, or to the unfiltered list when the rest is not valid. Any other choice
// changes no cookie.
func chooseRefresh(w http.ResponseWriter, r *http.Request, values url.Values, valid bool, cfg config.Config) {
	if v := values["refresh"]; len(v) == 1 && slices.ContainsFunc(refreshChoices, func(ch refreshChoice) bool { return ch.Value == v[0] }) {
		http.SetCookie(w, &http.Cookie{Name: refreshCookie, Value: v[0], Path: "/", MaxAge: 365 * 24 * 3600, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	}
	values.Del("refresh")
	var q listQuery
	if valid {
		q, _ = parseListQuery(values.Encode(), cfg)
	}
	http.Redirect(w, r, q.url(), http.StatusSeeOther)
}

type listPage struct {
	// Waiting and Review count the waiting rows that the filter shows, and those that need review.
	Waiting, Review int
	// Refresh is the Auto-refresh choice; RefreshMenu links to every choice with the filter.
	Refresh         refreshChoice
	RefreshMenu     []menuLink
	Accounts, Shows []filterLink
	Groups          []groupView
	// Notice is the outcome of a batch. Return is the list with the filter, where a decision from the list goes back
	// to. ClearURL is set on a page that ticks a group: Clear is then a link to the page without the ticks.
	Notice, Return, ClearURL string
}

type menuLink struct {
	Label, URL string
	Current    bool
}

type filterLink struct {
	Label, Title, URL string
	Count             int
	Current           bool
}

type groupView struct {
	listGroup
	Rows, Recents []listRow
	// Selected is true when the page ticks the group's rows. SelectURL ticks them, or clears them when Selected.
	Selected  bool
	SelectURL string
}

// listRow is one pending action in a list table. Every text in it can come from the agent or from mail.
type listRow struct {
	ID, Group, Label, Alias, Account, Age string
	// Score is nil in a group that shows no Score.
	Score *scoreCell
	// Review is true when the action needs review: a Messages or Meetings action whose check found a risk or failed.
	Review bool
	// Messages: the reply marker, the recipients shortened and in full, the subject, and a one-line preview of the body.
	ToMarker, To, ToTitle, Subject, Preview string
	NoSubject                               bool
	// Meetings. EventMissing is true when the event is only an ID; EventPlaceholder when Event is a placeholder, not
	// the subject; PeopleRole when People names a role, not a count.
	Event, When, People, Change, ChangeDetail  string
	EventMissing, EventPlaceholder, PeopleRole bool
	// Mailbox operations: the sender and subject of the message from its guarded view, the subject in Subject;
	// SubjectHidden when the view has no subject.
	Sender, Received              string
	MessageMissing, SubjectHidden bool
	// ref is the event or message that the list reads live, empty when the row needs no read; args are the stored
	// request arguments.
	ref  string
	args map[string]any
	// Recents: a summary from the stored fields, and the outcome, failed or not.
	Summary, Outcome string
	Failed           bool
	// The open row: the fields of the live read, every request field, the draft fields of a draft send, and the body
	// or meeting text under its caption.
	Read          []readField
	Fields, Draft []field
	Body, Caption string
	// BodyHTML is true when Body is HTML source, which the action page also shows in a sandboxed frame.
	BodyHTML bool
}

// readField is an open-row field from the live read of the event or message. Placeholder marks a Value that stands
// in for hidden text; Source names where the value was read.
type readField struct {
	Label, Value, Source string
	Placeholder          bool
}

// Bar is the row colour: the Bar of the Score, or none (grey) without a Score.
func (r listRow) Bar() string {
	if r.Score == nil {
		return "none"
	}
	return r.Score.Bar
}

// scoreCell is the Score of a row. Bar is the row colour: ok (green), risky (red), or none (grey).
type scoreCell struct {
	Value, Label, Bar string
	// Code is the fixed error code of a failed check.
	Code       string
	Risk, Fake bool
}

var notChecked = scoreCell{Value: "—", Label: "Not checked", Bar: "none"}

// checkScore is the Score of an action check. Only a valid score below the threshold without a flag is green; a check
// with a missing score or invalid data is a failed check, red.
func checkScore(c *model.ActionCheck) scoreCell {
	if c == nil {
		return notChecked
	}
	s := scoreCell{Label: "Needs review", Bar: "risky", Risk: true, Fake: c.Fake}
	switch {
	case c.Error != nil:
		s.Value, s.Code = "error", *c.Error
	case c.Score == nil:
		s.Value = "error"
	case model.ActionScore(*c.Score).Failed():
		s.Value, s.Code = "error", *model.ActionScore(*c.Score).Error
	case c.Flag || model.ActionScore(*c.Score).Flag:
		s.Value = scoreText(*c.Score)
	default:
		s.Value, s.Label, s.Bar, s.Risk = scoreText(*c.Score), "Checked", "ok", false
	}
	return s
}

// scoreText is a score from 0 to 1 cut, never rounded, to two decimals, so that a score below the flag threshold
// never reads as the threshold. The shortest decimal that gives back the score is below any decimal threshold that
// the score is below.
func scoreText(score float64) string {
	whole, frac, _ := strings.Cut(strconv.FormatFloat(score, 'f', -1, 64), ".")
	return whole + "." + (frac + "00")[:2]
}

func (c *console) list(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	st := c.s.current.Load()
	if st == nil {
		plain(w, 503, ErrUnavailable.Error())
		return
	}
	values, err := url.ParseQuery(r.URL.RawQuery)
	if values.Has("refresh") {
		chooseRefresh(w, r, values, err == nil, st.config)
		return
	}
	// An invalid query shows the unfiltered page.
	q, _ := parseListQuery(r.URL.RawQuery, st.config)
	// Recents follow the Account filter; the waiting actions of every account give the filter counts.
	items, err := c.s.cache.ListPending(ctx, recentsListed, q.key)
	if err != nil {
		plain(w, 500, "approvals unavailable")
		return
	}
	aliases := accountAliases(st.config)
	now := c.s.options.Now()
	var waiting []listRow
	recents := map[string][]listRow{}
	for _, item := range items {
		if _, ok := listedOps[item.Op]; !ok {
			continue
		}
		row := listedRow(item, aliases, now)
		if item.State == cache.StatePending {
			waiting = append(waiting, row)
		} else if len(recents[row.Group]) < recentsListed {
			recents[row.Group] = append(recents[row.Group], row)
		}
	}
	page := listPage{Notice: q.notice.text(), Return: q.url(), Refresh: requestRefresh(r)}
	for _, ch := range refreshChoices {
		v := q.values()
		v.Set("refresh", ch.Value)
		page.RefreshMenu = append(page.RefreshMenu, menuLink{ch.Long, listURL(v), ch.Value == page.Refresh.Value})
	}
	if q.sel != "" {
		page.ClearURL = q.url()
	}
	count := func(f listQuery) int {
		n := 0
		for _, row := range waiting {
			if f.matches(row) {
				n++
			}
		}
		return n
	}
	// The counts of each filter follow the other filter.
	link := func(label, title string, f listQuery, current bool) filterLink {
		return filterLink{label, title, f.url(), count(f), current}
	}
	page.Accounts = []filterLink{link("All", "", listQuery{show: q.show}, q.key == "")}
	for _, a := range st.config.Accounts {
		page.Accounts = append(page.Accounts, link(a.Alias, a.Key(), listQuery{alias: a.Alias, key: a.Key(), show: q.show}, q.key == a.Key()))
	}
	page.Shows = []filterLink{
		link("All", "", listQuery{alias: q.alias, key: q.key}, q.show == ""),
		link("Needs review", "", listQuery{alias: q.alias, key: q.key, show: showReview}, q.show == showReview),
		link("Others", "", listQuery{alias: q.alias, key: q.key, show: showOthers}, q.show == showOthers),
	}
	for _, g := range listGroups {
		view := groupView{listGroup: g, Recents: recents[g.Key], Selected: q.sel == g.Key}
		selectURL := q.values()
		if !view.Selected {
			selectURL.Set("select", g.Key)
		}
		view.SelectURL = listURL(selectURL)
		for _, row := range waiting {
			if row.Group == g.Key && q.shows(row) {
				view.Rows = append(view.Rows, row)
				page.Waiting++
				if row.Review {
					page.Review++
				}
			}
		}
		if len(view.Rows) > 0 || len(view.Recents) > 0 {
			page.Groups = append(page.Groups, view)
		}
	}
	var shown []*listRow
	for g := range page.Groups {
		for i := range page.Groups[g].Rows {
			shown = append(shown, &page.Groups[g].Rows[i])
		}
	}
	// Only waiting rows read live.
	c.readLive(ctx, shown, now)
	render(w, 200, "list", page)
}

// Live reads of one list page. The page reads the events and messages of at most listReadsPerPage rows, in page
// order, and at most listReadsPerAccount of one account at a time. Every read shares one deadline, listReadDeadline:
// a read that fails or is late leaves its row not available, and the page renders once every read has returned or
// the deadline has passed, also when a read waits where the deadline cannot stop it. A read waits for its account
// slot only until the deadline.
const (
	listReadsPerPage    = 50
	listReadsPerAccount = 4
)

// listReadDeadline is a variable only so that tests can change its cost through export_test.go.
var listReadDeadline = 3 * time.Second

// readLive fills the rows that name an event or message from its guarded view: Meetings rows of an update, cancel,
// or response, and Mailbox operations rows. A row whose read fails stays not available.
func (c *console) readLive(ctx context.Context, rows []*listRow, now time.Time) {
	var reads []*listRow
	slots := map[string]chan struct{}{}
	for _, r := range rows {
		if r.ref != "" && len(reads) < listReadsPerPage {
			reads = append(reads, r)
			if slots[r.Account] == nil {
				slots[r.Account] = make(chan struct{}, listReadsPerAccount)
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, listReadDeadline)
	defer cancel()
	type readResult struct {
		i int
		v any
	}
	// Each worker sends one result, which is nil when its read failed or was late. The channel holds every result,
	// so a worker that ends after the page never blocks.
	done := make(chan readResult, len(reads))
	for i, r := range reads {
		op, account, ref, slot := "mail.get", r.Account, r.ref, slots[r.Account]
		if r.Group == groupMeetings {
			op = "calendar.get"
		}
		go func() {
			var v any
			// A panic in a read leaves the row not available instead of ending the daemon.
			defer func() {
				_ = recover()
				done <- readResult{i, v}
			}()
			select {
			case slot <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-slot }()
			result, err := c.guardedRead(ctx, op, account, ref)
			if err == nil && ctx.Err() == nil {
				v = result
			}
		}()
	}
	results := make([]any, len(reads))
collect:
	for range reads {
		select {
		case r := <-done:
			results[r.i] = r.v
		case <-ctx.Done():
			break collect
		}
	}
	for i, r := range reads {
		switch v := results[i].(type) {
		case views.CalendarView:
			if r.Group == groupMeetings {
				eventRead(r, v)
			}
		case views.MailView:
			if r.Group == groupMailbox {
				messageRead(r, v, now)
			}
		}
	}
}

// eventRead fills a Meetings row from the guarded view of its event. A view without a subject, at a verdict that
// hides it, shows a placeholder.
func eventRead(r *listRow, v views.CalendarView) {
	r.EventMissing, r.EventPlaceholder = false, false
	r.Event = "Subject hidden"
	switch {
	case v.Subject == nil:
		r.EventPlaceholder = true
	case *v.Subject == "":
		r.Event, r.EventPlaceholder = "No subject", true
	default:
		r.Event = *v.Subject
	}
	r.When = eventWhen(v.Start, v.End)
	if !r.PeopleRole && v.Attendees != nil {
		r.People = people(len(*v.Attendees))
	}
	if r.Label == listedOps["calendar.update"].label {
		r.Change, r.ChangeDetail = updateChange(r.args, &v)
	}
	r.Read = []readField{{Label: "Event", Value: r.Event, Source: "from the calendar", Placeholder: r.EventPlaceholder}, {Label: "When", Value: eventWhenLong(v.Start, v.End)}}
	if v.Organizer.Address != "" {
		r.Read = append(r.Read, readField{Label: "Organizer", Value: addressText(v.Organizer)})
	}
}

// messageRead fills a Mailbox operations row from the guarded view of its message. The subject shows only when the
// view gives one; a red or sealed view never does.
func messageRead(r *listRow, v views.MailView, now time.Time) {
	r.MessageMissing = false
	r.SubjectHidden = v.Subject == nil || v.Verdict == model.Red || v.Verdict == model.Sealed
	if v.Sender != nil {
		r.Sender = v.Sender.Address
		r.Read = append(r.Read, readField{Label: "Sender", Value: addressText(*v.Sender)})
	}
	subject := readField{Label: "Subject", Value: "Subject hidden (sealed)", Placeholder: true}
	if !r.SubjectHidden {
		r.Subject, r.NoSubject = *v.Subject, *v.Subject == ""
		subject.Value, subject.Placeholder = r.Subject, r.NoSubject
		if r.NoSubject {
			r.Subject, subject.Value = "No subject", "No subject"
		}
	}
	r.Read = append(r.Read, subject)
	if at, err := time.Parse(time.RFC3339, v.ReceivedAt); err == nil {
		at = at.In(now.Location())
		r.Received = at.Format("Mon 2 Jan")
		if at.Format(time.DateOnly) == now.Format(time.DateOnly) {
			r.Received = at.Format("Today 15:04")
		}
		r.Read = append(r.Read, readField{Label: "Received", Value: at.Format(longTime)})
	}
}

// notRendered ends the caption of an HTML body that the page shows only as source.
const notRendered = ", not rendered"

// stateLabel is the state of a stored action as the console shows it: Pending, Done, Denied, Failed, Withdrawn.
func stateLabel(s cache.PendingState) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(string(s[:1])) + string(s[1:])
}

// accountAliases maps each account key of the configuration to its alias.
func accountAliases(cfg config.Config) map[string]string {
	out := map[string]string{}
	for _, a := range cfg.Accounts {
		out[a.Key()] = a.Alias
	}
	return out
}

// needsReview reports whether a pending action needs review: a Messages or Meetings action whose check found a risk
// or failed. Mailbox operations, which show no Score, never need review.
func needsReview(item cache.Pending) bool {
	return listedOps[item.Op].group != groupMailbox && checkScore(item.Jev).Risk
}

// listedRow builds the row of a pending action of a listed operation from its stored fields only. The action page
// shows the same row.
func listedRow(item cache.Pending, aliases map[string]string, now time.Time) listRow {
	op := listedOps[item.Op]
	alias, ok := aliases[item.Account]
	if !ok {
		alias = item.Account
	}
	row := listRow{ID: item.ID, Group: op.group, Label: op.label, Alias: alias, Account: item.Account, Age: age(now.Sub(item.Created))}
	if scored(op.group) {
		score := checkScore(item.Jev)
		row.Score = &score
	}
	row.Review = needsReview(item)
	args := storedFields(item.Args)
	row.args = args
	fields := args
	// A draft send names only its draft; what it sends is the stored draft fields.
	if item.Op == "draft.send" {
		fields = storedFields(item.Draft)
		row.Draft = openFields(requestFields(without(fields, "body")))
	}
	// The agent's text goes under a caption instead of among the fields.
	key, caption := "body", "Body"
	switch item.Op {
	case "calendar.invite", "calendar.update":
		caption = "Invitation text"
	case "calendar.respond":
		key, caption = "comment", "Comment to the organizer"
	case "calendar.cancel":
		key, caption = "comment", "Comment to the attendees"
	}
	row.Fields = openFields(requestFields(without(args, key)))
	if text, ok := fields[key].(string); ok {
		row.Body, row.Caption = text, caption+", plain text as the agent wrote it"
		if fields["html"] == true {
			row.BodyHTML, row.Caption = true, caption+", HTML source as the agent wrote it"+notRendered
		}
	}
	switch op.group {
	case groupMessages:
		messageCells(&row, item.Op, fields)
	case groupMeetings:
		meetingCells(&row, item.Op, args)
	case groupMailbox:
		// A waiting trash holds one message.
		row.MessageMissing, row.Received = true, "—"
		if ids := texts(args["ids"]); len(ids) > 0 {
			row.ref = ids[0]
		}
	}
	if item.State != cache.StatePending {
		row.Outcome, row.Failed = stateLabel(item.State), item.State == cache.StateFailed
		if row.Failed && item.Result != nil {
			row.Outcome += ": " + *item.Result
		}
		row.Age, row.Summary = age(now.Sub(item.Updated)), recentSummary(row, args)
	}
	return row
}

// recentSummary is the Summary of a handled action: the recipients and subject of a message, the event or the
// change of a meeting, and the number of messages of a mailbox operation.
func recentSummary(row listRow, args map[string]any) string {
	switch row.Group {
	case groupMessages:
		parts := []string{}
		to := row.To
		if row.ToMarker != "" {
			to = strings.TrimSuffix(row.ToMarker+"; "+to, "; ")
		}
		for _, part := range []string{to, row.Subject} {
			if part != "" {
				parts = append(parts, part)
			}
		}
		return strings.Join(parts, " · ")
	case groupMeetings:
		if !row.EventMissing {
			return row.Event
		}
		if row.ChangeDetail != "" {
			return row.Change + " — " + row.ChangeDetail
		}
		return row.Change
	}
	if n := len(texts(args["ids"])); n != 1 {
		return strconv.Itoa(n) + " messages"
	}
	return "1 message"
}

func without(args map[string]any, key string) map[string]any {
	out := maps.Clone(args)
	delete(out, key)
	return out
}

// openLabels are the open-row labels of argument names that are not words.
var openLabels = map[string]string{"html": "HTML", "id": "ID", "ids": "IDs"}

// openFields gives request fields their open-row labels, capitalized with spaces for underscores, and drops empty
// lines, so that an empty field shows as a dash.
func openFields(in []field) []field {
	out := make([]field, 0, len(in))
	for _, f := range in {
		label, ok := openLabels[f.Label]
		if !ok && f.Label != "" {
			label = strings.ToUpper(f.Label[:1]) + strings.ReplaceAll(f.Label[1:], "_", " ")
		}
		out = append(out, field{label, slices.DeleteFunc(slices.Clone(f.Lines), func(l string) bool { return l == "" })})
	}
	return out
}

// messageCells fills To, the subject, and the preview. A reply goes to the original's recipients too, which only
// the action page reads, so its To is marked reply or reply all before the cc and bcc of the request.
func messageCells(row *listRow, op string, args map[string]any) {
	type group struct {
		label string
		list  []string
	}
	if op == "draft.reply" || op == "mail.reply" {
		row.ToMarker = "reply"
		if all, _ := args["all"].(bool); all {
			row.ToMarker = "reply all"
		}
	}
	groups := []group{{"", texts(args["to"])}, {"cc: ", texts(args["cc"])}, {"bcc (hidden): ", texts(args["bcc"])}}
	join := func(short func(string) string) string {
		var parts []string
		for _, g := range groups {
			if len(g.list) == 0 {
				continue
			}
			list := make([]string, len(g.list))
			for j, a := range g.list {
				list[j] = short(a)
			}
			parts = append(parts, g.label+strings.Join(list, ", "))
		}
		return strings.Join(parts, "; ")
	}
	row.To, row.ToTitle = join(shortAddress), join(func(a string) string { return a })
	if row.ToMarker != "" {
		row.ToTitle = strings.TrimSuffix(row.ToMarker+"; "+row.ToTitle, "; ")
	}
	if subject, ok := args["subject"].(string); ok {
		row.Subject, row.NoSubject = subject, subject == ""
		if row.NoSubject {
			row.Subject = "No subject"
		}
	}
	if row.Body != "" {
		row.Preview = preview(row.Body, args["html"] == true)
	}
}

// meetingCells fills Event, When, People, and Change from the stored request. Only an invitation stores its event;
// for the others the event is an ID.
func meetingCells(row *listRow, op string, args map[string]any) {
	row.When, row.People = "—", "—"
	str := func(key string) string { s, _ := args[key].(string); return s }
	switch op {
	case "calendar.invite":
		row.Event, row.Change, row.ChangeDetail = str("subject"), "New event", str("location")
		row.When = eventWhen(str("start"), str("end"))
		row.People = people(len(texts(args["to"])) + len(texts(args["optional"])))
	case "calendar.update":
		row.EventMissing = true
		row.Change, row.ChangeDetail = updateChange(args, nil)
	case "calendar.cancel":
		row.EventMissing, row.Change, row.ChangeDetail = true, "Cancel event", str("comment")
	case "calendar.respond":
		row.EventMissing, row.People, row.PeopleRole, row.ChangeDetail = true, "Organizer", true, str("comment")
		row.Change = map[string]string{"accept": "Accept", "tentative": "Tentative", "decline": "Decline"}[str("response")]
	}
	if row.EventMissing {
		row.Event, row.EventPlaceholder = "Event not available", true
		row.ref = str("id")
	}
}

// updateChange is the Change of a meeting update and its detail. With the event's current view, a new start or end
// shows as a change from the current time; without it, as the new time only.
func updateChange(args map[string]any, current *views.CalendarView) (string, string) {
	str := func(key string) string { s, _ := args[key].(string); return s }
	var changes, details []string
	for _, key := range []string{"start", "end"} {
		if _, ok := args[key]; !ok {
			continue
		}
		label := strings.ToUpper(key[:1]) + key[1:]
		old := ""
		if current != nil {
			old = map[string]string{"start": current.Start, "end": current.End}[key]
		}
		if from, ok := timeChange(old, str(key)); ok {
			changes, details = append(changes, label), append(details, from)
		} else {
			changes = append(changes, label+" → "+shortTime(str(key)))
		}
	}
	if _, ok := args["duration"]; ok {
		changes = append(changes, "Duration → "+str("duration"))
	}
	if n := len(texts(args["add"])); n > 0 {
		changes = append(changes, "+"+people(n))
	}
	if n := len(texts(args["remove"])); n > 0 {
		changes = append(changes, "−"+people(n))
	}
	for _, key := range []string{"subject", "location"} {
		if _, ok := args[key]; ok {
			changes = append(changes, strings.ToUpper(key[:1])+key[1:])
			details = append(details, str(key))
		}
	}
	if _, ok := args["body"]; ok {
		changes = append(changes, "Text")
	}
	if args["online"] == true {
		changes = append(changes, "Online meeting")
	}
	return strings.Join(changes, ", "), strings.Join(details, " · ")
}

// timeChange is a change from the time old to the time to, both RFC 3339, in the offset of to: "10:00 → 11:00" on
// one day, with the dates otherwise. ok is false when a time is not RFC 3339.
func timeChange(old, to string) (string, bool) {
	o, err := time.Parse(time.RFC3339, old)
	n, err2 := time.Parse(time.RFC3339, to)
	if err != nil || err2 != nil {
		return "", false
	}
	o = o.In(n.Location())
	if o.Format(time.DateOnly) == n.Format(time.DateOnly) {
		return o.Format("15:04") + " → " + n.Format("15:04"), true
	}
	return o.Format("Mon 2 Jan, 15:04") + " → " + n.Format("Mon 2 Jan, 15:04"), true
}

func people(n int) string {
	if n == 1 {
		return "1 person"
	}
	return strconv.Itoa(n) + " people"
}

// eventWhen is a start and end as "Wed 1 Apr, 09:00–10:00", in the offset that the agent or the provider gave, or —
// when a time is not RFC 3339.
func eventWhen(start, end string) string { return timeSpan(start, end, "Mon 2 Jan, 15:04") }

// eventWhenLong is eventWhen with the year: "Wed 1 Apr 2026, 09:00–10:00".
func eventWhenLong(start, end string) string { return timeSpan(start, end, longTime) }

func timeSpan(start, end, layout string) string {
	s, err := time.Parse(time.RFC3339, start)
	e, err2 := time.Parse(time.RFC3339, end)
	if err != nil || err2 != nil {
		return "—"
	}
	if s.Format(time.DateOnly) == e.Format(time.DateOnly) {
		return s.Format(layout) + "–" + e.Format("15:04")
	}
	return s.Format(layout) + " – " + e.Format(layout)
}

// shortTime is a stored time as "Wed 1 Apr, 09:00", or the stored text when it is not RFC 3339.
func shortTime(v string) string {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return v
	}
	return t.Format("Mon 2 Jan, 15:04")
}

// shortAddressRunes is the length above which shortAddress cuts an address.
const shortAddressRunes = 28

// shortAddress cuts a long address in the middle of its local part, on rune boundaries, and keeps the domain whole:
// accounts…@vendor-billing.example.com.
func shortAddress(addr string) string {
	if utf8.RuneCountInString(addr) <= shortAddressRunes {
		return addr
	}
	at := strings.LastIndex(addr, "@")
	if at < 0 {
		return string([]rune(addr)[:shortAddressRunes]) + "…"
	}
	local, domain := []rune(addr[:at]), addr[at:]
	keep := max(8, shortAddressRunes-utf8.RuneCountInString(domain))
	if keep >= len(local) {
		return addr
	}
	return string(local[:keep]) + "…" + domain
}

// previewRunes caps the preview of a body.
const previewRunes = 200

// preview is a one-line preview of a body: the first line with text of a plain-text body, or the text of an HTML
// body, with whitespace collapsed.
func preview(body string, isHTML bool) string {
	text := ""
	if isHTML {
		text = htmlText(body, previewRunes)
	} else {
		for line := range strings.Lines(body) {
			if text = strings.Join(strings.Fields(line), " "); text != "" {
				break
			}
		}
	}
	if r := []rune(text); len(r) > previewRunes {
		return string(r[:previewRunes]) + "…"
	}
	return text
}

// htmlText is the text of an HTML body: tags and comments removed, the content of style, script, and head dropped,
// entities decoded, whitespace collapsed. It stops once the text has more than limit runes, so that a long body costs
// only the start that the preview shows. It is no security boundary: the template escapes its result like any text.
func htmlText(s string, limit int) string {
	var b strings.Builder
	text := func() string { return strings.Join(strings.Fields(html.UnescapeString(b.String())), " ") }
	// The text is counted only when the collected bytes pass check, which doubles, so the counts cost at most twice the
	// text. A rune has at least one byte, so fewer bytes than limit are never too many runes.
	for check := limit; ; {
		if b.Len() > check {
			if utf8.RuneCountInString(text()) > limit {
				break
			}
			check *= 2
		}
		i := strings.IndexByte(s, '<')
		if i < 0 || i+1 == len(s) {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		s = s[i:]
		// A < that starts no tag is text.
		if c := s[1]; !(c == '/' || c == '!' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			b.WriteByte('<')
			s = s[1:]
			continue
		}
		b.WriteByte(' ')
		if strings.HasPrefix(s, "<!--") {
			end := strings.Index(s, "-->")
			if end < 0 {
				break
			}
			s = s[end+len("-->"):]
			continue
		}
		end := tagEnd(s)
		if end < 0 {
			break
		}
		closing, name := s[1] == '/', tagName(s[1:end])
		s = s[end+1:]
		if !closing && (name == "style" || name == "script" || name == "head") {
			s = s[closingTag(s, name):]
		}
	}
	return text()
}

// tagEnd is the index of the > that ends the tag at the start of s, outside quotes, or -1.
func tagEnd(s string) int {
	var quote byte
	for i := 1; i < len(s); i++ {
		switch c := s[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '>':
			return i
		}
	}
	return -1
}

// tagName is the lowercase name of a tag from its text between < and >.
func tagName(tag string) string {
	tag = strings.TrimPrefix(tag, "/")
	if end := strings.IndexAny(tag, " \t\r\n\f/"); end >= 0 {
		tag = tag[:end]
	}
	return strings.ToLower(tag)
}

// closingTag is the index of the closing tag of name in s, in any letter case, or len(s) when there is none. As in a
// browser, the name ends at a space, / or >, so </scripture> does not close a script.
func closingTag(s, name string) int {
	for i := 0; ; {
		j := strings.Index(s[i:], "</")
		if j < 0 {
			return len(s)
		}
		i += j
		if end := i + 2 + len(name); end < len(s) && strings.EqualFold(s[i+2:end], name) && strings.IndexByte(" \t\n\f\r/>", s[end]) >= 0 {
			return i
		}
		i += 2
	}
}
