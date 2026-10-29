package store

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"
)

func TestOpenRecordsSchemaVersion(t *testing.T) {
	path := t.TempDir() + "/herald.db"
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.q.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != len(migrations) {
		t.Fatalf("user_version = %d, want %d", version, len(migrations))
	}
	_ = db.Close()

	again, err := Open(path)
	if err != nil {
		t.Fatalf("reopening a migrated database failed: %v", err)
	}
	_ = again.Close()
}

func TestOpenRefusesNewerSchema(t *testing.T) {
	path := t.TempDir() + "/herald.db"
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.q.ExecContext(context.Background(), `PRAGMA user_version = 999`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if _, err := Open(path); err == nil {
		t.Fatal("a database from a newer build must not be opened")
	}
}

func TestForeignKeysOnAfterMigrating(t *testing.T) {
	db := setupTestDB(t)
	var on int
	if err := db.q.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&on); err != nil {
		t.Fatal(err)
	}
	if on != 1 {
		t.Fatal("foreign keys must be re-enabled after migrations run")
	}
}

func TestRebuildConfigsKeepsDataAndCascades(t *testing.T) {
	path := t.TempDir() + "/old.db"
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := raw.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		t.Fatal(err)
	}
	tx, err := raw.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := baselineSchema(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_, err = raw.ExecContext(ctx, `
		PRAGMA user_version = 1;
		INSERT INTO users (id, pubkey_fp, pubkey) VALUES (1, 'fp', 'key');
		INSERT INTO configs (id, user_id, filename, email, cron_expr, digest, raw_text, next_run) VALUES (7, 1, 'a.txt', 'a@example.com', '0 8 * * *', 0, 'raw', '2026-01-01 08:00:00');
		INSERT INTO feeds (id, config_id, url) VALUES (3, 7, 'https://example.com/feed.xml');
		INSERT INTO seen_items (feed_id, guid) VALUES (3, 'g');`)
	_ = raw.Close()
	if err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer func() { _ = db.Close() }()

	cfg, err := db.GetConfigByID(ctx, 7)
	if err != nil || cfg.Filename != "a.txt" || !cfg.Active() {
		t.Fatalf("config not carried over: %+v %v", cfg, err)
	}
	if seen, _ := db.GetSeenGUIDs(ctx, 3, []string{"g"}); !seen["g"] {
		t.Fatal("feeds and history under the config must survive the rebuild")
	}

	if err := db.DeleteUser(ctx, 1); err != nil {
		t.Fatalf("delete user: %v", err)
	}
	var left int
	_ = db.q.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM configs) + (SELECT COUNT(*) FROM feeds) + (SELECT COUNT(*) FROM seen_items)`).Scan(&left)
	if left != 0 {
		t.Fatalf("deleting the user left %d dependent rows", left)
	}
}

func TestStoreStripImageHostsBackfills(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	user, _ := db.GetOrCreateUser(ctx, "fp", "key")
	if _, err := db.q.ExecContext(ctx,
		`INSERT INTO configs (id, user_id, filename, email, cron_expr, raw_text) VALUES (9, ?, 'a.txt', 'a@example.com', '0 8 * * *', ?)`,
		user.ID, "=: strip_image_hosts stickers.example, ads.example\n=: bogus line"); err != nil {
		t.Fatal(err)
	}

	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE configs DROP COLUMN strip_image_hosts`); err != nil {
		t.Fatal(err)
	}
	if err := storeStripImageHosts(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	cfg, err := db.GetConfigByID(ctx, 9)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.StripImageHosts) != 2 || cfg.StripImageHosts[0] != "stickers.example" {
		t.Fatalf("backfilled hosts = %v", cfg.StripImageHosts)
	}
}

