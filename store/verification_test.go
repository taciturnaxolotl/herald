package store

import (
	"context"
	"testing"
	"time"
)

func TestVerificationLifecycle(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	user, err := db.GetOrCreateUser(ctx, "SHA256:testfp", "ssh-ed25519 AAAA test")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	const email = "person@example.com"

	// Unknown pair is unverified.
	if v, err := db.IsEmailVerified(ctx, user.ID, email); err != nil || v {
		t.Fatalf("expected unverified, got v=%v err=%v", v, err)
	}

	// Pending creation is idempotent and yields a token.
	p1, err := db.EnsurePendingVerification(ctx, user.ID, email)
	if err != nil {
		t.Fatalf("ensure pending: %v", err)
	}
	if !p1.Token.Valid || p1.Token.String == "" {
		t.Fatal("expected a token on pending verification")
	}
	p2, err := db.EnsurePendingVerification(ctx, user.ID, email)
	if err != nil {
		t.Fatalf("ensure pending (2): %v", err)
	}
	if p2.ID != p1.ID || p2.Token.String != p1.Token.String {
		t.Fatal("ensure pending should be idempotent")
	}

	// A config for this pair, held inactive pending confirmation.
	cfg, err := db.CreateConfig(ctx, user.ID, "daily.txt", email, "0 8 * * *", true, false, "raw", time.Now())
	if err != nil {
		t.Fatalf("create config: %v", err)
	}
	if err := db.DeactivateConfig(ctx, cfg.ID); err != nil {
		t.Fatalf("deactivate: %v", err)
	}

	// Cooldown tracking.
	if last, err := db.LastConfirmationToEmail(ctx, email); err != nil || last.Valid {
		t.Fatalf("expected no prior confirmation, got valid=%v err=%v", last.Valid, err)
	}
	now := time.Now().UTC()
	if err := db.MarkConfirmationSent(ctx, p1.ID, now); err != nil {
		t.Fatalf("mark sent: %v", err)
	}
	if last, err := db.LastConfirmationToEmail(ctx, email); err != nil || !last.Valid {
		t.Fatalf("expected a recorded confirmation, got valid=%v err=%v", last.Valid, err)
	}

	// Token lookup honors the TTL.
	if _, err := db.GetVerificationByToken(ctx, p1.Token.String, time.Hour); err != nil {
		t.Fatalf("token lookup within TTL: %v", err)
	}
	if _, err := db.GetVerificationByToken(ctx, p1.Token.String, 0); err == nil {
		t.Fatal("expired token should not resolve")
	}

	// Confirm and activate.
	if err := db.MarkEmailVerified(ctx, p1.ID); err != nil {
		t.Fatalf("mark verified: %v", err)
	}
	if v, err := db.IsEmailVerified(ctx, user.ID, email); err != nil || !v {
		t.Fatalf("expected verified, got v=%v err=%v", v, err)
	}
	if err := db.ActivateConfigsForEmail(ctx, user.ID, email); err != nil {
		t.Fatalf("activate: %v", err)
	}
	got, err := db.GetConfig(ctx, user.ID, "daily.txt")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	if !got.NextRun.Valid {
		t.Fatal("config should be scheduled after verification")
	}

	// Spent token no longer resolves.
	if _, err := db.GetVerificationByToken(ctx, p1.Token.String, time.Hour); err == nil {
		t.Fatal("token should be cleared after verification")
	}
}

func TestDeleteStalePendingVerifications(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	user, err := db.GetOrCreateUser(ctx, "SHA256:testfp", "ssh-ed25519 AAAA test")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}

	// A fresh pending row must survive.
	if _, err := db.EnsurePendingVerification(ctx, user.ID, "fresh@example.com"); err != nil {
		t.Fatalf("ensure fresh: %v", err)
	}

	// An aged pending row (backdated created_at) must be swept.
	old := time.Now().UTC().Add(-72 * time.Hour)
	if _, err := db.ExecContext(ctx,
		`INSERT INTO verified_emails (user_id, email, token, token_created_at, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		user.ID, "stale@example.com", "stale-token", old, old,
	); err != nil {
		t.Fatalf("insert stale: %v", err)
	}

	deleted, err := db.DeleteStalePendingVerifications(ctx, 48*time.Hour)
	if err != nil {
		t.Fatalf("delete stale: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 stale row deleted, got %d", deleted)
	}
	if _, err := db.GetVerificationByToken(ctx, "stale-token", time.Hour); err == nil {
		t.Fatal("stale row should be gone")
	}
	if v, err := db.IsEmailVerified(ctx, user.ID, "fresh@example.com"); err != nil || v {
		t.Fatalf("fresh row should remain (pending), got v=%v err=%v", v, err)
	}
}
