package daemon

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CaliLuke/earwig/internal/config"
	"github.com/CaliLuke/earwig/internal/spool"
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
	if _, statErr := os.Stat(p); statErr != nil {
		t.Fatalf("lock file should persist: %v", statErr)
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

func TestWatcherSerializesRepeatedConcurrentRestartCycles(t *testing.T) {
	const (
		cycles         = 100
		eventsPerCycle = 25
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan int, cycles)
	release := make(chan struct{})
	var active, maximum, started atomic.Int32
	watcher := &Watcher{
		SweepTimeout: time.Minute,
		sweep: func(ctx context.Context) error {
			current := active.Add(1)
			for {
				previous := maximum.Load()
				if current <= previous || maximum.CompareAndSwap(previous, current) {
					break
				}
			}
			number := int(started.Add(1))
			entered <- number
			select {
			case <-release:
			case <-ctx.Done():
			}
			active.Add(-1)
			return ctx.Err()
		},
	}
	watcher.trigger(ctx)

	for cycle := 1; cycle <= cycles; cycle++ {
		select {
		case number := <-entered:
			if number != cycle {
				t.Fatalf("restart number = %d, want %d", number, cycle)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("restart %d did not begin", cycle)
		}
		if cycle == cycles {
			cancel()
			break
		}
		var events sync.WaitGroup
		events.Add(eventsPerCycle)
		for event := 0; event < eventsPerCycle; event++ {
			go func() {
				defer events.Done()
				watcher.trigger(ctx)
			}()
		}
		events.Wait()
		release <- struct{}{}
	}
	watcher.wait()

	if got := started.Load(); got != cycles {
		t.Fatalf("started %d sweeps, want %d", got, cycles)
	}
	if got := maximum.Load(); got != 1 {
		t.Fatalf("maximum concurrent sweeps = %d, want 1", got)
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("%d sweeps still active after shutdown", got)
	}
}

func TestWatchCancellationJoinsActiveSweep(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	allowExit := make(chan struct{})
	watcher := &Watcher{
		SweepTimeout: time.Minute,
		sweep: func(context.Context) error {
			close(started)
			<-allowExit
			return nil
		},
	}
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, watcher, []string{t.TempDir()})
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("startup sweep did not begin")
	}
	cancel()
	select {
	case err := <-done:
		t.Fatalf("Watch returned before active sweep exited: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(allowExit)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Watch did not return after active sweep exited")
	}
}

func TestWatchRegistersDirectoriesCreatedAfterStartup(t *testing.T) {
	root := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	swept := make(chan struct{}, 3)
	watcher := &Watcher{
		SweepTimeout: time.Minute,
		sweep: func(context.Context) error {
			swept <- struct{}{}
			return nil
		},
	}
	done := make(chan error, 1)
	go func() {
		done <- Watch(ctx, watcher, []string{root})
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Watch returned an error: %v", err)
		}
	})

	waitForSweep := func(reason string) {
		t.Helper()
		select {
		case <-swept:
		case <-time.After(5 * time.Second):
			t.Fatalf("no sweep after %s", reason)
		}
	}
	waitForSweep("startup")

	nested := filepath.Join(root, "new", "nested")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	waitForSweep("creating a nested directory")

	if err := os.WriteFile(filepath.Join(nested, "session.jsonl"), []byte("complete"), 0600); err != nil {
		t.Fatal(err)
	}
	waitForSweep("writing inside the new directory")
}
