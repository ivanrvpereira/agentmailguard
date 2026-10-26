package gmail

import (
	"context"
	"encoding/json"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"net/url"
	"regexp"
	"strings"
	"time"
)

type calendar struct{ c *Client }

func (c *Client) Calendar() model.Calendar { return &calendar{c} }

type eventTime struct{ Date, DateTime string }

func (t eventTime) parse() (time.Time, error) {
	var v time.Time
	var err error
	if t.Date != "" {
		v, err = time.Parse("2006-01-02", t.Date)
	} else {
		v, err = time.Parse(time.RFC3339Nano, t.DateTime)
	}
	if err != nil || v.Year() < 1 || v.Year() > 9999 {
		return time.Time{}, failure("Calendar", "ValueError")
	}
	return v.UTC(), nil
}

// Self marks the entry for the calendar that holds this copy of the event. Only Google sets it.
type eventAddress struct {
	DisplayName, Email string
	Self               bool
}

func (a eventAddress) model() model.Address {
	return model.Address{Name: a.DisplayName, Address: a.Email}
}

var frequencyRE = regexp.MustCompile(`\bFREQ=(\w+)`)
var htmlRE = regexp.MustCompile(`<[a-zA-Z!/]`)

func parseEvent(raw []byte) (model.Event, error) {
	var v struct {
		ID, Summary, Description, Location, HangoutLink, Visibility, Transparency, Status, RecurringEventID string
		Start, End                                                                                          eventTime
		Recurrence                                                                                          []string
		Organizer                                                                                           eventAddress
		Attendees                                                                                           []struct {
			eventAddress
			ResponseStatus string
		}
		Attachments    []struct{ Title string }
		ConferenceData struct {
			EntryPoints []struct{ EntryPointType, URI string }
		}
	}
	if json.Unmarshal(raw, &v) != nil || strings.TrimSpace(v.ID) == "" {
		return model.Event{}, failure("Calendar", "ValueError")
	}
	start, err := v.Start.parse()
	if err != nil {
		return model.Event{}, err
	}
	end, err := v.End.parse()
	if err != nil {
		return model.Event{}, err
	}
	kind := model.Text
	if htmlRE.MatchString(v.Description) {
		kind = model.HTML
	}
	e := model.Event{ID: v.ID, Subject: v.Summary, Start: start, End: end, IsAllDay: v.Start.Date != "", Location: v.Location, Organizer: v.Organizer.model(), Attendees: []model.Attendee{}, Attachments: []string{}, Categories: []string{}, IsPrivate: v.Visibility == "private" || v.Visibility == "confidential", IsCancelled: v.Status == "cancelled", ShowAs: "busy", Body: &v.Description, ContentType: &kind, IsOrganizer: v.Organizer.Self}
	if v.Transparency == "transparent" {
		e.ShowAs = "free"
	}
	for _, a := range v.Attendees {
		response := a.ResponseStatus
		if response == "" {
			response = "none"
		}
		e.Attendees = append(e.Attendees, model.Attendee{Address: a.model(), Response: response})
	}
	for _, a := range v.Attachments {
		e.Attachments = append(e.Attachments, a.Title)
	}
	recurrence := strings.Join(v.Recurrence, " ")
	if match := frequencyRE.FindStringSubmatch(recurrence); len(match) > 0 {
		s := strings.ToLower(match[1])
		e.Recurrence = &s
	} else if recurrence != "" || v.RecurringEventID != "" {
		s := "recurring"
		e.Recurrence = &s
	}
	join := v.HangoutLink
	if join == "" {
		for _, p := range v.ConferenceData.EntryPoints {
			if p.EntryPointType == "video" {
				join = p.URI
				break
			}
		}
	}
	if join != "" {
		e.JoinURL = &join
	}
	return e, nil
}
func (c *calendar) List(ctx context.Context, start, end time.Time) ([]model.Event, error) {
	if !end.After(start) || start.Year() < 1 || end.Year() > 9999 {
		return nil, failure("Calendar", "ValueError")
	}
	q := url.Values{"timeMin": {start.UTC().Format(time.RFC3339Nano)}, "timeMax": {end.UTC().Format(time.RFC3339Nano)}, "singleEvents": {"true"}, "orderBy": {"startTime"}, "maxResults": {"250"}}
	out := []model.Event{}
	seen := map[string]bool{}
	for {
		v, err := c.c.get(ctx, "Calendar", c.c.options.CalendarURL+"/events", q)
		if err != nil {
			return nil, err
		}
		a, err := items(v, "items", "Calendar")
		if err != nil {
			return nil, err
		}
		for _, raw := range a {
			event, err := parseEvent(raw)
			if err != nil {
				return nil, err
			}
			out = append(out, event)
		}
		token, err := pageToken(v, seen, "Calendar")
		if err != nil {
			return nil, err
		}
		if token == "" {
			return out, nil
		}
		q.Set("pageToken", token)
	}
}

// Search reads the first page of events.list with q only, so Google's default page size and order apply
// (https://developers.google.com/workspace/calendar/api/v3/reference/events/list). Its items are full events.
func (c *calendar) Search(ctx context.Context, text string) ([]model.Event, error) {
	v, err := c.c.get(ctx, "Calendar", c.c.options.CalendarURL+"/events", url.Values{"q": {text}})
	if err != nil {
		return nil, err
	}
	a, err := items(v, "items", "Calendar")
	if err != nil {
		return nil, err
	}
	out := []model.Event{}
	for _, raw := range a {
		event, err := parseEvent(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, nil
}
func (c *calendar) Get(ctx context.Context, id string) (model.Event, error) {
	v, err := c.c.get(ctx, "Calendar", c.c.options.CalendarURL+"/events/"+segment(id), nil)
	if err != nil {
		return model.Event{}, err
	}
	raw, _ := json.Marshal(v)
	e, err := parseEvent(raw)
	if err == nil && e.ID != id {
		return model.Event{}, failure("Calendar", "InvalidMetadata")
	}
	return e, err
}
