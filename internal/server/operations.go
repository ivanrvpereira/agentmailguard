package server

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/content"
	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/gmail"
	"github.com/ivanrvpereira/agentmailguard/internal/providers/m365"
	"github.com/ivanrvpereira/agentmailguard/internal/rules"
)

// providerGrants lists, for each provider, the grants of which one allows an operation. A provider without an
// entry needs no grant beyond its login scopes.
type providerGrants map[string][]string

// mailSendGrants allow mail.send, mail.reply, mail.forward, and draft.send. Gmail sends need no grant beyond gmail.modify.
var mailSendGrants = providerGrants{"m365": {"Mail.Send"}}

// calendarWriteGrants allow the meeting writes. Gmail logins made before calendar writes have only the read-only grant.
var calendarWriteGrants = providerGrants{"m365": {"Calendars.ReadWrite"}, "gmail": {gmail.CalendarWriteScope}}

// availabilityGrants allow calendar.availability on Microsoft 365.
var availabilityGrants = providerGrants{"m365": {"Calendars.ReadBasic", "Calendars.Read", "Calendars.ReadWrite"}}

func publicError(e error) (string, bool) {
	for _, fixed := range []error{errNoAccounts, errUnknownAccount} {
		if e == fixed {
			return fixed.Error(), true
		}
	}
	if e == guard.GuardError(guard.AuthFailed) {
		return signInFailed, true
	}
	if e, ok := e.(guard.GuardError); ok {
		switch e {
		case guard.CalendarWriteUnsupported, guard.CalendarAvailabilityUnsupported, guard.CalendarCancelCommentUnsupported, guard.CalendarNoNotifyUnsupported, "missing_scopes", "invalid request",
			guard.CalendarRecurring, guard.CalendarCancelled, guard.CalendarOrganizerOnly, guard.CalendarOrganizerCannotRespond, guard.DraftUpdateNotDraft,
			guard.DraftDeleteNotDraft, guard.DraftSendNotDraft:
			return string(e), true
		case "refused by policy: mail_draft", "refused by policy: mail_send", "refused by policy: calendar_invite", "refused by policy: calendar_update", "refused by policy: calendar_cancel", "refused by policy: calendar_respond":
			return string(e), true
		case "move refused: the destination must be a folder ID from `folders`", "move refused: use mail.trash", "flag state must be one of flagged, complete, none", "categorize needs at least one category to add or remove", "categorize refused: not in the category list",
			"link refused: message is red, not green", "link refused: message is yellow, not green", "link refused: message is sealed, not green", "link refused: message is error, not green",
			"search needs at least one of text, from, to, subject, after, before":
			return string(e), true
		}
	}
	return "", false
}

// call runs a validated request on one account.
type call func(*account) (any, error)

// decoder validates the complete typed params of an account operation before any account or provider access.
// A non-empty gmailRefusal refuses a valid request that Gmail cannot carry out.
type decoder func(ctx context.Context, s *Server, raw json.RawMessage) (c call, gmailRefusal guard.GuardError, err error)

// operation is one entry of the API. An account operation has decode and runs on one account; a daemon operation
// has daemon and runs on the daemon state.
type operation struct {
	decode decoder
	grants providerGrants
	daemon func(ctx context.Context, s *Server, st *state, raw json.RawMessage) (any, error)
}

// typed builds a decoder from a parameter struct with its defaults, a validation, and the run on one account.
func typed[P any](defaults P, valid func(*P) bool, run func(ctx context.Context, s *Server, p P, a *account) (any, error)) decoder {
	return func(ctx context.Context, s *Server, raw json.RawMessage) (call, guard.GuardError, error) {
		p := defaults
		if params(raw, &p) != nil || !valid(&p) {
			return nil, "", errInvalid
		}
		return func(a *account) (any, error) { return run(ctx, s, p, a) }, "", nil
	}
}

type none struct{}

func always[P any](*P) bool { return true }

// ids is the decoder of a mail action on one or more message IDs, or with thread on a conversation's messages in
// folder in (see targetValid).
func ids(run func(ctx context.Context, g *guard.Guard, ids []string) ([]model.MutationResult, error), conversation func(ctx context.Context, g *guard.Guard, thread, in string) ([]model.MutationResult, error)) decoder {
	return typed(idsParam{}, func(p *idsParam) bool { return targetValid(p.IDs, p.Thread, p.In) }, func(ctx context.Context, _ *Server, p idsParam, a *account) (any, error) {
		if p.Thread != nil {
			return mutations(conversation(ctx, a.guard, *p.Thread, inOrInbox(p.In)))
		}
		return mutations(run(ctx, a.guard, p.IDs))
	})
}

