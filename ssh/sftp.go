package ssh

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"charm.land/log/v2"
	"charm.land/ssh"
	"github.com/kierank/herald/ratelimit"
	"github.com/kierank/herald/scheduler"
	"github.com/kierank/herald/store"
	"github.com/pkg/sftp"
)

func SFTPHandler(st *store.DB, sched *scheduler.Scheduler, logger *log.Logger, rateLimiter *ratelimit.Limiter) func(ssh.Session) {
	return func(s ssh.Session) {
		user, ok := s.Context().Value("user").(*store.User)
		if !ok {
			logger.Error("SFTP: no user in context")
			return
		}

		handler := &sftpHandler{
			store:       st,
			scheduler:   sched,
			logger:      logger,
			user:        user,
			session:     s,
			rateLimiter: rateLimiter,
		}

		server := sftp.NewRequestServer(s, sftp.Handlers{
			FileGet:  handler,
			FilePut:  handler,
			FileCmd:  handler,
			FileList: handler,
		})

		// Send the SSH exit-status BEFORE closing the sftp server. Closing it
		// tears down the ssh channel, and if that happens first the library
		// cannot deliver exit-status -- which makes scp report a bogus exit -1
		// even though the transfer fully succeeded.
		err := server.Serve()
		code := 0
		if err != nil && !errors.Is(err, io.EOF) {
			logger.Error("SFTP server error", "err", err)
			code = 1
		}
		_ = s.Exit(code)
		_ = server.Close()
	}
}

type sftpHandler struct {
	store       *store.DB
	scheduler   *scheduler.Scheduler
	logger      *log.Logger
	user        *store.User
	session     ssh.Session
	rateLimiter *ratelimit.Limiter
}

// Fileread for downloads
func (h *sftpHandler) Fileread(r *sftp.Request) (io.ReaderAt, error) {
	filename := strings.TrimPrefix(r.Filepath, "/")
	if filename == "" || filename == "." {
		return nil, fmt.Errorf("invalid path")
	}

	cfg, err := h.store.GetConfig(h.session.Context(), h.user.ID, filename)
	if err != nil {
		return nil, fmt.Errorf("config not found: %w", err)
	}

	return &bytesReaderAt{data: []byte(cfg.RawText)}, nil
}

// Filewrite for uploads
func (h *sftpHandler) Filewrite(r *sftp.Request) (io.WriterAt, error) {
	filename := strings.TrimPrefix(r.Filepath, "/")
	if filename == "" || filename == "." {
		return nil, fmt.Errorf("invalid filename")
	}

	if !strings.HasSuffix(filename, ".txt") {
		return nil, fmt.Errorf("only .txt files are supported")
	}

	// Rate limit uploads (per user), matching the scp path.
	if h.rateLimiter != nil && !h.rateLimiter.Allow(fmt.Sprintf("upload:%d", h.user.ID)) {
		return nil, fmt.Errorf("rate limit exceeded, please try again later")
	}

	h.logger.Debug("SFTP write", "filename", filename, "user_id", h.user.ID)

	return &configWriter{
		handler:  h,
		filename: filename,
		buffer:   []byte{},
	}, nil
}

// Filecmd handles file operations
func (h *sftpHandler) Filecmd(r *sftp.Request) error {
	filename := strings.TrimPrefix(r.Filepath, "/")

	switch r.Method {
	case "Setstat":
		// Allow setstat (used by scp)
		return nil
	case "Remove":
		if filename == "" || filename == "." {
			return fmt.Errorf("invalid filename")
		}
		return h.store.DeleteConfig(h.session.Context(), h.user.ID, filename)
	case "Rename":
		return fmt.Errorf("rename not supported")
	case "Mkdir", "Rmdir":
		return fmt.Errorf("directories not supported")
	default:
		return sftp.ErrSSHFxOpUnsupported
	}
}

// Filelist for directory listings
func (h *sftpHandler) Filelist(r *sftp.Request) (sftp.ListerAt, error) {
	switch r.Method {
	case "List":
		configs, err := h.store.ListConfigs(h.session.Context(), h.user.ID)
		if err != nil {
			return nil, err
		}
		infos := make([]fs.FileInfo, len(configs))
		for i, cfg := range configs {
			infos[i] = &configFileInfo{cfg: cfg}
		}
		return listerAt(infos), nil
	case "Stat":
		filename := strings.TrimPrefix(r.Filepath, "/")
		if filename == "" || filename == "." || filename == "/" {
			// Return root directory info
			return listerAt{&dirInfo{}}, nil
		}
		cfg, err := h.store.GetConfig(h.session.Context(), h.user.ID, filename)
		if err != nil {
			return nil, err
		}
		return listerAt{&configFileInfo{cfg: cfg}}, nil
	default:
		return nil, sftp.ErrSSHFxOpUnsupported
	}
}

type configWriter struct {
	handler  *sftpHandler
	filename string
	buffer   []byte
}

func (w *configWriter) WriteAt(p []byte, off int64) (int, error) {
	// Expand buffer if needed
	needed := int(off) + len(p)
	if needed > len(w.buffer) {
		newBuf := make([]byte, needed)
		copy(newBuf, w.buffer)
		w.buffer = newBuf
	}
	copy(w.buffer[off:], p)
	return len(p), nil
}

func (w *configWriter) Close() error {
	// Same ingest path as scp, so default scp (which uses sftp) enforces the
	// identical validation, feed-URL checks and confirmed-opt-in gating.
	return ingestConfig(
		w.handler.session.Context(),
		w.handler.store,
		w.handler.scheduler,
		w.handler.logger,
		w.handler.user,
		w.handler.session.RemoteAddr(),
		w.filename,
		string(w.buffer),
	)
}

type bytesReaderAt struct {
	data []byte
}

func (r *bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(r.data)) {
		return 0, io.EOF
	}
	n := copy(p, r.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

type listerAt []fs.FileInfo

func (l listerAt) ListAt(ls []fs.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l)) {
		return 0, io.EOF
	}
	n := copy(ls, l[offset:])
	if n < len(ls) {
		return n, io.EOF
	}
	return n, nil
}

type dirInfo struct{}

func (d *dirInfo) Name() string       { return "." }
func (d *dirInfo) Size() int64        { return 0 }
func (d *dirInfo) Mode() fs.FileMode  { return fs.ModeDir | 0755 }
func (d *dirInfo) ModTime() time.Time { return time.Now() }
func (d *dirInfo) IsDir() bool        { return true }
func (d *dirInfo) Sys() any           { return nil }
