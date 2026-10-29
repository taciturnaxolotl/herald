package web

import (
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/taciturnaxolotl/herald/store"
)

const (
	maxFeedItems    = 100
	feedCacheMaxAge = 300 // seconds
)

type rssItem struct {
	Title   string `xml:"title"`
	Link    string `xml:"link"`
	GUID    string `xml:"guid"`
	PubDate string `xml:"pubDate"`
}

type rssChannel struct {
	Title       string    `xml:"title"`
	Link        string    `xml:"link"`
	Description string    `xml:"description"`
	Items       []rssItem `xml:"item"`
}

type rssFeed struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Channel rssChannel `xml:"channel"`
}

type jsonFeed struct {
	Version     string         `json:"version"`
	Title       string         `json:"title"`
	HomePageURL string         `json:"home_page_url"`
	FeedURL     string         `json:"feed_url"`
	Items       []jsonFeedItem `json:"items"`
}

type jsonFeedItem struct {
	ID            string `json:"id"`
	URL           string `json:"url,omitempty"`
	Title         string `json:"title,omitempty"`
	DatePublished string `json:"date_published"`
}

// loadFeed resolves a config's published feed and its newest items, answering
// the request itself (404, 304, 500) when it returns false.
func (s *Server) loadFeed(w http.ResponseWriter, r *http.Request, fingerprint, configFilename string) ([]*store.SeenItem, bool) {
	cfg, ok := s.lookupConfig(w, r, fingerprint, configFilename)
	if !ok {
		return nil, false
	}
	if notModified(w, r, cfg) {
		return nil, false
	}
	items, err := s.store.GetRecentItemsForConfig(r.Context(), cfg.ID, maxFeedItems)
	if err != nil {
		s.fail(w, err, "get recent items", "", "")
		return nil, false
	}
	return items, true
}

// notModified sets caching headers and reports whether the client's copy is current.
// The ETag covers the config text too, since editing feeds changes the output.
func notModified(w http.ResponseWriter, r *http.Request, cfg *store.Config) bool {
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", feedCacheMaxAge))
	if !cfg.LastRun.Valid {
		return false
	}
	sum := sha256.Sum256([]byte(cfg.RawText))
	etag := fmt.Sprintf(`"%d-%x"`, cfg.LastRun.Time.Unix(), sum[:8])
	w.Header().Set("ETag", etag)
	w.Header().Set("Last-Modified", cfg.LastRun.Time.UTC().Format(http.TimeFormat))

	if match := r.Header.Get("If-None-Match"); match != "" {
		for _, candidate := range strings.Split(match, ",") {
			candidate = strings.TrimPrefix(strings.TrimSpace(candidate), "W/")
			if candidate == etag || candidate == "*" {
				w.WriteHeader(http.StatusNotModified)
				return true
			}
		}
		return false
	}
	if t, err := http.ParseTime(r.Header.Get("If-Modified-Since")); err == nil && !cfg.LastRun.Time.After(t) {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}

func (s *Server) handleFeedXML(w http.ResponseWriter, r *http.Request, fingerprint, configFilename string) {
	items, ok := s.loadFeed(w, r, fingerprint, configFilename)
	if !ok {
		return
	}

	rssItems := make([]rssItem, len(items))
	for i, item := range items {
		rssItems[i] = rssItem{Title: item.Title.String, Link: item.Link.String, GUID: item.GUID, PubDate: item.SeenAt.Format(time.RFC1123Z)}
	}
	feed := rssFeed{
		Version: "2.0",
		Channel: rssChannel{
			Title:       "Herald - " + configFilename,
			Link:        s.origin + "/" + fingerprint + "/" + configFilename,
			Description: "Feed for " + configFilename,
			Items:       rssItems,
		},
	}

	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	_, _ = w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	_ = enc.Encode(feed)
}

func (s *Server) handleFeedJSON(w http.ResponseWriter, r *http.Request, fingerprint, configFilename string) {
	items, ok := s.loadFeed(w, r, fingerprint, configFilename)
	if !ok {
		return
	}

	jsonItems := make([]jsonFeedItem, len(items))
	for i, item := range items {
		jsonItems[i] = jsonFeedItem{ID: item.GUID, URL: item.Link.String, Title: item.Title.String, DatePublished: item.SeenAt.Format(time.RFC3339)}
	}
	feed := jsonFeed{
		Version:     "https://jsonfeed.org/version/1.1",
		Title:       "Herald - " + configFilename,
		HomePageURL: s.origin + "/" + fingerprint + "/" + configFilename,
		FeedURL:     s.origin + feedPath(fingerprint, configFilename, ".json"),
		Items:       jsonItems,
	}

	w.Header().Set("Content-Type", "application/feed+json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(feed)
}