// targetValid checks the messages of an organize action: either one or more message IDs, or a thread (a
// conversation ID) with an optional in (a folder ID), never both. in needs a thread.
func targetValid(ids []string, thread, in *string) bool {
	if thread == nil {
		return in == nil && listValid(ids, 1, noMax)
	}
	return ids == nil && idValid(*thread) && (in == nil || idValid(*in))
}

// inOrInbox is the folder of a conversation action: in, or the Inbox, as mail.list defaults to it.
func inOrInbox(in *string) string {
	if in == nil {
		return model.Inbox
	}
	return *in
}

// operations is the whole API: one entry per operation name, and any other name is refused. No entry writes
// credentials, the console password, accounts, or settings: the API uses mail and never manages the daemon.
// TestAPIOperationsAreReviewed lists every entry.
var operations = map[string]operation{
	"stats":            {daemon: stats},
	"account.list":     {daemon: accountList},
	"approvals.status": {daemon: approvalStatus},
	"account.status": {decode: typed(none{}, always, func(ctx context.Context, s *Server, _ none, a *account) (any, error) {
		return s.status(ctx, a)
	})},
	"rules.list": {decode: typed(none{}, always, func(ctx context.Context, _ *Server, _ none, a *account) (any, error) {
		return ruleList(ctx, a)
	})},
	"mail.get": {decode: typed(idParam{}, func(p *idParam) bool { return idValid(p.ID) }, func(ctx context.Context, _ *Server, p idParam, a *account) (any, error) {
		return a.guard.Get(ctx, p.ID)
	})},
	"mail.list": {decode: typed(listParam{Folder: model.Inbox}, func(p *listParam) bool {
		return idValid(p.Folder) && p.Count >= 0 && model.ValidPage(p.Page)
	}, func(ctx context.Context, _ *Server, p listParam, a *account) (any, error) {
		return a.guard.List(ctx, p.Folder, model.PageRequest{Size: p.Count, Page: p.Page})
	})},
	"mail.thread": {decode: typed(threadParam{}, func(p *threadParam) bool { return idValid(p.ID) }, func(ctx context.Context, _ *Server, p threadParam, a *account) (any, error) {
		return a.guard.Thread(ctx, p.ID)
	})},
	"mail.search": {decode: mailSearch},
	"mail.link": {decode: typed(linkParam{}, func(p *linkParam) bool {
		return idValid(p.ID) && p.All != (len(p.Links) > 0) && listValid(p.Links, 0, 1000)
	}, func(ctx context.Context, _ *Server, p linkParam, a *account) (any, error) {
		return a.guard.Links(ctx, p.ID, p.Links)
	})},
	"mail.folders": {decode: typed(none{}, always, func(ctx context.Context, _ *Server, _ none, a *account) (any, error) {
		v, e := a.guard.Folders(ctx)
		out := make([]folderDTO, 0, len(v))
		for _, f := range v {
			out = append(out, folderDTO{f.ID, f.Name})
		}
		return out, e
	})},
	"mail.categories": {decode: typed(none{}, always, func(ctx context.Context, _ *Server, _ none, a *account) (any, error) {
		return a.guard.Categories(ctx)
	})},
	"mail.archive": {decode: ids(func(ctx context.Context, g *guard.Guard, ids []string) ([]model.MutationResult, error) {
		return g.Archive(ctx, ids)
	}, func(ctx context.Context, g *guard.Guard, thread, in string) ([]model.MutationResult, error) {
		return g.ArchiveConversation(ctx, thread, in)
	})},
	"mail.trash": {decode: ids(func(ctx context.Context, g *guard.Guard, ids []string) ([]model.MutationResult, error) {
		return g.Trash(ctx, ids)
	}, func(ctx context.Context, g *guard.Guard, thread, in string) ([]model.MutationResult, error) {
		return g.TrashConversation(ctx, thread, in)
	})},
	"mail.read": {decode: ids(func(ctx context.Context, g *guard.Guard, ids []string) ([]model.MutationResult, error) {
		return g.MarkRead(ctx, ids, true)
	}, func(ctx context.Context, g *guard.Guard, thread, in string) ([]model.MutationResult, error) {
		return g.MarkReadConversation(ctx, thread, in, true)
	})},
	"mail.unread": {decode: ids(func(ctx context.Context, g *guard.Guard, ids []string) ([]model.MutationResult, error) {
		return g.MarkRead(ctx, ids, false)
	}, func(ctx context.Context, g *guard.Guard, thread, in string) ([]model.MutationResult, error) {
		return g.MarkReadConversation(ctx, thread, in, false)
	})},
	"mail.move": {decode: typed(moveParam{}, func(p *moveParam) bool { return targetValid(p.IDs, p.Thread, p.In) && idValid(p.Folder) }, func(ctx context.Context, _ *Server, p moveParam, a *account) (any, error) {
		if p.Thread != nil {
			return mutations(a.guard.MoveConversation(ctx, *p.Thread, inOrInbox(p.In), p.Folder))
		}
		return mutations(a.guard.Move(ctx, p.IDs, p.Folder))
	})},
	"mail.flag": {decode: typed(flagParam{}, func(p *flagParam) bool {
		return listValid(p.IDs, 1, noMax) && (p.State == model.Flagged || p.State == model.NoFlag || p.State == model.Complete)
	}, func(ctx context.Context, _ *Server, p flagParam, a *account) (any, error) {
		return mutations(a.guard.Flag(ctx, p.IDs, p.State))
	})},
	"mail.categorize": {decode: typed(categoryParam{}, func(p *categoryParam) bool {
		return targetValid(p.IDs, p.Thread, p.In) && listValid(p.Add, 0, noMax) && listValid(p.Remove, 0, noMax)
	}, func(ctx context.Context, _ *Server, p categoryParam, a *account) (any, error) {
		if p.Thread != nil {
			return mutations(a.guard.CategorizeConversation(ctx, *p.Thread, inOrInbox(p.In), p.Add, p.Remove))
		}
		return mutations(a.guard.Categorize(ctx, p.IDs, p.Add, p.Remove))
	})},
	"mail.organize": {decode: mailOrganize},
	"mail.send":     {decode: newMessage(true), grants: mailSendGrants},
	"mail.reply":    {decode: replyMessage(true), grants: mailSendGrants},
	"mail.forward":  {decode: forwardMessage(true), grants: mailSendGrants},
	"draft.new":     {decode: newMessage(false)},
	"draft.reply":   {decode: replyMessage(false)},
	"draft.update":  {decode: draftUpdate},
	"draft.forward": {decode: forwardMessage(false)},
	"draft.delete": {decode: typed(idParam{}, func(p *idParam) bool { return idValid(p.ID) }, func(ctx context.Context, _ *Server, p idParam, a *account) (any, error) {
		return sent(a.guard.DraftDelete(ctx, p.ID))
	})},
	"draft.send": {decode: typed(idParam{}, func(p *idParam) bool { return idValid(p.ID) }, func(ctx context.Context, _ *Server, p idParam, a *account) (any, error) {
		return sent(a.guard.DraftSend(ctx, p.ID))
	}), grants: mailSendGrants},
	"calendar.get": {decode: typed(idParam{}, func(p *idParam) bool { return idValid(p.ID) }, func(ctx context.Context, _ *Server, p idParam, a *account) (any, error) {
		return a.guard.CalendarGet(ctx, p.ID)
	})},
	"calendar.list": {decode: calendarList},
	"calendar.search": {decode: typed(calendarSearchParam{}, func(p *calendarSearchParam) bool {
		return validString(p.Text, 4096, false) && model.ValidPage(p.Page)
	}, func(ctx context.Context, _ *Server, p calendarSearchParam, a *account) (any, error) {
		return a.guard.CalendarSearch(ctx, p.Text, p.Page)
	})},
	"calendar.availability": {decode: calendarAvailability, grants: availabilityGrants},
	"calendar.invite":       {decode: calendarInvite, grants: calendarWriteGrants},
	"calendar.update":       {decode: calendarUpdate, grants: calendarWriteGrants},
	"calendar.cancel":       {decode: calendarCancel, grants: calendarWriteGrants},
	"calendar.respond":      {decode: calendarRespond, grants: calendarWriteGrants},
}

