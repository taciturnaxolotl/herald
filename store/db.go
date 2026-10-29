package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

// querier is satisfied by both *sql.DB and *sql.Tx, so every query is written once.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Queries holds every data operation; run them on the DB directly or inside WithTx.
type Queries struct {
	q querier
}

type DB struct {
	Queries
	conn *sql.DB
}

func Open(path string) (*DB, error) {
	conn, err := sql.Open("sqlite3", path+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// SQLite works best with single writer for WAL mode
	conn.SetMaxOpenConns(1)
	conn.SetMaxIdleConns(1)

	if err := conn.Ping(); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}

	db := &DB{Queries: Queries{q: conn}, conn: conn}
	if err := db.migrate(); err != nil {
		return nil, fmt.Errorf("migrate database: %w", err)
	}
	return db, nil
}

// WithTx runs fn in a transaction, committing only if fn returns nil.
func (db *DB) WithTx(ctx context.Context, fn func(*Queries) error) error {
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(&Queries{q: tx}); err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) Close() error {
	return db.conn.Close()
}

// scanner is the shared shape of *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

// queryAll runs a query and scans every row with scan.
func queryAll[T any](ctx context.Context, q querier, scan func(scanner) (*T, error), query string, args ...any) ([]*T, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []*T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// inClause returns "?,?,..." with n placeholders.
func inClause(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
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
		last_active_at DATETIME,
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

	-- Confirmed opt-in: a config only sends once the (user, destination email)
	-- pair has been verified by whoever controls the mailbox. verified_at NULL
	-- means a confirmation is still pending. last_sent_at drives a durable
	-- per-address cooldown so the confirmation prompt itself cannot be abused.
	CREATE TABLE IF NOT EXISTS verified_emails (
		id INTEGER PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		email TEXT NOT NULL,
		verified_at DATETIME,
		token TEXT UNIQUE,
		token_created_at DATETIME,
		last_sent_at DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		UNIQUE(user_id, email)
	);

	CREATE INDEX IF NOT EXISTS idx_configs_user_id ON configs(user_id);
	CREATE INDEX IF NOT EXISTS idx_configs_active_next_run ON configs(next_run) WHERE next_run IS NOT NULL;
	CREATE INDEX IF NOT EXISTS idx_feeds_config_id ON feeds(config_id);
	CREATE INDEX IF NOT EXISTS idx_seen_items_feed_id ON seen_items(feed_id);
	CREATE INDEX IF NOT EXISTS idx_logs_config_id ON logs(config_id);
	CREATE INDEX IF NOT EXISTS idx_logs_created_at ON logs(created_at);
	CREATE INDEX IF NOT EXISTS idx_unsubscribe_tokens_token ON unsubscribe_tokens(token);
	CREATE INDEX IF NOT EXISTS idx_verified_emails_user_email ON verified_emails(user_id, email);
	CREATE INDEX IF NOT EXISTS idx_verified_emails_token ON verified_emails(token);
	CREATE INDEX IF NOT EXISTS idx_verified_emails_email ON verified_emails(email);
	CREATE INDEX IF NOT EXISTS idx_email_sends_config_id ON email_sends(config_id);
	CREATE INDEX IF NOT EXISTS idx_email_sends_tracking_token ON email_sends(tracking_token);
	CREATE INDEX IF NOT EXISTS idx_email_sends_sent_at ON email_sends(sent_at);
	`

	if _, err := db.conn.Exec(schema); err != nil {
		return err
	}

	for _, c := range addedColumns {
		if err := db.ensureColumn(c.table, c.column, c.decl); err != nil {
			return err
		}
	}
	return nil
}

// addedColumns lists columns introduced after a table first shipped, since
// CREATE TABLE IF NOT EXISTS never alters an existing table.
var addedColumns = []struct{ table, column, decl string }{
	{"configs", "last_active_at", "DATETIME"},
}

func (db *DB) ensureColumn(table, column, decl string) error {
	rows, err := db.conn.Query(fmt.Sprintf("SELECT name FROM pragma_table_info('%s')", table))
	if err != nil {
		return fmt.Errorf("inspect %s: %w", table, err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_ = rows.Close()

	if _, err := db.conn.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, decl)); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
}
