package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/taciturnaxolotl/herald/config"
)

// Verification is the confirmed-opt-in state for a (user, email) pair.
type Verification struct {
	ID         int64
	UserID     int64
	Email      string
	VerifiedAt sql.NullTime
	Token      sql.NullString
	LastSentAt sql.NullTime
}

// IsEmailVerified reports whether the given user has a confirmed subscription
// to email. Sending is gated on this.
func (q *Queries) IsEmailVerified(ctx context.Context, userID int64, email string) (bool, error) {
	var verifiedAt sql.NullTime
	err := q.q.QueryRowContext(ctx,
		`SELECT verified_at FROM verified_emails WHERE user_id = ? AND email = ?`,
		userID, email,
	).Scan(&verifiedAt)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query verification: %w", err)
	}
	return verifiedAt.Valid, nil
}

// VerifyTokenTTL is how long a confirmation link stays valid.
const VerifyTokenTTL = 7 * 24 * time.Hour

// EnsurePendingVerification returns the verification row for (user, email),
// creating a pending one if none exists. A pending token past half its TTL is
// rotated so a resent link is never already dead; verified rows are returned
// unchanged.
func (q *Queries) EnsurePendingVerification(ctx context.Context, userID int64, email string) (*Verification, error) {
	token, err := NewToken()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()

	_, err = q.q.ExecContext(ctx,
		`INSERT INTO verified_emails (user_id, email, token, token_created_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(user_id, email) DO UPDATE SET token = excluded.token, token_created_at = excluded.token_created_at
		 WHERE verified_emails.verified_at IS NULL
		   AND (verified_emails.token IS NULL OR verified_emails.token_created_at IS NULL OR verified_emails.token_created_at < ?)`,
		userID, email, token, now, now.Add(-VerifyTokenTTL/2),
	)
	if err != nil {
		return nil, fmt.Errorf("upsert verification: %w", err)
	}
	return q.getVerification(ctx, userID, email)
}

