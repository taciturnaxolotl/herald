package ssh

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/log"
	"github.com/charmbracelet/ssh"
	"github.com/kierank/herald/scheduler"
	"github.com/kierank/herald/store"
)

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("12"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("8"))

	successStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("10"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("9"))
)

// print writes to the session, ignoring errors (connection drops are expected)
func print(w io.Writer, args ...interface{}) {
	_, _ = fmt.Fprint(w, args...)
}

// printf writes formatted output to the session, ignoring errors
func printf(w io.Writer, format string, args ...interface{}) {
	_, _ = fmt.Fprintf(w, format, args...)
}

// println writes a line to the session, ignoring errors
func println(w io.Writer, args ...interface{}) {
	_, _ = fmt.Fprintln(w, args...)
}

func HandleCommand(sess ssh.Session, user *store.User, st *store.DB, sched *scheduler.Scheduler, logger *log.Logger) {
	cmd := sess.Command()
	if len(cmd) == 0 {
		return
	}

	ctx := context.Background()

	switch cmd[0] {
	case "ls":
		handleLs(ctx, sess, user, st)
	case "cat":
		if len(cmd) < 2 {
			println(sess, errorStyle.Render("Usage: cat <filename>"))
			return
		}
		handleCat(ctx, sess, user, st, cmd[1])
	case "rm":
		if len(cmd) < 2 {
			println(sess, errorStyle.Render("Usage: rm <filename>"))
			return
		}
		handleRm(ctx, sess, user, st, cmd[1])
	case "activate":
		if len(cmd) < 2 {
			println(sess, errorStyle.Render("Usage: activate <filename>"))
			return
		}
		handleActivate(ctx, sess, user, st, cmd[1])
	case "deactivate":
		if len(cmd) < 2 {
			println(sess, errorStyle.Render("Usage: deactivate <filename>"))
			return
		}
		handleDeactivate(ctx, sess, user, st, cmd[1])
	case "run":
		if len(cmd) < 2 {
			println(sess, errorStyle.Render("Usage: run <filename>"))
			return
		}
		handleRun(ctx, sess, user, st, sched, cmd[1])
	case "logs":
		handleLogs(ctx, sess, user, st)
	default:
		printf(sess, errorStyle.Render("Unknown command: %s\n"), cmd[0])
		println(sess, "Available commands: ls, cat, rm, activate, deactivate, run, logs")
	}
}

func handleLs(ctx context.Context, sess ssh.Session, user *store.User, st *store.DB) {
	configs, err := st.ListConfigs(ctx, user.ID)
	if err != nil {
		println(sess, errorStyle.Render("Error: "+err.Error()))
		return
	}

	if len(configs) == 0 {
		println(sess, dimStyle.Render("No configs found. Upload one with: scp feeds.txt <host>:"))
		return
	}

	println(sess, titleStyle.Render("Your configs:"))

	for _, cfg := range configs {
		feeds, err := st.GetFeedsByConfig(ctx, cfg.ID)
		feedCount := 0
		if err == nil {
			feedCount = len(feeds)
		}

		nextRunStr := "never"
		if cfg.NextRun.Valid {
			nextRunStr = formatRelativeTime(cfg.NextRun.Time)
		}

		printf(sess, "  %-20s %s  next: %s\n",
			cfg.Filename,
			dimStyle.Render(fmt.Sprintf("%d feed(s)", feedCount)),
			nextRunStr,
		)
	}
}

func handleCat(ctx context.Context, sess ssh.Session, user *store.User, st *store.DB, filename string) {
	cfg, err := st.GetConfig(ctx, user.ID, filename)
	if err != nil {
		println(sess, errorStyle.Render("Config not found: "+filename))
		return
	}

	println(sess, titleStyle.Render("# "+filename))
	println(sess, cfg.RawText)
}

