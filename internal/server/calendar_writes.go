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
	Subject  *string  `json:"subject"`
	Body     *string  `json:"body"`
	Start    string   `json:"start"`
	End      string   `json:"end"`
	Location string   `json:"location"`
	Online   bool     `json:"online"`
}

type eventChangeParam struct {
	ID       string   `json:"id"`
	Subject  *string  `json:"subject"`
	Location *string  `json:"location"`
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
}

// calendarWriteCall validates a meeting write. gmailRefusal, when not empty, refuses a valid request that
// Gmail cannot carry out.
func calendarWriteCall(ctx context.Context, operation string, raw json.RawMessage) (call func(*account) (any, error), gmailRefusal guard.GuardError, err error) {
	switch operation {
	case "calendar.cancel":
		var p eventCancelParam
		if params(raw, &p) != nil || !idValid(p.ID) || !validString(p.Comment, MaxRequestBytes, true) {
			return nil, "", errInvalid
		}
		// Google sends no cancellation message, so a comment would be lost.
		if p.Comment != "" {
			gmailRefusal = guard.CalendarCancelCommentUnsupported
		}
		return func(a *account) (any, error) {
			return updateDTO{p.ID, true}, a.guard.CalendarCancel(ctx, p.ID, p.Comment)
		}, gmailRefusal, nil
	case "calendar.respond":
		var p eventRespondParam
		if params(raw, &p) != nil || !idValid(p.ID) || !validString(p.Comment, MaxRequestBytes, true) || !p.Response.Valid() {
			return nil, "", errInvalid
		}
		return func(a *account) (any, error) {
			return updateDTO{p.ID, true}, a.guard.CalendarRespond(ctx, p.ID, model.EventResponse{Response: p.Response, Comment: p.Comment})
		}, "", nil
	case "calendar.update":
		var p eventChangeParam
		if params(raw, &p) != nil || !idValid(p.ID) || p.Online != nil && !*p.Online || !listValid(p.Add, 0, 100) || !listValid(p.Remove, 0, 100) || p.Subject != nil && !validString(*p.Subject, 65536, true) || p.Location != nil && !validString(*p.Location, 4096, true) {
			return nil, "", errInvalid
		}
		start, ok := optionalCalendarTime(p.Start)
		end, ok2 := optionalCalendarTime(p.End)
		change := model.EventChange{Subject: p.Subject, Location: p.Location, Start: start, End: end, AddAttendees: p.Add, RemoveAttendees: p.Remove, Online: p.Online != nil}
		if !ok || !ok2 || start != nil && end != nil && !end.After(*start) {
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
	case "calendar.invite":
		// An online meeting is on unless the caller sends an explicit false.
		p := inviteParam{Online: true}
		if params(raw, &p) != nil || !listValid(p.To, 1, 100) || p.Subject == nil || p.Body == nil || !validString(*p.Subject, 65536, true) || !validString(*p.Body, MaxRequestBytes, true) || !validString(p.Location, 4096, true) {
			return nil, "", errInvalid
		}
		start, ok := calendarTime(p.Start)
		end, ok2 := calendarTime(p.End)
		if !ok || !ok2 || !end.After(start) {
			return nil, "", errInvalid
		}
		return func(a *account) (any, error) {
			id, e := a.guard.CalendarInvite(ctx, model.NewEvent{Subject: *p.Subject, Body: *p.Body, Location: p.Location, Start: start, End: end, Attendees: p.To, Online: p.Online})
			return draftDTO{id}, e
		}, "", nil
	}
	return nil, "", errInvalid
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
