package fake

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

type calendar struct{ p *Provider }

const eventColumns = `id, subject, body, content_type, location, start_at, end_at, is_all_day, recurrence, organizer_name, organizer_address, join_url, is_private, show_as, categories, attachments, is_cancelled`

// organizes reports whether the account organizes an event with the organizer address.
func organizes(organizer string, self model.Address) bool {
	return organizer == "" || strings.EqualFold(organizer, self.Address)
}

// scanEvent reads one row of eventColumns. self is the account's own address. Attendees are read later.
func scanEvent(row interface{ Scan(...any) error }, self model.Address) (model.Event, error) {
	var e model.Event
	var body, kind, start, end, categories, attachments string
	err := row.Scan(&e.ID, &e.Subject, &body, &kind, &e.Location, &start, &end, &e.IsAllDay, &e.Recurrence, &e.Organizer.Name, &e.Organizer.Address, &e.JoinURL, &e.IsPrivate, &e.ShowAs, &categories, &attachments, &e.IsCancelled)
	if errors.Is(err, sql.ErrNoRows) {
		return e, notFound
	}
	if err != nil {
		return e, storageError
	}
	e.IsOrganizer = organizes(e.Organizer.Address, self)
	if e.Organizer.Address == "" {
		e.Organizer = self
	}
	content := model.ContentType(kind)
	e.Body, e.ContentType = &body, &content
	var errs [4]error
	e.Start, errs[0] = time.Parse(time.RFC3339Nano, start)
	e.End, errs[1] = time.Parse(time.RFC3339Nano, end)
	e.Categories, errs[2] = list(categories)
	e.Attachments, errs[3] = list(attachments)
	if errors.Join(errs[:]...) != nil {
		return e, invalidData
	}
	e.Start, e.End = e.Start.UTC(), e.End.UTC()
	return e, nil
}

func (c *calendar) attendees(ctx context.Context, db *sql.DB, id string) ([]model.Attendee, error) {
	rows, err := db.QueryContext(ctx, `SELECT address, name, response FROM attendees WHERE account = ? AND event_id = ? ORDER BY rowid`, c.p.account, id)
	if err != nil {
		return nil, storageError
	}
	defer rows.Close()
	out := []model.Attendee{}
	for rows.Next() {
		var a model.Attendee
		if rows.Scan(&a.Address.Address, &a.Address.Name, &a.Response) != nil {
			return nil, storageError
		}
		out = append(out, a)
	}
	if rows.Err() != nil {
		return nil, storageError
	}
	return out, nil
}

