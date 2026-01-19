package scheduler

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/adhocore/gronx"
	"github.com/charmbracelet/log"
	"github.com/kierank/herald/email"
	"github.com/kierank/herald/ratelimit"
	"github.com/kierank/herald/store"
)

const (
	// Email rate limiting
	emailsPerMinutePerUser = 1
	emailRateBurst         = 1
	emailsPerSecondPerUser = emailsPerMinutePerUser / 60.0

	// Cleanup intervals
	cleanupInterval     = 24 * time.Hour
	seenItemsRetention  = 6 * 30 * 24 * time.Hour // 6 months
	itemMaxAge          = 3 * 30 * 24 * time.Hour // 3 months
	emailSendsRetention = 6 * 30                  // 6 months in days

	// Item limits
	minItemsForDigest = 5

	// Engagement tracking
	inactivityThreshold      = 90 // days without opens
	minSendsBeforeDeactivate = 3  // minimum sends before considering deactivation
)

// RunStats contains detailed statistics from a feed fetch run
type RunStats struct {
	TotalFeeds   int
	FetchedFeeds int
	FailedFeeds  int
	NewItems     int
	EmailSent    bool
}

type Scheduler struct {
	store       *store.DB
	mailer      *email.Mailer
	logger      *log.Logger
	interval    time.Duration
	originURL   string
	rateLimiter *ratelimit.Limiter
}

func NewScheduler(st *store.DB, mailer *email.Mailer, logger *log.Logger, interval time.Duration, originURL string) *Scheduler {
	return &Scheduler{
		store:       st,
		mailer:      mailer,
		logger:      logger,
		interval:    interval,
		originURL:   originURL,
		rateLimiter: ratelimit.New(emailsPerSecondPerUser, emailRateBurst),
	}
}

func (s *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	// Cleanup ticker runs every 24 hours
	cleanupTicker := time.NewTicker(24 * time.Hour)
	defer cleanupTicker.Stop()

	// Engagement check ticker runs weekly
	engagementTicker := time.NewTicker(7 * 24 * time.Hour)
	defer engagementTicker.Stop()

	s.logger.Info("scheduler started", "interval", s.interval)

	// Run cleanup on start
	s.cleanupOldSeenItems(ctx)
	s.cleanupOldEmailSends(ctx)

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("scheduler stopped")
			return
		case <-ticker.C:
			s.tick(ctx)
		case <-cleanupTicker.C:
			s.cleanupOldSeenItems(ctx)
			s.cleanupOldEmailSends(ctx)
		case <-engagementTicker.C:
			s.checkAndDeactivateInactiveConfigs(ctx)
		}
	}
}

func (s *Scheduler) cleanupOldSeenItems(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("panic during cleanup", "panic", r)
		}
	}()

	deleted, err := s.store.CleanupOldSeenItems(ctx, seenItemsRetention)
	if err != nil {
		s.logger.Error("failed to cleanup old seen items", "err", err)
		return
	}
	if deleted > 0 {
		s.logger.Info("cleaned up old seen items", "deleted", deleted)
	}
}

func (s *Scheduler) cleanupOldEmailSends(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("panic during email sends cleanup", "panic", r)
		}
	}()

	deleted, err := s.store.CleanupOldSends(emailSendsRetention)
	if err != nil {
		s.logger.Error("failed to cleanup old email sends", "err", err)
		return
	}
	if deleted > 0 {
		s.logger.Info("cleaned up old email sends", "deleted", deleted)
	}
}

