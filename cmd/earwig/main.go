package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"time"

	"github.com/CaliLuke/earwig/internal/config"
	"github.com/CaliLuke/earwig/internal/daemon"
	"github.com/CaliLuke/earwig/internal/hooks"
	"github.com/CaliLuke/earwig/internal/provider"
	"github.com/CaliLuke/earwig/internal/spool"
)

var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

type application struct {
	in       io.Reader
	out      io.Writer
	err      io.Writer
	cfg      *config.Config
	spool    *spool.Spool
	exitCode int
}

func main() {
	// Claude invokes this internal entrypoint from managed hooks. It stays
	// outside the public command tree and must always return zero so a capture
	// failure can never block or alter an agent session.
	if len(os.Args) > 1 && os.Args[1] == "hook" {
		runHook(os.Args[2:], os.Stdin, os.Stderr)
		return
	}
	os.Exit(runCLI(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func runCLI(args []string, in io.Reader, out, errOut io.Writer) int {
	app := &application{in: in, out: out, err: errOut}
	defer app.close()

	root := newRootCommand(app)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		if app.exitCode != 0 {
			return app.exitCode
		}
		return 1
	}
	return app.exitCode
}

func (a *application) config() (config.Config, error) {
	if a.cfg == nil {
		cfg, err := config.Load()
		if err != nil {
			return config.Config{}, err
		}
		a.cfg = &cfg
	}
	return *a.cfg, nil
}

func (a *application) openSpool() (*spool.Spool, error) {
	if a.spool == nil {
		cfg, err := a.config()
		if err != nil {
			return nil, err
		}
		a.spool, err = spool.Open(cfg.SpoolPath)
		if err != nil {
			return nil, err
		}
	}
	return a.spool, nil
}

func (a *application) sweeper() (*daemon.Sweeper, error) {
	cfg, err := a.config()
	if err != nil {
		return nil, err
	}
	store, err := a.openSpool()
	if err != nil {
		return nil, err
	}
	return &daemon.Sweeper{
		Config: cfg,
		Spool:  store,
		Log:    log.New(a.err, "earwig: ", log.LstdFlags),
	}, nil
}

func (a *application) close() {
	if a.spool != nil {
		_ = a.spool.Close()
	}
}

func runHook(args []string, in io.Reader, errOut io.Writer) {
	if len(args) != 2 || args[0] != "claude" || args[1] != hooks.ManagedArgument {
		_, _ = fmt.Fprintln(errOut, "earwig: invalid managed hook invocation")
		return
	}
	sessionID, err := hooks.SessionID(in)
	if err != nil || !provider.ValidClaudeID(sessionID) {
		if err == nil {
			err = fmt.Errorf("invalid Claude session ID")
		}
		_, _ = fmt.Fprintln(errOut, "earwig:", err)
		return
	}
	cfg, err := config.Load()
	if err != nil {
		_, _ = fmt.Fprintln(errOut, "earwig:", err)
		return
	}
	store, err := spool.Open(cfg.SpoolPath)
	if err != nil {
		_, _ = fmt.Fprintln(errOut, "earwig:", err)
		return
	}
	defer func() { _ = store.Close() }()

	sw := &daemon.Sweeper{
		Config: cfg,
		Spool:  store,
		Log:    log.New(errOut, "earwig: ", log.LstdFlags),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err = sw.Sweep(ctx, daemon.SweepOptions{Session: sessionID, Provider: "claude"}); err != nil {
		_, _ = fmt.Fprintln(errOut, "earwig:", err)
	}
}
