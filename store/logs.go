package store

import (
	"context"
	"fmt"
	"time"
)

type Log struct {
	ID        int64
	ConfigID  int64
	Message   string
	Level     string
	CreatedAt time.Time
}

func (db *DB) AddLog(ctx context.Context, configID int64, level, message string) error {
	_, err := db.ExecContext(ctx,
		`INSERT INTO logs (config_id, level, message) VALUES (?, ?, ?)`,
		configID, level, message,
	)
	if err != nil {
		return fmt.Errorf("add log: %w", err)
	}
	return nil
}

func (db *DB) GetLogs(ctx context.Context, configID int64, limit int) ([]*Log, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, config_id, message, level, created_at
		 FROM logs WHERE config_id = ? ORDER BY created_at DESC LIMIT ?`,
		configID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query logs: %w", err)
	}
	defer rows.Close()

	var logs []*Log
	for rows.Next() {
		var log Log
		if err := rows.Scan(&log.ID, &log.ConfigID, &log.Message, &log.Level, &log.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan log: %w", err)
		}
		logs = append(logs, &log)
	}
	return logs, rows.Err()
}

func (db *DB) GetRecentLogs(ctx context.Context, userID int64, limit int) ([]*Log, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT l.id, l.config_id, l.message, l.level, l.created_at
		 FROM logs l
		 JOIN configs c ON l.config_id = c.id
		 WHERE c.user_id = ?
		 ORDER BY l.created_at DESC LIMIT ?`,
		userID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query recent logs: %w", err)
	}
	defer rows.Close()

	var logs []*Log
	for rows.Next() {
		var log Log
		if err := rows.Scan(&log.ID, &log.ConfigID, &log.Message, &log.Level, &log.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan log: %w", err)
		}
		logs = append(logs, &log)
	}
	return logs, rows.Err()
}
