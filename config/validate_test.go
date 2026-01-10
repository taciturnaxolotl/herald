package config

import (
	"testing"
)

func TestValidate_NoEmail(t *testing.T) {
	cfg := &ParsedConfig{
		CronExpr: "0 8 * * *",
		Feeds:    []FeedEntry{{URL: "https://example.com/feed.xml"}},
	}
	err := Validate(cfg)
	if err != ErrNoEmail {
		t.Errorf("expected ErrNoEmail, got %v", err)
	}
}

func TestValidate_BadEmail(t *testing.T) {
	cfg := &ParsedConfig{
		Email:    "not-an-email",
		CronExpr: "0 8 * * *",
		Feeds:    []FeedEntry{{URL: "https://example.com/feed.xml"}},
	}
	err := Validate(cfg)
	if err != ErrBadEmail {
		t.Errorf("expected ErrBadEmail, got %v", err)
	}
}

func TestValidate_GoodEmail(t *testing.T) {
	validEmails := []string{
		"user@example.com",
		"test.user@example.com",
		"user+tag@example.com",
		"user@sub.example.com",
	}

	for _, email := range validEmails {
		cfg := &ParsedConfig{
			Email:    email,
			CronExpr: "0 8 * * *",
			Feeds:    []FeedEntry{{URL: "https://example.com/feed.xml"}},
		}
		err := Validate(cfg)
		if err != nil {
			t.Errorf("email %s should be valid, got error: %v", email, err)
		}
	}
}

func TestValidate_NoCron(t *testing.T) {
	cfg := &ParsedConfig{
		Email: "user@example.com",
		Feeds: []FeedEntry{{URL: "https://example.com/feed.xml"}},
	}
	err := Validate(cfg)
	if err != ErrNoCron {
		t.Errorf("expected ErrNoCron, got %v", err)
	}
}

func TestValidate_BadCron(t *testing.T) {
	invalidCrons := []string{
		"invalid",
		"* * * *",    // too few fields
		"60 * * * *", // minute out of range
		"* 25 * * *", // hour out of range
	}

	for _, cron := range invalidCrons {
		cfg := &ParsedConfig{
			Email:    "user@example.com",
			CronExpr: cron,
			Feeds:    []FeedEntry{{URL: "https://example.com/feed.xml"}},
		}
		err := Validate(cfg)
		if err != ErrBadCron {
			t.Errorf("cron %q should be invalid, got error: %v", cron, err)
		}
	}
}

func TestValidate_GoodCron(t *testing.T) {
	validCrons := []string{
		"0 8 * * *",
		"*/5 * * * *",
		"0 0 * * 0",
		"0 12 1 * *",
		"30 14 * * 1-5",
	}

	for _, cron := range validCrons {
		cfg := &ParsedConfig{
			Email:    "user@example.com",
			CronExpr: cron,
			Feeds:    []FeedEntry{{URL: "https://example.com/feed.xml"}},
		}
		err := Validate(cfg)
		if err != nil {
			t.Errorf("cron %q should be valid, got error: %v", cron, err)
		}
	}
}

func TestValidate_NoFeeds(t *testing.T) {
	cfg := &ParsedConfig{
		Email:    "user@example.com",
		CronExpr: "0 8 * * *",
		Feeds:    []FeedEntry{},
	}
	err := Validate(cfg)
	if err != ErrNoFeeds {
		t.Errorf("expected ErrNoFeeds, got %v", err)
	}
}

func TestValidate_BadFeedURL(t *testing.T) {
	invalidURLs := []string{
		"not-a-url",
		"://missing-scheme.com",
		"http://",
	}

	for _, url := range invalidURLs {
		cfg := &ParsedConfig{
			Email:    "user@example.com",
			CronExpr: "0 8 * * *",
			Feeds:    []FeedEntry{{URL: url}},
		}
		err := Validate(cfg)
		if err != ErrBadFeedURL {
			t.Errorf("URL %q should be invalid, got error: %v", url, err)
		}
	}
}

func TestValidate_GoodFeedURL(t *testing.T) {
	validURLs := []string{
		"https://example.com/feed.xml",
		"http://example.com/rss",
		"https://sub.example.com/atom.xml",
		"https://example.com:8080/feed",
		"https://example.com/path/to/feed.xml",
	}

	for _, url := range validURLs {
		cfg := &ParsedConfig{
			Email:    "user@example.com",
			CronExpr: "0 8 * * *",
			Feeds:    []FeedEntry{{URL: url}},
		}
		err := Validate(cfg)
		if err != nil {
			t.Errorf("URL %q should be valid, got error: %v", url, err)
		}
	}
}

func TestValidate_MultipleFeeds(t *testing.T) {
	cfg := &ParsedConfig{
		Email:    "user@example.com",
		CronExpr: "0 8 * * *",
		Feeds: []FeedEntry{
			{URL: "https://feed1.com/rss"},
			{URL: "https://feed2.com/atom"},
			{URL: "https://feed3.com/json"},
		},
	}
	err := Validate(cfg)
	if err != nil {
		t.Errorf("valid config failed: %v", err)
	}
}

func TestValidate_CompleteConfig(t *testing.T) {
	cfg := &ParsedConfig{
		Email:    "user@example.com",
		CronExpr: "0 9 * * *",
		Digest:   true,
		Inline:   false,
		Feeds: []FeedEntry{
			{URL: "https://blog.example.com/feed.xml", Name: "Example Blog"},
			{URL: "https://news.example.com/rss"},
		},
	}
	err := Validate(cfg)
	if err != nil {
		t.Errorf("complete valid config failed: %v", err)
	}
}
