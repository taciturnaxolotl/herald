package ssh

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"net"
	"path/filepath"
	"time"

	"charm.land/log/v2"
	"charm.land/ssh"
	"charm.land/wish/v2/scp"
	"github.com/adhocore/gronx"
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
		Mode:     0o644,
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

	// Rate limit uploads (per user)
	if !h.rateLimiter.Allow(fmt.Sprintf("upload:%d", user.ID)) {
		return 0, fmt.Errorf("rate limit exceeded, please try again later")
	}

	if entry.Size > maxUploadBytes {
		return 0, fmt.Errorf("file too large (max 1MB)")
	}

	content, err := io.ReadAll(io.LimitReader(entry.Reader, maxUploadBytes))
	if err != nil {
		return 0, fmt.Errorf("failed to read file: %w", err)
	}

	if err := ingestConfig(s.Context(), h.store, h.scheduler, h.logger, user, s.RemoteAddr(), entry.Name, string(content)); err != nil {
		return 0, err
	}
	return int64(len(content)), nil
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

func calculateNextRun(cronExpr string) (time.Time, error) {
	return gronx.NextTickAfter(cronExpr, time.Now().UTC(), true)
}

type configFileInfo struct {
	cfg *store.Config
}

func (i *configFileInfo) Name() string       { return i.cfg.Filename }
func (i *configFileInfo) Size() int64        { return int64(len(i.cfg.RawText)) }
func (i *configFileInfo) Mode() fs.FileMode  { return 0o644 }
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
