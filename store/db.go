package store

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	*sql.DB
}

func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}

	store := &DB{db}
	if err := store.migrate(); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	return store, nil
}

func (db *DB) migrate() error {
	schema := `
	CREATE TABLE IF NOT EXISTS users (
		id INTEGER PRIMARY KEY,
		pubkey_fp TEXT UNIQUE NOT NULL,
		pubkey TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS configs (
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
	);

	CREATE TABLE IF NOT EXISTS feeds (
		id INTEGER PRIMARY KEY,
		config_id INTEGER NOT NULL REFERENCES configs(id) ON DELETE CASCADE,
		url TEXT NOT NULL,
		name TEXT,
		last_fetched DATETIME,
		etag TEXT,
		last_modified TEXT
	);

	CREATE TABLE IF NOT EXISTS seen_items (
		id INTEGER PRIMARY KEY,
		feed_id INTEGER NOT NULL REFERENCES feeds(id) ON DELETE CASCADE,
		guid TEXT NOT NULL,
		title TEXT,
		link TEXT,
		seen_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(feed_id, guid)
	);

	CREATE TABLE IF NOT EXISTS logs (
		id INTEGER PRIMARY KEY,
		config_id INTEGER NOT NULL REFERENCES configs(id) ON DELETE CASCADE,
		message TEXT NOT NULL,
		level TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS unsubscribe_tokens (
		id INTEGER PRIMARY KEY,
		token TEXT UNIQUE NOT NULL,
		config_id INTEGER NOT NULL REFERENCES configs(id) ON DELETE CASCADE,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_configs_user_id ON configs(user_id);
	CREATE INDEX IF NOT EXISTS idx_configs_next_run ON configs(next_run);
	CREATE INDEX IF NOT EXISTS idx_feeds_config_id ON feeds(config_id);
	CREATE INDEX IF NOT EXISTS idx_seen_items_feed_id ON seen_items(feed_id);
	CREATE INDEX IF NOT EXISTS idx_logs_config_id ON logs(config_id);
	CREATE INDEX IF NOT EXISTS idx_logs_created_at ON logs(created_at);
	CREATE INDEX IF NOT EXISTS idx_unsubscribe_tokens_token ON unsubscribe_tokens(token);
	`

	_, err := db.Exec(schema)
	return err
}

func (db *DB) Close() error {
	return db.DB.Close()
}

func (db *DB) Migrate(ctx context.Context) error {
	return db.migrate()
}

func (db *DB) BeginTx(ctx context.Context) (*sql.Tx, error) {
	return db.DB.BeginTx(ctx, nil)
}
