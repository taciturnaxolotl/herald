package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/charmbracelet/fang"
	"github.com/charmbracelet/log"
	"github.com/kierank/herald/config"
	"github.com/kierank/herald/email"
	"github.com/kierank/herald/scheduler"
	"github.com/kierank/herald/ssh"
	"github.com/kierank/herald/store"
	"github.com/kierank/herald/web"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

var (
	version   = "dev"
	cfgFile   string
	logger    *log.Logger
)

func main() {
	logger = log.NewWithOptions(os.Stderr, log.Options{
		ReportTimestamp: true,
		Level:           log.InfoLevel,
	})

	rootCmd := &cobra.Command{
		Use:   "herald",
		Short: "RSS-to-Email via SSH",
		Long: `Herald is a minimal, SSH-powered RSS to email service.
Upload a feed config via SCP, get email digests on a schedule.`,
		Version: version,
	}

	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file path")

	rootCmd.AddCommand(serveCmd())
	rootCmd.AddCommand(initCmd())

	if err := fang.Execute(
		context.Background(),
		rootCmd,
		fang.WithNotifySignal(os.Interrupt, os.Kill),
	); err != nil {
		os.Exit(1)
	}
}

func serveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Start the Herald server",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServer(cmd.Context())
		},
	}
}

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init [config_path]",
		Short: "Generate a sample configuration file",
		Long:  "Create a config.yaml file with default values. If no path is provided, uses config.yaml",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "config.yaml"
			if len(args) > 0 {
				path = args[0]
			}

			if _, err := os.Stat(path); err == nil {
				return fmt.Errorf("config file already exists at %s", path)
			}

			sampleConfig := `# Herald Configuration

host: 0.0.0.0
ssh_port: 2222
http_port: 8080

# Public URL where Herald is accessible
origin: http://localhost:8080

# External SSH port (defaults to ssh_port if not set)
# Use this when SSH is exposed through a different port publicly
# external_ssh_port: 22

# SSH host keys (generated on first run if missing)
host_key_path: ./host_key

# Database
db_path: ./herald.db

# SMTP
smtp:
  host: smtp.example.com
  port: 587
  user: sender@example.com
  pass: ${SMTP_PASS}  # Env var substitution
  from: herald@example.com

# Auth
allow_all_keys: true
# allowed_keys:
#   - "ssh-ed25519 AAAA... user@host"
`

			if err := os.WriteFile(path, []byte(sampleConfig), 0644); err != nil {
				return fmt.Errorf("failed to write config file: %w", err)
			}

			logger.Info("created config file", "path", path)
			return nil
		},
	}
}

func runServer(ctx context.Context) error {
	cfg, err := config.LoadAppConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	logger.Info("starting herald",
		"ssh_port", cfg.SSHPort,
		"http_port", cfg.HTTPPort,
		"db_path", cfg.DBPath,
	)

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}
	defer db.Close()

	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("failed to migrate database: %w", err)
	}

	mailer := email.NewMailer(email.SMTPConfig{
		Host: cfg.SMTP.Host,
		Port: cfg.SMTP.Port,
		User: cfg.SMTP.User,
		Pass: cfg.SMTP.Pass,
		From: cfg.SMTP.From,
	}, cfg.Origin)

	sched := scheduler.NewScheduler(db, mailer, logger, 60*time.Second, cfg.Origin)

	sshServer := ssh.NewServer(ssh.Config{
		Host:         cfg.Host,
		Port:         cfg.SSHPort,
		HostKeyPath:  cfg.HostKeyPath,
		AllowAllKeys: cfg.AllowAllKeys,
		AllowedKeys:  cfg.AllowedKeys,
	}, db, sched, logger)

	webServer := web.NewServer(db, fmt.Sprintf("%s:%d", cfg.Host, cfg.HTTPPort), cfg.Origin, cfg.ExternalSSHPort, logger)

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		return sshServer.ListenAndServe(ctx)
	})

	g.Go(func() error {
		return webServer.ListenAndServe(ctx)
	})

	g.Go(func() error {
		sched.Start(ctx)
		return nil
	})

	return g.Wait()
}
