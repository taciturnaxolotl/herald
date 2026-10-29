package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

func (q *Queries) GetConfigByToken(ctx context.Context, token string) (*Config, error) {
	var configID int64
	if err := q.q.QueryRowContext(ctx,
		`SELECT config_id FROM unsubscribe_tokens WHERE token = ?`, token,
	).Scan(&configID); err != nil {
		return nil, err
	}
	return q.GetConfigByID(ctx, configID)
}

func (q *Queries) DeleteToken(ctx context.Context, token string) error {
	_, err := q.q.ExecContext(ctx, `DELETE FROM unsubscribe_tokens WHERE token = ?`, token)
	return err
}

func (q *Queries) GetOrCreateUnsubscribeToken(ctx context.Context, configID int64) (string, error) {
	var token string
	err := q.q.QueryRowContext(ctx,
		`SELECT token FROM unsubscribe_tokens WHERE config_id = ? LIMIT 1`, configID,
	).Scan(&token)
	if err == nil {
		return token, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}

	token, err = NewToken()
	if err != nil {
		return "", err
	}
	if _, err := q.q.ExecContext(ctx,
		`INSERT INTO unsubscribe_tokens (token, config_id) VALUES (?, ?)`, token, configID,
	); err != nil {
		return "", fmt.Errorf("insert token: %w", err)
	}
	return token, nil
}
