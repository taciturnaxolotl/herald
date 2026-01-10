// Package scheduler provides functionality for Herald.
package scheduler

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kierank/herald/store"
	"github.com/mmcdole/gofeed"
)

const (
	feedFetchTimeout   = 15 * time.Second
	maxConcurrentFetch = 30
)

type FetchResult struct {
	FeedID       int64
	FeedName     string
	FeedURL      string
	Items        []FetchedItem
	ETag         string
	LastModified string
	Error        error
}

type FetchedItem struct {
	GUID      string
	Title     string
	Link      string
	Content   string
	Published time.Time
}

func FetchFeed(ctx context.Context, feed *store.Feed) *FetchResult {
	result := &FetchResult{
		FeedID:  feed.ID,
		FeedURL: feed.URL,
	}

	if feed.Name.Valid {
		result.FeedName = feed.Name.String
	}

	ctx, cancel := context.WithTimeout(ctx, feedFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, feed.URL, nil)
	if err != nil {
		result.Error = err
		return result
	}

	req.Header.Set("User-Agent", "Herald/1.0 (RSS Aggregator)")

	if feed.ETag.Valid && feed.ETag.String != "" {
		req.Header.Set("If-None-Match", feed.ETag.String)
	}
	if feed.LastModified.Valid && feed.LastModified.String != "" {
		req.Header.Set("If-Modified-Since", feed.LastModified.String)
	}

	client := &http.Client{
		Timeout: 15 * time.Second,
	}

	resp, err := client.Do(req)
	if err != nil {
		result.Error = err
		return result
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNotModified {
		return result
	}

	if resp.StatusCode != http.StatusOK {
		result.Error = &httpError{StatusCode: resp.StatusCode}
		return result
	}

	result.ETag = resp.Header.Get("ETag")
	result.LastModified = resp.Header.Get("Last-Modified")

	parser := gofeed.NewParser()
	parsedFeed, err := parser.Parse(resp.Body)
	if err != nil {
		result.Error = err
		return result
	}

	if result.FeedName == "" && parsedFeed.Title != "" {
		result.FeedName = parsedFeed.Title
	}

	for _, item := range parsedFeed.Items {
		fetchedItem := FetchedItem{
			GUID:  item.GUID,
			Title: item.Title,
			Link:  item.Link,
		}

		if fetchedItem.GUID == "" {
			fetchedItem.GUID = item.Link
		}

		if item.Content != "" {
			fetchedItem.Content = item.Content
		} else if item.Description != "" {
			fetchedItem.Content = item.Description
		}

		if item.PublishedParsed != nil {
			fetchedItem.Published = *item.PublishedParsed
		} else if item.UpdatedParsed != nil {
			fetchedItem.Published = *item.UpdatedParsed
		}

		result.Items = append(result.Items, fetchedItem)
	}

	return result
}

func FetchFeeds(ctx context.Context, feeds []*store.Feed, progress *atomic.Int32) []*FetchResult {
	results := make([]*FetchResult, len(feeds))
	var wg sync.WaitGroup

	concurrent := maxConcurrentFetch
	if len(feeds) < concurrent {
		concurrent = len(feeds)
	}
	semaphore := make(chan struct{}, concurrent)

	for i, feed := range feeds {
		wg.Add(1)
		go func(idx int, f *store.Feed) {
			defer func() {
				if progress != nil {
					progress.Add(1)
				}
				wg.Done()
			}()
			semaphore <- struct{}{}        // Acquire
			defer func() { <-semaphore }() // Release
			results[idx] = FetchFeed(ctx, f)
		}(i, feed)
	}

	wg.Wait()
	return results
}

type httpError struct {
	StatusCode int
}

func (e *httpError) Error() string {
	return http.StatusText(e.StatusCode)
}
