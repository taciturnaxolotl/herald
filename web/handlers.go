package web

import (
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"net/http"
	"sort"
	"time"
)

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	host := parseOriginHost(s.origin)
	data := struct {
		Origin     string
		OriginHost string
		SSHHost    string
		SSHPort    int
	}{
		Origin:     s.origin,
		OriginHost: stripProtocol(s.origin),
		SSHHost:    host,
		SSHPort:    s.sshPort,
	}
	if err := s.tmpl.ExecuteTemplate(w, "index.html", data); err != nil {
		s.logger.Error("render index", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (s *Server) handleStyleCSS(w http.ResponseWriter, r *http.Request) {
	css, err := templatesFS.ReadFile("templates/style.css")
	if err != nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Write(css)
}

type userPageData struct {
	Fingerprint      string
	ShortFingerprint string
	Configs          []configInfo
	NextRun          string
	FeedXMLURL       string
	FeedJSONURL      string
	Origin           string
}

type configInfo struct {
	Filename  string
	FeedCount int
	URL       string
}

func (s *Server) handleUser(w http.ResponseWriter, r *http.Request, fingerprint string) {
	ctx := r.Context()

	user, err := s.store.GetUserByFingerprint(ctx, fingerprint)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "User Not Found", http.StatusNotFound)
			return
		}
		s.logger.Error("get user", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	configs, err := s.store.ListConfigs(ctx, user.ID)
	if err != nil {
		s.logger.Error("list configs", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var configInfos []configInfo
	var earliestNextRun time.Time

	for _, cfg := range configs {
		feeds, err := s.store.GetFeedsByConfig(ctx, cfg.ID)
		if err != nil {
			s.logger.Error("get feeds", "err", err)
			continue
		}

		configInfos = append(configInfos, configInfo{
			Filename:  cfg.Filename,
			FeedCount: len(feeds),
			URL:       "/" + fingerprint + "/" + cfg.Filename,
		})

		if cfg.NextRun.Valid {
			if earliestNextRun.IsZero() || cfg.NextRun.Time.Before(earliestNextRun) {
				earliestNextRun = cfg.NextRun.Time
			}
		}
	}

	nextRunStr := "—"
	if !earliestNextRun.IsZero() {
		nextRunStr = earliestNextRun.Format("2006-01-02 15:04 MST")
	}

	shortFP := fingerprint
	if len(shortFP) > 12 {
		shortFP = shortFP[:12]
	}

	data := userPageData{
		Fingerprint:      fingerprint,
		ShortFingerprint: shortFP,
		Configs:          configInfos,
		NextRun:          nextRunStr,
		FeedXMLURL:       "/" + fingerprint + "/feed.xml",
		FeedJSONURL:      "/" + fingerprint + "/feed.json",
		Origin:           s.origin,
	}

	if err := s.tmpl.ExecuteTemplate(w, "user.html", data); err != nil {
		s.logger.Error("render user", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

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

func (s *Server) handleFeedXML(w http.ResponseWriter, r *http.Request, fingerprint string) {
	ctx := r.Context()

	user, err := s.store.GetUserByFingerprint(ctx, fingerprint)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "User Not Found", http.StatusNotFound)
			return
		}
		s.logger.Error("get user", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	configs, err := s.store.ListConfigs(ctx, user.ID)
	if err != nil {
		s.logger.Error("list configs", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var items []rssItem
	for _, cfg := range configs {
		feeds, err := s.store.GetFeedsByConfig(ctx, cfg.ID)
		if err != nil {
			continue
		}
		for _, feed := range feeds {
			seenItems, err := s.store.GetSeenItems(ctx, feed.ID, 50)
			if err != nil {
				continue
			}
			for _, item := range seenItems {
				rItem := rssItem{
					GUID:    item.GUID,
					PubDate: item.SeenAt.Format(time.RFC1123Z),
				}
				if item.Title.Valid {
					rItem.Title = item.Title.String
				}
				if item.Link.Valid {
					rItem.Link = item.Link.String
				}
				items = append(items, rItem)
			}
		}
	}

	sort.Slice(items, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC1123Z, items[i].PubDate)
		tj, _ := time.Parse(time.RFC1123Z, items[j].PubDate)
		return ti.After(tj)
	})

	if len(items) > 100 {
		items = items[:100]
	}

	feed := rssFeed{
		Version: "2.0",
		Channel: rssChannel{
			Title:       "Herald - " + fingerprint[:12],
			Link:        s.origin + "/" + fingerprint,
			Description: "Aggregated feed for " + fingerprint[:12],
			Items:       items,
		},
	}

	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	enc.Encode(feed)
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

func (s *Server) handleFeedJSON(w http.ResponseWriter, r *http.Request, fingerprint string) {
	ctx := r.Context()

	user, err := s.store.GetUserByFingerprint(ctx, fingerprint)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "User Not Found", http.StatusNotFound)
			return
		}
		s.logger.Error("get user", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	configs, err := s.store.ListConfigs(ctx, user.ID)
	if err != nil {
		s.logger.Error("list configs", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var items []jsonFeedItem
	for _, cfg := range configs {
		feeds, err := s.store.GetFeedsByConfig(ctx, cfg.ID)
		if err != nil {
			continue
		}
		for _, feed := range feeds {
			seenItems, err := s.store.GetSeenItems(ctx, feed.ID, 50)
			if err != nil {
				continue
			}
			for _, item := range seenItems {
				jItem := jsonFeedItem{
					ID:            item.GUID,
					DatePublished: item.SeenAt.Format(time.RFC3339),
				}
				if item.Title.Valid {
					jItem.Title = item.Title.String
				}
				if item.Link.Valid {
					jItem.URL = item.Link.String
				}
				items = append(items, jItem)
			}
		}
	}

	sort.Slice(items, func(i, j int) bool {
		ti, _ := time.Parse(time.RFC3339, items[i].DatePublished)
		tj, _ := time.Parse(time.RFC3339, items[j].DatePublished)
		return ti.After(tj)
	})

	if len(items) > 100 {
		items = items[:100]
	}

	feed := jsonFeed{
		Version:     "https://jsonfeed.org/version/1.1",
		Title:       "Herald - " + fingerprint[:12],
		HomePageURL: s.origin + "/" + fingerprint,
		FeedURL:     s.origin + "/" + fingerprint + "/feed.json",
		Items:       items,
	}

	w.Header().Set("Content-Type", "application/feed+json; charset=utf-8")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(feed)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request, fingerprint, filename string) {
	ctx := r.Context()

	user, err := s.store.GetUserByFingerprint(ctx, fingerprint)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "User Not Found", http.StatusNotFound)
			return
		}
		s.logger.Error("get user", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	cfg, err := s.store.GetConfig(ctx, user.ID, filename)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Config Not Found", http.StatusNotFound)
			return
		}
		s.logger.Error("get config", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(cfg.RawText))
}

func stripProtocol(origin string) string {
	if len(origin) == 0 {
		return origin
	}
	
	// Remove http:// or https://
	if len(origin) > 7 && origin[:7] == "http://" {
		return origin[7:]
	}
	if len(origin) > 8 && origin[:8] == "https://" {
		return origin[8:]
	}
	
	return origin
}

func parseOriginHost(origin string) string {
	// Strip protocol
	hostPort := stripProtocol(origin)
	if hostPort == "" {
		return "localhost"
	}
	
	// Strip port if present
	for i := len(hostPort) - 1; i >= 0; i-- {
		if hostPort[i] == ':' {
			return hostPort[:i]
		}
	}
	
	return hostPort
}
