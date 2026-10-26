// Package model defines provider-independent internal data, not agent-facing JSON views.
// All message and event text remains untrusted. Never serialize these types to a client.
package model

import (
	"context"
	"errors"
	"html"
	"regexp"
	"slices"
	"strings"
	"time"
)

type MessageID = string

// ThreadID is the provider's opaque conversation ID. Empty: the provider gave none, so the message joins no thread.
type ThreadID = string

type FolderID = string
type EventID = string

// MailboxError carries safe status/code text only. Adapters must not construct it from raw provider error messages.
type MailboxError string

func (e MailboxError) Error() string { return string(e) }

type FlagState string

const (
	Flagged  FlagState = "flagged"
	Complete FlagState = "complete"
	NoFlag   FlagState = "none"
)

type ContentType string

const (
	HTML ContentType = "html"
	Text ContentType = "text"
)

type Address struct{ Name, Address string }
type AuthResults struct{ SPF, DKIM, DMARC, CompAuth, CompAuthReason *string }

var authToken = regexp.MustCompile(`^[a-z]+$`)
var authReason = regexp.MustCompile(`^[0-9]+$`)

func (a AuthResults) Validate() error {
	for _, token := range []*string{a.SPF, a.DKIM, a.DMARC, a.CompAuth} {
		if token != nil && !authToken.MatchString(*token) {
			return errors.New("invalid authentication result")
		}
	}
	if a.CompAuthReason != nil && !authReason.MatchString(*a.CompAuthReason) {
		return errors.New("invalid authentication reason")
	}
	return nil
}

// Adapters extract unqualified error type names (for example SyntaxError or
// url.Error's name Error), or map errors without a typed code to a fixed code.
// Never pass error.Error() or provider response text to persistence.
const errorType = `(?:[A-Za-z][A-Za-z0-9_]*)?(?:Error|Exception)`

var failureText = regexp.MustCompile(`^(?:` + errorType + `(?: [1-5][0-9]{2})?|render ` + errorType + `|invalid answer (?:injection|account_security|folder|category)|(?:Graph|Gmail|Calendar) error (?:[1-5][0-9]{2}(?: [A-Za-z][A-Za-z0-9_]*)?|` + errorType + `|RequestFailed|InvalidPage|InvalidMetadata))$`)

func ValidateFailureText(text *string) error {
	if text != nil && *text != "failed" && !failureText.MatchString(*text) {
		return errors.New("invalid error metadata")
	}
	return nil
}

type Message struct {
	ID              MessageID
	ThreadID        ThreadID
	Sender          Address
	Subject         string
	ReceivedAt      time.Time
	To, CC          []Address
	BCC             []Address // filled where the provider returns it: drafts and sent mail
	Folders         []FolderID
	Attachments     []string
	IsDraft, IsRead bool
	Flag            FlagState
	Categories      []string
	Auth            *AuthResults // nil: headers not fetched
	Body            *string      // nil: not fetched; pointer to "": fetched and empty
	ContentType     *ContentType
}
type Folder struct {
	ID   FolderID
	Name string
	// Trash marks the provider's deleted-items folder and every folder below it. Moves there must use trash instead.
	Trash bool
}
type Attendee struct {
	Address  Address
	Response string
}
type Event struct {
	ID          EventID
	Subject     string
	Start, End  time.Time
	IsAllDay    bool
	Recurrence  *string
	Location    string
	Organizer   Address
	IsOrganizer bool
	Attendees   []Attendee
	JoinURL     *string
	Attachments []string
	IsPrivate   bool
	Categories  []string
	ShowAs      string
	IsCancelled bool
	Body        *string
	ContentType *ContentType
}

// SearchQuery selects messages. After and Before are exact instants: a message matches when After <= received
// time < Before. Nil means no bound.
type SearchQuery struct {
	Text, Sender, To, Subject *string
	After, Before             *time.Time
	Folder                    *FolderID
}

// RangeValid reports whether the bounds leave time for a match: Before, when both are set, is after After.
func (q SearchQuery) RangeValid() bool {
	return q.After == nil || q.Before == nil || q.Before.After(*q.After)
}

