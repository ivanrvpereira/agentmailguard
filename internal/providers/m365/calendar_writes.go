package m365

import (
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

func (c *calendar) Update(ctx context.Context, id model.EventID, change model.EventChange) error {
	// Guard resolves start-only moves; end-only changes are complete as sent.
	if change.Start != nil && change.End == nil {
		return invalidMetadata
	}
	if !c.c.lockMutation(ctx) {
		return requestFailed
	}
	defer func() { <-c.c.mutationGate }()
	body := map[string]any{}
	if change.Subject != nil {
		body["subject"] = *change.Subject
	}
	if change.Location != nil {
		body["location"] = map[string]string{"displayName": *change.Location}
	}
	if change.Start != nil {
		body["start"] = calendarTime(*change.Start)
	}
	if change.End != nil {
		body["end"] = calendarTime(*change.End)
	}
	// Enable only. The body is never sent, so the online-meeting details in it stay intact.
	if change.Teams {
		body["isOnlineMeeting"] = true
		body["onlineMeetingProvider"] = "teamsForBusiness"
	}
	if len(change.AddAttendees) > 0 || len(change.RemoveAttendees) > 0 {
		d, err := c.c.get(ctx, c.c.endpoint("/me/events/"+escapeID(id), url.Values{"$select": {"attendees"}}), true)
		if err != nil {
			return err
		}
		remove, seen := map[string]bool{}, map[string]bool{}
		for _, address := range change.RemoveAttendees {
			remove[strings.ToLower(address)] = true
		}
		attendees := []any{}
		var current []object
		if json.Unmarshal(d["attendees"], &current) != nil || current == nil {
			return invalidMetadata
		}
		for _, attendee := range current {
			address := text(obj(attendee, "emailAddress"), "address")
			if strings.TrimSpace(address) == "" {
				return invalidMetadata
			}
			switch text(attendee, "type") {
			case "required", "optional", "resource":
			default:
				return invalidMetadata
			}
			key := strings.ToLower(address)
			if !remove[key] && !seen[key] {
				attendees = append(attendees, attendee)
				seen[key] = true
			}
		}
		for _, address := range change.AddAttendees {
			if key := strings.ToLower(address); !seen[key] {
				attendees = append(attendees, map[string]any{"emailAddress": map[string]string{"address": address}, "type": "required"})
				seen[key] = true
			}
		}
		body["attendees"] = attendees
	}
	_, err := c.c.request(ctx, http.MethodPatch, c.c.endpoint("/me/events/"+escapeID(id), nil), body, true)
	return err
}

func (c *calendar) Cancel(ctx context.Context, id model.EventID, comment string) error {
	if !c.c.lockMutation(ctx) {
		return requestFailed
	}
	defer func() { <-c.c.mutationGate }()
	_, err := c.c.request(ctx, http.MethodPost, c.c.endpoint("/me/events/"+escapeID(id)+"/cancel", nil), map[string]any{"comment": comment}, true)
	return err
}

func (c *calendar) Respond(ctx context.Context, id model.EventID, response model.EventResponse) error {
	if !response.Response.Valid() {
		return invalidMetadata
	}
	action := string(response.Response)
	if response.Response == model.ResponseTentative {
		action = "tentativelyAccept"
	}
	if !c.c.lockMutation(ctx) {
		return requestFailed
	}
	defer func() { <-c.c.mutationGate }()
	_, err := c.c.request(ctx, http.MethodPost, c.c.endpoint("/me/events/"+escapeID(id)+"/"+action, nil), map[string]any{"comment": response.Comment, "sendResponse": true}, true)
	return err
}

func calendarTime(t time.Time) map[string]string {
	return map[string]string{"dateTime": t.UTC().Format("2006-01-02T15:04:05.9999999"), "timeZone": "UTC"}
}

// \r\n comes first so it gives one <br>.
var htmlLineBreaks = strings.NewReplacer("\r\n", "<br>", "\r", "<br>", "\n", "<br>")

func (c *calendar) Invite(ctx context.Context, event model.NewEvent) (model.EventID, error) {
	if !c.c.lockMutation(ctx) {
		return "", requestFailed
	}
	defer func() { <-c.c.mutationGate }()
	attendees := recipients(event.Attendees)
	for _, attendee := range attendees {
		attendee["type"] = "required"
	}
	// The plain-text body goes as escaped HTML, so Outlook shows the Teams block that Graph adds as formatted HTML.
	body := map[string]any{
		"subject": event.Subject, "body": map[string]string{"contentType": "html", "content": htmlLineBreaks.Replace(html.EscapeString(event.Body))},
		"location": map[string]string{"displayName": event.Location},
		"start":    calendarTime(event.Start), "end": calendarTime(event.End), "attendees": attendees,
		"isOnlineMeeting": event.Teams,
	}
	if event.Teams {
		body["onlineMeetingProvider"] = "teamsForBusiness"
	}
	d, err := c.c.request(ctx, http.MethodPost, c.c.endpoint("/me/events", nil), body, true)
	if err != nil {
		return "", err
	}
	id := text(d, "id")
	if id == "" {
		return "", invalidMetadata
	}
	return id, nil
}