func (s *Scheduler) checkAndDeactivateInactiveConfigs(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("panic during inactive config check", "panic", r)
		}
	}()

	inactiveConfigs, err := s.store.GetInactiveConfigs(inactivityThreshold, minSendsBeforeDeactivate)
	if err != nil {
		s.logger.Error("failed to get inactive configs", "err", err)
		return
	}

	if len(inactiveConfigs) == 0 {
		return
	}

	s.logger.Info("found inactive configs", "count", len(inactiveConfigs))

	for _, configID := range inactiveConfigs {
		cfg, err := s.store.GetConfigByID(ctx, configID)
		if err != nil {
			s.logger.Error("failed to get config", "config_id", configID, "err", err)
			continue
		}

		// Only deactivate if next_run is set (config is active)
		if !cfg.NextRun.Valid {
			continue
		}

		// Deactivate by setting next_run to NULL
		if err := s.store.UpdateNextRun(ctx, configID, nil); err != nil {
			s.logger.Error("failed to deactivate inactive config", "config_id", configID, "err", err)
			continue
		}

		s.logger.Info("deactivated inactive config", "config_id", configID, "email", cfg.Email)
		_ = s.store.AddLog(ctx, configID, "info", fmt.Sprintf("Auto-deactivated due to no email opens in %d days", inactivityThreshold))
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("panic during tick", "panic", r)
		}
	}()

	now := time.Now().UTC()
	configs, err := s.store.GetDueConfigs(ctx, now)
	if err != nil {
		s.logger.Error("failed to get due configs", "err", err)
		return
	}

	for _, cfg := range configs {
		if err := s.processConfig(ctx, cfg); err != nil {
			s.logger.Error("failed to process config", "config_id", cfg.ID, "err", err)
			_ = s.store.AddLog(ctx, cfg.ID, "error", fmt.Sprintf("Failed: %v", err))
		}
	}
}

func (s *Scheduler) RunNow(ctx context.Context, configID int64, progress *atomic.Int32) (*RunStats, error) {
	cfg, err := s.store.GetConfigByID(ctx, configID)
	if err != nil {
		return nil, fmt.Errorf("get config: %w", err)
	}

	feeds, err := s.store.GetFeedsByConfig(ctx, cfg.ID)
	if err != nil {
		return nil, fmt.Errorf("get feeds: %w", err)
	}

	if len(feeds) == 0 {
		return nil, fmt.Errorf("no feeds configured")
	}

	stats := &RunStats{
		TotalFeeds: len(feeds),
	}

	results := FetchFeeds(ctx, feeds, progress)
	s.logger.Debug("RunNow: fetching complete", "total", len(feeds))

	// Count successful and failed fetches
	for _, result := range results {
		if result.Error != nil {
			stats.FailedFeeds++
		} else {
			stats.FetchedFeeds++
		}
	}
	s.logger.Debug("RunNow: counting complete", "fetched", stats.FetchedFeeds, "failed", stats.FailedFeeds)

	feedGroups, totalNew, err := s.collectNewItems(ctx, results)
	s.logger.Debug("RunNow: collectNewItems complete", "totalNew", totalNew, "err", err)
	if err != nil {
		return stats, err
	}

	stats.NewItems = totalNew

	if totalNew > 0 {
		s.logger.Debug("RunNow: starting email send")
		if err := s.sendDigestAndMarkSeen(ctx, cfg, feedGroups, totalNew, results); err != nil {
			s.logger.Error("RunNow: sendDigestAndMarkSeen failed", "err", err)
			return stats, err
		}
		stats.EmailSent = true
		s.logger.Info("email sent", "to", cfg.Email, "items", totalNew)
	}
	s.logger.Debug("RunNow: email phase complete")

	// Update feed metadata
	s.logger.Debug("RunNow: updating feed metadata", "count", len(results))
	for _, result := range results {
		if result.ETag != "" || result.LastModified != "" {
			if err := s.store.UpdateFeedFetched(ctx, result.FeedID, result.ETag, result.LastModified); err != nil {
				s.logger.Warn("failed to update feed fetched", "err", err)
			}
		}
	}
	s.logger.Debug("RunNow: feed metadata updated")

	s.logger.Debug("RunNow: calculating next run")
	now := time.Now().UTC()
	nextRun, err := gronx.NextTickAfter(cfg.CronExpr, now, true)
	if err != nil {
		return stats, fmt.Errorf("calculate next run: %w", err)
	}
	s.logger.Debug("RunNow: updating last run", "nextRun", nextRun)

	if err := s.store.UpdateLastRun(ctx, cfg.ID, now, nextRun); err != nil {
		return stats, fmt.Errorf("update last run: %w", err)
	}

	_ = s.store.AddLog(ctx, cfg.ID, "info", fmt.Sprintf("Processed: %d new items, next run: %s", totalNew, nextRun.Format(time.RFC3339)))

	s.logger.Debug("RunNow: complete")
	return stats, nil
}

