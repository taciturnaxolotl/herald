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

func (db *DB) CreateFeed(ctx context.Context, configID int64, url, name string) (*Feed, error) {
	var nameVal sql.NullString
	if name != "" {
		nameVal = sql.NullString{String: name, Valid: true}
	}

	result, err := db.ExecContext(ctx,
		`INSERT INTO feeds (config_id, url, name) VALUES (?, ?, ?)`,
		configID, url, nameVal,
	)
	if err != nil {
		return nil, fmt.Errorf("insert feed: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get last insert id: %w", err)
	}

	return &Feed{
		ID:       id,
		ConfigID: configID,
		URL:      url,
		Name:     nameVal,
	}, nil
}

func (db *DB) CreateFeedTx(ctx context.Context, tx *sql.Tx, configID int64, url, name string) (*Feed, error) {
	var nameVal sql.NullString
	if name != "" {
		nameVal = sql.NullString{String: name, Valid: true}
	}

	result, err := tx.ExecContext(ctx,
		`INSERT INTO feeds (config_id, url, name) VALUES (?, ?, ?)`,
		configID, url, nameVal,
	)
	if err != nil {
		return nil, fmt.Errorf("insert feed: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get last insert id: %w", err)
	}

	return &Feed{
		ID:       id,
		ConfigID: configID,
		URL:      url,
		Name:     nameVal,
	}, nil
}

func (db *DB) GetFeedsByConfig(ctx context.Context, configID int64) ([]*Feed, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, config_id, url, name, last_fetched, etag, last_modified
		 FROM feeds WHERE config_id = ? ORDER BY id`,
		configID,
	)
	if err != nil {
		return nil, fmt.Errorf("query feeds: %w", err)
	}
	defer rows.Close()

	var feeds []*Feed
	for rows.Next() {
		var f Feed
		if err := rows.Scan(&f.ID, &f.ConfigID, &f.URL, &f.Name, &f.LastFetched, &f.ETag, &f.LastModified); err != nil {
			return nil, fmt.Errorf("scan feed: %w", err)
		}
		feeds = append(feeds, &f)
	}
	return feeds, rows.Err()
}

// GetFeedsByConfigs returns a map of configID to feeds for multiple configs in a single query
func (db *DB) GetFeedsByConfigs(ctx context.Context, configIDs []int64) (map[int64][]*Feed, error) {
	if len(configIDs) == 0 {
		return make(map[int64][]*Feed), nil
	}

	// Build the query with the appropriate number of placeholders
	args := make([]interface{}, len(configIDs))
	placeholders := "?"
	for i := 0; i < len(configIDs)-1; i++ {
		placeholders += ",?"
	}
	for i, id := range configIDs {
		args[i] = id
	}

	query := fmt.Sprintf(
		`SELECT id, config_id, url, name, last_fetched, etag, last_modified
		 FROM feeds WHERE config_id IN (%s) ORDER BY config_id, id`,
		placeholders,
	)

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("query feeds: %w", err)
	}
	defer rows.Close()

	feedMap := make(map[int64][]*Feed)
	for rows.Next() {
		var f Feed
		if err := rows.Scan(&f.ID, &f.ConfigID, &f.URL, &f.Name, &f.LastFetched, &f.ETag, &f.LastModified); err != nil {
			return nil, fmt.Errorf("scan feed: %w", err)
		}
		feedMap[f.ConfigID] = append(feedMap[f.ConfigID], &f)
	}
	
	return feedMap, rows.Err()
}

func (db *DB) UpdateFeedFetched(ctx context.Context, feedID int64, etag, lastModified string) error {
	var etagVal, lmVal sql.NullString
	if etag != "" {
		etagVal = sql.NullString{String: etag, Valid: true}
	}
	if lastModified != "" {
		lmVal = sql.NullString{String: lastModified, Valid: true}
	}

	_, err := db.ExecContext(ctx,
		`UPDATE feeds SET last_fetched = ?, etag = ?, last_modified = ? WHERE id = ?`,
		time.Now(), etagVal, lmVal, feedID,
	)
	if err != nil {
		return fmt.Errorf("update feed fetched: %w", err)
	}
	return nil
}

func (db *DB) DeleteFeedsByConfig(ctx context.Context, configID int64) error {
	_, err := db.ExecContext(ctx,
		`DELETE FROM feeds WHERE config_id = ?`,
		configID,
	)
	if err != nil {
		return fmt.Errorf("delete feeds: %w", err)
	}
	return nil
}
