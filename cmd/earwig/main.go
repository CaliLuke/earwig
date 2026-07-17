package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/CaliLuke/earwig/internal/config"
	"github.com/CaliLuke/earwig/internal/daemon"
	"github.com/CaliLuke/earwig/internal/hooks"
	"github.com/CaliLuke/earwig/internal/provider"
	"github.com/CaliLuke/earwig/internal/spool"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		return
	}
	cfg, e := config.Load()
	if e != nil {
		fatal(e)
	}
	s, e := spool.Open(spool.DefaultPath())
	if e != nil {
		fatal(e)
	}
	defer s.Close()
	sw := &daemon.Sweeper{Config: cfg, Spool: s, Log: log.New(os.Stderr, "earwig: ", log.LstdFlags)}
	switch os.Args[1] {
	case "sweep":
		fs := flag.NewFlagSet("sweep", flag.ExitOnError)
		session := fs.String("session", "", "")
		p := fs.String("provider", "", "")
		_ = fs.Parse(os.Args[2:])
		if *p != "" && *p != "claude" && *p != "codex" {
			fatal(fmt.Errorf("provider must be claude or codex"))
		}
		if *session != "" && ((*p == "claude" && !provider.ValidClaudeID(*session)) || (*p == "codex" && !provider.ValidCodexID(*session))) {
			fatal(fmt.Errorf("invalid session ID"))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if e := sw.Sweep(ctx, daemon.SweepOptions{Session: *session, Provider: *p}); e != nil {
			fmt.Fprintln(os.Stderr, "earwig:", e)
		}
	case "watch":
		lock, e := daemon.Acquire(spool.DefaultPath() + ".lock")
		if e != nil {
			fmt.Fprintln(os.Stderr, "earwig: daemon already running")
			os.Exit(2)
		}
		defer lock.Release()
		ctx, stop := signalContext()
		defer stop()
		home, _ := os.UserHomeDir()
		paths := []string{filepath.Join(home, ".claude", "projects"), filepath.Join(home, ".codex", "sessions")}
		_ = daemon.Watch(ctx, &daemon.Watcher{Sweeper: sw, Spool: s}, paths)
	case "status":
		total, recent, e := s.Stats()
		if e != nil {
			fatal(e)
		}
		behind := false
		for _, name := range []string{"jsondir", "opik"} {
			v, e := s.GetHealth("exporter_" + name + "_behind")
			if e == nil && v != "null" {
				behind = true
				fmt.Printf("%s: behind (%s)\n", name, v)
			}
		}
		fmt.Printf("turns: %d total / %d last 24h\n", total, recent)
		var gaps int
		_ = s.DB.QueryRow(`SELECT COUNT(*) FROM sessions WHERE gap_warned=1`).Scan(&gaps)
		if gaps > 0 {
			behind = true
			fmt.Printf("gap warnings: %d\n", gaps)
		}
		if behind {
			os.Exit(1)
		}
	case "stop":
		if e := daemon.Stop(spool.DefaultPath() + ".lock"); e != nil {
			fatal(e)
		}
	case "prune":
		fs := flag.NewFlagSet("prune", flag.ExitOnError)
		older := fs.String("older-than", "", "")
		_ = fs.Parse(os.Args[2:])
		d, e := time.ParseDuration(*older)
		if e != nil || d <= 0 {
			fatal(fmt.Errorf("--older-than positive duration required"))
		}
		n, e := s.Prune(time.Now().Add(-d))
		if e != nil {
			fatal(e)
		}
		fmt.Printf("pruned %d turns\n", n)
	case "export":
		fs := flag.NewFlagSet("export", flag.ExitOnError)
		dir := fs.String("dir", "", "")
		_ = fs.Parse(os.Args[2:])
		if *dir == "" {
			fatal(fmt.Errorf("--dir required"))
		}
		old := sw.Config.JSONDir
		sw.Config.JSONDir = *dir
		sw.Config.OpikURL = ""
		if e := sw.Sweep(context.Background(), daemon.SweepOptions{}); e != nil {
			fmt.Fprintln(os.Stderr, "earwig:", e)
		}
		sw.Config.JSONDir = old
	case "hooks":
		hooksCmd(os.Args[2:])
	case "install":
		exe, _ := os.Executable()
		fmt.Print(daemon.Plist(exe))
		fmt.Print("Install this launchd agent? [y/N] ")
		var answer string
		_, _ = fmt.Fscan(os.Stdin, &answer)
		if strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes") {
			if e := daemon.Install(exe); e != nil {
				fatal(e)
			}
		}
	case "uninstall":
		if e := daemon.Uninstall(); e != nil {
			fatal(e)
		}
	default:
		usage()
	}
}
func hooksCmd(args []string) {
	if len(args) != 1 {
		fatal(fmt.Errorf("hooks install|remove"))
	}
	switch args[0] {
	case "install":
		exe, _ := os.Executable()
		p, e := hooks.Preview(exe)
		if e != nil {
			fatal(e)
		}
		fmt.Println(p)
		fmt.Print("Install these Claude hooks? [y/N] ")
		var answer string
		_, _ = fmt.Fscan(os.Stdin, &answer)
		if strings.ToLower(answer) != "y" && strings.ToLower(answer) != "yes" {
			return
		}
		exe, _ = os.Executable()
		if e := hooks.Install(exe); e != nil {
			fatal(e)
		}
	case "remove":
		if e := hooks.Remove(); e != nil {
			fatal(e)
		}
	default:
		fatal(fmt.Errorf("hooks install|remove"))
	}
}
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
func usage()        { fmt.Fprintln(os.Stderr, "usage: earwig sweep|watch|status|stop|prune|export|hooks") }
func fatal(e error) { fmt.Fprintln(os.Stderr, "earwig:", e); os.Exit(1) }
