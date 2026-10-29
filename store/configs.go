package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/adhocore/gronx"
)

type Config struct {
	ID            int64
	UserID        int64
	Filename      string
	Email         string
	CronExpr      string
	Digest        bool
	InlineContent bool
	RawText       string
	LastRun       sql.NullTime
	NextRun       sql.NullTime
	CreatedAt     time.Time
	LastActiveAt  sql.NullTime
}

const configCols = `id, user_id, filename, email, cron_expr, digest, inline_content, raw_text, last_run, next_run, created_at, last_active_at`

func scanConfig(s scanner) (*Config, error) {
	var c Config
	err := s.Scan(&c.ID, &c.UserID, &c.Filename, &c.Email, &c.CronExpr, &c.Digest, &c.InlineContent, &c.RawText, &c.LastRun, &c.NextRun, &c.CreatedAt, &c.LastActiveAt)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (q *Queries) CreateConfig(ctx context.Context, userID int64, filename, email, cronExpr string, digest, inline bool, rawText string, nextRun time.Time) (*Config, error) {
	result, err := q.q.ExecContext(ctx,
		`INSERT INTO configs (user_id, filename, email, cron_expr, digest, inline_content, raw_text, next_run)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, filename, email, cronExpr, digest, inline, rawText, nextRun,
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
		Digest:        digest,
		InlineContent: inline,
		RawText:       rawText,
		NextRun:       sql.NullTime{Time: nextRun, Valid: true},
		CreatedAt:     time.Now().UTC(),
	}, nil
}

func (q *Queries) UpdateConfig(ctx context.Context, configID int64, email, cronExpr string, digest, inline bool, rawText string, nextRun time.Time) error {
	_, err := q.q.ExecContext(ctx,
		`UPDATE configs SET email = ?, cron_expr = ?, digest = ?, inline_content = ?, raw_text = ?, next_run = ? WHERE id = ?`,
		email, cronExpr, digest, inline, rawText, nextRun, configID,
	)
	if err != nil {
		return fmt.Errorf("update config: %w", err)
	}
	return nil
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
	nextRun, err := gronx.NextTickAfter(cfg.CronExpr, time.Now().UTC(), true)
	if err != nil {
		return fmt.Errorf("calculate next run: %w", err)
	}
	return q.SetNextRun(ctx, cfg.ID, &nextRun)
}
