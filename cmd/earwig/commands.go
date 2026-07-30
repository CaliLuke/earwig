package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/CaliLuke/earwig/internal/config"
	"github.com/CaliLuke/earwig/internal/daemon"
	"github.com/CaliLuke/earwig/internal/doctor"
	"github.com/CaliLuke/earwig/internal/exporter"
	"github.com/CaliLuke/earwig/internal/hooks"
	"github.com/CaliLuke/earwig/internal/provider"
)

const (
	captureGroup     = "capture"
	inspectGroup     = "inspect"
	serviceGroup     = "service"
	maintenanceGroup = "maintenance"
)

func newRootCommand(app *application) *cobra.Command {
	build := fmt.Sprintf("%s (commit %s, built %s)", version, commit, buildDate)
	root := &cobra.Command{
		Use:          "earwig",
		Short:        "Capture and inspect local AI coding sessions",
		Long:         "Earwig captures local Claude and Codex sessions into a private spool, then exports them to configured destinations.",
		Version:      build,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.SetIn(app.in)
	root.SetOut(app.out)
	root.SetErr(app.err)
	root.SetVersionTemplate("earwig {{.Version}}\n")
	root.AddGroup(
		&cobra.Group{ID: captureGroup, Title: "Capture Commands:"},
		&cobra.Group{ID: inspectGroup, Title: "Inspect Commands:"},
		&cobra.Group{ID: serviceGroup, Title: "Service Commands:"},
		&cobra.Group{ID: maintenanceGroup, Title: "Maintenance Commands:"},
	)
	root.AddCommand(
		newSweepCommand(app),
		newWatchCommand(app),
		newSessionsCommand(app),
		newStatusCommand(app),
		newExportCommand(app),
		newStopCommand(app),
		newInstallCommand(app),
		newUninstallCommand(),
		newPruneCommand(app),
		newHooksCommand(app),
		newDoctorCommand(app),
		newVersionCommand(build),
		newCompletionCommand(root),
	)
	return root
}

func newSweepCommand(app *application) *cobra.Command {
	var session, providerName string
	cmd := &cobra.Command{
		Use:     "sweep",
		Short:   "Capture new and changed sessions once",
		GroupID: captureGroup,
		Args:    cobra.NoArgs,
		PreRunE: func(_ *cobra.Command, _ []string) error {
			if err := validateProvider(providerName); err != nil {
				return err
			}
			if session != "" && providerName == "" {
				return fmt.Errorf("--session requires --provider")
			}
			if session != "" && ((providerName == "claude" && !provider.ValidClaudeID(session)) ||
				(providerName == "codex" && !provider.ValidCodexID(session))) {
				return fmt.Errorf("invalid %s session ID", providerName)
			}
			return nil
		},
		RunE: func(_ *cobra.Command, _ []string) error {
			sw, err := app.sweeper()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			// Capture failures are reported without changing the historical
			// sweep exit contract; durable rows may still have been captured.
			if err = sw.Sweep(ctx, daemon.SweepOptions{Session: session, Provider: providerName}); err != nil {
				_, _ = fmt.Fprintln(app.err, "earwig:", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&session, "session", "", "capture only this session ID")
	cmd.Flags().StringVarP(&providerName, "provider", "p", "", "capture only one provider (claude or codex)")
	_ = cmd.RegisterFlagCompletionFunc("provider", providerCompletion)
	return cmd
}

func newWatchCommand(app *application) *cobra.Command {
	return &cobra.Command{
		Use:     "watch",
		Short:   "Run the capture daemon in the foreground",
		GroupID: captureGroup,
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := app.config()
			if err != nil {
				return err
			}
			store, err := app.openSpool()
			if err != nil {
				return err
			}
			sw, err := app.sweeper()
			if err != nil {
				return err
			}
			lock, err := daemon.Acquire(cfg.SpoolPath + ".lock")
			if err != nil {
				app.exitCode = 2
				return err
			}
			defer lock.Release()
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			home, _ := os.UserHomeDir()
			paths := []string{filepath.Join(home, ".claude", "projects"), config.CodexSessionsPath()}
			return daemon.Watch(ctx, &daemon.Watcher{Sweeper: sw, Spool: store}, paths)
		},
	}
}

func newStopCommand(app *application) *cobra.Command {
	return &cobra.Command{
		Use:     "stop",
		Short:   "Stop the running capture daemon",
		GroupID: serviceGroup,
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := app.config()
			if err != nil {
				return err
			}
			return daemon.Stop(cfg.SpoolPath + ".lock")
		},
	}
}

func newPruneCommand(app *application) *cobra.Command {
	var older string
	cmd := &cobra.Command{
		Use:     "prune",
		Short:   "Remove old turns that are not protected upstream",
		GroupID: maintenanceGroup,
		Args:    cobra.NoArgs,
		PreRunE: func(_ *cobra.Command, _ []string) error {
			duration, err := time.ParseDuration(older)
			if err != nil || duration <= 0 {
				return fmt.Errorf("--older-than must be a positive duration such as 720h")
			}
			return nil
		},
		RunE: func(_ *cobra.Command, _ []string) error {
			duration, _ := time.ParseDuration(older)
			cfg, err := app.config()
			if err != nil {
				return err
			}
			store, err := app.openSpool()
			if err != nil {
				return err
			}
			before := time.Now().Add(-duration)
			opikIDs, err := store.ExportedTraceIDsBefore("opik", before)
			if err != nil {
				return err
			}
			protected := map[string]bool{}
			if len(opikIDs) > 0 {
				if cfg.OpikURL == "" {
					return fmt.Errorf("cannot prune: opik_url is required to verify retention tags for %d exported traces", len(opikIDs))
				}
				protected, err = (exporter.Opik{
					URL:         cfg.OpikURL,
					ProjectName: cfg.OpikProject,
				}).ProtectedTraceIDs(opikIDs)
				if err != nil {
					return fmt.Errorf("cannot verify upstream retention state: %w", err)
				}
			}
			count, err := store.Prune(before, protected)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(app.out, "Pruned %d turns.\n", count)
			return err
		},
	}
	cmd.Flags().StringVar(&older, "older-than", "", "prune turns older than this duration (for example 720h)")
	_ = cmd.MarkFlagRequired("older-than")
	return cmd
}

func newExportCommand(app *application) *cobra.Command {
	var directory string
	cmd := &cobra.Command{
		Use:     "export",
		Short:   "Export captured turns to a JSON directory",
		GroupID: inspectGroup,
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			sw, err := app.sweeper()
			if err != nil {
				return err
			}
			sw.Config.JSONDir = directory
			sw.Config.OpikURL = ""
			if err = sw.Sweep(context.Background(), daemon.SweepOptions{}); err != nil {
				_, _ = fmt.Fprintln(app.err, "earwig:", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&directory, "dir", "", "directory to receive JSON files")
	_ = cmd.MarkFlagRequired("dir")
	return cmd
}

func newHooksCommand(app *application) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "hooks",
		Short:   "Manage Claude lifecycle hooks",
		GroupID: maintenanceGroup,
		Args:    cobra.NoArgs,
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "install",
			Short: "Preview and install Claude capture hooks",
			Args:  cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				return previewInstall(app, "Install these Claude hooks?", hooks.Preview, hooks.Install)
			},
		},
		&cobra.Command{
			Use:   "remove",
			Short: "Remove Earwig-managed Claude hooks",
			Args:  cobra.NoArgs,
			RunE: func(_ *cobra.Command, _ []string) error {
				return hooks.Remove()
			},
		},
	)
	return cmd
}

