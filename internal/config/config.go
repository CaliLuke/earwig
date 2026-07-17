package config

import (
	"github.com/pelletier/go-toml/v2"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	WorkspaceRoots    []string `toml:"workspace_roots"`
	Claude            bool     `toml:"claude"`
	Codex             bool     `toml:"codex"`
	JSONDir           string   `toml:"jsondir"`
	OpikURL           string   `toml:"opik_url"`
	ClaudeHelper      string   `toml:"claude_helper"`
	ActivePollSeconds int      `toml:"active_poll_seconds"`
	IdlePollSeconds   int      `toml:"idle_poll_seconds"`
}

func Default() Config {
	h, _ := os.UserHomeDir()
	return Config{WorkspaceRoots: []string{h}, Claude: true, Codex: true, JSONDir: filepath.Join(h, ".local", "share", "earwig", "json"), ClaudeHelper: "", ActivePollSeconds: 15, IdlePollSeconds: 300}
}
func Path() string {
	if p := os.Getenv("EARWIG_CONFIG"); p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "earwig", "config.toml")
}
func Load() (Config, error) {
	c := Default()
	b, e := os.ReadFile(Path())
	if os.IsNotExist(e) {
		return c, nil
	}
	if e != nil {
		return c, e
	}
	if e = toml.Unmarshal(b, &c); e != nil {
		return c, e
	}
	for i := range c.WorkspaceRoots {
		c.WorkspaceRoots[i] = filepath.Clean(c.WorkspaceRoots[i])
	}
	return c, nil
}
func Allowed(c Config, cwd string) bool {
	cwd = filepath.Clean(cwd)
	for _, r := range c.WorkspaceRoots {
		r = filepath.Clean(r)
		if cwd == r || strings.HasPrefix(cwd, r+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
