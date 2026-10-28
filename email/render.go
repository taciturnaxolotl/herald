package email

import (
	"bytes"
	"embed"
	"fmt"
	"html"
	htmltemplate "html/template"
	"net/url"
	"regexp"
	"strings"
	texttemplate "text/template"
	"time"

	"github.com/microcosm-cc/bluemonday"
)

//go:embed templates/*
var templateFS embed.FS

type DigestData struct {
	ConfigName      string
	TotalItems      int
	FeedGroups      []FeedGroup
	StripImageHosts []string
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
var emailUnsafeTags = regexp.MustCompile(`</?(?:article|section|nav|header|footer|aside|main|figure|figcaption|mark|time|dialog)(?:\s[^>]*)?>`)

// tinyImageTags matches <img> tags with small fixed dimensions (e.g., character stickers, decorative avatars).
// The class-based patterns cover Tailwind h-8 (32px), h-12 (48px), h-16 (64px) sizing.
// The src pattern catches images from sticker/image delivery domains.
var (
	tinyImageTags    = regexp.MustCompile(`<img[^>]*\sclass="[^"]*\bh-(?:8|12|16)\b[^"]*"[^>]*\/?>`)
	tinyInlineImages = regexp.MustCompile(`<img[^>]*\s(?:width|height)="(?:\d|[1-5]\d|6[0-4])"[^>]*\/?>`)
)

// detailsBlocks flattens <details>/<summary>, which Gmail and Outlook can't collapse, into a labelled aside.
var detailsBlocks = strings.NewReplacer(
	"<details>", detailsOpen, `<details open="">`, detailsOpen, "</details>", "</div>",
	"<summary>", `<p style="font-weight:bold;margin:0 0 6px">`, "</summary>", "</p>",
)

const detailsOpen = `<div style="border-left:3px solid #ccc;padding-left:12px;margin:1em 0">`

// spanTags matches span tags (used to strip syntax highlighting noise from code blocks)
var spanTags = regexp.MustCompile(`</?span(?:\s[^>]*)?>`)

// preBlockFull matches <pre>...</pre>, capturing any indentation before the tag on its own line.
var preBlockFull = regexp.MustCompile(`(?s)(\n([ \t]*))?(<pre[^>]*>)(.*?)(</pre>)`)

// preTagOpen matches opening pre tags to add styling
var preTagOpen = regexp.MustCompile(`<pre(?:\s[^>]*)?>`)

// codeBlockStyle is inline CSS for code blocks in emails
const codeBlockStyle = `<pre style="background-color:#f5f5f5;padding:12px;border-radius:4px;overflow-x:auto;font-family:monospace;font-size:13px;line-height:1.4">`

// sanitizeHTML sanitizes HTML content, allowing safe tags while stripping styles and unsafe elements.
// stripHosts lists image source hosts whose <img> tags should be removed.
// base resolves relative src/href values, which email clients cannot.
func sanitizeHTML(htmlStr string, base *url.URL, stripHosts []string) string {
	sanitized := policy.Sanitize(htmlStr)
	sanitized = resolveRelativeURLs(sanitized, base)
	// Strip HTML5 semantic tags that email clients don't support
	sanitized = emailUnsafeTags.ReplaceAllString(sanitized, "")
	sanitized = detailsBlocks.Replace(sanitized)
	// Strip tiny decorative images (character stickers, small avatars) that don't render well in email
	sanitized = tinyImageTags.ReplaceAllString(sanitized, "")
	for _, host := range stripHosts {
		re := regexp.MustCompile(`<img[^>]*\ssrc="[^"]*` + regexp.QuoteMeta(host) + `[^"]*"[^>]*\/?>`)
		sanitized = re.ReplaceAllString(sanitized, "")
	}
	sanitized = tinyInlineImages.ReplaceAllString(sanitized, "")
	// Strip span tags (removes syntax highlighting noise from code blocks)
	sanitized = spanTags.ReplaceAllString(sanitized, "")
	sanitized = preBlockFull.ReplaceAllStringFunc(sanitized, dedentPreBlock)
	// Add styling to pre tags for better code block appearance
	sanitized = preTagOpen.ReplaceAllString(sanitized, codeBlockStyle)
	return sanitized
}

// urlAttr matches src/href in bluemonday output, which always double-quotes values.
var urlAttr = regexp.MustCompile(`(\s(?:src|href)=")([^"]*)(")`)

func resolveRelativeURLs(htmlStr string, base *url.URL) string {
	if base == nil || !base.IsAbs() {
		return htmlStr
	}
	return urlAttr.ReplaceAllStringFunc(htmlStr, func(match string) string {
		m := urlAttr.FindStringSubmatch(match)
		raw := html.UnescapeString(m[2])
		if raw == "" || strings.HasPrefix(raw, "#") {
			return match
		}
		ref, err := url.Parse(raw)
		if err != nil || ref.IsAbs() {
			return match
		}
		return m[1] + html.EscapeString(base.ResolveReference(ref).String()) + m[3]
	})
}

// dedentPreBlock removes the indent a pretty-printed <pre> tag leaks into its later lines, and nothing more.
func dedentPreBlock(block string) string {
	m := preBlockFull.FindStringSubmatch(block)
	lead, indent, openTag, inner, closeTag := m[1], m[2], m[3], m[4], m[5]
	if indent == "" {
		return block
	}

	lines := strings.Split(inner, "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = strings.TrimPrefix(lines[i], indent)
	}
	return lead + openTag + strings.Join(lines, "\n") + closeTag
}

// Plain-text layout: source whitespace is collapsed, then structure comes back from block tags.
var (
	htmlTagRegex       = regexp.MustCompile(`<[^>]*>`)
	anyWhitespace      = regexp.MustCompile(`\s+`)
	horizontalSpace    = regexp.MustCompile(`[ \t]+`)
	spaceAroundNewline = regexp.MustCompile(` *\n *`)
	multipleNewlines   = regexp.MustCompile(`\n{3,}`)
	blockTags          = regexp.MustCompile(`(?i)</?(?:p|div|h[1-6]|blockquote|ul|ol|table|tr|details|summary|hr)(?:\s[^>]*)?/?>`)
	lineBreakTags      = regexp.MustCompile(`(?i)<br\s*/?>`)
	listItemTag        = regexp.MustCompile(`(?i)<li(?:\s[^>]*)?>`)
	orderedLists       = regexp.MustCompile(`(?is)<ol(?:\s[^>]*)?>.*?</ol>`)
)

// numberListItems replaces each <li> in an ordered list with its number.
func numberListItems(list string) string {
	n := 0
	return listItemTag.ReplaceAllStringFunc(list, func(string) string {
		n++
		return fmt.Sprintf("\n%d. ", n)
	})
}

// stripHTML renders feed HTML as plain text, keeping paragraphs, lists and code blocks.
func stripHTML(htmlStr string) string {
	sanitized := policy.Sanitize(htmlStr)

	// Code blocks are set aside so whitespace collapsing can't touch them.
	var codeBlocks []string
	sanitized = preBlockFull.ReplaceAllStringFunc(sanitized, func(block string) string {
		code := preBlockFull.FindStringSubmatch(dedentPreBlock(block))[4]
		code = html.UnescapeString(htmlTagRegex.ReplaceAllString(code, ""))
		lines := strings.Split(strings.TrimSpace(code), "\n")
		for i, line := range lines {
			lines[i] = "    " + line
		}
		codeBlocks = append(codeBlocks, strings.Join(lines, "\n"))
		return fmt.Sprintf("<p>__CODEBLOCK_%d__</p>", len(codeBlocks)-1)
	})

	sanitized = anyWhitespace.ReplaceAllString(sanitized, " ")
	sanitized = orderedLists.ReplaceAllStringFunc(sanitized, numberListItems)
	sanitized = listItemTag.ReplaceAllString(sanitized, "\n- ")
	sanitized = lineBreakTags.ReplaceAllString(sanitized, "\n")
	sanitized = blockTags.ReplaceAllString(sanitized, "\n\n")

	text := html.UnescapeString(htmlTagRegex.ReplaceAllString(sanitized, ""))
	text = horizontalSpace.ReplaceAllString(text, " ")
	text = spaceAroundNewline.ReplaceAllString(text, "\n")
	text = multipleNewlines.ReplaceAllString(text, "\n\n")

	for i, block := range codeBlocks {
		text = strings.ReplaceAll(text, fmt.Sprintf("__CODEBLOCK_%d__", i), block)
	}
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

// entryURL resolves an entry link against its feed, so empty or relative links fall back sensibly.
func entryURL(feedURL, link string) *url.URL {
	feed, err := url.Parse(feedURL)
	if err != nil {
		return nil
	}
	ref, err := url.Parse(link)
	if err != nil {
		return feed
	}
	return feed.ResolveReference(ref)
}

// DigestSubject names the source and counts the entries. A subject that varies
// per send reads as real list mail; a constant one reads as a blast.
func DigestSubject(data *DigestData) string {
	entries := "entries"
	if data.TotalItems == 1 {
		entries = "entry"
	}

	switch {
	case len(data.FeedGroups) == 1:
		return fmt.Sprintf("%s: %d new %s", data.FeedGroups[0].FeedName, data.TotalItems, entries)
	case data.ConfigName != "":
		return fmt.Sprintf("%s: %d new %s from %d feeds", data.ConfigName, data.TotalItems, entries, len(data.FeedGroups))
	default:
		return fmt.Sprintf("%d new %s from %d feeds", data.TotalItems, entries, len(data.FeedGroups))
	}
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
				SanitizedContent: htmltemplate.HTML(sanitizeHTML(item.Content, entryURL(group.FeedURL, item.Link), data.StripImageHosts)), // #nosec G203 -- Content is sanitized by bluemonday before conversion
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
