//go:build live

package livevalidation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/CaliLuke/earwig/internal/config"
	"github.com/CaliLuke/earwig/internal/daemon"
	"github.com/CaliLuke/earwig/internal/exporter"
	"github.com/CaliLuke/earwig/internal/normalizer"
	"github.com/CaliLuke/earwig/internal/spool"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestV4FreshExportAndUnchangedResweep(t *testing.T) {
	requireOpik(t)
	project := uniqueProject("fresh")
	transcript := testTranscript(liveSessionID, "fresh-turn", "fresh answer", time.Now().UnixMilli())
	traceID := traceIDFor(transcript)
	defer cleanupOpik(t, project, traceID)

	s := openSpool(t, filepath.Join(t.TempDir(), "spool.sqlite"))
	if _, err := s.UpsertTranscript(transcript, time.Now()); err != nil {
		t.Fatal(err)
	}
	sweeper := &daemon.Sweeper{Config: config.Config{OpikURL: opikURL(), OpikProject: project}, Spool: s}
	if err := sweeper.Sweep(context.Background(), daemon.SweepOptions{}); err != nil {
		t.Fatal(err)
	}
	trace := getTrace(t, traceID)
	if trace["thread_id"] != liveSessionID || trace["project_id"] != projectID(t, project) {
		t.Fatalf("trace identity mapping: %#v", trace)
	}
	input, _ := trace["input"].(map[string]any)
	output, _ := trace["output"].(map[string]any)
	if len(asArray(input["user_messages"])) != 1 || len(asArray(output["assistant_messages"])) != 1 || output["final_answer"] == nil {
		t.Fatalf("trace review fields missing: input=%#v output=%#v", input, output)
	}
	tags := stringSet(asArray(trace["tags"]))
	for _, expected := range []string{"auto-checkpoint", "inbox", "claude-code-agent-sdk", "completed"} {
		if !tags[expected] {
			t.Fatalf("trace tags missing %q: %#v", expected, tags)
		}
	}
	var firstExportedAt int64
	if err := s.DB.QueryRow(`SELECT exported_at_ms FROM exports WHERE trace_uuid=? AND exporter='opik'`, traceID).Scan(&firstExportedAt); err != nil {
		t.Fatal(err)
	}
	if err := sweeper.Sweep(context.Background(), daemon.SweepOptions{}); err != nil {
		t.Fatal(err)
	}
	var secondExportedAt int64
	if err := s.DB.QueryRow(`SELECT exported_at_ms FROM exports WHERE trace_uuid=? AND exporter='opik'`, traceID).Scan(&secondExportedAt); err != nil {
		t.Fatal(err)
	}
	if secondExportedAt != firstExportedAt {
		t.Fatalf("unchanged resweep wrote exporter checkpoint: %d -> %d", firstExportedAt, secondExportedAt)
	}
	t.Logf("fresh trace %s mapped review fields; unchanged resweep performed zero writes", traceID)
}

