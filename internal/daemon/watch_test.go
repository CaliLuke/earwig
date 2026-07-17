package daemon

import (
	"bufio"
	"context"
	"fmt"
	"github.com/CaliLuke/earwig/internal/config"
	"github.com/CaliLuke/earwig/internal/spool"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLockContention(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lock")
	a, e := Acquire(p)
	if e != nil {
		t.Fatal(e)
	}
	defer a.Release()
	if _, e = Acquire(p); e == nil {
		t.Fatal("second lock acquired")
	}
}

func TestLockCanBeReacquiredAfterReleaseWithPersistentFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lock")
	a, err := Acquire(p)
	if err != nil {
		t.Fatal(err)
	}
	a.Release()
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("lock file should persist: %v", err)
	}
	b, err := Acquire(p)
	if err != nil {
		t.Fatalf("stale lock blocked reacquisition: %v", err)
	}
	b.Release()
}

func TestLockReleasedAfterProcessKilled(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lock")
	cmd := exec.Command(os.Args[0], "-test.run=TestLockHelperProcess")
	cmd.Env = append(os.Environ(), "EARWIG_LOCK_HELPER="+p)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("lock helper did not start: %q", scanner.Text())
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()

	lock, err := Acquire(p)
	if err != nil {
		t.Fatalf("SIGKILL stranded lock: %v", err)
	}
	lock.Release()
}

func TestLockHelperProcess(t *testing.T) {
	p := os.Getenv("EARWIG_LOCK_HELPER")
	if p == "" {
		return
	}
	lock, err := Acquire(p)
	if err != nil {
		os.Exit(3)
	}
	defer lock.Release()
	fmt.Println("ready")
	for {
		time.Sleep(time.Hour)
	}
}
func TestStopRejectsBadPID(t *testing.T) {
	p := filepath.Join(t.TempDir(), "lock")
	if e := os.WriteFile(p, []byte("not-a-pid"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := Stop(p); e == nil {
		t.Fatal("bad pid accepted")
	}
}

func TestWatcherTimesOutSweepWithoutClaimingSuccess(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "hanging-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nsleep 10\n"), 0700); err != nil {
		t.Fatal(err)
	}
	s, err := spool.Open(filepath.Join(dir, "spool.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.SetHealth("last_sweep_success", "old"); err != nil {
		t.Fatal(err)
	}
	sw := &Sweeper{Config: config.Config{WorkspaceRoots: []string{dir}, Claude: true, ClaudeHelper: helper}, Spool: s}
	w := &Watcher{Sweeper: sw, Spool: s, SweepTimeout: 50 * time.Millisecond}
	w.trigger(context.Background())

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		v, healthErr := s.GetHealth("last_sweep_error")
		if healthErr == nil && strings.Contains(v, "deadline exceeded") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	v, err := s.GetHealth("last_sweep_error")
	if err != nil || !strings.Contains(v, "deadline exceeded") {
		t.Fatalf("timeout was not surfaced: %q, %v", v, err)
	}
	lastSuccess, err := s.GetHealth("last_sweep_success")
	if err != nil || lastSuccess != `"old"` {
		t.Fatalf("failed sweep advanced success: %q, %v", lastSuccess, err)
	}
}
