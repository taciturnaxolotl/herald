package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/adhocore/gronx"
	"github.com/charmbracelet/log"
	"github.com/kierank/herald/email"
	"github.com/kierank/herald/ratelimit"
	"github.com/kierank/herald/store"
)

const (
	// Email rate limiting
	emailsPerMinutePerUser   = 1
	emailRateBurst           = 1
	emailsPerSecondPerUser   = emailsPerMinutePerUser / 60.0

	// Cleanup intervals
	cleanupInterval     = 24 * time.Hour
	seenItemsRetention  = 6 * 30 * 24 * time.Hour // 6 months
	itemMaxAge          = 3 * 30 * 24 * time.Hour // 3 months

	// Item limits
	minItemsForDigest = 5
)

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

	s.logger.Info("scheduler started", "interval", s.interval)

	// Run cleanup on start
	s.cleanupOldSeenItems(ctx)

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("scheduler stopped")
			return
		case <-ticker.C:
			s.tick(ctx)
		case <-cleanupTicker.C:
			s.cleanupOldSeenItems(ctx)
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

func (s *Scheduler) tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("panic during tick", "panic", r)
		}
	}()

	now := time.Now()
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

func (s *Scheduler) RunNow(ctx context.Context, configID int64) (int, error) {
	cfg, err := s.store.GetConfigByID(ctx, configID)
	if err != nil {
		return 0, fmt.Errorf("get config: %w", err)
	}

	feeds, err := s.store.GetFeedsByConfig(ctx, cfg.ID)
	if err != nil {
		return 0, fmt.Errorf("get feeds: %w", err)
	}

	if len(feeds) == 0 {
		return 0, fmt.Errorf("no feeds configured")
	}

	results := FetchFeeds(ctx, feeds)

	feedGroups, totalNew, err := s.collectNewItems(ctx, results)
	if err != nil {
		return 0, err
	}

	if totalNew > 0 {
		if err := s.sendDigestAndMarkSeen(ctx, cfg, feedGroups, totalNew, results); err != nil {
			return 0, err
		}
		s.logger.Info("email sent", "to", cfg.Email, "items", totalNew)
	}

	// Update feed metadata
	for _, result := range results {
		if result.ETag != "" || result.LastModified != "" {
			if err := s.store.UpdateFeedFetched(ctx, result.FeedID, result.ETag, result.LastModified); err != nil {
				s.logger.Warn("failed to update feed fetched", "err", err)
			}
		}
	}

	now := time.Now()
	nextRun, err := gronx.NextTick(cfg.CronExpr, false)
	if err != nil {
		return totalNew, fmt.Errorf("calculate next run: %w", err)
	}

	if err := s.store.UpdateLastRun(ctx, cfg.ID, now, nextRun); err != nil {
		return totalNew, fmt.Errorf("update last run: %w", err)
	}

	_ = s.store.AddLog(ctx, cfg.ID, "info", fmt.Sprintf("Processed: %d new items, next run: %s", totalNew, nextRun.Format(time.RFC3339)))

	return totalNew, nil
}

func (s *Scheduler) collectNewItems(ctx context.Context, results []*FetchResult) ([]email.FeedGroup, int, error) {
	var feedGroups []email.FeedGroup
	totalNew := 0
	maxAge := time.Now().Add(-itemMaxAge)
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
	digestData := &email.DigestData{
		ConfigName: cfg.Filename,
		TotalItems: totalNew,
		FeedGroups: feedGroups,
	}

	inline := cfg.InlineContent
	if totalNew > minItemsForDigest {
		inline = false
	}

	htmlBody, textBody, err := email.RenderDigest(digestData, inline)
	if err != nil {
		return fmt.Errorf("render digest: %w", err)
	}

	unsubToken, err := s.store.GetOrCreateUnsubscribeToken(ctx, cfg.ID)
	if err != nil {
		s.logger.Warn("failed to create unsubscribe token", "err", err)
		unsubToken = ""
	}

	user, err := s.store.GetUserByID(ctx, cfg.UserID)
	dashboardURL := ""
	if err == nil {
		dashboardURL = s.originURL + "/" + user.PubkeyFP
	} else {
		s.logger.Warn("failed to get user for dashboard URL", "err", err)
	}

	// Rate limit email sending per user
	if !s.rateLimiter.Allow(fmt.Sprintf("email:%d", cfg.UserID)) {
		return fmt.Errorf("rate limit exceeded for email sending")
	}

	// Begin transaction to mark items seen
	tx, err := s.store.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

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

	// Send email - if this fails, transaction will rollback
	subject := "feed digest"
	if err := s.mailer.Send(cfg.Email, subject, htmlBody, textBody, unsubToken, dashboardURL); err != nil {
		return fmt.Errorf("send email: %w", err)
	}

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

	results := FetchFeeds(ctx, feeds)

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

	now := time.Now()
	nextRun, err := gronx.NextTick(cfg.CronExpr, false)
	if err != nil {
		return fmt.Errorf("calculate next run: %w", err)
	}

	if err := s.store.UpdateLastRun(ctx, cfg.ID, now, nextRun); err != nil {
		return fmt.Errorf("update last run: %w", err)
	}

	_ = s.store.AddLog(ctx, cfg.ID, "info", fmt.Sprintf("Processed: %d new items, next run: %s", totalNew, nextRun.Format(time.RFC3339)))

	return nil
}
