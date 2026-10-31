package fake

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

type mailbox struct{ p *Provider }

const columns = `id, COALESCE(thread_id, id), folder, sender_name, sender_address, to_addresses, cc_addresses, bcc_addresses, subject, body, content_type, received_at, is_draft, is_read, flag, categories, attachments, spf, dkim, dmarc, compauth, compauth_reason`

// newest orders by the instant, not the text, because seeded times can have any offset.
const newest = ` ORDER BY julianday(received_at) DESC, id`

func list(text string) ([]string, error) {
	out := []string{}
	if json.Unmarshal([]byte(text), &out) != nil {
		return nil, invalidData
	}
	if out == nil {
		return []string{}, nil
	}
	return out, nil
}
func addresses(text string) ([]model.Address, error) {
	values, err := list(text)
	out := []model.Address{}
	for _, v := range values {
		out = append(out, model.Address{Address: v})
	}
	return out, err
}

// scan reads one row of columns. Body, content type, and authentication results are set only when body is true.
func scan(row interface{ Scan(...any) error }, body bool) (model.Message, error) {
	var m model.Message
	var folder, to, cc, bcc, text, kind, received, flag, categories, attachments string
	var auth model.AuthResults
	err := row.Scan(&m.ID, &m.ThreadID, &folder, &m.Sender.Name, &m.Sender.Address, &to, &cc, &bcc, &m.Subject, &text, &kind, &received, &m.IsDraft, &m.IsRead, &flag, &categories, &attachments, &auth.SPF, &auth.DKIM, &auth.DMARC, &auth.CompAuth, &auth.CompAuthReason)
	if errors.Is(err, sql.ErrNoRows) {
		return m, notFound
	}
	if err != nil {
		return m, storageError
	}
	m.Folders, m.Flag = []string{folder}, model.FlagState(flag)
	if m.ReceivedAt, err = time.Parse(time.RFC3339Nano, received); err != nil {
		return m, invalidData
	}
	var errs [5]error
	m.To, errs[0] = addresses(to)
	m.CC, errs[1] = addresses(cc)
	m.BCC, errs[2] = addresses(bcc)
	m.Categories, errs[3] = list(categories)
	m.Attachments, errs[4] = list(attachments)
	if err := errors.Join(errs[:]...); err != nil {
		return m, invalidData
	}
	if body {
		content := model.ContentType(kind)
		m.Body, m.ContentType, m.Auth = &text, &content, &auth
	}
	return m, nil
}

