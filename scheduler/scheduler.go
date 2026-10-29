package scheduler

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"charm.land/log/v2"
	"github.com/taciturnaxolotl/herald/config"
	"github.com/taciturnaxolotl/herald/email"
	"github.com/taciturnaxolotl/herald/ratelimit"
	"github.com/taciturnaxolotl/herald/store"
)

const (
	// Email rate limiting
	emailsPerMinutePerUser = 1
	emailRateBurst         = 1
	emailsPerSecondPerUser = emailsPerMinutePerUser / 60.0

	cleanupInterval     = 24 * time.Hour
	engagementInterval  = 7 * 24 * time.Hour
	seenItemsRetention  = 6 * 30 * 24 * time.Hour // 6 months
	logsRetention       = 6 * 30 * 24 * time.Hour
	itemMaxAge          = 3 * 30 * 24 * time.Hour // 3 months
	emailSendsRetention = 6 * 30                  // 6 months in days

	// Digests with more items than this skip inline content.
	maxInlineItems = 5

	minSendsBeforeDeactivate = 3 // digests sent before inactivity can pause a config

	staleVerificationAge = store.VerifyTokenTTL
)

// RunStats contains detailed statistics from a feed fetch run
type RunStats struct {
	TotalFeeds   int
	FetchedFeeds int
	FailedFeeds  int
	NewItems     int
	EmailSent    bool
}

// Mailer delivers digests.
type Mailer interface {
	Send(to, subject, htmlBody, textBody string, links email.DigestLinks) error
}

type Scheduler struct {
	store       *store.DB
	mailer      Mailer
	logger      *log.Logger
	interval    time.Duration
	originURL   string
	rateLimiter *ratelimit.Limiter
	running     sync.Map // config ID -> struct{}, so one config never runs twice at once
}

func NewScheduler(st *store.DB, mailer Mailer, logger *log.Logger, interval time.Duration, originURL string) *Scheduler {
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
	cleanupTicker := time.NewTicker(cleanupInterval)
	defer cleanupTicker.Stop()
	engagementTicker := time.NewTicker(engagementInterval)
	defer engagementTicker.Stop()

	s.logger.Info("scheduler started", "interval", s.interval)
	s.cleanup(ctx)

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("scheduler stopped")
			return
		case <-ticker.C:
			s.tick(ctx)
		case <-cleanupTicker.C:
			s.cleanup(ctx)
		case <-engagementTicker.C:
			s.checkAndDeactivateInactiveConfigs(ctx)
		}
	}
}

// cleanup prunes old records; each job is isolated so one failure or panic doesn't skip the rest.
func (s *Scheduler) cleanup(ctx context.Context) {
	jobs := []struct {
		name string
		run  func(context.Context) (int64, error)
	}{
		{"seen items", func(ctx context.Context) (int64, error) {
			return s.store.CleanupOldSeenItems(ctx, seenItemsRetention)
		}},
		{"email sends", func(ctx context.Context) (int64, error) {
			return s.store.CleanupOldSends(ctx, emailSendsRetention)
		}},
		{"logs", func(ctx context.Context) (int64, error) {
			return s.store.CleanupOldLogs(ctx, logsRetention)
		}},
		{"stale verifications", func(ctx context.Context) (int64, error) {
			return s.store.DeleteStalePendingVerifications(ctx, staleVerificationAge)
		}},
	}
	for _, job := range jobs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					s.logger.Error("panic during cleanup", "job", job.name, "panic", r)
				}
			}()
			deleted, err := job.run(ctx)
			if err != nil {
				s.logger.Error("cleanup failed", "job", job.name, "err", err)
			} else if deleted > 0 {
				s.logger.Info("cleaned up", "job", job.name, "deleted", deleted)
			}
		}()
	}
}

func (s *Scheduler) checkAndDeactivateInactiveConfigs(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("panic during inactive config check", "panic", r)
		}
	}()

	paused, err := s.store.DeactivateInactiveConfigs(ctx, minSendsBeforeDeactivate)
	if err != nil {
		s.logger.Error("failed to deactivate inactive configs", "err", err)
		return
	}
	for _, cfg := range paused {
		s.logger.Info("deactivated inactive config", "config_id", cfg.ID, "email", cfg.Email)
		_ = s.store.AddLog(ctx, cfg.ID, "info", fmt.Sprintf("Paused: the keep-alive link was not used in %d days", store.InactivityDays))
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("panic during tick", "panic", r)
		}
	}()

	configs, err := s.store.GetDueConfigs(ctx, time.Now().UTC())
	if err != nil {
		s.logger.Error("failed to get due configs", "err", err)
		return
	}
	for _, cfg := range configs {
		if _, err := s.run(ctx, cfg, nil); err != nil {
			s.logger.Error("failed to process config", "config_id", cfg.ID, "err", err)
		}
	}
}

