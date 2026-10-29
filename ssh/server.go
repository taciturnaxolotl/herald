// Package ssh provides functionality for Herald.
package ssh

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"time"

	"charm.land/log/v2"
	"charm.land/ssh"
	"charm.land/wish/v2"
	"charm.land/wish/v2/scp"
	"github.com/taciturnaxolotl/herald/ratelimit"
	"github.com/taciturnaxolotl/herald/scheduler"
	"github.com/taciturnaxolotl/herald/store"
	"github.com/taciturnaxolotl/herald/verify"
	gossh "golang.org/x/crypto/ssh"
)

type Config struct {
	Host         string
	Port         int
	HostKeyPath  string
	AllowAllKeys bool
	AllowedKeys  []string
}

type Server struct {
	cfg         Config
	allowedFPs  map[string]bool
	store       *store.DB
	scheduler   *scheduler.Scheduler
	verifier    *verify.Service
	logger      *log.Logger
	rateLimiter *ratelimit.Limiter
}

func NewServer(cfg Config, st *store.DB, sched *scheduler.Scheduler, verifier *verify.Service, logger *log.Logger) *Server {
	return &Server{
		cfg:         cfg,
		allowedFPs:  allowedFingerprints(cfg.AllowedKeys, logger),
		store:       st,
		scheduler:   sched,
		verifier:    verifier,
		logger:      logger,
		rateLimiter: ratelimit.New(5, 10), // 5 req/sec, burst of 10 for SSH/SCP
	}
}

// allowedFingerprints parses authorized_keys-style entries, comments included.
func allowedFingerprints(keys []string, logger *log.Logger) map[string]bool {
	fps := make(map[string]bool, len(keys))
	for _, k := range keys {
		key, _, _, _, err := gossh.ParseAuthorizedKey([]byte(k))
		if err != nil {
			logger.Warn("ignoring unparseable allowed key", "key", k, "err", err)
			continue
		}
		fps[gossh.FingerprintSHA256(key)] = true
	}
	return fps
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	if err := s.ensureHostKey(); err != nil {
		return fmt.Errorf("failed to ensure host key: %w", err)
	}

	up := &uploader{
		store:       s.store,
		verifier:    s.verifier,
		logger:      s.logger,
		rateLimiter: s.rateLimiter,
	}
	handler := &scpHandler{uploader: up}

	srv, err := wish.NewServer(
		wish.WithAddress(fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)),
		wish.WithHostKeyPath(s.cfg.HostKeyPath),
		wish.WithPublicKeyAuth(s.publicKeyHandler),
		wish.WithSubsystem("sftp", SFTPHandler(up)),
		wish.WithMiddleware(
			scp.Middleware(handler, handler),
			s.commandMiddleware,
		),
	)
	if err != nil {
		return fmt.Errorf("failed to create SSH server: %w", err)
	}

	s.logger.Info("SSH server starting", "addr", fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port))

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		s.logger.Info("shutting down SSH server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, ssh.ErrServerClosed) {
			return err
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, ssh.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) publicKeyHandler(ctx ssh.Context, key ssh.PublicKey) bool {
	fp := store.FingerprintSlug(gossh.FingerprintSHA256(key))

	// Rate limit authentication attempts by fingerprint
	if !s.rateLimiter.Allow(fmt.Sprintf("auth:%s", fp)) {
		s.logger.Warn("rate limit exceeded for auth", "fingerprint", fp)
		return false
	}

	if !s.cfg.AllowAllKeys && !s.allowedFPs[gossh.FingerprintSHA256(key)] {
		s.logger.Warn("rejected key", "fingerprint", fp)
		return false
	}

	return true
}

type sessionUserKey struct{}

// sessionUser loads (creating on first login) the account for the key the
// session actually authenticated with. The auth callback also runs for
// unsigned key queries and its results are cached, so it must not decide
// identity or create accounts.
func sessionUser(sess ssh.Session, st *store.DB) (*store.User, error) {
	ctx := sess.Context()
	if user, ok := ctx.Value(sessionUserKey{}).(*store.User); ok {
		return user, nil
	}
	key := sess.PublicKey()
	if key == nil {
		return nil, errors.New("session has no authenticated public key")
	}
	fp := store.FingerprintSlug(gossh.FingerprintSHA256(key))
	user, err := st.GetOrCreateUser(ctx, fp, string(gossh.MarshalAuthorizedKey(key)))
	if err != nil {
		return nil, fmt.Errorf("load user: %w", err)
	}
	ctx.SetValue(sessionUserKey{}, user)
	return user, nil
}

func (s *Server) commandMiddleware(next ssh.Handler) ssh.Handler {
	return func(sess ssh.Session) {
		cmd := sess.Command()
		s.logger.Debug("commandMiddleware", "cmd", cmd, "len", len(cmd))

		user, err := sessionUser(sess, s.store)
		if err != nil {
			s.logger.Error("failed to load session user", "err", err)
			println(sess, "Authentication error")
			return
		}

		// No command = interactive session (welcome message)
		if len(cmd) == 0 {
			s.handleWelcome(sess, user)
			return
		}

		// scp is handled by the SCP middleware (and default scp uses the sftp
		// subsystem, handled separately).
		if cmd[0] == "scp" {
			s.logger.Debug("passing to SCP middleware")
			next(sess)
			return
		}

		// rsync is not supported: Herald has no real filesystem, uploads are
		// parsed into config records. Fail with a clear hint instead of the
		// cryptic "unexpected end of file" the client gets when the server
		// never speaks the rsync protocol.
		if cmd[0] == "rsync" {
			printf(sess.Stderr(), "herald: rsync is not supported. Upload with scp instead:\n  scp <file>.txt %s:\n", sess.User())
			_ = sess.Exit(2)
			return
		}

		// Handle our custom commands (ls, cat, rm, run, logs)
		HandleCommand(sess, user, s.store, s.scheduler, s.verifier, s.logger)
	}
}

func (s *Server) handleWelcome(sess ssh.Session, user *store.User) {
	printf(sess, "Welcome to Herald!\n\n")
	printf(sess, "Your fingerprint: %s\n\n", user.PubkeyFP)
	printf(sess, "Upload a config with:\n")
	printf(sess, "  scp feeds.txt %s:\n\n", sess.User())
	printf(sess, "Commands:\n")
	printf(sess, "  ls                   List your configs\n")
	printf(sess, "  cat <file>           Show config contents\n")
	printf(sess, "  rm <file>            Delete a config\n")
	printf(sess, "  activate <file>      Enable a config\n")
	printf(sess, "  deactivate <file>    Disable a config\n")
	printf(sess, "  run <file>           Run a config now\n")
	printf(sess, "  logs                 Show recent activity\n")
}

func (s *Server) ensureHostKey() error {
	if _, err := os.Stat(s.cfg.HostKeyPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	s.logger.Info("generating new host key", "path", s.cfg.HostKeyPath)

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("failed to generate key: %w", err)
	}

	privBytes, err := gossh.MarshalPrivateKey(priv, "")
	if err != nil {
		return fmt.Errorf("failed to marshal private key: %w", err)
	}

	pemBlock := pem.EncodeToMemory(privBytes)
	if err := os.WriteFile(s.cfg.HostKeyPath, pemBlock, 0o600); err != nil {
		return fmt.Errorf("failed to write host key: %w", err)
	}

	return nil
}
