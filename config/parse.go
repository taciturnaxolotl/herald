// Package config handles Herald configuration parsing and validation.
package config

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type FeedEntry struct {
	URL  string
	Name string
}

type ParsedConfig struct {
	Email           string
	CronExpr        string
	Inline          bool
	StripImageHosts []string
	Feeds           []FeedEntry
}

var feedLineRegex = regexp.MustCompile(`^=>\s+(\S+)(?:\s+"([^"]*)")?$`)

// LineError is one line of a config that could not be understood.
type LineError struct {
	Line int
	Msg  string
}

func (e LineError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// LineErrors lists every problem in a config, so one upload reports them all.
type LineErrors []LineError

func (e LineErrors) Error() string {
	parts := make([]string, len(e))
	for i, le := range e {
		parts[i] = le.Error()
	}
	return strings.Join(parts, "; ")
}

// Parse reads a feeds config. The returned config always holds every line
// that parsed; err is a LineErrors naming the lines that did not.
func Parse(text string) (*ParsedConfig, error) {
	cfg := &ParsedConfig{Feeds: []FeedEntry{}}
	var errs LineErrors

	for i, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		var msg string
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
			continue
		case strings.HasPrefix(line, "=:"):
			msg = parseDirective(cfg, strings.TrimSpace(strings.TrimPrefix(line, "=:")))
		case strings.HasPrefix(line, "=>"):
			msg = parseFeed(cfg, line)
		default:
			msg = fmt.Sprintf("%q is not a setting (=:), a feed (=>) or a comment (#)", line)
		}
		if msg != "" {
			errs = append(errs, LineError{Line: i + 1, Msg: msg})
		}
	}

	if errs != nil {
		return cfg, errs
	}
	return cfg, nil
}

// parseDirective applies one "=: key value" line, returning a problem or "".
func parseDirective(cfg *ParsedConfig, content string) string {
	key, value, _ := strings.Cut(content, " ")
	key = strings.ToLower(key)
	value = strings.TrimSpace(value)
	if key == "" {
		return "empty setting"
	}
	if value == "" {
		return fmt.Sprintf("%q needs a value", key)
	}

	switch key {
	case "email":
		cfg.Email = value
	case "cron":
		cfg.CronExpr = value
	case "inline":
		b, err := strconv.ParseBool(value)
		if err != nil {
			return fmt.Sprintf("inline must be true or false, got %q", value)
		}
		cfg.Inline = b
	case "digest":
		// Accepted for pico.sh compatibility; every send is a digest.
	case "strip_image_hosts":
		for _, h := range strings.Split(value, ",") {
			if h = strings.TrimSpace(h); h != "" {
				cfg.StripImageHosts = append(cfg.StripImageHosts, h)
			}
		}
	default:
		return fmt.Sprintf("unknown setting %q", key)
	}
	return ""
}

// parseFeed applies one "=> url "name"" line, returning a problem or "".
func parseFeed(cfg *ParsedConfig, line string) string {
	matches := feedLineRegex.FindStringSubmatch(line)
	if matches == nil {
		return `feed lines look like: => https://example.com/feed.xml "Optional name"`
	}
	cfg.Feeds = append(cfg.Feeds, FeedEntry{URL: matches[1], Name: matches[2]})
	return ""
}
