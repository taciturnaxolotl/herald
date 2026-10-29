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

// GetInactiveConfigs returns configs sent at least minSends digests with no
// keep-alive in the last daysWithoutActivity days.
func (q *Queries) GetInactiveConfigs(ctx context.Context, daysWithoutActivity int, minSends int) ([]int64, error) {
	cutoff := time.Now().UTC().AddDate(0, 0, -daysWithoutActivity)
	ids, err := queryAll(ctx, q.q, func(s scanner) (*int64, error) {
		var id int64
		return &id, s.Scan(&id)
	},
		`SELECT c.id FROM configs c
		 WHERE (SELECT COUNT(*) FROM email_sends es WHERE es.config_id = c.id) >= ?
		   AND (c.last_active_at IS NULL OR c.last_active_at < ?)
		   AND c.created_at < ?`,
		minSends, cutoff, cutoff)
	if err != nil {
		return nil, fmt.Errorf("query inactive configs: %w", err)
	}
	out := make([]int64, len(ids))
	for i, id := range ids {
		out[i] = *id
	}
	return out, nil
}

// CountRecentSends returns how many digests a config was sent in the last days.
func (q *Queries) CountRecentSends(ctx context.Context, configID int64, days int) (int, error) {
	var n int
	err := q.q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM email_sends WHERE config_id = ? AND sent_at > ?`,
		configID, time.Now().UTC().AddDate(0, 0, -days),
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count recent sends: %w", err)
	}
	return n, nil
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
