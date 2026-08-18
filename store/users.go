// Package store provides functionality for Herald.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// FingerprintSlug converts an OpenSSH fingerprint (e.g. "SHA256:xxxx") into a
// URL-safe slug. Standard base64 uses +, /, and = padding, all of which cause
// problems in URL paths: / creates extra path segments, = becomes %3D or =3D
// in quoted-printable. The SHA256: prefix is preserved. The conversion is
// deterministic and reversible.
func FingerprintSlug(fp string) string {
	s := strings.ReplaceAll(fp, "+", "-")
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.TrimRight(s, "=")
	return s
}

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
		CreatedAt: time.Now().UTC(),
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

func (db *DB) GetUserByID(ctx context.Context, userID int64) (*User, error) {
	var user User
	err := db.QueryRowContext(ctx,
		`SELECT id, pubkey_fp, pubkey, created_at FROM users WHERE id = ?`,
		userID,
	).Scan(&user.ID, &user.PubkeyFP, &user.Pubkey, &user.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (db *DB) DeleteUser(ctx context.Context, userID int64) error {
	_, err := db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID)
	return err
}
