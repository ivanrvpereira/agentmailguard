package m365

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/ivanrvpereira/agentmailguard-go/internal/model"
)

type calendar struct{ c *Client }

func (c *Client) Calendar() model.Calendar { return &calendar{c} }
func calendarQuery() url.Values {
	return url.Values{"$select": {"id,subject,start,end,isAllDay,type,recurrence,location,organizer,attendees,onlineMeeting,sensitivity,categories,showAs,isCancelled,body"}, "$expand": {attachments}}
}
func (c *calendar) List(ctx context.Context, start, end time.Time) ([]model.Event, error) {
	if !end.After(start) {
		return nil, invalidMetadata
	}
	q := calendarQuery()
	q.Set("startDateTime", start.UTC().Format(time.RFC3339))
	q.Set("endDateTime", end.UTC().Format(time.RFC3339))
	q.Set("$orderby", "start/dateTime")
	q.Set("$top", "100")
	items, err := c.c.all(ctx, c.c.endpoint("/me/calendarView", q), true)
	if err != nil {
		return nil, err
	}
	out := []model.Event{}
	for _, d := range items {
		ev, err := toEvent(d)
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}
func (c *calendar) Get(ctx context.Context, id string) (model.Event, error) {
	d, err := c.c.get(ctx, c.c.endpoint("/me/events/"+escapeID(id), calendarQuery()), true)
	if err != nil {
		return model.Event{}, err
	}
	return toEvent(d)
}
func eventTime(d object) (time.Time, error) {
	if text(d, "timeZone") != "UTC" {
		return time.Time{}, invalidMetadata
	}
	date := text(d, "dateTime")
	stamp, err := time.Parse(time.RFC3339Nano, date)
	if err != nil && !strings.ContainsAny(strings.TrimPrefix(date, "-"), "Z+") {
		stamp, err = time.Parse("2006-01-02T15:04:05", date)
	}
	if err != nil {
		return time.Time{}, invalidMetadata
	}
	_, offset := stamp.Zone()
	if offset != 0 {
		return time.Time{}, invalidMetadata
	}
	return stamp.UTC(), nil
}
func toEvent(d object) (model.Event, error) {
	ev := model.Event{ID: text(d, "id"), Subject: text(d, "subject"), IsAllDay: boolean(d, "isAllDay"), Location: text(obj(d, "location"), "displayName"), Organizer: address(obj(d, "organizer")), Attendees: []model.Attendee{}, Attachments: names(d), IsPrivate: text(d, "sensitivity") == "private", Categories: stringsAt(d, "categories"), ShowAs: text(d, "showAs"), IsCancelled: boolean(d, "isCancelled")}
	if ev.ID == "" {
		return ev, invalidMetadata
	}
	var err error
	ev.Start, err = eventTime(obj(d, "start"))
	if err != nil {
		return ev, err
	}
	ev.End, err = eventTime(obj(d, "end"))
	if err != nil {
		return ev, err
	}
	recurrence := text(obj(obj(d, "recurrence"), "pattern"), "type")
	if recurrence == "" && (text(d, "type") == "occurrence" || text(d, "type") == "exception") {
		recurrence = "recurring"
	}
	if recurrence != "" {
		ev.Recurrence = &recurrence
	}
	for _, a := range array(d, "attendees") {
		response := text(obj(a, "status"), "response")
		if response == "" {
			response = "none"
		}
		ev.Attendees = append(ev.Attendees, model.Attendee{Address: address(a), Response: response})
	}
	if join := text(obj(d, "onlineMeeting"), "joinUrl"); join != "" {
		ev.JoinURL = &join
	}
	ev.Body, ev.ContentType = content(d)
	return ev, nil
}
