package store

import (
	"context"
	"testing"
	"time"
)

func TestEmailTracking(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	user, err := db.GetOrCreateUser(ctx, "test-fp", "test-pubkey")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	cfg, err := db.CreateConfig(ctx, user.ID, "test.txt", "test@example.com", "0 0 * * *", false, "test config", time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("create config: %v", err)
	}

	t.Run("RecordEmailSendAndLookupByToken", func(t *testing.T) {
		token, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if err := db.RecordEmailSend(ctx, cfg.ID, "test@example.com", "Subject", token); err != nil {
			t.Fatalf("record email send: %v", err)
		}
		got, err := db.GetConfigByTrackingToken(ctx, token)
		if err != nil || got.ID != cfg.ID {
			t.Fatalf("GetConfigByTrackingToken = %v, %v", got, err)
		}

		if err := db.UpdateLastActive(ctx, token); err != nil {
			t.Fatalf("update last active: %v", err)
		}
		if got, _ := db.GetConfigByID(ctx, cfg.ID); !got.LastActiveAt.Valid {
			t.Error("last active should be set")
		}
		if err := db.UpdateLastActive(ctx, "nope"); err == nil {
			t.Error("unknown token should fail")
		}
	})

	t.Run("CountRecentSends", func(t *testing.T) {
		counts, err := db.CountRecentSends(ctx, []int64{cfg.ID}, 30)
		if err != nil {
			t.Fatal(err)
		}
		if n := counts[cfg.ID]; n != 1 {
			t.Errorf("expected 1 recent send, got %d", n)
		}
	})

	t.Run("DeactivateInactiveConfigs", func(t *testing.T) {
		cfg2, err := db.CreateConfig(ctx, user.ID, "inactive.txt", "inactive@example.com", "0 0 * * *", false, "inactive config", time.Now().Add(24*time.Hour))
		if err != nil {
			t.Fatalf("create config: %v", err)
		}
		for i := 0; i < 3; i++ {
			if err := db.RecordEmailSend(ctx, cfg2.ID, "inactive@example.com", "Subject", ""); err != nil {
				t.Fatal(err)
			}
		}
		old := time.Now().UTC().AddDate(0, 0, -InactivityDays-10)
		if _, err := db.q.ExecContext(ctx, `UPDATE configs SET created_at = ? WHERE id = ?`, old, cfg2.ID); err != nil {
			t.Fatal(err)
		}

		paused, err := db.DeactivateInactiveConfigs(ctx, 3)
		if err != nil {
			t.Fatalf("deactivate inactive configs: %v", err)
		}
		if len(paused) != 1 || paused[0].ID != cfg2.ID {
			t.Fatalf("expected only the old, never-renewed config, got %+v", paused)
		}
		if got, _ := db.GetConfigByID(ctx, cfg2.ID); got.Active() {
			t.Fatal("inactive config should be paused")
		}
		if again, _ := db.DeactivateInactiveConfigs(ctx, 3); len(again) != 0 {
			t.Fatal("an already paused config should not be reported again")
		}
	})

	t.Run("CleanupOldSends", func(t *testing.T) {
		old := time.Now().UTC().AddDate(0, 0, -200)
		if _, err := db.q.ExecContext(ctx, `UPDATE email_sends SET sent_at = ? WHERE config_id = ?`, old, cfg.ID); err != nil {
			t.Fatal(err)
		}
		deleted, err := db.CleanupOldSends(ctx, 180)
		if err != nil {
			t.Fatalf("cleanup old sends: %v", err)
		}
		if deleted != 1 {
			t.Errorf("expected 1 old send deleted, got %d", deleted)
		}
	})
}
