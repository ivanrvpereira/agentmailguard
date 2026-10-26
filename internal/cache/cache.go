// Package cache persists security metadata and explicit organization answers, never mail content,
// in mail_cache. The pending table stores write actions that wait for user approval; their request
// arguments can contain message content (recipients, subject, body).
package cache

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ivanrvpereira/agentmailguard/internal/model"
	"github.com/mattn/go-sqlite3"
)

//go:embed migrations/*.sql
var migrations embed.FS

const columns = `message_id, received_at, score, verdict, error, spf, dkim, dmarc, compauth, compauth_reason, screened_at, organization, organization_applied_at, security_applied_verdict`
const timestamp = "2006-01-02T15:04:05+00:00"

type Row struct {
	MessageID              string
	ReceivedAt             time.Time
	Score                  *float64
	Verdict                model.Verdict
	Error                  *string // safe error type/status only
	Auth                   model.AuthResults
	ScreenedAt             time.Time
	Organization           *model.Organization // nil: classification needed; empty answer is completed
	OrganizationAppliedAt  *time.Time
	SecurityAppliedVerdict *model.Verdict
}
type Cache struct{ db *sql.DB }

// Open uses only the explicit path; it never imports Python state.
func Open(ctx context.Context, path string) (*Cache, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	folder := filepath.Dir(path)
	if err := os.MkdirAll(folder, 0700); err != nil {
		return nil, errors.New("cannot create cache directory")
	}
	if err := os.Chmod(folder, 0700); err != nil {
		return nil, errors.New("cannot secure cache directory")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, errors.New("cannot open cache file")
	}
	err = f.Chmod(0600)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return nil, errors.New("cannot secure cache file")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, errors.New("invalid cache path")
	}
	uri := url.URL{Scheme: "file", Path: absolute}
	db, err := sql.Open("sqlite3", uri.String()+"?_busy_timeout=5000&_txlock=immediate")
	if err != nil {
		return nil, errors.New("cannot open cache database")
	}
	db.SetMaxOpenConns(1)
	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Cache{db: db}, nil
}
func (c *Cache) Close() error { return c.db.Close() }

func migrate(ctx context.Context, db *sql.DB) error {
	scripts, err := migrations.ReadDir("migrations")
	if err != nil || len(scripts) == 0 {
		return errors.New("invalid cache migration scripts")
	}
	for i, script := range scripts {
		if !strings.HasPrefix(script.Name(), fmt.Sprintf("%03d_", i+1)) || !strings.HasSuffix(script.Name(), ".sql") {
			return errors.New("invalid cache migration numbering")
		}
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return errors.New("cannot connect to cache")
	}
	defer conn.Close()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return errors.New("cannot begin cache migration")
	}
	// Disable the authorizer before rollback/commit: scripts cannot split our transaction.
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return errors.New("cannot read cache schema version")
	}
	if version < 0 {
		return errors.New("incompatible cache schema version")
	}
	if version > len(scripts) {
		return errors.New("cache schema version is newer than this program")
	}
	authorize := func(enabled bool) error {
		return conn.Raw(func(raw any) error {
			connection := raw.(*sqlite3.SQLiteConn)
			if !enabled {
				connection.RegisterAuthorizer(nil)
				return nil
			}
			connection.RegisterAuthorizer(func(action int, _, _, _ string) int {
				if action == sqlite3.SQLITE_TRANSACTION || action == sqlite3.SQLITE_SAVEPOINT {
					return sqlite3.SQLITE_DENY
				}
				return sqlite3.SQLITE_OK
			})
			return nil
		})
	}
	if err := authorize(true); err != nil {
		return errors.New("cannot protect cache migration")
	}
	defer authorize(false)
	for i := version; i < len(scripts); i++ {
		script, err := migrations.ReadFile("migrations/" + scripts[i].Name())
		if err != nil {
			return errors.New("cannot read cache migration")
		}
		if _, err = tx.ExecContext(ctx, string(script)); err != nil {
			return fmt.Errorf("cache migration %d failed", i+1)
		}
		if _, err = tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			return errors.New("cannot update cache version")
		}
	}
	for _, query := range []string{
		"SELECT account, " + columns + " FROM mail_cache LIMIT 0",
		"EXPLAIN INSERT INTO mail_cache (account, message_id) VALUES (NULL, NULL) ON CONFLICT (account, message_id) DO NOTHING",
		"SELECT " + pendingColumns + " FROM pending LIMIT 0",
	} {
		rows, err := tx.QueryContext(ctx, query)
		if err != nil {
			return errors.New("incompatible cache schema")
		}
		if err := rows.Close(); err != nil {
			return errors.New("incompatible cache schema")
		}
	}
	if err := authorize(false); err != nil {
		return errors.New("cannot finish cache migration")
	}
	if err := tx.Commit(); err != nil {
		return errors.New("cannot commit cache migration")
	}
	return nil
}

