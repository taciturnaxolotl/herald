package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"charm.land/log/v2"
	"github.com/charmbracelet/fang"
	"github.com/spf13/cobra"
	"github.com/taciturnaxolotl/herald/config"
	"github.com/taciturnaxolotl/herald/email"
	"github.com/taciturnaxolotl/herald/scheduler"
	"github.com/taciturnaxolotl/herald/ssh"
	"github.com/taciturnaxolotl/herald/store"
	"github.com/taciturnaxolotl/herald/verify"
	"github.com/taciturnaxolotl/herald/web"
	"golang.org/x/sync/errgroup"
)

//go:embed config.example.yaml
var sampleConfig []byte

var (
	version    = "dev"
	commitHash = "dev"
	cfgFile    string
	logger     *log.Logger
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
	rootCmd.AddCommand(previewCmd())

	if err := fang.Execute(
		context.Background(),
		rootCmd,
		fang.WithVersion(version),
		fang.WithCommit(commitHash),
		fang.WithNotifySignal(os.Interrupt, syscall.SIGTERM),
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

			if err := os.WriteFile(path, sampleConfig, 0o600); err != nil {
				return fmt.Errorf("failed to write config file: %w", err)
			}

			logger.Info("created config file", "path", path)
			return nil
		},
	}
}

func previewCmd() *cobra.Command {
	var inline bool

	cmd := &cobra.Command{
		Use:   "preview [feed_url]",
		Short: "Fetch an RSS feed and render email preview to HTML",
		Long: `Fetch the given RSS feed URL, render it through Herald's email pipeline,
write to a temp file, and open it in the browser.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := os.CreateTemp("", "herald-preview-*.html")
			if err != nil {
				return fmt.Errorf("create temp file: %w", err)
			}
			outputFile := f.Name()
			_ = f.Close()

			if err := runPreview(cmd.Context(), args[0], outputFile, inline); err != nil {
				return err
			}
			opener := "xdg-open"
			if runtime.GOOS == "darwin" {
				opener = "open"
			}
			return exec.Command(opener, outputFile).Run() //nolint:gosec // outputFile is from os.CreateTemp, not user-controlled
		},
	}

	cmd.Flags().BoolVarP(&inline, "inline", "i", true, "render inline content (false = summary only)")

	return cmd
}

func runPreview(ctx context.Context, feedURL, outputFile string, inline bool) error {
	fetched := scheduler.FetchFeed(ctx, &store.Feed{URL: feedURL})
	if fetched.Error != nil {
		return fmt.Errorf("fetch feed: %w", fetched.Error)
	}

	group := scheduler.DigestGroup(fetched, nil, time.Now())
	if len(group.Items) == 0 {
		return fmt.Errorf("no recent items found in feed")
	}
	data := &email.DigestData{
		ConfigName: "preview",
		TotalItems: len(group.Items),
		FeedGroups: []email.FeedGroup{group},
	}

	htmlOutput, _, err := email.RenderDigest(data, inline, store.InactivityDays)
	if err != nil {
		return fmt.Errorf("render digest: %w", err)
	}

	gmailOutput, err := email.WrapForGMailPreview(htmlOutput, email.DigestSubject(data))
	if err != nil {
		return fmt.Errorf("gmail wrap: %w", err)
	}

	if err := os.WriteFile(outputFile, []byte(gmailOutput), 0o600); err != nil {
		return fmt.Errorf("write output: %w", err)
	}
	logger.Info("preview written", "file", outputFile, "items", len(group.Items))
	return nil
}

func runServer(ctx context.Context) error {
	cfg, err := config.LoadAppConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if level, err := log.ParseLevel(cfg.LogLevel); err == nil {
		logger.SetLevel(level)
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
	defer func() { _ = db.Close() }()

	mailer, err := email.NewMailer(email.SMTPConfig(cfg.SMTP))
	if err != nil {
		return fmt.Errorf("failed to create mailer: %w", err)
	}

	// Validate SMTP configuration
	if err := mailer.ValidateConfig(); err != nil {
		return fmt.Errorf("SMTP validation failed: %w", err)
	}

	sched := scheduler.NewScheduler(db, mailer, logger, 60*time.Second, cfg.Origin)

	sshServer := ssh.NewServer(ssh.Config{
		Host:         cfg.Host,
		Port:         cfg.SSHPort,
		HostKeyPath:  cfg.HostKeyPath,
		AllowAllKeys: cfg.AllowAllKeys,
		AllowedKeys:  cfg.AllowedKeys,
	}, db, sched, verify.New(db, mailer, logger, cfg.Origin), logger)

	// Get commit hash - prefer build-time embedded hash, fallback to git
	hash := commitHash
	if hash == "" || hash == "dev" {
		cmd := exec.Command("git", "log", "-1", "--format=%H")
		if output, err := cmd.Output(); err == nil {
			hash = strings.TrimSpace(string(output))
		} else {
			hash = "dev"
		}
	}

	webServer := web.NewServer(db, fmt.Sprintf("%s:%d", cfg.Host, cfg.HTTPPort), cfg.Origin, cfg.ExternalSSHPort, logger, hash)

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		return sshServer.ListenAndServe(ctx)
	})

	g.Go(func() error {
		return webServer.ListenAndServe(ctx)
	})

	g.Go(func() (err error) {
		// A dead scheduler must stop the process rather than leave the servers running without it.
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("scheduler panic: %v", r)
			}
		}()
		sched.Start(ctx)
		return nil
	})

	return g.Wait()
}
