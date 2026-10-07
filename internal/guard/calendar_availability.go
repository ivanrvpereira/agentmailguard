package guard

import (
	"context"
	"slices"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// CalendarAvailability returns free/busy metadata for the requested addresses only. It is a read:
// no screening, cache, or audit. Data that the provider does not prove for a participant is unknown.
func (g *Guard) CalendarAvailability(ctx context.Context, addresses []string, start, end time.Time) ([]model.Schedule, error) {
	reader, ok := g.calendar.(model.CalendarAvailability)
	if !ok {
		return nil, CalendarAvailabilityUnsupported
	}
	if !model.ValidAvailabilityRequest(addresses, start, end) {
		return nil, GuardError("invalid request")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	found, err := reader.Availability(ctx, addresses, start, end)
	if err != nil {
		return nil, safeErr(ctx, err)
	}
	// The provider contract is positional: entry i is labeled addresses[i]. A wrong entry count makes every
	// participant unknown; a mislabeled entry, wrong slot count, or unproven status makes that participant unknown.
	slots := model.AvailabilitySlots(start, end)
	counted := len(found) == len(addresses)
	out := make([]model.Schedule, len(addresses))
	for i, address := range addresses {
		out[i] = model.Schedule{Address: address, Slots: slices.Repeat([]model.AvailabilityStatus{model.SlotUnknown}, slots)}
		if counted && found[i].Address == address && len(found[i].Slots) == slots && !slices.ContainsFunc(found[i].Slots, unproven) {
			out[i].Slots = slices.Clone(found[i].Slots)
		}
	}
	return out, nil
}

// unproven reports a status other than the four a provider can establish.
func unproven(s model.AvailabilityStatus) bool {
	return s != model.SlotFree && s != model.SlotTentative && s != model.SlotBusy && s != model.SlotOutOfOffice
}
