package config

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"time"

	"github.com/adhocore/gronx"
	"github.com/kierank/herald/safehttp"
	"github.com/mmcdole/gofeed"
)

var (
	ErrNoEmail    = errors.New("email is required")
	ErrBadEmail   = errors.New("invalid email format")
	ErrNoCron     = errors.New("cron expression is required")
	ErrBadCron    = errors.New("invalid cron expression")
	ErrNoFeeds    = errors.New("at least one feed URL is required")
	ErrBadFeedURL = errors.New("invalid feed URL")
)

func Validate(cfg *ParsedConfig) error {
	if cfg.Email == "" {
		return ErrNoEmail
	}
	a, err := mail.ParseAddress(cfg.Email)
	if err != nil {
		return ErrBadEmail
	}
	cfg.Email = a.Address

	if cfg.CronExpr == "" {
		return ErrNoCron
	}
	gron := gronx.New()
	if !gron.IsValid(cfg.CronExpr) {
		return ErrBadCron
	}

	if len(cfg.Feeds) == 0 {
		return ErrNoFeeds
	}

	for _, feed := range cfg.Feeds {
		if err := safehttp.ValidateURL(feed.URL); err != nil {
			return fmt.Errorf("%w: %w", ErrBadFeedURL, err)
		}
	}

	return nil
}

// ValidateFeedURLs attempts to fetch and parse each feed URL with a short timeout
func ValidateFeedURLs(ctx context.Context, cfg *ParsedConfig) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	parser := gofeed.NewParser()
	client := safehttp.Client(5 * time.Second)

	for _, feed := range cfg.Feeds {
		if err := safehttp.ValidateURL(feed.URL); err != nil {
			return fmt.Errorf("invalid feed URL %s: %w", feed.URL, err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, feed.URL, nil)
		if err != nil {
			return fmt.Errorf("invalid feed URL %s: %w", feed.URL, err)
		}

		req.Header.Set("User-Agent", "Herald/1.0 (RSS Aggregator)")

		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("failed to fetch feed %s: %w", feed.URL, err)
		}

		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return fmt.Errorf("feed %s returned status %d", feed.URL, resp.StatusCode)
		}

		_, err = parser.Parse(io.LimitReader(resp.Body, safehttp.MaxBodyBytes))
		_ = resp.Body.Close()
		if err != nil {
			return fmt.Errorf("failed to parse feed %s: %w", feed.URL, err)
		}
	}

	return nil
}
