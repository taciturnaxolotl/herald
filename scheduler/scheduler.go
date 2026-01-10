package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/adhocore/gronx"
	"github.com/charmbracelet/log"
	"github.com/kierank/herald/email"
	"github.com/kierank/herald/store"
)

type Scheduler struct {
	store    *store.DB
	mailer   *email.Mailer
	logger   *log.Logger
	interval time.Duration
}

func NewScheduler(st *store.DB, mailer *email.Mailer, logger *log.Logger, interval time.Duration) *Scheduler {
	return &Scheduler{
		store:    st,
		mailer:   mailer,
		logger:   logger,
		interval: interval,
	}
}

func (s *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	s.logger.Info("scheduler started", "interval", s.interval)

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("scheduler stopped")
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
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

	var feedGroups []email.FeedGroup
	totalNew := 0
	threeMonthsAgo := time.Now().AddDate(0, -3, 0)
	feedErrors := 0

	for _, result := range results {
		if result.Error != nil {
			s.logger.Warn("feed fetch error", "feed_id", result.FeedID, "url", result.FeedURL, "err", result.Error)
			feedErrors++
			continue
		}

		var newItems []email.FeedItem
		for _, item := range result.Items {
			if !item.Published.IsZero() && item.Published.Before(threeMonthsAgo) {
				continue
			}

			seen, err := s.store.IsItemSeen(ctx, result.FeedID, item.GUID)
			if err != nil {
				s.logger.Warn("failed to check if item seen", "err", err)
				continue
			}

			if !seen {
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

		if result.ETag != "" || result.LastModified != "" {
			if err := s.store.UpdateFeedFetched(ctx, result.FeedID, result.ETag, result.LastModified); err != nil {
				s.logger.Warn("failed to update feed fetched", "err", err)
			}
		}
	}

	if feedErrors == len(results) {
		return 0, fmt.Errorf("all feeds failed to fetch")
	}

	if totalNew > 0 {
		digestData := &email.DigestData{
			ConfigName: cfg.Filename,
			TotalItems: totalNew,
			FeedGroups: feedGroups,
		}

		inline := cfg.InlineContent
		if totalNew > 5 {
			inline = false
		}

		htmlBody, textBody, err := email.RenderDigest(digestData, inline)
		if err != nil {
			return 0, fmt.Errorf("render digest: %w", err)
		}

		subject := "feed digest"
		if err := s.mailer.Send(cfg.Email, subject, htmlBody, textBody); err != nil {
			return 0, fmt.Errorf("send email: %w", err)
		}

		s.logger.Info("email sent", "to", cfg.Email, "items", totalNew)

		for _, result := range results {
			if result.Error != nil {
				continue
			}
			for _, item := range result.Items {
				if err := s.store.MarkItemSeen(ctx, result.FeedID, item.GUID, item.Title, item.Link); err != nil {
					s.logger.Warn("failed to mark item seen", "err", err)
				}
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

	var feedGroups []email.FeedGroup
	totalNew := 0
	threeMonthsAgo := time.Now().AddDate(0, -3, 0)

	for _, result := range results {
		if result.Error != nil {
			s.logger.Warn("feed fetch error", "feed_id", result.FeedID, "url", result.FeedURL, "err", result.Error)
			continue
		}

		var newItems []email.FeedItem
		for _, item := range result.Items {
			// Skip items older than 3 months
			if !item.Published.IsZero() && item.Published.Before(threeMonthsAgo) {
				continue
			}

			seen, err := s.store.IsItemSeen(ctx, result.FeedID, item.GUID)
			if err != nil {
				s.logger.Warn("failed to check if item seen", "err", err)
				continue
			}

			if !seen {
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

		if result.ETag != "" || result.LastModified != "" {
			if err := s.store.UpdateFeedFetched(ctx, result.FeedID, result.ETag, result.LastModified); err != nil {
				s.logger.Warn("failed to update feed fetched", "err", err)
			}
		}
	}

	if totalNew == 0 {
		s.logger.Info("no new items", "config_id", cfg.ID)
	} else {
		digestData := &email.DigestData{
			ConfigName: cfg.Filename,
			TotalItems: totalNew,
			FeedGroups: feedGroups,
		}

		// Auto-disable inline content if more than 5 items
		inline := cfg.InlineContent
		if totalNew > 5 {
			inline = false
		}

		htmlBody, textBody, err := email.RenderDigest(digestData, inline)
		if err != nil {
			return fmt.Errorf("render digest: %w", err)
		}

		subject := "feed digest"
		if err := s.mailer.Send(cfg.Email, subject, htmlBody, textBody); err != nil {
			return fmt.Errorf("send email: %w", err)
		}

		s.logger.Info("email sent", "to", cfg.Email, "items", totalNew)

		for _, result := range results {
			if result.Error != nil {
				continue
			}
			for _, item := range result.Items {
				if err := s.store.MarkItemSeen(ctx, result.FeedID, item.GUID, item.Title, item.Link); err != nil {
					s.logger.Warn("failed to mark item seen", "err", err)
				}
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
