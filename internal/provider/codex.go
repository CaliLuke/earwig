package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

type CodexClient struct {
	cmd        *exec.Cmd
	in         chan []byte
	responses  map[float64]chan map[string]any
	mu         sync.Mutex
	next       int
	closing    chan struct{}
	closeOnce  sync.Once
	writerDone chan struct{}
	stdoutDone chan struct{}
	waitDone   chan struct{}
	waitErr    error
}

func StartCodex(ctx context.Context, cwd string) (*CodexClient, error) {
	c := exec.CommandContext(ctx, "codex", "app-server", "--listen", "stdio://")
	c.Dir = cwd
	in, e := c.StdinPipe()
	if e != nil {
		return nil, e
	}
	out, e := c.StdoutPipe()
	if e != nil {
		return nil, e
	}
	if e = c.Start(); e != nil {
		return nil, e
	}
	x := &CodexClient{
		cmd:        c,
		in:         make(chan []byte),
		responses:  map[float64]chan map[string]any{},
		next:       1,
		closing:    make(chan struct{}),
		writerDone: make(chan struct{}),
		stdoutDone: make(chan struct{}),
		waitDone:   make(chan struct{}),
	}
	go func() {
		defer close(x.stdoutDone)
		decoder := json.NewDecoder(out)
		for {
			var m map[string]any
			if decoder.Decode(&m) != nil {
				return
			}
			id, ok := m["id"].(float64)
			if !ok {
				continue
			}
			x.mu.Lock()
			ch := x.responses[id]
			delete(x.responses, id)
			x.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}()
	go func() {
		defer close(x.writerDone)
		defer func() { _ = in.Close() }()
		for {
			select {
			case b := <-x.in:
				_, _ = in.Write(append(b, '\n'))
			case <-x.closing:
				return
			}
		}
	}()
	// Stdout must be drained before Wait closes its pipe. This goroutine is
	// the sole owner of Wait, so every successfully started child is reaped
	// exactly once whether it exits naturally, is cancelled, or is closed.
	go func() {
		<-x.stdoutDone
		x.waitErr = c.Wait()
		close(x.waitDone)
	}()
	if _, e = x.request(ctx, "initialize", map[string]any{"clientInfo": map[string]any{"name": "earwig", "title": "earwig", "version": "1"}}); e != nil {
		x.Close()
		return nil, e
	}
	x.notify("initialized", map[string]any{})
	return x, nil
}
func (x *CodexClient) request(ctx context.Context, method string, params any) (any, error) {
	x.mu.Lock()
	id := x.next
	x.next++
	ch := make(chan map[string]any, 1)
	x.responses[float64(id)] = ch
	x.mu.Unlock()
	defer func() {
		x.mu.Lock()
		delete(x.responses, float64(id))
		x.mu.Unlock()
	}()
	b, _ := json.Marshal(map[string]any{"method": method, "id": id, "params": params})
	select {
	case x.in <- b:
	case <-x.closing:
		return nil, fmt.Errorf("codex app-server is closing")
	case <-x.waitDone:
		return nil, x.exitError()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case m := <-ch:
		if er, ok := m["error"]; ok {
			return nil, fmt.Errorf("codex app-server: %v", er)
		}
		return m["result"], nil
	case <-x.closing:
		return nil, fmt.Errorf("codex app-server is closing")
	case <-x.waitDone:
		return nil, x.exitError()
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("codex app-server request timed out: %s", method)
	}
}
func (x *CodexClient) notify(method string, params any) {
	b, _ := json.Marshal(map[string]any{"method": method, "params": params})
	select {
	case x.in <- b:
	case <-x.closing:
	case <-x.waitDone:
	default:
	}
}
func (x *CodexClient) List(ctx context.Context) ([]map[string]any, error) {
	return x.listPages(ctx, "thread/list", map[string]any{
		"limit":         100,
		"sortKey":       "updated_at",
		"sortDirection": "desc",
	})
}

func (x *CodexClient) listPages(ctx context.Context, method string, params map[string]any) ([]map[string]any, error) {
	items := []map[string]any{}
	seenCursors := map[string]bool{}
	for {
		r, err := x.request(ctx, method, params)
		if err != nil {
			return nil, err
		}
		result := obj(r)
		items = append(items, maps(result["data"])...)
		next, _ := result["nextCursor"].(string)
		if next == "" {
			return items, nil
		}
		if seenCursors[next] {
			return nil, fmt.Errorf("codex app-server returned repeated %s cursor", method)
		}
		seenCursors[next] = true
		params["cursor"] = next
	}
}

func (x *CodexClient) Read(ctx context.Context, id string) (map[string]any, error) {
	if !ValidCodexID(id) {
		return nil, fmt.Errorf("invalid Codex thread ID")
	}
	r, err := x.request(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": false})
	if err != nil {
		return nil, err
	}
	thread, ok := obj(r)["thread"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("codex app-server contract drift: thread/read omitted thread")
	}
	turns, err := x.listPages(ctx, "thread/turns/list", map[string]any{
		"threadId":      id,
		"limit":         100,
		"sortDirection": "asc",
		"itemsView":     "notLoaded",
	})
	if err != nil {
		return nil, err
	}
	turnByID := make(map[string]map[string]any, len(turns))
	rawTurns := make([]any, len(turns))
	for index, turn := range turns {
		turnID, _ := turn["id"].(string)
		if turnID == "" {
			return nil, fmt.Errorf("codex app-server contract drift: thread/turns/list omitted turn ID")
		}
		turn["items"] = []any{}
		turnByID[turnID] = turn
		rawTurns[index] = turn
	}
	if len(turns) > 0 {
		entries, listErr := x.listPages(ctx, "thread/items/list", map[string]any{
			"threadId":      id,
			"limit":         100,
			"sortDirection": "asc",
		})
		if listErr != nil {
			return nil, listErr
		}
		for _, entry := range entries {
			turnID, _ := entry["turnId"].(string)
			turn := turnByID[turnID]
			item, found := entry["item"]
			turnItems, validItems := turn["items"].([]any)
			if turn == nil || !found || !validItems {
				return nil, fmt.Errorf("codex app-server contract drift: thread/items/list returned an invalid entry")
			}
			turn["items"] = append(turnItems, item)
		}
	}
	thread["turns"] = rawTurns
	return thread, nil
}

func (x *CodexClient) Close() {
	x.closeOnce.Do(func() {
		close(x.closing)
		_ = x.cmd.Process.Kill()
		<-x.waitDone
		<-x.writerDone
	})
}

func (x *CodexClient) exitError() error {
	if x.waitErr == nil {
		return fmt.Errorf("codex app-server exited")
	}
	return fmt.Errorf("codex app-server exited: %w", x.waitErr)
}

func obj(v any) map[string]any { m, _ := v.(map[string]any); return m }
func maps(v any) []map[string]any {
	a, _ := v.([]any)
	o := []map[string]any{}
	for _, q := range a {
		if m, ok := q.(map[string]any); ok {
			o = append(o, m)
		}
	}
	return o
}
