package ssh

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
	"unicode"

	"charm.land/log/v2"
	"charm.land/ssh"

	"github.com/taciturnaxolotl/herald/config"
	"github.com/taciturnaxolotl/herald/ratelimit"
	"github.com/taciturnaxolotl/herald/scheduler"
	"github.com/taciturnaxolotl/herald/store"
	"github.com/taciturnaxolotl/herald/verify"
)

const maxUploadBytes = 1024 * 1024 // 1 MiB

// uploader is the single upload path shared by scp (scp -O) and sftp (default
// scp), so both enforce identical limits, validation and opt-in gating.
type uploader struct {
	store       *store.DB
	verifier    *verify.Service
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
	if err := validateUploadName(filename); err != nil {
		return err
	}
	if len(content) > maxUploadBytes {
		return fmt.Errorf("file too large (max 1MB)")
	}

	parsed, err := config.Parse(content)
	if err != nil {
		return err
	}
	if err := config.Validate(parsed); err != nil {
		return fmt.Errorf("invalid config: %w", err)
	}
	fetched, err := fetchFeeds(ctx, parsed.Feeds)
	if err != nil {
		return err
	}

	nextRun, err := config.NextRun(parsed.CronExpr, time.Now())
	if err != nil {
		return fmt.Errorf("failed to calculate next run: %w", err)
	}

	// Confirmed opt-in: only a verified address is scheduled. A failed check
	// holds the config paused rather than mailing an unconfirmed address.
	verified, verifyErr := u.store.IsEmailVerified(ctx, user.ID, parsed.Email)
	if verifyErr != nil {
		u.logger.Warn("verification check failed, holding config inactive", "err", verifyErr)
	}
	var schedule *time.Time
	if verified {
		schedule = &nextRun
	}

	err = u.store.WithTx(ctx, func(q *store.Queries) error {
		cfg, created, err := q.UpsertConfig(ctx, user.ID, filename, parsed.Email, parsed.CronExpr, parsed.Inline, content, schedule)
		if err != nil {
			return err
		}
		// A new config's first digest carries the feeds' current entries; feeds
		// added to an existing config start from what is there now.
		preseed := fetched
		if created {
			preseed = nil
		}
		return syncFeeds(ctx, q, cfg.ID, parsed.Feeds, preseed)
	})
	if err != nil {
		return err
	}

	if verifyErr == nil && !verified {
		ipKey := ""
		if remoteAddr != nil {
			ipKey = rateLimitIPKey(remoteAddr)
		}
		status, err := u.verifier.Request(ctx, user.ID, parsed.Email, user.PubkeyFP, ipKey)
		if err != nil {
			u.logger.Error("failed to request email verification", "email", parsed.Email, "err", err)
		} else {
			u.logger.Info("email verification requested", "email", parsed.Email, "status", status.String())
		}
	}

	u.logger.Info("config uploaded", "user_id", user.ID, "filename", filename, "feeds", len(parsed.Feeds), "verified", verified)
	return nil
}

const maxFilenameLen = 128

// validateUploadName holds scp and sftp uploads to one naming rule: a short,
// printable .txt name with no directories, since names are echoed to terminals.
func validateUploadName(name string) error {
	switch {
	case !strings.HasSuffix(name, ".txt"):
		return errors.New("only .txt files are supported")
	case name == ".txt" || strings.ContainsAny(name, "/\\") || strings.HasPrefix(name, "."):
		return errors.New("upload a plain file name like feeds.txt, without directories")
	case len(name) > maxFilenameLen:
		return fmt.Errorf("file name too long (max %d characters)", maxFilenameLen)
	}
	for _, r := range name {
		if !unicode.IsPrint(r) {
			return errors.New("file name contains unprintable characters")
		}
	}
	return nil
}

// requestPath turns a client path into a config name, or false for the root.
func requestPath(p string) (string, bool) {
	name := strings.TrimPrefix(p, "/")
	return name, name != "" && name != "."
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
	var lineErrs config.LineErrors
	if errors.As(err, &lineErrs) {
		fmt.Fprintf(&b, "\nherald: error parsing %s\n\n", filename)
		for _, le := range lineErrs {
			fmt.Fprintf(&b, "  line %d: %s\n", le.Line, le.Msg)
		}
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

// syncFeeds reconciles a config's feeds with the parsed set: update names of
// feeds that persist, create new ones (marking their items in fetched as
// seen), and delete those no longer present.
func syncFeeds(ctx context.Context, q *store.Queries, configID int64, feeds []config.FeedEntry, fetched map[string][]scheduler.FetchedItem) error {
	existingFeeds, err := q.GetFeedsByConfig(ctx, configID)
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
			if err := q.UpdateFeedName(ctx, existingFeed.ID, newFeed.Name); err != nil {
				return fmt.Errorf("failed to update feed: %w", err)
			}
			continue
		}
		newFeedRecord, err := q.CreateFeed(ctx, configID, newFeed.URL, newFeed.Name)
		if err != nil {
			return fmt.Errorf("failed to create feed: %w", err)
		}
		for _, item := range fetched[newFeed.URL] {
			if err := q.MarkItemSeen(ctx, newFeedRecord.ID, item.GUID, item.Title, item.Link); err != nil {
				return fmt.Errorf("failed to preseed seen items: %w", err)
			}
		}
	}

	for _, existingFeed := range existingFeeds {
		if _, stillExists := newByURL[existingFeed.URL]; !stillExists {
			if err := q.DeleteFeed(ctx, existingFeed.ID); err != nil {
				return fmt.Errorf("failed to delete feed: %w", err)
			}
		}
	}
	return nil
}

// rateLimitIPKey derives a rate-limit bucket key from a client address. IPv4 is
// keyed per address; IPv6 is keyed per /64, since a single host typically owns
// a whole /64 and could otherwise rotate addresses to dodge the limit.
func rateLimitIPKey(addr net.Addr) string {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}
