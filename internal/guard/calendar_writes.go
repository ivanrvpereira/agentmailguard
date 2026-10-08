package guard

import (
	"context"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/audit"
	"github.com/ivanrvpereira/agentmailguard/internal/config"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// Public calendar refusals. Their text is safe to return to clients.
const (
	CalendarWriteUnsupported        = GuardError("calendar writes are not supported for this account")
	CalendarAvailabilityUnsupported = GuardError("calendar availability is not supported for this account")
	CalendarRecurring               = GuardError("calendar write refused: recurring events are not supported")
	CalendarCancelled               = GuardError("calendar write refused: event is cancelled")
	CalendarOrganizerOnly           = GuardError("calendar write refused: only the organizer can update or cancel")
	CalendarOrganizerCannotRespond  = GuardError("calendar response refused: organizer cannot respond")
)

// calendarWrite holds the mutation gate from check through write and audit. A denied action stops before
// any provider access. Otherwise check validates input, fetches the writable event, and may resolve what
// write sends; an action that the policy approves is stored with args only after check passes.
// write returns the event ID to audit; provider errors become safe codes. ref is the event the action changes.
func (g *Guard) calendarWrite(ctx context.Context, op audit.Operation, ref *string, args any, check func() error, write func(model.CalendarWriter) (model.EventID, error)) (model.EventID, error) {
	if err := g.acquire(ctx); err != nil {
		return "", err
	}
	defer g.release()
	writer, ok := g.calendar.(model.CalendarWriter)
	if !ok {
		return "", CalendarWriteUnsupported
	}
	key := string(op)
	d := decision{value: g.base(ctx, key)}
	if d.value != config.Deny {
		if err := check(); err != nil {
			return "", err
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
	}
	if err := g.single(ctx, op, key, d, ref, args); err != nil {
		return "", err
	}
	id, err := write(writer)
	if err != nil {
		return "", safeErr(ctx, err)
	}
	if id == "" {
		return "", GuardError("ValueError")
	}
	if err := g.recordDecision(ctx, op, key, d, ref, nil, nil); err != nil {
		return "", err
	}
	if err := g.log.Record(context.WithoutCancel(ctx), g.account, op, []model.MutationResult{{ID: id, OK: true}}, audit.Options{}); err != nil {
		return "", GuardError("AuditError")
	}
	return id, nil
}

func (g *Guard) CalendarInvite(ctx context.Context, event model.NewEvent) (string, error) {
	args := inviteArgs{To: event.Attendees, Subject: event.Subject, Body: event.Body, Start: event.Start.Format(time.RFC3339Nano), End: event.End.Format(time.RFC3339Nano), Location: event.Location, Teams: event.Teams}
	return g.calendarWrite(ctx, audit.CalendarInvite, nil, args, func() error {
		if !calendarRangeValid(event.Start, event.End) {
			return GuardError("invalid request")
		}
		return nil
	}, func(w model.CalendarWriter) (string, error) { return w.Invite(ctx, event) })
}

// CalendarUpdate applies change to the organizer's event. duration 0 means not given; a positive duration
// sets the end from the new or current start. A start-only move keeps the current duration.
func (g *Guard) CalendarUpdate(ctx context.Context, id string, change model.EventChange, duration time.Duration) error {
	_, err := g.calendarWrite(ctx, audit.CalendarUpdate, &id, updateArgs(id, change, duration), func() error {
		if change.Empty() && duration == 0 || duration < 0 || duration > 0 && change.End != nil || change.AttendeeConflict() {
			return GuardError("invalid request")
		}
		event, err := g.calendarWritableEvent(ctx, id, asOrganizer)
		if err != nil {
			return err
		}
		start := event.Start
		if change.Start != nil {
			start = *change.Start
		}
		if duration > 0 {
			change.End = pointer(start.Add(duration))
		} else if change.Start != nil && change.End == nil {
			kept := event.End.Sub(event.Start)
			if !calendarRangeValid(event.Start, event.End) || !event.Start.Add(kept).Equal(event.End) {
				return GuardError("invalid request")
			}
			change.End = pointer(start.Add(kept))
		}
		if change.Start != nil || change.End != nil {
			end := event.End
			if change.End != nil {
				end = *change.End
			}
			if !calendarRangeValid(start, end) {
				return GuardError("invalid request")
			}
		}
		return nil
	}, func(w model.CalendarWriter) (string, error) { return id, w.Update(ctx, id, change) })
	return err
}

func (g *Guard) CalendarCancel(ctx context.Context, id, comment string) error {
	_, err := g.calendarWrite(ctx, audit.CalendarCancel, &id, cancelArgs{id, comment}, func() error {
		_, err := g.calendarWritableEvent(ctx, id, asOrganizer)
		return err
	}, func(w model.CalendarWriter) (string, error) { return id, w.Cancel(ctx, id, comment) })
	return err
}

func (g *Guard) CalendarRespond(ctx context.Context, id string, response model.EventResponse) error {
	_, err := g.calendarWrite(ctx, audit.CalendarRespond, &id, respondArgs{id, response.Response, response.Comment}, func() error {
		if !response.Response.Valid() {
			return GuardError("invalid request")
		}
		_, err := g.calendarWritableEvent(ctx, id, asAttendee)
		return err
	}, func(w model.CalendarWriter) (string, error) { return id, w.Respond(ctx, id, response) })
	return err
}

func calendarRangeValid(start, end time.Time) bool {
	return model.CalendarTimeValid(start) && model.CalendarTimeValid(end) && end.After(start)
}

// calendarRole is the role the account must hold in an event: organizers update and cancel, attendees respond.
type calendarRole bool

const (
	asOrganizer calendarRole = true
	asAttendee  calendarRole = false
)

// Raw event text stays inside the guard; only provider metadata establishes authority.
func (g *Guard) calendarWritableEvent(ctx context.Context, id string, role calendarRole) (model.Event, error) {
	event, err := g.calendar.Get(ctx, id)
	if err != nil {
		return model.Event{}, safeErr(ctx, err)
	}
	switch {
	case id == "" || event.ID != id:
		return model.Event{}, GuardError("ValueError")
	case event.Recurrence != nil:
		return model.Event{}, CalendarRecurring
	case event.IsCancelled:
		return model.Event{}, CalendarCancelled
	case role == asOrganizer && !event.IsOrganizer:
		return model.Event{}, CalendarOrganizerOnly
	case role == asAttendee && event.IsOrganizer:
		return model.Event{}, CalendarOrganizerCannotRespond
	}
	return event, nil
}
