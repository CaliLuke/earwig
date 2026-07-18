package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/CaliLuke/earwig/internal/spool"
)

type Lock struct {
	file *os.File
}

func Acquire(path string) (*Lock, error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		_ = f.Close()
		if errors.Is(e, syscall.EWOULDBLOCK) || errors.Is(e, syscall.EAGAIN) {
			if pid, running, statusErr := LockStatus(path); statusErr == nil && running {
				return nil, fmt.Errorf("daemon already running (pid %d)", pid)
			}
			return nil, fmt.Errorf("daemon already running")
		}
		return nil, e
	}
	if e = f.Truncate(0); e == nil {
		_, e = f.Seek(0, 0)
	}
	if e == nil {
		_, e = fmt.Fprint(f, os.Getpid())
	}
	if e != nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		return nil, e
	}
	_ = f.Sync()
	return &Lock{file: f}, nil
}
func (l *Lock) Release() {
	if l == nil {
		return
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	_ = l.file.Close()
}

// LockStatus distinguishes a live owner from a stale, intentionally
// persistent lock file. flock ownership is released by the kernel on exit,
// including SIGKILL.
func LockStatus(path string) (pid int, running bool, err error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if os.IsNotExist(err) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	defer func() { _ = f.Close() }()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return 0, false, nil
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
		return 0, false, err
	}
	b := make([]byte, 64)
	n, readErr := f.ReadAt(b, 0)
	if readErr != nil && n == 0 {
		return 0, true, readErr
	}
	pid, err = strconv.Atoi(strings.TrimSpace(string(b[:n])))
	if err != nil || pid <= 1 {
		return 0, true, fmt.Errorf("daemon lock contains invalid pid")
	}
	return pid, true, nil
}

func Stop(path string) error {
	pid, running, e := LockStatus(path)
	if e != nil {
		return e
	}
	if !running {
		return fmt.Errorf("daemon is not running")
	}
	p, e := os.FindProcess(pid)
	if e != nil {
		return e
	}
	return p.Signal(os.Interrupt)
}

type Watcher struct {
	Sweeper        *Sweeper
	Spool          *spool.Spool
	SweepTimeout   time.Duration
	mu             sync.Mutex
	running, dirty bool
}

func (w *Watcher) trigger(ctx context.Context) {
	w.Sweeper.note("last_poll", time.Now())
	w.mu.Lock()
	if w.running {
		w.dirty = true
		w.mu.Unlock()
		return
	}
	w.running = true
	w.mu.Unlock()
	go func() {
		for {
			timeout := w.SweepTimeout
			if timeout <= 0 {
				timeout = 3 * time.Minute
			}
			sweepCtx, cancel := context.WithTimeout(ctx, timeout)
			_ = w.Sweeper.Sweep(sweepCtx, SweepOptions{})
			cancel()
			w.mu.Lock()
			if !w.dirty {
				w.running = false
				w.mu.Unlock()
				return
			}
			w.dirty = false
			w.mu.Unlock()
		}
	}()
}
func Watch(ctx context.Context, w *Watcher, paths []string) error {
	f, e := fsnotify.NewWatcher()
	if e != nil {
		return e
	}
	defer func() { _ = f.Close() }()
	for _, p := range paths {
		_ = filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = f.Add(path)
			}
			return nil
		})
	}
	w.trigger(ctx)
	activeEvery := time.Duration(w.Sweeper.Config.ActivePollSeconds) * time.Second
	if activeEvery <= 0 {
		activeEvery = 15 * time.Second
	}
	idleEvery := time.Duration(w.Sweeper.Config.IdlePollSeconds) * time.Second
	if idleEvery <= 0 {
		idleEvery = 5 * time.Minute
	}
	poll := time.NewTicker(activeEvery)
	defer poll.Stop()
	lastChange, lastIdlePoll := time.Now(), time.Now()
	debounce := time.NewTimer(time.Hour)
	if !debounce.Stop() {
		<-debounce.C
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-f.Errors:
			if e != nil {
				w.Sweeper.note("watch_error", e.Error())
			}
		case ev := <-f.Events:
			if ev.Name != "" {
				lastChange = time.Now()
				debounce.Reset(2 * time.Second)
			}
		case <-debounce.C:
			w.trigger(ctx)
		case now := <-poll.C:
			if now.Sub(lastChange) <= 10*time.Minute || now.Sub(lastIdlePoll) >= idleEvery {
				w.trigger(ctx)
				if now.Sub(lastChange) > 10*time.Minute {
					lastIdlePoll = now
				}
			}
		}
	}
}
