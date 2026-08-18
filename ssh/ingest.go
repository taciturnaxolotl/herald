package ssh

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"strings"

	"charm.land/log/v2"

	"github.com/kierank/herald/config"
	"github.com/kierank/herald/scheduler"
	"github.com/kierank/herald/store"
)

const maxUploadBytes = 1024 * 1024 // 1 MiB

// ingestConfig validates uploaded feed-config content and stores it for the
// user, syncing feeds and enforcing confirmed opt-in.
//
// It is the single upload path shared by both transfer handlers -- the scp
// protocol (scp -O) and the sftp subsystem (default scp). Keeping them on one
// path is what guarantees they enforce identical validation, feed-URL checks,
// and the same verification gating; an earlier split let sftp uploads skip
// both. remoteAddr keys the confirmation-email rate limit and may be nil.
func ingestConfig(ctx context.Context, st *store.DB, sched *scheduler.Scheduler, logger *log.Logger, user *store.User, remoteAddr net.Addr, filename, content string) error {
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
	if err := config.ValidateFeedURLs(ctx, parsed); err != nil {
		return fmt.Errorf("feed validation failed: %w", err)
	}

	nextRun, err := calculateNextRun(parsed.CronExpr)
	if err != nil {
		return fmt.Errorf("failed to calculate next run: %w", err)
	}

	tx, err := st.BeginTx(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existingCfg, err := st.GetConfigTx(ctx, tx, user.ID, filename)
	var cfg *store.Config
	if err == nil {
		if err := st.UpdateConfigTx(ctx, tx, existingCfg.ID, parsed.Email, parsed.CronExpr, parsed.Digest, parsed.Inline, content, nextRun); err != nil {
			return fmt.Errorf("failed to update config: %w", err)
		}
		cfg = existingCfg
		if err := syncFeedsTx(ctx, st, tx, logger, cfg.ID, parsed.Feeds); err != nil {
			return err
		}
	} else {
		cfg, err = st.CreateConfigTx(ctx, tx, user.ID, filename, parsed.Email, parsed.CronExpr, parsed.Digest, parsed.Inline, content, nextRun)
		if err != nil {
			return fmt.Errorf("failed to create config: %w", err)
		}
		for _, feed := range parsed.Feeds {
			if _, err := st.CreateFeedTx(ctx, tx, cfg.ID, feed.URL, feed.Name); err != nil {
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
	verified, err := st.IsEmailVerified(ctx, user.ID, parsed.Email)
	if err != nil {
		// Fail closed: an unanswered check holds the config rather than letting
		// it run against an address nobody has confirmed.
		logger.Warn("verification check failed, holding config inactive", "err", err)
	}
	if err != nil || !verified {
		if err := st.DeactivateConfig(ctx, cfg.ID); err != nil {
			logger.Warn("failed to hold unverified config inactive", "err", err)
		}
	}
	if err == nil && !verified {
		ipKey := ""
		if remoteAddr != nil {
			ipKey = rateLimitIPKey(remoteAddr)
		}
		status, err := sched.RequestEmailVerification(ctx, user.ID, parsed.Email, user.PubkeyFP, ipKey)
		if err != nil {
			logger.Error("failed to request email verification", "email", parsed.Email, "err", err)
		} else {
			logger.Info("email verification requested", "email", parsed.Email, "status", status.String())
		}
	}

	logger.Info("config uploaded", "user_id", user.ID, "filename", filename, "feeds", len(parsed.Feeds), "verified", verified)
	return nil
}

// syncFeedsTx reconciles an existing config's feeds with the parsed set: update
// names of feeds that persist, create new ones (pre-seeding their current items
// as seen), and delete those no longer present.
func syncFeedsTx(ctx context.Context, st *store.DB, tx *sql.Tx, logger *log.Logger, configID int64, feeds []config.FeedEntry) error {
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
		if err := preseedSeenItems(ctx, st, tx, logger, newFeedRecord); err != nil {
			logger.Warn("failed to preseed seen items", "feed_url", newFeed.URL, "err", err)
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

// preseedSeenItems fetches a newly added feed and marks its current items as
// seen, so adding a feed does not blast out its whole backlog on the next run.
func preseedSeenItems(ctx context.Context, st *store.DB, tx *sql.Tx, logger *log.Logger, feed *store.Feed) error {
	result := scheduler.FetchFeed(ctx, feed)
	if result.Error != nil {
		return result.Error
	}
	for _, item := range result.Items {
		if err := st.MarkItemSeenTx(ctx, tx, feed.ID, item.GUID, item.Title, item.Link); err != nil {
			return err
		}
	}
	logger.Debug("preseeded seen items for new feed", "feed_url", feed.URL, "count", len(result.Items))
	return nil
}