func (s *Scheduler) collectNewItems(ctx context.Context, results []*FetchResult) ([]email.FeedGroup, int, error) {
	var feedGroups []email.FeedGroup
	totalNew := 0
	maxAge := time.Now().UTC().Add(-itemMaxAge)
	feedErrors := 0

	for _, result := range results {
		if result.Error != nil {
			s.logger.Warn("feed fetch error", "feed_id", result.FeedID, "url", result.FeedURL, "err", result.Error)
			feedErrors++
			continue
		}

		// Collect all GUIDs for this feed to batch check
		var guids []string
		for _, item := range result.Items {
			if !item.Published.IsZero() && item.Published.Before(maxAge) {
				continue
			}
			guids = append(guids, item.GUID)
		}

		// Batch check which items have been seen
		seenSet, err := s.store.GetSeenGUIDs(ctx, result.FeedID, guids)
		if err != nil {
			s.logger.Warn("failed to check seen items", "err", err)
			continue
		}

		// Collect new items
		var newItems []email.FeedItem
		for _, item := range result.Items {
			if !item.Published.IsZero() && item.Published.Before(maxAge) {
				continue
			}

			if !seenSet[item.GUID] {
				newItems = append(newItems, email.FeedItem{
					Title:     item.Title,
					Link:      item.Link,
					Content:   item.Content,
					Published: item.Published,
				})
			}
		}

		if len(newItems) > 0 {
			feedName := result.FeedName
			if feedName == "" {
				feedName = result.FeedURL
			}
			feedGroups = append(feedGroups, email.FeedGroup{
				FeedName: feedName,
				FeedURL:  result.FeedURL,
				Items:    newItems,
			})
			totalNew += len(newItems)
		}
	}

	if feedErrors == len(results) {
		return nil, 0, fmt.Errorf("all feeds failed to fetch")
	}

	return feedGroups, totalNew, nil
}

