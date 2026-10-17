package gmail

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// Every write notifies all guests: Google's default for sendUpdates is to send nothing, and a meeting change
// that attendees do not hear about is worse than a notification.
func notifyAll() url.Values { return url.Values{"sendUpdates": {"all"}} }

// calendarWrite sends one JSON write to the primary calendar. Writes are never retried.
func (c *calendar) calendarWrite(ctx context.Context, method, endpoint string, q url.Values, body any) (map[string]json.RawMessage, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, failure("Calendar", "ValueError")
	}
	return c.c.request(ctx, "Calendar", method, endpoint, q, data, "application/json")
}

func eventDateTime(t time.Time) map[string]string {
	return map[string]string{"dateTime": t.UTC().Format(time.RFC3339Nano)}
}

// addMeet asks Google to create a Google Meet conference for the event. The request ID is new for every
// request, so Google never ignores it as a repeat.
func addMeet(body map[string]any, q url.Values) {
	q.Set("conferenceDataVersion", "1")
	body["conferenceData"] = map[string]any{"createRequest": map[string]any{"requestId": rand.Text(), "conferenceSolutionKey": map[string]string{"type": "hangoutsMeet"}}}
}

func (c *calendar) Invite(ctx context.Context, event model.NewEvent) (model.EventID, error) {
	attendees := make([]map[string]string, 0, len(event.Attendees))
	for _, address := range event.Attendees {
		attendees = append(attendees, map[string]string{"email": address})
	}
	// Google renders the description as HTML, so the plain-text body goes escaped.
	body := map[string]any{"summary": event.Subject, "description": event.HTMLBody(), "location": event.Location, "start": eventDateTime(event.Start), "end": eventDateTime(event.End), "attendees": attendees, "guestsCanInviteOthers": false}
	q := notifyAll()
	if event.Online {
		addMeet(body, q)
	}
	v, err := c.calendarWrite(ctx, "POST", c.c.options.CalendarURL+"/events", q, body)
	if err != nil {
		return "", err
	}
	id := stringField(v, "id")
	if id == "" {
		return "", failure("Calendar", "InvalidMetadata")
	}
	return id, nil
}

func (c *calendar) eventURL(id model.EventID) string {
	return c.c.options.CalendarURL + "/events/" + segment(id)
}

// attendees reads the event's attendee entries as Google sent them, so a full-list write keeps each
// entry unchanged. An event without attendees has none; an entry without an email address is invalid.
func attendees(event map[string]json.RawMessage) ([]map[string]json.RawMessage, error) {
	var list []map[string]json.RawMessage
	if raw, ok := event["attendees"]; ok && json.Unmarshal(raw, &list) != nil {
		return nil, failure("Calendar", "InvalidMetadata")
	}
	for _, entry := range list {
		if entry == nil || strings.TrimSpace(stringField(entry, "email")) == "" {
			return nil, failure("Calendar", "InvalidMetadata")
		}
	}
	return list, nil
}

// Update patches only the changed fields. Every patch also keeps guests from inviting others.
func (c *calendar) Update(ctx context.Context, id model.EventID, change model.EventChange) error {
	// Guard resolves start-only moves; end-only changes are complete as sent.
	if change.Start != nil && change.End == nil {
		return failure("Calendar", "InvalidMetadata")
	}
	body := map[string]any{"guestsCanInviteOthers": false}
	q := notifyAll()
	if change.Subject != nil {
		body["summary"] = *change.Subject
	}
	if change.Location != nil {
		body["location"] = *change.Location
	}
	if change.Start != nil {
		body["start"] = eventDateTime(*change.Start)
	}
	if change.End != nil {
		body["end"] = eventDateTime(*change.End)
	}
	attendeeChange := len(change.AddAttendees) > 0 || len(change.RemoveAttendees) > 0
	if attendeeChange || change.Online {
		current, err := c.c.get(ctx, "Calendar", c.eventURL(id), nil)
		if err != nil {
			return err
		}
		// Add only: an existing conference stays as it is.
		if change.Online && !hasConference(current) {
			addMeet(body, q)
		}
		if attendeeChange {
			list, err := attendees(current)
			if err != nil {
				return err
			}
			remove, seen := map[string]bool{}, map[string]bool{}
			for _, address := range change.RemoveAttendees {
				remove[strings.ToLower(address)] = true
			}
			kept := []any{}
			for _, entry := range list {
				key := strings.ToLower(stringField(entry, "email"))
				if !remove[key] && !seen[key] {
					kept = append(kept, entry)
					seen[key] = true
				}
			}
			for _, address := range change.AddAttendees {
				if key := strings.ToLower(address); !seen[key] {
					kept = append(kept, map[string]string{"email": address})
					seen[key] = true
				}
			}
			body["attendees"] = kept
		}
	}
	_, err := c.calendarWrite(ctx, "PATCH", c.eventURL(id), q, body)
	return err
}

// hasConference reports a conference of any kind, including a legacy Hangouts link.
func hasConference(event map[string]json.RawMessage) bool {
	raw, ok := event["conferenceData"]
	return ok && string(raw) != "null" || stringField(event, "hangoutLink") != ""
}

// Cancel deletes the event and notifies the guests, which Google treats as the cancellation. Google keeps
// the deleted event with status cancelled on the organizer's calendar so that it can be restored; this is
// not a permanent delete. Google has no cancellation message, so a comment is refused, not dropped.
func (c *calendar) Cancel(ctx context.Context, id model.EventID, comment string) error {
	if comment != "" {
		return failure("Calendar", "ValueError")
	}
	_, err := c.c.request(ctx, "Calendar", "DELETE", c.eventURL(id), notifyAll(), nil, "")
	return err
}

var responseStatus = map[model.MeetingResponse]string{model.ResponseAccept: "accepted", model.ResponseTentative: "tentative", model.ResponseDecline: "declined"}

// Respond sends the full attendee list with only the account's own entry changed: its response, and the
// comment given now (none drops an earlier one). It does not set guest permissions: Google refuses shared
// properties from a copy that is not the organizer's (403 forbiddenForNonOrganizer).
func (c *calendar) Respond(ctx context.Context, id model.EventID, response model.EventResponse) error {
	status, ok := responseStatus[response.Response]
	if !ok {
		return failure("Calendar", "ValueError")
	}
	current, err := c.c.get(ctx, "Calendar", c.eventURL(id), nil)
	if err != nil {
		return err
	}
	list, err := attendees(current)
	if err != nil {
		return err
	}
	out, self := make([]any, 0, len(list)), 0
	for _, entry := range list {
		var isSelf bool
		if raw, ok := entry["self"]; ok && json.Unmarshal(raw, &isSelf) != nil {
			return failure("Calendar", "InvalidMetadata")
		}
		if !isSelf {
			out = append(out, entry)
			continue
		}
		self++
		changed := map[string]any{}
		for k, v := range entry {
			if k != "comment" {
				changed[k] = v
			}
		}
		changed["responseStatus"] = status
		if response.Comment != "" {
			changed["comment"] = response.Comment
		}
		out = append(out, changed)
	}
	if self != 1 {
		return failure("Calendar", "InvalidMetadata")
	}
	_, err = c.calendarWrite(ctx, "PATCH", c.eventURL(id), notifyAll(), map[string]any{"attendees": out})
	return err
}
