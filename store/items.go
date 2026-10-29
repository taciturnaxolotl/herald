package store

import (
	"context"
	"database/sql"
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

func (q *Queries) MarkItemSeen(ctx context.Context, feedID int64, guid, title, link string) error {
	_, err := q.q.ExecContext(ctx,
		`INSERT INTO seen_items (feed_id, guid, title, link) VALUES (?, ?, ?, ?)
		 ON CONFLICT(feed_id, guid) DO UPDATE SET title = excluded.title, link = excluded.link`,
		feedID, guid, nullIfEmpty(title), nullIfEmpty(link),
	)
	if err != nil {
		return fmt.Errorf("mark item seen: %w", err)
	}
	return nil
}

// GetRecentItemsForConfig returns the newest items seen across all of a config's feeds.
func (q *Queries) GetRecentItemsForConfig(ctx context.Context, configID int64, limit int) ([]*SeenItem, error) {
	items, err := queryAll(ctx, q.q, func(s scanner) (*SeenItem, error) {
		var item SeenItem
		if err := s.Scan(&item.ID, &item.FeedID, &item.GUID, &item.Title, &item.Link, &item.SeenAt); err != nil {
			return nil, err
		}
		return &item, nil
	},
		`SELECT si.id, si.feed_id, si.guid, si.title, si.link, si.seen_at
		 FROM seen_items si JOIN feeds f ON f.id = si.feed_id
		 WHERE f.config_id = ?
		 ORDER BY si.seen_at DESC, si.id DESC LIMIT ?`, configID, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent items: %w", err)
	}
	return items, nil
}

// GetSeenGUIDs returns the subset of guids already seen for a feed.
func (q *Queries) GetSeenGUIDs(ctx context.Context, feedID int64, guids []string) (map[string]bool, error) {
	seen := make(map[string]bool)
	if len(guids) == 0 {
		return seen, nil
	}

	args := make([]any, 0, len(guids)+1)
	args = append(args, feedID)
	for _, guid := range guids {
		args = append(args, guid)
	}
	found, err := queryAll(ctx, q.q, func(s scanner) (*string, error) {
		var guid string
		return &guid, s.Scan(&guid)
	}, `SELECT guid FROM seen_items WHERE feed_id = ? AND guid IN (`+inClause(len(guids))+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("query seen guids: %w", err)
	}
	for _, guid := range found {
		seen[*guid] = true
	}
	return seen, nil
}

// CleanupOldSeenItems deletes seen items older than the specified duration
func (q *Queries) CleanupOldSeenItems(ctx context.Context, olderThan time.Duration) (int64, error) {
	result, err := q.q.ExecContext(ctx, `DELETE FROM seen_items WHERE seen_at < ?`, time.Now().UTC().Add(-olderThan))
	if err != nil {
		return 0, fmt.Errorf("cleanup old seen items: %w", err)
	}
	return result.RowsAffected()
}
