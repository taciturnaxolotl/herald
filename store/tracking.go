package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"time"
)

type EmailSend struct {
	ID            int64
	ConfigID      int64
	Recipient     string
	Subject       string
	TrackingToken string
	SentAt        time.Time
	Bounced       bool
	BounceReason  sql.NullString
	Opened        bool
	OpenedAt      sql.NullTime
}

// RecordEmailSend records an email send with optional tracking token
func (db *DB) RecordEmailSend(configID int64, recipient, subject string, includeTracking bool) (string, error) {
	var trackingToken string
	if includeTracking {
		token, err := generateTrackingToken()
		if err != nil {
			return "", fmt.Errorf("generate tracking token: %w", err)
		}
		trackingToken = token
	}

	query := `INSERT INTO email_sends (config_id, recipient, subject, tracking_token)
	          VALUES (?, ?, ?, ?)`
	_, err := db.Exec(query, configID, recipient, subject, sql.NullString{String: trackingToken, Valid: trackingToken != ""})
	if err != nil {
		return "", fmt.Errorf("insert email send: %w", err)
	}

	return trackingToken, nil
}

// RecordEmailSendTx records an email send within an existing transaction
func (db *DB) RecordEmailSendTx(tx *sql.Tx, configID int64, recipient, subject, trackingToken string) error {
	query := `INSERT INTO email_sends (config_id, recipient, subject, tracking_token)
	          VALUES (?, ?, ?, ?)`
	_, err := tx.Exec(query, configID, recipient, subject, sql.NullString{String: trackingToken, Valid: trackingToken != ""})
	if err != nil {
		return fmt.Errorf("insert email send: %w", err)
	}
	return nil
}

// MarkEmailBounced marks an email as bounced
func (db *DB) MarkEmailBounced(configID int64, recipient, reason string) error {
	query := `UPDATE email_sends
	          SET bounced = TRUE, bounce_reason = ?
	          WHERE config_id = ? AND recipient = ?
	          AND sent_at > datetime('now', '-7 days')
	          ORDER BY sent_at DESC
	          LIMIT 1`
	_, err := db.Exec(query, reason, configID, recipient)
	return err
}

// MarkEmailOpened marks an email as opened via tracking token
func (db *DB) MarkEmailOpened(trackingToken string) error {
	query := `UPDATE email_sends
	          SET opened = TRUE, opened_at = CURRENT_TIMESTAMP
	          WHERE tracking_token = ? AND opened = FALSE`
	result, err := db.Exec(query, trackingToken)
	if err != nil {
		return fmt.Errorf("update email opened: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}

	if rows == 0 {
		return fmt.Errorf("tracking token not found or already opened")
	}

	return nil
}

// GetInactiveConfigs returns config IDs that haven't had keep-alive activity in the specified days
func (db *DB) GetInactiveConfigs(daysWithoutActivity int, minSends int) ([]int64, error) {
	query := `
		SELECT DISTINCT c.id
		FROM configs c
		INNER JOIN email_sends es ON es.config_id = c.id
		WHERE c.id IN (
			SELECT config_id
			FROM email_sends
			GROUP BY config_id
			HAVING COUNT(*) >= ?
		)
		AND (
			c.last_active_at IS NULL
			OR c.last_active_at < datetime('now', '-' || ? || ' days')
		)
		AND c.created_at < datetime('now', '-' || ? || ' days')
		GROUP BY c.id
	`

	rows, err := db.Query(query, minSends, daysWithoutActivity, daysWithoutActivity)
	if err != nil {
		return nil, fmt.Errorf("query inactive configs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var configIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan config id: %w", err)
		}
		configIDs = append(configIDs, id)
	}

	return configIDs, rows.Err()
}

// GetConfigEngagement returns engagement stats for a config
func (db *DB) GetConfigEngagement(configID int64, days int) (totalSends, opens, bounces int, lastOpen *time.Time, err error) {
	// First get counts
	countQuery := `
		SELECT
			COUNT(*) as total_sends,
			COALESCE(SUM(CASE WHEN opened = TRUE THEN 1 ELSE 0 END), 0) as opens,
			COALESCE(SUM(CASE WHEN bounced = TRUE THEN 1 ELSE 0 END), 0) as bounces
		FROM email_sends
		WHERE config_id = ?
		AND sent_at > datetime('now', '-' || ? || ' days')
	`

	err = db.QueryRow(countQuery, configID, days).Scan(&totalSends, &opens, &bounces)
	if err != nil {
		return 0, 0, 0, nil, fmt.Errorf("query engagement counts: %w", err)
	}

	// Get most recent open
	openQuery := `
		SELECT opened_at
		FROM email_sends
		WHERE config_id = ?
		AND opened = TRUE
		AND sent_at > datetime('now', '-' || ? || ' days')
		ORDER BY opened_at DESC
		LIMIT 1
	`

	var lastOpenStr sql.NullString
	err = db.QueryRow(openQuery, configID, days).Scan(&lastOpenStr)
	if err != nil && err != sql.ErrNoRows {
		return 0, 0, 0, nil, fmt.Errorf("query last open: %w", err)
	}

	if lastOpenStr.Valid && lastOpenStr.String != "" {
		t, err := time.Parse("2006-01-02 15:04:05", lastOpenStr.String)
		if err == nil {
			lastOpen = &t
		}
	}

	return totalSends, opens, bounces, lastOpen, nil
}

// CleanupOldSends removes email send records older than specified days
func (db *DB) CleanupOldSends(daysToKeep int) (int64, error) {
	query := `DELETE FROM email_sends WHERE sent_at < datetime('now', '-' || ? || ' days')`
	result, err := db.Exec(query, daysToKeep)
	if err != nil {
		return 0, fmt.Errorf("cleanup old sends: %w", err)
	}
	return result.RowsAffected()
}

// GenerateTrackingToken generates a secure random tracking token
func (db *DB) GenerateTrackingToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

func generateTrackingToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// UpdateLastActive updates the last_active_at timestamp for a config by tracking token
func (db *DB) UpdateLastActive(trackingToken string) error {
	query := `UPDATE configs
	          SET last_active_at = CURRENT_TIMESTAMP
	          WHERE id = (
	              SELECT config_id FROM email_sends WHERE tracking_token = ? LIMIT 1
	          )`
	result, err := db.Exec(query, trackingToken)
	if err != nil {
		return fmt.Errorf("update last active: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}

	if rows == 0 {
		return fmt.Errorf("tracking token not found")
	}

	return nil
}
