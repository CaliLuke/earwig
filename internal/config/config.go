package config

import (
	"fmt"
	"github.com/pelletier/go-toml/v2"
	"os"
	"path/filepath"
	"strings"
)

type Config struct {
	WorkspaceRoots    []string `toml:"workspace_roots"`
	Claude            bool     `toml:"claude"`
	Codex             bool     `toml:"codex"`
	SpoolPath         string   `toml:"spool_path"`
	JSONDir           string   `toml:"jsondir"`
	OpikURL           string   `toml:"opik_url"`
	OpikProject       string   `toml:"opik_project"`
	ClaudeHelper      string   `toml:"claude_helper"`
	ActivePollSeconds int      `toml:"active_poll_seconds"`
	IdlePollSeconds   int      `toml:"idle_poll_seconds"`
}

func Default() Config {
	h, _ := os.UserHomeDir()
	project := os.Getenv("OPIK_PROJECT_NAME")
	if project == "" {
		project = "earwig"
	}
	return Config{WorkspaceRoots: []string{h}, Claude: true, Codex: true, SpoolPath: filepath.Join(h, ".local", "share", "earwig", "spool.sqlite"), JSONDir: filepath.Join(h, ".local", "share", "earwig", "json"), OpikProject: project, ClaudeHelper: defaultClaudeHelper(), ActivePollSeconds: 15, IdlePollSeconds: 300}
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
	path := Path()
	if !filepath.IsAbs(path) {
		path, _ = filepath.Abs(path)
	}
	b, e := os.ReadFile(path)
	if os.IsNotExist(e) {
		return c, nil
	}
	if e != nil {
		return c, e
	}
	if e = toml.Unmarshal(b, &c); e != nil {
		return c, e
	}
	base := filepath.Dir(path)
	for i := range c.WorkspaceRoots {
		c.WorkspaceRoots[i] = absoluteFrom(base, c.WorkspaceRoots[i])
	}
	c.SpoolPath = absoluteFrom(base, c.SpoolPath)
	c.JSONDir = absoluteFrom(base, c.JSONDir)
	c.ClaudeHelper = absoluteFrom(base, c.ClaudeHelper)
	if c.SpoolPath == "" {
		return c, fmt.Errorf("spool_path must not be empty")
	}
	if c.OpikURL != "" && strings.TrimSpace(c.OpikProject) == "" {
		return c, fmt.Errorf("opik_project must not be empty when opik_url is configured")
	}
	return c, nil
}

func defaultClaudeHelper() string {
	exe, err := os.Executable()
	if err != nil {
		path, _ := filepath.Abs(filepath.Join("helpers", "claude-reader", "index.mjs"))
		return path
	}
	dir := filepath.Dir(exe)
	if resolved, resolveErr := filepath.EvalSymlinks(exe); resolveErr == nil {
		dir = filepath.Dir(resolved)
	}
	candidates := []string{
		filepath.Join(dir, "..", "libexec", "claude-reader"),
		filepath.Join(dir, "..", "libexec", "earwig", "claude-reader"),
		filepath.Join(dir, "claude-reader"),
		filepath.Join(dir, "helpers", "claude-reader", "index.mjs"),
	}
	for _, candidate := range candidates {
		candidate = filepath.Clean(candidate)
		if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
			return candidate
		}
	}
	return filepath.Clean(candidates[len(candidates)-1])
}

func absoluteFrom(base, path string) string {
	if path == "" {
		return ""
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(base, path))
}

func CodexSessionsPath() string {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		userHome, _ := os.UserHomeDir()
		home = filepath.Join(userHome, ".codex")
	}
	return filepath.Join(filepath.Clean(home), "sessions")
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
