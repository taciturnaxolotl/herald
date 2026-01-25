package email

import (
	"bytes"
	"embed"
	htmltemplate "html/template"
	"regexp"
	"strings"
	texttemplate "text/template"
	"time"

	"github.com/microcosm-cc/bluemonday"
)

//go:embed templates/*
var templateFS embed.FS

type DigestData struct {
	ConfigName string
	TotalItems int
	FeedGroups []FeedGroup
}

type FeedGroup struct {
	FeedName string
	FeedURL  string
	Items    []FeedItem
}

type FeedItem struct {
	Title     string
	Link      string
	Content   string
	Published time.Time
}

// templateFeedItem is used for template rendering with sanitized HTML content
type templateFeedItem struct {
	Title            string
	Link             string
	Content          string            // Original content (unused, kept for compatibility)
	PlainContent     string            // HTML-stripped content for text template
	SanitizedContent htmltemplate.HTML // Sanitized HTML for HTML template
	Published        time.Time
}

// templateFeedGroup is used for template rendering with sanitized items
type templateFeedGroup struct {
	FeedName string
	FeedURL  string
	Items    []templateFeedItem
}

// emailUnsafeTags are HTML5 semantic tags not supported by most email clients (Gmail, Outlook, etc.)
var emailUnsafeTags = regexp.MustCompile(`</?(?:article|section|nav|header|footer|aside|main|figure|figcaption|details|summary|mark|time|dialog)(?:\s[^>]*)?>`)

// sanitizeHTML sanitizes HTML content, allowing safe tags while stripping styles and unsafe elements
func sanitizeHTML(html string) string {
	sanitized := policy.Sanitize(html)
	// Strip HTML5 semantic tags that email clients don't support
	return emailUnsafeTags.ReplaceAllString(sanitized, "")
}

// htmlTagRegex matches HTML tags for stripping
var htmlTagRegex = regexp.MustCompile(`<[^>]*>`)

// stripHTML removes all HTML tags and decodes entities for plain text output
func stripHTML(html string) string {
	// First sanitize to ensure we're working with clean HTML
	sanitized := policy.Sanitize(html)
	// Strip all remaining HTML tags
	text := htmlTagRegex.ReplaceAllString(sanitized, "")
	// Decode common HTML entities
	text = strings.ReplaceAll(text, "&amp;", "&")
	text = strings.ReplaceAll(text, "&lt;", "<")
	text = strings.ReplaceAll(text, "&gt;", ">")
	text = strings.ReplaceAll(text, "&quot;", "\"")
	text = strings.ReplaceAll(text, "&#39;", "'")
	text = strings.ReplaceAll(text, "&nbsp;", " ")
	// Collapse multiple whitespace/newlines
	text = regexp.MustCompile(`\s+`).ReplaceAllString(text, " ")
	return strings.TrimSpace(text)
}

var (
	htmlTmpl *htmltemplate.Template
	textTmpl *texttemplate.Template
	policy   *bluemonday.Policy
)

func init() {
	var err error
	htmlTmpl, err = htmltemplate.ParseFS(templateFS, "templates/digest.html")
	if err != nil {
		panic("failed to parse HTML template: " + err.Error())
	}
	textTmpl, err = texttemplate.ParseFS(templateFS, "templates/digest.txt")
	if err != nil {
		panic("failed to parse text template: " + err.Error())
	}

	// Initialize HTML sanitization policy
	// UGCPolicy allows safe HTML tags but strips styles and unsafe elements
	// This prevents XSS attacks while allowing basic formatting
	policy = bluemonday.UGCPolicy()
}

func RenderDigest(data *DigestData, inline bool, daysUntilExpiry int, showUrgentBanner, showWarningBanner bool) (html string, text string, err error) {
	// Convert FeedGroups to templateFeedGroups with sanitized HTML content
	sanitizedGroups := make([]templateFeedGroup, len(data.FeedGroups))
	for i, group := range data.FeedGroups {
		sanitizedItems := make([]templateFeedItem, len(group.Items))
		for j, item := range group.Items {
			sanitizedItems[j] = templateFeedItem{
				Title:            item.Title,
				Link:             item.Link,
				Content:          item.Content,
				PlainContent:     stripHTML(item.Content),
				SanitizedContent: htmltemplate.HTML(sanitizeHTML(item.Content)), // #nosec G203 -- Content is sanitized by bluemonday before conversion
				Published:        item.Published,
			}
		}
		sanitizedGroups[i] = templateFeedGroup{
			FeedName: group.FeedName,
			FeedURL:  group.FeedURL,
			Items:    sanitizedItems,
		}
	}

	// Prepare template data for HTML template (with sanitized content)
	htmlTmplData := struct {
		ConfigName        string
		TotalItems        int
		FeedGroups        []templateFeedGroup
		Inline            bool
		DaysUntilExpiry   int
		ShowUrgentBanner  bool
		ShowWarningBanner bool
	}{
		ConfigName:        data.ConfigName,
		TotalItems:        data.TotalItems,
		FeedGroups:        sanitizedGroups,
		Inline:            inline,
		DaysUntilExpiry:   daysUntilExpiry,
		ShowUrgentBanner:  showUrgentBanner,
		ShowWarningBanner: showWarningBanner,
	}

	// Prepare template data for text template (with plain text content)
	textTmplData := struct {
		ConfigName        string
		TotalItems        int
		FeedGroups        []templateFeedGroup
		Inline            bool
		DaysUntilExpiry   int
		ShowUrgentBanner  bool
		ShowWarningBanner bool
	}{
		ConfigName:        data.ConfigName,
		TotalItems:        data.TotalItems,
		FeedGroups:        sanitizedGroups,
		Inline:            inline,
		DaysUntilExpiry:   daysUntilExpiry,
		ShowUrgentBanner:  showUrgentBanner,
		ShowWarningBanner: showWarningBanner,
	}

	var htmlBuf, textBuf bytes.Buffer

	if err = htmlTmpl.Execute(&htmlBuf, htmlTmplData); err != nil {
		return "", "", err
	}

	if err = textTmpl.Execute(&textBuf, textTmplData); err != nil {
		return "", "", err
	}

	return htmlBuf.String(), textBuf.String(), nil
}
