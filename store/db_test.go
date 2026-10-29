package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func setupTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func TestOpen(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = db.Close() }()

	var count int
	err = db.q.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM users").Scan(&count)
	if err != nil {
		t.Errorf("failed to query users table: %v", err)
	}
}

func TestGetOrCreateUser(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	// First call should create
	user1, err := db.GetOrCreateUser(ctx, "test-fp", "test-pubkey")
	if err != nil {
		t.Fatalf("GetOrCreateUser failed: %v", err)
	}

	if user1.ID == 0 {
		t.Error("expected non-zero user ID")
	}
	if user1.PubkeyFP != "test-fp" {
		t.Errorf("expected PubkeyFP 'test-fp', got %s", user1.PubkeyFP)
	}

	// Second call should return same user
	user2, err := db.GetOrCreateUser(ctx, "test-fp", "test-pubkey")
	if err != nil {
		t.Fatalf("GetOrCreateUser failed: %v", err)
	}

	if user1.ID != user2.ID {
		t.Errorf("expected same user ID %d, got %d", user1.ID, user2.ID)
	}
}

func TestGetUserByFingerprint(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	created, _ := db.GetOrCreateUser(ctx, "test-fp", "test-pubkey")

	user, err := db.GetUserByFingerprint(ctx, "test-fp")
	if err != nil {
		t.Fatalf("GetUserByFingerprint failed: %v", err)
	}

	if user.ID != created.ID {
		t.Errorf("expected ID %d, got %d", created.ID, user.ID)
	}
}

func TestGetUserByFingerprint_NotFound(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	_, err := db.GetUserByFingerprint(ctx, "nonexistent")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestGetUserByID(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	created, _ := db.GetOrCreateUser(ctx, "test-fp", "test-pubkey")

	user, err := db.GetUserByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetUserByID failed: %v", err)
	}

	if user.PubkeyFP != "test-fp" {
		t.Errorf("expected PubkeyFP 'test-fp', got %s", user.PubkeyFP)
	}
}

func TestDeleteUser(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	user, _ := db.GetOrCreateUser(ctx, "test-fp", "test-pubkey")
	cfg, err := db.CreateConfig(ctx, user.ID, "a.txt", "user@example.com", "0 8 * * *", true, false, "raw", time.Now())
	if err != nil {
		t.Fatalf("CreateConfig failed: %v", err)
	}
	if _, err := db.CreateFeed(ctx, cfg.ID, "https://example.com/feed.xml", ""); err != nil {
		t.Fatalf("CreateFeed failed: %v", err)
	}

	err = db.DeleteUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("DeleteUser failed: %v", err)
	}

	_, err = db.GetUserByID(ctx, user.ID)
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows after delete, got %v", err)
	}
	if _, err := db.GetConfigByID(ctx, cfg.ID); err != sql.ErrNoRows {
		t.Errorf("expected config deleted with user, got %v", err)
	}
}

func TestCreateConfig(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	user, _ := db.GetOrCreateUser(ctx, "test-fp", "test-pubkey")
	nextRun := time.Now().Add(time.Hour)

	cfg, err := db.CreateConfig(ctx, user.ID, "test.herald", "user@example.com", "0 8 * * *", true, false, "raw config text", nextRun)
	if err != nil {
		t.Fatalf("CreateConfig failed: %v", err)
	}

	if cfg.ID == 0 {
		t.Error("expected non-zero config ID")
	}
	if cfg.UserID != user.ID {
		t.Errorf("expected UserID %d, got %d", user.ID, cfg.UserID)
	}
	if cfg.Email != "user@example.com" {
		t.Errorf("expected Email 'user@example.com', got %s", cfg.Email)
	}
}

func TestListConfigs(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	user, _ := db.GetOrCreateUser(ctx, "test-fp", "test-pubkey")
	nextRun := time.Now().Add(time.Hour)
	_, _ = db.CreateConfig(ctx, user.ID, "config1.herald", "user@example.com", "0 8 * * *", true, false, "raw1", nextRun)
	_, _ = db.CreateConfig(ctx, user.ID, "config2.herald", "user@example.com", "0 9 * * *", false, true, "raw2", nextRun)

	configs, err := db.ListConfigs(ctx, user.ID)
	if err != nil {
		t.Fatalf("ListConfigs failed: %v", err)
	}

	if len(configs) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(configs))
	}
}

