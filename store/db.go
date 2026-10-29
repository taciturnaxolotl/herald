package store

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// querier is satisfied by both *sql.DB and *sql.Tx, so every query is written once.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// utcQuerier binds every time in UTC. SQLite compares timestamps as text, so
// one stray local-time offset would sort wrongly against everything else.
type utcQuerier struct{ querier }

func (u utcQuerier) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return u.querier.ExecContext(ctx, query, utcArgs(args)...)
}

func (u utcQuerier) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return u.querier.QueryContext(ctx, query, utcArgs(args)...)
}

func (u utcQuerier) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return u.querier.QueryRowContext(ctx, query, utcArgs(args)...)
}

func utcArgs(args []any) []any {
	var out []any
	for i, arg := range args {
		var utc any
		switch v := arg.(type) {
		case time.Time:
			utc = v.UTC()
		case *time.Time:
			if v == nil {
				continue
			}
			t := v.UTC()
			utc = &t
		case sql.NullTime:
			v.Time = v.Time.UTC()
			utc = v
		default:
			continue
		}
		if out == nil {
			out = append([]any(nil), args...)
		}
		out[i] = utc
	}
	if out == nil {
		return args
	}
	return out
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

	db := &DB{Queries: Queries{q: utcQuerier{conn}}, conn: conn}
	if err := db.migrate(context.Background()); err != nil {
		_ = conn.Close()
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

	if err := fn(&Queries{q: utcQuerier{tx}}); err != nil {
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