func TestV4UpdatePreservesCuration(t *testing.T) {
	requireOpik(t)
	project := uniqueProject("curation")
	started := time.Now().UnixMilli()
	transcript := testTranscript(liveSessionID, "curated-turn", "before", started)
	traceID := traceIDFor(transcript)
	defer cleanupOpik(t, project, traceID)
	s := openSpool(t, filepath.Join(t.TempDir(), "spool.sqlite"))
	if _, err := s.UpsertTranscript(transcript, time.Now()); err != nil {
		t.Fatal(err)
	}
	sweeper := &daemon.Sweeper{Config: config.Config{OpikURL: opikURL(), OpikProject: project}, Spool: s}
	_ = sweeper.Sweep(context.Background(), daemon.SweepOptions{})
	opikRequest(t, http.MethodPatch, "/api/v1/private/traces/"+traceID, map[string]any{
		"name": "reviewer-curated-name", "tags": []string{"promoted", "needs-human-review"}, "project_name": project,
	})
	transcript = testTranscript(liveSessionID, "curated-turn", "after", started)
	if _, err := s.UpsertTranscript(transcript, time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := sweeper.Sweep(context.Background(), daemon.SweepOptions{}); err != nil {
		t.Fatal(err)
	}
	trace := getTrace(t, traceID)
	if trace["name"] != "reviewer-curated-name" {
		t.Fatalf("PATCH clobbered name: %#v", trace["name"])
	}
	tags := stringSet(asArray(trace["tags"]))
	if !tags["promoted"] || !tags["needs-human-review"] {
		t.Fatalf("PATCH clobbered curation tags: %#v", tags)
	}
	output := trace["output"].(map[string]any)
	final := output["final_answer"].(map[string]any)
	if final["text"] != "after" {
		t.Fatalf("PATCH did not update content: %#v", output)
	}
	t.Logf("trace %s preserved reviewer name and promoted tag across POST→409→PATCH", traceID)
}

func TestV4PoisonedRowIsolation(t *testing.T) {
	requireOpik(t)
	sourceProject := uniqueProject("poison-source")
	targetProject := uniqueProject("poison-target")
	poison := validationRow("poison", time.Now().UnixMilli())
	good := validationRow("good", time.Now().UnixMilli()+2000)
	defer cleanupOpik(t, sourceProject, poison.TraceUUID)
	defer cleanupOpik(t, targetProject, good.TraceUUID)
	if err := (exporter.Opik{URL: opikURL(), ProjectName: sourceProject}).Export([]spool.Row{poison}); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	spoolPath := filepath.Join(directory, "spool.sqlite")
	s := openSpool(t, spoolPath)
	insertRow(t, s, poison)
	insertRow(t, s, good)
	sweeper := &daemon.Sweeper{Config: config.Config{OpikURL: opikURL(), OpikProject: targetProject}, Spool: s}
	if err := sweeper.Sweep(context.Background(), daemon.SweepOptions{}); err != nil {
		t.Fatal(err)
	}
	var goodExports, poisonExports int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM exports WHERE trace_uuid=? AND exporter='opik'`, good.TraceUUID).Scan(&goodExports)
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM exports WHERE trace_uuid=? AND exporter='opik'`, poison.TraceUUID).Scan(&poisonExports)
	if goodExports != 1 || poisonExports != 0 {
		t.Fatalf("poison isolation exports good=%d poison=%d", goodExports, poisonExports)
	}
	var failure string
	if err := s.DB.QueryRow(`SELECT error FROM export_failures WHERE trace_uuid=? AND exporter='opik'`, poison.TraceUUID).Scan(&failure); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(failure, "different project") || !strings.Contains(failure, "opik_project") {
		t.Fatalf("project mismatch is not actionable: %q", failure)
	}
	configPath := writeConfig(t, directory, fmt.Sprintf("claude = false\ncodex = false\nspool_path = %q\njsondir = \"\"\nopik_url = %q\nopik_project = %q\n", spoolPath, opikURL(), targetProject))
	status, exitCode := runBinary(t, configPath, "status")
	if exitCode != 1 || !strings.Contains(status, "behind") || !strings.Contains(status, "pending") {
		t.Fatalf("status did not expose poisoned row (exit %d): %s", exitCode, status)
	}
	t.Logf("poison %s quarantined while %s exported; status remained visibly behind", poison.TraceUUID, good.TraceUUID)
}

func TestV4ExporterDownRecovery(t *testing.T) {
	requireOpik(t)
	project := uniqueProject("recovery")
	directory := t.TempDir()
	workspace := filepath.Join(directory, "workspace")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	helper := writeStubHelper(t, directory, workspace, []string{liveSessionID})
	spoolPath := filepath.Join(directory, "spool.sqlite")
	downURL := closedLoopbackURL(t)
	configContents := func(endpoint string) string {
		return fmt.Sprintf("workspace_roots = [%q]\nclaude = true\ncodex = false\nspool_path = %q\njsondir = \"\"\nopik_url = %q\nopik_project = %q\nclaude_helper = %q\n", workspace, spoolPath, endpoint, project, helper)
	}
	configPath := writeConfig(t, directory, configContents(downURL))
	output, exitCode := runBinary(t, configPath, "sweep", "--provider", "claude")
	if exitCode != 0 {
		t.Fatalf("capture with exporter down exited %d: %s", exitCode, output)
	}
	s := openSpool(t, spoolPath)
	var turns int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM turns`).Scan(&turns); err != nil || turns != 1 {
		t.Fatalf("down exporter lost capture: turns=%d err=%v", turns, err)
	}
	status, statusExit := runBinary(t, configPath, "status")
	if statusExit != 1 || !strings.Contains(status, "behind") {
		t.Fatalf("down exporter status (exit %d): %s", statusExit, status)
	}
	if err := os.WriteFile(configPath, []byte(configContents(opikURL())), 0600); err != nil {
		t.Fatal(err)
	}
	output, exitCode = runBinary(t, configPath, "sweep", "--provider", "claude")
	if exitCode != 0 {
		t.Fatalf("recovery sweep exited %d: %s", exitCode, output)
	}
	var traceID string
	if err := s.DB.QueryRow(`SELECT trace_uuid FROM turns`).Scan(&traceID); err != nil {
		t.Fatal(err)
	}
	defer cleanupOpik(t, project, traceID)
	_ = getTrace(t, traceID)
	status, statusExit = runBinary(t, configPath, "status")
	if statusExit != 0 || strings.Contains(status, "pending") {
		t.Fatalf("recovered exporter status (exit %d): %s", statusExit, status)
	}
	var exportRows int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM exports WHERE exporter='opik'`).Scan(&exportRows)
	if exportRows != 1 {
		t.Fatalf("recovery produced duplicate checkpoints: %d", exportRows)
	}
	t.Logf("down exporter captured one turn, status reported behind, and recovery drained exactly once")
}

