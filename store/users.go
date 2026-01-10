package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type User struct {
	ID        int64
	PubkeyFP  string
	Pubkey    string
	CreatedAt time.Time
}

func (db *DB) GetOrCreateUser(ctx context.Context, pubkeyFP, pubkey string) (*User, error) {
	user, err := db.GetUserByFingerprint(ctx, pubkeyFP)
	if err == nil {
		return user, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	result, err := db.ExecContext(ctx,
		`INSERT INTO users (pubkey_fp, pubkey) VALUES (?, ?)`,
		pubkeyFP, pubkey,
	)
	if err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get last insert id: %w", err)
	}

	return &User{
		ID:        id,
		PubkeyFP:  pubkeyFP,
		Pubkey:    pubkey,
		CreatedAt: time.Now(),
	}, nil
}

func (db *DB) GetUserByFingerprint(ctx context.Context, fp string) (*User, error) {
	var user User
	err := db.QueryRowContext(ctx,
		`SELECT id, pubkey_fp, pubkey, created_at FROM users WHERE pubkey_fp = ?`,
		fp,
	).Scan(&user.ID, &user.PubkeyFP, &user.Pubkey, &user.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &user, nil
}
