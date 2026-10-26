package fake

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

func stamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// Invite stores an event that the account organizes, with an empty organizer_address, and one attendee
// row with response none per address, optional for the optional ones. An address listed twice keeps its first
// row. The body is HTML, as with both real providers: the agent's HTML as is, or plain text escaped.
func (c *calendar) Invite(ctx context.Context, event model.NewEvent) (model.EventID, error) {
	db, err := c.p.conn(ctx)
	if err != nil {
		return "", err
	}
	id := "fake-" + rand.Text()
	var join *string
	if event.Online {
		url := "https://meet.fake.test/" + id
		join = &url
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return "", storageError
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO events (account, id, subject, body, content_type, location, start_at, end_at, join_url) VALUES (?, ?, ?, ?, 'html', ?, ?, ?, ?)`,
		c.p.account, id, event.Subject, model.EventBodyHTML(event.Body, event.HTML), event.Location, stamp(event.Start), stamp(event.End), join)
	if err != nil {
		return "", storageError
	}
	for i, address := range slices.Concat(event.Attendees, event.Optional) {
		optional := i >= len(event.Attendees)
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO attendees (account, event_id, address, optional) VALUES (?, ?, ?, ?)`, c.p.account, id, address, optional); err != nil {
			return "", storageError
		}
	}
	if err := commit(tx); err != nil {
		return "", err
	}
	return id, nil
}

// begin starts a transaction for a write to the account's event id. The account must organize the event
// when organizer is true, and must not organize it when organizer is false. Cancelled events are refused.
func (c *calendar) begin(ctx context.Context, id string, organizer bool) (*sql.Tx, model.Address, error) {
	db, err := c.p.conn(ctx)
	if err != nil {
		return nil, model.Address{}, err
	}
	// The identity is read first: the transaction holds the only connection.
	self, err := c.p.identity(ctx, db)
	if err != nil {
		return nil, self, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, self, storageError
	}
	var address string
	var cancelled bool
	err = tx.QueryRowContext(ctx, `SELECT organizer_address, is_cancelled FROM events WHERE account = ? AND id = ?`, c.p.account, id).Scan(&address, &cancelled)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		err = notFound
	case err != nil:
		err = storageError
	case cancelled || organizes(address, self) != organizer:
		err = invalid
	}
	if err != nil {
		_ = tx.Rollback()
		return nil, self, err
	}
	return tx, self, nil
}

func commit(tx *sql.Tx) error {
	if tx.Commit() != nil {
		return storageError
	}
	return nil
}

// Update changes the event and its attendee rows in one transaction. A removed attendee loses its row.
// Added attendees get response none; an address already present (ignoring case) is kept. Online adds a link only when the event has none.
func (c *calendar) Update(ctx context.Context, id model.EventID, change model.EventChange) error {
	tx, _, err := c.begin(ctx, id, true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	set, args := []string{}, []any{}
	add := func(clause string, value any) { set, args = append(set, clause), append(args, value) }
	if change.Subject != nil {
		add("subject = ?", *change.Subject)
	}
	if change.Location != nil {
		add("location = ?", *change.Location)
	}
	// A new body is HTML, as in Invite.
	if change.Body != nil {
		add("body = ?", model.EventBodyHTML(*change.Body, change.HTML))
		add("content_type = ?", "html")
	}
	if change.Start != nil {
		add("start_at = ?", stamp(*change.Start))
	}
	if change.End != nil {
		add("end_at = ?", stamp(*change.End))
	}
	if change.Online {
		add("join_url = coalesce(join_url, ?)", "https://meet.fake.test/"+id)
	}
	if len(set) > 0 {
		if _, err := tx.ExecContext(ctx, `UPDATE events SET `+strings.Join(set, ", ")+` WHERE account = ? AND id = ?`, append(args, c.p.account, id)...); err != nil {
			return storageError
		}
	}
	// The address column compares without case.
	for _, address := range change.RemoveAttendees {
		if _, err := tx.ExecContext(ctx, `DELETE FROM attendees WHERE account = ? AND event_id = ? AND address = ?`, c.p.account, id, address); err != nil {
			return storageError
		}
	}
	for _, address := range change.AddAttendees {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO attendees (account, event_id, address) VALUES (?, ?, ?)`, c.p.account, id, address); err != nil {
			return storageError
		}
	}
	return commit(tx)
}

// Cancel marks the event cancelled. No row is deleted.
func (c *calendar) Cancel(ctx context.Context, id model.EventID, comment string) error {
	tx, _, err := c.begin(ctx, id, true)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE events SET is_cancelled = 1, cancel_comment = ? WHERE account = ? AND id = ?`, comment, c.p.account, id); err != nil {
		return storageError
	}
	return commit(tx)
}

var responses = map[model.MeetingResponse]string{model.ResponseAccept: "accepted", model.ResponseTentative: "tentativelyAccepted", model.ResponseDecline: "declined"}

// Respond sets the response and comment of the account's own attendee row, and adds the row if the
// event has none for the account address (ignoring case).
func (c *calendar) Respond(ctx context.Context, id model.EventID, response model.EventResponse) error {
	value, ok := responses[response.Response]
	if !ok {
		return invalid
	}
	tx, self, err := c.begin(ctx, id, false)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO attendees (account, event_id, address, name, response, comment) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (account, event_id, address) DO UPDATE SET response = excluded.response, comment = excluded.comment`,
		c.p.account, id, self.Address, self.Name, value, response.Comment)
	if err != nil {
		return storageError
	}
	return commit(tx)
}
