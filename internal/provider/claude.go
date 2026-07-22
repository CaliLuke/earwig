package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

type ClaudeSession struct {
	ID           string `json:"id"`
	Summary      string `json:"summary"`
	CWD          string `json:"cwd"`
	LastModified any    `json:"last_modified"`
}
type ClaudeReader struct{ Helper string }

func claudeCommand(ctx context.Context, name string, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, name, args...)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	c.Cancel = func() error {
		if c.Process == nil {
			return os.ErrProcessDone
		}
		if err := syscall.Kill(-c.Process.Pid, syscall.SIGKILL); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		return nil
	}
	return c
}

func (r ClaudeReader) command(ctx context.Context, args ...string) ([]byte, error) {
	if r.Helper == "" {
		return nil, fmt.Errorf("claude helper missing: configure claude_helper (expected SDK 0.3.217)")
	}
	var c *exec.Cmd
	if filepath.Ext(r.Helper) == ".mjs" {
		c = claudeCommand(ctx, "node", append([]string{r.Helper}, args...)...)
	} else {
		c = claudeCommand(ctx, r.Helper, args...)
	}
	b, err := c.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return b, err
}
func (r ClaudeReader) List(ctx context.Context) ([]ClaudeSession, error) {
	b, e := r.command(ctx, "list")
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
