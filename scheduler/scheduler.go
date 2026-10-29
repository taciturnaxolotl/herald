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

	// Cleanup intervals
	cleanupInterval     = 24 * time.Hour
	seenItemsRetention  = 6 * 30 * 24 * time.Hour // 6 months
	itemMaxAge          = 3 * 30 * 24 * time.Hour // 3 months
	emailSendsRetention = 6 * 30                  // 6 months in days

	// Digests with more items than this skip inline content.
	maxInlineItems = 5

	minSendsBeforeDeactivate = 3 // digests sent before inactivity can pause a config

	// Confirmed opt-in (double opt-in) limits. A destination address gets at
	// most one confirmation prompt per cooldown regardless of source (durable,
	// in the DB); the per-IP and per-fingerprint caps bound spraying. The
	// per-IP cap is the meaningful one since fresh fingerprints are free.
	verifyEmailCooldown  = 24 * time.Hour
	verifyIPPerDay       = 5
	verifyFPPerDay       = 3
	staleVerificationAge = store.VerifyTokenTTL
	secondsPerDay        = 86400.0
)

// VerifyStatus is the outcome of a confirmation-email request.
type VerifyStatus int

const (
	VerifyAlreadyVerified VerifyStatus = iota // address already confirmed, nothing to do
	VerifySent                                // confirmation email sent
	VerifyPending                             // a recent confirmation is still outstanding (cooldown)
	VerifyThrottled                           // per-IP or per-fingerprint cap hit
)

func (v VerifyStatus) String() string {
	switch v {
	case VerifyAlreadyVerified:
		return "already-verified"
	case VerifySent:
		return "sent"
	case VerifyPending:
		return "pending"
	case VerifyThrottled:
		return "throttled"
	default:
		return "unknown"
	}
}

// RunStats contains detailed statistics from a feed fetch run
type RunStats struct {
	TotalFeeds   int
	FetchedFeeds int
	FailedFeeds  int
	NewItems     int
	EmailSent    bool
}

// Mailer delivers digests and confirmation emails.
type Mailer interface {
	Send(to, subject, htmlBody, textBody, unsubToken, dashboardURL, keepAliveURL string) error
	SendVerification(to, fingerprint, verifyURL string) error
}

type Scheduler struct {
	store         *store.DB
	mailer        Mailer
	logger        *log.Logger
	interval      time.Duration
	originURL     string
	rateLimiter   *ratelimit.Limiter
	verifyIPLimit *ratelimit.Limiter
	verifyFPLimit *ratelimit.Limiter
	running       sync.Map // config ID -> struct{}, so one config never runs twice at once
}

func NewScheduler(st *store.DB, mailer Mailer, logger *log.Logger, interval time.Duration, originURL string) *Scheduler {
	return &Scheduler{
		store:         st,
		mailer:        mailer,
		logger:        logger,
		interval:      interval,
		originURL:     originURL,
		rateLimiter:   ratelimit.New(emailsPerSecondPerUser, emailRateBurst),
		verifyIPLimit: ratelimit.New(verifyIPPerDay/secondsPerDay, verifyIPPerDay),
		verifyFPLimit: ratelimit.New(verifyFPPerDay/secondsPerDay, verifyFPPerDay),
	}
}

// RequestEmailVerification sends a confirmation email for the (user, email)
// pair unless it is already verified or a limit blocks it. It is safe to call
// on every upload: the durable per-address cooldown and the per-IP and
// per-fingerprint caps keep it from being turned into a mail cannon.
func (s *Scheduler) RequestEmailVerification(ctx context.Context, userID int64, email, fingerprint, ipKey string) (VerifyStatus, error) {
	verified, err := s.store.IsEmailVerified(ctx, userID, email)
	if err != nil {
		return VerifyThrottled, fmt.Errorf("check verified: %w", err)
	}
	if verified {
		return VerifyAlreadyVerified, nil
	}

	v, err := s.store.EnsurePendingVerification(ctx, userID, email)
	if err != nil {
		return VerifyThrottled, fmt.Errorf("ensure pending: %w", err)
	}

	// Durable, global-per-address cooldown: the mailbox gets at most one prompt
	// per window no matter how many users/keys/IPs target it. Checked before
	// spending limiter tokens.
	last, err := s.store.LastConfirmationToEmail(ctx, email)
	if err != nil {
		return VerifyThrottled, fmt.Errorf("last confirmation: %w", err)
	}
	if last.Valid && time.Since(last.Time) < verifyEmailCooldown {
		return VerifyPending, nil
	}

	if ipKey != "" && !s.verifyIPLimit.Allow("verify:ip:"+ipKey) {
		return VerifyThrottled, nil
	}
	if !s.verifyFPLimit.Allow("verify:fp:" + fingerprint) {
		return VerifyThrottled, nil
	}

	if !v.Token.Valid {
		return VerifyThrottled, fmt.Errorf("pending verification has no token")
	}
	verifyURL := s.originURL + "/verify/" + v.Token.String
	if err := s.mailer.SendVerification(email, fingerprint, verifyURL); err != nil {
		return VerifyThrottled, fmt.Errorf("send verification: %w", err)
	}
	if err := s.store.MarkConfirmationSent(ctx, v.ID, time.Now().UTC()); err != nil {
		s.logger.Warn("failed to record confirmation send", "err", err)
	}
	return VerifySent, nil
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
	s.cleanupStalePendingVerifications(ctx)

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
			s.cleanupStalePendingVerifications(ctx)
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

	deleted, err := s.store.CleanupOldSends(ctx, emailSendsRetention)
	if err != nil {
		s.logger.Error("failed to cleanup old email sends", "err", err)
		return
	}
	if deleted > 0 {
		s.logger.Info("cleaned up old email sends", "deleted", deleted)
	}
}

