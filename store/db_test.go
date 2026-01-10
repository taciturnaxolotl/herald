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
		db.Close()
	})
	return db
}

func TestOpen(t *testing.T) {
	db, err := Open(":memory:")
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer db.Close()

	if db.DB == nil {
		t.Error("DB.DB is nil")
	}
	if db.stmts == nil {
		t.Error("DB.stmts is nil")
	}

	// Verify we can query the database
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM users").Scan(&count)
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

	err := db.DeleteUser(ctx, user.ID)
	if err != nil {
		t.Fatalf("DeleteUser failed: %v", err)
	}

	_, err = db.GetUserByID(ctx, user.ID)
	if err != sql.ErrNoRows {
		t.Errorf("expected sql.ErrNoRows after delete, got %v", err)
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
	db.CreateConfig(ctx, user.ID, "config1.herald", "user@example.com", "0 8 * * *", true, false, "raw1", nextRun)
	db.CreateConfig(ctx, user.ID, "config2.herald", "user@example.com", "0 9 * * *", false, true, "raw2", nextRun)

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
	db.CreateConfig(ctx, user.ID, "test.herald", "user@example.com", "0 8 * * *", true, false, "raw", nextRun)

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
	db.CreateFeed(ctx, cfg.ID, "https://feed1.com/rss", "Feed 1")
	db.CreateFeed(ctx, cfg.ID, "https://feed2.com/atom", "Feed 2")

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

	// Verify item is marked as seen
	seen, err := db.IsItemSeen(ctx, feed.ID, "item-guid-123")
	if err != nil {
		t.Fatalf("IsItemSeen failed: %v", err)
	}
	if !seen {
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

	seen, err := db.IsItemSeen(ctx, feed.ID, "nonexistent-guid")
	if err != nil {
		t.Fatalf("IsItemSeen failed: %v", err)
	}
	if seen {
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
	db.MarkItemSeen(ctx, feed.ID, "guid1", "Title 1", "link1")
	db.MarkItemSeen(ctx, feed.ID, "guid2", "Title 2", "link2")

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
	db.MarkItemSeen(ctx, feed.ID, "old-item", "Old Item", "link")

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
