package store

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	*sql.DB
	stmts *preparedStmts
}

type preparedStmts struct {
	markItemSeen     *sql.Stmt
	isItemSeen       *sql.Stmt
	getSeenItems     *sql.Stmt
	getConfig        *sql.Stmt
	updateConfigRun  *sql.Stmt
	updateFeedMeta   *sql.Stmt
	cleanupSeenItems *sql.Stmt
}

func Open(path string) (*DB, error) {
	db, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// SQLite works best with single writer for WAL mode
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}

	store := &DB{DB: db}
	if err := store.migrate(); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	if err := store.prepareStatements(); err != nil {
		return nil, fmt.Errorf("prepare statements: %w", err)
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

	CREATE TABLE IF NOT EXISTS email_sends (
		id INTEGER PRIMARY KEY,
		config_id INTEGER NOT NULL REFERENCES configs(id) ON DELETE CASCADE,
		recipient TEXT NOT NULL,
		subject TEXT NOT NULL,
		tracking_token TEXT UNIQUE,
		sent_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		bounced BOOLEAN DEFAULT FALSE,
		bounce_reason TEXT,
		opened BOOLEAN DEFAULT FALSE,
		opened_at DATETIME
	);

	CREATE INDEX IF NOT EXISTS idx_configs_user_id ON configs(user_id);
	CREATE INDEX IF NOT EXISTS idx_configs_active_next_run ON configs(next_run) WHERE next_run IS NOT NULL;
	CREATE INDEX IF NOT EXISTS idx_feeds_config_id ON feeds(config_id);
	CREATE INDEX IF NOT EXISTS idx_seen_items_feed_id ON seen_items(feed_id);
	CREATE INDEX IF NOT EXISTS idx_logs_config_id ON logs(config_id);
	CREATE INDEX IF NOT EXISTS idx_logs_created_at ON logs(created_at);
	CREATE INDEX IF NOT EXISTS idx_unsubscribe_tokens_token ON unsubscribe_tokens(token);
	CREATE INDEX IF NOT EXISTS idx_email_sends_config_id ON email_sends(config_id);
	CREATE INDEX IF NOT EXISTS idx_email_sends_tracking_token ON email_sends(tracking_token);
	CREATE INDEX IF NOT EXISTS idx_email_sends_sent_at ON email_sends(sent_at);
	`

	_, err := db.Exec(schema)
	return err
}

func (db *DB) Close() error {
	if db.stmts != nil {
		_ = db.stmts.markItemSeen.Close()
		_ = db.stmts.isItemSeen.Close()
		_ = db.stmts.getSeenItems.Close()
		_ = db.stmts.getConfig.Close()
		_ = db.stmts.updateConfigRun.Close()
		_ = db.stmts.updateFeedMeta.Close()
		_ = db.stmts.cleanupSeenItems.Close()
	}
	return db.DB.Close()
}

func (db *DB) prepareStatements() error {
	db.stmts = &preparedStmts{}

	var err error

	db.stmts.markItemSeen, err = db.Prepare(
		`INSERT INTO seen_items (feed_id, guid, title, link) VALUES (?, ?, ?, ?)
		 ON CONFLICT(feed_id, guid) DO UPDATE SET title = excluded.title, link = excluded.link`)
	if err != nil {
		return fmt.Errorf("prepare markItemSeen: %w", err)
	}

	db.stmts.isItemSeen, err = db.Prepare(
		`SELECT id FROM seen_items WHERE feed_id = ? AND guid = ?`)
	if err != nil {
		return fmt.Errorf("prepare isItemSeen: %w", err)
	}

	db.stmts.getSeenItems, err = db.Prepare(
		`SELECT id, feed_id, guid, title, link, seen_at
		 FROM seen_items WHERE feed_id = ? ORDER BY seen_at DESC LIMIT ?`)
	if err != nil {
		return fmt.Errorf("prepare getSeenItems: %w", err)
	}

	db.stmts.getConfig, err = db.Prepare(
		`SELECT id, user_id, filename, email, cron_expr, digest, inline_content, raw_text, last_run, next_run, created_at
		 FROM configs WHERE user_id = ? AND filename = ?`)
	if err != nil {
		return fmt.Errorf("prepare getConfig: %w", err)
	}

	db.stmts.updateConfigRun, err = db.Prepare(
		`UPDATE configs SET last_run = ?, next_run = ? WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("prepare updateConfigRun: %w", err)
	}

	db.stmts.updateFeedMeta, err = db.Prepare(
		`UPDATE feeds SET last_fetched = ?, etag = ?, last_modified = ? WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("prepare updateFeedMeta: %w", err)
	}

	db.stmts.cleanupSeenItems, err = db.Prepare(
		`DELETE FROM seen_items WHERE seen_at < ?`)
	if err != nil {
		return fmt.Errorf("prepare cleanupSeenItems: %w", err)
	}

	return nil
}

func (db *DB) Migrate() error {
	return db.migrate()
}

func (db *DB) BeginTx(ctx context.Context) (*sql.Tx, error) {
	return db.DB.BeginTx(ctx, nil)
}
