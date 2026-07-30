package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/CaliLuke/earwig/internal/daemon"
	"github.com/CaliLuke/earwig/internal/spool"
)

type sessionsResult struct {
	Total    int                     `json:"total"`
	Sessions []spool.CapturedSession `json:"sessions"`
}

func newSessionsCommand(app *application) *cobra.Command {
	var providerName string
	var limit int
	var warningsOnly, asJSON bool
	cmd := &cobra.Command{
		Use:     "sessions",
		Aliases: []string{"list"},
		Short:   "List captured sessions",
		Long:    "List sessions in Earwig's local spool without loading or printing their captured content.",
		Example: "  earwig sessions\n  earwig sessions --provider claude --limit 50\n  earwig sessions --warnings\n  earwig sessions --json",
		GroupID: inspectGroup,
		Args:    cobra.NoArgs,
		PreRunE: func(_ *cobra.Command, _ []string) error {
			if err := validateProvider(providerName); err != nil {
				return err
			}
			if limit < 1 || limit > 1000 {
				return fmt.Errorf("--limit must be between 1 and 1000")
			}
			return nil
		},
		RunE: func(_ *cobra.Command, _ []string) error {
			store, err := app.openSpool()
			if err != nil {
				return err
			}
			sessions, total, err := store.ListSessions(spool.SessionFilter{
				Provider:        providerSource(providerName),
				GapWarningsOnly: warningsOnly,
				Limit:           limit,
			})
			if err != nil {
				return err
			}
			result := sessionsResult{Total: total, Sessions: sessions}
			if asJSON {
				return writeJSON(app.out, result)
			}
			return writeSessionsTable(app, result)
		},
	}
	cmd.Flags().StringVarP(&providerName, "provider", "p", "", "show one provider (claude or codex)")
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "maximum number of sessions to show")
	cmd.Flags().BoolVar(&warningsOnly, "warnings", false, "show only sessions with capture gap warnings")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	_ = cmd.RegisterFlagCompletionFunc("provider", providerCompletion)
	return cmd
}

func writeSessionsTable(app *application, result sessionsResult) error {
	if result.Total == 0 {
		_, err := fmt.Fprintln(app.out, "No captured sessions found.")
		if err == nil {
			_, err = fmt.Fprintln(app.out, "Run `earwig sweep` to capture available Claude and Codex sessions.")
		}
		return err
	}
	if _, err := fmt.Fprintf(app.out, "Captured sessions (showing %d of %d)\n\n", len(result.Sessions), result.Total); err != nil {
		return err
	}
	table := tabwriter.NewWriter(app.out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(table, "LAST CAPTURED\tPROVIDER\tTURNS\tSTATE\tWORKSPACE\tSESSION")
	for _, session := range result.Sessions {
		state := "ok"
		if session.HasGapWarning {
			state = "gap"
		}
		_, _ = fmt.Fprintf(
			table,
			"%s\t%s\t%d\t%s\t%s\t%s\n",
			formatCapturedTime(session.LastCapturedMS),
			friendlyProvider(session.Provider),
			session.Turns,
			state,
			shortenHome(session.CWD),
			session.SessionID,
		)
	}
	if err := table.Flush(); err != nil {
		return err
	}
	if len(result.Sessions) < result.Total {
		_, err := fmt.Fprintln(app.out, "\nUse --limit to show more sessions.")
		return err
	}
	return nil
}

type daemonStatus struct {
	Running bool `json:"running"`
	PID     int  `json:"pid,omitempty"`
}

type destinationStatus struct {
	Name    string `json:"name"`
	Path    string `json:"path,omitempty"`
	URL     string `json:"url,omitempty"`
	Behind  string `json:"behind,omitempty"`
	Pending int    `json:"pending"`
}

type statusResult struct {
	Healthy        bool                `json:"healthy"`
	Daemon         daemonStatus        `json:"daemon"`
	LastPoll       string              `json:"last_poll,omitempty"`
	LastSweep      string              `json:"last_sweep,omitempty"`
	LastSuccessful string              `json:"last_successful_sweep,omitempty"`
	LastError      string              `json:"last_error,omitempty"`
	Sessions       int                 `json:"sessions"`
	Turns          int                 `json:"turns"`
	TurnsLast24H   int                 `json:"turns_last_24h"`
	Compactions    int                 `json:"compactions"`
	GapWarnings    int                 `json:"gap_warnings"`
	Destinations   []destinationStatus `json:"destinations"`
}

func newStatusCommand(app *application) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "status",
		Short:   "Show capture, daemon, and export health",
		GroupID: inspectGroup,
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			result, err := collectStatus(app)
			if err != nil {
				return err
			}
			if asJSON {
				err = writeJSON(app.out, result)
			} else {
				err = writeStatus(app, result)
			}
			if !result.Healthy {
				app.exitCode = 1
			}
			return err
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}

