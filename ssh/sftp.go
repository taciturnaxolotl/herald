package ssh

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"charm.land/ssh"
	"github.com/pkg/sftp"
	"github.com/taciturnaxolotl/herald/store"
)

func SFTPHandler(up *uploader) func(ssh.Session) {
	return func(s ssh.Session) {
		user, err := sessionUser(s, up.store)
		if err != nil {
			up.logger.Error("SFTP: failed to load session user", "err", err)
			return
		}

		handler := &sftpHandler{uploader: up, user: user, session: s}

		server := sftp.NewRequestServer(s, sftp.Handlers{
			FileGet:  handler,
			FilePut:  handler,
			FileCmd:  handler,
			FileList: handler,
		})

		// Exit before Close: closing tears down the channel, losing exit-status (scp shows -1).
		err = server.Serve()
		code := 0
		if err != nil && !errors.Is(err, io.EOF) {
			up.logger.Error("SFTP server error", "err", err)
			code = 1
		}
		_ = s.Exit(code)
		_ = server.Close()
	}
}

type sftpHandler struct {
	*uploader
	user    *store.User
	session ssh.Session
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

// WriteAt keeps at most one byte past the cap, letting ingest reject oversize files with a readable error.
func (w *configWriter) WriteAt(p []byte, off int64) (int, error) {
	const limit = maxUploadBytes + 1
	if off < 0 {
		return 0, fmt.Errorf("negative offset")
	}
	if off >= limit {
		return len(p), nil
	}
	kept := p[:min(int64(len(p)), limit-off)]
	if needed := int(off) + len(kept); needed > len(w.buffer) {
		w.buffer = append(w.buffer, make([]byte, needed-len(w.buffer))...)
	}
	copy(w.buffer[off:], kept)
	return len(p), nil
}

// Close ingests the upload; OpenSSH shows a failed close only as "Failure", so upload also reports to stderr.
func (w *configWriter) Close() error {
	return w.handler.upload(w.handler.session, w.handler.user, w.filename, string(w.buffer))
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
func (d *dirInfo) Mode() fs.FileMode  { return fs.ModeDir | 0o755 }
func (d *dirInfo) ModTime() time.Time { return time.Now() }
func (d *dirInfo) IsDir() bool        { return true }
func (d *dirInfo) Sys() any           { return nil }
