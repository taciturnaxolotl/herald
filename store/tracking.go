package store

import (
	"context"
	"fmt"
	"time"
)

// RecordEmailSend logs a sent digest; trackingToken backs its keep-alive link.
func (q *Queries) RecordEmailSend(ctx context.Context, configID int64, recipient, subject, trackingToken string) error {
	_, err := q.q.ExecContext(ctx,
		`INSERT INTO email_sends (config_id, recipient, subject, tracking_token) VALUES (?, ?, ?, ?)`,
		configID, recipient, subject, nullIfEmpty(trackingToken),
	)
	if err != nil {
		return fmt.Errorf("insert email send: %w", err)
	}
	return nil
}

// DeactivateInactiveConfigs pauses active digests that were sent at least
// minSends times with no keep-alive in InactivityDays, returning them.
func (q *Queries) DeactivateInactiveConfigs(ctx context.Context, minSends int) ([]*Config, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -InactivityDays)
	configs, err := queryAll(ctx, q.q, scanConfig,
		`UPDATE configs SET next_run = NULL
		 WHERE next_run IS NOT NULL
		   AND (SELECT COUNT(*) FROM email_sends es WHERE es.config_id = configs.id) >= ?
		   AND (last_active_at IS NULL OR last_active_at < ?)
		   AND created_at < ?
		 RETURNING `+configCols,
		minSends, cutoff, cutoff)
	if err != nil {
		return nil, fmt.Errorf("deactivate inactive configs: %w", err)
	}
	return configs, nil
}

// CountRecentSends returns how many digests each config was sent in the last days.
func (q *Queries) CountRecentSends(ctx context.Context, configIDs []int64, days int) (map[int64]int, error) {
	counts := make(map[int64]int, len(configIDs))
	if len(configIDs) == 0 {
		return counts, nil
	}
	args := []any{time.Now().UTC().AddDate(0, 0, -days)}
	for _, id := range configIDs {
		args = append(args, id)
	}
	type row struct {
		id    int64
		count int
	}
	rows, err := queryAll(ctx, q.q, func(s scanner) (*row, error) {
		var r row
		return &r, s.Scan(&r.id, &r.count)
	}, `SELECT config_id, COUNT(*) FROM email_sends
	    WHERE sent_at > ? AND config_id IN (`+inClause(len(configIDs))+`)
	    GROUP BY config_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("count recent sends: %w", err)
	}
	for _, r := range rows {
		counts[r.id] = r.count
	}
	return counts, nil
}

// CleanupOldSends removes email send records older than specified days
func (q *Queries) CleanupOldSends(ctx context.Context, daysToKeep int) (int64, error) {
	result, err := q.q.ExecContext(ctx,
		`DELETE FROM email_sends WHERE sent_at < ?`, time.Now().UTC().AddDate(0, 0, -daysToKeep))
	if err != nil {
		return 0, fmt.Errorf("cleanup old sends: %w", err)
	}
	return result.RowsAffected()
}

// GetConfigByTrackingToken returns the config a digest's keep-alive token belongs to.
func (q *Queries) GetConfigByTrackingToken(ctx context.Context, trackingToken string) (*Config, error) {
	var configID int64
	if err := q.q.QueryRowContext(ctx,
		`SELECT config_id FROM email_sends WHERE tracking_token = ? LIMIT 1`, trackingToken,
	).Scan(&configID); err != nil {
		return nil, err
	}
	return q.GetConfigByID(ctx, configID)
}

// UpdateLastActive updates the last_active_at timestamp for a config by tracking token
func (q *Queries) UpdateLastActive(ctx context.Context, trackingToken string) error {
	result, err := q.q.ExecContext(ctx,
		`UPDATE configs SET last_active_at = ?
		 WHERE id = (SELECT config_id FROM email_sends WHERE tracking_token = ? LIMIT 1)`,
		time.Now().UTC(), trackingToken)
	if err != nil {
		return fmt.Errorf("update last active: %w", err)
	}
	if rows, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("rows affected: %w", err)
	} else if rows == 0 {
		return fmt.Errorf("tracking token not found")
	}
	return nil
}
