package config

import (
	"errors"
	"slices"
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

func TestParse_DigestDirectiveIsAccepted(t *testing.T) {
	if _, err := Parse("=: digest false"); err != nil {
		t.Fatalf("pico.sh configs with a digest line must still parse: %v", err)
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
	if !cfg.Inline {
		t.Error("INLINE directive not parsed")
	}
}

func TestParse_ReportsEveryBadLine(t *testing.T) {
	input := `=: email a@example.com
=: emial typo@example.com
=: cron
=: inline yes
=> https://ok.example/feed.xml
=> https://bad.example/feed.xml "unterminated
just some text
=> https://ok2.example/feed.xml "Named"`

	cfg, err := Parse(input)
	var lineErrs LineErrors
	if !errors.As(err, &lineErrs) {
		t.Fatalf("expected LineErrors, got %v", err)
	}
	var lines []int
	for _, le := range lineErrs {
		lines = append(lines, le.Line)
	}
	if want := []int{2, 3, 4, 6, 7}; !slices.Equal(lines, want) {
		t.Fatalf("bad lines = %v, want %v (%v)", lines, want, err)
	}
	if cfg.Email != "a@example.com" || len(cfg.Feeds) != 2 {
		t.Errorf("good lines should still parse, got %+v", cfg)
	}
}