func (s *Scheduler) sendDigestAndMarkSeen(ctx context.Context, cfg *store.Config, feedGroups []email.FeedGroup, totalNew int, results []*FetchResult) error {
	s.logger.Debug("sendDigestAndMarkSeen: start", "totalNew", totalNew)
	digestData := &email.DigestData{
		ConfigName: cfg.Filename,
		TotalItems: totalNew,
		FeedGroups: feedGroups,
	}

	inline := cfg.InlineContent
	if totalNew > minItemsForDigest {
		inline = false
	}

	// Calculate expiry info
	expiryDate := cfg.CreatedAt.AddDate(0, 0, 90)
	daysUntilExpiry := int(time.Until(expiryDate).Hours() / 24)
	showUrgentBanner := daysUntilExpiry <= 7 && daysUntilExpiry >= 0
	showWarningBanner := daysUntilExpiry > 7 && daysUntilExpiry <= 30

	s.logger.Debug("sendDigestAndMarkSeen: rendering digest")
	htmlBody, textBody, err := email.RenderDigest(digestData, inline, daysUntilExpiry, showUrgentBanner, showWarningBanner)
	if err != nil {
		return fmt.Errorf("render digest: %w", err)
	}
	s.logger.Debug("sendDigestAndMarkSeen: digest rendered")

	unsubToken, err := s.store.GetOrCreateUnsubscribeToken(ctx, cfg.ID)
	if err != nil {
		s.logger.Warn("failed to create unsubscribe token", "err", err)
		unsubToken = ""
	}
	s.logger.Debug("sendDigestAndMarkSeen: got unsub token")

	user, err := s.store.GetUserByID(ctx, cfg.UserID)
	dashboardURL := ""
	if err == nil {
		dashboardURL = s.originURL + "/" + user.PubkeyFP
	} else {
		s.logger.Warn("failed to get user for dashboard URL", "err", err)
	}
	s.logger.Debug("sendDigestAndMarkSeen: got dashboard URL")

	// Rate limit email sending per user
	if !s.rateLimiter.Allow(fmt.Sprintf("email:%d", cfg.UserID)) {
		return fmt.Errorf("rate limit exceeded for email sending")
	}
	s.logger.Debug("sendDigestAndMarkSeen: rate limit ok")

	// Begin transaction to mark items seen
	tx, err := s.store.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	s.logger.Debug("sendDigestAndMarkSeen: transaction started")

	// Mark items seen BEFORE sending email
	for _, result := range results {
		if result.Error != nil {
			continue
		}
		for _, item := range result.Items {
			if err := s.store.MarkItemSeenTx(ctx, tx, result.FeedID, item.GUID, item.Title, item.Link); err != nil {
				s.logger.Warn("failed to mark item seen", "err", err)
			}
		}
	}
	s.logger.Debug("sendDigestAndMarkSeen: items marked seen")

	// Generate tracking token BEFORE recording (needed for keep-alive URL)
	trackingToken, err := s.store.GenerateTrackingToken()
	if err != nil {
		s.logger.Warn("failed to generate tracking token", "err", err)
		trackingToken = ""
	}
	s.logger.Debug("sendDigestAndMarkSeen: generated tracking token")

	// Record email send with tracking (within transaction)
	subject := "feed digest"
	s.logger.Debug("sendDigestAndMarkSeen: recording email send")
	if err := s.store.RecordEmailSendTx(tx, cfg.ID, cfg.Email, subject, trackingToken); err != nil {
		s.logger.Warn("failed to record email send", "err", err)
	}
	s.logger.Debug("sendDigestAndMarkSeen: recorded email send")

	// Build keep-alive URL
	keepAliveURL := ""
	if trackingToken != "" {
		keepAliveURL = s.originURL + "/keep-alive/" + trackingToken
	}

	// Send email - if this fails, transaction will rollback
	s.logger.Debug("sendDigestAndMarkSeen: calling mailer.Send", "to", cfg.Email)
	if err := s.mailer.Send(cfg.Email, subject, htmlBody, textBody, unsubToken, dashboardURL, keepAliveURL); err != nil {
		s.logger.Error("sendDigestAndMarkSeen: mailer.Send failed", "err", err)
		return fmt.Errorf("send email: %w", err)
	}
	s.logger.Debug("sendDigestAndMarkSeen: mailer.Send returned successfully")

	// Commit transaction only after successful email send
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

func (s *Scheduler) processConfig(ctx context.Context, cfg *store.Config) error {
	s.logger.Info("processing config", "config_id", cfg.ID, "filename", cfg.Filename)

	feeds, err := s.store.GetFeedsByConfig(ctx, cfg.ID)
	if err != nil {
		return fmt.Errorf("get feeds: %w", err)
	}

	if len(feeds) == 0 {
		s.logger.Warn("no feeds for config", "config_id", cfg.ID)
		return nil
	}

	results := FetchFeeds(ctx, feeds, nil) // No progress tracking for background jobs

	feedGroups, totalNew, err := s.collectNewItems(ctx, results)
	if err != nil {
		s.logger.Warn("failed to collect items", "config_id", cfg.ID, "err", err)
	}

	if totalNew > 0 {
		if err := s.sendDigestAndMarkSeen(ctx, cfg, feedGroups, totalNew, results); err != nil {
			return fmt.Errorf("send digest: %w", err)
		}
		s.logger.Info("email sent", "to", cfg.Email, "items", totalNew)
	} else {
		s.logger.Info("no new items", "config_id", cfg.ID)
	}

	// Update feed metadata
	for _, result := range results {
		if result.ETag != "" || result.LastModified != "" {
			if err := s.store.UpdateFeedFetched(ctx, result.FeedID, result.ETag, result.LastModified); err != nil {
				s.logger.Warn("failed to update feed fetched", "err", err)
			}
		}
	}

	now := time.Now().UTC()
	nextRun, err := gronx.NextTickAfter(cfg.CronExpr, now, true)
	if err != nil {
		return fmt.Errorf("calculate next run: %w", err)
	}

	if err := s.store.UpdateLastRun(ctx, cfg.ID, now, nextRun); err != nil {
		return fmt.Errorf("update last run: %w", err)
	}

	_ = s.store.AddLog(ctx, cfg.ID, "info", fmt.Sprintf("Processed: %d new items, next run: %s", totalNew, nextRun.Format(time.RFC3339)))

	return nil
}
