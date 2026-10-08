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

// Pending is a stored write action. Args can contain message content.
type Pending struct {
	ID, Account, Op  string
	Args             json.RawMessage
	State            PendingState
	Result           *string // provider IDs on done, safe error code on failed
	Created, Updated time.Time
}

var (
	ErrPendingNotFound = errors.New("pending action not found")
	// ErrPendingHandled means the action exists but is not in the state the change needs.
	ErrPendingHandled = errors.New("pending action already handled")
)

const pendingColumns = `id, account, op, args, state, result, created, updated`

// AddPending stores an action in state pending and returns its new random ID.
func (c *Cache) AddPending(ctx context.Context, account, op string, args json.RawMessage, now time.Time) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !json.Valid(args) {
		return "", errors.New("pending action arguments are not valid JSON")
	}
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", errors.New("cannot create pending action ID")
	}
	id := hex.EncodeToString(random)
	at := iso(now)
	if _, err := c.db.ExecContext(ctx, `INSERT INTO pending (id, account, op, args, state, created, updated) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, account, op, string(args), StatePending, at, at); err != nil {
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

func scanPending(row *sql.Row) (Pending, error) {
	var p Pending
	var args, created, updated string
	err := row.Scan(&p.ID, &p.Account, &p.Op, &args, &p.State, &p.Result, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return Pending{}, ErrPendingNotFound
	}
	if err != nil {
		return Pending{}, errors.New("pending action read failed")
	}
	p.Args = json.RawMessage(args)
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
