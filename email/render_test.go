package email

import (
	"strings"
	"testing"
	"time"
)

func TestRenderDigest_HTMLNotEscaped(t *testing.T) {
	// Create test data with HTML content
	data := &DigestData{
		ConfigName: "Test Config",
		TotalItems: 1,
		FeedGroups: []FeedGroup{
			{
				FeedName: "Test Feed",
				FeedURL:  "https://example.com/feed",
				Items: []FeedItem{
					{
						Title:     "Test Article",
						Link:      "https://example.com/article",
						Content:   "<p>This is a <strong>test</strong> article with <a href='https://example.com'>a link</a>.</p>",
						Published: time.Now(),
					},
				},
			},
		},
	}

	// Render with inline mode enabled
	htmlOutput, _, err := RenderDigest(data, true, 30, false, false)
	if err != nil {
		t.Fatalf("RenderDigest failed: %v", err)
	}

	// Debug: print actual output
	t.Logf("HTML Output:\n%s", htmlOutput)

	// Verify HTML is NOT escaped (should contain actual tags, not &lt; entities)
	if strings.Contains(htmlOutput, "&lt;p&gt;") {
		t.Error("HTML is being escaped - found &lt;p&gt; instead of <p>")
	}
	if strings.Contains(htmlOutput, "&lt;strong&gt;") {
		t.Error("HTML is being escaped - found &lt;strong&gt; instead of <strong>")
	}

	// Verify HTML tags are present (not escaped)
	if !strings.Contains(htmlOutput, "<p>This is a <strong>test</strong>") {
		t.Error("HTML tags are not being rendered - content appears to be escaped")
	}
}

func TestRenderDigest_UnsafeHTMLStripped(t *testing.T) {
	// Create test data with unsafe HTML content
	data := &DigestData{
		ConfigName: "Test Config",
		TotalItems: 1,
		FeedGroups: []FeedGroup{
			{
				FeedName: "Test Feed",
				FeedURL:  "https://example.com/feed",
				Items: []FeedItem{
					{
						Title:     "Test Article",
						Link:      "https://example.com/article",
						Content:   "<p>Safe content</p><script>alert('xss')</script><p style='color:red'>No styles</p>",
						Published: time.Now(),
					},
				},
			},
		},
	}

	// Render with inline mode enabled
	htmlOutput, _, err := RenderDigest(data, true, 30, false, false)
	if err != nil {
		t.Fatalf("RenderDigest failed: %v", err)
	}

	// Debug: print actual output
	t.Logf("HTML Output:\n%s", htmlOutput)

	// Verify script tags are removed
	if strings.Contains(htmlOutput, "<script>") {
		t.Error("Unsafe <script> tags were not stripped")
	}
	if strings.Contains(htmlOutput, "alert('xss')") {
		t.Error("Script content was not removed")
	}

	// Verify safe content remains
	if !strings.Contains(htmlOutput, "<p>Safe content</p>") {
		t.Error("Safe HTML content was incorrectly removed")
	}
}

func TestRenderDigest_TextOutputNoHTMLTags(t *testing.T) {
	// Create test data with HTML content
	data := &DigestData{
		ConfigName: "Test Config",
		TotalItems: 1,
		FeedGroups: []FeedGroup{
			{
				FeedName: "Test Feed",
				FeedURL:  "https://example.com/feed",
				Items: []FeedItem{
					{
						Title:     "Test Article",
						Link:      "https://example.com/article",
						Content:   "<article><p>This is a <strong>test</strong> article with <a href='https://example.com'>a link</a>.</p></article>",
						Published: time.Now(),
					},
				},
			},
		},
	}

	// Render with inline mode enabled
	_, textOutput, err := RenderDigest(data, true, 30, false, false)
	if err != nil {
		t.Fatalf("RenderDigest failed: %v", err)
	}

	// Verify text output does NOT contain HTML tags
	htmlTags := []string{"<article>", "<p>", "<strong>", "<a href", "</article>", "</p>", "</strong>", "</a>"}
	for _, tag := range htmlTags {
		if strings.Contains(textOutput, tag) {
			t.Errorf("Text output contains HTML tag %q - should be stripped", tag)
		}
	}

	// Verify the actual text content is present
	if !strings.Contains(textOutput, "This is a test article with a link") {
		t.Error("Text content was not preserved after HTML stripping")
	}
}