// RunNow runs a config immediately, reporting fetch progress through progress.
func (s *Scheduler) RunNow(ctx context.Context, configID int64, progress *atomic.Int32) (*RunStats, error) {
	cfg, err := s.store.GetConfigByID(ctx, configID)
	if err != nil {
		return nil, fmt.Errorf("get config: %w", err)
	}
	return s.run(ctx, cfg, progress)
}

// run is the one pipeline for scheduled and manual runs: fetch, send what is
// new, then record the run. A run always advances the schedule, so a failing
// config is retried on its next tick rather than every scheduler interval;
// unsent items stay unseen and go out then.
func (s *Scheduler) run(ctx context.Context, cfg *store.Config, progress *atomic.Int32) (*RunStats, error) {
	if _, busy := s.running.LoadOrStore(cfg.ID, struct{}{}); busy {
		return nil, errors.New("this digest is already running")
	}
	defer s.running.Delete(cfg.ID)

	stats, newItems, runErr := s.fetchAndSend(ctx, cfg, progress)

	// Record the run even if the caller has gone away mid-send.
	ctx = context.WithoutCancel(ctx)
	now := time.Now().UTC()
	nextRun, err := config.NextRun(cfg.CronExpr, now)
	if err != nil {
		return stats, errors.Join(runErr, fmt.Errorf("calculate next run: %w", err))
	}
	if err := s.store.UpdateLastRun(ctx, cfg.ID, now, nextRun); err != nil {
		return stats, errors.Join(runErr, fmt.Errorf("update last run: %w", err))
	}

	switch {
	case runErr != nil:
		_ = s.store.AddLog(ctx, cfg.ID, "error", fmt.Sprintf("Failed: %v", runErr))
	case !cfg.Active():
		_ = s.store.AddLog(ctx, cfg.ID, "info", fmt.Sprintf("Processed: %d new items, digest is paused", newItems))
	default:
		_ = s.store.AddLog(ctx, cfg.ID, "info", fmt.Sprintf("Processed: %d new items, next run: %s", newItems, nextRun.Format(time.RFC3339)))
	}
	return stats, runErr
}

func (s *Scheduler) fetchAndSend(ctx context.Context, cfg *store.Config, progress *atomic.Int32) (*RunStats, int, error) {
	feeds, err := s.store.GetFeedsByConfig(ctx, cfg.ID)
	if err != nil {
		return nil, 0, fmt.Errorf("get feeds: %w", err)
	}
	if len(feeds) == 0 {
		return nil, 0, errors.New("no feeds configured")
	}

	results := FetchFeeds(ctx, feeds, progress)
	stats := &RunStats{TotalFeeds: len(feeds)}
	for _, result := range results {
		if result.Error != nil {
			stats.FailedFeeds++
			s.logger.Warn("feed fetch error", "feed_id", result.FeedID, "url", result.FeedURL, "err", result.Error)
			continue
		}
		stats.FetchedFeeds++
		if result.ETag != "" || result.LastModified != "" {
			if err := s.store.UpdateFeedFetched(ctx, result.FeedID, result.ETag, result.LastModified); err != nil {
				s.logger.Warn("failed to update feed fetched", "err", err)
			}
		}
	}
	if stats.FetchedFeeds == 0 {
		return stats, 0, errors.New("all feeds failed to fetch")
	}

	batch := s.collectNewItems(ctx, results)
	stats.NewItems = batch.total()
	if stats.NewItems == 0 {
		return stats, 0, nil
	}
	if err := s.sendDigest(ctx, cfg, batch); err != nil {
		return stats, stats.NewItems, err
	}
	stats.EmailSent = true
	s.logger.Info("email sent", "to", cfg.Email, "items", stats.NewItems)
	return stats, stats.NewItems, nil
}

// digestBatch is what one run would send, and what to mark seen once it has.
type digestBatch struct {
	groups []email.FeedGroup
	seen   []seenItem
}

type seenItem struct {
	feedID            int64
	guid, title, link string
}

func (b digestBatch) total() int {
	n := 0
	for _, g := range b.groups {
		n += len(g.Items)
	}
	return n
}

