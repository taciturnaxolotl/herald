package web

import (
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/taciturnaxolotl/herald/store"
)

const (
	maxFeedItems        = 100
	shortFingerprintLen = 8
	recentItemsLimit    = 50
	feedCacheMaxAge     = 300 // 5 minutes
)

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	host := parseOriginHost(s.origin)

	shortHash := short(s.commitHash, 7)

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
	s.render(w, http.StatusOK, "index.html", data)
}

func (s *Server) handleStyleCSS(w http.ResponseWriter, r *http.Request) {
	css, err := templatesFS.ReadFile("templates/style.css")
	if err != nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	_, _ = w.Write(css)
}

func (s *Server) handleFaviconSVG(w http.ResponseWriter, r *http.Request) {
	svg, err := publicFS.ReadFile("public/favicon.svg")
	if err != nil {
		http.Error(w, "Not Found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml")
	_, _ = w.Write(svg)
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
	Filename        string
	FeedCount       int
	URL             string
	FeedXMLURL      string
	FeedJSONURL     string
	IsActive        bool
	TotalSends      int
	LastActiveDays  int
	DaysUntilExpiry int
}

func (s *Server) handleUser(w http.ResponseWriter, r *http.Request, fingerprint string) {
	ctx := r.Context()

	user, err := s.store.GetUserByFingerprint(ctx, fingerprint)
	if err != nil {
		s.fail(w, err, "get user", "", "")
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

		isActive := cfg.Active()
		if isActive {
			hasAnyActive = true
		}

		totalSends, err := s.store.CountRecentSends(ctx, cfg.ID, store.InactivityDays)
		if err != nil {
			s.logger.Warn("get engagement", "config_id", cfg.ID, "err", err)
			// Continue without engagement data
		}

		// Calculate last active days and expiry
		lastActiveDays := -1
		if cfg.LastActiveAt.Valid {
			lastActiveDays = int(time.Since(cfg.LastActiveAt.Time).Hours() / 24)
		}

		daysUntilExpiry := int(time.Until(cfg.ExpiresAt()).Hours() / 24)

		configInfos = append(configInfos, configInfo{
			Filename:        cfg.Filename,
			FeedCount:       len(feeds),
			URL:             "/" + fingerprint + "/" + cfg.Filename,
			FeedXMLURL:      feedPath(fingerprint, cfg.Filename, ".xml"),
			FeedJSONURL:     feedPath(fingerprint, cfg.Filename, ".json"),
			IsActive:        isActive,
			TotalSends:      totalSends,
			LastActiveDays:  lastActiveDays,
			DaysUntilExpiry: daysUntilExpiry,
		})

		if cfg.Active() {
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

	shortFP := short(fingerprint, shortFingerprintLen)

	data := userPageData{
		Fingerprint:      fingerprint,
		ShortFingerprint: shortFP,
		Configs:          configInfos,
		Status:           status,
		NextRun:          nextRunStr,
		Origin:           s.origin,
	}

	s.render(w, http.StatusOK, "user.html", data)
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

	cfg, ok := s.lookupConfig(w, r, fingerprint, configFilename)
	if !ok {
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
		etag := fmt.Sprintf(`"%s-%d"`, short(fingerprint, shortFingerprintLen), cfg.LastRun.Time.Unix())
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

	_, _ = w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	_ = enc.Encode(feed)
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

	cfg, ok := s.lookupConfig(w, r, fingerprint, configFilename)
	if !ok {
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
		FeedURL:     s.origin + feedPath(fingerprint, configFilename, ".json"),
		Items:       jsonItems,
	}

	// Add caching headers
	w.Header().Set("Content-Type", "application/feed+json; charset=utf-8")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", feedCacheMaxAge))
	if cfg.LastRun.Valid {
		etag := fmt.Sprintf(`"%s-%d"`, short(fingerprint, shortFingerprintLen), cfg.LastRun.Time.Unix())
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
	_ = enc.Encode(feed)
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request, fingerprint, filename string) {
	cfg, ok := s.lookupConfig(w, r, fingerprint, filename)
	if !ok {
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	//nolint:gosec // text/plain content-type prevents script execution
	_, _ = w.Write([]byte(cfg.RawText))
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
		s.fail(w, err, "get config by token", "Invalid Link", "This unsubscribe link is invalid.")
		return
	}

	user, err := s.store.GetUserByID(ctx, cfg.UserID)
	if err != nil {
		s.logger.Warn("get user", "err", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	shortFP := short(user.PubkeyFP, shortFingerprintLen)

	data := unsubscribePageData{
		Token:            token,
		ShortFingerprint: shortFP,
		Filename:         cfg.Filename,
	}

	s.render(w, http.StatusOK, "unsubscribe.html", data)
}

func (s *Server) handleUnsubscribePOST(w http.ResponseWriter, r *http.Request, token string) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// RFC 8058 one-click unsubscribe pauses the digest and expects a plain reply.
	oneClick := r.FormValue("List-Unsubscribe") == "One-Click"
	action := store.PauseConfig
	if !oneClick {
		switch r.FormValue("action") {
		case "deactivate":
		case "delete":
			action = store.DeleteAccount
		default:
			http.Error(w, "Invalid action", http.StatusBadRequest)
			return
		}
	}

	cfg, err := s.store.RedeemUnsubscribeToken(r.Context(), token, action)
	if err != nil {
		switch {
		case !errors.Is(err, sql.ErrNoRows):
			s.logger.Error("redeem unsubscribe token", "err", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		case oneClick:
			http.Error(w, "Invalid token", http.StatusNotFound)
		default:
			s.notFound(w, "Invalid Link", "This unsubscribe link is invalid or has expired.")
		}
		return
	}

	var message string
	if action == store.DeleteAccount {
		message = "All data deleted. You have been completely removed from Herald."
		s.logger.Info("user deleted", "user_id", cfg.UserID)
	} else {
		message = fmt.Sprintf("Config '%s' deactivated. You will no longer receive emails for this config. Other configs remain active. Files remain accessible via SSH/SCP.", cfg.Filename)
		s.logger.Info("config deactivated", "config_id", cfg.ID, "filename", cfg.Filename, "one_click", oneClick)
	}

	if oneClick {
		_, _ = w.Write([]byte("Unsubscribed"))
		return
	}

	data := unsubscribePageData{Success: true, Message: message}
	s.render(w, http.StatusOK, "unsubscribe.html", data)
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

type keepAlivePageData struct {
	Filename  string
	ExpiresAt string
	Success   bool
}

// handleKeepAlive shows a confirm button on GET and renews only on POST, so
// mail scanners that prefetch links cannot keep every digest alive.
func (s *Server) handleKeepAlive(w http.ResponseWriter, r *http.Request, token string) {
	ctx := r.Context()

	cfg, err := s.store.GetConfigByTrackingToken(ctx, token)
	if err != nil {
		s.fail(w, err, "get config by tracking token", "Invalid Link", "This keep-alive link is invalid.")
		return
	}

	data := keepAlivePageData{
		Filename:  cfg.Filename,
		ExpiresAt: time.Now().AddDate(0, 0, store.InactivityDays).Format("January 2, 2006"),
	}

	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		if err := s.store.UpdateLastActive(ctx, token); err != nil {
			s.logger.Error("keep-alive", "err", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		data.Success = true
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	s.render(w, http.StatusOK, "keepalive.html", data)
}

type verifyPageData struct {
	Token            string
	Email            string
	ShortFingerprint string
	Success          bool
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request, token string) {
	switch r.Method {
	case http.MethodGet:
		s.handleVerifyGET(w, r, token)
	case http.MethodPost:
		s.handleVerifyPOST(w, r, token)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleVerifyGET(w http.ResponseWriter, r *http.Request, token string) {
	ctx := r.Context()

	v, err := s.store.GetVerificationByToken(ctx, token, store.VerifyTokenTTL)
	if err != nil {
		s.fail(w, err, "get verification by token", "Invalid Link", "This confirmation link is invalid or has expired.")
		return
	}

	shortFP := ""
	if user, err := s.store.GetUserByID(ctx, v.UserID); err == nil {
		shortFP = short(user.PubkeyFP, shortFingerprintLen)
	}

	// A GET is only a landing page; confirmation happens on POST so that link
	// prefetchers (mail scanners, SafeLinks) cannot auto-confirm.
	data := verifyPageData{
		Token:            token,
		Email:            v.Email,
		ShortFingerprint: shortFP,
	}
	s.render(w, http.StatusOK, "verify.html", data)
}

func (s *Server) handleVerifyPOST(w http.ResponseWriter, r *http.Request, token string) {
	v, err := s.store.ConfirmVerification(r.Context(), token)
	if err != nil {
		s.fail(w, err, "confirm verification", "Invalid Link", "This confirmation link is invalid or has expired.")
		return
	}

	s.logger.Info("email verified", "user_id", v.UserID, "email", v.Email)

	data := verifyPageData{Email: v.Email, Success: true}
	s.render(w, http.StatusOK, "verify.html", data)
}