func newInstallCommand(app *application) *cobra.Command {
	return &cobra.Command{
		Use:     "install",
		Short:   "Preview and install the user service",
		GroupID: serviceGroup,
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return previewInstall(app, "Install this user service?", daemon.ServiceDefinition, daemon.Install)
		},
	}
}

func newUninstallCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "uninstall",
		Short:   "Uninstall the user service",
		GroupID: serviceGroup,
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return daemon.Uninstall()
		},
	}
}

func newDoctorCommand(app *application) *cobra.Command {
	return &cobra.Command{
		Use:     "doctor",
		Short:   "Check configuration and capture dependencies",
		GroupID: maintenanceGroup,
		Args:    cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			cfg, err := app.config()
			if err != nil {
				return err
			}
			return doctor.Run(cfg, app.out)
		},
	}
}

func newVersionCommand(build string) *cobra.Command {
	return &cobra.Command{
		Use:     "version",
		Short:   "Print release build information",
		GroupID: maintenanceGroup,
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "earwig %s\n", build)
			return err
		},
	}
}

func newCompletionCommand(root *cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:       "completion [bash|zsh|fish|powershell]",
		Short:     "Generate shell completion",
		Long:      "Generate a completion script for the selected shell. See this command's help for installation examples.",
		Example:   "  earwig completion zsh > \"${fpath[1]}/_earwig\"\n  earwig completion bash > /usr/local/etc/bash_completion.d/earwig",
		GroupID:   maintenanceGroup,
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return root.GenBashCompletion(cmd.OutOrStdout())
			case "zsh":
				return root.GenZshCompletion(cmd.OutOrStdout())
			case "fish":
				return root.GenFishCompletion(cmd.OutOrStdout(), true)
			case "powershell":
				return root.GenPowerShellCompletionWithDesc(cmd.OutOrStdout())
			default:
				return fmt.Errorf("unsupported shell %q", args[0])
			}
		},
	}
	return cmd
}

func providerCompletion(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
	return []string{"claude\tClaude Code", "codex\tCodex"}, cobra.ShellCompDirectiveNoFileComp
}

func validateProvider(name string) error {
	if name != "" && name != "claude" && name != "codex" {
		return fmt.Errorf("--provider must be claude or codex")
	}
	return nil
}

func confirm(app *application, prompt string) (bool, error) {
	if _, err := fmt.Fprintf(app.out, "%s [y/N] ", prompt); err != nil {
		return false, err
	}
	var answer string
	_, _ = fmt.Fscan(app.in, &answer)
	return strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes"), nil
}

func previewInstall(
	app *application,
	prompt string,
	preview func(string) (string, error),
	install func(string) error,
) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	output, err := preview(exe)
	if err != nil {
		return err
	}
	if _, err = fmt.Fprintln(app.out, output); err != nil {
		return err
	}
	confirmed, err := confirm(app, prompt)
	if err != nil || !confirmed {
		return err
	}
	return install(exe)
}
