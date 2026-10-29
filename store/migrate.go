package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/taciturnaxolotl/herald/config"
)

// A migration moves the schema forward one version. PRAGMA user_version
// records how many have run; databases from before versioning start at 0.
type migration struct {
	name string
	up   func(ctx context.Context, tx *sql.Tx) error
}

// migrations only ever grow at the end: never edit or reorder one that has shipped.
var migrations = []migration{
	{"baseline schema", baselineSchema},
	{"cascade config deletes and drop the unused digest column", rebuildConfigs},
	{"store strip_image_hosts", storeStripImageHosts},
	{"enforce uniqueness and drop redundant indexes", tightenIndexes},
	{"drop never-recorded open and bounce tracking", dropEmailTracking},
}

// migrate applies pending migrations, each in its own transaction. Foreign
// keys are off while they run, since rebuilding a table requires it, and
// every migration must leave PRAGMA foreign_key_check clean.
func (db *DB) migrate(ctx context.Context) error {
	conn, err := db.conn.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()

	var version int
	if err := conn.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if version > len(migrations) {
		return fmt.Errorf("database schema version %d is newer than this build (%d)", version, len(migrations))
	}
	if version == len(migrations) {
		return nil
	}

	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return err
	}
	defer func() { _, _ = conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`) }()

	for i := version; i < len(migrations); i++ {
		if err := runMigration(ctx, conn, i+1, migrations[i]); err != nil {
			return fmt.Errorf("migration %d (%s): %w", i+1, migrations[i].name, err)
		}
	}
	return nil
}

func runMigration(ctx context.Context, conn *sql.Conn, version int, m migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if err := m.up(ctx, tx); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	broken := rows.Next()
	_ = rows.Close()
	if broken {
		return errors.New("left rows violating foreign keys")
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`PRAGMA user_version = %d`, version)); err != nil {
		return err
	}
	return tx.Commit()
}

// baselineSchema is the schema as it stood before versioning. It is written to
// be idempotent so unversioned databases of any age converge on it.
func baselineSchema(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `
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
	`); err != nil {
		return err
	}
	return addColumnIfMissing(ctx, tx, "configs", "last_active_at", "DATETIME")
}

func addColumnIfMissing(ctx context.Context, tx *sql.Tx, table, column, decl string) error {
	var n int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column,
	).Scan(&n); err != nil {
		return fmt.Errorf("inspect %s: %w", table, err)
	}
	if n > 0 {
		return nil
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, decl)); err != nil {
		return fmt.Errorf("add %s.%s: %w", table, column, err)
	}
	return nil
}

// rebuildConfigs makes deleting a user delete their configs, and drops the
// digest column nothing reads. SQLite cannot change a foreign key in place.
func rebuildConfigs(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
	CREATE TABLE configs_new (
		id INTEGER PRIMARY KEY,
		user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
		filename TEXT NOT NULL,
		email TEXT NOT NULL,
		cron_expr TEXT NOT NULL,
		inline_content BOOLEAN DEFAULT FALSE,
		raw_text TEXT NOT NULL,
		last_run DATETIME,
		next_run DATETIME,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		last_active_at DATETIME,
		UNIQUE(user_id, filename)
	);
	INSERT INTO configs_new (id, user_id, filename, email, cron_expr, inline_content, raw_text, last_run, next_run, created_at, last_active_at)
		SELECT id, user_id, filename, email, cron_expr, inline_content, raw_text, last_run, next_run, created_at, last_active_at FROM configs;
	DROP TABLE configs;
	ALTER TABLE configs_new RENAME TO configs;
	CREATE INDEX idx_configs_active_next_run ON configs(next_run) WHERE next_run IS NOT NULL;
	`)
	return err
}

// storeStripImageHosts saves each config's strip_image_hosts so sends need not
// re-parse the raw config text.
func storeStripImageHosts(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, `ALTER TABLE configs ADD COLUMN strip_image_hosts TEXT NOT NULL DEFAULT ''`); err != nil {
		return err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, raw_text FROM configs`)
	if err != nil {
		return err
	}
	hosts := map[int64]string{}
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			_ = rows.Close()
			return err
		}
		// Saved configs may predate strict parsing, so take whatever parses.
		if parsed, _ := config.Parse(raw); len(parsed.StripImageHosts) > 0 {
			hosts[id] = joinHosts(parsed.StripImageHosts)
		}
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for id, h := range hosts {
		if _, err := tx.ExecContext(ctx, `UPDATE configs SET strip_image_hosts = ? WHERE id = ?`, h, id); err != nil {
			return err
		}
	}
	return nil
}

// tightenIndexes makes a config list each feed URL once and own one
// unsubscribe token, and drops indexes a UNIQUE constraint already provides.
// Foreign keys are off here, so duplicate feeds' history is removed by hand.
func tightenIndexes(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
	DELETE FROM seen_items WHERE feed_id IN (
		SELECT id FROM feeds WHERE id NOT IN (SELECT MIN(id) FROM feeds GROUP BY config_id, url));
	DELETE FROM feeds WHERE id NOT IN (SELECT MIN(id) FROM feeds GROUP BY config_id, url);
	DELETE FROM unsubscribe_tokens WHERE id NOT IN (SELECT MIN(id) FROM unsubscribe_tokens GROUP BY config_id);

	CREATE UNIQUE INDEX idx_feeds_config_url ON feeds(config_id, url);
	CREATE UNIQUE INDEX idx_unsubscribe_tokens_config ON unsubscribe_tokens(config_id);

	DROP INDEX IF EXISTS idx_feeds_config_id;
	DROP INDEX IF EXISTS idx_configs_user_id;
	DROP INDEX IF EXISTS idx_seen_items_feed_id;
	DROP INDEX IF EXISTS idx_unsubscribe_tokens_token;
	DROP INDEX IF EXISTS idx_verified_emails_user_email;
	DROP INDEX IF EXISTS idx_verified_emails_token;
	DROP INDEX IF EXISTS idx_email_sends_tracking_token;
	`)
	return err
}

// dropEmailTracking removes open and bounce columns that nothing ever wrote.
func dropEmailTracking(ctx context.Context, tx *sql.Tx) error {
	for _, col := range []string{"bounced", "bounce_reason", "opened", "opened_at"} {
		if _, err := tx.ExecContext(ctx, `ALTER TABLE email_sends DROP COLUMN `+col); err != nil {
			return err
		}
	}
	return nil
}
