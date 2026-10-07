package m365

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// Graph reports workingElsewhere as 0 (free) for backward compatibility; 4 is never requested.
var graphAvailability = map[byte]model.AvailabilityStatus{'0': model.SlotFree, '1': model.SlotTentative, '2': model.SlotBusy, '3': model.SlotOutOfOffice}

// Availability reads free/busy metadata only. Results are labeled with the requested addresses and
// never carry schedule items, working hours, returned IDs, or provider error text.
func (c *calendar) Availability(ctx context.Context, addresses []string, start, end time.Time) ([]model.Schedule, error) {
	slots := model.AvailabilitySlots(start, end)
	// Graph limits: at most 20 queried entities and a range shorter than 62 days.
	if slots == 0 || end.Sub(start) >= 62*24*time.Hour || len(addresses) == 0 || len(addresses) > 20 {
		return nil, invalidMetadata
	}
	body := map[string]any{"schedules": addresses, "startTime": calendarTime(start), "endTime": calendarTime(end), "availabilityViewInterval": int(model.AvailabilityInterval / time.Minute)}
	d, err := c.c.request(ctx, http.MethodPost, c.c.endpoint("/me/calendar/getSchedule", nil), body, true)
	if err != nil {
		return nil, err
	}
	items, err := pageItems(d)
	if err != nil {
		return nil, err
	}
	// A participant is known only with exactly one error-free entry and a complete string view.
	views, entries := map[string]string{}, map[string]int{}
	for _, item := range items {
		key := strings.ToLower(text(item, "scheduleId"))
		entries[key]++
		var view *string
		if e, ok := item["error"]; (!ok || string(e) == "null") && json.Unmarshal(item["availabilityView"], &view) == nil && view != nil {
			views[key] = *view
		}
	}
	out := make([]model.Schedule, len(addresses))
	for i, address := range addresses {
		key := strings.ToLower(address)
		view := views[key]
		statuses := make([]model.AvailabilityStatus, slots)
		known := entries[key] == 1 && len(view) == slots
		for j := 0; known && j < slots; j++ {
			statuses[j], known = graphAvailability[view[j]]
		}
		if !known {
			for j := range statuses {
				statuses[j] = model.SlotUnknown
			}
		}
		out[i] = model.Schedule{Address: address, Slots: statuses}
	}
	return out, nil
}
