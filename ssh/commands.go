package ssh

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"charm.land/lipgloss/v2"
	"charm.land/ssh"
	"github.com/taciturnaxolotl/herald/scheduler"
	"github.com/taciturnaxolotl/herald/store"
	"github.com/taciturnaxolotl/herald/verify"
)

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12"))
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
)

// out and outf write to the session, ignoring errors since dropped connections are expected.
func out(w io.Writer, args ...any) { _, _ = fmt.Fprintln(w, args...) }

func outf(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }

// cmdCtx is everything a command runs with.
type cmdCtx struct {
	ctx      context.Context
	sess     ssh.Session
	user     *store.User
	store    *store.DB
	sched    *scheduler.Scheduler
	verifier *verify.Service
}

type command struct {
	name, arg, help string
	run             func(c *cmdCtx, arg string)
}

// commands is the single list behind dispatch, usage errors and the help text.
var commands = []command{
	{"ls", "", "List your configs", runLs},
	{"cat", "<file>", "Show config contents", runCat},
	{"rm", "<file>", "Delete a config", runRm},
	{"activate", "<file>", "Enable a config", runActivate},
	{"deactivate", "<file>", "Disable a config", runDeactivate},
	{"run", "<file>", "Run a config now", runRun},
	{"verify", "<file>", "Resend the email confirmation", runVerify},
	{"logs", "", "Show recent activity", runLogs},
}

func commandHelp() string {
	var b strings.Builder
	for _, cmd := range commands {
		fmt.Fprintf(&b, "  %-20s %s\n", strings.TrimSpace(cmd.name+" "+cmd.arg), cmd.help)
	}
	return b.String()
}

func runCommand(c *cmdCtx, args []string) {
	for _, cmd := range commands {
		if cmd.name != args[0] {
			continue
		}
		arg := ""
		if cmd.arg != "" {
			if len(args) < 2 {
				out(c.sess, errorStyle.Render("Usage: "+cmd.name+" "+cmd.arg))
				return
			}
			arg = args[1]
		}
		cmd.run(c, arg)
		return
	}
	out(c.sess, errorStyle.Render("Unknown command: "+args[0]))
	outf(c.sess, "Commands:\n%s", commandHelp())
}

func (c *cmdCtx) config(filename string) (*store.Config, bool) {
	cfg, err := c.store.GetConfig(c.ctx, c.user.ID, filename)
	if err != nil {
		out(c.sess, errorStyle.Render("Config not found: "+filename))
		return nil, false
	}
	return cfg, true
}

// verifiedConfig is config plus the opt-in gate that scheduling and running share.
func (c *cmdCtx) verifiedConfig(filename string) (*store.Config, bool) {
	cfg, ok := c.config(filename)
	if !ok {
		return nil, false
	}
	if verified, err := c.store.IsEmailVerified(c.ctx, c.user.ID, cfg.Email); err != nil || !verified {
		out(c.sess, errorStyle.Render("Email not verified: "+cfg.Email))
		out(c.sess, dimStyle.Render("Run `verify "+filename+"` to get a fresh confirmation link."))
		return nil, false
	}
	return cfg, true
}

func runLs(c *cmdCtx, _ string) {
	configs, err := c.store.ListConfigs(c.ctx, c.user.ID)
	if err != nil {
		out(c.sess, errorStyle.Render("Error: "+err.Error()))
		return
	}
	if len(configs) == 0 {
		out(c.sess, dimStyle.Render("No configs found. Upload one with: scp feeds.txt <host>:"))
		return
	}

	ids := make([]int64, len(configs))
	for i, cfg := range configs {
		ids[i] = cfg.ID
	}
	feeds, err := c.store.GetFeedsByConfigs(c.ctx, ids)
	if err != nil {
		out(c.sess, errorStyle.Render("Error: "+err.Error()))
		return
	}

	out(c.sess, titleStyle.Render("Your configs:"))
	for _, cfg := range configs {
		nextRun := "never"
		if cfg.Active() {
			nextRun = formatRelativeTime(cfg.NextRun.Time)
		}
		marker := ""
		if verified, err := c.store.IsEmailVerified(c.ctx, c.user.ID, cfg.Email); err == nil && !verified {
			marker = errorStyle.Render(" (unverified, check email)")
		}
		outf(c.sess, "  %-20s %s  next: %s%s\n",
			cfg.Filename, dimStyle.Render(fmt.Sprintf("%d feed(s)", len(feeds[cfg.ID]))), nextRun, marker)
	}
}

func runCat(c *cmdCtx, filename string) {
	if cfg, ok := c.config(filename); ok {
		out(c.sess, titleStyle.Render("# "+filename))
		out(c.sess, cfg.RawText)
	}
}

func runRm(c *cmdCtx, filename string) {
	if err := c.store.DeleteConfig(c.ctx, c.user.ID, filename); err != nil {
		out(c.sess, errorStyle.Render("Error: "+err.Error()))
		return
	}
	out(c.sess, successStyle.Render("Deleted: "+filename))
}

