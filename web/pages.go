package web

import (
	"net/http"
	"time"

	"github.com/taciturnaxolotl/herald/store"
)

const shortFingerprintLen = 8

func (s *Server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	s.render(w, http.StatusOK, "index.html", s.index)
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
	LastActiveDays  *int // nil until the keep-alive link is first used
	DaysUntilExpiry int
}

func (s *Server) handleUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	fingerprint := r.PathValue("fingerprint")

	user, err := s.store.GetUserByFingerprint(ctx, fingerprint)
	if err != nil {
		s.fail(w, err, "get user", "", "")
		return
	}
	configs, err := s.store.ListConfigs(ctx, user.ID)
	if err != nil {
		s.fail(w, err, "list configs", "", "")
		return
	}

	configIDs := make([]int64, len(configs))
	for i, cfg := range configs {
		configIDs[i] = cfg.ID
	}
	feedsByConfig, err := s.store.GetFeedsByConfigs(ctx, configIDs)
	if err != nil {
		s.fail(w, err, "get feeds by configs", "", "")
		return
	}
	sends, err := s.store.CountRecentSends(ctx, configIDs, store.InactivityDays)
	if err != nil {
		s.logger.Warn("count recent sends", "err", err)
	}

	data := userPageData{
		Fingerprint:      fingerprint,
		ShortFingerprint: short(fingerprint, shortFingerprintLen),
		Status:           "INACTIVE",
		NextRun:          "—",
		Origin:           s.origin,
	}
	var earliest time.Time
	for _, cfg := range configs {
		info := configInfo{
			Filename:        cfg.Filename,
			FeedCount:       len(feedsByConfig[cfg.ID]),
			URL:             "/" + fingerprint + "/" + cfg.Filename,
			FeedXMLURL:      feedPath(fingerprint, cfg.Filename, ".xml"),
			FeedJSONURL:     feedPath(fingerprint, cfg.Filename, ".json"),
			IsActive:        cfg.Active(),
			TotalSends:      sends[cfg.ID],
			DaysUntilExpiry: int(time.Until(cfg.ExpiresAt()).Hours() / 24),
		}
		if cfg.LastActiveAt.Valid {
			days := int(time.Since(cfg.LastActiveAt.Time).Hours() / 24)
			info.LastActiveDays = &days
		}
		data.Configs = append(data.Configs, info)

		if cfg.Active() && (earliest.IsZero() || cfg.NextRun.Time.Before(earliest)) {
			earliest = cfg.NextRun.Time
		}
	}
	if !earliest.IsZero() {
		data.Status = "ACTIVE"
		data.NextRun = earliest.Format("2006-01-02 15:04 MST")
	}

	s.render(w, http.StatusOK, "user.html", data)
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
