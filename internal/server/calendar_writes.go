package server

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

type inviteParam struct {
	To       []string `json:"to"`
	Subject  *string  `json:"subject"`
	Body     *string  `json:"body"`
	Start    string   `json:"start"`
	End      string   `json:"end"`
	Location string   `json:"location"`
	Teams    bool     `json:"teams"`
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
}

type eventCancelParam struct {
	ID      string `json:"id"`
	Comment string `json:"comment"`
}
type eventRespondParam struct {
	ID       string `json:"id"`
	Response string `json:"response"`
	Comment  string `json:"comment"`
}

func calendarWriteCall(ctx context.Context, operation string, raw json.RawMessage) (func(*account) (any, error), error) {
	switch operation {
	case "calendar.cancel":
		var p eventCancelParam
		if params(raw, &p) != nil || !idValid(p.ID) || !validString(p.Comment, MaxRequestBytes, true) {
			return nil, invalid
		}
		return func(a *account) (any, error) {
			return updateDTO{p.ID, true}, a.guard.CalendarCancel(ctx, p.ID, p.Comment)
		}, nil
	case "calendar.respond":
		var p eventRespondParam
		if params(raw, &p) != nil || !idValid(p.ID) || !validString(p.Comment, MaxRequestBytes, true) || p.Response != "accept" && p.Response != "tentative" && p.Response != "decline" {
			return nil, invalid
		}
		return func(a *account) (any, error) {
			return updateDTO{p.ID, true}, a.guard.CalendarRespond(ctx, p.ID, model.EventResponse{Response: p.Response, Comment: p.Comment})
		}, nil
	case "calendar.update":
		var p eventChangeParam
		if params(raw, &p) != nil || !idValid(p.ID) || !listValid(p.Add, 0, 100) || !listValid(p.Remove, 0, 100) || p.Subject != nil && !validString(*p.Subject, 65536, true) || p.Location != nil && !validString(*p.Location, 4096, true) {
			return nil, invalid
		}
		change := model.EventChange{Subject: p.Subject, Location: p.Location, AddAttendees: p.Add, RemoveAttendees: p.Remove}
		for _, v := range []struct {
			in  *string
			out **time.Time
		}{{p.Start, &change.Start}, {p.End, &change.End}} {
			if v.in != nil {
				stamp, e := time.Parse(time.RFC3339, *v.in)
				if e != nil || !calendarTimeValid(stamp) {
					return nil, invalid
				}
				*v.out = &stamp
			}
		}
		if p.Duration != nil {
			d, e := time.ParseDuration(*p.Duration)
			if e != nil || d <= 0 || p.End != nil {
				return nil, invalid
			}
			change.Duration = &d
		}
		if change.Start != nil && change.End != nil && !change.End.After(*change.Start) {
			return nil, invalid
		}
		if change.Start != nil && change.Duration != nil && !calendarTimeValid(change.Start.Add(*change.Duration)) {
			return nil, invalid
		}
		for _, add := range p.Add {
			for _, remove := range p.Remove {
				if strings.EqualFold(add, remove) {
					return nil, invalid
				}
			}
		}
		if p.Subject == nil && p.Location == nil && p.Start == nil && p.End == nil && p.Duration == nil && len(p.Add)+len(p.Remove) == 0 {
			return nil, invalid
		}
		return func(a *account) (any, error) { return updateDTO{p.ID, true}, a.guard.CalendarUpdate(ctx, p.ID, change) }, nil
	case "calendar.invite":
		var p inviteParam
		if params(raw, &p) != nil || !listValid(p.To, 1, 100) || p.Subject == nil || p.Body == nil || !validString(*p.Subject, 65536, true) || !validString(*p.Body, MaxRequestBytes, true) || !validString(p.Location, 4096, true) {
			return nil, invalid
		}
		start, e := time.Parse(time.RFC3339, p.Start)
		end, e2 := time.Parse(time.RFC3339, p.End)
		if e != nil || e2 != nil || !calendarTimeValid(start) || !calendarTimeValid(end) || !end.After(start) {
			return nil, invalid
		}
		return func(a *account) (any, error) {
			id, e := a.guard.CalendarInvite(ctx, model.NewEvent{Subject: *p.Subject, Body: *p.Body, Location: p.Location, Start: start, End: end, Attendees: p.To, Teams: p.Teams})
			return draftDTO{id}, e
		}, nil
	}
	return nil, invalid
}

func calendarTimeValid(t time.Time) bool {
	return t.Year() >= 1 && t.Year() <= 9999 && t.UTC().Year() >= 1 && t.UTC().Year() <= 9999
}