// UnknownReceived is the ReceivedAt that adapters set when the provider gives no received time.
var UnknownReceived = time.Unix(0, 0).UTC()

// Received reports whether a message received at t is inside [After, Before). An unknown received time is outside
// any bound.
func (q SearchQuery) Received(t time.Time) bool {
	if q.After == nil && q.Before == nil {
		return true
	}
	return !t.Equal(UnknownReceived) && (q.After == nil || !t.Before(*q.After)) && (q.Before == nil || t.Before(*q.Before))
}

// NewDraft is a new message. Every field is optional for a draft; a send needs a recipient (Addressed). HTML marks
// Body as HTML source, sent unchanged; otherwise Body is plain text.
type NewDraft struct {
	To, CC, BCC   []string
	Subject, Body string
	HTML          bool
}

// Addressed reports a recipient in To, CC, or BCC, which a send needs: both providers refuse a send without one.
func (d NewDraft) Addressed() bool { return len(d.To)+len(d.CC)+len(d.BCC) > 0 }

// DraftResult is the draft that a provider made: its message ID, its conversation (empty: the provider gave
// none), and its recipients and subject, from the provider's answer or from what the adapter built. Like all
// provider data, it is untrusted.
type DraftResult struct {
	ID          MessageID
	ThreadID    ThreadID
	To, CC, BCC []string
	Subject     string
}

// PageRequest asks for one page of a list or search. Size 0 sends no size, so the provider's default page size
// applies. Page "" is the first page; otherwise it is the Next of an earlier result for the same request: untrusted
// agent input that the adapter checks for form; the Microsoft 365 mail adapter also refuses one from another request.
type PageRequest struct {
	Size int
	Page string
}

// MessagePage is one page of messages. Next is "" on the last page, else the provider's marker for the next page.
type MessagePage struct {
	Messages []Message
	Next     string
}

// EventPage is one page of events. Next is "" on the last page, else the provider's marker for the next page.
type EventPage struct {
	Events []Event
	Next   string
}

// MaxPage bounds the length of a page marker, from the agent and from the provider.
const MaxPage = 16 << 10

// ValidPage reports whether s can be a page marker: at most MaxPage bytes of printable ASCII without spaces. "" is
// no marker.
func ValidPage(s string) bool {
	return len(s) <= MaxPage && !strings.ContainsFunc(s, func(r rune) bool { return r <= ' ' || r > '~' })
}

// Error must contain only a safe error code/status, never a provider response body.
// ApprovalID is set only by the guard, for an action stored for approval (Error "pending").
type MutationResult struct {
	ID         MessageID
	OK         bool
	Error      *string
	ApprovalID *string
}
type InboxEntry struct {
	ID         MessageID
	ReceivedAt time.Time
}
type ScanFailure struct {
	ID    *MessageID
	Error string
}
type InboxScan struct {
	Messages []InboxEntry
	Failures []ScanFailure
}

