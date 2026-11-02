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
	"unicode/utf8"

	"github.com/ivanrvpereira/agentmailguard/internal/cache"
	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// listGroup is one table of the list page. Key names it in URLs and in the data-group attribute.
type listGroup struct{ Key, Title, Hint, Class string }

const (
	groupMessages = "messages"
	groupMeetings = "meetings"
	groupMailbox  = "mailbox"
)

// listGroups are the list tables in page order.
var listGroups = []listGroup{
	{groupMessages, "Messages", "Sends go out when approved. Drafts are only saved or deleted.", "t-mail"},
	{groupMeetings, "Meetings", "Approving notifies the attendees or the organizer.", "t-meet"},
	{groupMailbox, "Mailbox operations", "Changes to existing messages. Score is the message's screening score. Trash is a move, never a permanent delete.", "t-box"},
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

// listQuery is the validated filter of the list page; zero shows everything. key is the account key of alias.
type listQuery struct{ alias, key, show string }

// parseListQuery validates the list query against the loaded configuration. A key that is not account or show,
// a repeated key, or an unknown value gives the unfiltered page.
func parseListQuery(raw string, cfg config.Config) listQuery {
	values, err := url.ParseQuery(raw)
	if err != nil {
		return listQuery{}
	}
	var q listQuery
	for key, v := range values {
		if len(v) != 1 {
			return listQuery{}
		}
		switch key {
		case "account":
			a, ok := cfg.Account(v[0])
			if !ok {
				return listQuery{}
			}
			q.alias, q.key = a.Alias, a.Key()
		case "status":
			if v[0] != showReview && v[0] != showOthers {
				return listQuery{}
			}
			q.show = v[0]
		default:
			return listQuery{}
		}
	}
	return q
}

// url is the list page with the filter, its parameters in a fixed order.
func (q listQuery) url() string {
	v := url.Values{}
	if q.alias != "" {
		v.Set("account", q.alias)
	}
	if q.show != "" {
		v.Set("status", q.show)
	}
	if len(v) == 0 {
		return "/"
	}
	return "/?" + v.Encode()
}

// shows reports whether a waiting row passes the filter.
func (q listQuery) shows(r listRow) bool {
	return (q.key == "" || r.Account == q.key) && (q.show == "" || (q.show == showReview) == r.Review)
}

type listPage struct {
	// Waiting and Review count the waiting rows that the filter shows, and those that need review.
	Waiting, Review int
	Accounts, Shows []filterLink
	Groups          []groupView
}

type filterLink struct {
	Label, Title, URL string
	Count             int
	Current           bool
}

type groupView struct {
	listGroup
	Rows, Recents []listRow
}

// listRow is one pending action in a list table. Every text in it can come from the agent or from mail.
type listRow struct {
	ID, Group, Label, Alias, Account, Age string
	Score                                 scoreCell
	// Review is true when the action needs review: a Messages or Meetings action whose check found a risk or failed.
	Review bool
	// Messages: the reply marker, the recipients shortened and in full, the subject, and a one-line preview of the body.
	ToMarker, To, ToTitle, Subject, Preview string
	NoSubject                               bool
	// Meetings. EventMissing is true when the event is only an ID; PeopleRole when People names a role, not a count.
	Event, When, People, Change, ChangeDetail string
	EventMissing, PeopleRole                  bool
	// Recents: a summary from the stored fields, and the outcome, failed or not.
	Summary, Outcome string
	Failed           bool
	// The open row: every request field, the draft fields of a draft send, and the body or meeting text under its
	// caption.
	Fields, Draft []field
	Body, Caption string
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
	q := parseListQuery(r.URL.RawQuery, st.config)
	// Recents follow the Account filter; the waiting actions of every account give the filter counts.
	items, err := c.s.cache.ListPending(ctx, recentsListed, q.key)
	if err != nil {
		plain(w, 500, "approvals unavailable")
		return
	}
	aliases := map[string]string{}
	for _, a := range st.config.Accounts {
		aliases[a.Key()] = a.Alias
	}
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
	page := listPage{}
	count := func(f listQuery) int {
		n := 0
		for _, row := range waiting {
			if f.shows(row) {
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
		page.Accounts = append(page.Accounts, link(a.Alias, a.Key(), listQuery{a.Alias, a.Key(), q.show}, q.key == a.Key()))
	}
	page.Shows = []filterLink{
		link("All", "", listQuery{q.alias, q.key, ""}, q.show == ""),
		link("Needs review", "", listQuery{q.alias, q.key, showReview}, q.show == showReview),
		link("Others", "", listQuery{q.alias, q.key, showOthers}, q.show == showOthers),
	}
	for _, g := range listGroups {
		view := groupView{listGroup: g, Recents: recents[g.Key]}
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
	render(w, 200, "list", page)
}

// listedRow builds the row of a pending action of a listed operation from its stored fields only.
func listedRow(item cache.Pending, aliases map[string]string, now time.Time) listRow {
	op := listedOps[item.Op]
	alias, ok := aliases[item.Account]
	if !ok {
		alias = item.Account
	}
	row := listRow{ID: item.ID, Group: op.group, Label: op.label, Alias: alias, Account: item.Account, Age: age(now.Sub(item.Created)), Score: checkScore(item.Jev)}
	// Mailbox operations carry no action check; their score is the message's screening verdict, which the list
	// does not read yet.
	if op.group == groupMailbox {
		row.Score = notChecked
	}
	row.Review = op.group != groupMailbox && row.Score.Risk
	args := storedFields(item.Args)
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
			row.Caption = caption + ", HTML source as the agent wrote it, not rendered"
		}
	}
	switch op.group {
	case groupMessages:
		messageCells(&row, item.Op, fields)
	case groupMeetings:
		meetingCells(&row, item.Op, args)
	}
	switch item.State {
	case cache.StateDone:
		row.Outcome = "Done"
	case cache.StateDenied:
		row.Outcome = "Denied"
	case cache.StateWithdrawn:
		row.Outcome = "Withdrawn"
	case cache.StateFailed:
		row.Outcome, row.Failed = "Failed", true
		if item.Result != nil {
			row.Outcome += ": " + *item.Result
		}
	}
	if item.State != cache.StatePending {
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
		var changes, details []string
		for _, key := range []string{"start", "end"} {
			if _, ok := args[key]; ok {
				changes = append(changes, strings.ToUpper(key[:1])+key[1:]+" → "+shortTime(str(key)))
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
		row.Change, row.ChangeDetail = strings.Join(changes, ", "), strings.Join(details, " · ")
	case "calendar.cancel":
		row.EventMissing, row.Change, row.ChangeDetail = true, "Cancel event", str("comment")
	case "calendar.respond":
		row.EventMissing, row.People, row.PeopleRole, row.ChangeDetail = true, "Organizer", true, str("comment")
		row.Change = map[string]string{"accept": "Accept", "tentative": "Tentative", "decline": "Decline"}[str("response")]
	}
	if row.EventMissing {
		row.Event = "Event not available"
	}
}

func people(n int) string {
	if n == 1 {
		return "1 person"
	}
	return strconv.Itoa(n) + " people"
}

// eventWhen is a stored start and end as "Wed 1 Apr, 09:00–10:00", in the offset that the agent gave, or — when a
// time is not RFC 3339.
func eventWhen(start, end string) string {
	s, err := time.Parse(time.RFC3339, start)
	e, err2 := time.Parse(time.RFC3339, end)
	if err != nil || err2 != nil {
		return "—"
	}
	if s.Format(time.DateOnly) == e.Format(time.DateOnly) {
		return s.Format("Mon 2 Jan, 15:04") + "–" + e.Format("15:04")
	}
	return s.Format("Mon 2 Jan, 15:04") + " – " + e.Format("Mon 2 Jan, 15:04")
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
		text = htmlText(body)
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
// entities decoded, whitespace collapsed. It is no security boundary: the template escapes its result like any text.
func htmlText(s string) string {
	var b strings.Builder
	for {
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
	return strings.Join(strings.Fields(html.UnescapeString(b.String())), " ")
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

// closingTag is the index of the closing tag of name in s, in any letter case, or len(s) when there is none.
func closingTag(s, name string) int {
	for i := 0; ; {
		j := strings.Index(s[i:], "</")
		if j < 0 {
			return len(s)
		}
		i += j
		if end := i + 2 + len(name); end <= len(s) && strings.EqualFold(s[i+2:end], name) {
			return i
		}
		i += 2
	}
}
