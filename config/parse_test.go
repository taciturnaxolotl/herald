package config

import (
	"testing"
)

func TestParse_Empty(t *testing.T) {
	cfg, err := Parse("")
	if err != nil {
		t.Fatalf("Parse(\"\") failed: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if !cfg.Digest {
		t.Error("expected Digest default to be true")
	}
	if cfg.Inline {
		t.Error("expected Inline default to be false")
	}
	if len(cfg.Feeds) != 0 {
		t.Errorf("expected 0 feeds, got %d", len(cfg.Feeds))
	}
}

func TestParse_Comments(t *testing.T) {
	input := `
# This is a comment
# Another comment

=: email test@example.com
`
	cfg, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if cfg.Email != "test@example.com" {
		t.Errorf("expected email test@example.com, got %s", cfg.Email)
	}
}

func TestParse_EmailDirective(t *testing.T) {
	input := "=: email user@example.com"
	cfg, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if cfg.Email != "user@example.com" {
		t.Errorf("expected email user@example.com, got %s", cfg.Email)
	}
}

func TestParse_CronDirective(t *testing.T) {
	input := "=: cron 0 8 * * *"
	cfg, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if cfg.CronExpr != "0 8 * * *" {
		t.Errorf("expected cron '0 8 * * *', got %s", cfg.CronExpr)
	}
}

func TestParse_DigestDirective(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"=: digest true", true},
		{"=: digest false", false},
		{"=: digest 1", true},
		{"=: digest 0", false},
		{"=: digest invalid", true}, // default
	}

	for _, tt := range tests {
		cfg, err := Parse(tt.input)
		if err != nil {
			t.Fatalf("Parse(%q) failed: %v", tt.input, err)
		}
		if cfg.Digest != tt.expected {
			t.Errorf("Parse(%q): expected Digest=%v, got %v", tt.input, tt.expected, cfg.Digest)
		}
	}
}

func TestParse_InlineDirective(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"=: inline true", true},
		{"=: inline false", false},
		{"=: inline 1", true},
		{"=: inline 0", false},
		{"=: inline invalid", false}, // default
	}

	for _, tt := range tests {
		cfg, err := Parse(tt.input)
		if err != nil {
			t.Fatalf("Parse(%q) failed: %v", tt.input, err)
		}
		if cfg.Inline != tt.expected {
			t.Errorf("Parse(%q): expected Inline=%v, got %v", tt.input, tt.expected, cfg.Inline)
		}
	}
}

func TestParse_FeedWithoutName(t *testing.T) {
	input := "=> https://example.com/feed.xml"
	cfg, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(cfg.Feeds) != 1 {
		t.Fatalf("expected 1 feed, got %d", len(cfg.Feeds))
	}
	if cfg.Feeds[0].URL != "https://example.com/feed.xml" {
		t.Errorf("expected URL https://example.com/feed.xml, got %s", cfg.Feeds[0].URL)
	}
	if cfg.Feeds[0].Name != "" {
		t.Errorf("expected empty name, got %s", cfg.Feeds[0].Name)
	}
}

func TestParse_FeedWithName(t *testing.T) {
	input := `=> https://example.com/feed.xml "Example Feed"`
	cfg, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(cfg.Feeds) != 1 {
		t.Fatalf("expected 1 feed, got %d", len(cfg.Feeds))
	}
	if cfg.Feeds[0].URL != "https://example.com/feed.xml" {
		t.Errorf("expected URL https://example.com/feed.xml, got %s", cfg.Feeds[0].URL)
	}
	if cfg.Feeds[0].Name != "Example Feed" {
		t.Errorf("expected name 'Example Feed', got %s", cfg.Feeds[0].Name)
	}
}

func TestParse_MultipleFeeds(t *testing.T) {
	input := `
=> https://feed1.com/rss
=> https://feed2.com/atom "Feed Two"
=> https://feed3.com/json "Feed Three"
`
	cfg, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if len(cfg.Feeds) != 3 {
		t.Fatalf("expected 3 feeds, got %d", len(cfg.Feeds))
	}
	
	if cfg.Feeds[0].URL != "https://feed1.com/rss" {
		t.Errorf("feed[0] URL wrong: %s", cfg.Feeds[0].URL)
	}
	if cfg.Feeds[0].Name != "" {
		t.Errorf("feed[0] should have empty name, got %s", cfg.Feeds[0].Name)
	}
	
	if cfg.Feeds[1].URL != "https://feed2.com/atom" {
		t.Errorf("feed[1] URL wrong: %s", cfg.Feeds[1].URL)
	}
	if cfg.Feeds[1].Name != "Feed Two" {
		t.Errorf("feed[1] name wrong: %s", cfg.Feeds[1].Name)
	}
	
	if cfg.Feeds[2].Name != "Feed Three" {
		t.Errorf("feed[2] name wrong: %s", cfg.Feeds[2].Name)
	}
}

func TestParse_CompleteConfig(t *testing.T) {
	input := `
# My feed configuration
=: email user@example.com
=: cron 0 9 * * *
=: digest true
=: inline false

=> https://blog.example.com/feed.xml "Example Blog"
=> https://news.example.com/rss
`
	cfg, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	
	if cfg.Email != "user@example.com" {
		t.Errorf("email wrong: %s", cfg.Email)
	}
	if cfg.CronExpr != "0 9 * * *" {
		t.Errorf("cron wrong: %s", cfg.CronExpr)
	}
	if !cfg.Digest {
		t.Error("digest should be true")
	}
	if cfg.Inline {
		t.Error("inline should be false")
	}
	if len(cfg.Feeds) != 2 {
		t.Fatalf("expected 2 feeds, got %d", len(cfg.Feeds))
	}
}

func TestParse_CaseInsensitiveDirectives(t *testing.T) {
	input := `
=: EMAIL test@example.com
=: CRON 0 8 * * *
=: DIGEST false
=: INLINE true
`
	cfg, err := Parse(input)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if cfg.Email != "test@example.com" {
		t.Error("EMAIL directive not parsed")
	}
	if cfg.CronExpr != "0 8 * * *" {
		t.Error("CRON directive not parsed")
	}
	if cfg.Digest {
		t.Error("DIGEST directive not parsed")
	}
	if !cfg.Inline {
		t.Error("INLINE directive not parsed")
	}
}

func TestParseBool(t *testing.T) {
	tests := []struct {
		input      string
		defaultVal bool
		expected   bool
	}{
		{"true", false, true},
		{"false", true, false},
		{"1", false, true},
		{"0", true, false},
		{"yes", false, false}, // invalid, returns default
		{"no", true, true},    // invalid, returns default
		{"", false, false},    // invalid, returns default
		{"invalid", true, true}, // invalid, returns default
	}

	for _, tt := range tests {
		result := parseBool(tt.input, tt.defaultVal)
		if result != tt.expected {
			t.Errorf("parseBool(%q, %v) = %v, expected %v", tt.input, tt.defaultVal, result, tt.expected)
		}
	}
}