func (s *Scheduler) cleanupStalePendingVerifications(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("panic during verification cleanup", "panic", r)
		}
	}()

	deleted, err := s.store.DeleteStalePendingVerifications(ctx, staleVerificationAge)
	if err != nil {
		s.logger.Error("failed to clean up stale verifications", "err", err)
		return
	}
	if deleted > 0 {
		s.logger.Info("cleaned up stale pending verifications", "deleted", deleted)
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
	maxAge := time.Now().UTC().Add(-itemMaxAge)

	for _, result := range results {
		if result.Error != nil {
			continue
		}

		var recent []FetchedItem
		var guids []string
		for _, item := range result.Items {
			if item.Published.IsZero() || !item.Published.Before(maxAge) {
				recent = append(recent, item)
				guids = append(guids, item.GUID)
			}
		}

		seenSet, err := s.store.GetSeenGUIDs(ctx, result.FeedID, guids)
		if err != nil {
			// Skip the feed entirely, so nothing from it is marked seen unsent.
			s.logger.Warn("failed to check seen items", "feed_id", result.FeedID, "err", err)
			continue
		}

		var newItems []email.FeedItem
		for _, item := range recent {
			if !seenSet[item.GUID] {
				newItems = append(newItems, email.FeedItem{
					Title:     item.Title,
					Link:      item.Link,
					Content:   item.Content,
					Published: item.Published,
				})
			}
		}
		for _, item := range result.Items {
			batch.seen = append(batch.seen, seenItem{result.FeedID, item.GUID, item.Title, item.Link})
		}

		if len(newItems) > 0 {
			batch.groups = append(batch.groups, email.FeedGroup{
				FeedName: cmp.Or(result.FeedName, result.FeedURL),
				FeedURL:  result.FeedURL,
				Items:    newItems,
			})
		}
	}
	return batch
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
	if parsed, err := config.Parse(cfg.RawText); err == nil {
		digestData.StripImageHosts = parsed.StripImageHosts
	}
	inline := cfg.InlineContent && total <= maxInlineItems

	daysUntilExpiry := int(time.Until(cfg.ExpiresAt()).Hours() / 24)

	htmlBody, textBody, err := email.RenderDigest(digestData, inline, daysUntilExpiry)
	if err != nil {
		return fmt.Errorf("render digest: %w", err)
	}

	// Every digest must carry a working unsubscribe link, so no token means no send.
	unsubToken, err := s.store.GetOrCreateUnsubscribeToken(ctx, cfg.ID)
	if err != nil {
		return fmt.Errorf("create unsubscribe token: %w", err)
	}
	trackingToken, err := store.NewToken()
	if err != nil {
		return fmt.Errorf("create keep-alive token: %w", err)
	}

	dashboardURL := ""
	if user, err := s.store.GetUserByID(ctx, cfg.UserID); err == nil {
		dashboardURL = s.originURL + "/" + user.PubkeyFP
	} else {
		s.logger.Warn("failed to get user for dashboard URL", "err", err)
	}

	if !s.rateLimiter.Allow(fmt.Sprintf("email:%d", cfg.UserID)) {
		return fmt.Errorf("rate limit exceeded for email sending")
	}

	subject := email.DigestSubject(digestData)
	keepAliveURL := s.originURL + "/keep-alive/" + trackingToken
	if err := s.mailer.Send(cfg.Email, subject, htmlBody, textBody, unsubToken, dashboardURL, keepAliveURL); err != nil {
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
