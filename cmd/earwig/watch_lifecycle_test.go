//go:build !windows

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/CaliLuke/earwig/internal/daemon"
)

func TestWatchSIGINTAndSIGTERMWaitForActiveChild(t *testing.T) {
	for _, test := range []struct {
		name   string
		signal os.Signal
	}{
		{name: "SIGINT", signal: os.Interrupt},
		{name: "SIGTERM", signal: syscall.SIGTERM},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			workspace := filepath.Join(directory, "workspace")
			home := filepath.Join(directory, "home")
			for _, path := range []string{
				workspace,
				filepath.Join(home, ".claude", "projects"),
				filepath.Join(home, ".codex", "sessions"),
			} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			childPIDPath := filepath.Join(directory, "child.pid")
			helper := filepath.Join(directory, "hanging-claude-helper")
			helperBody := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$$\" > %q\nexec sleep 300\n", childPIDPath)
			if err := os.WriteFile(helper, []byte(helperBody), 0700); err != nil {
				t.Fatal(err)
			}
			spoolPath := filepath.Join(directory, "spool.sqlite")
			configPath := filepath.Join(directory, "config.toml")
			configBody := fmt.Sprintf(
				"workspace_roots = [%q]\nclaude = true\ncodex = false\nspool_path = %q\njsondir = \"\"\nopik_url = \"\"\nclaude_helper = %q\n",
				workspace,
				spoolPath,
				helper,
			)
			if err := os.WriteFile(configPath, []byte(configBody), 0600); err != nil {
				t.Fatal(err)
			}

			command := exec.Command(os.Args[0], "-test.run=^TestWatchSignalProcess$")
			command.Env = append(
				os.Environ(),
				"EARWIG_WATCH_SIGNAL_PROCESS=1",
				"EARWIG_CONFIG="+configPath,
				"HOME="+home,
				"CODEX_HOME="+filepath.Join(home, ".codex"),
			)
			var output bytes.Buffer
			command.Stdout = &output
			command.Stderr = &output
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if command.ProcessState == nil {
					_ = command.Process.Kill()
					_ = command.Wait()
				}
			})

			childPID := waitForPIDFile(t, childPIDPath)
			t.Cleanup(func() { _ = syscall.Kill(-childPID, syscall.SIGKILL) })
			if err := command.Process.Signal(test.signal); err != nil {
				t.Fatal(err)
			}
			waitDone := make(chan error, 1)
			go func() { waitDone <- command.Wait() }()
			select {
			case err := <-waitDone:
				if err != nil {
					t.Fatalf("watch exit after %s: %v\n%s", test.name, err, output.String())
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("watch did not exit after %s\n%s", test.name, output.String())
			}
			if processExists(childPID) {
				t.Fatalf("child PID %d remained after watch handled %s", childPID, test.name)
			}
			if _, running, err := daemon.LockStatus(spoolPath + ".lock"); err != nil || running {
				t.Fatalf("watch lock after %s: running=%t err=%v", test.name, running, err)
			}
		})
	}
}

func TestWatchSignalProcess(t *testing.T) {
	if os.Getenv("EARWIG_WATCH_SIGNAL_PROCESS") == "" {
		return
	}
	os.Exit(runCLI([]string{"watch"}, strings.NewReader(""), os.Stdout, os.Stderr))
}

func waitForPIDFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		body, err := os.ReadFile(path)
		if err == nil {
			pid, parseErr := strconv.Atoi(strings.TrimSpace(string(body)))
			if parseErr == nil && pid > 1 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for child PID file %s", path)
	return 0
}

func processExists(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
