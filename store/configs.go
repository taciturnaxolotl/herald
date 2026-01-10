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

func (db *DB) CreateConfig(ctx context.Context, userID int64, filename, email, cronExpr string, digest, inline bool, rawText string, nextRun time.Time) (*Config, error) {
	result, err := db.ExecContext(ctx,
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
		CreatedAt:     time.Now(),
	}, nil
}

func (db *DB) CreateConfigTx(ctx context.Context, tx *sql.Tx, userID int64, filename, email, cronExpr string, digest, inline bool, rawText string, nextRun time.Time) (*Config, error) {
	result, err := tx.ExecContext(ctx,
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
		CreatedAt:     time.Now(),
	}, nil
}

func (db *DB) DeleteConfigTx(ctx context.Context, tx *sql.Tx, userID int64, filename string) error {
	result, err := tx.ExecContext(ctx,
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

func (db *DB) GetConfig(ctx context.Context, userID int64, filename string) (*Config, error) {
	var cfg Config
	err := db.stmts.getConfig.QueryRowContext(ctx, userID, filename).Scan(&cfg.ID, &cfg.UserID, &cfg.Filename, &cfg.Email, &cfg.CronExpr, &cfg.Digest, &cfg.InlineContent, &cfg.RawText, &cfg.LastRun, &cfg.NextRun, &cfg.CreatedAt, &cfg.LastActiveAt)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (db *DB) GetConfigByID(ctx context.Context, id int64) (*Config, error) {
	var cfg Config
	err := db.QueryRowContext(ctx,
		`SELECT id, user_id, filename, email, cron_expr, digest, inline_content, raw_text, last_run, next_run, created_at, last_active_at
		 FROM configs WHERE id = ?`,
		id,
	).Scan(&cfg.ID, &cfg.UserID, &cfg.Filename, &cfg.Email, &cfg.CronExpr, &cfg.Digest, &cfg.InlineContent, &cfg.RawText, &cfg.LastRun, &cfg.NextRun, &cfg.CreatedAt, &cfg.LastActiveAt)
	if err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (db *DB) ListConfigs(ctx context.Context, userID int64) ([]*Config, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, user_id, filename, email, cron_expr, digest, inline_content, raw_text, last_run, next_run, created_at, last_active_at
		 FROM configs WHERE user_id = ? ORDER BY filename`,
		userID,
	)
	if err != nil {
		return nil, fmt.Errorf("query configs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var configs []*Config
	for rows.Next() {
		var cfg Config
		if err := rows.Scan(&cfg.ID, &cfg.UserID, &cfg.Filename, &cfg.Email, &cfg.CronExpr, &cfg.Digest, &cfg.InlineContent, &cfg.RawText, &cfg.LastRun, &cfg.NextRun, &cfg.CreatedAt, &cfg.LastActiveAt); err != nil {
			return nil, fmt.Errorf("scan config: %w", err)
		}
		configs = append(configs, &cfg)
	}
	return configs, rows.Err()
}

func (db *DB) DeleteConfig(ctx context.Context, userID int64, filename string) error {
	result, err := db.ExecContext(ctx,
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

func (db *DB) UpdateLastRun(ctx context.Context, configID int64, lastRun, nextRun time.Time) error {
	_, err := db.stmts.updateConfigRun.ExecContext(ctx, lastRun, nextRun, configID)
	if err != nil {
		return fmt.Errorf("update last run: %w", err)
	}
	return nil
}

func (db *DB) GetDueConfigs(ctx context.Context, now time.Time) ([]*Config, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, user_id, filename, email, cron_expr, digest, inline_content, raw_text, last_run, next_run, created_at, last_active_at
		 FROM configs WHERE next_run IS NOT NULL AND next_run <= ? ORDER BY next_run`,
		now,
	)
	if err != nil {
		return nil, fmt.Errorf("query due configs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var configs []*Config
	for rows.Next() {
		var cfg Config
		if err := rows.Scan(&cfg.ID, &cfg.UserID, &cfg.Filename, &cfg.Email, &cfg.CronExpr, &cfg.Digest, &cfg.InlineContent, &cfg.RawText, &cfg.LastRun, &cfg.NextRun, &cfg.CreatedAt, &cfg.LastActiveAt); err != nil {
			return nil, fmt.Errorf("scan config: %w", err)
		}
		configs = append(configs, &cfg)
	}
	return configs, rows.Err()
}

func (db *DB) DeactivateConfig(ctx context.Context, configID int64) error {
	_, err := db.ExecContext(ctx,
		`UPDATE configs SET next_run = NULL WHERE id = ?`,
		configID,
	)
	if err != nil {
		return fmt.Errorf("deactivate config: %w", err)
	}
	return nil
}

func (db *DB) DeactivateConfigByFilename(ctx context.Context, userID int64, filename string) error {
	cfg, err := db.GetConfig(ctx, userID, filename)
	if err != nil {
		return err
	}
	return db.DeactivateConfig(ctx, cfg.ID)
}

func (db *DB) ActivateConfig(ctx context.Context, userID int64, filename string) error {
	cfg, err := db.GetConfig(ctx, userID, filename)
	if err != nil {
		return err
	}

	nextRun, err := gronx.NextTick(cfg.CronExpr, false)
	if err != nil {
		return fmt.Errorf("calculate next run: %w", err)
	}

	_, err = db.ExecContext(ctx,
		`UPDATE configs SET next_run = ? WHERE id = ?`,
		nextRun,
		cfg.ID,
	)
	if err != nil {
		return fmt.Errorf("activate config: %w", err)
	}
	return nil
}

func (db *DB) UpdateNextRun(ctx context.Context, configID int64, nextRun *time.Time) error {
	var err error
	if nextRun == nil {
		_, err = db.ExecContext(ctx,
			`UPDATE configs SET next_run = NULL WHERE id = ?`,
			configID,
		)
	} else {
		_, err = db.ExecContext(ctx,
			`UPDATE configs SET next_run = ? WHERE id = ?`,
			nextRun,
			configID,
		)
	}
	if err != nil {
		return fmt.Errorf("update next run: %w", err)
	}
	return nil
}
