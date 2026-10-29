package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

type Feed struct {
	ID           int64
	ConfigID     int64
	URL          string
	Name         sql.NullString
	LastFetched  sql.NullTime
	ETag         sql.NullString
	LastModified sql.NullString
}

const feedCols = `id, config_id, url, name, last_fetched, etag, last_modified`

func scanFeed(s scanner) (*Feed, error) {
	var f Feed
	if err := s.Scan(&f.ID, &f.ConfigID, &f.URL, &f.Name, &f.LastFetched, &f.ETag, &f.LastModified); err != nil {
		return nil, err
	}
	return &f, nil
}

// nullIfEmpty stores "" as NULL.
func nullIfEmpty(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func (q *Queries) CreateFeed(ctx context.Context, configID int64, url, name string) (*Feed, error) {
	result, err := q.q.ExecContext(ctx,
		`INSERT INTO feeds (config_id, url, name) VALUES (?, ?, ?)`,
		configID, url, nullIfEmpty(name),
	)
	if err != nil {
		return nil, fmt.Errorf("insert feed: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get last insert id: %w", err)
	}

	return &Feed{ID: id, ConfigID: configID, URL: url, Name: nullIfEmpty(name)}, nil
}

func (q *Queries) UpdateFeedName(ctx context.Context, feedID int64, name string) error {
	if _, err := q.q.ExecContext(ctx, `UPDATE feeds SET name = ? WHERE id = ?`, nullIfEmpty(name), feedID); err != nil {
		return fmt.Errorf("update feed: %w", err)
	}
	return nil
}

func (q *Queries) DeleteFeed(ctx context.Context, feedID int64) error {
	if _, err := q.q.ExecContext(ctx, `DELETE FROM feeds WHERE id = ?`, feedID); err != nil {
		return fmt.Errorf("delete feed: %w", err)
	}
	return nil
}

func (q *Queries) GetFeedsByConfig(ctx context.Context, configID int64) ([]*Feed, error) {
	feeds, err := queryAll(ctx, q.q, scanFeed,
		`SELECT `+feedCols+` FROM feeds WHERE config_id = ? ORDER BY id`, configID)
	if err != nil {
		return nil, fmt.Errorf("query feeds: %w", err)
	}
	return feeds, nil
}

// GetFeedsByConfigs returns a map of configID to feeds for multiple configs in a single query
func (q *Queries) GetFeedsByConfigs(ctx context.Context, configIDs []int64) (map[int64][]*Feed, error) {
	feedMap := make(map[int64][]*Feed)
	if len(configIDs) == 0 {
		return feedMap, nil
	}

	args := make([]any, len(configIDs))
	for i, id := range configIDs {
		args[i] = id
	}
	feeds, err := queryAll(ctx, q.q, scanFeed,
		`SELECT `+feedCols+` FROM feeds WHERE config_id IN (`+inClause(len(args))+`) ORDER BY config_id, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("query feeds: %w", err)
	}
	for _, f := range feeds {
		feedMap[f.ConfigID] = append(feedMap[f.ConfigID], f)
	}
	return feedMap, nil
}

func (q *Queries) UpdateFeedFetched(ctx context.Context, feedID int64, etag, lastModified string) error {
	_, err := q.q.ExecContext(ctx,
		`UPDATE feeds SET last_fetched = ?, etag = ?, last_modified = ? WHERE id = ?`,
		time.Now().UTC(), nullIfEmpty(etag), nullIfEmpty(lastModified), feedID,
	)
	if err != nil {
		return fmt.Errorf("update feed fetched: %w", err)
	}
	return nil
}
