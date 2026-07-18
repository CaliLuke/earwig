//go:build live

package livevalidation

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CaliLuke/earwig/internal/config"
	"github.com/CaliLuke/earwig/internal/daemon"
)

var lifecycleSessions = []string{
	"10000000-0000-4000-8000-000000000001",
	"10000000-0000-4000-8000-000000000002",
	"10000000-0000-4000-8000-000000000003",
	"10000000-0000-4000-8000-000000000004",
	"10000000-0000-4000-8000-000000000005",
}

func TestV5DaemonLifecycleConcurrencyAndRSS(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("flock, signals, and RSS probing require Unix")
	}
	directory := t.TempDir()
	home := filepath.Join(directory, "home")
	workspace := filepath.Join(directory, "workspace")
	jsonDir := filepath.Join(directory, "json")
	for _, path := range []string{home, workspace, filepath.Join(home, ".claude", "projects"), filepath.Join(home, ".codex", "sessions")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	helper := writeStubHelper(t, directory, workspace, lifecycleSessions)
	spoolPath := filepath.Join(directory, "spool.sqlite")
	configPath := writeConfig(t, directory, fmt.Sprintf("workspace_roots = [%q]\nclaude = true\ncodex = false\nspool_path = %q\njsondir = %q\nopik_url = \"\"\nclaude_helper = %q\nactive_poll_seconds = 1\nidle_poll_seconds = 2\n", workspace, spoolPath, jsonDir, helper))
	processEnv := append(os.Environ(), "EARWIG_CONFIG="+configPath, "HOME="+home, "CODEX_HOME="+filepath.Join(home, ".codex"))
	startWatch := func() (*exec.Cmd, *bytes.Buffer) {
		command := exec.Command(liveBinary(t), "watch")
		command.Env = processEnv
		var output bytes.Buffer
		command.Stdout = &output
		command.Stderr = &output
		if err := command.Start(); err != nil {
			t.Fatal(err)
		}
		return command, &output
	}
	stopProcess := func(command *exec.Cmd) {
		if command == nil || command.ProcessState != nil {
			return
		}
		_ = command.Process.Kill()
		_ = command.Wait()
	}

	slow := filepath.Join(directory, "helper-slow")
	marker := filepath.Join(directory, "helper-active")
	if err := os.WriteFile(slow, []byte("slow"), 0600); err != nil {
		t.Fatal(err)
	}
	first, _ := startWatch()
	t.Cleanup(func() { stopProcess(first) })
	waitFor(t, 10*time.Second, "first daemon entering startup sweep", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
	if err := first.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = first.Wait()
	if err := os.Remove(slow); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	restarted, restartedOutput := startWatch()
	t.Cleanup(func() { stopProcess(restarted) })
	waitFor(t, 10*time.Second, "daemon restart acquiring lock", func() bool {
		pid, running, err := daemon.LockStatus(spoolPath + ".lock")
		return err == nil && running && pid == restarted.Process.Pid
	})
	s := openSpool(t, spoolPath)
	waitFor(t, 10*time.Second, "restart convergence", func() bool {
		turns, _, _ := s.Stats()
		exports, _ := s.ExportCount("jsondir")
		return turns == len(lifecycleSessions) && exports == len(lifecycleSessions)
	})

	second := exec.Command(liveBinary(t), "watch")
	second.Env = processEnv
	secondOutput, secondErr := second.CombinedOutput()
	if secondErr == nil {
		t.Fatal("second watch instance unexpectedly started")
	}
	exitError, ok := secondErr.(*exec.ExitError)
	if !ok || exitError.ExitCode() != 2 || !strings.Contains(string(secondOutput), strconv.Itoa(restarted.Process.Pid)) {
		t.Fatalf("second watch contention: %v: %s", secondErr, secondOutput)
	}

	if err := os.WriteFile(slow, []byte("slow"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	waitFor(t, 10*time.Second, "resident daemon actively sweeping", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
	type hookResult struct {
		output string
		err    error
	}
	results := make(chan hookResult, len(lifecycleSessions))
	var group sync.WaitGroup
	for _, sessionID := range lifecycleSessions {
		sessionID := sessionID
		group.Add(1)
		go func() {
			defer group.Done()
			command := exec.Command(liveBinary(t), "hook", "claude", "--managed-by-earwig")
			command.Env = processEnv
			command.Stdin = strings.NewReader(fmt.Sprintf(`{"session_id":%q}`, sessionID))
			output, err := command.CombinedOutput()
			results <- hookResult{output: string(output), err: err}
		}()
	}
	group.Wait()
	close(results)
	if err := os.Remove(slow); err != nil {
		t.Fatal(err)
	}
	for result := range results {
		if result.err != nil || strings.Contains(strings.ToLower(result.output), "database is locked") {
			t.Fatalf("concurrent hook failed: %v: %s", result.err, result.output)
		}
	}
	waitFor(t, 10*time.Second, "all hook captures", func() bool { return countTurns(t, s) == len(lifecycleSessions) })

	waitFor(t, 10*time.Second, "startup sweep completion", func() bool {
		value, err := s.GetHealth("last_sweep_completed")
		return err == nil && value != "null"
	})
	time.Sleep(30 * time.Second)
	rssCommand := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(restarted.Process.Pid))
	rssOutput, err := rssCommand.Output()
	if err != nil {
		t.Fatal(err)
	}
	rssKB, err := strconv.Atoi(strings.TrimSpace(string(rssOutput)))
	if err != nil {
		t.Fatal(err)
	}
	if rssKB >= 30*1024 {
		t.Fatalf("idle RSS %d KB exceeds 30 MB", rssKB)
	}
	t.Logf("IDLE_RSS_KB=%d (%.1f MB)", rssKB, float64(rssKB)/1024)

	stop := exec.Command(liveBinary(t), "stop")
	stop.Env = processEnv
	if output, err := stop.CombinedOutput(); err != nil {
		t.Fatalf("earwig stop: %v: %s", err, output)
	}
	done := make(chan error, 1)
	go func() { done <- restarted.Wait() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not stop")
	}
	status := exec.Command(liveBinary(t), "status")
	status.Env = processEnv
	statusOutput, statusErr := status.CombinedOutput()
	if statusErr != nil || !strings.Contains(string(statusOutput), "daemon: stopped") {
		t.Fatalf("stopped status: %v: %s", statusErr, statusOutput)
	}
	t.Logf("SIGKILL restart converged, second watch named PID, five concurrent hooks succeeded, and stop terminated PID %d", restarted.Process.Pid)
	_ = restartedOutput
}

func TestV5HangResistance(t *testing.T) {
	directory := t.TempDir()
	workspace := filepath.Join(directory, "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	helper := writeStubHelper(t, directory, workspace, []string{liveSessionID})
	hang := filepath.Join(directory, "hang")
	body, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	body = append([]byte(fmt.Sprintf("#!/bin/sh\nif [ -f %q ]; then exec sleep 300; fi\n", hang)), bytes.TrimPrefix(body, []byte("#!/bin/sh\n"))...)
	if err = os.WriteFile(helper, body, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(hang, []byte("hang"), 0600); err != nil {
		t.Fatal(err)
	}
	s := openSpool(t, filepath.Join(directory, "spool.sqlite"))
	sweeper := &daemon.Sweeper{Config: config.Config{WorkspaceRoots: []string{workspace}, Claude: true, ClaudeHelper: helper, ActivePollSeconds: 1}, Spool: s}
	watcher := &daemon.Watcher{Sweeper: sweeper, Spool: s, SweepTimeout: 100 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- daemon.Watch(ctx, watcher, []string{workspace}) }()
	defer func() {
		cancel()
		<-done
	}()
	waitFor(t, 5*time.Second, "timed-out sweep health", func() bool {
		value, healthErr := s.GetHealth("last_sweep_error")
		return healthErr == nil && strings.Contains(value, "deadline exceeded")
	})
	if err = os.Remove(hang); err != nil {
		t.Fatal(err)
	}
	trigger := filepath.Join(workspace, "trigger")
	if err = os.WriteFile(trigger, []byte("retry"), 0600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 8*time.Second, "successful sweep after timeout", func() bool {
		value, healthErr := s.GetHealth("last_sweep_error")
		return healthErr == nil && value == "null" && countTurns(t, s) == 1
	})
	t.Log("hung helper timed out, surfaced health error, and the next trigger recovered")
}

func TestV5LaunchdReducedEnvironment(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("launchd simulation applies only to macOS")
	}
	directory := t.TempDir()
	binDir := filepath.Join(directory, "bin")
	libexecDir := filepath.Join(directory, "libexec")
	home := filepath.Join(directory, "home")
	workspace := filepath.Join(directory, "workspace")
	for _, path := range []string{binDir, libexecDir, filepath.Join(home, ".config", "earwig"), workspace} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	binaryBytes, err := os.ReadFile(liveBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(binDir, "earwig")
	if err = os.WriteFile(binary, binaryBytes, 0700); err != nil {
		t.Fatal(err)
	}
	helper := writeStubHelper(t, directory, workspace, []string{liveSessionID})
	helperBytes, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(libexecDir, "claude-reader"), helperBytes, 0700); err != nil {
		t.Fatal(err)
	}
	spoolPath := filepath.Join(directory, "spool.sqlite")
	configPath := filepath.Join(home, ".config", "earwig", "config.toml")
	configBody := fmt.Sprintf("workspace_roots = [%q]\nclaude = true\ncodex = false\nspool_path = %q\njsondir = \"\"\nopik_url = \"\"\n", workspace, spoolPath)
	if err = os.WriteFile(configPath, []byte(configBody), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(binary, "sweep", "--provider", "claude")
	command.Dir = "/"
	command.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + home}
	if output, runErr := command.CombinedOutput(); runErr != nil {
		t.Fatalf("launchd simulation: %v: %s", runErr, output)
	}
	s := openSpool(t, spoolPath)
	if count := countTurns(t, s); count != 1 {
		t.Fatalf("launchd simulation captured %d turns", count)
	}
	t.Log("cwd=/ with reduced PATH/HOME resolved config and executable-relative helper")
}