func (b *mailbox) query(ctx context.Context, body bool, where string, args ...any) ([]model.Message, error) {
	db, err := b.p.conn(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT `+columns+` FROM messages WHERE account = ? `+where, append([]any{b.p.account}, args...)...)
	if err != nil {
		return nil, storageError
	}
	defer rows.Close()
	out := []model.Message{}
	for rows.Next() {
		m, err := scan(rows, body)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if rows.Err() != nil {
		return nil, storageError
	}
	return out, nil
}

func (b *mailbox) folderExists(ctx context.Context, folder string) error {
	db, err := b.p.conn(ctx)
	if err != nil {
		return err
	}
	var found int
	err = db.QueryRowContext(ctx, `SELECT 1 FROM folders WHERE account = ? AND id = ?`, b.p.account, folder).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound
	}
	if err != nil {
		return storageError
	}
	return nil
}

// pageSize and maxPageSize are Graph's $top default and maximum, which the fake follows.
const pageSize, maxPageSize = 10, 1000

// window reads a page request as the offset that its marker names and the page size. A marker is the decimal
// offset that cut made; anything else is invalid.
func window(p model.PageRequest) (offset, size int, err error) {
	size = p.Size
	if size == 0 {
		size = pageSize
	}
	if size < 0 || size > maxPageSize {
		return 0, 0, invalid
	}
	if p.Page == "" {
		return 0, size, nil
	}
	offset, e := strconv.Atoi(p.Page)
	if e != nil || offset < 0 || strconv.Itoa(offset) != p.Page {
		return 0, 0, invalid
	}
	return offset, size, nil
}

// cut returns the page of all at offset and the marker of the next page, "" on the last page.
func cut[T any](all []T, offset, size int) ([]T, string) {
	if offset >= len(all) {
		return []T{}, ""
	}
	end := offset + min(size, len(all)-offset)
	if end == len(all) {
		return all[offset:], ""
	}
	return all[offset:end], strconv.Itoa(end)
}

func (b *mailbox) List(ctx context.Context, folder string, page model.PageRequest, body bool) (model.MessagePage, error) {
	offset, size, err := window(page)
	if err != nil {
		return model.MessagePage{}, err
	}
	if err := b.folderExists(ctx, folder); err != nil {
		return model.MessagePage{}, err
	}
	all, err := b.query(ctx, body, `AND folder = ?`+newest, folder)
	if err != nil {
		return model.MessagePage{}, err
	}
	out, next := cut(all, offset, size)
	return model.MessagePage{Messages: out, Next: next}, nil
}

// Thread reads the conversation from every folder by the effective thread ID: a NULL thread_id is the message ID.
func (b *mailbox) Thread(ctx context.Context, thread string) ([]model.Message, error) {
	out, err := b.query(ctx, false, `AND COALESCE(thread_id, id) = ?`+newest, thread)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, notFound
	}
	return out, nil
}

// ThreadMessagesIn keeps the messages of Thread whose folder column is folder. An unknown folder is notFound, as in
// List. trash is the folder's Trash mark from Folders.
func (b *mailbox) ThreadMessagesIn(ctx context.Context, thread, folder string) ([]string, bool, error) {
	if err := b.folderExists(ctx, folder); err != nil {
		return nil, false, err
	}
	folders, err := b.Folders(ctx)
	if err != nil {
		return nil, false, err
	}
	trash := slices.ContainsFunc(folders, func(f model.Folder) bool { return f.ID == folder && f.Trash })
	all, err := b.Thread(ctx, thread)
	if err != nil {
		return nil, false, err
	}
	out := []string{}
	for _, m := range all {
		if slices.Contains(m.Folders, folder) {
			out = append(out, m.ID)
		}
	}
	return out, trash, nil
}

func contains(value string, part *string) bool {
	return part == nil || strings.Contains(strings.ToLower(value), strings.ToLower(*part))
}

// Search matches case-insensitive substrings; To matches any one To or Cc address. After and Before are exact
// instants, matched with model.SearchQuery.Received.
func (b *mailbox) Search(ctx context.Context, q model.SearchQuery, page model.PageRequest, body bool) (model.MessagePage, error) {
	offset, size, err := window(page)
	if err != nil {
		return model.MessagePage{}, err
	}
	where, args := newest, []any{}
	if q.Folder != nil {
		if err := b.folderExists(ctx, *q.Folder); err != nil {
			return model.MessagePage{}, err
		}
		where, args = `AND folder = ?`+newest, []any{*q.Folder}
	}
	all, err := b.query(ctx, true, where, args...)
	if err != nil {
		return model.MessagePage{}, err
	}
	found := []model.Message{}
	for _, m := range all {
		sender := m.Sender.Name + "\n" + m.Sender.Address
		recipient := q.To == nil || *q.To == "" || slices.ContainsFunc(slices.Concat(m.To, m.CC), func(a model.Address) bool { return contains(a.Address, q.To) })
		if !contains(m.Subject+"\n"+*m.Body+"\n"+sender, q.Text) || !contains(sender, q.Sender) || !recipient || !contains(m.Subject, q.Subject) ||
			!q.Received(m.ReceivedAt) {
			continue
		}
		if !body {
			m.Body, m.ContentType, m.Auth = nil, nil, nil
		}
		found = append(found, m)
	}
	out, next := cut(found, offset, size)
	return model.MessagePage{Messages: out, Next: next}, nil
}

// ScanInbox reads metadata of non-draft inbox mail with since <= received_at < until. A row with an
// unreadable time is a failure, as in Microsoft 365.
func (b *mailbox) ScanInbox(ctx context.Context, since time.Time, until *time.Time) (model.InboxScan, error) {
	out := model.InboxScan{Messages: []model.InboxEntry{}, Failures: []model.ScanFailure{}}
	if since.IsZero() || until != nil && !until.After(since) {
		return out, invalid
	}
	fail := func(id *string, code model.MailboxError) {
		out.Failures = append(out.Failures, model.ScanFailure{ID: id, Error: string(code)})
	}
	db, err := b.p.conn(ctx)
	if err != nil {
		fail(nil, storageError)
		return out, nil
	}
	rows, err := db.QueryContext(ctx, `SELECT id, received_at FROM messages WHERE account = ? AND folder = 'inbox' AND is_draft = 0 ORDER BY julianday(received_at), id`, b.p.account)
	if err != nil {
		fail(nil, storageError)
		return out, nil
	}
	defer rows.Close()
	for rows.Next() {
		var id, received string
		if rows.Scan(&id, &received) != nil {
			fail(nil, storageError)
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, received)
		if err != nil {
			fail(&id, invalidData)
			continue
		}
		if !at.Before(since) && (until == nil || at.Before(*until)) {
			out.Messages = append(out.Messages, model.InboxEntry{ID: id, ReceivedAt: at})
		}
	}
	if rows.Err() != nil {
		fail(nil, storageError)
	}
	return out, nil
}

// Get fails the whole call when any ID is missing, like Microsoft 365.
func (b *mailbox) Get(ctx context.Context, ids []string, body bool) ([]model.Message, error) {
	db, err := b.p.conn(ctx)
	if err != nil {
		return nil, err
	}
	out := []model.Message{}
	for _, id := range ids {
		m, err := scan(db.QueryRowContext(ctx, `SELECT `+columns+` FROM messages WHERE account = ? AND id = ?`, b.p.account, id), body)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}
