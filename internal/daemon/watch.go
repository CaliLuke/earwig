package daemon

import (
	"context"
	"fmt"
	"github.com/CaliLuke/earwig/internal/spool"
	"github.com/fsnotify/fsnotify"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Lock struct {
	path string
	file *os.File
}

func Acquire(path string) (*Lock, error) {
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return nil, e
	}
	_, _ = fmt.Fprint(f, os.Getpid())
	return &Lock{path, f}, nil
}
func (l *Lock) Release() {
	if l == nil {
		return
	}
	_ = l.file.Close()
	_ = os.Remove(l.path)
}
func Stop(path string) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	pid, e := strconv.Atoi(strings.TrimSpace(string(b)))
	if e != nil {
		return e
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
			_ = w.Sweeper.Sweep(ctx, SweepOptions{})
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
	defer f.Close()
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
