package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/taciturnaxolotl/herald/config"
)

type Config struct {
	ID            int64
	UserID        int64
	Filename      string
	Email         string
	CronExpr      string
	InlineContent bool
	RawText       string
	LastRun       sql.NullTime
	NextRun       sql.NullTime
	CreatedAt     time.Time
	LastActiveAt  sql.NullTime
}

// InactivityDays is how long a digest keeps sending without a keep-alive click.
const InactivityDays = 90

// Active reports whether the config is scheduled to send.
func (c *Config) Active() bool { return c.NextRun.Valid }

// ExpiresAt is when the digest pauses unless its keep-alive link is used.
func (c *Config) ExpiresAt() time.Time {
	base := c.CreatedAt
	if c.LastActiveAt.Valid && c.LastActiveAt.Time.After(base) {
		base = c.LastActiveAt.Time
	}
	return base.AddDate(0, 0, InactivityDays)
}

const configCols = `id, user_id, filename, email, cron_expr, inline_content, raw_text, last_run, next_run, created_at, last_active_at`

func scanConfig(s scanner) (*Config, error) {
	var c Config
	err := s.Scan(&c.ID, &c.UserID, &c.Filename, &c.Email, &c.CronExpr, &c.InlineContent, &c.RawText, &c.LastRun, &c.NextRun, &c.CreatedAt, &c.LastActiveAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (q *Queries) CreateConfig(ctx context.Context, userID int64, filename, email, cronExpr string, inline bool, rawText string, nextRun time.Time) (*Config, error) {
	result, err := q.q.ExecContext(ctx,
		`INSERT INTO configs (user_id, filename, email, cron_expr, inline_content, raw_text, next_run)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		userID, filename, email, cronExpr, inline, rawText, nextRun,
	)
	if err != nil {
		return nil, fmt.Errorf("insert config: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get last insert id: %w", err)
	}

	return &Config{
		ID:            id,
		UserID:        userID,
		Filename:      filename,
		Email:         email,
		CronExpr:      cronExpr,
		InlineContent: inline,
		RawText:       rawText,
		NextRun:       sql.NullTime{Time: nextRun, Valid: true},
		CreatedAt:     time.Now().UTC(),
	}, nil
}

// UpsertConfig creates or replaces the user's config named filename, reporting
// whether it is new. A nil nextRun stores it paused.
func (q *Queries) UpsertConfig(ctx context.Context, userID int64, filename, email, cronExpr string, inline bool, rawText string, nextRun *time.Time) (cfg *Config, created bool, err error) {
	var existing int
	if err := q.q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM configs WHERE user_id = ? AND filename = ?`, userID, filename,
	).Scan(&existing); err != nil {
		return nil, false, fmt.Errorf("check config: %w", err)
	}
	cfg, err = scanConfig(q.q.QueryRowContext(ctx,
		`INSERT INTO configs (user_id, filename, email, cron_expr, inline_content, raw_text, next_run)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(user_id, filename) DO UPDATE SET
		   email = excluded.email, cron_expr = excluded.cron_expr,
		   inline_content = excluded.inline_content, raw_text = excluded.raw_text, next_run = excluded.next_run
		 RETURNING `+configCols,
		userID, filename, email, cronExpr, inline, rawText, nextRun))
	if err != nil {
		return nil, false, fmt.Errorf("upsert config: %w", err)
	}
	return cfg, existing == 0, nil
}

func (q *Queries) GetConfig(ctx context.Context, userID int64, filename string) (*Config, error) {
	return scanConfig(q.q.QueryRowContext(ctx,
		`SELECT `+configCols+` FROM configs WHERE user_id = ? AND filename = ?`, userID, filename))
}

func (q *Queries) GetConfigByID(ctx context.Context, id int64) (*Config, error) {
	return scanConfig(q.q.QueryRowContext(ctx, `SELECT `+configCols+` FROM configs WHERE id = ?`, id))
}

func (q *Queries) ListConfigs(ctx context.Context, userID int64) ([]*Config, error) {
	configs, err := queryAll(ctx, q.q, scanConfig,
		`SELECT `+configCols+` FROM configs WHERE user_id = ? ORDER BY filename`, userID)
	if err != nil {
		return nil, fmt.Errorf("list configs: %w", err)
	}
	return configs, nil
}

func (q *Queries) GetDueConfigs(ctx context.Context, now time.Time) ([]*Config, error) {
	configs, err := queryAll(ctx, q.q, scanConfig,
		`SELECT `+configCols+` FROM configs WHERE next_run IS NOT NULL AND next_run <= ? ORDER BY next_run`, now)
	if err != nil {
		return nil, fmt.Errorf("query due configs: %w", err)
	}
	return configs, nil
}

func (q *Queries) DeleteConfig(ctx context.Context, userID int64, filename string) error {
	result, err := q.q.ExecContext(ctx,
		`DELETE FROM configs WHERE user_id = ? AND filename = ?`,
		userID, filename,
	)
	if err != nil {
		return fmt.Errorf("delete config: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// UpdateLastRun records a run; a paused config (no next run) stays paused.
func (q *Queries) UpdateLastRun(ctx context.Context, configID int64, lastRun, nextRun time.Time) error {
	_, err := q.q.ExecContext(ctx,
		`UPDATE configs SET last_run = ?, next_run = CASE WHEN next_run IS NULL THEN NULL ELSE ? END WHERE id = ?`,
		lastRun, nextRun, configID,
	)
	if err != nil {
		return fmt.Errorf("update last run: %w", err)
	}
	return nil
}

// SetNextRun schedules a config, or pauses it when nextRun is nil.
func (q *Queries) SetNextRun(ctx context.Context, configID int64, nextRun *time.Time) error {
	if _, err := q.q.ExecContext(ctx, `UPDATE configs SET next_run = ? WHERE id = ?`, nextRun, configID); err != nil {
		return fmt.Errorf("set next run: %w", err)
	}
	return nil
}

// ActivateConfig schedules cfg for its next cron tick.
func (q *Queries) ActivateConfig(ctx context.Context, cfg *Config) error {
	nextRun, err := config.NextRun(cfg.CronExpr, time.Now())
	if err != nil {
		return fmt.Errorf("calculate next run: %w", err)
	}
	return q.SetNextRun(ctx, cfg.ID, &nextRun)
}