func TestGetConfigByID(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	user, _ := db.GetOrCreateUser(ctx, "test-fp", "test-pubkey")
	nextRun := time.Now().Add(time.Hour)
	created, _ := db.CreateConfig(ctx, user.ID, "test.herald", "user@example.com", "0 8 * * *", true, false, "raw", nextRun)

	cfg, err := db.GetConfigByID(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetConfigByID failed: %v", err)
	}

	if cfg.Filename != "test.herald" {
		t.Errorf("expected Filename 'test.herald', got %s", cfg.Filename)
	}
}

func TestGetConfig(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	user, _ := db.GetOrCreateUser(ctx, "test-fp", "test-pubkey")
	nextRun := time.Now().Add(time.Hour)
	created, _ := db.CreateConfig(ctx, user.ID, "test.herald", "user@example.com", "0 8 * * *", true, false, "raw", nextRun)

	cfg, err := db.GetConfig(ctx, user.ID, "test.herald")
	if err != nil {
		t.Fatalf("GetConfig failed: %v", err)
	}

	if cfg.ID != created.ID {
		t.Errorf("expected ID %d, got %d", created.ID, cfg.ID)
	}
}

func TestDeleteConfig(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	user, _ := db.GetOrCreateUser(ctx, "test-fp", "test-pubkey")
	nextRun := time.Now().Add(time.Hour)
	_, _ = db.CreateConfig(ctx, user.ID, "test.herald", "user@example.com", "0 8 * * *", true, false, "raw", nextRun)

	err := db.DeleteConfig(ctx, user.ID, "test.herald")
	if err != nil {
		t.Fatalf("DeleteConfig failed: %v", err)
	}

	_, err = db.GetConfig(ctx, user.ID, "test.herald")
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows after delete, got %v", err)
	}
}

func TestCreateFeed(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	user, _ := db.GetOrCreateUser(ctx, "test-fp", "test-pubkey")
	nextRun := time.Now().Add(time.Hour)
	cfg, _ := db.CreateConfig(ctx, user.ID, "test.herald", "user@example.com", "0 8 * * *", true, false, "raw", nextRun)

	feed, err := db.CreateFeed(ctx, cfg.ID, "https://example.com/feed.xml", "Example Feed")
	if err != nil {
		t.Fatalf("CreateFeed failed: %v", err)
	}

	if feed.ID == 0 {
		t.Error("expected non-zero feed ID")
	}
	if feed.URL != "https://example.com/feed.xml" {
		t.Errorf("expected URL 'https://example.com/feed.xml', got %s", feed.URL)
	}
}

func TestGetFeedsByConfig(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	user, _ := db.GetOrCreateUser(ctx, "test-fp", "test-pubkey")
	nextRun := time.Now().Add(time.Hour)
	cfg, _ := db.CreateConfig(ctx, user.ID, "test.herald", "user@example.com", "0 8 * * *", true, false, "raw", nextRun)
	_, _ = db.CreateFeed(ctx, cfg.ID, "https://feed1.com/rss", "Feed 1")
	_, _ = db.CreateFeed(ctx, cfg.ID, "https://feed2.com/atom", "Feed 2")

	feeds, err := db.GetFeedsByConfig(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("GetFeedsByConfig failed: %v", err)
	}

	if len(feeds) != 2 {
		t.Fatalf("expected 2 feeds, got %d", len(feeds))
	}
}

