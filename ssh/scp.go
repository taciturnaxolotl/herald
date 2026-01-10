package ssh

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"github.com/adhocore/gronx"
	"github.com/charmbracelet/log"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish/scp"
	"github.com/kierank/herald/config"
	"github.com/kierank/herald/ratelimit"
	"github.com/kierank/herald/scheduler"
	"github.com/kierank/herald/store"
)

type scpHandler struct {
	store       *store.DB
	scheduler   *scheduler.Scheduler
	logger      *log.Logger
	rateLimiter *ratelimit.Limiter
}

func (h *scpHandler) Glob(s ssh.Session, pattern string) ([]string, error) {
	user, ok := s.Context().Value("user").(*store.User)
	if !ok {
		return nil, fmt.Errorf("no user in context")
	}

	configs, err := h.store.ListConfigs(s.Context(), user.ID)
	if err != nil {
		return nil, err
	}

	var matches []string
	for _, cfg := range configs {
		matched, _ := filepath.Match(pattern, cfg.Filename)
		if matched || pattern == "*" || pattern == cfg.Filename {
			matches = append(matches, cfg.Filename)
		}
	}
	return matches, nil
}

func (h *scpHandler) WalkDir(s ssh.Session, path string, fn fs.WalkDirFunc) error {
	user, ok := s.Context().Value("user").(*store.User)
	if !ok {
		return fmt.Errorf("no user in context")
	}

	configs, err := h.store.ListConfigs(s.Context(), user.ID)
	if err != nil {
		return err
	}

	for _, cfg := range configs {
		info := &configFileInfo{cfg: cfg}
		if err := fn(cfg.Filename, &configDirEntry{info: info}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (h *scpHandler) NewDirEntry(s ssh.Session, name string) (*scp.DirEntry, error) {
	return nil, fmt.Errorf("directories not supported")
}

func (h *scpHandler) NewFileEntry(s ssh.Session, name string) (*scp.FileEntry, func() error, error) {
	user, ok := s.Context().Value("user").(*store.User)
	if !ok {
		return nil, nil, fmt.Errorf("no user in context")
	}

	cfg, err := h.store.GetConfig(s.Context(), user.ID, name)
	if err != nil {
		return nil, nil, fmt.Errorf("config not found: %w", err)
	}

	content := []byte(cfg.RawText)
	entry := &scp.FileEntry{
		Name:     cfg.Filename,
		Mode:     0644,
		Size:     int64(len(content)),
		Mtime:    cfg.CreatedAt.Unix(),
		Atime:    cfg.CreatedAt.Unix(),
		Reader:   bytes.NewReader(content),
		Filepath: cfg.Filename,
	}

	return entry, nil, nil
}

func (h *scpHandler) Mkdir(s ssh.Session, entry *scp.DirEntry) error {
	return fmt.Errorf("directories not supported")
}

func (h *scpHandler) Write(s ssh.Session, entry *scp.FileEntry) (int64, error) {
	h.logger.Debug("SCP Write called", "name", entry.Name, "size", entry.Size)
	
	user, ok := s.Context().Value("user").(*store.User)
	if !ok {
		return 0, fmt.Errorf("no user in context")
	}

	// Rate limit SCP uploads (per user)
	if !h.rateLimiter.Allow(fmt.Sprintf("scp:%d", user.ID)) {
		return 0, fmt.Errorf("rate limit exceeded, please try again later")
	}

	// Max file size: 1MB
	if entry.Size > 1024*1024 {
		return 0, fmt.Errorf("file too large (max 1MB)")
	}

	name := entry.Name
	if !strings.HasSuffix(name, ".txt") {
		return 0, fmt.Errorf("only .txt files are supported")
	}

	content, err := io.ReadAll(io.LimitReader(entry.Reader, 1024*1024))
	if err != nil {
		return 0, fmt.Errorf("failed to read file: %w", err)
	}

	parsed, err := config.Parse(string(content))
	if err != nil {
		return 0, fmt.Errorf("failed to parse config: %w", err)
	}

	if err := config.Validate(parsed); err != nil {
		return 0, fmt.Errorf("invalid config: %w", err)
	}

	ctx := s.Context()

	// Validate feed URLs by attempting to fetch them
	if err := config.ValidateFeedURLs(ctx, parsed); err != nil {
		return 0, fmt.Errorf("feed validation failed: %w", err)
	}

	nextRun, err := calculateNextRun(parsed.CronExpr)
	if err != nil {
		return 0, fmt.Errorf("failed to calculate next run: %w", err)
	}
	
	// Use transaction for config update
	tx, err := h.store.BeginTx(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback()

	if err := h.store.DeleteConfigTx(ctx, tx, user.ID, name); err != nil {
		h.logger.Debug("no existing config to delete", "filename", name)
	} else {
		h.logger.Debug("deleted existing config", "filename", name)
	}

	cfg, err := h.store.CreateConfigTx(ctx, tx, user.ID, name, parsed.Email, parsed.CronExpr, parsed.Digest, parsed.Inline, string(content), nextRun)
	if err != nil {
		return 0, fmt.Errorf("failed to save config: %w", err)
	}

	for _, feed := range parsed.Feeds {
		if _, err := h.store.CreateFeedTx(ctx, tx, cfg.ID, feed.URL, feed.Name); err != nil {
			return 0, fmt.Errorf("failed to save feed: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit transaction: %w", err)
	}

	h.logger.Info("config uploaded", "user_id", user.ID, "filename", name, "feeds", len(parsed.Feeds), "next_run", nextRun)
	return int64(len(content)), nil
}

func calculateNextRun(cronExpr string) (time.Time, error) {
	return gronx.NextTick(cronExpr, false)
}

type configFileInfo struct {
	cfg *store.Config
}

func (i *configFileInfo) Name() string       { return i.cfg.Filename }
func (i *configFileInfo) Size() int64        { return int64(len(i.cfg.RawText)) }
func (i *configFileInfo) Mode() fs.FileMode  { return 0644 }
func (i *configFileInfo) ModTime() time.Time { return i.cfg.CreatedAt }
func (i *configFileInfo) IsDir() bool        { return false }
func (i *configFileInfo) Sys() any           { return nil }

type configDirEntry struct {
	info *configFileInfo
}

func (e *configDirEntry) Name() string               { return e.info.Name() }
func (e *configDirEntry) IsDir() bool                { return false }
func (e *configDirEntry) Type() fs.FileMode          { return e.info.Mode() }
func (e *configDirEntry) Info() (fs.FileInfo, error) { return e.info, nil }
