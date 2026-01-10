package web

import (
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	maxFeedItems        = 100
	shortFingerprintLen = 8
	recentItemsLimit    = 50
	feedCacheMaxAge     = 300 // 5 minutes
)

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	host := parseOriginHost(s.origin)

	// Get short commit hash (first 7 chars)
	shortHash := s.commitHash
	if len(shortHash) > 7 {
		shortHash = shortHash[:7]
	}

	data := struct {
		Origin          string
		OriginHost      string
		SSHHost         string
		SSHPort         int
		CommitHash      string
		ShortCommitHash string
	}{
		Origin:          s.origin,
		OriginHost:      stripProtocol(s.origin),
		SSHHost:         host,
		SSHPort:         s.sshPort,
		CommitHash:      s.commitHash,
		ShortCommitHash: shortHash,
	}
	if err := s.tmpl.ExecuteTemplate(w, "index.html", data); err != nil {
		s.logger.Warn("render index", "err", err)
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
	Status           string
	NextRun          string
	Origin           string
}

type configInfo struct {
	Filename    string
	FeedCount   int
	URL         string
	FeedXMLURL  string
	FeedJSONURL string
	IsActive    bool
}

func (s *Server) handleUser(w http.ResponseWriter, r *http.Request, fingerprint string) {
	ctx := r.Context()

	user, err := s.store.GetUserByFingerprint(ctx, fingerprint)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.handle404(w, r)
			return
		}
		s.logger.Warn("get user", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	configs, err := s.store.ListConfigs(ctx, user.ID)
	if err != nil {
		s.logger.Warn("list configs", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Batch fetch all feeds for all configs
	configIDs := make([]int64, len(configs))
	for i, cfg := range configs {
		configIDs[i] = cfg.ID
	}
	feedsByConfig, err := s.store.GetFeedsByConfigs(ctx, configIDs)
	if err != nil {
		s.logger.Warn("get feeds by configs", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var configInfos []configInfo
	var earliestNextRun time.Time
	hasAnyActive := false

	for _, cfg := range configs {
		feeds := feedsByConfig[cfg.ID]

		isActive := cfg.NextRun.Valid
		if isActive {
			hasAnyActive = true
		}

		// Generate feed URLs - trim .txt extension for cleaner URLs
		feedBaseName := strings.TrimSuffix(cfg.Filename, ".txt")

		configInfos = append(configInfos, configInfo{
			Filename:    cfg.Filename,
			FeedCount:   len(feeds),
			URL:         "/" + fingerprint + "/" + cfg.Filename,
			FeedXMLURL:  "/" + fingerprint + "/" + feedBaseName + ".xml",
			FeedJSONURL: "/" + fingerprint + "/" + feedBaseName + ".json",
			IsActive:    isActive,
		})

		if cfg.NextRun.Valid {
			if earliestNextRun.IsZero() || cfg.NextRun.Time.Before(earliestNextRun) {
				earliestNextRun = cfg.NextRun.Time
			}
		}
	}

	nextRunStr := "—"
	status := "INACTIVE"
	if hasAnyActive {
		if !earliestNextRun.IsZero() {
			nextRunStr = earliestNextRun.Format("2006-01-02 15:04 MST")
		}
		status = "ACTIVE"
	}

	shortFP := fingerprint
	if len(shortFP) > shortFingerprintLen {
		shortFP = shortFP[:shortFingerprintLen]
	}

	data := userPageData{
		Fingerprint:      fingerprint,
		ShortFingerprint: shortFP,
		Configs:          configInfos,
		Status:           status,
		NextRun:          nextRunStr,
		Origin:           s.origin,
	}

	if err := s.tmpl.ExecuteTemplate(w, "user.html", data); err != nil {
		s.logger.Warn("render user", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

type rssItem struct {
	Title   string `xml:"title"`
	Link    string `xml:"link"`
	GUID    string `xml:"guid"`
	PubDate string `xml:"pubDate"`
}

type rssItemWithTime struct {
	rssItem
	parsedTime time.Time
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

func (s *Server) handleFeedXML(w http.ResponseWriter, r *http.Request, fingerprint, configFilename string) {
	ctx := r.Context()

	user, err := s.store.GetUserByFingerprint(ctx, fingerprint)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.handle404(w, r)
			return
		}
		s.logger.Warn("get user", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	cfg, err := s.store.GetConfig(ctx, user.ID, configFilename)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.handle404(w, r)
			return
		}
		s.logger.Warn("get config", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var items []rssItemWithTime
	feeds, err := s.store.GetFeedsByConfig(ctx, cfg.ID)
	if err != nil {
		s.logger.Warn("get feeds", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	for _, feed := range feeds {
		seenItems, err := s.store.GetSeenItems(ctx, feed.ID, 50)
		if err != nil {
			continue
		}
		for _, item := range seenItems {
			rItem := rssItemWithTime{
				rssItem: rssItem{
					GUID:    item.GUID,
					PubDate: item.SeenAt.Format(time.RFC1123Z),
				},
				parsedTime: item.SeenAt,
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

	sort.Slice(items, func(i, j int) bool {
		return items[i].parsedTime.After(items[j].parsedTime)
	})

	if len(items) > maxFeedItems {
		items = items[:maxFeedItems]
	}

	// Convert to rssItem for XML encoding
	rssItems := make([]rssItem, len(items))
	for i, item := range items {
		rssItems[i] = item.rssItem
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

	// Add caching headers
	w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", feedCacheMaxAge))
	if cfg.LastRun.Valid {
		etag := fmt.Sprintf(`"%s-%d"`, fingerprint[:shortFingerprintLen], cfg.LastRun.Time.Unix())
		w.Header().Set("ETag", etag)
		w.Header().Set("Last-Modified", cfg.LastRun.Time.UTC().Format(http.TimeFormat))

		// Check If-None-Match
		if match := r.Header.Get("If-None-Match"); match == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		// Check If-Modified-Since
		if modSince := r.Header.Get("If-Modified-Since"); modSince != "" {
			if t, err := http.ParseTime(modSince); err == nil && !cfg.LastRun.Time.After(t) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}

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

type jsonFeedItemWithTime struct {
	jsonFeedItem
	parsedTime time.Time
}

func (s *Server) handleFeedJSON(w http.ResponseWriter, r *http.Request, fingerprint, configFilename string) {
	ctx := r.Context()

	user, err := s.store.GetUserByFingerprint(ctx, fingerprint)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.handle404(w, r)
			return
		}
		s.logger.Warn("get user", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	cfg, err := s.store.GetConfig(ctx, user.ID, configFilename)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.handle404(w, r)
			return
		}
		s.logger.Warn("get config", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var items []jsonFeedItemWithTime
	feeds, err := s.store.GetFeedsByConfig(ctx, cfg.ID)
	if err != nil {
		s.logger.Warn("get feeds", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	for _, feed := range feeds {
		seenItems, err := s.store.GetSeenItems(ctx, feed.ID, recentItemsLimit)
		if err != nil {
			continue
		}
		for _, item := range seenItems {
			jItem := jsonFeedItemWithTime{
				jsonFeedItem: jsonFeedItem{
					ID:            item.GUID,
					DatePublished: item.SeenAt.Format(time.RFC3339),
				},
				parsedTime: item.SeenAt,
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

	sort.Slice(items, func(i, j int) bool {
		return items[i].parsedTime.After(items[j].parsedTime)
	})

	if len(items) > maxFeedItems {
		items = items[:maxFeedItems]
	}

	// Convert to jsonFeedItem for JSON encoding
	jsonItems := make([]jsonFeedItem, len(items))
	for i, item := range items {
		jsonItems[i] = item.jsonFeedItem
	}

	feed := jsonFeed{
		Version:     "https://jsonfeed.org/version/1.1",
		Title:       "Herald - " + configFilename,
		HomePageURL: s.origin + "/" + fingerprint + "/" + configFilename,
		FeedURL:     s.origin + "/" + fingerprint + "/" + configFilename + ".json",
		Items:       jsonItems,
	}

	// Add caching headers
	w.Header().Set("Content-Type", "application/feed+json; charset=utf-8")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", feedCacheMaxAge))
	if cfg.LastRun.Valid {
		etag := fmt.Sprintf(`"%s-%d"`, fingerprint[:shortFingerprintLen], cfg.LastRun.Time.Unix())
		w.Header().Set("ETag", etag)
		w.Header().Set("Last-Modified", cfg.LastRun.Time.UTC().Format(http.TimeFormat))

		// Check If-None-Match
		if match := r.Header.Get("If-None-Match"); match == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}

		// Check If-Modified-Since
		if modSince := r.Header.Get("If-Modified-Since"); modSince != "" {
			if t, err := http.ParseTime(modSince); err == nil && !cfg.LastRun.Time.After(t) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(feed)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request, fingerprint, filename string) {
	ctx := r.Context()

	user, err := s.store.GetUserByFingerprint(ctx, fingerprint)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.handle404(w, r)
			return
		}
		s.logger.Warn("get user", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	cfg, err := s.store.GetConfig(ctx, user.ID, filename)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.handle404(w, r)
			return
		}
		s.logger.Warn("get config", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(cfg.RawText))
}

type unsubscribePageData struct {
	Token            string
	ShortFingerprint string
	Filename         string
	Success          bool
	Message          string
	Error            string
}

func (s *Server) handleUnsubscribeGET(w http.ResponseWriter, r *http.Request, token string) {
	ctx := r.Context()

	cfg, err := s.store.GetConfigByToken(ctx, token)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.handle404WithMessage(w, r, "Invalid Link", "This unsubscribe link is invalid or has expired.")
			return
		}
		s.logger.Error("get config by token", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	user, err := s.store.GetUserByID(ctx, cfg.UserID)
	if err != nil {
		s.logger.Warn("get user", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	shortFP := user.PubkeyFP
	if len(shortFP) > shortFingerprintLen {
		shortFP = shortFP[:shortFingerprintLen]
	}

	data := unsubscribePageData{
		Token:            token,
		ShortFingerprint: shortFP,
		Filename:         cfg.Filename,
	}

	if err := s.tmpl.ExecuteTemplate(w, "unsubscribe.html", data); err != nil {
		s.logger.Warn("render unsubscribe", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (s *Server) handleUnsubscribePOST(w http.ResponseWriter, r *http.Request, token string) {
	ctx := r.Context()

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	action := r.FormValue("action")
	if action != "deactivate" && action != "delete" {
		http.Error(w, "Invalid action", http.StatusBadRequest)
		return
	}

	cfg, err := s.store.GetConfigByToken(ctx, token)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			s.handle404WithMessage(w, r, "Invalid Link", "This unsubscribe link is invalid or has expired.")
			return
		}
		s.logger.Error("get config by token", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	var message string

	if action == "deactivate" {
		if err := s.store.DeactivateConfig(ctx, cfg.ID); err != nil {
			s.logger.Error("deactivate config", "err", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		message = fmt.Sprintf("Config '%s' deactivated. You will no longer receive emails for this config. Other configs remain active. Files remain accessible via SSH/SCP.", cfg.Filename)
		s.logger.Info("config deactivated", "config_id", cfg.ID, "filename", cfg.Filename)
	} else {
		if err := s.store.DeleteUser(ctx, cfg.UserID); err != nil {
			s.logger.Error("delete user", "err", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		message = "All data deleted. You have been completely removed from Herald."
		s.logger.Info("user deleted", "user_id", cfg.UserID)
	}

	if err := s.store.DeleteToken(ctx, token); err != nil {
		s.logger.Warn("delete token", "err", err)
	}

	data := unsubscribePageData{
		Success: true,
		Message: message,
	}

	if err := s.tmpl.ExecuteTemplate(w, "unsubscribe.html", data); err != nil {
		s.logger.Warn("render unsubscribe", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
	}
}

func (s *Server) handleUnsubscribe(w http.ResponseWriter, r *http.Request, token string) {
	switch r.Method {
	case http.MethodGet:
		s.handleUnsubscribeGET(w, r, token)
	case http.MethodPost:
		s.handleUnsubscribePOST(w, r, token)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
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

func (s *Server) handle404(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
	data := struct {
		Title   string
		Message string
	}{}
	if err := s.tmpl.ExecuteTemplate(w, "404.html", data); err != nil {
		s.logger.Warn("render 404", "err", err)
		http.Error(w, "Not Found", http.StatusNotFound)
	}
}

func (s *Server) handle404WithMessage(w http.ResponseWriter, r *http.Request, title, message string) {
	w.WriteHeader(http.StatusNotFound)
	data := struct {
		Title   string
		Message string
	}{
		Title:   title,
		Message: message,
	}
	if err := s.tmpl.ExecuteTemplate(w, "404.html", data); err != nil {
		s.logger.Warn("render 404", "err", err)
		http.Error(w, "Not Found", http.StatusNotFound)
	}
}
