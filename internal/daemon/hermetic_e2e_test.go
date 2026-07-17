package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/CaliLuke/earwig/internal/config"
	"github.com/CaliLuke/earwig/internal/spool"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestHermeticClaudeSDKEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture project-key encoding is Unix-specific")
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(repoRoot, "helpers", "claude-reader", "index.mjs")
	if _, err = os.Stat(filepath.Join(repoRoot, "helpers", "claude-reader", "node_modules")); err != nil {
		t.Fatalf("pinned helper dependencies are required: %v", err)
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err = os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	configDir := filepath.Join(root, "claude-config")
	projectKey := strings.ReplaceAll(workspace, string(filepath.Separator), "-")
	projectDir := filepath.Join(configDir, "projects", projectKey)
	if err = os.MkdirAll(projectDir, 0700); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile(filepath.Join(repoRoot, "testdata", "provider", "claude-session.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	fixture = []byte(strings.ReplaceAll(string(fixture), "__WORKSPACE__", workspace))
	sessionPath := filepath.Join(projectDir, "0aaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee.jsonl")
	if err = os.WriteFile(sessionPath, fixture, 0600); err != nil {
		t.Fatal(err)
	}
	readLog := filepath.Join(root, "reads.log")
	wrapper := filepath.Join(root, "claude-reader-wrapper")
	wrapperBody := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = read ]; then printf 'read\\n' >> %q; fi\nexec node %q \"$@\"\n", readLog, helper)
	if err = os.WriteFile(wrapper, []byte(wrapperBody), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", configDir)

	spoolPath := filepath.Join(root, "spool.sqlite")
	s, err := spool.Open(spoolPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	exportDir := filepath.Join(root, "json")
	var sweepLog bytes.Buffer
	sweeper := &Sweeper{Config: config.Config{WorkspaceRoots: []string{workspace}, Claude: true, ClaudeHelper: wrapper, JSONDir: exportDir}, Spool: s, Log: log.New(&sweepLog, "", 0)}
	if err = sweeper.Sweep(context.Background(), SweepOptions{Provider: "claude"}); err != nil {
		t.Fatal(err)
	}
	assertReadCount(t, readLog, 1)

	var turnCount int
	if err = s.DB.QueryRow(`SELECT COUNT(*) FROM turns`).Scan(&turnCount); err != nil || turnCount != 2 {
		t.Fatalf("completed/failed turn count = %d, %v", turnCount, err)
	}
	rows, err := s.Pending("inspection", 10)
	if err != nil || len(rows) != 2 {
		t.Fatalf("inspection rows = %d, %v", len(rows), err)
	}
	var first spool.TurnPayload
	if err = json.Unmarshal([]byte(rows[0].Payload), &first); err != nil {
		t.Fatal(err)
	}
	if first.Turn.Status != "completed" || first.Turn.Trajectory[0]["input"].(map[string]any)["command"] != "API_TOKEN=[REDACTED] echo ok" {
		t.Fatalf("first turn lost status/redaction: %#v", first.Turn)
	}
	if len(first.Turn.FollowingUserMessages) != 1 {
		t.Fatalf("redirect linkage missing: %#v", first.Turn.FollowingUserMessages)
	}
	var compactions int
	if err = s.DB.QueryRow(`SELECT compaction_count FROM sessions`).Scan(&compactions); err != nil || compactions != 1 {
		t.Fatalf("compactions = %d, %v", compactions, err)
	}
	var gapWarned int
	if err = s.DB.QueryRow(`SELECT gap_warned FROM sessions`).Scan(&gapWarned); err != nil || gapWarned != 0 || strings.Contains(sweepLog.String(), "GAP WARNING") {
		t.Fatalf("pre-adoption compaction warned: gap=%d log=%q err=%v", gapWarned, sweepLog.String(), err)
	}

	if err = sweeper.Sweep(context.Background(), SweepOptions{Provider: "claude"}); err != nil {
		t.Fatal(err)
	}
	assertReadCount(t, readLog, 1)
	var adoptedAtMS int64
	if err = s.DB.QueryRow(`SELECT last_swept_ms FROM sessions`).Scan(&adoptedAtMS); err != nil {
		t.Fatal(err)
	}
	compactedAt := time.UnixMilli(adoptedAtMS + 1).UTC().Format(time.RFC3339Nano)
	compactionEntry, err := json.Marshal(map[string]any{
		"type": "user", "uuid": "99999999-9999-4999-8999-999999999999", "parentUuid": "88888888-8888-4888-8888-888888888888", "sessionId": "0aaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "timestamp": compactedAt, "cwd": workspace, "gitBranch": "main", "version": "1.0.0", "userType": "external", "isSidechain": false,
		"message": map[string]any{"role": "user", "content": "This session is being continued from a previous conversation. New summary."},
	})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(sessionPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = file.Write(append(compactionEntry, '\n')); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	if err = os.Chtimes(sessionPath, future, future); err != nil {
		t.Fatal(err)
	}
	if err = sweeper.Sweep(context.Background(), SweepOptions{Provider: "claude"}); err != nil {
		t.Fatal(err)
	}
	assertReadCount(t, readLog, 2)
	if err = s.DB.QueryRow(`SELECT gap_warned,compaction_count FROM sessions`).Scan(&gapWarned, &compactions); err != nil || gapWarned != 1 || compactions != 2 {
		t.Fatalf("post-adoption compaction gap=%d compactions=%d err=%v", gapWarned, compactions, err)
	}
	if count := strings.Count(sweepLog.String(), "GAP WARNING"); count != 1 {
		t.Fatalf("gap warning count = %d: %q", count, sweepLog.String())
	}
	future = future.Add(2 * time.Second)
	if err = os.Chtimes(sessionPath, future, future); err != nil {
		t.Fatal(err)
	}
	if err = sweeper.Sweep(context.Background(), SweepOptions{Provider: "claude"}); err != nil {
		t.Fatal(err)
	}
	assertReadCount(t, readLog, 3)
	if count := strings.Count(sweepLog.String(), "GAP WARNING"); count != 1 {
		t.Fatalf("gap warning repeated: %q", sweepLog.String())
	}
}