// events reads the account's events that match where, with their attendees.
func (c *calendar) events(ctx context.Context, where string, args ...any) ([]model.Event, error) {
	db, err := c.p.conn(ctx)
	if err != nil {
		return nil, err
	}
	self, err := c.p.identity(ctx, db)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+eventColumns+` FROM events WHERE account = ? `+where, append([]any{c.p.account}, args...)...)
	if err != nil {
		return nil, storageError
	}
	out := []model.Event{}
	for rows.Next() {
		e, err := scanEvent(rows, self)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, e)
	}
	failed := rows.Err() != nil
	rows.Close()
	if failed {
		return nil, storageError
	}
	for i := range out {
		if out[i].Attendees, err = c.attendees(ctx, db, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// List returns the events that overlap [start, end), ordered by start. Seeded times can have any offset,
// so the range is checked after parsing.
func (c *calendar) List(ctx context.Context, start, end time.Time) ([]model.Event, error) {
	if !end.After(start) {
		return nil, invalid
	}
	all, err := c.events(ctx, `ORDER BY id`)
	if err != nil {
		return nil, err
	}
	out := slices.DeleteFunc(all, func(e model.Event) bool { return !e.Start.Before(end) || !e.End.After(start) })
	slices.SortStableFunc(out, func(a, b model.Event) int { return a.Start.Compare(b.Start) })
	return out, nil
}

// Search matches text, ignoring case, in the subject, body, location, and the names and addresses of the
// organizer and attendees, as Google Calendar's q does. It returns every match, ordered by start.
func (c *calendar) Search(ctx context.Context, text string) ([]model.Event, error) {
	all, err := c.events(ctx, `ORDER BY id`)
	if err != nil {
		return nil, err
	}
	text = strings.ToLower(text)
	has := func(s string) bool { return strings.Contains(strings.ToLower(s), text) }
	out := slices.DeleteFunc(all, func(e model.Event) bool {
		return !has(e.Subject) && !has(*e.Body) && !has(e.Location) && !has(e.Organizer.Name) && !has(e.Organizer.Address) &&
			!slices.ContainsFunc(e.Attendees, func(a model.Attendee) bool { return has(a.Address.Name) || has(a.Address.Address) })
	})
	slices.SortStableFunc(out, func(a, b model.Event) int { return a.Start.Compare(b.Start) })
	return out, nil
}

// strength orders the statuses of free_busy rows; a slot shows its strongest row.
var strength = map[model.AvailabilityStatus]int{model.SlotFree: 1, model.SlotTentative: 2, model.SlotBusy: 3, model.SlotOutOfOffice: 4}

// Availability reads only free_busy rows, never events. An address without rows, or with a row that
// cannot be read or that does not end after it starts, is unknown. Otherwise a slot without an
// overlapping row is free.
func (c *calendar) Availability(ctx context.Context, addresses []string, start, end time.Time) ([]model.Schedule, error) {
	if !model.ValidAvailabilityRequest(addresses, start, end) {
		return nil, invalid
	}
	db, err := c.p.conn(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]model.Schedule, len(addresses))
	for i, address := range addresses {
		statuses, err := c.schedule(ctx, db, address, start, end)
		if err != nil {
			return nil, err
		}
		out[i] = model.Schedule{Address: address, Slots: statuses}
	}
	return out, nil
}

func (c *calendar) schedule(ctx context.Context, db *sql.DB, address string, start, end time.Time) ([]model.AvailabilityStatus, error) {
	slots := model.AvailabilitySlots(start, end)
	unknown := slices.Repeat([]model.AvailabilityStatus{model.SlotUnknown}, slots)
	// The address column compares without case.
	rows, err := db.QueryContext(ctx, `SELECT start_at, end_at, status FROM free_busy WHERE account = ? AND address = ?`, c.p.account, address)
	if err != nil {
		return nil, storageError
	}
	defer rows.Close()
	out, found := slices.Repeat([]model.AvailabilityStatus{model.SlotFree}, slots), false
	for rows.Next() {
		var from, until, text string
		if rows.Scan(&from, &until, &text) != nil {
			return nil, storageError
		}
		found = true
		status := model.AvailabilityStatus(text)
		busyFrom, err := time.Parse(time.RFC3339Nano, from)
		busyUntil, err2 := time.Parse(time.RFC3339Nano, until)
		if err != nil || err2 != nil || !busyUntil.After(busyFrom) || strength[status] == 0 {
			return unknown, nil
		}
		for j := range out {
			slotStart := start.Add(time.Duration(j) * model.AvailabilityInterval)
			slotEnd := slotStart.Add(model.AvailabilityInterval)
			if slotEnd.After(end) {
				slotEnd = end
			}
			if busyFrom.Before(slotEnd) && busyUntil.After(slotStart) && strength[status] > strength[out[j]] {
				out[j] = status
			}
		}
	}
	if rows.Err() != nil {
		return nil, storageError
	}
	if !found {
		return unknown, nil
	}
	return out, nil
}

func (c *calendar) Get(ctx context.Context, id model.EventID) (model.Event, error) {
	found, err := c.events(ctx, `AND id = ?`, id)
	if err != nil {
		return model.Event{}, err
	}
	if len(found) == 0 {
		return model.Event{}, notFound
	}
	return found[0], nil
}