type AccountStats struct {
	Total                   int
	Counts                  map[model.Verdict]int
	Shares                  map[model.Verdict]float64
	GreenShareNonSealed     *float64
	DMARCFail, CompAuthFail int
}

// Stats uses an explicit clock value so windows are deterministic for callers and tests.
func (c *Cache) Stats(ctx context.Context, days int, now time.Time) (map[string]AccountStats, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	since := iso(now.UTC().AddDate(0, 0, -days))
	rows, err := c.db.QueryContext(ctx, `SELECT account, SUM(verdict='green'), SUM(verdict='yellow'), SUM(verdict='sealed'), SUM(verdict='red'), SUM(verdict='error'), SUM(dmarc IS 'fail'), SUM(compauth IS 'fail') FROM mail_cache WHERE received_at >= ? GROUP BY account`, since)
	if err != nil {
		return nil, errors.New("cache stats failed")
	}
	defer rows.Close()
	result := map[string]AccountStats{}
	round := func(v float64) float64 { return math.RoundToEven(v*1000) / 1000 }
	for rows.Next() {
		var account string
		var green, yellow, sealed, red, failed int
		var stats AccountStats
		if err := rows.Scan(&account, &green, &yellow, &sealed, &red, &failed, &stats.DMARCFail, &stats.CompAuthFail); err != nil {
			return nil, errors.New("cache stats failed")
		}
		stats.Counts = map[model.Verdict]int{model.Green: green, model.Yellow: yellow, model.Sealed: sealed, model.Red: red, model.Error: failed}
		stats.Total = green + yellow + sealed + red + failed
		stats.Shares = map[model.Verdict]float64{}
		for verdict, count := range stats.Counts {
			stats.Shares[verdict] = round(float64(count) / float64(stats.Total))
		}
		if other := stats.Total - sealed; other != 0 {
			share := round(float64(green) / float64(other))
			stats.GreenShareNonSealed = &share
		}
		result[account] = stats
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("cache stats failed")
	}
	return result, nil
}
func (c *Cache) Clear(ctx context.Context) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	result, err := c.db.ExecContext(ctx, "DELETE FROM mail_cache")
	if err != nil {
		return 0, errors.New("cache clear failed")
	}
	return result.RowsAffected()
}

// DeleteAccount removes the cached verdicts and answers of an account. Its pending actions stay.
func (c *Cache) DeleteAccount(ctx context.Context, account string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	result, err := c.db.ExecContext(ctx, "DELETE FROM mail_cache WHERE account = ?", account)
	if err != nil {
		return 0, errors.New("cache clear failed")
	}
	return result.RowsAffected()
}

// SaveOrganization accepts only explicit, validated answer fields, never a generic map.
func (c *Cache) SaveOrganization(ctx context.Context, account, messageID string, organization model.Organization) error {
	if err := organization.Validate(); err != nil {
		return err
	}
	text, err := json.Marshal(organization)
	if err != nil {
		return errors.New("invalid organization answers")
	}
	return c.update(ctx, account, messageID, "organization", string(text))
}
func (c *Cache) MarkOrganizationApplied(ctx context.Context, account, messageID string, at time.Time) error {
	return c.update(ctx, account, messageID, "organization_applied_at", iso(at))
}
func (c *Cache) MarkSecurityApplied(ctx context.Context, account, messageID string, verdict model.Verdict) error {
	if verdict != model.Yellow && verdict != model.Sealed && verdict != model.Red {
		return errors.New("only yellow, sealed, and red have a security tag")
	}
	return c.update(ctx, account, messageID, "security_applied_verdict", string(verdict))
}
func (c *Cache) update(ctx context.Context, account, messageID, column, value string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	result, err := c.db.ExecContext(ctx, "UPDATE mail_cache SET "+column+" = ? WHERE account = ? AND message_id = ?", value, account, messageID)
	if err != nil {
		return errors.New("cache update failed")
	}
	count, err := result.RowsAffected()
	if err != nil {
		return errors.New("cache update failed")
	}
	if count == 0 {
		return errors.New("no cached verdict to update")
	}
	return nil
}

