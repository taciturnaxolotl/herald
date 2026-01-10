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

	_, err := db.ExecContext(ctx,
		`INSERT INTO seen_items (feed_id, guid, title, link) VALUES (?, ?, ?, ?)
		 ON CONFLICT(feed_id, guid) DO UPDATE SET title = excluded.title, link = excluded.link`,
		feedID, guid, titleVal, linkVal,
	)
	if err != nil {
		return fmt.Errorf("mark item seen: %w", err)
	}
	return nil
}

func (db *DB) IsItemSeen(ctx context.Context, feedID int64, guid string) (bool, error) {
	var id int64
	err := db.QueryRowContext(ctx,
		`SELECT id FROM seen_items WHERE feed_id = ? AND guid = ?`,
		feedID, guid,
	).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("check item seen: %w", err)
	}
	return true, nil
}

func (db *DB) GetSeenItems(ctx context.Context, feedID int64, limit int) ([]*SeenItem, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, feed_id, guid, title, link, seen_at
		 FROM seen_items WHERE feed_id = ? ORDER BY seen_at DESC LIMIT ?`,
		feedID, limit,
	)
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
