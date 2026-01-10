package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
)

func (db *DB) CreateUnsubscribeToken(ctx context.Context, configID int64) (string, error) {
	// Generate random token
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	token := base64.URLEncoding.EncodeToString(tokenBytes)

	_, err := db.ExecContext(ctx,
		`INSERT INTO unsubscribe_tokens (token, config_id) VALUES (?, ?)`,
		token, configID,
	)
	if err != nil {
		return "", fmt.Errorf("insert token: %w", err)
	}

	return token, nil
}

func (db *DB) GetConfigByToken(ctx context.Context, token string) (*Config, error) {
	var configID int64
	err := db.QueryRowContext(ctx,
		`SELECT config_id FROM unsubscribe_tokens WHERE token = ?`,
		token,
	).Scan(&configID)
	if err != nil {
		return nil, err
	}

	return db.GetConfigByID(ctx, configID)
}

func (db *DB) DeleteToken(ctx context.Context, token string) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM unsubscribe_tokens WHERE token = ?`,
		token,
	)
	return err
}

func (db *DB) GetOrCreateUnsubscribeToken(ctx context.Context, configID int64) (string, error) {
	// Check if token already exists
	var token string
	err := db.QueryRowContext(ctx,
		`SELECT token FROM unsubscribe_tokens WHERE config_id = ? LIMIT 1`,
		configID,
	).Scan(&token)

	if err == nil {
		return token, nil
	}

	if err != sql.ErrNoRows {
		return "", err
	}

	// Create new token
	return db.CreateUnsubscribeToken(ctx, configID)
}
