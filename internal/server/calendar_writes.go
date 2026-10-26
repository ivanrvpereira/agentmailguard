package server

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/guard"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

type inviteParam struct {
	To       []string `json:"to"`
	Optional []string `json:"optional"`
	Subject  *string  `json:"subject"`
	Body     *string  `json:"body"`
	HTML     bool     `json:"html"`
	Start    string   `json:"start"`
	End      string   `json:"end"`
	Location string   `json:"location"`
	Online   bool     `json:"online"`
}

type eventChangeParam struct {
	ID       string   `json:"id"`
	Subject  *string  `json:"subject"`
	Location *string  `json:"location"`
	Body     *string  `json:"body"`
	HTML     bool     `json:"html"` // only with body
	Start    *string  `json:"start"`
	End      *string  `json:"end"`
	Duration *string  `json:"duration"`
	Add      []string `json:"add"`
	Remove   []string `json:"remove"`
	Online   *bool    `json:"online"` // only true is valid; updates never remove an online meeting
}

type eventCancelParam struct {
	ID      string `json:"id"`
	Comment string `json:"comment"`
}
type eventRespondParam struct {
	ID       string                `json:"id"`
	Response model.MeetingResponse `json:"response"`
	Comment  string                `json:"comment"`
	NoNotify bool                  `json:"no_notify"`
}

func calendarCancel(ctx context.Context, _ *Server, raw json.RawMessage) (call, guard.GuardError, error) {
	var p eventCancelParam
	if params(raw, &p) != nil || !idValid(p.ID) || !validString(p.Comment, MaxRequestBytes, true) {
		return nil, "", errInvalid
	}
	// Google sends no cancellation message, so a comment would be lost.
	var gmailRefusal guard.GuardError
	if p.Comment != "" {
		gmailRefusal = guard.CalendarCancelCommentUnsupported
	}
	return func(a *account) (any, error) {
		return updateDTO{p.ID, true}, a.guard.CalendarCancel(ctx, p.ID, p.Comment)
	}, gmailRefusal, nil
}

func calendarRespond(ctx context.Context, _ *Server, raw json.RawMessage) (call, guard.GuardError, error) {
	var p eventRespondParam
	if params(raw, &p) != nil || !idValid(p.ID) || !validString(p.Comment, MaxRequestBytes, true) {
		return nil, "", errInvalid
	}
	response := model.EventResponse{Response: p.Response, Comment: p.Comment, NoNotify: p.NoNotify}
	if !response.Valid() {
		return nil, "", errInvalid
	}
	// Google Calendar always notifies the organizer of a response.
	var gmailRefusal guard.GuardError
	if p.NoNotify {
		gmailRefusal = guard.CalendarNoNotifyUnsupported
	}
	return func(a *account) (any, error) {
		return updateDTO{p.ID, true}, a.guard.CalendarRespond(ctx, p.ID, response)
	}, gmailRefusal, nil
}

func calendarUpdate(ctx context.Context, _ *Server, raw json.RawMessage) (call, guard.GuardError, error) {
	var p eventChangeParam
	if params(raw, &p) != nil || !idValid(p.ID) || p.Online != nil && !*p.Online || !listValid(p.Add, 0, noMax) || !listValid(p.Remove, 0, noMax) || p.Subject != nil && !validString(*p.Subject, 65536, true) || p.Location != nil && !validString(*p.Location, 4096, true) || p.Body != nil && !validString(*p.Body, MaxRequestBytes, true) {
		return nil, "", errInvalid
	}
	start, ok := optionalCalendarTime(p.Start)
	end, ok2 := optionalCalendarTime(p.End)
	change := model.EventChange{Subject: p.Subject, Location: p.Location, Body: p.Body, HTML: p.HTML, Start: start, End: end, AddAttendees: p.Add, RemoveAttendees: p.Remove, Online: p.Online != nil}
	if !ok || !ok2 || change.HTMLWithoutBody() || start != nil && end != nil && !end.After(*start) {
		return nil, "", errInvalid
	}
	var duration time.Duration // 0: not given
	if p.Duration != nil {
		d, e := time.ParseDuration(*p.Duration)
		if e != nil || d <= 0 || end != nil || start != nil && !model.CalendarTimeValid(start.Add(d)) {
			return nil, "", errInvalid
		}
		duration = d
	}
	if change.AttendeeConflict() || change.Empty() && duration == 0 {
		return nil, "", errInvalid
	}
	return func(a *account) (any, error) {
		return updateDTO{p.ID, true}, a.guard.CalendarUpdate(ctx, p.ID, change, duration)
	}, "", nil
}

func calendarInvite(ctx context.Context, _ *Server, raw json.RawMessage) (call, guard.GuardError, error) {
	// An online meeting is on unless the caller sends an explicit false.
	p := inviteParam{Online: true}
	if params(raw, &p) != nil || !listValid(p.To, 1, noMax) || !listValid(p.Optional, 0, noMax) || p.Subject == nil || p.Body == nil || !validString(*p.Subject, 65536, true) || !validString(*p.Body, MaxRequestBytes, true) || !validString(p.Location, 4096, true) {
		return nil, "", errInvalid
	}
	start, ok := calendarTime(p.Start)
	end, ok2 := calendarTime(p.End)
	event := model.NewEvent{Subject: *p.Subject, Body: *p.Body, HTML: p.HTML, Location: p.Location, Start: start, End: end, Attendees: p.To, Optional: p.Optional, Online: p.Online}
	if !ok || !ok2 || !end.After(start) || event.AttendeeConflict() {
		return nil, "", errInvalid
	}
	return func(a *account) (any, error) {
		id, e := a.guard.CalendarInvite(ctx, event)
		return draftDTO{id}, e
	}, "", nil
}

// calendarTime parses an RFC 3339 time with a year the providers accept.
func calendarTime(v string) (time.Time, bool) {
	t, e := time.Parse(time.RFC3339, v)
	return t, e == nil && model.CalendarTimeValid(t)
}

// optionalCalendarTime is calendarTime for an optional field; nil stays nil.
func optionalCalendarTime(v *string) (*time.Time, bool) {
	if v == nil {
		return nil, true
	}
	t, ok := calendarTime(*v)
	return &t, ok
}
