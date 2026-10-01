package views

import (
	"errors"
	"strings"

	"github.com/ivanrvpereira/agentmailguard/internal/content"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

type Attendee struct {
	Address
	Response *string `json:"response,omitempty"`
}
type CalendarView struct {
	ID          string             `json:"id"`
	Verdict     model.Verdict      `json:"verdict"`
	Start       string             `json:"start"`
	End         string             `json:"end"`
	Organizer   Address            `json:"organizer"`
	Subject     *string            `json:"subject,omitempty"`
	Attendees   *[]Attendee        `json:"attendees,omitempty"`
	AllDay      *bool              `json:"all_day,omitempty"`
	Recurrence  *string            `json:"recurrence,omitempty"`
	Location    *string            `json:"location,omitempty"`
	JoinURL     *string            `json:"join_url,omitempty"`
	Attachments *[]string          `json:"attachments,omitempty"`
	Categories  *[]string          `json:"categories,omitempty"`
	ShowAs      *string            `json:"show_as,omitempty"`
	Cancelled   *bool              `json:"cancelled,omitempty"`
	Body        *string            `json:"body,omitempty"`
	Links       *map[string]string `json:"links,omitempty"`
	Truncated   *bool              `json:"truncated,omitempty"`
	Reason      *string            `json:"reason,omitempty"`
	Error       *string            `json:"error,omitempty"`
}

func joinURL(s *string) *string {
	if s == nil || !strings.HasPrefix(*s, "https://") {
		return nil
	}
	for _, r := range *s {
		if r < 33 || r > 126 {
			return nil
		}
	}
	return pointer(*s)
}
func attendees(as []model.Attendee, full bool) []Attendee {
	result := make([]Attendee, 0, len(as))
	for _, a := range as {
		item := Attendee{Address: Address{Address: content.CleanSubject(a.Address.Address)}}
		if full {
			item.Address = address(a.Address)
			item.Response = pointer(content.CleanSubject(a.Response))
		}
		result = append(result, item)
	}
	return result
}

// EventScreeningState has no private-event body, even if a provider returned one.
func EventScreeningState(e model.Event, r *content.Rendered) map[string]any {
	var recurrence, join any
	if e.Recurrence != nil {
		recurrence = *e.Recurrence
	}
	if u := joinURL(e.JoinURL); u != nil {
		join = *u
	}
	fields := map[string]any{"subject": content.CleanSubject(e.Subject), "all_day": e.IsAllDay, "recurrence": recurrence, "location": content.CleanSubject(e.Location), "organizer": address(e.Organizer), "attendees": attendees(e.Attendees, true), "join_url": join, "attachments": cleanStrings(e.Attachments), "categories": cleanStrings(e.Categories), "show_as": e.ShowAs, "cancelled": e.IsCancelled}
	if r != nil && !e.IsPrivate {
		fields["body"] = r.Markdown
		fields["links"] = r.Domains()
		fields["truncated"] = r.Truncated
	}
	return fields
}
func EventView(e model.Event, screening model.Screening, r *content.Rendered, categories []string) (CalendarView, error) {
	v := verdict(screening.Verdict)
	result := CalendarView{ID: e.ID, Verdict: v, Start: iso(e.Start), End: iso(e.End), Organizer: Address{Address: content.CleanSubject(e.Organizer.Address)}, Reason: reason(v)}
	switch v {
	case model.Green:
		if r == nil && !e.IsPrivate {
			return CalendarView{}, errors.New("green event view needs the rendered body")
		}
		result.Subject = pointer(content.CleanSubject(e.Subject))
		result.AllDay = pointer(e.IsAllDay)
		result.Recurrence = e.Recurrence
		result.Location = pointer(content.CleanSubject(e.Location))
		result.Organizer = address(e.Organizer)
		result.Attendees = pointer(attendees(e.Attendees, true))
		result.JoinURL = joinURL(e.JoinURL)
		result.Attachments = pointer(cleanStrings(e.Attachments))
		result.Categories = pointer(listed(e.Categories, categories))
		result.ShowAs = pointer(e.ShowAs)
		result.Cancelled = pointer(e.IsCancelled)
		if r != nil && !e.IsPrivate {
			result.Body = pointer(r.Markdown)
			result.Links = pointer(r.Domains())
			result.Truncated = pointer(r.Truncated)
		}
	case model.Yellow:
		result.Subject = pointer(content.CleanSubject(e.Subject))
		result.Attendees = pointer(attendees(e.Attendees, false))
	case model.Error:
		result.Error = safeError(screening.Error)
	}
	return result, nil
}