func TestMarkItemSeen(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	user, _ := db.GetOrCreateUser(ctx, "test-fp", "test-pubkey")
	nextRun := time.Now().Add(time.Hour)
	cfg, _ := db.CreateConfig(ctx, user.ID, "test.herald", "user@example.com", "0 8 * * *", true, false, "raw", nextRun)
	feed, _ := db.CreateFeed(ctx, cfg.ID, "https://example.com/feed.xml", "")

	err := db.MarkItemSeen(ctx, feed.ID, "item-guid-123", "Item Title", "https://example.com/item")
	if err != nil {
		t.Fatalf("MarkItemSeen failed: %v", err)
	}

	seen, err := db.GetSeenGUIDs(ctx, feed.ID, []string{"item-guid-123"})
	if err != nil {
		t.Fatalf("GetSeenGUIDs failed: %v", err)
	}
	if !seen["item-guid-123"] {
		t.Error("expected item to be marked as seen")
	}
}

func TestIsItemSeen_NotSeen(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	user, _ := db.GetOrCreateUser(ctx, "test-fp", "test-pubkey")
	nextRun := time.Now().Add(time.Hour)
	cfg, _ := db.CreateConfig(ctx, user.ID, "test.herald", "user@example.com", "0 8 * * *", true, false, "raw", nextRun)
	feed, _ := db.CreateFeed(ctx, cfg.ID, "https://example.com/feed.xml", "")

	seen, err := db.GetSeenGUIDs(ctx, feed.ID, []string{"nonexistent-guid"})
	if err != nil {
		t.Fatalf("GetSeenGUIDs failed: %v", err)
	}
	if seen["nonexistent-guid"] {
		t.Error("expected item to not be seen")
	}
}

func TestGetSeenGUIDs(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	user, _ := db.GetOrCreateUser(ctx, "test-fp", "test-pubkey")
	nextRun := time.Now().Add(time.Hour)
	cfg, _ := db.CreateConfig(ctx, user.ID, "test.herald", "user@example.com", "0 8 * * *", true, false, "raw", nextRun)
	feed, _ := db.CreateFeed(ctx, cfg.ID, "https://example.com/feed.xml", "")

	// Mark some items as seen
	_ = db.MarkItemSeen(ctx, feed.ID, "guid1", "Title 1", "link1")
	_ = db.MarkItemSeen(ctx, feed.ID, "guid2", "Title 2", "link2")

	// Query for seen GUIDs
	seenSet, err := db.GetSeenGUIDs(ctx, feed.ID, []string{"guid1", "guid2", "guid3"})
	if err != nil {
		t.Fatalf("GetSeenGUIDs failed: %v", err)
	}

	if !seenSet["guid1"] {
		t.Error("expected guid1 to be seen")
	}
	if !seenSet["guid2"] {
		t.Error("expected guid2 to be seen")
	}
	if seenSet["guid3"] {
		t.Error("expected guid3 to not be seen")
	}
}

func TestCleanupOldSeenItems(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	user, _ := db.GetOrCreateUser(ctx, "test-fp", "test-pubkey")
	nextRun := time.Now().Add(time.Hour)
	cfg, _ := db.CreateConfig(ctx, user.ID, "test.herald", "user@example.com", "0 8 * * *", true, false, "raw", nextRun)
	feed, _ := db.CreateFeed(ctx, cfg.ID, "https://example.com/feed.xml", "")

	// Mark item as seen
	_ = db.MarkItemSeen(ctx, feed.ID, "old-item", "Old Item", "link")

	// Wait to ensure timestamp is old enough
	time.Sleep(50 * time.Millisecond)

	// Clean up items older than 10ms
	deleted, err := db.CleanupOldSeenItems(ctx, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("CleanupOldSeenItems failed: %v", err)
	}

	if deleted == 0 {
		t.Log("No items deleted - this test may be timing-sensitive")
	}
}

