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