// Mailbox deliberately has no permanent-delete operation. Its send operations reach real recipients at once.
// List/Search/Thread may return bodies already available even when body is false.
// ScanInbox reads all pages before returning, metadata only, excluding drafts,
// with since <= received_at < until (nil until means no upper bound).
type Mailbox interface {
	List(ctx context.Context, folder FolderID, page PageRequest, body bool) (MessagePage, error)
	Search(ctx context.Context, query SearchQuery, page PageRequest, body bool) (MessagePage, error)
	Get(ctx context.Context, ids []MessageID, body bool) ([]Message, error)
	// Thread returns every message of the conversation that the provider gives, newest first, from every folder
	// (trash, spam or junk, sent, and drafts included), metadata only like List. An unknown thread is the adapter's
	// not-found error, as for an unknown message ID.
	Thread(ctx context.Context, thread ThreadID) ([]Message, error)
	ScanInbox(ctx context.Context, since time.Time, until *time.Time) (InboxScan, error)
	Folders(ctx context.Context) ([]Folder, error)
	Archive(ctx context.Context, ids []MessageID) ([]MutationResult, error)
	Trash(ctx context.Context, ids []MessageID) ([]MutationResult, error)
	Move(ctx context.Context, ids []MessageID, dest FolderID) ([]MutationResult, error)
	MarkRead(ctx context.Context, ids []MessageID, read bool) ([]MutationResult, error)
	Flag(ctx context.Context, ids []MessageID, state FlagState) ([]MutationResult, error)
	Categorize(ctx context.Context, ids []MessageID, add, remove []string) ([]MutationResult, error)
	Categories(ctx context.Context) ([]string, error)
	DraftNew(ctx context.Context, draft NewDraft) (DraftResult, error)
	DraftReply(ctx context.Context, id MessageID, comment string) (MessageID, error)
	// DraftForward must be provider-built; unsupported providers must fail, not substitute a new draft.
	DraftForward(ctx context.Context, id MessageID, to []string) (MessageID, error)
	// DraftUpdate is restricted to new drafts, and returns the possibly changed message ID.
	DraftUpdate(ctx context.Context, id MessageID, body string) (MessageID, error)
	// SendNew, SendReply, and SendForward send mail at once, with the same content as the matching draft
	// operation. A send request that may have reached the provider is never sent again.
	SendNew(ctx context.Context, draft NewDraft) error
	SendReply(ctx context.Context, id MessageID, comment string) error
	SendForward(ctx context.Context, id MessageID, to []string) error
}
type Calendar interface {
	List(ctx context.Context, start, end time.Time) ([]Event, error)
	Get(ctx context.Context, id EventID) (Event, error)
	// Search returns one page of the events that the provider's own search finds for text, in the provider's
	// default size and order, each with every field that Get reads. page is "" for the first page, else the Next
	// of an earlier result for the same text: untrusted, so the adapter refuses a marker of the wrong form.
	Search(ctx context.Context, text, page string) (EventPage, error)
}

type NewEvent struct {
	Subject, Body, Location string
	HTML                    bool // Body is HTML source, sent unchanged; otherwise plain text (EventBodyHTML)
	Start, End              time.Time
	Attendees               []string
	Optional                []string // optional attendees; they receive the invitation too
	Online                  bool     // adds the provider's online meeting: Teams on M365, Google Meet on Gmail
}

// \r\n comes first so it gives one <br>.
var htmlLineBreaks = strings.NewReplacer("\r\n", "<br>", "\r", "<br>", "\n", "<br>")

// HTMLBody returns a plain-text event body as escaped HTML that keeps the line breaks. Both providers render
// event bodies as HTML, so body text never becomes markup or a disguised link.
func HTMLBody(text string) string { return htmlLineBreaks.Replace(html.EscapeString(text)) }

// EventBodyHTML is the HTML that a provider gets as an event body: HTML source unchanged when isHTML, plain
// text escaped by HTMLBody otherwise.
func EventBodyHTML(body string, isHTML bool) string {
	if isHTML {
		return body
	}
	return HTMLBody(body)
}

// CalendarTimeValid reports whether t has a year from 1 to 9999 in its own zone and in UTC.
func CalendarTimeValid(t time.Time) bool {
	return t.Year() >= 1 && t.Year() <= 9999 && t.UTC().Year() >= 1 && t.UTC().Year() <= 9999
}

type EventChange struct {
	Subject, Location             *string
	Body                          *string // replaces the whole body; plain text unless HTML
	HTML                          bool    // Body is HTML source, sent unchanged; valid only with Body (HTMLWithoutBody)
	Start, End                    *time.Time
	AddAttendees, RemoveAttendees []string
	Online                        bool // true adds an online meeting if none exists; false leaves it unchanged
}

// Empty reports a change with no field set. A duration given beside it is a change too.
func (c EventChange) Empty() bool {
	return c.Subject == nil && c.Location == nil && c.Body == nil && c.Start == nil && c.End == nil && len(c.AddAttendees)+len(c.RemoveAttendees) == 0 && !c.Online
}

// HTMLWithoutBody reports an HTML flag without a body: the body type goes only with a body.
func (c EventChange) HTMLWithoutBody() bool { return c.HTML && c.Body == nil }

// AttendeeConflict reports an address that is both added and removed, ignoring case.
func (c EventChange) AttendeeConflict() bool { return overlap(c.AddAttendees, c.RemoveAttendees) }

// AttendeeConflict reports an address that is both a required and an optional attendee, ignoring case.
func (e NewEvent) AttendeeConflict() bool { return overlap(e.Attendees, e.Optional) }

