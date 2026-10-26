package fake

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// Folders marks trash and every folder below it, so the guard refuses moves there.
func (b *mailbox) Folders(ctx context.Context) ([]model.Folder, error) {
	db, err := b.p.conn(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, name, parent FROM folders WHERE account = ? ORDER BY rowid`, b.p.account)
	if err != nil {
		return nil, storageError
	}
	defer rows.Close()
	out := []model.Folder{}
	parents := map[string]string{}
	for rows.Next() {
		var f model.Folder
		var parent sql.NullString
		if rows.Scan(&f.ID, &f.Name, &parent) != nil {
			return nil, storageError
		}
		parents[f.ID] = parent.String
		out = append(out, f)
	}
	if rows.Err() != nil {
		return nil, storageError
	}
	for i := range out {
		// The step limit ends parent cycles.
		for id, steps := out[i].ID, 0; id != "" && steps <= len(parents); id, steps = parents[id], steps+1 {
			if id == "trash" {
				out[i].Trash = true
				break
			}
		}
	}
	return out, nil
}

func (b *mailbox) Categories(ctx context.Context) ([]string, error) {
	db, err := b.p.conn(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT name FROM categories WHERE account = ? ORDER BY rowid`, b.p.account)
	if err != nil {
		return nil, storageError
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			return nil, storageError
		}
		out = append(out, name)
	}
	if rows.Err() != nil {
		return nil, storageError
	}
	return out, nil
}

type execer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// update changes one message of the account; a missing message is notFound.
func (b *mailbox) update(ctx context.Context, db execer, id, set string, args ...any) error {
	r, err := db.ExecContext(ctx, `UPDATE messages SET `+set+` WHERE account = ? AND id = ?`, append(args, b.p.account, id)...)
	if err != nil {
		return storageError
	}
	if n, err := r.RowsAffected(); err != nil || n == 0 {
		return notFound
	}
	return nil
}

// each changes every ID on its own and returns one result per input ID, in input order.
func (b *mailbox) each(ctx context.Context, ids []string, change func(*sql.DB, string) error) ([]model.MutationResult, error) {
	db, err := b.p.conn(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]model.MutationResult, len(ids))
	for i, id := range ids {
		out[i] = model.MutationResult{ID: id, OK: true}
		if err := change(db, id); err != nil {
			code := err.Error()
			out[i] = model.MutationResult{ID: id, Error: &code}
		}
	}
	return out, nil
}

func (b *mailbox) Archive(ctx context.Context, ids []string) ([]model.MutationResult, error) {
	return b.Move(ctx, ids, "archive")
}
func (b *mailbox) Trash(ctx context.Context, ids []string) ([]model.MutationResult, error) {
	return b.Move(ctx, ids, "trash")
}
func (b *mailbox) Move(ctx context.Context, ids []string, dest string) ([]model.MutationResult, error) {
	if err := b.folderExists(ctx, dest); err != nil {
		return nil, err
	}
	return b.each(ctx, ids, func(db *sql.DB, id string) error { return b.update(ctx, db, id, `folder = ?`, dest) })
}
func (b *mailbox) MarkRead(ctx context.Context, ids []string, read bool) ([]model.MutationResult, error) {
	return b.each(ctx, ids, func(db *sql.DB, id string) error { return b.update(ctx, db, id, `is_read = ?`, read) })
}
func (b *mailbox) Flag(ctx context.Context, ids []string, state model.FlagState) ([]model.MutationResult, error) {
	if state != model.Flagged && state != model.Complete && state != model.NoFlag {
		return nil, invalid
	}
	return b.each(ctx, ids, func(db *sql.DB, id string) error { return b.update(ctx, db, id, `flag = ?`, string(state)) })
}

// Categorize reads and writes the categories of each message in one transaction.
func (b *mailbox) Categorize(ctx context.Context, ids []string, add, remove []string) ([]model.MutationResult, error) {
	return b.each(ctx, ids, func(db *sql.DB, id string) error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return storageError
		}
		defer tx.Rollback()
		var text string
		err = tx.QueryRowContext(ctx, `SELECT categories FROM messages WHERE account = ? AND id = ?`, b.p.account, id).Scan(&text)
		if errors.Is(err, sql.ErrNoRows) {
			return notFound
		}
		if err != nil {
			return storageError
		}
		current, err := list(text)
		if err != nil {
			return err
		}
		out := slices.DeleteFunc(current, func(name string) bool { return slices.Contains(remove, name) })
		for _, name := range add {
			if !slices.Contains(out, name) {
				out = append(out, name)
			}
		}
		encoded, _ := json.Marshal(out)
		if err := b.update(ctx, tx, id, `categories = ?`, string(encoded)); err != nil {
			return err
		}
		if tx.Commit() != nil {
			return storageError
		}
		return nil
	})
}

