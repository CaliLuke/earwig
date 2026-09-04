package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/CaliLuke/earwig/internal/spool"
)

type turnSummary struct {
	ID         string `json:"id"`
	TraceUUID  string `json:"trace_uuid"`
	Status     string `json:"status"`
	StartedMS  int64  `json:"started_at_ms"`
	DurationMS int64  `json:"duration_ms"`
	StepCount  int    `json:"steps"`
}

type turnsResult struct {
	Session       spool.CapturedSession `json:"session"`
	JSONDirectory string                `json:"json_directory,omitempty"`
	Turns         []turnSummary         `json:"turns"`
}

func newTurnsCommand(app *application) *cobra.Command {
	var turnID string
	var asJSON bool
	cmd := &cobra.Command{
		Use:     "turns <session-id-or-prefix>",
		Short:   "List or inspect captured turns",
		Long:    "List captured turns for one session or print one normalized turn. The default JSON export directory is ~/.local/share/earwig/json.",
		Example: "  earwig turns 019fb35e\n  earwig turns 019fb35e --turn turn-id\n  earwig turns 019fb35e --turn turn-id --json",
		GroupID: inspectGroup,
		Args:    cobra.MatchAll(cobra.ExactArgs(1), validateSessionPrefix),
		RunE: func(_ *cobra.Command, args []string) error {
			store, err := app.openSpool()
			if err != nil {
				return err
			}
			session, err := store.FindSession(args[0])
			if err != nil {
				return err
			}
			rows, err := store.RowsForSession(session.Provider, session.SessionID)
			if err != nil {
				return err
			}
			if turnID != "" {
				row, findErr := findTurn(rows, turnID)
				if findErr != nil {
					return findErr
				}
				return writeTurn(app, row, asJSON)
			}
			summaries := make([]turnSummary, 0, len(rows))
			for _, row := range rows {
				summary, summaryErr := summarizeTurn(row)
				if summaryErr != nil {
					return summaryErr
				}
				summaries = append(summaries, summary)
			}
			cfg, err := app.config()
			if err != nil {
				return err
			}
			result := turnsResult{Session: session, JSONDirectory: cfg.JSONDir, Turns: summaries}
			if asJSON {
				return writeJSON(app.out, result)
			}
			return writeTurnsTable(app, result)
		},
		ValidArgsFunction: func(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
			if len(args) > 0 {
				return nil, cobra.ShellCompDirectiveNoFileComp
			}
			return completeSessionIDs(app, toComplete)
		},
	}
	cmd.Flags().StringVar(&turnID, "turn", "", "show one turn ID or unique prefix")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print machine-readable JSON")
	return cmd
}

func summarizeTurn(row spool.Row) (turnSummary, error) {
	var payload spool.TurnPayload
	if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
		return turnSummary{}, fmt.Errorf("decode turn %s: %w", row.TurnID, err)
	}
	duration := row.CompletedMS - row.StartedMS
	if duration < 0 {
		duration = 0
	}
	return turnSummary{
		ID:         row.TurnID,
		TraceUUID:  row.TraceUUID,
		Status:     row.Status,
		StartedMS:  row.StartedMS,
		DurationMS: duration,
		StepCount:  len(payload.Turn.Trajectory),
	}, nil
}

func findTurn(rows []spool.Row, prefix string) (spool.Row, error) {
	matches := make([]spool.Row, 0, 1)
	for _, row := range rows {
		if row.TurnID == prefix || row.TraceUUID == prefix {
			return row, nil
		}
		if strings.HasPrefix(row.TurnID, prefix) || strings.HasPrefix(row.TraceUUID, prefix) {
			matches = append(matches, row)
		}
	}
	if len(matches) == 0 {
		return spool.Row{}, fmt.Errorf("no captured turn matches %q", prefix)
	}
	if len(matches) > 1 {
		return spool.Row{}, fmt.Errorf("turn prefix %q is ambiguous; use more characters", prefix)
	}
	return matches[0], nil
}

func writeTurn(app *application, row spool.Row, asJSON bool) error {
	var payload spool.TurnPayload
	if err := json.Unmarshal([]byte(row.Payload), &payload); err != nil {
		return fmt.Errorf("decode turn %s: %w", row.TurnID, err)
	}
	if asJSON {
		return writeJSON(app.out, payload)
	}
	summary, err := summarizeTurn(row)
	if err != nil {
		return err
	}
	var content bytes.Buffer
	encoder := json.NewEncoder(&content)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(payload); err != nil {
		return err
	}
	_, err = fmt.Fprintf(app.out,
		"Turn %s\ntrace: %s\nstatus: %s\nstarted: %s\nduration: %s\nsteps: %d\n\n%s",
		row.TurnID, row.TraceUUID, row.Status, formatDetailedTime(row.StartedMS),
		formatTurnDuration(summary.DurationMS), summary.StepCount, content.String(),
	)
	return err
}

func writeTurnsTable(app *application, result turnsResult) error {
	writer := tabwriter.NewWriter(app.out, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "TURN\tSTATUS\tSTARTED\tDURATION\tSTEPS"); err != nil {
		return err
	}
	for _, turn := range result.Turns {
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%d\n",
			shortID(turn.ID, 20), turn.Status, formatExactTime(turn.StartedMS),
			formatTurnDuration(turn.DurationMS), turn.StepCount,
		); err != nil {
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(app.out, "\n%s %s in session %s.\n",
		comma(len(result.Turns)), plural(len(result.Turns), "turn", "turns"), result.Session.IDPrefix,
	); err != nil {
		return err
	}
	if result.JSONDirectory != "" {
		_, err := fmt.Fprintf(app.out, "JSON files: %s\n", shortenHome(result.JSONDirectory))
		return err
	}
	return nil
}

func formatTurnDuration(milliseconds int64) string {
	if milliseconds <= 0 {
		return "—"
	}
	return (time.Duration(milliseconds) * time.Millisecond).String()
}
