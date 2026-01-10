package config

import (
	"errors"
	"net/mail"
	"net/url"

	"github.com/adhocore/gronx"
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
	if _, err := mail.ParseAddress(cfg.Email); err != nil {
		return ErrBadEmail
	}

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
		u, err := url.Parse(feed.URL)
		if err != nil || u.Scheme == "" || u.Host == "" {
			return ErrBadFeedURL
		}
	}

	return nil
}
