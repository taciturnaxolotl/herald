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

// UnsubscribeAction is what redeeming an unsubscribe link does.
type UnsubscribeAction int

const (
	// PauseConfig stops the one digest the link came from.
	PauseConfig UnsubscribeAction = iota
	// DeleteAccount removes the owner and every digest they have.
	DeleteAccount
)

// RedeemUnsubscribeToken applies action and spends the token in one transaction.
// It returns the config the token belonged to.
func (db *DB) RedeemUnsubscribeToken(ctx context.Context, token string, action UnsubscribeAction) (*Config, error) {
	var cfg *Config
	err := db.WithTx(ctx, func(q *Queries) error {
		var err error
		if cfg, err = q.GetConfigByToken(ctx, token); err != nil {
			return err
		}
		switch action {
		case PauseConfig:
			if err := q.SetNextRun(ctx, cfg.ID, nil); err != nil {
				return err
			}
		case DeleteAccount:
			if err := q.deleteUser(ctx, cfg.UserID); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown unsubscribe action %d", action)
		}
		return q.DeleteToken(ctx, token)
	})
	if err != nil {
		return nil, err
	}
	return cfg, nil
}
