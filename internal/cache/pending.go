package cache

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
)

// PendingState is the life cycle of a stored action:
// pending -> running -> done|failed, or pending -> denied.
type PendingState string

const (
	StatePending PendingState = "pending"
	StateRunning PendingState = "running"
	StateDone    PendingState = "done"
	StateDenied  PendingState = "denied"
	StateFailed  PendingState = "failed"
)

// Pending is a stored write action. Args and Draft can contain message content.
type Pending struct {
	ID, Account, Op  string
	Args             json.RawMessage
	Draft            json.RawMessage // a draft send: the draft fields that the Jev action check saw; nil for other actions
	State            PendingState
	Result           *string            // provider IDs on done, safe error code on failed
	Jev              *model.ActionCheck // nil when no Jev action check ran
	Created, Updated time.Time
}

var (
	ErrPendingNotFound = errors.New("pending action not found")
	// ErrPendingHandled means the action exists but is not in the state the change needs.
	ErrPendingHandled = errors.New("pending action already handled")
)

const pendingColumns = `id, account, op, args, draft, jev, state, result, created, updated`

// AddPending stores an action in state pending and returns its new random ID. draft is the draft fields of a draft
// send, nil for other actions. jev is the result of the Jev action check, nil when no check ran.
func (c *Cache) AddPending(ctx context.Context, account, op string, args, draft json.RawMessage, jev *model.ActionCheck, now time.Time) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !json.Valid(args) {
		return "", errors.New("pending action arguments are not valid JSON")
	}
	var fields *string
	if draft != nil {
		if !json.Valid(draft) {
			return "", errors.New("pending draft fields are not valid JSON")
		}
		fields = new(string(draft))
	}
	var check *string
	if jev != nil {
		data, err := json.Marshal(jev)
		if err != nil {
			return "", errors.New("invalid Jev check")
		}
		text := string(data)
		check = &text
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", errors.New("cannot create pending action ID")
	}
	id := hex.EncodeToString(random)
	at := iso(now)
	if _, err := c.db.ExecContext(ctx, `INSERT INTO pending (id, account, op, args, draft, jev, state, created, updated) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, account, op, string(args), fields, check, StatePending, at, at); err != nil {
		return "", errors.New("pending action write failed")
	}
	return id, nil
}

// GetPending returns ErrPendingNotFound when no action has the ID.
func (c *Cache) GetPending(ctx context.Context, id string) (Pending, error) {
	if err := ctx.Err(); err != nil {
		return Pending{}, err
	}
	return scanPending(c.db.QueryRowContext(ctx, "SELECT "+pendingColumns+" FROM pending WHERE id = ?", id))
}

// maxListedPending caps the waiting actions that ListPending returns.
const maxListedPending = 500

// ListPending returns the waiting actions newest first, at most maxListedPending, then the resolvedLimit most
// recently updated actions in any other state.
func (c *Cache) ListPending(ctx context.Context, resolvedLimit int) ([]Pending, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := []Pending{}
	for _, query := range []struct {
		where string
		limit int
	}{
		{"state = 'pending' ORDER BY created DESC, rowid DESC", maxListedPending},
		{"state <> 'pending' ORDER BY updated DESC, rowid DESC", resolvedLimit},
	} {
		rows, err := c.db.QueryContext(ctx, "SELECT "+pendingColumns+" FROM pending WHERE "+query.where+" LIMIT ?", query.limit)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errors.New("pending action read failed")
		}
		for rows.Next() {
			p, err := scanPending(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, p)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, errors.New("pending action read failed")
		}
	}
	return out, nil
}

func scanPending(row interface{ Scan(...any) error }) (Pending, error) {
	var p Pending
	var args, created, updated string
	var draft, jev *string
	err := row.Scan(&p.ID, &p.Account, &p.Op, &args, &draft, &jev, &p.State, &p.Result, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Pending{}, ErrPendingNotFound
	}
	if err != nil {
		return Pending{}, errors.New("pending action read failed")
	}
	p.Args = json.RawMessage(args)
	if draft != nil {
		p.Draft = json.RawMessage(*draft)
	}
	if jev != nil {
		p.Jev = &model.ActionCheck{}
		if json.Unmarshal([]byte(*jev), p.Jev) != nil {
			return Pending{}, errors.New("invalid pending action Jev check")
		}
	}
	if p.Created, err = time.Parse(time.RFC3339, created); err != nil {
		return Pending{}, errors.New("invalid pending action date")
	}
	if p.Updated, err = time.Parse(time.RFC3339, updated); err != nil {
		return Pending{}, errors.New("invalid pending action date")
	}
	p.Created, p.Updated = p.Created.UTC(), p.Updated.UTC()
	return p, nil
}

// ClaimPending atomically moves pending -> running and returns the claimed action.
// Exactly one of many concurrent claims wins; the others get ErrPendingHandled.
func (c *Cache) ClaimPending(ctx context.Context, id string, now time.Time) (Pending, error) {
	return c.transition(ctx, id, StatePending, StateRunning, nil, now)
}

// transition changes state in one conditional UPDATE, so the check and the change are atomic.
// A missing ID gives ErrPendingNotFound; an ID in another state gives ErrPendingHandled.
func (c *Cache) transition(ctx context.Context, id string, from, to PendingState, result *string, now time.Time) (Pending, error) {
	if err := ctx.Err(); err != nil {
		return Pending{}, err
	}
	p, err := scanPending(c.db.QueryRowContext(ctx, `UPDATE pending SET state = ?, result = ?, updated = ? WHERE id = ? AND state = ? RETURNING `+pendingColumns,
		to, result, iso(now), id, from))
	if !errors.Is(err, ErrPendingNotFound) {
		return p, err
	}
	if _, err := c.GetPending(ctx, id); err != nil {
		return Pending{}, err
	}
	return Pending{}, ErrPendingHandled
}

// DenyPending atomically moves pending -> denied and returns the denied action.
func (c *Cache) DenyPending(ctx context.Context, id string, now time.Time) (Pending, error) {
	return c.transition(ctx, id, StatePending, StateDenied, nil, now)
}

// FinishPending atomically moves running -> done or failed and stores the result:
// provider IDs on done, the safe error code on failed.
func (c *Cache) FinishPending(ctx context.Context, id string, state PendingState, result string, now time.Time) error {
	if state != StateDone && state != StateFailed {
		return errors.New("pending action can finish only as done or failed")
	}
	if state == StateFailed && model.ValidateFailureText(&result) != nil {
		return errors.New("invalid pending action error code")
	}
	_, err := c.transition(ctx, id, StateRunning, state, &result, now)
	return err
}

// AccountRemoved is the result code of the pending actions that DenyAccount denies.
const AccountRemoved = "account_removed"

// DenyAccount denies every waiting action of the account with the result code AccountRemoved, and returns them.
// Actions that already run finish as they are.
func (c *Cache) DenyAccount(ctx context.Context, account string, now time.Time) ([]Pending, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rows, err := c.db.QueryContext(ctx, `UPDATE pending SET state = ?, result = ?, updated = ? WHERE account = ? AND state = ? RETURNING `+pendingColumns,
		StateDenied, AccountRemoved, iso(now), account, StatePending)
	if err != nil {
		return nil, errors.New("pending action write failed")
	}
	defer rows.Close()
	out := []Pending{}
	for rows.Next() {
		p, err := scanPending(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if rows.Err() != nil {
		return nil, errors.New("pending action write failed")
	}
	return out, nil
}
