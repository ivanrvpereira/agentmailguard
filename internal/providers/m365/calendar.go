package m365

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

type calendar struct{ c *Client }

func (c *Client) Calendar() model.Calendar { return &calendar{c} }
func calendarQuery() url.Values {
	return url.Values{"$select": {"id,subject,start,end,isAllDay,type,recurrence,location,organizer,isOrganizer,attendees,onlineMeeting,sensitivity,categories,showAs,isCancelled,body"}, "$expand": {attachments}}
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

// Search posts one Microsoft Search request for events in the primary calendar, without size, so Graph's default
// page applies (https://learn.microsoft.com/en-us/graph/search-concept-events). A hit carries only some event
// fields, so each hit is read again with Get. Hit IDs come in the EWS format, which /me/events/{id} refuses (checked
// live on 2026-10-26), so they are first translated to REST immutable IDs, the IDs that List and Get return.
func (c *calendar) Search(ctx context.Context, query string) ([]model.Event, error) {
	body := map[string]any{"requests": []any{map[string]any{"entityTypes": []string{"event"}, "query": map[string]any{"queryString": query}}}}
	d, err := c.c.request(ctx, http.MethodPost, c.c.endpoint("/search/query", nil), body, true)
	if err != nil {
		return nil, err
	}
	responses, err := pageItems(d)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, response := range responses {
		containers, ok := optionalArray(response, "hitsContainers")
		for _, container := range containers {
			hits, ok2 := optionalArray(container, "hits")
			ok = ok && ok2
			for _, hit := range hits {
				id := text(hit, "hitId")
				if id == "" {
					return nil, invalidMetadata
				}
				ids = append(ids, id)
			}
		}
		// 500 is the largest page that a Microsoft Search request can ask for.
		if !ok || len(ids) > 500 {
			return nil, invalidMetadata
		}
	}
	if len(ids) == 0 {
		return []model.Event{}, nil
	}
	ids, err = c.translate(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]model.Event, len(ids))
	errs := make([]error, len(ids))
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Go(func() { out[i], errs[i] = c.Get(ctx, id) })
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// translate turns EWS IDs into REST immutable IDs with one translateExchangeIds request
// (https://learn.microsoft.com/en-us/graph/api/user-translateexchangeids). It fails unless every ID comes back
// translated, in the request order.
func (c *calendar) translate(ctx context.Context, ids []string) ([]string, error) {
	body := map[string]any{"inputIds": ids, "sourceIdType": "ewsId", "targetIdType": "restImmutableEntryId"}
	d, err := c.c.request(ctx, http.MethodPost, c.c.endpoint("/me/translateExchangeIds", nil), body, false)
	if err != nil {
		return nil, err
	}
	results, err := pageItems(d)
	if err != nil {
		return nil, err
	}
	if len(results) != len(ids) {
		return nil, invalidMetadata
	}
	out := make([]string, len(ids))
	for i, r := range results {
		if out[i] = text(r, "targetId"); out[i] == "" || text(r, "sourceId") != ids[i] {
			return nil, invalidMetadata
		}
	}
	return out, nil
}

// optionalArray reads d[k] as an array of objects. A missing or null field is empty; any other type is not ok.
func optionalArray(d object, k string) ([]object, bool) {
	var a []object
	raw, found := d[k]
	return a, !found || string(raw) == "null" || json.Unmarshal(raw, &a) == nil
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
	// Write authority comes from this field, so absent, null, or non-boolean values fail closed.
	var organizer *bool
	if ev.ID == "" || json.Unmarshal(d["isOrganizer"], &organizer) != nil || organizer == nil {
		return ev, invalidMetadata
	}
	ev.IsOrganizer = *organizer
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
