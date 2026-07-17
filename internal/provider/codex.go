package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

type CodexClient struct {
	cmd       *exec.Cmd
	in        chan []byte
	responses map[float64]chan map[string]any
	mu        sync.Mutex
	next      int
	done      chan struct{}
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
	x := &CodexClient{cmd: c, responses: map[float64]chan map[string]any{}, next: 1, done: make(chan struct{})}
	go func() {
		defer close(x.done)
		scan := bufio.NewScanner(out)
		buf := make([]byte, 1024*1024)
		scan.Buffer(buf, 128*1024*1024)
		for scan.Scan() {
			var m map[string]any
			if json.Unmarshal(scan.Bytes(), &m) != nil {
				continue
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
	x.in = make(chan []byte)
	go func() {
		for b := range x.in {
			_, _ = in.Write(append(b, '\n'))
		}
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
	b, _ := json.Marshal(map[string]any{"method": method, "id": id, "params": params})
	select {
	case x.in <- b:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case m := <-ch:
		if er, ok := m["error"]; ok {
			return nil, fmt.Errorf("codex app-server: %v", er)
		}
		return m["result"], nil
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
	default:
	}
}
func (x *CodexClient) List(ctx context.Context, cwd string) ([]map[string]any, error) {
	r, e := x.request(ctx, "thread/list", map[string]any{"cwd": cwd, "limit": 100, "sortKey": "updated_at", "sortDirection": "desc"})
	if e != nil {
		return nil, e
	}
	return maps(obj(r)["data"]), nil
}
func (x *CodexClient) Read(ctx context.Context, id string) (map[string]any, error) {
	if !ValidCodexID(id) {
		return nil, fmt.Errorf("invalid Codex thread ID")
	}
	r, e := x.request(ctx, "thread/read", map[string]any{"threadId": id, "includeTurns": true})
	if e != nil {
		return nil, e
	}
	thread, ok := obj(r)["thread"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("codex app-server contract drift: thread/read omitted thread")
	}
	return thread, nil
}
func (x *CodexClient) Close()  { close(x.in); _ = x.cmd.Process.Kill(); <-x.done }
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
