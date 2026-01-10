package config

import (
	"regexp"
	"strconv"
	"strings"
)

type FeedEntry struct {
	URL  string
	Name string
}

type ParsedConfig struct {
	Email    string
	CronExpr string
	Digest   bool
	Inline   bool
	Feeds    []FeedEntry
}

var feedLineRegex = regexp.MustCompile(`^=>\s+(\S+)(?:\s+"([^"]*)")?$`)

func Parse(text string) (*ParsedConfig, error) {
	cfg := &ParsedConfig{
		Digest: true,
		Inline: false,
		Feeds:  []FeedEntry{},
	}

	lines := strings.Split(text, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "=:") {
			if err := parseDirective(cfg, line); err != nil {
				return nil, err
			}
		} else if strings.HasPrefix(line, "=>") {
			if err := parseFeed(cfg, line); err != nil {
				return nil, err
			}
		}
	}

	return cfg, nil
}

func parseDirective(cfg *ParsedConfig, line string) error {
	content := strings.TrimPrefix(line, "=:")
	content = strings.TrimSpace(content)

	parts := strings.SplitN(content, " ", 2)
	if len(parts) < 2 {
		return nil
	}

	key := strings.ToLower(parts[0])
	value := strings.TrimSpace(parts[1])

	switch key {
	case "email":
		cfg.Email = value
	case "cron":
		cfg.CronExpr = value
	case "digest":
		cfg.Digest = parseBool(value, true)
	case "inline":
		cfg.Inline = parseBool(value, false)
	}

	return nil
}

func parseFeed(cfg *ParsedConfig, line string) error {
	matches := feedLineRegex.FindStringSubmatch(line)
	if matches == nil {
		return nil
	}

	entry := FeedEntry{
		URL:  matches[1],
		Name: matches[2],
	}
	cfg.Feeds = append(cfg.Feeds, entry)

	return nil
}

func parseBool(s string, defaultVal bool) bool {
	b, err := strconv.ParseBool(s)
	if err != nil {
		return defaultVal
	}
	return b
}