// Operations lists the names of every API operation.
func Operations() []string { return slices.Sorted(maps.Keys(operations)) }

func params(b json.RawMessage, p any) error {
	typ := reflect.TypeOf(p).Elem()
	keys := make([]string, typ.NumField())
	for i := range keys {
		keys[i] = typ.Field(i).Tag.Get("json")
	}
	return strict(b, p, keys...)
}

// noMax is the max of a list that has no limit of ours: the provider refuses what it does not accept, and
// MaxRequestBytes bounds the request.
const noMax = math.MaxInt

func listValid(v []string, min, max int) bool {
	if len(v) < min || len(v) > max {
		return false
	}
	for _, s := range v {
		if !validString(s, 4096, false) {
			return false
		}
	}
	return true
}
func idValid(id string) bool { return validString(id, 4096, false) }

type idParam struct {
	ID string `json:"id"`
}
type idsParam struct {
	IDs    []string `json:"ids"`
	Thread *string  `json:"thread"`
	In     *string  `json:"in"`
}
type listParam struct {
	Folder string `json:"folder"`
	Count  int    `json:"count"`
	Page   string `json:"page"`
}
type threadParam struct {
	ID string `json:"id"`
}
type searchParam struct {
	Text    *string `json:"text"`
	Sender  *string `json:"from"`
	To      *string `json:"to"`
	Subject *string `json:"subject"`
	After   *string `json:"after"`
	Before  *string `json:"before"`
	Folder  *string `json:"folder"`
	Count   int     `json:"count"`
	Page    string  `json:"page"`
}
type linkParam struct {
	ID    string   `json:"id"`
	Links []string `json:"links"`
	All   bool     `json:"all"`
}
type moveParam struct {
	IDs    []string `json:"ids"`
	Thread *string  `json:"thread"`
	In     *string  `json:"in"`
	Folder string   `json:"folder"`
}
type flagParam struct {
	IDs   []string        `json:"ids"`
	State model.FlagState `json:"state"`
}
type categoryParam struct {
	IDs    []string `json:"ids"`
	Thread *string  `json:"thread"`
	In     *string  `json:"in"`
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}
type organizeParam struct {
	Since string  `json:"since"`
	Until *string `json:"until"`
	Apply bool    `json:"apply"`
}
type calendarParam struct {
	Start string `json:"start"`
	End   string `json:"end"`
}
type calendarSearchParam struct {
	Text string `json:"text"`
	Page string `json:"page"`
}
type availabilityParam struct {
	To    []string `json:"to"`
	Start string   `json:"start"`
	End   string   `json:"end"`
}

