package guard

import (
	"context"
	"strings"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/audit"
	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

const calendarUnsupported = GuardError("calendar writes are not supported for this account")

func (g *Guard) CalendarInvite(ctx context.Context, event model.NewEvent) (string, error) {
	if err := g.acquire(ctx); err != nil {
		return "", err
	}
	defer g.release()
	writer, ok := g.calendar.(model.CalendarWriter)
	if !ok {
		return "", calendarUnsupported
	}
	if !calendarRangeValid(event.Start, event.End) {
		return "", GuardError("invalid request")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	id, err := writer.Invite(ctx, event)
	if err != nil {
		return "", safeErr(ctx, err)
	}
	if id == "" {
		return "", GuardError("ValueError")
	}
	if err := g.calendarAudit(ctx, audit.CalendarInvite, id); err != nil {
		return "", err
	}
	return id, nil
}

func (g *Guard) CalendarUpdate(ctx context.Context, id string, change model.EventChange) error {
	if err := g.acquire(ctx); err != nil {
		return err
	}
	defer g.release()
	writer, ok := g.calendar.(model.CalendarWriter)
	if !ok {
		return calendarUnsupported
	}
	if change.Subject == nil && change.Location == nil && change.Start == nil && change.End == nil && change.Duration == nil && len(change.AddAttendees)+len(change.RemoveAttendees) == 0 && !change.Teams {
		return GuardError("calendar update needs at least one change")
	}
	if change.Duration != nil && (*change.Duration <= 0 || change.End != nil) {
		return GuardError("invalid request")
	}
	for _, add := range change.AddAttendees {
		for _, remove := range change.RemoveAttendees {
			if strings.EqualFold(add, remove) {
				return GuardError("invalid request")
			}
		}
	}
	event, err := g.calendarWritableEvent(ctx, id, true)
	if err != nil {
		return err
	}
	start := event.Start
	if change.Start != nil {
		start = *change.Start
	}
	if change.Duration != nil {
		change.End = pointer(start.Add(*change.Duration))
		change.Duration = nil
	} else if change.Start != nil && change.End == nil {
		duration := event.End.Sub(event.Start)
		if !calendarRangeValid(event.Start, event.End) || !event.Start.Add(duration).Equal(event.End) {
			return GuardError("invalid request")
		}
		change.End = pointer(start.Add(duration))
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
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := writer.Update(ctx, id, change); err != nil {
		return safeErr(ctx, err)
	}
	return g.calendarAudit(ctx, audit.CalendarUpdate, id)
}

func (g *Guard) CalendarCancel(ctx context.Context, id, comment string) error {
	return g.calendarAction(ctx, id, audit.CalendarCancel, model.EventResponse{Comment: comment})
}

func (g *Guard) CalendarRespond(ctx context.Context, id string, response model.EventResponse) error {
	return g.calendarAction(ctx, id, audit.CalendarRespond, response)
}

func (g *Guard) calendarAction(ctx context.Context, id string, op audit.Operation, response model.EventResponse) error {
	if err := g.acquire(ctx); err != nil {
		return err
	}
	defer g.release()
	writer, ok := g.calendar.(model.CalendarWriter)
	if !ok {
		return calendarUnsupported
	}
	if op == audit.CalendarRespond && response.Response != "accept" && response.Response != "tentative" && response.Response != "decline" {
		return GuardError("invalid request")
	}
	if _, err := g.calendarWritableEvent(ctx, id, op == audit.CalendarCancel); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var err error
	if op == audit.CalendarCancel {
		err = writer.Cancel(ctx, id, response.Comment)
	} else {
		err = writer.Respond(ctx, id, response)
	}
	if err != nil {
		return safeErr(ctx, err)
	}
	return g.calendarAudit(ctx, op, id)
}

func calendarRangeValid(start, end time.Time) bool {
	for _, t := range []time.Time{start, end} {
		if t.Year() < 1 || t.Year() > 9999 || t.UTC().Year() < 1 || t.UTC().Year() > 9999 {
			return false
		}
	}
	return end.After(start)
}

// Raw event text stays inside the guard; only provider metadata establishes authority.
func (g *Guard) calendarWritableEvent(ctx context.Context, id string, organizer bool) (model.Event, error) {
	event, err := g.calendar.Get(ctx, id)
	if err != nil {
		return model.Event{}, safeErr(ctx, err)
	}
	if event.ID != id {
		return model.Event{}, GuardError("ValueError")
	}
	if event.Recurrence != nil {
		return model.Event{}, GuardError("calendar write refused: recurring events are not supported")
	}
	if event.IsCancelled {
		return model.Event{}, GuardError("calendar write refused: event is cancelled")
	}
	if organizer && !event.IsOrganizer {
		return model.Event{}, GuardError("calendar write refused: only the organizer can update or cancel")
	}
	if !organizer && event.IsOrganizer {
		return model.Event{}, GuardError("calendar response refused: organizer cannot respond")
	}
	return event, nil
}

func (g *Guard) calendarAudit(ctx context.Context, op audit.Operation, id string) error {
	if err := g.log.Record(context.WithoutCancel(ctx), g.account, op, []model.MutationResult{{ID: id, OK: true}}, audit.Options{}); err != nil {
		return GuardError("AuditError")
	}
	return nil
}