func collectStatus(app *application) (statusResult, error) {
	cfg, err := app.config()
	if err != nil {
		return statusResult{}, err
	}
	store, err := app.openSpool()
	if err != nil {
		return statusResult{}, err
	}
	total, recent, err := store.Stats()
	if err != nil {
		return statusResult{}, err
	}
	sessionStats, err := store.SessionStats()
	if err != nil {
		return statusResult{}, err
	}
	pid, running, err := daemon.LockStatus(cfg.SpoolPath + ".lock")
	if err != nil {
		return statusResult{}, err
	}
	result := statusResult{
		Healthy:      sessionStats.GapWarnings == 0,
		Daemon:       daemonStatus{Running: running, PID: pid},
		Sessions:     sessionStats.Sessions,
		Turns:        total,
		TurnsLast24H: recent,
		Compactions:  sessionStats.Compactions,
		GapWarnings:  sessionStats.GapWarnings,
		Destinations: []destinationStatus{},
	}
	result.LastPoll = healthString(store, "last_poll")
	result.LastSweep = healthString(store, "last_sweep_completed")
	result.LastSuccessful = healthString(store, "last_sweep_success")
	result.LastError = healthString(store, "last_sweep_error")

	if cfg.JSONDir != "" {
		result.Destinations = append(result.Destinations, collectDestination(store, "jsondir", cfg.JSONDir, ""))
	}
	if cfg.OpikURL != "" {
		result.Destinations = append(result.Destinations, collectDestination(store, "opik", "", cfg.OpikURL))
	}
	for _, destination := range result.Destinations {
		if destination.Behind != "" || destination.Pending > 0 {
			result.Healthy = false
		}
	}
	return result, nil
}

func collectDestination(store *spool.Spool, name, path, url string) destinationStatus {
	result := destinationStatus{Name: name, Path: path, URL: url}
	result.Behind = healthString(store, "exporter_"+name+"_behind")
	result.Pending, _ = store.PendingCount(name)
	return result
}

func writeStatus(app *application, result statusResult) error {
	var report strings.Builder
	_, _ = fmt.Fprint(&report, "Earwig status\n\n")
	daemonValue := "stopped"
	if result.Daemon.Running {
		daemonValue = fmt.Sprintf("running (PID %d)", result.Daemon.PID)
	}
	_, _ = fmt.Fprintf(&report, "daemon: %s\n", daemonValue)
	_, _ = fmt.Fprintf(&report, "last sweep: %s\n", formatStatusTime(result.LastSweep))
	_, _ = fmt.Fprintf(&report, "sessions: %s\n", comma(result.Sessions))
	_, _ = fmt.Fprintf(&report, "turns: %s total (%s in the last 24 hours)\n", comma(result.Turns), comma(result.TurnsLast24H))
	if result.Compactions > 0 {
		_, _ = fmt.Fprintf(&report, "compactions: %s observed\n", comma(result.Compactions))
	}
	if len(result.Destinations) == 0 {
		_, _ = fmt.Fprintln(&report, "destinations: none configured")
	}
	for _, destination := range result.Destinations {
		detail := "up to date"
		switch {
		case destination.Behind != "" && destination.Pending > 0:
			detail = fmt.Sprintf("behind (%s); %s pending", destination.Behind, comma(destination.Pending))
		case destination.Behind != "":
			detail = "behind (" + destination.Behind + ")"
		case destination.Pending > 0:
			detail = fmt.Sprintf("behind; %s pending", comma(destination.Pending))
		}
		target := destination.Path
		if target != "" {
			target = shortenHome(target)
		} else {
			target = destination.URL
		}
		if target != "" {
			detail += " → " + target
		}
		_, _ = fmt.Fprintf(&report, "%s: %s\n", friendlyDestination(destination.Name), detail)
	}
	if result.LastError != "" {
		_, _ = fmt.Fprintf(&report, "\nLast capture error: %s\n", result.LastError)
	}
	if result.GapWarnings > 0 {
		noun := "sessions have"
		if result.GapWarnings == 1 {
			noun = "session has"
		}
		_, _ = fmt.Fprintf(&report, "\nWarning: %s %s a capture gap.\n", comma(result.GapWarnings), noun)
		_, _ = fmt.Fprintln(&report, "Run `earwig sessions --warnings` for details.")
	}
	_, err := io.WriteString(app.out, report.String())
	return err
}

func writeJSON(out interface{ Write([]byte) (int, error) }, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func healthString(store *spool.Spool, key string) string {
	raw, err := store.GetHealth(key)
	if err != nil || raw == "null" {
		return ""
	}
	var value string
	if json.Unmarshal([]byte(raw), &value) == nil {
		return value
	}
	return strings.Trim(raw, `"`)
}

func providerSource(name string) string {
	switch name {
	case "claude":
		return "claude-code-agent-sdk"
	case "codex":
		return "codex-app-server"
	default:
		return ""
	}
}

func friendlyProvider(source string) string {
	switch source {
	case "claude-code-agent-sdk":
		return "Claude"
	case "codex-app-server":
		return "Codex"
	default:
		return source
	}
}

func friendlyDestination(name string) string {
	switch name {
	case "jsondir":
		return "JSON files"
	case "opik":
		return "Opik"
	default:
		return name
	}
}

func formatCapturedTime(milliseconds int64) string {
	if milliseconds <= 0 {
		return "—"
	}
	return time.UnixMilli(milliseconds).Local().Format("2006-01-02 15:04")
}

func formatStatusTime(value string) string {
	if value == "" {
		return "never"
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	elapsed := time.Since(parsed)
	if elapsed < 0 {
		return parsed.Local().Format("2006-01-02 15:04:05")
	}
	var relative string
	switch {
	case elapsed < time.Minute:
		relative = "just now"
	case elapsed < time.Hour:
		relative = fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		relative = fmt.Sprintf("%dh ago", int(elapsed.Hours()))
	default:
		relative = fmt.Sprintf("%dd ago", int(elapsed.Hours()/24))
	}
	return fmt.Sprintf("%s (%s)", relative, parsed.Local().Format("2006-01-02 15:04:05"))
}

func shortenHome(path string) string {
	home, err := os.UserHomeDir()
	if err == nil && (path == home || strings.HasPrefix(path, home+string(filepath.Separator))) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

func comma(value int) string {
	text := strconv.Itoa(value)
	for i := len(text) - 3; i > 0; i -= 3 {
		text = text[:i] + "," + text[i:]
	}
	return text
}