// overlap reports an address that is in both lists, ignoring case.
func overlap(a, b []string) bool {
	for _, x := range a {
		if slices.ContainsFunc(b, func(y string) bool { return strings.EqualFold(x, y) }) {
			return true
		}
	}
	return false
}

type MeetingResponse string

const (
	ResponseAccept    MeetingResponse = "accept"
	ResponseTentative MeetingResponse = "tentative"
	ResponseDecline   MeetingResponse = "decline"
)

func (r MeetingResponse) Valid() bool {
	return r == ResponseAccept || r == ResponseTentative || r == ResponseDecline
}

type EventResponse struct {
	Response MeetingResponse
	Comment  string
	NoNotify bool // true records the response without notifying the organizer; zero notifies
}

// Valid refuses an unknown response, and a comment without notification, which would reach nobody.
func (r EventResponse) Valid() bool { return r.Response.Valid() && !(r.NoNotify && r.Comment != "") }

// CalendarWriter is optional; read-only calendar providers need not implement it.
// Callers must resolve start-only changes into explicit Start and End.
type CalendarWriter interface {
	Invite(ctx context.Context, event NewEvent) (EventID, error)
	Update(ctx context.Context, id EventID, change EventChange) error
	Cancel(ctx context.Context, id EventID, comment string) error
	Respond(ctx context.Context, id EventID, response EventResponse) error
}

// AvailabilityInterval is fixed: slot i covers start+i*interval until the next slot or the requested end.
const AvailabilityInterval = 30 * time.Minute

type AvailabilityStatus string

const (
	SlotFree        AvailabilityStatus = "free"
	SlotTentative   AvailabilityStatus = "tentative"
	SlotBusy        AvailabilityStatus = "busy"
	SlotOutOfOffice AvailabilityStatus = "out_of_office"
	SlotUnknown     AvailabilityStatus = "unknown"
)

// Schedule is free/busy metadata for one requested address. It never carries event text.
type Schedule struct {
	Address string
	Slots   []AvailabilityStatus
}

// AvailabilitySlots counts slots from start to end; a partial final slot counts as one.
func AvailabilitySlots(start, end time.Time) int {
	if !end.After(start) {
		return 0
	}
	d := end.Sub(start)
	n := int(d / AvailabilityInterval)
	if d%AvailabilityInterval != 0 {
		n++
	}
	return n
}

// Availability requests name 1 to MaxAvailabilityAddresses addresses for a range shorter than MaxAvailabilityRange.
// These are Microsoft Graph getSchedule limits for one query
// (https://learn.microsoft.com/en-us/graph/outlook-get-free-busy-schedule, "Limits and error conditions").
const (
	MaxAvailabilityAddresses = 20
	MaxAvailabilityRange     = 62 * 24 * time.Hour
)

// ValidAvailabilityRequest refuses blank or duplicate (ignoring case) addresses, too many addresses,
// invalid times, an end not after the start, and a range of MaxAvailabilityRange or more.
func ValidAvailabilityRequest(addresses []string, start, end time.Time) bool {
	if len(addresses) == 0 || len(addresses) > MaxAvailabilityAddresses || !CalendarTimeValid(start) || !CalendarTimeValid(end) || !end.After(start) || end.Sub(start) >= MaxAvailabilityRange {
		return false
	}
	seen := map[string]bool{}
	for _, address := range addresses {
		key := strings.ToLower(address)
		if strings.TrimSpace(address) == "" || seen[key] {
			return false
		}
		seen[key] = true
	}
	return true
}

// CalendarAvailability is optional and read-only. It returns one schedule per requested address, in
// request order, labeled with the requested address. Missing or failed participant data is unknown.
type CalendarAvailability interface {
	Availability(ctx context.Context, addresses []string, start, end time.Time) ([]Schedule, error)
}

type Verdict string

const (
	Green  Verdict = "green"
	Yellow Verdict = "yellow"
	Sealed Verdict = "sealed"
	Red    Verdict = "red"
	Error  Verdict = "error"
)

