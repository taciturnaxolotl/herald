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
	htmlOutput, _, err := RenderDigest(data, true, 90)
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
	htmlOutput, _, err := RenderDigest(data, true, 90)
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
	_, textOutput, err := RenderDigest(data, true, 90)
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

func TestRenderDigest_StripImageHosts(t *testing.T) {
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

	htmlOutput, _, err := RenderDigest(data, true, 90)
	if err != nil {
		t.Fatalf("RenderDigest failed: %v", err)
	}

	if strings.Contains(htmlOutput, `stickers.xeiaso.net`) {
		t.Error("image from a strip_image_hosts host was not removed")
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

	htmlOutput, textOutput, err := RenderDigest(data, true, 90)
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

func TestRenderDigest_FooterAndBanner(t *testing.T) {
	data := &DigestData{
		FeedGroups: []FeedGroup{{FeedName: "Blog", FeedURL: "https://example.com/feed.xml", Items: []FeedItem{{Title: "Post", Link: "https://example.com/post"}}}},
		TotalItems: 1,
		Links: DigestLinks{
			KeepAlive:   "https://herald.test/keep-alive/k",
			Dashboard:   "https://herald.test/fp",
			Unsubscribe: "https://herald.test/unsubscribe/u",
		},
	}
	html, text, err := RenderDigest(data, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`href="https://herald.test/keep-alive/k"`, `href="https://herald.test/unsubscribe/u"`, "expires in 1 day."} {
		if !strings.Contains(html, want) {
			t.Errorf("HTML missing %q", want)
		}
	}
	if !strings.Contains(text, "unsubscribe: https://herald.test/unsubscribe/u") {
		t.Errorf("text footer missing unsubscribe link:\n%s", text)
	}
	if strings.Count(html, "<hr") != 1 {
		t.Errorf("expected exactly one divider above the footer, got %d", strings.Count(html, "<hr"))
	}

	data.Links.KeepAlive = ""
	html, _, err = RenderDigest(data, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "expiry-banner expiry-") {
		t.Error("no keep-alive link means no banner telling the reader to click it")
	}
}

func TestSanitizeHTML_ResolvesRelativeURLs(t *testing.T) {
	in := `<img src="/images/a.avif" alt="a"/><img src="https://cdn.example.com/b.png"/><a href="../other/?x=1&y=2">o</a><a href="#top">t</a>`
	out := sanitizeHTML(in, entryURL("https://dbushell.com/rss.xml", "https://dbushell.com/2026/10/28/danse-macabre/"), nil)

	for _, want := range []string{
		`src="https://dbushell.com/images/a.avif"`,
		`src="https://cdn.example.com/b.png"`,
		`href="https://dbushell.com/2026/10/28/other/?x=1&amp;y=2"`,
		`href="#top"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
}

func TestEntryURL(t *testing.T) {
	for link, want := range map[string]string{
		"":                        "https://example.com/blog/feed.xml",
		"post/1":                  "https://example.com/blog/post/1",
		"https://other.example/x": "https://other.example/x",
	} {
		if got := entryURL("https://example.com/blog/feed.xml", link).String(); got != want {
			t.Errorf("entryURL(%q) = %q, want %q", link, got, want)
		}
	}
}

func TestSanitizeHTML_DetailsBecomeLabelledBlock(t *testing.T) {
	out := sanitizeHTML(`<p>a</p><details open class="x"><summary>Hint one</summary><p>secret</p></details>`, nil, nil)

	if strings.Contains(out, "<details") || strings.Contains(out, "<summary") {
		t.Errorf("details/summary should be flattened: %s", out)
	}
	if !strings.Contains(out, `<p style="font-weight:bold;margin:0 0 6px">Hint one</p>`) {
		t.Errorf("summary should become a bold label: %s", out)
	}
	if !strings.Contains(out, detailsOpen+`<p style="font-weight:bold;margin:0 0 6px">Hint one</p><p>secret</p></div>`) {
		t.Errorf("details body should be wrapped in an aside: %s", out)
	}
}

func TestStripHTML_PreservesBlockStructure(t *testing.T) {
	in := `<p>First
  para.</p><p>Second<br>line</p><details><summary>Hint one</summary><p>secret</p></details>` +
		`<ol><li>alpha</li><li>beta</li></ol><ul><li>x</li><li>y</li></ul><pre>if x:
    y()</pre><p>end</p>`
	want := "First para.\n\nSecond\nline\n\nHint one\n\nsecret\n\n1. alpha\n2. beta\n\n- x\n- y\n\n    if x:\n        y()\n\nend"

	if got := stripHTML(in); got != want {
		t.Errorf("stripHTML:\ngot:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestDedentPreBlock(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"pretty-printed indent is removed": {
			"<div>\n    <pre>for x:\n        y()\n    done</pre>",
			"<div>\n    <pre>for x:\n    y()\ndone</pre>",
		},
		"inline pre keeps its indentation": {
			"<p>a</p><pre>sleep 4 && \\\n  ip link up</pre>",
			"<p>a</p><pre>sleep 4 && \\\n  ip link up</pre>",
		},
		"unindented pre on its own line is untouched": {
			"\n<pre>if x:\n    y()</pre>",
			"\n<pre>if x:\n    y()</pre>",
		},
	}
	for name, c := range cases {
		if got := preBlockFull.ReplaceAllStringFunc(c.in, dedentPreBlock); got != c.want {
			t.Errorf("%s:\ngot  %q\nwant %q", name, got, c.want)
		}
	}
}

func TestSanitizeHTML_StripsTinySizedImages(t *testing.T) {
	out := sanitizeHTML(`<p><img src="https://x/a.png" width="32"><img src="https://x/b.png" width="600"></p>`, nil, nil)
	if strings.Contains(out, "a.png") || !strings.Contains(out, "b.png") {
		t.Errorf("only the 32px image should be removed, got %q", out)
	}
}

func TestRenderDigest_TextHasNoBlankRunsOrLeadingBlankLines(t *testing.T) {
	data := &DigestData{
		FeedGroups: []FeedGroup{
			{FeedName: "A", FeedURL: "https://a/f", Items: []FeedItem{{Title: "1", Link: "https://a/1", Content: "<p>one</p>"}, {Title: "2", Link: "https://a/2"}}},
			{FeedName: "B", FeedURL: "https://b/f", Items: []FeedItem{{Title: "3", Link: "https://b/3"}}},
		},
		TotalItems: 3,
	}
	_, text, err := RenderDigest(data, true, 90)
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(text, "\n") {
		t.Errorf("text digest starts with blank lines: %q", text)
	}
	if strings.Contains(text, "\n\n\n\n") {
		t.Errorf("text digest has runs of blank lines: %q", text)
	}
}
