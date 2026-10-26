// Package fake implements a local provider whose mailbox and calendar live in <home>/fake-provider.db. It makes no
// network calls and needs no login. It reads and writes the database on every call, so edits made with
// sqlite3 while the server runs are seen. Its mail and events are untrusted, as with real providers.
package fake

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
	_ "github.com/mattn/go-sqlite3"
)

//go:embed schema.sql
var schema string

// Error codes are fixed: they never carry message text or SQL and driver errors.
const (
	notFound     = model.MailboxError("NotFoundError")
	invalid      = model.MailboxError("ValueError")
	invalidData  = model.MailboxError("DataError")
	storageError = model.MailboxError("StorageError")
)

type Provider struct {
	account, path string
	now           func() time.Time
	mu            sync.Mutex
	db            *sql.DB
}

// New has no side effects. fake-provider.db is opened on the first call.
func New(account, home string, now func() time.Time) *Provider {
	if now == nil {
		now = time.Now
	}
	return &Provider{account: account, path: filepath.Join(home, "fake-provider.db"), now: now}
}

// conn opens fake-provider.db once, applies the schema, adds columns that older files lack, and adds the fixed
// folders of the account. A failed open is tried again on the next call.
func (p *Provider) conn(ctx context.Context) (*sql.DB, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.db != nil {
		return p.db, nil
	}
	db, err := open(ctx, p.path)
	if err != nil {
		return nil, storageError
	}
	if err := addColumn(ctx, db, "messages", "thread_id", "TEXT"); err != nil {
		_ = db.Close()
		return nil, storageError
	}
	if err := addColumn(ctx, db, "attendees", "optional", "INTEGER NOT NULL DEFAULT 0 CHECK (optional IN (0, 1))"); err != nil {
		_ = db.Close()
		return nil, storageError
	}
	_, err = db.ExecContext(ctx, `INSERT OR IGNORE INTO folders (account, id, name) VALUES (?1, 'inbox', 'Inbox'), (?1, 'drafts', 'Drafts'), (?1, 'sent', 'Sent'), (?1, 'archive', 'Archive'), (?1, 'trash', 'Trash')`, p.account)
	if err != nil {
		_ = db.Close()
		return nil, storageError
	}
	p.db = db
	return db, nil
}

func open(ctx context.Context, path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	err = errors.Join(f.Chmod(0600), f.Close())
	if err != nil {
		return nil, err
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: absolute}
	db, err := sql.Open("sqlite3", uri.String()+"?_busy_timeout=5000&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, schema); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// addColumn adds a column of schema.sql to a file made before the column existed. table, column, and definition are
// constants. The check and the change run in one write transaction, so another account's provider on the same file
// cannot add it twice.
func addColumn(ctx context.Context, db *sql.DB, table, column, definition string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var found int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&found); err != nil {
		return err
	}
	if found == 0 {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+column+` `+definition); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// identity is the account's own address from the identities table, or <account>@fake.test.
func (p *Provider) identity(ctx context.Context, db *sql.DB) (model.Address, error) {
	a := model.Address{}
	err := db.QueryRowContext(ctx, `SELECT name, address FROM identities WHERE account = ?`, p.account).Scan(&a.Name, &a.Address)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Address{Address: p.account + "@fake.test"}, nil
	}
	if err != nil {
		return a, storageError
	}
	return a, nil
}

// Status returns the account's own address. Fake accounts have no scopes.
func (p *Provider) Status(ctx context.Context) (string, []string, error) {
	db, err := p.conn(ctx)
	if err != nil {
		return "", nil, err
	}
	a, err := p.identity(ctx, db)
	return a.Address, []string{}, err
}

func (p *Provider) Mailbox() model.Mailbox { return &mailbox{p} }

// Calendar also writes meetings and reads availability, only in fake-provider.db. No notification leaves the machine.
func (p *Provider) Calendar() model.Calendar { return &calendar{p} }