// Decide accepts only validated probabilities. Invalid scores fail closed.
func Decide(injection, accountSecurity float64) Verdict {
	if !unit(injection) || !unit(accountSecurity) {
		return Error
	}
	if injection >= .40 {
		return Red
	}
	if accountSecurity >= .80 {
		return Sealed
	}
	if injection >= .30 {
		return Yellow
	}
	return Green
}
func unit(v float64) bool { return v >= 0 && v <= 1 }

type Screening struct {
	Verdict Verdict
	Score   *float64
	Error   *string
}
type FolderAnswer struct {
	Choice        string             `json:"choice"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

// Organization contains explicit answers only. It cannot carry instructions or raw responses.
type Organization struct {
	Folder     *FolderAnswer      `json:"folder"`
	Categories map[string]float64 `json:"categories"`
}

var ruleName = regexp.MustCompile(`^[a-z0-9-]+$`)

func ValidRuleName(name string) bool { return ruleName.MatchString(name) }

// Validate checks persisted answer fields, not whether current rules still match.
// Edited rules do not invalidate saved answers. The screener must additionally
// validate exact option/category sets against the questions it asked.
func (o Organization) Validate() error {
	if o.Folder != nil {
		f := o.Folder
		if !unit(f.Confidence) {
			return errors.New("invalid answer folder")
		}
		if _, ok := f.Probabilities[f.Choice]; !ok {
			return errors.New("invalid answer folder")
		}
		total, correction := 0.0, 0.0
		for name, probability := range f.Probabilities {
			if !ruleName.MatchString(name) || !unit(probability) {
				return errors.New("invalid answer folder")
			}
			// Match Python 3.12+ compensated sum without map-order rounding at the boundary.
			next := total + probability
			if total >= probability {
				correction += (total - next) + probability
			} else {
				correction += (probability - next) + total
			}
			total = next
		}
		total += correction
		if total < .98 || total > 1.02 {
			return errors.New("invalid answer folder")
		}
	}
	for name, probability := range o.Categories {
		if !ruleName.MatchString(name) || !unit(probability) {
			return errors.New("invalid answer category")
		}
	}
	return nil
}

type Classification struct {
	Screening    *Screening    // nil: security was not asked and the request succeeded
	Organization *Organization // nil: absent or invalid answers
	Error        *string       // safe code only
}

// ChoiceQuestion and OrganizationQuestions are internal request types, not Jev wire types.
type ChoiceQuestion struct {
	Instructions string
	Criteria     map[string]string
}
type OrganizationQuestions struct {
	Folder     *ChoiceQuestion
	Categories map[string]string
}

// ActionFlagThreshold is the Jev action score at or above which a write action counts as suspicious.
// The value is provisional and will be tuned.
const ActionFlagThreshold = 0.5

// ActionCheck is the result of Jev's check of one write action. It never carries action text.
// A check that failed has Error set, no Score, and Flag true. Fake is true only for a check that the fake
// keyword engine made, so that the approval console does not show it as a Jev check. The agent never sees it.
type ActionCheck struct {
	Flag  bool     `json:"flag"`
	Score *float64 `json:"score,omitempty"`
	Error *string  `json:"error,omitempty"`
	Fake  bool     `json:"fake,omitempty"`
}

// Failed reports a check without a valid score.
func (c ActionCheck) Failed() bool { return c.Error != nil }

// ActionScore is the check result for score. A score outside 0..1 is a failed check.
func ActionScore(score float64) ActionCheck {
	if !unit(score) {
		return ActionFailure("ValidationError")
	}
	return ActionCheck{Flag: score >= ActionFlagThreshold, Score: &score}
}

// ActionFailure is a failed check with the safe code.
func ActionFailure(code string) ActionCheck { return ActionCheck{Flag: true, Error: &code} }

// Screener must return an Error verdict for missing/failed screening, with safe error text only. The Go error return is for cancellation; callers must never treat a zero result as green.
// CheckAction returns a failed check, never a clean one, when the check cannot complete.
type Screener interface {
	Screen(ctx context.Context, state map[string]any) (Screening, error)
	Classify(ctx context.Context, state map[string]any, security bool, organization *OrganizationQuestions) (Classification, error)
	CheckAction(ctx context.Context, operation, effect string, state map[string]any) (ActionCheck, error)
}