func TestTightenIndexesDedupes(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	user, _ := db.GetOrCreateUser(ctx, "fp", "key")
	cfg, _ := db.CreateConfig(ctx, user.ID, "a.txt", "a@example.com", "0 8 * * *", false, "raw", time.Now())

	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`DROP INDEX idx_feeds_config_url`,
		`DROP INDEX idx_unsubscribe_tokens_config`,
		`INSERT INTO feeds (id, config_id, url) VALUES (1, ?1, 'https://x/f'), (2, ?1, 'https://x/f')`,
		`INSERT INTO seen_items (feed_id, guid) VALUES (1, 'keep'), (2, 'drop')`,
		`INSERT INTO unsubscribe_tokens (token, config_id) VALUES ('t1', ?1), ('t2', ?1)`,
	} {
		var args []any
		if strings.Contains(stmt, "?1") {
			args = []any{cfg.ID}
		}
		if _, err := tx.ExecContext(ctx, stmt, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := tightenIndexes(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	feeds, _ := db.GetFeedsByConfig(ctx, cfg.ID)
	if len(feeds) != 1 || feeds[0].ID != 1 {
		t.Fatalf("expected the first feed kept, got %+v", feeds)
	}
	if token, _ := db.GetOrCreateUnsubscribeToken(ctx, cfg.ID); token != "t1" {
		t.Fatalf("expected the first token kept, got %q", token)
	}
	if _, err := db.CreateFeed(ctx, cfg.ID, "https://x/f", ""); err == nil {
		t.Fatal("a duplicate feed URL must now be refused")
	}
}

func TestGetOrCreateUnsubscribeTokenIsStable(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	user, _ := db.GetOrCreateUser(ctx, "fp", "key")
	cfg, _ := db.CreateConfig(ctx, user.ID, "a.txt", "a@example.com", "0 8 * * *", false, "raw", time.Now())
	a, err := db.GetOrCreateUnsubscribeToken(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := db.GetOrCreateUnsubscribeToken(ctx, cfg.ID); a != b {
		t.Fatalf("token changed: %q then %q", a, b)
	}
}

func TestTimesAreStoredInUTC(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	user, _ := db.GetOrCreateUser(ctx, "fp", "key")
	cfg, _ := db.CreateConfig(ctx, user.ID, "a.txt", "a@example.com", "0 8 * * *", false, "raw", time.Now())

	// 08:30 in New York is 13:30 UTC: due at 13:00 UTC only if compared as UTC.
	ny := time.FixedZone("EST", -5*3600)
	next := time.Date(2026, 1, 1, 8, 30, 0, 0, ny)
	if err := db.SetNextRun(ctx, cfg.ID, &next); err != nil {
		t.Fatal(err)
	}
	var raw string
	_ = db.q.QueryRowContext(ctx, `SELECT next_run || '' FROM configs WHERE id = ?`, cfg.ID).Scan(&raw)
	if !strings.HasPrefix(raw, "2026-01-01 13:30:00") {
		t.Fatalf("stored %q, want UTC", raw)
	}
	if due, _ := db.GetDueConfigs(ctx, time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)); len(due) != 0 {
		t.Fatal("not due until 13:30 UTC")
	}
}

func TestNormalizeTimestampsConvertsOffsets(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	if _, err := db.conn.ExecContext(ctx,
		`INSERT INTO users (pubkey_fp, pubkey, created_at) VALUES ('a', 'k', '2026-01-01 08:30:00.5-05:00'), ('b', 'k', '2026-01-01 13:00:00')`); err != nil {
		t.Fatal(err)
	}
	tx, _ := db.conn.BeginTx(ctx, nil)
	if err := normalizeTimestamps(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var first string
	_ = db.conn.QueryRowContext(ctx, `SELECT pubkey_fp FROM users ORDER BY created_at LIMIT 1`).Scan(&first)
	if first != "b" {
		t.Fatalf("13:00 UTC should sort before 08:30 EST (13:30 UTC), got %q first", first)
	}
	u, err := db.GetUserByFingerprint(ctx, "a")
	if err != nil || !u.CreatedAt.Equal(time.Date(2026, 1, 1, 13, 30, 0, 5e8, time.UTC)) {
		t.Fatalf("normalized time reads back as %v, %v", u.CreatedAt, err)
	}
}

func TestGrandfatherSubscriptions(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	user, _ := db.GetOrCreateUser(ctx, "fp", "key")
	_, _ = db.CreateConfig(ctx, user.ID, "old.txt", "old@example.com", "0 8 * * *", false, "raw", time.Now())
	_, _ = db.CreateConfig(ctx, user.ID, "new.txt", "new@example.com", "0 8 * * *", false, "raw", time.Now())
	if _, err := db.EnsurePendingVerification(ctx, user.ID, "new@example.com"); err != nil {
		t.Fatal(err)
	}

	tx, _ := db.conn.BeginTx(ctx, nil)
	if err := grandfatherSubscriptions(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if ok, _ := db.IsEmailVerified(ctx, user.ID, "old@example.com"); !ok {
		t.Error("a subscription with no verification history predates opt-in and should be confirmed")
	}
	if ok, _ := db.IsEmailVerified(ctx, user.ID, "new@example.com"); ok {
		t.Error("a pending confirmation must stay pending")
	}
}
