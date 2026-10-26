package m365

import (
	"context"
	"encoding/json"
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
	// The plain-text body goes as escaped HTML, as in Invite. It replaces the whole body.
	if change.Body != nil {
		body["body"] = map[string]string{"contentType": "html", "content": model.HTMLBody(*change.Body)}
	}
	if change.Start != nil {
		body["start"] = calendarTime(*change.Start)
	}
	if change.End != nil {
		body["end"] = calendarTime(*change.End)
	}
	// Enable only. Without a body change the body is not sent, so the online-meeting details in it stay intact.
	if change.Online {
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

// Respond notifies the organizer unless NoNotify is set: sendResponse false records the response only
// (https://learn.microsoft.com/en-us/graph/api/event-accept).
func (c *calendar) Respond(ctx context.Context, id model.EventID, response model.EventResponse) error {
	if !response.Valid() {
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
	_, err := c.c.request(ctx, http.MethodPost, c.c.endpoint("/me/events/"+escapeID(id)+"/"+action, nil), map[string]any{"comment": response.Comment, "sendResponse": !response.NoNotify}, true)
	return err
}

func calendarTime(t time.Time) map[string]string {
	return map[string]string{"dateTime": t.UTC().Format("2006-01-02T15:04:05.9999999"), "timeZone": "UTC"}
}

func (c *calendar) Invite(ctx context.Context, event model.NewEvent) (model.EventID, error) {
	if !c.c.lockMutation(ctx) {
		return "", requestFailed
	}
	defer func() { <-c.c.mutationGate }()
	attendees := recipients(event.Attendees)
	for _, attendee := range attendees {
		attendee["type"] = "required"
	}
	// Graph attendee type: required, optional, or resource (https://learn.microsoft.com/en-us/graph/api/resources/attendee).
	for _, attendee := range recipients(event.Optional) {
		attendee["type"] = "optional"
		attendees = append(attendees, attendee)
	}
	// The plain-text body goes as escaped HTML, so Outlook shows the Teams block that Graph adds as formatted HTML.
	body := map[string]any{
		"subject": event.Subject, "body": map[string]string{"contentType": "html", "content": model.HTMLBody(event.Body)},
		"location": map[string]string{"displayName": event.Location},
		"start":    calendarTime(event.Start), "end": calendarTime(event.End), "attendees": attendees,
		"isOnlineMeeting": event.Online,
	}
	if event.Online {
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