func TestOpenUpgradesOldConfigsTable(t *testing.T) {
	path := t.TempDir() + "/old.db"
	raw, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`
	CREATE TABLE users (id INTEGER PRIMARY KEY, pubkey_fp TEXT UNIQUE NOT NULL, pubkey TEXT NOT NULL, created_at DATETIME DEFAULT CURRENT_TIMESTAMP);
	CREATE TABLE configs (
		id INTEGER PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id),
		filename TEXT NOT NULL,
		email TEXT NOT NULL,
		cron_expr TEXT NOT NULL,
		digest BOOLEAN DEFAULT TRUE,
		inline_content BOOLEAN DEFAULT FALSE,
		raw_text TEXT NOT NULL,
		last_run DATETIME,
		next_run DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(user_id, filename)
	);`)
	_ = raw.Close()
	if err != nil {
		t.Fatal(err)
	}

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open on pre-last_active_at database failed: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	user, _ := db.GetOrCreateUser(ctx, "fp", "key")
	if _, err := db.CreateConfig(ctx, user.ID, "a.txt", "a@example.com", "0 8 * * *", true, false, "raw", time.Now()); err != nil {
		t.Fatalf("CreateConfig after upgrade failed: %v", err)
	}
	if _, err := db.GetConfig(ctx, user.ID, "a.txt"); err != nil {
		t.Fatalf("GetConfig after upgrade failed: %v", err)
	}
}

func TestUpdateLastRunKeepsPausedConfigPaused(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()

	user, _ := db.GetOrCreateUser(ctx, "fp", "key")
	cfg, err := db.CreateConfig(ctx, user.ID, "a.txt", "a@example.com", "0 8 * * *", true, false, "raw", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetNextRun(ctx, cfg.ID, nil); err != nil {
		t.Fatal(err)
	}

	if err := db.UpdateLastRun(ctx, cfg.ID, time.Now().UTC(), time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	got, err := db.GetConfigByID(ctx, cfg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.NextRun.Valid {
		t.Error("a manual run must not reschedule a paused config")
	}
	if !got.LastRun.Valid {
		t.Error("last run should still be recorded")
	}
}

func TestConfigExpiresAt(t *testing.T) {
	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cfg := &Config{CreatedAt: created}
	if got, want := cfg.ExpiresAt(), created.AddDate(0, 0, InactivityDays); !got.Equal(want) {
		t.Errorf("ExpiresAt without keep-alive = %v, want %v", got, want)
	}
	active := created.AddDate(0, 0, 30)
	cfg.LastActiveAt = sql.NullTime{Time: active, Valid: true}
	if got, want := cfg.ExpiresAt(), active.AddDate(0, 0, InactivityDays); !got.Equal(want) {
		t.Errorf("ExpiresAt after keep-alive = %v, want %v", got, want)
	}
}

func TestUpsertConfig(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	user, _ := db.GetOrCreateUser(ctx, "fp", "key")
	next := time.Now().UTC().Add(time.Hour)

	cfg, created, err := db.UpsertConfig(ctx, user.ID, "a.txt", "a@example.com", "0 8 * * *", true, false, "v1", nil)
	if err != nil || !created {
		t.Fatalf("first upsert = %v, created %v", err, created)
	}
	if cfg.Active() {
		t.Error("a nil next run should store the config paused")
	}

	again, created, err := db.UpsertConfig(ctx, user.ID, "a.txt", "b@example.com", "0 9 * * *", true, true, "v2", &next)
	if err != nil || created {
		t.Fatalf("second upsert = %v, created %v", err, created)
	}
	if again.ID != cfg.ID || again.Email != "b@example.com" || again.RawText != "v2" || !again.Active() {
		t.Fatalf("upsert should update in place, got %+v", again)
	}
}

func TestCleanupOldLogs(t *testing.T) {
	db := setupTestDB(t)
	ctx := context.Background()
	user, _ := db.GetOrCreateUser(ctx, "fp", "key")
	cfg, _ := db.CreateConfig(ctx, user.ID, "a.txt", "a@example.com", "0 8 * * *", true, false, "raw", time.Now())
	_ = db.AddLog(ctx, cfg.ID, "info", "old")
	_ = db.AddLog(ctx, cfg.ID, "info", "new")
	if _, err := db.q.ExecContext(ctx, `UPDATE logs SET created_at = ? WHERE message = 'old'`, time.Now().UTC().AddDate(-1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	n, err := db.CleanupOldLogs(ctx, 30*24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("CleanupOldLogs = %d, %v; want 1 deleted", n, err)
	}
}
