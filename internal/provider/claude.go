package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"
)

type ClaudeSession struct {
	ID           string `json:"id"`
	Summary      string `json:"summary"`
	CWD          string `json:"cwd"`
	LastModified any    `json:"last_modified"`
}
type ClaudeReader struct{ Helper string }

func (r ClaudeReader) command(ctx context.Context, args ...string) ([]byte, error) {
	if r.Helper == "" {
		return nil, fmt.Errorf("Claude helper missing: configure claude_helper (expected SDK 0.3.212)")
	}
	var c *exec.Cmd
	if filepath.Ext(r.Helper) == ".mjs" {
		c = exec.CommandContext(ctx, "node", append([]string{r.Helper}, args...)...)
	} else {
		c = exec.CommandContext(ctx, r.Helper, args...)
	}
	b, err := c.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return b, err
}
func (r ClaudeReader) List(ctx context.Context, dir string) ([]ClaudeSession, error) {
	b, e := r.command(ctx, "list", "--dir", dir)
	if e != nil {
		return nil, e
	}
	var out []ClaudeSession
	e = json.Unmarshal(b, &out)
	return out, e
}
func (r ClaudeReader) Read(ctx context.Context, id, dir string) (map[string]any, []any, error) {
	if !ValidClaudeID(id) {
		return nil, nil, fmt.Errorf("invalid Claude session ID")
	}
	b, e := r.command(ctx, "read", id, "--dir", dir)
	if e != nil {
		return nil, nil, e
	}
	var x struct {
		Info     map[string]any `json:"info"`
		Messages []any          `json:"messages"`
	}
	e = json.Unmarshal(b, &x)
	return x.Info, x.Messages, e
}
func DefaultClaudeTimeout() time.Duration { return 120 * time.Second }
