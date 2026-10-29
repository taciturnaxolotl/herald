package store

import (
	"context"
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