func iso(at time.Time) string { return at.UTC().Format(timestamp) }
func (c *Cache) Get(ctx context.Context, account string, ids []string) (map[string]Row, error) {
	result := map[string]Row{}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return result, nil
	}
	args := []any{account}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := c.db.QueryContext(ctx, "SELECT "+columns+" FROM mail_cache WHERE account = ? AND message_id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+")", args...)
	if err != nil {
		return nil, errors.New("cache read failed")
	}
	defer rows.Close()
	for rows.Next() {
		var r Row
		var received, screened string
		var organization, applied *string
		if err := rows.Scan(&r.MessageID, &received, &r.Score, &r.Verdict, &r.Error, &r.Auth.SPF, &r.Auth.DKIM, &r.Auth.DMARC, &r.Auth.CompAuth, &r.Auth.CompAuthReason, &screened, &organization, &applied, &r.SecurityAppliedVerdict); err != nil {
			return nil, errors.New("invalid cache row")
		}
		r.ReceivedAt, err = time.Parse(time.RFC3339, received)
		if err != nil {
			return nil, errors.New("invalid cache date")
		}
		r.ReceivedAt = r.ReceivedAt.UTC()
		r.ScreenedAt, err = time.Parse(time.RFC3339, screened)
		if err != nil {
			return nil, errors.New("invalid cache date")
		}
		r.ScreenedAt = r.ScreenedAt.UTC()
		if applied != nil {
			at, err := time.Parse(time.RFC3339, *applied)
			if err != nil {
				return nil, errors.New("invalid cache date")
			}
			at = at.UTC()
			r.OrganizationAppliedAt = &at
		}
		if organization != nil {
			var answer model.Organization
			decoder := json.NewDecoder(strings.NewReader(*organization))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&answer); err != nil {
				return nil, errors.New("invalid cached organization")
			}
			if decoder.Decode(new(any)) != io.EOF || answer.Validate() != nil {
				return nil, errors.New("invalid cached organization")
			}
			r.Organization = &answer
		}
		result[r.MessageID] = r
	}
	if err := rows.Err(); err != nil {
		return nil, errors.New("cache read failed")
	}
	return result, nil
}

// Put writes security columns only, preserving both independent application markers and answers.
func (c *Cache) Put(ctx context.Context, account string, r Row) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if r.Score != nil && !(*r.Score >= 0 && *r.Score <= 1) {
		return errors.New("invalid cache score")
	}
	if err := model.ValidateFailureText(r.Error); err != nil {
		return err
	}
	if err := r.Auth.Validate(); err != nil {
		return err
	}
	_, err := c.db.ExecContext(ctx, `INSERT INTO mail_cache (account, message_id, received_at, score, verdict, error, spf, dkim, dmarc, compauth, compauth_reason, screened_at)
 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
 ON CONFLICT (account, message_id) DO UPDATE SET
 received_at=excluded.received_at, score=excluded.score, verdict=excluded.verdict, error=excluded.error,
 spf=excluded.spf, dkim=excluded.dkim, dmarc=excluded.dmarc, compauth=excluded.compauth, compauth_reason=excluded.compauth_reason, screened_at=excluded.screened_at`,
		account, r.MessageID, iso(r.ReceivedAt), r.Score, r.Verdict, r.Error, r.Auth.SPF, r.Auth.DKIM, r.Auth.DMARC, r.Auth.CompAuth, r.Auth.CompAuthReason, iso(r.ScreenedAt))
	if err != nil {
		return errors.New("cache write failed")
	}
	return nil
}
