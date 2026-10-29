package store

import (
	"context"
	"database/sql"
	"testing"
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
