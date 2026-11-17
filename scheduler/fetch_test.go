package scheduler

import (
	"net/url"
	"testing"
)

func TestItemGUID(t *testing.T) {
	if got := itemGUID("g", "l", "t", "c"); got != "g" {
		t.Errorf("prefers GUID, got %q", got)
	}
	if got := itemGUID("", "l", "t", "c"); got != "l" {
		t.Errorf("falls back to link, got %q", got)
	}
	a := itemGUID("", "", "first", "body")
	b := itemGUID("", "", "second", "body")
	if a == "" || a == b {
		t.Errorf("items with no GUID or link need distinct ids, got %q and %q", a, b)
	}
	if a != itemGUID("", "", "first", "body") {
		t.Error("fallback id must be stable across fetches")
	}
}

func TestResolveLink(t *testing.T) {
	base, _ := url.Parse("https://michael.bacarella.com/feed.xml")
	for link, want := range map[string]string{
		"":                       "",
		"/2026/11/16/coolio/":    "https://michael.bacarella.com/2026/11/16/coolio/",
		"post/1":                 "https://michael.bacarella.com/post/1",
		"//cdn.example/x":        "https://cdn.example/x",
		"https://other.example/": "https://other.example/",
	} {
		if got := resolveLink(base, link); got != want {
			t.Errorf("resolveLink(%q) = %q, want %q", link, got, want)
		}
	}
}

func TestLinkBase(t *testing.T) {
	fetched, _ := url.Parse("https://feeds.example.net/blog/feed.xml")
	for site, want := range map[string]string{
		"":                               "https://feeds.example.net/blog/feed.xml",
		"https://blog.example.com/":      "https://blog.example.com/",
		"http://blog.example.com":        "http://blog.example.com",
		"https://FEEDS.example.net/blog": "https://feeds.example.net/blog/feed.xml",
		"/":                              "https://feeds.example.net/blog/feed.xml",
		"ftp://blog.example.com/":        "https://feeds.example.net/blog/feed.xml",
		"http://localhost:4000/":         "https://feeds.example.net/blog/feed.xml",
		"http://site.localhost/":         "https://feeds.example.net/blog/feed.xml",
		"http://127.0.0.1/":              "https://feeds.example.net/blog/feed.xml",
		"http://[::1]/":                  "https://feeds.example.net/blog/feed.xml",
		"://bad":                         "https://feeds.example.net/blog/feed.xml",
	} {
		if got := linkBase(fetched, site).String(); got != want {
			t.Errorf("linkBase(%q) = %q, want %q", site, got, want)
		}
	}

	if got := resolveLink(linkBase(fetched, "https://blog.example.com/"), "/2026/post/"); got != "https://blog.example.com/2026/post/" {
		t.Errorf("cross-host feed resolved to %q", got)
	}
}