// Slot i covers start+i*interval_minutes; the last slot ends at end.
type availabilityDTO struct {
	Start     string        `json:"start"`
	End       string        `json:"end"`
	Interval  int           `json:"interval_minutes"`
	Schedules []scheduleDTO `json:"schedules"`
}
type scheduleDTO struct {
	Address string                     `json:"address"`
	Slots   []model.AvailabilityStatus `json:"slots"`
}
type newParam struct {
	To      []string `json:"to"`
	CC      []string `json:"cc"`
	BCC     []string `json:"bcc"`
	Subject string   `json:"subject"`
	Body    string   `json:"body"`
	HTML    bool     `json:"html"`
}
type replyParam struct {
	ID   string   `json:"id"`
	Body string   `json:"body"`
	HTML bool     `json:"html"`
	All  bool     `json:"all"`
	CC   []string `json:"cc"`
	BCC  []string `json:"bcc"`
}
type draftChangeParam struct {
	ID      string    `json:"id"`
	To      *[]string `json:"to"`
	CC      *[]string `json:"cc"`
	BCC     *[]string `json:"bcc"`
	Subject *string   `json:"subject"`
	Body    *string   `json:"body"`
	HTML    bool      `json:"html"`
}
type forwardParam struct {
	ID   string   `json:"id"`
	To   []string `json:"to"`
	CC   []string `json:"cc"`
	BCC  []string `json:"bcc"`
	Body string   `json:"body"`
	HTML bool     `json:"html"`
}
type mutationDTO struct {
	ID         string  `json:"id"`
	OK         bool    `json:"ok"`
	Error      *string `json:"error"`
	ApprovalID *string `json:"approval_id,omitempty"`
}

func mutations(v []model.MutationResult, e error) (any, error) {
	out := make([]mutationDTO, 0, len(v))
	for _, r := range v {
		out = append(out, mutationDTO{r.ID, r.OK, r.Error, r.ApprovalID})
	}
	return out, e
}

// pendingDTO is the result of a single action that the policy stored for approval. URL is the
// approval console page of the item.
type pendingDTO struct {
	Status     string `json:"status"`
	ApprovalID string `json:"approval_id"`
	URL        string `json:"url"`
}

type folderDTO struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
type draftDTO struct {
	ID string `json:"id"`
}

