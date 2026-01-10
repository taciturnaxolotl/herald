package store

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"time"
)

type EmailSend struct {
	ID             int64
	ConfigID       int64
	Recipient      string
	Subject        string
	TrackingToken  string
	SentAt         time.Time
	Bounced        bool
	BounceReason   sql.NullString
	Opened         bool
	OpenedAt       sql.NullTime
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

// GetInactiveConfigs returns config IDs that haven't had opens in the specified days
func (db *DB) GetInactiveConfigs(daysWithoutOpen int, minSends int) ([]int64, error) {
	query := `
		SELECT DISTINCT es.config_id
		FROM email_sends es
		WHERE es.config_id IN (
			SELECT config_id
			FROM email_sends
			GROUP BY config_id
			HAVING COUNT(*) >= ?
		)
		AND es.sent_at > datetime('now', '-' || ? || ' days')
		AND es.config_id NOT IN (
			SELECT config_id
			FROM email_sends
			WHERE opened = TRUE
			AND sent_at > datetime('now', '-' || ? || ' days')
		)
		GROUP BY es.config_id
	`

	rows, err := db.Query(query, minSends, daysWithoutOpen, daysWithoutOpen)
	if err != nil {
		return nil, fmt.Errorf("query inactive configs: %w", err)
	}
	defer rows.Close()

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
	query := `
		SELECT
			COUNT(*) as total_sends,
			SUM(CASE WHEN opened = TRUE THEN 1 ELSE 0 END) as opens,
			SUM(CASE WHEN bounced = TRUE THEN 1 ELSE 0 END) as bounces,
			MAX(opened_at) as last_open
		FROM email_sends
		WHERE config_id = ?
		AND sent_at > datetime('now', '-' || ? || ' days')
	`

	var lastOpenTime sql.NullTime
	err = db.QueryRow(query, configID, days).Scan(&totalSends, &opens, &bounces, &lastOpenTime)
	if err != nil {
		return 0, 0, 0, nil, fmt.Errorf("query engagement: %w", err)
	}

	if lastOpenTime.Valid {
		lastOpen = &lastOpenTime.Time
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

func generateTrackingToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}