func TestV4PruneProtectionAndFailClosed(t *testing.T) {
	requireOpik(t)
	project := uniqueProject("prune")
	keep := validationRow("keep", time.Now().Add(-3*time.Hour).UnixMilli())
	remove := validationRow("remove", time.Now().Add(-3*time.Hour).UnixMilli()+2000)
	defer cleanupOpik(t, project, keep.TraceUUID, remove.TraceUUID)
	directory := t.TempDir()
	spoolPath := filepath.Join(directory, "spool.sqlite")
	s := openSpool(t, spoolPath)
	insertRow(t, s, keep)
	insertRow(t, s, remove)
	if err := exporter.Drain(s, exporter.Opik{URL: opikURL(), ProjectName: project}); err != nil {
		t.Fatal(err)
	}
	opikRequest(t, http.MethodPatch, "/api/v1/private/traces/"+keep.TraceUUID, map[string]any{"tags": []string{"promoted"}, "project_name": project})
	configPath := writeConfig(t, directory, fmt.Sprintf("claude = false\ncodex = false\nspool_path = %q\njsondir = \"\"\nopik_url = %q\nopik_project = %q\n", spoolPath, opikURL(), project))
	output, exitCode := runBinary(t, configPath, "prune", "--older-than", "1h")
	if exitCode != 0 {
		t.Fatalf("prune exited %d: %s", exitCode, output)
	}
	var keepCount, removeCount int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM turns WHERE trace_uuid=?`, keep.TraceUUID).Scan(&keepCount)
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM turns WHERE trace_uuid=?`, remove.TraceUUID).Scan(&removeCount)
	if keepCount != 1 || removeCount != 0 {
		t.Fatalf("prune protection keep=%d remove=%d", keepCount, removeCount)
	}
	failClosed := validationRow("fail-closed", time.Now().Add(-3*time.Hour).UnixMilli()+4000)
	insertRow(t, s, failClosed)
	if err := s.MarkExported("opik", []spool.Row{failClosed}, time.Now().Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	before := countTurns(t, s)
	downConfig := fmt.Sprintf("claude = false\ncodex = false\nspool_path = %q\njsondir = \"\"\nopik_url = %q\nopik_project = %q\n", spoolPath, closedLoopbackURL(t), project)
	if err := os.WriteFile(configPath, []byte(downConfig), 0600); err != nil {
		t.Fatal(err)
	}
	output, exitCode = runBinary(t, configPath, "prune", "--older-than", "1h")
	if exitCode == 0 || countTurns(t, s) != before {
		t.Fatalf("unreachable Opik did not fail closed (exit %d before %d after %d): %s", exitCode, before, countTurns(t, s), output)
	}
	t.Logf("promoted trace survived prune; unreachable Opik aborted with zero deletions")
}

func validationRow(label string, startedMS int64) spool.Row {
	transcript := testTranscript(liveSessionID+label, label, "answer-"+label, startedMS)
	// Session IDs in direct spool rows need not satisfy provider CLI validation.
	traceID := normalizer.DeterministicUUIDv7(startedMS, "claude-code", transcript.Session["id"].(string), label)
	payload, _ := json.Marshal(spool.TurnPayload{SchemaVersion: 1, Source: transcript.Source, Capture: transcript.Capture, Session: transcript.Session, Turn: transcript.Turns[0]})
	digest := sha256.Sum256(payload)
	return spool.Row{TraceUUID: traceID, Provider: transcript.Source, SessionID: transcript.Session["id"].(string), TurnID: label, Status: "completed", StartedMS: startedMS, CompletedMS: startedMS + 1000, Payload: string(payload), Hash: hex.EncodeToString(digest[:]), CapturedMS: startedMS}
}

func insertRow(t *testing.T, s *spool.Spool, row spool.Row) {
	t.Helper()
	_, err := s.DB.Exec(`INSERT INTO turns(trace_uuid,provider,session_id,turn_id,turn_status,started_at_ms,completed_at_ms,payload_json,content_hash,captured_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?)`, row.TraceUUID, row.Provider, row.SessionID, row.TurnID, row.Status, row.StartedMS, row.CompletedMS, row.Payload, row.Hash, row.CapturedMS)
	if err != nil {
		t.Fatal(err)
	}
}

func asArray(value any) []any {
	array, _ := value.([]any)
	return array
}

func stringSet(values []any) map[string]bool {
	result := map[string]bool{}
	for _, value := range values {
		if text, ok := value.(string); ok {
			result[text] = true
		}
	}
	return result
}

func countTurns(t *testing.T, s *spool.Spool) int {
	t.Helper()
	var count int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM turns`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
