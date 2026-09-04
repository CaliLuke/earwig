package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadResolvesConfiguredPathsFromConfigDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	contents := `workspace_roots = ["workspace"]
spool_path = "state/spool.sqlite"
jsondir = ""
claude_helper = "../bin/claude-reader"
omp_sessions_path = "omp/sessions"
opik_project = "review-project"
`
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EARWIG_CONFIG", path)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.WorkspaceRoots[0] != filepath.Join(dir, "workspace") {
		t.Fatalf("workspace root = %q", cfg.WorkspaceRoots[0])
	}
	if cfg.SpoolPath != filepath.Join(dir, "state", "spool.sqlite") {
		t.Fatalf("spool path = %q", cfg.SpoolPath)
	}
	if cfg.JSONDir != "" {
		t.Fatalf("disabled JSON dir = %q", cfg.JSONDir)
	}
	if cfg.ClaudeHelper != filepath.Clean(filepath.Join(dir, "..", "bin", "claude-reader")) {
		t.Fatalf("helper path = %q", cfg.ClaudeHelper)
	}
	if cfg.OMPSessionsPath != filepath.Join(dir, "omp", "sessions") {
		t.Fatalf("OMP sessions path = %q", cfg.OMPSessionsPath)
	}
	if cfg.OpikProject != "review-project" {
		t.Fatalf("Opik project = %q", cfg.OpikProject)
	}
}

func TestCodexSessionsPathHonorsCodexHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "codex-home")
	t.Setenv("CODEX_HOME", home)
	if got := CodexSessionsPath(); got != filepath.Join(home, "sessions") {
		t.Fatalf("Codex sessions path = %q", got)
	}
}

func TestDefaultClaudeHelperIsAbsolute(t *testing.T) {
	if helper := Default().ClaudeHelper; !filepath.IsAbs(helper) {
		t.Fatalf("default helper is relative: %q", helper)
	}
}

func TestDefaultOMPSessionsPathUsesProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PI_CONFIG_DIR", ".custom-omp")
	t.Setenv("PI_CODING_AGENT_DIR", filepath.Join(home, "ignored-agent"))
	t.Setenv("OMP_PROFILE", "review")
	want := filepath.Join(home, ".custom-omp", "profiles", "review", "agent", "sessions")
	if got := DefaultOMPSessionsPath(); got != want {
		t.Fatalf("OMP sessions path = %q, want %q", got, want)
	}
}