func (s *Scheduler) collectNewItems(ctx context.Context, results []*FetchResult) digestBatch {
	var batch digestBatch
	for _, result := range results {
		if result.Error != nil {
			continue
		}
		guids := make([]string, len(result.Items))
		for i, item := range result.Items {
			guids[i] = item.GUID
		}
		seenSet, err := s.store.GetSeenGUIDs(ctx, result.FeedID, guids)
		if err != nil {
			// Skip the feed entirely, so nothing from it is marked seen unsent.
			s.logger.Warn("failed to check seen items", "feed_id", result.FeedID, "err", err)
			continue
		}

		for _, item := range result.Items {
			batch.seen = append(batch.seen, seenItem{result.FeedID, item.GUID, item.Title, item.Link})
		}
		if group := DigestGroup(result, seenSet, time.Now()); len(group.Items) > 0 {
			batch.groups = append(batch.groups, group)
		}
	}
	return batch
}

// DigestGroup is the section of a digest one fetched feed contributes: its
// recent entries that are not in seen.
func DigestGroup(result *FetchResult, seen map[string]bool, now time.Time) email.FeedGroup {
	group := email.FeedGroup{FeedName: cmp.Or(result.FeedName, result.FeedURL), FeedURL: result.FeedURL}
	maxAge := now.Add(-itemMaxAge)
	for _, item := range result.Items {
		if seen[item.GUID] || (!item.Published.IsZero() && item.Published.Before(maxAge)) {
			continue
		}
		group.Items = append(group.Items, email.FeedItem{
			Title:     item.Title,
			Link:      item.Link,
			Content:   item.Content,
			Published: item.Published,
		})
	}
	return group
}

func (s *Scheduler) sendDigest(ctx context.Context, cfg *store.Config, batch digestBatch) error {
	// Confirmed opt-in is enforced here, at the one point every send path funnels
	// through. Scheduling is the fast gate; this is the one that has to hold, and
	// it fails closed: an error checking means no send.
	verified, err := s.store.IsEmailVerified(ctx, cfg.UserID, cfg.Email)
	if err != nil {
		return fmt.Errorf("check verified: %w", err)
	}
	if !verified {
		if err := s.store.SetNextRun(ctx, cfg.ID, nil); err != nil {
			s.logger.Warn("failed to deactivate unverified config", "config_id", cfg.ID, "err", err)
		}
		cfg.NextRun.Valid = false
		return fmt.Errorf("held: %s is not verified, run `verify %s` over SSH to get a new confirmation link", cfg.Email, cfg.Filename)
	}

	total := batch.total()
	digestData := &email.DigestData{
		ConfigName: cfg.Filename,
		TotalItems: total,
		FeedGroups: batch.groups,
	}
	digestData.StripImageHosts = cfg.StripImageHosts
	inline := cfg.InlineContent && total <= maxInlineItems

	// Every digest must carry a working unsubscribe link, so no token means no send.
	unsubToken, err := s.store.GetOrCreateUnsubscribeToken(ctx, cfg.ID)
	if err != nil {
		return fmt.Errorf("create unsubscribe token: %w", err)
	}
	trackingToken, err := store.NewToken()
	if err != nil {
		return fmt.Errorf("create keep-alive token: %w", err)
	}
	digestData.Links = email.DigestLinks{
		KeepAlive:   s.originURL + "/keep-alive/" + trackingToken,
		Unsubscribe: s.originURL + "/unsubscribe/" + unsubToken,
	}
	if user, err := s.store.GetUserByID(ctx, cfg.UserID); err == nil {
		digestData.Links.Dashboard = s.originURL + "/" + user.PubkeyFP
	} else {
		s.logger.Warn("failed to get user for dashboard URL", "err", err)
	}

	daysUntilExpiry := int(time.Until(cfg.ExpiresAt()).Hours() / 24)
	htmlBody, textBody, err := email.RenderDigest(digestData, inline, daysUntilExpiry)
	if err != nil {
		return fmt.Errorf("render digest: %w", err)
	}

	if !s.rateLimiter.Allow(fmt.Sprintf("email:%d", cfg.UserID)) {
		return fmt.Errorf("rate limit exceeded for email sending")
	}

	subject := email.DigestSubject(digestData)
	if err := s.mailer.Send(cfg.Email, subject, htmlBody, textBody, digestData.Links); err != nil {
		return fmt.Errorf("send email: %w", err)
	}

	// The mail is out: record it even if the caller has gone away, so the next run does not resend it.
	ctx = context.WithoutCancel(ctx)
	err = s.store.WithTx(ctx, func(q *store.Queries) error {
		for _, item := range batch.seen {
			if err := q.MarkItemSeen(ctx, item.feedID, item.guid, item.title, item.link); err != nil {
				return err
			}
		}
		return q.RecordEmailSend(ctx, cfg.ID, cfg.Email, subject, trackingToken)
	})
	if err != nil {
		return fmt.Errorf("digest sent but not recorded, it may be sent again: %w", err)
	}
	return nil
}
