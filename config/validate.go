package config

import (
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/adhocore/gronx"
	"github.com/taciturnaxolotl/herald/safehttp"
)

var (
	ErrNoEmail    = errors.New("email is required")
	ErrBadEmail   = errors.New("invalid email format")
	ErrNoCron     = errors.New("cron expression is required")
	ErrBadCron    = errors.New("invalid cron expression")
	ErrNoFeeds    = errors.New("at least one feed URL is required")
	ErrBadFeedURL = errors.New("invalid feed URL")
)

// MaxFeeds caps feeds per config, since every upload fetches each one.
const MaxFeeds = 200

var ErrTooManyFeeds = fmt.Errorf("too many feeds (max %d per config)", MaxFeeds)

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

	cfg.Feeds = dedupeFeeds(cfg.Feeds)
	if len(cfg.Feeds) == 0 {
		return ErrNoFeeds
	}

	if len(cfg.Feeds) > MaxFeeds {
		return ErrTooManyFeeds
	}

	var errs FeedErrors
	for _, feed := range cfg.Feeds {
		if err := safehttp.ValidateURL(feed.URL); err != nil {
			errs = append(errs, FeedError{URL: feed.URL, Err: fmt.Errorf("%w: %w", ErrBadFeedURL, err)})
		}
	}
	if errs != nil {
		return errs
	}
	return nil
}

// dedupeFeeds keeps the first entry for each URL, so a feed listed twice is fetched and mailed once.
func dedupeFeeds(feeds []FeedEntry) []FeedEntry {
	seen := make(map[string]bool, len(feeds))
	var out []FeedEntry
	for _, f := range feeds {
		if !seen[f.URL] {
			seen[f.URL] = true
			out = append(out, f)
		}
	}
	return out
}

// FeedError records why a single feed URL could not be used.
type FeedError struct {
	URL string
	Err error
}

func (e FeedError) Error() string { return e.URL + ": " + e.Err.Error() }
func (e FeedError) Unwrap() error { return e.Err }

// FeedErrors lists every unusable feed in config order, so one upload reports them all.
type FeedErrors []FeedError

func (e FeedErrors) Unwrap() []error {
	errs := make([]error, len(e))
	for i, fe := range e {
		errs[i] = fe
	}
	return errs
}

func (e FeedErrors) Error() string {
	parts := make([]string, len(e))
	for i, fe := range e {
		parts[i] = fe.Error()
	}
	return fmt.Sprintf("%d feed(s) failed validation: %s", len(e), strings.Join(parts, "; "))
}

// NextRun returns the first cron tick strictly after after, in UTC.
func NextRun(cronExpr string, after time.Time) (time.Time, error) {
	return gronx.NextTickAfter(cronExpr, after.UTC(), true)
}