// draftIDsDTO names a draft: its provider IDs, which are not mail content. thread_id is left out when the
// provider gave none.
type draftIDsDTO struct {
	ID       string `json:"id"`
	ThreadID string `json:"thread_id,omitempty"`
}

// newDraftDTO is the result of draft.new, and of draft.reply and draft.forward when sourceContentShown: the draft
// that the provider made.
type newDraftDTO struct {
	draftIDsDTO
	To      []string `json:"to"`
	CC      []string `json:"cc"`
	BCC     []string `json:"bcc"`
	Subject string   `json:"subject"`
}

// newDraft is the result DTO of a draft, with every text field cleaned and the IDs as given.
func newDraft(d model.DraftResult) newDraftDTO {
	return newDraftDTO{draftIDsDTO{d.ID, d.ThreadID}, content.CleanList(d.To), content.CleanList(d.CC), content.CleanList(d.BCC), content.CleanSubject(d.Subject)}
}

// updatedDraftDTO is the result of draft.update: the draft's IDs, the message ID possibly new, and only the recipient
// lists and subject that the request gave. Fields that the update did not change are not read back. OK is always
// true, as in the result before the IDs. SendApprovalID is the new waiting send that replaced a withdrawn one, left
// out when the update made none.
type updatedDraftDTO struct {
	draftIDsDTO
	OK             bool      `json:"ok"`
	To             *[]string `json:"to,omitempty"`
	CC             *[]string `json:"cc,omitempty"`
	BCC            *[]string `json:"bcc,omitempty"`
	Subject        *string   `json:"subject,omitempty"`
	SendApprovalID string    `json:"send_approval_id,omitempty"`
}

// updatedDraftIDsDTO is the stored result of an approved draft update: its draft IDs and the new waiting send, which
// are IDs, not mail content.
type updatedDraftIDsDTO struct {
	draftIDsDTO
	SendApprovalID string `json:"send_approval_id,omitempty"`
}

// updatedDraft is the result DTO of a draft update, with every text field of the change cleaned and the IDs as given.
func updatedDraft(d guard.UpdatedDraft, c model.DraftChange) updatedDraftDTO {
	list := func(v *[]string) *[]string {
		if v == nil {
			return nil
		}
		return new(content.CleanList(*v))
	}
	var subject *string
	if c.Subject != nil {
		subject = new(content.CleanSubject(*c.Subject))
	}
	return updatedDraftDTO{draftIDsDTO{d.ID, d.ThreadID}, true, list(c.To), list(c.CC), list(c.BCC), subject, d.SendApprovalID}
}

// sourceContentShown is the verdict limit of a reply or forward draft result. Its recipients and subject come from
// the source message, so they show only at the source verdicts that show its headers: green and yellow. The policy
// refuses a reply or forward of any other source. Two cases still reach the fallback, which fails closed to the
// draft IDs: a source verdict that cannot be read after the write, such as that of a draft, which is never cached;
// and an approved run, which skips the policy, such as an item that waited before red sources were refused.
func sourceContentShown(source model.Verdict) bool {
	return source == model.Green || source == model.Yellow
}

// okDTO is the result of a send or a draft delete: providers give no ID for a sent message.
type okDTO struct {
	OK bool `json:"ok"`
}

func sent(e error) (any, error) {
	if e != nil {
		return nil, e
	}
	return okDTO{true}, nil
}

type updateDTO struct {
	ID string `json:"id"`
	OK bool   `json:"ok"`
}
type statusDTO struct {
	Account       string    `json:"account"`
	Email         string    `json:"email"`
	OK            bool      `json:"ok"`
	LoggedIn      bool      `json:"logged_in"`
	Scopes        *[]string `json:"scopes,omitempty"`
	ExtraScopes   *[]string `json:"extra_scopes,omitempty"`
	MissingScopes *[]string `json:"missing_scopes,omitempty"`
}
type ruleDTO struct {
	Rule     string  `json:"rule"`
	Folder   *string `json:"folder,omitempty"`
	Category *string `json:"category,omitempty"`
	Name     *string `json:"name"`
	MinAge   *int64  `json:"min_age_hours,omitempty"`
}
type rulesDTO struct {
	Account  string          `json:"account"`
	Rules    []ruleDTO       `json:"rules"`
	Warnings []guard.Warning `json:"warnings"`
}

