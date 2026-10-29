// Package store provides functionality for Herald.
package store

import (
	"context"
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

func scanUser(s scanner) (*User, error) {
	var u User
	if err := s.Scan(&u.ID, &u.PubkeyFP, &u.Pubkey, &u.CreatedAt); err != nil {
		return nil, err
	}
	return &u, nil
}

// GetOrCreateUser is race-free: concurrent first logins for one key both get the same row.
func (q *Queries) GetOrCreateUser(ctx context.Context, pubkeyFP, pubkey string) (*User, error) {
	if _, err := q.q.ExecContext(ctx,
		`INSERT INTO users (pubkey_fp, pubkey) VALUES (?, ?) ON CONFLICT(pubkey_fp) DO NOTHING`,
		pubkeyFP, pubkey,
	); err != nil {
		return nil, fmt.Errorf("insert user: %w", err)
	}
	return q.GetUserByFingerprint(ctx, pubkeyFP)
}

func (q *Queries) GetUserByFingerprint(ctx context.Context, fp string) (*User, error) {
	return scanUser(q.q.QueryRowContext(ctx, `SELECT id, pubkey_fp, pubkey, created_at FROM users WHERE pubkey_fp = ?`, fp))
}

func (q *Queries) GetUserByID(ctx context.Context, userID int64) (*User, error) {
	return scanUser(q.q.QueryRowContext(ctx, `SELECT id, pubkey_fp, pubkey, created_at FROM users WHERE id = ?`, userID))
}

func (db *DB) DeleteUser(ctx context.Context, userID int64) error {
	return db.WithTx(ctx, func(q *Queries) error { return q.deleteUser(ctx, userID) })
}

// deleteUser must run in a transaction: configs.user_id has no ON DELETE
// CASCADE, so configs go first.
func (q *Queries) deleteUser(ctx context.Context, userID int64) error {
	if _, err := q.q.ExecContext(ctx, `DELETE FROM configs WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("delete configs: %w", err)
	}
	if _, err := q.q.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, userID); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return nil
}
