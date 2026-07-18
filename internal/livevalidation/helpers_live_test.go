//go:build live

package livevalidation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CaliLuke/earwig/internal/normalizer"
	"github.com/CaliLuke/earwig/internal/spool"
)

const liveSessionID = "0aaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

func liveBinary(t *testing.T) string {
	t.Helper()
	binary := os.Getenv("EARWIG_LIVE_BIN")
	if binary == "" {
		t.Fatal("EARWIG_LIVE_BIN is required")
	}
	return binary
}

func opikURL() string {
	if value := os.Getenv("EARWIG_OPIK_URL"); value != "" {
		return strings.TrimRight(value, "/")
	}
	return "http://127.0.0.1:5173"
}

func opikAvailable() error {
	request, err := http.NewRequest(http.MethodGet, opikURL()+"/api/v1/private/projects?size=1", nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", response.StatusCode)
	}
	return nil
}

func requireOpik(t *testing.T) {
	t.Helper()
	if err := opikAvailable(); err != nil {
		t.Skipf("Opik unavailable at %s: %v", opikURL(), err)
	}
}

func uniqueProject(label string) string {
	return fmt.Sprintf("earwig-validation-%s-%d-%d", label, os.Getpid(), time.Now().UnixNano())
}

func opikRequest(t *testing.T, method, endpoint string, body any) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, opikURL()+endpoint, reader)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := (&http.Client{Timeout: 10 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	decoded := map[string]any{}
	if len(bytes.TrimSpace(responseBody)) > 0 {
		_ = json.Unmarshal(responseBody, &decoded)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		t.Fatalf("Opik %s %s: HTTP %d: %s", method, endpoint, response.StatusCode, responseBody)
	}
	return response.StatusCode, decoded
}

func getTrace(t *testing.T, id string) map[string]any {
	t.Helper()
	_, trace := opikRequest(t, http.MethodGet, "/api/v1/private/traces/"+url.PathEscape(id), nil)
	return trace
}

func projectID(t *testing.T, project string) string {
	t.Helper()
	_, result := opikRequest(t, http.MethodGet, "/api/v1/private/projects?name="+url.QueryEscape(project)+"&size=100", nil)
	for _, raw := range asArray(result["content"]) {
		candidate, _ := raw.(map[string]any)
		if candidate["name"] == project {
			id, _ := candidate["id"].(string)
			if id != "" {
				return id
			}
		}
	}
	t.Fatalf("Opik project %q was not found", project)
	return ""
}

func cleanupOpik(t *testing.T, project string, traceIDs ...string) {
	t.Helper()
	for _, id := range traceIDs {
		request, _ := http.NewRequest(http.MethodDelete, opikURL()+"/api/v1/private/traces/"+url.PathEscape(id), nil)
		response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
		if err == nil {
			response.Body.Close()
		}
	}
	request, _ := http.NewRequest(http.MethodGet, opikURL()+"/api/v1/private/projects?name="+url.QueryEscape(project)+"&size=100", nil)
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		t.Logf("cleanup project lookup %s: %v", project, err)
		return
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	var result struct {
		Content []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"content"`
	}
	if json.Unmarshal(body, &result) != nil {
		return
	}
	for _, candidate := range result.Content {
		if candidate.Name != project {
			continue
		}
		deleteRequest, _ := http.NewRequest(http.MethodDelete, opikURL()+"/api/v1/private/projects/"+url.PathEscape(candidate.ID), nil)
		deleteResponse, deleteErr := (&http.Client{Timeout: 5 * time.Second}).Do(deleteRequest)
		if deleteErr == nil {
			deleteResponse.Body.Close()
		}
	}
}

func testTranscript(sessionID, turnID, answer string, startedMS int64) normalizer.Transcript {
	return normalizer.Transcript{
		SchemaVersion: 1,
		Source:        "claude-code-agent-sdk",
		Capture:       normalizer.Capture{Fidelity: "full", Redactions: normalizer.Redactions{CommandSecretPatterns: true, BinaryPayloads: "omitted"}},
		Session: map[string]any{
			"id":            sessionID,
			"cwd":           "/tmp/earwig-live-validation",
			"created_at":    startedMS,
			"last_modified": startedMS + 1000,
			"compactions":   []any{},
		},
		Turns: []normalizer.Turn{{
			ID:                    turnID,
			Status:                "completed",
			StartedAt:             startedMS,
			CompletedAt:           startedMS + 1000,
			DurationMS:            int64(1000),
			UserMessages:          []map[string]any{{"id": "user", "content": []any{map[string]any{"type": "text", "text": "question"}}}},
			AssistantMessages:     []map[string]any{{"id": "assistant", "kind": "text", "text": answer}},
			FinalAnswer:           map[string]any{"id": "assistant", "kind": "text", "text": answer},
			Trajectory:            []map[string]any{},
			FollowingUserMessages: []map[string]any{},
		}},
	}
}

func traceIDFor(transcript normalizer.Transcript) string {
	turn := transcript.Turns[0]
	return normalizer.TraceID("claude-code", transcript.Session["id"].(string), turn, transcript.Session["created_at"].(int64))
}

func writeConfig(t *testing.T, directory, contents string) string {
	t.Helper()
	path := filepath.Join(directory, "config.toml")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func binaryCommand(t *testing.T, configPath string, args ...string) *exec.Cmd {
	t.Helper()
	command := exec.Command(liveBinary(t), args...)
	command.Env = append(os.Environ(), "EARWIG_CONFIG="+configPath)
	return command
}

func runBinary(t *testing.T, configPath string, args ...string) (string, int) {
	t.Helper()
	command := binaryCommand(t, configPath, args...)
	output, err := command.CombinedOutput()
	if err == nil {
		return string(output), 0
	}
	var exitError *exec.ExitError
	if !strings.Contains(fmt.Sprintf("%T", err), "ExitError") {
		t.Fatalf("run earwig %v: %v: %s", args, err, output)
	}
	if !asExitError(err, &exitError) {
		t.Fatalf("run earwig %v: %v: %s", args, err, output)
	}
	return string(output), exitError.ExitCode()
}

func asExitError(err error, target **exec.ExitError) bool {
	value, ok := err.(*exec.ExitError)
	if ok {
		*target = value
	}
	return ok
}

func closedLoopbackURL(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	return "http://" + address
}

func waitFor(t *testing.T, timeout time.Duration, description string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func writeStubHelper(t *testing.T, directory, workspace string, sessionIDs []string) string {
	t.Helper()
	list := make([]map[string]any, 0, len(sessionIDs))
	for index, id := range sessionIDs {
		list = append(list, map[string]any{"id": id, "cwd": workspace, "last_modified": 1000 + index})
	}
	listJSON, _ := json.Marshal(list)
	marker := filepath.Join(directory, "helper-active")
	slow := filepath.Join(directory, "helper-slow")
	body := fmt.Sprintf(`#!/bin/sh
set -eu
if [ -f %s ]; then
  : > %s
  sleep 2
fi
if [ "$1" = list ]; then
  printf '%%s\n' %s
  exit 0
fi
session_id=$2
printf '{"info":{"sessionId":"%%s","cwd":%s,"createdAt":1,"lastModified":2000},"messages":[{"type":"user","uuid":"11111111-1111-4111-8111-111111111111","timestamp":"2026-07-16T10:00:00Z","message":{"content":"question"}},{"type":"assistant","uuid":"22222222-2222-4222-8222-222222222222","timestamp":"2026-07-16T10:00:01Z","message":{"id":"answer","model":"fixture","stop_reason":"end_turn","content":[{"type":"text","text":"answer"}]}}]}\n' "$session_id"
`, strconv.Quote(slow), strconv.Quote(marker), strconv.Quote(string(listJSON)), strconv.Quote(workspace))
	path := filepath.Join(directory, "claude-reader")
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func openSpool(t *testing.T, path string) *spool.Spool {
	t.Helper()
	s, err := spool.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