func mailSearch(ctx context.Context, _ *Server, raw json.RawMessage) (call, guard.GuardError, error) {
	var p searchParam
	if params(raw, &p) != nil || p.Count < 0 || !model.ValidPage(p.Page) {
		return nil, "", errInvalid
	}
	for _, v := range []*string{p.Text, p.Sender, p.To, p.Subject, p.Folder} {
		if v != nil && !validString(*v, 4096, true) {
			return nil, "", errInvalid
		}
	}
	q := model.SearchQuery{Text: p.Text, Sender: p.Sender, To: p.To, Subject: p.Subject, Folder: p.Folder}
	for _, d := range []struct {
		v   *string
		out **time.Time
	}{{p.After, &q.After}, {p.Before, &q.Before}} {
		if d.v != nil {
			t, e := time.Parse(time.RFC3339, *d.v)
			if e != nil {
				return nil, "", errInvalid
			}
			*d.out = &t
		}
	}
	if !q.RangeValid() {
		return nil, "", errInvalid
	}
	return func(a *account) (any, error) {
		return a.guard.Search(ctx, q, model.PageRequest{Size: p.Count, Page: p.Page})
	}, "", nil
}

func mailOrganize(ctx context.Context, s *Server, raw json.RawMessage) (call, guard.GuardError, error) {
	var p organizeParam
	if params(raw, &p) != nil {
		return nil, "", errInvalid
	}
	// The client resolves days: since and until are exact times, and until may be in the future.
	now := s.options.Now().UTC()
	since, e := time.Parse(time.RFC3339, p.Since)
	if e != nil || since.Year() < 1 || since.After(now) {
		return nil, "", errInvalid
	}
	var until *time.Time
	if p.Until != nil {
		u, e := time.Parse(time.RFC3339, *p.Until)
		if e != nil || !u.After(since) {
			return nil, "", errInvalid
		}
		until = &u
	}
	return func(a *account) (any, error) { return a.guard.Organize(ctx, since, a.rules, now, p.Apply, until) }, "", nil
}

func calendarList(ctx context.Context, _ *Server, raw json.RawMessage) (call, guard.GuardError, error) {
	var p calendarParam
	if params(raw, &p) != nil {
		return nil, "", errInvalid
	}
	first, e := time.Parse(time.RFC3339, p.Start)
	last, e2 := time.Parse(time.RFC3339, p.End)
	if e != nil || e2 != nil || !last.After(first) {
		return nil, "", errInvalid
	}
	return func(a *account) (any, error) { return a.guard.CalendarList(ctx, first, last) }, "", nil
}

func calendarAvailability(ctx context.Context, _ *Server, raw json.RawMessage) (call, guard.GuardError, error) {
	var p availabilityParam
	if params(raw, &p) != nil || !listValid(p.To, 1, model.MaxAvailabilityAddresses) {
		return nil, "", errInvalid
	}
	first, e := time.Parse(time.RFC3339, p.Start)
	last, e2 := time.Parse(time.RFC3339, p.End)
	if e != nil || e2 != nil || !model.ValidAvailabilityRequest(p.To, first, last) {
		return nil, "", errInvalid
	}
	return func(a *account) (any, error) {
		v, e := a.guard.CalendarAvailability(ctx, p.To, first, last)
		if e != nil {
			return nil, e
		}
		out := availabilityDTO{first.Format(time.RFC3339Nano), last.Format(time.RFC3339Nano), int(model.AvailabilityInterval / time.Minute), make([]scheduleDTO, 0, len(v))}
		for _, schedule := range v {
			out.Schedules = append(out.Schedules, scheduleDTO{schedule.Address, schedule.Slots})
		}
		return out, nil
	}, guard.CalendarAvailabilityUnsupported, nil
}

// newMessage is the decoder of mail.send (send) and draft.new. Every param is optional; a send needs a
// recipient in to, cc, or bcc.
func newMessage(send bool) decoder {
	return func(ctx context.Context, _ *Server, raw json.RawMessage) (call, guard.GuardError, error) {
		var p newParam
		if params(raw, &p) != nil || !listValid(p.To, 0, noMax) || !listValid(p.CC, 0, noMax) || !listValid(p.BCC, 0, noMax) || !validString(p.Subject, 65536, true) || !validString(p.Body, MaxRequestBytes, true) {
			return nil, "", errInvalid
		}
		draft := model.NewDraft{To: p.To, CC: p.CC, BCC: p.BCC, Subject: p.Subject, Body: p.Body, HTML: p.HTML}
		if send && !draft.Addressed() {
			return nil, "", errInvalid
		}
		if send {
			return func(a *account) (any, error) { return sent(a.guard.SendNew(ctx, draft)) }, "", nil
		}
		return func(a *account) (any, error) {
			d, e := a.guard.DraftNew(ctx, draft)
			if e != nil {
				return nil, e
			}
			return newDraft(d), nil
		}, "", nil
	}
}

