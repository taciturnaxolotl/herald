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

func (q *Queries) AddLog(ctx context.Context, configID int64, level, message string) error {
	_, err := q.q.ExecContext(ctx,
		`INSERT INTO logs (config_id, level, message) VALUES (?, ?, ?)`,
		configID, level, message,
	)
	if err != nil {
		return fmt.Errorf("add log: %w", err)
	}
	return nil
}

func (q *Queries) GetRecentLogs(ctx context.Context, userID int64, limit int) ([]*Log, error) {
	logs, err := queryAll(ctx, q.q, func(s scanner) (*Log, error) {
		var l Log
		if err := s.Scan(&l.ID, &l.ConfigID, &l.Message, &l.Level, &l.CreatedAt); err != nil {
			return nil, err
		}
		return &l, nil
	},
		`SELECT l.id, l.config_id, l.message, l.level, l.created_at
		 FROM logs l
		 JOIN configs c ON l.config_id = c.id
		 WHERE c.user_id = ?
		 ORDER BY l.created_at DESC LIMIT ?`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent logs: %w", err)
	}
	return logs, nil
}

// CleanupOldLogs deletes log entries older than olderThan.
func (q *Queries) CleanupOldLogs(ctx context.Context, olderThan time.Duration) (int64, error) {
	result, err := q.q.ExecContext(ctx, `DELETE FROM logs WHERE created_at < ?`, time.Now().UTC().Add(-olderThan))
	if err != nil {
		return 0, fmt.Errorf("cleanup old logs: %w", err)
	}
	return result.RowsAffected()
}