func (q *Queries) getVerification(ctx context.Context, userID int64, email string) (*Verification, error) {
	var v Verification
	err := q.q.QueryRowContext(ctx,
		`SELECT id, user_id, email, verified_at, token, last_sent_at
		 FROM verified_emails WHERE user_id = ? AND email = ?`,
		userID, email,
	).Scan(&v.ID, &v.UserID, &v.Email, &v.VerifiedAt, &v.Token, &v.LastSentAt)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

// GetVerificationByToken looks up a pending verification by its confirmation
// token, rejecting tokens older than maxAge.
func (q *Queries) GetVerificationByToken(ctx context.Context, token string, maxAge time.Duration) (*Verification, error) {
	var v Verification
	var tokenCreated sql.NullTime
	err := q.q.QueryRowContext(ctx,
		`SELECT id, user_id, email, verified_at, token, last_sent_at, token_created_at
		 FROM verified_emails WHERE token = ?`,
		token,
	).Scan(&v.ID, &v.UserID, &v.Email, &v.VerifiedAt, &v.Token, &v.LastSentAt, &tokenCreated)
	if err != nil {
		return nil, err
	}
	if tokenCreated.Valid && time.Since(tokenCreated.Time) > maxAge {
		return nil, sql.ErrNoRows
	}
	return &v, nil
}

// ConfirmVerification spends a confirmation token, marks the address
// verified and schedules the user's digests for it, all or nothing.
func (db *DB) ConfirmVerification(ctx context.Context, token string) (*Verification, error) {
	var v *Verification
	err := db.WithTx(ctx, func(q *Queries) error {
		var err error
		if v, err = q.GetVerificationByToken(ctx, token, VerifyTokenTTL); err != nil {
			return err
		}
		if err := q.MarkEmailVerified(ctx, v.ID); err != nil {
			return err
		}
		return q.ActivateConfigsForEmail(ctx, v.UserID, v.Email)
	})
	if err != nil {
		return nil, err
	}
	return v, nil
}

// MarkEmailVerified confirms a verification and clears its (now spent) token.
func (q *Queries) MarkEmailVerified(ctx context.Context, id int64) error {
	_, err := q.q.ExecContext(ctx,
		`UPDATE verified_emails SET verified_at = ?, token = NULL WHERE id = ?`,
		time.Now().UTC(), id,
	)
	if err != nil {
		return fmt.Errorf("mark verified: %w", err)
	}
	return nil
}

// MarkConfirmationSent records that a confirmation email was just sent, for the
// per-address cooldown.
func (q *Queries) MarkConfirmationSent(ctx context.Context, id int64, when time.Time) error {
	_, err := q.q.ExecContext(ctx,
		`UPDATE verified_emails SET last_sent_at = ? WHERE id = ?`,
		when.UTC(), id,
	)
	if err != nil {
		return fmt.Errorf("mark confirmation sent: %w", err)
	}
	return nil
}

// LastConfirmationToEmail returns the most recent time a confirmation was sent
// to email, across all users. This backs the global per-address cooldown, so
// several attackers cannot each prompt the same victim.
func (q *Queries) LastConfirmationToEmail(ctx context.Context, email string) (sql.NullTime, error) {
	// Select the bare column (not MAX) so the sqlite driver keeps DATETIME type
	// affinity and scans into time.Time; an aggregate returns it as a string.
	var last sql.NullTime
	err := q.q.QueryRowContext(ctx,
		`SELECT last_sent_at FROM verified_emails
		 WHERE email = ? AND last_sent_at IS NOT NULL
		 ORDER BY last_sent_at DESC LIMIT 1`,
		email,
	).Scan(&last)
	if err == sql.ErrNoRows {
		return sql.NullTime{}, nil
	}
	if err != nil {
		return sql.NullTime{}, fmt.Errorf("query last confirmation: %w", err)
	}
	return last, nil
}

// ActivateConfigsForEmail schedules every config the user owns that targets
// email, by computing each one's next run from its cron expression. Called
// once the address is verified.
func (q *Queries) ActivateConfigsForEmail(ctx context.Context, userID int64, email string) error {
	rows, err := q.q.QueryContext(ctx,
		`SELECT id, cron_expr FROM configs WHERE user_id = ? AND email = ?`,
		userID, email,
	)
	if err != nil {
		return fmt.Errorf("query configs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type pending struct {
		id   int64
		cron string
	}
	var configs []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.cron); err != nil {
			return fmt.Errorf("scan config: %w", err)
		}
		configs = append(configs, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	now := time.Now().UTC()
	for _, p := range configs {
		nextRun, err := config.NextRun(p.cron, now)
		if err != nil {
			continue // leave a config with a bad cron inactive
		}
		if err := q.SetNextRun(ctx, p.id, &nextRun); err != nil {
			return fmt.Errorf("activate config %d: %w", p.id, err)
		}
	}
	return nil
}

// DeleteStalePendingVerifications removes unconfirmed verification rows older
// than maxAge along with any still-inactive configs that depend on them, so the
// tables do not accumulate junk from abandoned or abusive uploads.
func (db *DB) DeleteStalePendingVerifications(ctx context.Context, maxAge time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-maxAge)
	var n int64
	err := db.WithTx(ctx, func(q *Queries) error {
		// Delete inactive configs whose (user, email) is still unverified and whose
		// verification attempt has aged out.
		if _, err := q.q.ExecContext(ctx,
			`DELETE FROM configs
			 WHERE next_run IS NULL
			   AND EXISTS (
			     SELECT 1 FROM verified_emails v
			     WHERE v.user_id = configs.user_id AND v.email = configs.email
			       AND v.verified_at IS NULL
			       AND COALESCE(v.token_created_at, v.created_at) < ?
			   )`,
			cutoff,
		); err != nil {
			return fmt.Errorf("delete stale configs: %w", err)
		}

		res, err := q.q.ExecContext(ctx,
			`DELETE FROM verified_emails
			 WHERE verified_at IS NULL AND COALESCE(token_created_at, created_at) < ?`,
			cutoff,
		)
		if err != nil {
			return fmt.Errorf("delete stale verifications: %w", err)
		}
		n, _ = res.RowsAffected()
		return nil
	})
	return n, err
}
