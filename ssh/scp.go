package ssh

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"time"

	"charm.land/ssh"
	"charm.land/wish/v2/scp"
	"github.com/taciturnaxolotl/herald/store"
)

type scpHandler struct {
	*uploader
}

func (h *scpHandler) Glob(s ssh.Session, pattern string) ([]string, error) {
	user, err := sessionUser(s, h.store)
	if err != nil {
		return nil, err
	}

	configs, err := h.store.ListConfigs(s.Context(), user.ID)
	if err != nil {
		return nil, err
	}

	var matches []string
	for _, cfg := range configs {
		if matched, _ := filepath.Match(pattern, cfg.Filename); matched {
			matches = append(matches, cfg.Filename)
		}
	}
	return matches, nil
}

func (h *scpHandler) WalkDir(s ssh.Session, path string, fn fs.WalkDirFunc) error {
	user, err := sessionUser(s, h.store)
	if err != nil {
		return err
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
	user, err := sessionUser(s, h.store)
	if err != nil {
		return nil, nil, err
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

	user, err := sessionUser(s, h.store)
	if err != nil {
		return 0, err
	}

	// Read one byte past the cap so ingest's size check sees oversize files.
	content, err := io.ReadAll(io.LimitReader(entry.Reader, maxUploadBytes+1))
	if err != nil {
		return 0, fmt.Errorf("failed to read file: %w", err)
	}

	if err := h.upload(s, user, entry.Name, string(content)); err != nil {
		// Close first so the middleware's own unformatted copy of err is dropped.
		_ = s.Exit(1)
		_ = s.Close()
		return 0, err
	}
	return int64(len(content)), nil
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