func runActivate(c *cmdCtx, filename string) {
	cfg, ok := c.verifiedConfig(filename)
	if !ok {
		return
	}
	if err := c.store.ActivateConfig(c.ctx, cfg); err != nil {
		out(c.sess, errorStyle.Render("Error: "+err.Error()))
		return
	}
	out(c.sess, successStyle.Render("Activated: "+filename))
}

func runDeactivate(c *cmdCtx, filename string) {
	cfg, ok := c.config(filename)
	if !ok {
		return
	}
	if err := c.store.SetNextRun(c.ctx, cfg.ID, nil); err != nil {
		out(c.sess, errorStyle.Render("Error: "+err.Error()))
		return
	}
	out(c.sess, successStyle.Render("Deactivated: "+filename))
}

func runRun(c *cmdCtx, filename string) {
	cfg, ok := c.verifiedConfig(filename)
	if !ok {
		return
	}
	feeds, err := c.store.GetFeedsByConfig(c.ctx, cfg.ID)
	if err != nil {
		out(c.sess, errorStyle.Render("Error: "+err.Error()))
		return
	}

	var progress atomic.Int32
	stop := startSpinner(c.sess, &progress, len(feeds))
	stats, err := c.sched.RunNow(c.ctx, cfg.ID, &progress)
	stop()

	if err != nil {
		out(c.sess, errorStyle.Render("Error: "+err.Error()))
		return
	}
	if stats.FailedFeeds > 0 {
		outf(c.sess, "%s Fetched %d/%d feeds (%d failed)\n", dimStyle.Render("⚠"), stats.FetchedFeeds, stats.TotalFeeds, stats.FailedFeeds)
	} else {
		outf(c.sess, "%s Fetched %d/%d feeds\n", successStyle.Render("✓"), stats.FetchedFeeds, stats.TotalFeeds)
	}
	switch {
	case stats.NewItems == 0:
		out(c.sess, dimStyle.Render("No new items found."))
	case stats.EmailSent:
		out(c.sess, successStyle.Render(fmt.Sprintf("Sent %d new item(s) to %s", stats.NewItems, cfg.Email)))
	default:
		out(c.sess, dimStyle.Render(fmt.Sprintf("Found %d new item(s) but did not send email", stats.NewItems)))
	}
}

// startSpinner draws fetch progress until the returned stop is called, which also clears the line.
func startSpinner(w io.Writer, progress *atomic.Int32, total int) (stop func()) {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		for i := 0; ; i++ {
			outf(w, "\r%s Fetching feeds... %d/%d", frames[i%len(frames)], progress.Load(), total)
			select {
			case <-done:
				return
			case <-ticker.C:
			}
		}
	}()
	return func() {
		close(done)
		<-finished
		outf(w, "\r\033[K")
	}
}

// runVerify asks again for a confirmation email after the first is lost or expired.
func runVerify(c *cmdCtx, filename string) {
	cfg, ok := c.config(filename)
	if !ok {
		return
	}
	ipKey := ""
	if addr := c.sess.RemoteAddr(); addr != nil {
		ipKey = rateLimitIPKey(addr)
	}

	status, err := c.verifier.Request(c.ctx, c.user.ID, cfg.Email, c.user.PubkeyFP, ipKey)
	if err != nil {
		out(c.sess, errorStyle.Render("Could not send confirmation: "+err.Error()))
		return
	}
	switch status {
	case verify.AlreadyVerified:
		out(c.sess, successStyle.Render(cfg.Email+" is already verified."))
	case verify.Sent:
		out(c.sess, successStyle.Render("Confirmation sent to "+cfg.Email))
	case verify.Pending:
		out(c.sess, dimStyle.Render("A confirmation was sent to "+cfg.Email+" recently. Check that mailbox, including spam."))
	case verify.Throttled:
		out(c.sess, errorStyle.Render("Too many confirmation requests. Try again tomorrow."))
	}
}

func runLogs(c *cmdCtx, _ string) {
	logs, err := c.store.GetRecentLogs(c.ctx, c.user.ID, 20)
	if err != nil {
		out(c.sess, errorStyle.Render("Error: "+err.Error()))
		return
	}
	if len(logs) == 0 {
		out(c.sess, dimStyle.Render("No logs yet."))
		return
	}

	out(c.sess, titleStyle.Render("Recent activity:"))
	for _, l := range logs {
		levelStyle := dimStyle
		switch strings.ToLower(l.Level) {
		case "error":
			levelStyle = errorStyle
		case "info":
			levelStyle = successStyle
		}
		outf(c.sess, "  %s  %s  %s\n",
			dimStyle.Render(l.CreatedAt.Format("Jan 02 15:04")), levelStyle.Render("["+l.Level+"]"), l.Message)
	}
}

func formatRelativeTime(t time.Time) string {
	diff := time.Until(t)
	switch {
	case diff < 0:
		return "overdue"
	case diff < time.Minute:
		return "< 1 min"
	case diff < time.Hour:
		return fmt.Sprintf("%d min", int(diff.Minutes()))
	case diff < 24*time.Hour:
		return fmt.Sprintf("%d hr", int(diff.Hours()))
	default:
		return fmt.Sprintf("%d day(s)", int(diff.Hours()/24))
	}
}