func TestRenderDigest_TinyImagesStripped(t *testing.T) {
	data := &DigestData{
		ConfigName:      "Test Config",
		TotalItems:      1,
		StripImageHosts: []string{"stickers.xeiaso.net"},
		FeedGroups: []FeedGroup{
			{
				FeedName: "Test Feed",
				FeedURL:  "https://example.com/feed",
				Items: []FeedItem{
					{
						Title: "Article with callout",
						Link:  "https://example.com/article",
						Content: `<p>Normal text</p>
<div><img class="h-16 w-16 rounded-xs" alt="Numa is smug" loading="lazy" src="https://stickers.xeiaso.net/sticker/numa/smug"/><span>Numa says: hi</span></div>
<p><img src="https://example.com/photo.jpg" alt="A large photo"/></p>`,
						Published: time.Now(),
					},
				},
			},
		},
	}

	htmlOutput, _, err := RenderDigest(data, true, 30, false, false)
	if err != nil {
		t.Fatalf("RenderDigest failed: %v", err)
	}

	// Verify tiny image with Tailwind sizing class is stripped
	if strings.Contains(htmlOutput, `stickers.xeiaso.net`) {
		t.Error("tiny sticker image was not stripped from HTML output")
	}

	// Verify normal <img> without tiny classes is preserved
	if !strings.Contains(htmlOutput, `<img src="https://example.com/photo.jpg"`) {
		t.Error("normal (non-tiny) image was incorrectly stripped")
	}

	// Verify surrounding text content is preserved (just the image removed)
	if !strings.Contains(htmlOutput, "Numa says: hi") {
		t.Error("text content around stripped image was also removed")
	}
}

func TestRenderDigest_CodeBlockFormatting(t *testing.T) {
	data := &DigestData{
		ConfigName: "Test Config",
		TotalItems: 1,
		FeedGroups: []FeedGroup{
			{
				FeedName: "Test Feed",
				FeedURL:  "https://example.com/feed",
				Items: []FeedItem{
					{
						Title: "Test Article",
						Link:  "https://example.com/article",
						Content: `<p>Code example:</p><pre><span class="c1"># comment</span>
echo hello</pre><p>Done.</p>`,
						Published: time.Now(),
					},
				},
			},
		},
	}

	htmlOutput, textOutput, err := RenderDigest(data, true, 30, false, false)
	if err != nil {
		t.Fatalf("RenderDigest failed: %v", err)
	}

	// HTML: verify code block has styling
	if !strings.Contains(htmlOutput, `<pre style="background-color:#f5f5f5`) {
		t.Error("HTML code block missing styling")
	}

	// HTML: verify syntax highlighting spans are stripped
	if strings.Contains(htmlOutput, `class="c1"`) {
		t.Error("Syntax highlighting classes should be stripped")
	}

	// Text: verify code is indented
	if !strings.Contains(textOutput, "    # comment") {
		t.Error("Text code block should be indented with 4 spaces")
	}

	// Text: verify no HTML tags in code block
	if strings.Contains(textOutput, "<span") || strings.Contains(textOutput, "<pre") {
		t.Error("Text output should not contain HTML tags")
	}
}

func TestDigestSubject(t *testing.T) {
	single := &DigestData{ConfigName: "feeds", TotalItems: 1, FeedGroups: []FeedGroup{{FeedName: "Xe's Blog"}}}
	if got := DigestSubject(single); got != "Xe's Blog: 1 new entry" {
		t.Errorf("single feed subject = %q", got)
	}

	many := &DigestData{ConfigName: "feeds", TotalItems: 12, FeedGroups: []FeedGroup{{FeedName: "a"}, {FeedName: "b"}}}
	if got := DigestSubject(many); got != "feeds: 12 new entries from 2 feeds" {
		t.Errorf("multi feed subject = %q", got)
	}
}

func TestInjectHTMLFooter(t *testing.T) {
	got := injectHTMLFooter("<html><body><p>hi</p></body></html>", "<hr>")
	if got != "<html><body><p>hi</p><hr></body></html>" {
		t.Errorf("footer landed outside body: %q", got)
	}

	if got := injectHTMLFooter("<p>hi</p>", "<hr>"); got != "<p>hi</p><hr>" {
		t.Errorf("fragment append = %q", got)
	}
}