// replyMessage is the decoder of mail.reply (send) and draft.reply: one message ID; body, html, all, cc, and bcc
// are optional.
func replyMessage(send bool) decoder {
	return func(ctx context.Context, _ *Server, raw json.RawMessage) (call, guard.GuardError, error) {
		var p replyParam
		if params(raw, &p) != nil || !idValid(p.ID) || !validString(p.Body, MaxRequestBytes, true) || !listValid(p.CC, 0, noMax) || !listValid(p.BCC, 0, noMax) {
			return nil, "", errInvalid
		}
		reply := model.Reply{Body: p.Body, HTML: p.HTML, All: p.All, CC: p.CC, BCC: p.BCC}
		if send {
			return func(a *account) (any, error) { return sent(a.guard.SendReply(ctx, p.ID, reply)) }, "", nil
		}
		return func(a *account) (any, error) {
			d, source, e := a.guard.DraftReply(ctx, p.ID, reply)
			if e != nil {
				return nil, e
			}
			if !sourceContentShown(source) {
				return draftIDsDTO{d.ID, d.ThreadID}, nil
			}
			return newDraft(d), nil
		}, "", nil
	}
}

// draftUpdate is the decoder of draft.update: one message ID and at least one of to, cc, bcc, subject, and body. An
// absent param is unchanged; an empty list clears; html goes only with body.
func draftUpdate(ctx context.Context, _ *Server, raw json.RawMessage) (call, guard.GuardError, error) {
	var p draftChangeParam
	if params(raw, &p) != nil || !idValid(p.ID) || p.Subject != nil && !validString(*p.Subject, 65536, true) || p.Body != nil && !validString(*p.Body, MaxRequestBytes, true) {
		return nil, "", errInvalid
	}
	for _, list := range []*[]string{p.To, p.CC, p.BCC} {
		if list != nil && !listValid(*list, 0, noMax) {
			return nil, "", errInvalid
		}
	}
	change := model.DraftChange{To: p.To, CC: p.CC, BCC: p.BCC, Subject: p.Subject, Body: p.Body, HTML: p.HTML}
	if change.Empty() || change.HTMLWithoutBody() {
		return nil, "", errInvalid
	}
	return func(a *account) (any, error) {
		d, e := a.guard.DraftUpdate(ctx, p.ID, change)
		if e != nil {
			return nil, e
		}
		return updatedDraft(d, change), nil
	}, "", nil
}

// forwardMessage is the decoder of mail.forward (send) and draft.forward: one message ID; to, cc, bcc, body, and
// html are optional, but a send needs a recipient in to, cc, or bcc.
func forwardMessage(send bool) decoder {
	return func(ctx context.Context, _ *Server, raw json.RawMessage) (call, guard.GuardError, error) {
		var p forwardParam
		if params(raw, &p) != nil || !idValid(p.ID) || !listValid(p.To, 0, noMax) || !listValid(p.CC, 0, noMax) || !listValid(p.BCC, 0, noMax) || !validString(p.Body, MaxRequestBytes, true) {
			return nil, "", errInvalid
		}
		forward := model.Forward{Body: p.Body, HTML: p.HTML, To: p.To, CC: p.CC, BCC: p.BCC}
		if send && !forward.Addressed() {
			return nil, "", errInvalid
		}
		if send {
			return func(a *account) (any, error) { return sent(a.guard.SendForward(ctx, p.ID, forward)) }, "", nil
		}
		return func(a *account) (any, error) {
			d, source, e := a.guard.DraftForward(ctx, p.ID, forward)
			if e != nil {
				return nil, e
			}
			if !sourceContentShown(source) {
				return draftIDsDTO{d.ID, d.ThreadID}, nil
			}
			return newDraft(d), nil
		}, "", nil
	}
}

