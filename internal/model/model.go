// Package model defines provider-independent internal data, not agent-facing JSON views.
// All message and event text remains untrusted. Never serialize these types to a client.
package model

import (
	"context"
	"errors"
	"regexp"
	"time"
)

type MessageID = string
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
	Sender          Address
	Subject         string
	ReceivedAt      time.Time
	To, CC          []Address
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
type SearchQuery struct {
	Text, Sender, Subject *string
	After, Before         *time.Time
	Folder                *FolderID
}
type NewDraft struct {
	To            []string
	Subject, Body string
}

// Error must contain only a safe error code/status, never a provider response body.
type MutationResult struct {
	ID    MessageID
	OK    bool
	Error *string
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

// Mailbox deliberately has neither a send operation nor a permanent-delete operation.
// List/Search may return bodies already available even when body is false.
// ScanInbox reads all pages before returning, metadata only, excluding drafts,
// with since <= received_at < until (nil until means no upper bound).
type Mailbox interface {
	List(ctx context.Context, folder FolderID, count, skip int, body bool) ([]Message, error)
	Search(ctx context.Context, query SearchQuery, count int, body bool) ([]Message, error)
	Get(ctx context.Context, ids []MessageID, body bool) ([]Message, error)
	ScanInbox(ctx context.Context, since time.Time, until *time.Time) (InboxScan, error)
	Folders(ctx context.Context) ([]Folder, error)
	Archive(ctx context.Context, ids []MessageID) ([]MutationResult, error)
	Trash(ctx context.Context, ids []MessageID) ([]MutationResult, error)
	Move(ctx context.Context, ids []MessageID, dest FolderID) ([]MutationResult, error)
	MarkRead(ctx context.Context, ids []MessageID, read bool) ([]MutationResult, error)
	Flag(ctx context.Context, ids []MessageID, state FlagState) ([]MutationResult, error)
	Categorize(ctx context.Context, ids []MessageID, add, remove []string) ([]MutationResult, error)
	Categories(ctx context.Context) ([]string, error)
	DraftNew(ctx context.Context, draft NewDraft) (MessageID, error)
	DraftReply(ctx context.Context, id MessageID, comment string) (MessageID, error)
	// DraftForward must be provider-built; unsupported providers must fail, not substitute a new draft.
	DraftForward(ctx context.Context, id MessageID, to []string) (MessageID, error)
	// DraftUpdate is restricted to new drafts, and returns the possibly changed message ID.
	DraftUpdate(ctx context.Context, id MessageID, body string) (MessageID, error)
}
type Calendar interface {
	List(ctx context.Context, start, end time.Time) ([]Event, error)
	Get(ctx context.Context, id EventID) (Event, error)
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
		total := 0.0
		for name, probability := range f.Probabilities {
			if !ruleName.MatchString(name) || !unit(probability) {
				return errors.New("invalid answer folder")
			}
			total += probability
		}
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

// Screener must return an Error verdict for missing/failed screening, with safe error text only. The Go error return is for cancellation; callers must never treat a zero result as green.
type Screener interface {
	Screen(ctx context.Context, state map[string]any) (Screening, error)
	Classify(ctx context.Context, state map[string]any, security bool, organization *OrganizationQuestions) (Classification, error)
}
