package doctor

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CaliLuke/earwig/internal/config"
	"github.com/CaliLuke/earwig/internal/spool"
)

func TestRunReportsReadyWithPackagedDependencies(t *testing.T) {
	dir := t.TempDir()
	workspace := filepath.Join(dir, "workspace")
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0700); err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(binDir, "claude-reader")
	if err := os.WriteFile(helper, []byte("helper"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte("codex"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("EARWIG_CONFIG", filepath.Join(dir, "missing-config.toml"))
	cfg := config.Config{
		WorkspaceRoots: []string{workspace}, Claude: true, Codex: true,
		ClaudeHelper: helper, SpoolPath: filepath.Join(dir, "spool.sqlite"),
		JSONDir: filepath.Join(dir, "json"),
	}
	var output bytes.Buffer
	if err := Run(cfg, &output); err != nil {
		t.Fatalf("doctor failed: %v\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "[ok] workspace root") || !strings.HasSuffix(output.String(), "ready\n") {
		t.Fatalf("unexpected doctor output:\n%s", output.String())
	}
}

func TestRunReportsMissingHelper(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EARWIG_CONFIG", filepath.Join(dir, "missing-config.toml"))
	cfg := config.Config{
		WorkspaceRoots: []string{dir}, Claude: true,
		ClaudeHelper: filepath.Join(dir, "missing-helper"), SpoolPath: filepath.Join(dir, "spool.sqlite"),
	}
	var output bytes.Buffer
	if err := Run(cfg, &output); err == nil || !strings.Contains(output.String(), "[error] Claude helper") {
		t.Fatalf("doctor did not report missing helper: err=%v\n%s", err, output.String())
	}
}
func TestRunReportsPersistedCaptureError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EARWIG_CONFIG", filepath.Join(dir, "missing-config.toml"))
	cfg := config.Config{
		WorkspaceRoots: []string{dir},
		SpoolPath:      filepath.Join(dir, "spool.sqlite"),
	}
	store, err := spool.Open(cfg.SpoolPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetHealth("last_sweep_error", "fork/exec /old/claude-reader: no such file or directory"); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	err = Run(cfg, &output)
	if err == nil {
		t.Fatalf("doctor reported ready despite capture error:\n%s", output.String())
	}
	if !strings.Contains(output.String(), "[error] last capture: fork/exec /old/claude-reader: no such file or directory") {
		t.Fatalf("doctor did not surface capture error: err=%v\n%s", err, output.String())
	}
}