// accountCall validates the complete typed request first. Account and provider access come only afterwards.
func (s *Server) accountCall(ctx context.Context, st *state, op operation, r request) (any, error) {
	run, unsupported, e := op.decode(ctx, s, r.Params)
	if e != nil {
		return nil, e
	}
	a, e := st.account(r.Account, r.key)
	if e != nil {
		return nil, e
	}
	if unsupported != "" && a.config.Provider == "gmail" {
		return nil, unsupported
	}
	// The grant check comes before the policy decision.
	if needed := op.grants[a.config.Provider]; needed != nil {
		_, scopes, e := a.provider.Status(ctx)
		if e != nil {
			return nil, e
		}
		if !slices.ContainsFunc(scopes, func(scope string) bool {
			return slices.ContainsFunc(needed, func(grant string) bool { return strings.EqualFold(scope, grant) })
		}) {
			return nil, guard.GuardError("missing_scopes")
		}
	}
	v, e := run(a)
	if pending, ok := errors.AsType[guard.PendingError](e); ok {
		return pendingDTO{"pending", pending.ApprovalID, s.approvalURL(pending.ApprovalID)}, nil
	}
	return v, e
}

// approvalURL is the console page of a stored action.
func (s *Server) approvalURL(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return "http://" + s.listen + "/a/" + id
}

// status reports the sign-in state of an account from the credential snapshot of the state that the request uses.
func (s *Server) status(ctx context.Context, a *account) (any, error) {
	out := statusDTO{Account: a.config.Alias, Email: a.config.Email}
	if a.config.Provider == "fake" {
		out.LoggedIn = true
		_, scopes, e := a.provider.Status(ctx)
		if e == nil {
			out.OK, out.Scopes = true, &scopes
		}
		return out, nil
	}
	if _, e := a.logins.LoadOAuth(ctx, a.config.Provider, a.config.Key()); e != nil {
		return out, nil
	}
	out.LoggedIn = true
	_, scopes, e := a.provider.Status(ctx)
	if e != nil {
		var diagnostic interface{ MissingScopes() []string }
		if errors.As(e, &diagnostic) {
			missing := safeScopeDiagnostics(diagnostic.MissingScopes())
			out.MissingScopes = &missing
		}
		return out, nil
	}
	out.OK = true
	extra := m365.ExtraScopes(scopes)
	if a.config.Provider == "gmail" {
		extra = gmail.ExtraScopes(scopes)
	}
	// scopes lists requested grants; extra grants appear only as bounded diagnostic labels.
	scopes = slices.DeleteFunc(scopes, func(s string) bool { return slices.Contains(extra, s) })
	out.Scopes = &scopes
	if extra = safeScopeDiagnostics(extra); len(extra) != 0 {
		out.ExtraScopes = &extra
	}
	return out, nil
}

// Scope labels come from credentials or token responses, not trusted error text.
var scopeLabel = regexp.MustCompile(`^(?:(?:role:)?[A-Za-z][A-Za-z0-9]*(?:\.[A-Za-z][A-Za-z0-9]*)+|https://www\.googleapis\.com/auth/[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*)$`)

func safeScopeDiagnostics(scopes []string) []string {
	out := []string{}
	for _, scope := range scopes {
		if len(scope) <= 256 && scopeLabel.MatchString(scope) {
			out = append(out, scope)
			if len(out) == 100 {
				break
			}
		}
	}
	return out
}

// ruleList shows the rules of the account from the current state, with the folder and category names.
func ruleList(ctx context.Context, a *account) (any, error) {
	r := a.rules
	out := rulesDTO{Account: a.config.Alias, Rules: []ruleDTO{}, Warnings: []guard.Warning{}}
	if len(r.Rules) == 0 {
		return out, nil
	}
	g := a.guard
	folders, e := g.Folders(ctx)
	if e != nil {
		return nil, e
	}
	cats, e := g.Categories(ctx)
	if e != nil {
		return nil, e
	}
	names := map[string]string{}
	for _, f := range folders {
		names[f.ID] = content.CleanSubject(f.Name)
	}
	for _, v := range r.Rules {
		line := ruleDTO{Rule: v.Name}
		if v.Kind == rules.Folder {
			line.Folder = &v.Target
			line.MinAge = &v.MinAgeHours
			if v.Target == "archive" || v.Target == "trash" {
				line.Name = &v.Target
			} else if name, ok := names[v.Target]; ok {
				line.Name = &name
			}
		} else {
			line.Category = &v.Target
			if slices.Contains(cats, v.Target) {
				line.Name = &v.Target
			}
		}
		out.Rules = append(out.Rules, line)
		if line.Name == nil {
			out.Warnings = append(out.Warnings, guard.Warning{Rule: v.Name, Problem: string(v.Kind) + " not found"})
		}
	}
	return out, nil
}