// outgoing is a message that the account writes: a draft, or a sent message. A nil thread makes it its own
// conversation.
type outgoing struct {
	to                         []string
	subject, body, contentType string
	attachments                []string
	origin                     string
	source, thread             *string
}

func created(d model.NewDraft) outgoing {
	return outgoing{to: d.To, subject: d.Subject, body: d.Body, contentType: string(model.Text), origin: "new"}
}

// reply answers the source sender with the comment as body.
func (b *mailbox) reply(ctx context.Context, id, comment string) (outgoing, error) {
	source, err := b.Get(ctx, []string{id}, false)
	if err != nil {
		return outgoing{}, err
	}
	m := source[0]
	return outgoing{to: []string{m.Sender.Address}, subject: "Re: " + m.Subject, body: comment, contentType: string(model.Text), origin: "reply", source: &id, thread: &m.ThreadID}, nil
}

// forward carries the source body and attachments to new recipients.
func (b *mailbox) forward(ctx context.Context, id string, to []string) (outgoing, error) {
	source, err := b.Get(ctx, []string{id}, true)
	if err != nil {
		return outgoing{}, err
	}
	m := source[0]
	return outgoing{to: to, subject: "Fw: " + m.Subject, body: *m.Body, contentType: string(*m.ContentType), attachments: m.Attachments, origin: "forward", source: &id, thread: &m.ThreadID}, nil
}

// insert stores o with one INSERT, as a draft in drafts or as a sent message in sent. A generated ID is
// random; the primary key refuses, never overwrites, a row with the same ID.
func (b *mailbox) insert(ctx context.Context, o outgoing, draft bool) (string, error) {
	db, err := b.p.conn(ctx)
	if err != nil {
		return "", err
	}
	from, err := b.p.identity(ctx, db)
	if err != nil {
		return "", err
	}
	id := "fake-" + rand.Text()
	folder := "sent"
	if draft {
		folder = "drafts"
	}
	if o.to == nil {
		o.to = []string{}
	}
	if o.attachments == nil {
		o.attachments = []string{}
	}
	to, _ := json.Marshal(o.to)
	attachments, _ := json.Marshal(o.attachments)
	_, err = db.ExecContext(ctx, `INSERT INTO messages (account, id, folder, sender_name, sender_address, to_addresses, subject, body, content_type, received_at, is_draft, is_read, attachments, origin, source_id, thread_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`,
		b.p.account, id, folder, from.Name, from.Address, string(to), o.subject, o.body, o.contentType, b.p.now().UTC().Format(time.RFC3339Nano), draft, string(attachments), o.origin, o.source, o.thread)
	if err != nil {
		return "", storageError
	}
	return id, nil
}

func (b *mailbox) DraftNew(ctx context.Context, d model.NewDraft) (string, error) {
	return b.insert(ctx, created(d), true)
}
func (b *mailbox) DraftReply(ctx context.Context, id, comment string) (string, error) {
	o, err := b.reply(ctx, id, comment)
	if err != nil {
		return "", err
	}
	return b.insert(ctx, o, true)
}
func (b *mailbox) DraftForward(ctx context.Context, id string, to []string) (string, error) {
	o, err := b.forward(ctx, id, to)
	if err != nil {
		return "", err
	}
	return b.insert(ctx, o, true)
}

// DraftUpdate changes only drafts that DraftNew made.
func (b *mailbox) DraftUpdate(ctx context.Context, id, body string) (string, error) {
	db, err := b.p.conn(ctx)
	if err != nil {
		return "", err
	}
	r, err := db.ExecContext(ctx, `UPDATE messages SET body = ?, content_type = 'text' WHERE account = ? AND id = ? AND is_draft = 1 AND origin = 'new'`, body, b.p.account, id)
	if err != nil {
		return "", storageError
	}
	if n, err := r.RowsAffected(); err != nil || n == 0 {
		if _, err := b.Get(ctx, []string{id}, false); err != nil {
			return "", err
		}
		return "", invalid
	}
	return id, nil
}

// Sends only store the message in sent. Nothing leaves the machine.
func (b *mailbox) SendNew(ctx context.Context, d model.NewDraft) error {
	_, err := b.insert(ctx, created(d), false)
	return err
}
func (b *mailbox) SendReply(ctx context.Context, id, comment string) error {
	o, err := b.reply(ctx, id, comment)
	if err == nil {
		_, err = b.insert(ctx, o, false)
	}
	return err
}
func (b *mailbox) SendForward(ctx context.Context, id string, to []string) error {
	o, err := b.forward(ctx, id, to)
	if err == nil {
		_, err = b.insert(ctx, o, false)
	}
	return err
}
