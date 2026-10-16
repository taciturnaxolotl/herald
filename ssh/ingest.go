package ssh

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"charm.land/log/v2"
	"charm.land/ssh"

	"github.com/kierank/herald/config"
	"github.com/kierank/herald/ratelimit"
	"github.com/kierank/herald/scheduler"
	"github.com/kierank/herald/store"
)

const maxUploadBytes = 1024 * 1024 // 1 MiB

// uploader is the single upload path shared by scp (scp -O) and sftp (default
// scp), so both enforce identical limits, validation and opt-in gating.
type uploader struct {
	store       *store.DB
	scheduler   *scheduler.Scheduler
	logger      *log.Logger
	rateLimiter *ratelimit.Limiter
}

// upload ingests a config and, on failure, logs it and reports it on the session's stderr.
func (u *uploader) upload(s ssh.Session, user *store.User, filename, content string) error {
	err := u.ingest(s.Context(), user, s.RemoteAddr(), filename, content)
	if err != nil {
		u.logger.Warn("upload rejected", "filename", filename, "user_id", user.ID, "err", err)
		_, _ = io.WriteString(s.Stderr(), formatUploadError(filename, err))
	}
	return err
}

// ingest validates a config and stores it, syncing feeds and enforcing confirmed opt-in.
func (u *uploader) ingest(ctx context.Context, user *store.User, remoteAddr net.Addr, filename, content string) error {
	if !u.rateLimiter.Allow(fmt.Sprintf("upload:%d", user.ID)) {
		return fmt.Errorf("rate limit exceeded, please try again later")
	}
	if !strings.HasSuffix(filename, ".txt") {
		return fmt.Errorf("only .txt files are supported")
	}
	if len(content) > maxUploadBytes {
		return fmt.Errorf("file too large (max 1MB)")
	}

	parsed, err := config.Parse(content)
	if err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}
	if err := config.Validate(parsed); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	fetched, err := fetchFeeds(ctx, parsed.Feeds)
	if err != nil {
		return err
	}

	nextRun, err := calculateNextRun(parsed.CronExpr)
	if err != nil {
		return fmt.Errorf("failed to calculate next run: %w", err)
	}

	tx, err := u.store.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existingCfg, err := u.store.GetConfigTx(ctx, tx, user.ID, filename)
	var cfg *store.Config
	if err == nil {
		if err := u.store.UpdateConfigTx(ctx, tx, existingCfg.ID, parsed.Email, parsed.CronExpr, parsed.Digest, parsed.Inline, content, nextRun); err != nil {
			return fmt.Errorf("failed to update config: %w", err)
		}
		cfg = existingCfg
		if err := syncFeedsTx(ctx, u.store, tx, cfg.ID, parsed.Feeds, fetched); err != nil {
			return err
		}
	} else {
		cfg, err = u.store.CreateConfigTx(ctx, tx, user.ID, filename, parsed.Email, parsed.CronExpr, parsed.Digest, parsed.Inline, content, nextRun)
		if err != nil {
			return fmt.Errorf("failed to create config: %w", err)
		}
		for _, feed := range parsed.Feeds {
			if _, err := u.store.CreateFeedTx(ctx, tx, cfg.ID, feed.URL, feed.Name); err != nil {
				return fmt.Errorf("failed to create feed: %w", err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	// Confirmed opt-in: a config only becomes active once the destination
	// address is verified for this user. Until then keep it inactive and
	// (rate-limited) send a confirmation email.
	verified, err := u.store.IsEmailVerified(ctx, user.ID, parsed.Email)
	if err != nil {
		// Fail closed: an unanswered check holds the config rather than letting
		// it run against an address nobody has confirmed.
		u.logger.Warn("verification check failed, holding config inactive", "err", err)
	}
	if err != nil || !verified {
		if err := u.store.DeactivateConfig(ctx, cfg.ID); err != nil {
			u.logger.Warn("failed to hold unverified config inactive", "err", err)
		}
	}
	if err == nil && !verified {
		ipKey := ""
		if remoteAddr != nil {
			ipKey = rateLimitIPKey(remoteAddr)
		}
		status, err := u.scheduler.RequestEmailVerification(ctx, user.ID, parsed.Email, user.PubkeyFP, ipKey)
		if err != nil {
			u.logger.Error("failed to request email verification", "email", parsed.Email, "err", err)
		} else {
			u.logger.Info("email verification requested", "email", parsed.Email, "status", status.String())
		}
	}

	u.logger.Info("config uploaded", "user_id", user.ID, "filename", filename, "feeds", len(parsed.Feeds), "verified", verified)
	return nil
}

// formatUploadError renders a failure as a checklist, starting on a fresh line past scp's progress meter.
func formatUploadError(filename string, err error) string {
	var b strings.Builder
	var feedErrs config.FeedErrors
	if errors.As(err, &feedErrs) {
		noun := "feeds"
		if len(feedErrs) == 1 {
			noun = "feed"
		}
		fmt.Fprintf(&b, "\nherald: rejected %s, %d %s could not be used:\n\n", filename, len(feedErrs), noun)
		for _, fe := range feedErrs {
			fmt.Fprintf(&b, "  %s\n    %v\n\n", fe.URL, fe.Err)
		}
		b.WriteString("Nothing was saved. Fix or remove these feeds and upload again.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "\nherald: rejected %s: %v\nNothing was saved.\n", filename, err)
	return b.String()
}

// fetchFeeds fetches every feed in parallel, outside any transaction, returning
// each feed's current items by URL or FeedErrors for the ones that fail.
func fetchFeeds(ctx context.Context, feeds []config.FeedEntry) (map[string][]scheduler.FetchedItem, error) {
	targets := make([]*store.Feed, len(feeds))
	for i, f := range feeds {
		targets[i] = &store.Feed{URL: f.URL}
	}
	items := make(map[string][]scheduler.FetchedItem, len(feeds))
	var errs config.FeedErrors
	for _, r := range scheduler.FetchFeeds(ctx, targets, nil) {
		if r.Error != nil {
			errs = append(errs, config.FeedError{URL: r.FeedURL, Err: r.Error})
			continue
		}
		items[r.FeedURL] = r.Items
	}
	if errs != nil {
		return nil, errs
	}
	return items, nil
}

// syncFeedsTx reconciles an existing config's feeds with the parsed set: update
// names of feeds that persist, create new ones (pre-seeding their current items
// as seen), and delete those no longer present.
func syncFeedsTx(ctx context.Context, st *store.DB, tx *sql.Tx, configID int64, feeds []config.FeedEntry, fetched map[string][]scheduler.FetchedItem) error {
	existingFeeds, err := st.GetFeedsByConfigTx(ctx, tx, configID)
	if err != nil {
		return fmt.Errorf("failed to get existing feeds: %w", err)
	}

	existingByURL := make(map[string]*store.Feed)
	for _, f := range existingFeeds {
		existingByURL[f.URL] = f
	}
	newByURL := make(map[string]struct{})
	for _, f := range feeds {
		newByURL[f.URL] = struct{}{}
	}

	for _, newFeed := range feeds {
		if existingFeed, exists := existingByURL[newFeed.URL]; exists {
			if err := st.UpdateFeedTx(ctx, tx, existingFeed.ID, newFeed.Name); err != nil {
				return fmt.Errorf("failed to update feed: %w", err)
			}
			continue
		}
		newFeedRecord, err := st.CreateFeedTx(ctx, tx, configID, newFeed.URL, newFeed.Name)
		if err != nil {
			return fmt.Errorf("failed to create feed: %w", err)
		}
		for _, item := range fetched[newFeed.URL] {
			if err := st.MarkItemSeenTx(ctx, tx, newFeedRecord.ID, item.GUID, item.Title, item.Link); err != nil {
				return fmt.Errorf("failed to preseed seen items: %w", err)
			}
		}
	}

	for _, existingFeed := range existingFeeds {
		if _, stillExists := newByURL[existingFeed.URL]; !stillExists {
			if err := st.DeleteFeedTx(ctx, tx, existingFeed.ID); err != nil {
				return fmt.Errorf("failed to delete feed: %w", err)
			}
		}
	}
	return nil
}
