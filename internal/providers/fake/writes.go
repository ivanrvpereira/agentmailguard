package fake

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

func (b *mailbox) Folders(ctx context.Context) ([]model.Folder, error) {
	db, err := b.p.conn(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, name FROM folders WHERE account = ? ORDER BY rowid`, b.p.account)
	if err != nil {
		return nil, storageError
	}
	defer rows.Close()
	out := []model.Folder{}
	for rows.Next() {
		var f model.Folder
		if rows.Scan(&f.ID, &f.Name) != nil {
			return nil, storageError
		}
		out = append(out, f)
	}
	if rows.Err() != nil {
		return nil, storageError
	}
	return out, nil
}

func (b *mailbox) ResolveFolder(ctx context.Context, folder string) (string, error) {
	if err := b.folderExists(ctx, folder); err != nil {
		return "", err
	}
	return folder, nil
}

func (b *mailbox) ClosedFolders(ctx context.Context) (map[string]model.Bin, error) {
	db, err := b.p.conn(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT id, parent FROM folders WHERE account = ?`, b.p.account)
	if err != nil {
		return nil, storageError
	}
	defer rows.Close()
	parents := map[string]string{}
	for rows.Next() {
		var id string
		var parent sql.NullString
		if rows.Scan(&id, &parent) != nil {
			return nil, storageError
		}
		parents[id] = parent.String
	}
	if rows.Err() != nil {
		return nil, storageError
	}
	for _, root := range []string{"trash", "spam"} {
		if _, ok := parents[root]; !ok {
			return nil, notFound
		}
	}
	out := map[string]model.Bin{}
	for folder := range parents {
		for id, steps := folder, 0; id != ""; id, steps = parents[id], steps+1 {
			if steps >= len(parents) {
				return nil, storageError
			}
			if id == "trash" || id == "spam" {
				out[folder] = model.Bin(id)
				break
			}
			if _, ok := parents[id]; !ok {
				return nil, notFound
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
	return b.Move(ctx, ids, "inbox", "archive")
}
func (b *mailbox) Trash(ctx context.Context, ids []string) ([]model.MutationResult, error) {
	return b.Move(ctx, ids, "inbox", "trash")
}
func (b *mailbox) Spam(ctx context.Context, ids []string) ([]model.MutationResult, error) {
	return b.Move(ctx, ids, "inbox", "spam")
}
func (b *mailbox) Move(ctx context.Context, ids []string, _, dest string) ([]model.MutationResult, error) {
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
	to, cc, bcc                []string
	subject, body, contentType string
	attachments                []string
	origin                     string
	source, thread             *string
}

func created(d model.NewDraft) outgoing {
	kind := model.Text
	if d.HTML {
		kind = model.HTML
	}
	return outgoing{to: d.To, cc: d.CC, bcc: d.BCC, subject: d.Subject, body: d.Body, contentType: string(kind), origin: "new"}
}

// reply answers the source by the Gmail rule (ReplyRecipients) plus the agent's CC and BCC. A plain-text reply
// body is the agent's text; an HTML reply keeps the quoted original below the agent's HTML.
func (b *mailbox) reply(ctx context.Context, id string, r model.Reply) (outgoing, error) {
	source, err := b.Get(ctx, []string{id}, true)
	if err != nil {
		return outgoing{}, err
	}
	m := source[0]
	to := b.replyRecipients(m, r.All).With(r.CC, r.BCC)
	body, kind := r.Body, model.Text
	if r.HTML {
		quote := *m.Body
		if *m.ContentType != model.HTML {
			quote = model.HTMLBody(quote)
		}
		body, kind = r.Body+"<blockquote>"+quote+"</blockquote>", model.HTML
	}
	return outgoing{to: to.To, cc: to.CC, bcc: to.BCC, subject: "Re: " + m.Subject, body: body, contentType: string(kind), origin: "reply", source: &id, thread: &m.ThreadID}, nil
}

// replyRecipients applies model.ReplyRecipients to m with the account's address as the own address. The fake has
// no Reply-To; a message in sent is one that the account sent.
func (b *mailbox) replyRecipients(m model.Message, all bool) model.Recipients {
	bare := func(list []model.Address) []string {
		out := []string{}
		for _, a := range list {
			out = append(out, a.Address)
		}
		return out
	}
	src := model.ReplySource{From: []string{m.Sender.Address}, To: bare(m.To), CC: bare(m.CC), Sent: slices.Contains(m.Folders, "sent")}
	return model.ReplyRecipients(src, all, []string{b.p.account})
}

func (b *mailbox) ReplyRecipients(ctx context.Context, id string, all bool) (model.Recipients, error) {
	source, err := b.Get(ctx, []string{id}, false)
	if err != nil {
		return model.Recipients{}, err
	}
	return b.replyRecipients(source[0], all), nil
}

// forward carries the agent's note, recipients, and body type, and the source's attachments. A plain-text forward
// body is the note; an HTML forward keeps the original below the note's HTML, as a reply does.
func (b *mailbox) forward(ctx context.Context, id string, f model.Forward) (outgoing, error) {
	source, err := b.Get(ctx, []string{id}, true)
	if err != nil {
		return outgoing{}, err
	}
	m := source[0]
	body, kind := f.Body, model.Text
	if f.HTML {
		quote := *m.Body
		if *m.ContentType != model.HTML {
			quote = model.HTMLBody(quote)
		}
		body, kind = f.Body+"<blockquote>"+quote+"</blockquote>", model.HTML
	}
	return outgoing{to: f.To, cc: f.CC, bcc: f.BCC, subject: "Fw: " + m.Subject, body: body, contentType: string(kind), attachments: m.Attachments, origin: "forward", source: &id, thread: &m.ThreadID}, nil
}

// jsonList is the JSON array text of a list column; nil is the empty array.
func jsonList(v []string) string {
	if v == nil {
		v = []string{}
	}
	out, _ := json.Marshal(v)
	return string(out)
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
	to, cc, bcc, attachments := jsonList(o.to), jsonList(o.cc), jsonList(o.bcc), jsonList(o.attachments)
	_, err = db.ExecContext(ctx, `INSERT INTO messages (account, id, folder, sender_name, sender_address, to_addresses, cc_addresses, bcc_addresses, subject, body, content_type, received_at, is_draft, is_read, attachments, origin, source_id, thread_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?)`,
		b.p.account, id, folder, from.Name, from.Address, to, cc, bcc, o.subject, o.body, o.contentType, b.p.now().UTC().Format(time.RFC3339Nano), draft, attachments, o.origin, o.source, o.thread)
	if err != nil {
		return "", storageError
	}
	return id, nil
}

// DraftNew makes a draft in its own conversation, so its thread ID is its message ID.
func (b *mailbox) DraftNew(ctx context.Context, d model.NewDraft) (model.DraftResult, error) {
	o := created(d)
	id, err := b.insert(ctx, o, true)
	if err != nil {
		return model.DraftResult{}, err
	}
	return model.DraftResult{ID: id, ThreadID: id, To: o.to, CC: o.cc, BCC: o.bcc, Subject: o.subject}, nil
}
func (b *mailbox) DraftReply(ctx context.Context, id string, r model.Reply) (model.DraftResult, error) {
	o, err := b.reply(ctx, id, r)
	if err != nil {
		return model.DraftResult{}, err
	}
	return b.draftResult(ctx, o)
}

// draftResult stores the reply or forward o as a draft in the conversation of its source and gives what it stored.
func (b *mailbox) draftResult(ctx context.Context, o outgoing) (model.DraftResult, error) {
	draft, err := b.insert(ctx, o, true)
	if err != nil {
		return model.DraftResult{}, err
	}
	return model.DraftResult{ID: draft, ThreadID: *o.thread, To: o.to, CC: o.cc, BCC: o.bcc, Subject: o.subject}, nil
}
func (b *mailbox) DraftForward(ctx context.Context, id string, f model.Forward) (model.DraftResult, error) {
	o, err := b.forward(ctx, id, f)
	if err != nil {
		return model.DraftResult{}, err
	}
	return b.draftResult(ctx, o)
}

// ReadDraft gives only IsDraft false for a message that is not a draft.
func (b *mailbox) ReadDraft(ctx context.Context, id string) (model.DraftContent, error) {
	found, err := b.Get(ctx, []string{id}, true)
	if err != nil || !found[0].IsDraft {
		return model.DraftContent{}, err
	}
	m := found[0]
	bare := func(list []model.Address) []string {
		out := []string{}
		for _, a := range list {
			out = append(out, a.Address)
		}
		return out
	}
	return model.DraftContent{IsDraft: true, To: bare(m.To), CC: bare(m.CC), BCC: bare(m.BCC), Subject: m.Subject, Body: *m.Body, HTML: *m.ContentType == model.HTML}, nil
}

// notDraft refuses a draft write of a message that is not a draft.
const notDraft = model.MailboxError("DraftError")

// DraftUpdate changes the given fields of any draft in one UPDATE; the draft keeps its ID.
func (b *mailbox) DraftUpdate(ctx context.Context, id string, change model.DraftChange) (string, string, error) {
	db, err := b.p.conn(ctx)
	if err != nil {
		return "", "", err
	}
	set, args := []string{}, []any{}
	for _, list := range []struct {
		column string
		value  *[]string
	}{{"to_addresses", change.To}, {"cc_addresses", change.CC}, {"bcc_addresses", change.BCC}} {
		if list.value != nil {
			set, args = append(set, list.column+" = ?"), append(args, jsonList(*list.value))
		}
	}
	if change.Subject != nil {
		set, args = append(set, "subject = ?"), append(args, *change.Subject)
	}
	if change.Body != nil {
		kind := model.Text
		if change.HTML {
			kind = model.HTML
		}
		set, args = append(set, "body = ?", "content_type = ?"), append(args, *change.Body, string(kind))
	}
	if len(set) == 0 || change.HTMLWithoutBody() {
		return "", "", invalid
	}
	var thread string
	err = db.QueryRowContext(ctx, `UPDATE messages SET `+strings.Join(set, ", ")+` WHERE account = ? AND id = ? AND is_draft = 1 RETURNING COALESCE(thread_id, id)`, append(args, b.p.account, id)...).Scan(&thread)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := b.Get(ctx, []string{id}, false); err != nil {
			return "", "", err
		}
		return "", "", notDraft
	}
	if err != nil {
		return "", "", storageError
	}
	return id, thread, nil
}

// DraftDelete removes the draft row, the one delete of the fake provider. A message that is not a draft stays.
func (b *mailbox) DraftDelete(ctx context.Context, id string) error {
	return b.draftWrite(ctx, id, `DELETE FROM messages WHERE account = ? AND id = ? AND is_draft = 1`)
}

// DraftSend moves the draft to sent as a sent message at the send time; the message keeps its ID. Nothing leaves the
// machine.
func (b *mailbox) DraftSend(ctx context.Context, id string) error {
	return b.draftWrite(ctx, id, `UPDATE messages SET folder = 'sent', is_draft = 0, received_at = ? WHERE account = ? AND id = ? AND is_draft = 1`, b.p.now().UTC().Format(time.RFC3339Nano))
}

// draftWrite runs statement, whose last arguments are the account and the message ID, on draft id only. A missing
// message is notFound, and a message that is not a draft is notDraft.
func (b *mailbox) draftWrite(ctx context.Context, id, statement string, args ...any) error {
	db, err := b.p.conn(ctx)
	if err != nil {
		return err
	}
	r, err := db.ExecContext(ctx, statement, append(args, b.p.account, id)...)
	if err != nil {
		return storageError
	}
	if n, err := r.RowsAffected(); err != nil || n == 0 {
		if _, err := b.Get(ctx, []string{id}, false); err != nil {
			return err
		}
		return notDraft
	}
	return nil
}

// Sends only store the message in sent. Nothing leaves the machine.
func (b *mailbox) SendNew(ctx context.Context, d model.NewDraft) error {
	_, err := b.insert(ctx, created(d), false)
	return err
}
func (b *mailbox) SendReply(ctx context.Context, id string, r model.Reply) error {
	o, err := b.reply(ctx, id, r)
	if err == nil {
		_, err = b.insert(ctx, o, false)
	}
	return err
}
func (b *mailbox) SendForward(ctx context.Context, id string, f model.Forward) error {
	o, err := b.forward(ctx, id, f)
	if err == nil {
		_, err = b.insert(ctx, o, false)
	}
	return err
}