func handleRm(ctx context.Context, sess ssh.Session, user *store.User, st *store.DB, filename string) {
	err := st.DeleteConfig(ctx, user.ID, filename)
	if err != nil {
		println(sess, errorStyle.Render("Error: "+err.Error()))
		return
	}

	println(sess, successStyle.Render("Deleted: "+filename))
}

func handleActivate(ctx context.Context, sess ssh.Session, user *store.User, st *store.DB, filename string) {
	err := st.ActivateConfig(ctx, user.ID, filename)
	if err != nil {
		println(sess, errorStyle.Render("Error: "+err.Error()))
		return
	}

	println(sess, successStyle.Render("Activated: "+filename))
}

func handleDeactivate(ctx context.Context, sess ssh.Session, user *store.User, st *store.DB, filename string) {
	err := st.DeactivateConfigByFilename(ctx, user.ID, filename)
	if err != nil {
		println(sess, errorStyle.Render("Error: "+err.Error()))
		return
	}

	println(sess, successStyle.Render("Deactivated: "+filename))
}

func handleRun(ctx context.Context, sess ssh.Session, user *store.User, st *store.DB, sched *scheduler.Scheduler, filename string) {
	cfg, err := st.GetConfig(ctx, user.ID, filename)
	if err != nil {
		println(sess, errorStyle.Render("Config not found: "+filename))
		return
	}

	// Simple spinner animation
	spinChars := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	done := make(chan struct{})
	result := make(chan struct {
		items int
		err   error
	})

	// Spinner goroutine
	go func() {
		i := 0
		for {
			select {
			case <-done:
				return
			default:
				printf(sess, "\r%s Fetching feeds...", spinChars[i%len(spinChars)])
				i++
				time.Sleep(80 * time.Millisecond)
			}
		}
	}()

	// Work goroutine
	go func() {
		newItems, err := sched.RunNow(ctx, cfg.ID)
		result <- struct {
			items int
			err   error
		}{items: newItems, err: err}
	}()

	// Wait for result
	res := <-result
	close(done)
	print(sess, "\r\033[K") // Clear the spinner line

	if res.err != nil {
		println(sess, errorStyle.Render("Error: "+res.err.Error()))
		return
	}

	if res.items == 0 {
		println(sess, dimStyle.Render("No new items found."))
	} else {
		println(sess, successStyle.Render(fmt.Sprintf("Sent %d new item(s) to %s", res.items, cfg.Email)))
	}
}

func handleLogs(ctx context.Context, sess ssh.Session, user *store.User, st *store.DB) {
	logs, err := st.GetRecentLogs(ctx, user.ID, 20)
	if err != nil {
		println(sess, errorStyle.Render("Error: "+err.Error()))
		return
	}

	if len(logs) == 0 {
		println(sess, dimStyle.Render("No logs yet."))
		return
	}

	println(sess, titleStyle.Render("Recent activity:"))

	for _, l := range logs {
		levelStyle := dimStyle
		switch strings.ToLower(l.Level) {
		case "error":
			levelStyle = errorStyle
		case "info":
			levelStyle = successStyle
		}

		timestamp := l.CreatedAt.Format("Jan 02 15:04")
		printf(sess, "  %s  %s  %s\n",
			dimStyle.Render(timestamp),
			levelStyle.Render(fmt.Sprintf("[%s]", l.Level)),
			l.Message,
		)
	}
}

func formatRelativeTime(t time.Time) string {
	now := time.Now()
	diff := t.Sub(now)

	if diff < 0 {
		return "overdue"
	}

	if diff < time.Minute {
		return "< 1 min"
	}
	if diff < time.Hour {
		mins := int(diff.Minutes())
		return fmt.Sprintf("%d min", mins)
	}
	if diff < 24*time.Hour {
		hours := int(diff.Hours())
		return fmt.Sprintf("%d hr", hours)
	}

	days := int(diff.Hours() / 24)
	return fmt.Sprintf("%d day(s)", days)
}
