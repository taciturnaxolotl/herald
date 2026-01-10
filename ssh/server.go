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

	"github.com/charmbracelet/log"
	"github.com/charmbracelet/ssh"
	"github.com/charmbracelet/wish"
	"github.com/charmbracelet/wish/scp"
	"github.com/kierank/herald/scheduler"
	"github.com/kierank/herald/store"
	gossh "golang.org/x/crypto/ssh"
)

type Config struct {
	Host        string
	Port        int
	HostKeyPath string
	AllowAllKeys bool
	AllowedKeys  []string
}

type Server struct {
	cfg       Config
	store     *store.DB
	scheduler *scheduler.Scheduler
	logger    *log.Logger
}

func NewServer(cfg Config, st *store.DB, sched *scheduler.Scheduler, logger *log.Logger) *Server {
	return &Server{
		cfg:       cfg,
		store:     st,
		scheduler: sched,
		logger:    logger,
	}
}

func (s *Server) ListenAndServe(ctx context.Context) error {
	if err := s.ensureHostKey(); err != nil {
		return fmt.Errorf("failed to ensure host key: %w", err)
	}

	handler := &scpHandler{
		store:     s.store,
		scheduler: s.scheduler,
		logger:    s.logger,
	}

	srv, err := wish.NewServer(
		wish.WithAddress(fmt.Sprintf("%s:%d", s.cfg.Host, s.cfg.Port)),
		wish.WithHostKeyPath(s.cfg.HostKeyPath),
		wish.WithPublicKeyAuth(s.publicKeyHandler),
		wish.WithSubsystem("sftp", SFTPHandler(s.store, s.scheduler, s.logger)),
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
	fp := gossh.FingerprintSHA256(key)
	pubkeyStr := string(gossh.MarshalAuthorizedKey(key))

	if !s.cfg.AllowAllKeys {
		allowed := false
		for _, k := range s.cfg.AllowedKeys {
			if k == pubkeyStr {
				allowed = true
				break
			}
		}
		if !allowed {
			s.logger.Warn("rejected key", "fingerprint", fp)
			return false
		}
	}

	user, err := s.store.GetOrCreateUser(ctx, fp, pubkeyStr)
	if err != nil {
		s.logger.Error("failed to get/create user", "err", err)
		return false
	}

	ctx.SetValue("user", user)
	ctx.SetValue("fingerprint", fp)
	s.logger.Debug("authenticated user", "fingerprint", fp, "user_id", user.ID)
	return true
}

func (s *Server) commandMiddleware(next ssh.Handler) ssh.Handler {
	return func(sess ssh.Session) {
		cmd := sess.Command()
		s.logger.Debug("commandMiddleware", "cmd", cmd, "len", len(cmd))
		
		user, ok := sess.Context().Value("user").(*store.User)
		if !ok {
			fmt.Fprintln(sess, "Authentication error")
			return
		}

		// No command = interactive session (welcome message)
		if len(cmd) == 0 {
			s.handleWelcome(sess, user)
			return
		}
		
		// Check if it's an SCP command - let SCP middleware handle it
		if len(cmd) > 0 && cmd[0] == "scp" {
			s.logger.Debug("passing to SCP middleware")
			next(sess)
			return
		}

		// Handle our custom commands (ls, cat, rm, run, logs)
		HandleCommand(sess, user, s.store, s.scheduler, s.logger)
	}
}

func (s *Server) handleWelcome(sess ssh.Session, user *store.User) {
	fp := sess.Context().Value("fingerprint").(string)
	fmt.Fprintf(sess, "Welcome to Herald!\n\n")
	fmt.Fprintf(sess, "Your fingerprint: %s\n\n", fp)
	fmt.Fprintf(sess, "Upload a config with:\n")
	fmt.Fprintf(sess, "  scp feeds.txt %s:\n\n", sess.User())
	fmt.Fprintf(sess, "Commands:\n")
	fmt.Fprintf(sess, "  ls              List your configs\n")
	fmt.Fprintf(sess, "  cat <file>      Show config contents\n")
	fmt.Fprintf(sess, "  rm <file>       Delete a config\n")
	fmt.Fprintf(sess, "  run <file>      Run a config now\n")
	fmt.Fprintf(sess, "  logs            Show recent activity\n")
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
	if err := os.WriteFile(s.cfg.HostKeyPath, pemBlock, 0600); err != nil {
		return fmt.Errorf("failed to write host key: %w", err)
	}

	return nil
}
