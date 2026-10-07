package guard

import (
	"context"
	"strings"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// CalendarAvailability returns free/busy metadata for the requested addresses only. It is a read:
// no screening, cache, or audit. Data that the provider does not prove for a participant is unknown.
func (g *Guard) CalendarAvailability(ctx context.Context, addresses []string, start, end time.Time) ([]model.Schedule, error) {
	reader, ok := g.calendar.(model.CalendarAvailability)
	if !ok {
		return nil, GuardError("calendar availability is not supported for this account")
	}
	if len(addresses) == 0 || len(addresses) > 20 || !calendarRangeValid(start, end) || end.Sub(start) >= 62*24*time.Hour {
		return nil, GuardError("invalid request")
	}
	seen := map[string]bool{}
	for _, address := range addresses {
		key := strings.ToLower(address)
		if strings.TrimSpace(address) == "" || seen[key] {
			return nil, GuardError("invalid request")
		}
		seen[key] = true
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	found, err := reader.Availability(ctx, addresses, start, end)
	if err != nil {
		return nil, safeErr(ctx, err)
	}
	slots := model.AvailabilitySlots(start, end)
	byAddress, entries := map[string][]model.AvailabilityStatus{}, map[string]int{}
	for _, s := range found {
		key := strings.ToLower(s.Address)
		byAddress[key] = s.Slots
		entries[key]++
	}
	out := make([]model.Schedule, len(addresses))
	for i, address := range addresses {
		key := strings.ToLower(address)
		got := byAddress[key]
		statuses := make([]model.AvailabilityStatus, slots)
		known := entries[key] == 1 && len(got) == slots
		for j := 0; known && j < slots; j++ {
			switch got[j] {
			case model.SlotFree, model.SlotTentative, model.SlotBusy, model.SlotOutOfOffice:
				statuses[j] = got[j]
			default:
				known = false
			}
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
