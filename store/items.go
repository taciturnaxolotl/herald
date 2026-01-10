package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type SeenItem struct {
	ID     int64
	FeedID int64
	GUID   string
	Title  sql.NullString
	Link   sql.NullString
	SeenAt time.Time
}

func (db *DB) MarkItemSeen(ctx context.Context, feedID int64, guid, title, link string) error {
	var titleVal, linkVal sql.NullString
	if title != "" {
		titleVal = sql.NullString{String: title, Valid: true}
	}
	if link != "" {
		linkVal = sql.NullString{String: link, Valid: true}
	}

	_, err := db.stmts.markItemSeen.ExecContext(ctx, feedID, guid, titleVal, linkVal)
	if err != nil {
		return fmt.Errorf("mark item seen: %w", err)
	}
	return nil
}

func (db *DB) IsItemSeen(ctx context.Context, feedID int64, guid string) (bool, error) {
	var id int64
	err := db.stmts.isItemSeen.QueryRowContext(ctx, feedID, guid).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("check item seen: %w", err)
	}
	return true, nil
}

func (db *DB) MarkItemSeenTx(ctx context.Context, tx *sql.Tx, feedID int64, guid, title, link string) error {
	var titleVal, linkVal sql.NullString
	if title != "" {
		titleVal = sql.NullString{String: title, Valid: true}
	}
	if link != "" {
		linkVal = sql.NullString{String: link, Valid: true}
	}

	_, err := tx.ExecContext(ctx,
		`INSERT INTO seen_items (feed_id, guid, title, link) VALUES (?, ?, ?, ?)
		 ON CONFLICT(feed_id, guid) DO UPDATE SET title = excluded.title, link = excluded.link`,
		feedID, guid, titleVal, linkVal,
	)
	if err != nil {
		return fmt.Errorf("mark item seen: %w", err)
	}
	return nil
}

func (db *DB) GetSeenItems(ctx context.Context, feedID int64, limit int) ([]*SeenItem, error) {
	rows, err := db.stmts.getSeenItems.QueryContext(ctx, feedID, limit)
	if err != nil {
		return nil, fmt.Errorf("query seen items: %w", err)
	}
	defer rows.Close()

	var items []*SeenItem
	for rows.Next() {
		var item SeenItem
		if err := rows.Scan(&item.ID, &item.FeedID, &item.GUID, &item.Title, &item.Link, &item.SeenAt); err != nil {
			return nil, fmt.Errorf("scan seen item: %w", err)
		}
		items = append(items, &item)
	}
	return items, rows.Err()
}

// GetSeenGUIDs returns a set of GUIDs that have been seen for a given feed
func (db *DB) GetSeenGUIDs(ctx context.Context, feedID int64, guids []string) (map[string]bool, error) {
	if len(guids) == 0 {
		return make(map[string]bool), nil
	}

	// Build the query with the appropriate number of placeholders
	args := make([]interface{}, 0, len(guids)+1)
	args = append(args, feedID)

	placeholders := "?"
	for i := 0; i < len(guids)-1; i++ {
		placeholders += ",?"
	}
	for _, guid := range guids {
		args = append(args, guid)
	}

	query := fmt.Sprintf(
		`SELECT guid FROM seen_items WHERE feed_id = ? AND guid IN (%s)`,
		placeholders,
	)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query seen guids: %w", err)
	}
	defer rows.Close()

	seenSet := make(map[string]bool)
	for rows.Next() {
		var guid string
		if err := rows.Scan(&guid); err != nil {
			return nil, fmt.Errorf("scan guid: %w", err)
		}
		seenSet[guid] = true
	}

	return seenSet, rows.Err()
}

// CleanupOldSeenItems deletes seen items older than the specified duration
func (db *DB) CleanupOldSeenItems(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().Add(-olderThan)
	result, err := db.stmts.cleanupSeenItems.ExecContext(ctx, cutoff)
	if err != nil {
		return 0, fmt.Errorf("cleanup old seen items: %w", err)
	}

	deleted, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("get rows affected: %w", err)
	}

	return deleted, nil
}
