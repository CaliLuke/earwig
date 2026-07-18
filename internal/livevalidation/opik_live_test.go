//go:build live

package livevalidation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CaliLuke/earwig/internal/config"
	"github.com/CaliLuke/earwig/internal/daemon"
	"github.com/CaliLuke/earwig/internal/exporter"
	"github.com/CaliLuke/earwig/internal/normalizer"
	"github.com/CaliLuke/earwig/internal/spool"
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
	firstExportedAt, exported, err := s.ExportedAt(traceID, "opik")
	if err != nil || !exported {
		t.Fatalf("first export checkpoint: exported=%v err=%v", exported, err)
	}
	if err := sweeper.Sweep(context.Background(), daemon.SweepOptions{}); err != nil {
		t.Fatal(err)
	}
	secondExportedAt, exported, err := s.ExportedAt(traceID, "opik")
	if err != nil || !exported {
		t.Fatalf("second export checkpoint: exported=%v err=%v", exported, err)
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
	_, goodExported, goodErr := s.ExportedAt(good.TraceUUID, "opik")
	_, poisonExported, poisonErr := s.ExportedAt(poison.TraceUUID, "opik")
	if goodErr != nil || poisonErr != nil || !goodExported || poisonExported {
		t.Fatalf("poison isolation exports good=%v poison=%v good err=%v poison err=%v", goodExported, poisonExported, goodErr, poisonErr)
	}
	failure, failed, failureErr := s.ExportFailure(poison.TraceUUID, "opik")
	if failureErr != nil || !failed {
		t.Fatalf("poison failure: found=%v err=%v", failed, failureErr)
	}
	if !strings.Contains(failure.Error, "different project") || !strings.Contains(failure.Error, "opik_project") {
		t.Fatalf("project mismatch is not actionable: %q", failure.Error)
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
	turns, _, err := s.Stats()
	if err != nil || turns != 1 {
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
	rows, err := s.Pending("inspection", 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("captured rows: %d, %v", len(rows), err)
	}
	traceID := rows[0].TraceUUID
	defer cleanupOpik(t, project, traceID)
	_ = getTrace(t, traceID)
	status, statusExit = runBinary(t, configPath, "status")
	if statusExit != 0 || strings.Contains(status, "pending") {
		t.Fatalf("recovered exporter status (exit %d): %s", statusExit, status)
	}
	exportRows, err := s.ExportCount("opik")
	if err != nil || exportRows != 1 {
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
	_, keepExists, keepErr := s.Turn(keep.TraceUUID)
	_, removeExists, removeErr := s.Turn(remove.TraceUUID)
	if keepErr != nil || removeErr != nil || !keepExists || removeExists {
		t.Fatalf("prune protection keep=%v remove=%v keep err=%v remove err=%v", keepExists, removeExists, keepErr, removeErr)
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
	if err := s.StoreRow(row); err != nil {
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
	count, _, err := s.Stats()
	if err != nil {
		t.Fatal(err)
	}
	return count
}
