// Package doctor checks whether Earwig's local runtime dependencies are ready.
package doctor

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/CaliLuke/earwig/internal/config"
	"github.com/CaliLuke/earwig/internal/spool"
)

func Run(cfg config.Config, out io.Writer) error {
	var problems []error
	var writeErr error
	report := func(ok bool, label, detail string) {
		state := "ok"
		if !ok {
			state = "error"
			problems = append(problems, fmt.Errorf("%s: %s", label, detail))
		}
		if _, err := fmt.Fprintf(out, "[%s] %s: %s\n", state, label, detail); err != nil && writeErr == nil {
			writeErr = err
		}
	}

	supported := runtime.GOOS == "darwin" || runtime.GOOS == "linux"
	report(supported, "platform", runtime.GOOS+"/"+runtime.GOARCH)
	if _, err := os.Stat(config.Path()); err == nil {
		report(true, "config", config.Path())
	} else if os.IsNotExist(err) {
		report(true, "config", "defaults (no config file)")
	} else {
		report(false, "config", err.Error())
	}

	if len(cfg.WorkspaceRoots) == 0 {
		report(false, "workspace roots", "none configured")
	}
	for _, root := range cfg.WorkspaceRoots {
		info, err := os.Stat(root)
		report(err == nil && info.IsDir(), "workspace root", pathStatus(root, info, err))
	}

	if cfg.Claude {
		info, err := os.Stat(cfg.ClaudeHelper)
		helperOK := err == nil && !info.IsDir()
		report(helperOK, "Claude helper", pathStatus(cfg.ClaudeHelper, info, err))
		if helperOK && filepath.Ext(cfg.ClaudeHelper) == ".mjs" {
			_, nodeErr := exec.LookPath("node")
			report(nodeErr == nil, "Node.js", commandStatus("node", nodeErr))
		}
	}
	if cfg.Codex {
		_, err := exec.LookPath("codex")
		report(err == nil, "Codex CLI", commandStatus("codex", err))
	}

	if !filepath.IsAbs(cfg.SpoolPath) {
		report(false, "spool", cfg.SpoolPath)
	} else {
		captureError, err := readCaptureError(cfg.SpoolPath)
		detail := cfg.SpoolPath
		if err != nil {
			detail += ": " + err.Error()
		}
		report(err == nil, "spool", detail)
		if err == nil && captureError != "" {
			report(false, "last capture", captureError)
		}
	}
	if cfg.JSONDir != "" {
		report(filepath.IsAbs(cfg.JSONDir), "JSON exporter", cfg.JSONDir)
	}
	if cfg.OpikURL != "" {
		report(true, "Opik exporter", cfg.OpikURL+" (connectivity checked during sweep)")
	}

	if len(problems) == 0 {
		if _, err := fmt.Fprintln(out, "ready"); err != nil && writeErr == nil {
			writeErr = err
		}
		return writeErr
	}
	if _, err := fmt.Fprintf(out, "not ready: %d problem(s)\n", len(problems)); err != nil && writeErr == nil {
		writeErr = err
	}
	if writeErr != nil {
		problems = append(problems, writeErr)
	}
	return errors.Join(problems...)
}

func pathStatus(path string, info os.FileInfo, err error) string {
	if err != nil {
		return path + ": " + err.Error()
	}
	if info.IsDir() {
		return path
	}
	return path
}

func commandStatus(name string, err error) string {
	if err != nil {
		return err.Error()
	}
	path, _ := exec.LookPath(name)
	return path
}
func readCaptureError(path string) (string, error) {
	store, err := spool.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = store.Close() }()

	raw, err := store.GetHealth("last_sweep_error")
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var captureError *string
	if err = json.Unmarshal([]byte(raw), &captureError); err != nil {
		return "", err
	}
	if captureError == nil {
		return "", nil
	}
	return *captureError, nil
}
