package spool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CaliLuke/earwig/internal/normalizer"
)

func sample() normalizer.Transcript {
	return normalizer.Transcript{SchemaVersion: 1, Source: "claude-code-agent-sdk", Capture: normalizer.Capture{Fidelity: "full"}, Session: map[string]any{"id": "0aaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "cwd": "/tmp/project", "summary": "first", "created_at": int64(1), "last_modified": int64(100), "compactions": []any{}}, Turns: []normalizer.Turn{{ID: "u1", Status: "completed", StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:00:01Z", UserMessages: []map[string]any{}, AssistantMessages: []map[string]any{}, Trajectory: []map[string]any{}, FollowingUserMessages: []map[string]any{}}}}
}
func TestUpsertAndRehash(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "spool.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	x := sample()
	if n, upsertErr := s.UpsertTranscript(x, time.Now()); upsertErr != nil || n != 1 {
		t.Fatalf("%d %v", n, upsertErr)
	}
	if n, upsertErr := s.UpsertTranscript(x, time.Now()); upsertErr != nil || n != 0 {
		t.Fatalf("%d %v", n, upsertErr)
	}
	x.Turns[0].Status = "failed"
	if n, upsertErr := s.UpsertTranscript(x, time.Now()); upsertErr != nil || n != 1 {
		t.Fatalf("%d %v", n, upsertErr)
	}
	r, e := s.Pending("test", 10)
	if e != nil || len(r) != 1 {
		t.Fatal(e, len(r))
	}
	if e = s.MarkExported("test", r, time.Now()); e != nil {
		t.Fatal(e)
	}
	r, e = s.Pending("test", 10)
	if e != nil || len(r) != 0 {
		t.Fatal(e, len(r))
	}
}

func TestTurnAndExportCountInspection(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "spool.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = s.UpsertTranscript(sample(), time.Now()); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Pending("test", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("pending rows = %d, %v", len(rows), err)
	}
	stored, exists, err := s.Turn(rows[0].TraceUUID)
	if err != nil || !exists || stored.TraceUUID != rows[0].TraceUUID {
		t.Fatalf("stored turn = %#v, exists=%v, err=%v", stored, exists, err)
	}
	if err = s.MarkExported("test", rows, time.Now()); err != nil {
		t.Fatal(err)
	}
	count, err := s.ExportCount("test")
	if err != nil || count != 1 {
		t.Fatalf("export count = %d, %v", count, err)
	}
}

func TestPayloadIsPerTurnAndIgnoresMutableSessionFields(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "spool.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	x := sample()
	if n, err := s.UpsertTranscript(x, time.Now()); err != nil || n != 1 {
		t.Fatalf("%d, %v", n, err)
	}
	rows, err := s.Pending("test", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%d, %v", len(rows), err)
	}
	var payload map[string]any
	if err = json.Unmarshal([]byte(rows[0].Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["transcript"] != nil || payload["turns"] != nil || payload["turn"] == nil {
		t.Fatalf("payload embeds the wrong unit: %#v", payload)
	}
	session := payload["session"].(map[string]any)
	for _, mutable := range []string{"summary", "last_modified", "compactions"} {
		if _, ok := session[mutable]; ok {
			t.Fatalf("mutable session field %q is in turn payload", mutable)
		}
	}
	if err = s.MarkExported("test", rows, time.Now()); err != nil {
		t.Fatal(err)
	}

	x.Session["summary"] = "later"
	x.Session["last_modified"] = int64(200)
	x.Session["compactions"] = []any{map[string]any{"timestamp": "2026-01-01T00:00:02Z"}}
	if n, upsertErr := s.UpsertTranscript(x, time.Now()); upsertErr != nil || n != 0 {
		t.Fatalf("mutable session metadata rewrote turn: %d, %v", n, upsertErr)
	}
	rows, err = s.Pending("test", 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("unchanged turn was queued again: %d, %v", len(rows), err)
	}
}

func TestSessionNeedsSweepUsesProviderMtime(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "spool.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	x := sample()
	sid := x.Session["id"].(string)
	if changed, err := s.SessionNeedsSweep(x.Source, sid, int64(100)); err != nil || !changed {
		t.Fatalf("new session: %v, %v", changed, err)
	}
	if _, err := s.UpsertTranscript(x, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []any{int64(99), int64(100), float64(100)} {
		if changed, err := s.SessionNeedsSweep(x.Source, sid, candidate); err != nil || changed {
			t.Fatalf("unchanged session %v: %v, %v", candidate, changed, err)
		}
	}
	if changed, err := s.SessionNeedsSweep(x.Source, sid, int64(101)); err != nil || !changed {
		t.Fatalf("advanced session: %v, %v", changed, err)
	}
	if changed, err := s.SessionNeedsSweep(x.Source, sid, nil); err != nil || !changed {
		t.Fatalf("unknown mtime must be conservative: %v, %v", changed, err)
	}
}

func TestOpenMigratesLegacyWholeTranscriptPayload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	x := sample()
	legacy, err := json.Marshal(struct {
		Transcript normalizer.Transcript `json:"transcript"`
		Turn       normalizer.Turn       `json:"turn"`
	}{x, x.Turns[0]})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO turns(trace_uuid,provider,session_id,turn_id,turn_status,started_at_ms,completed_at_ms,payload_json,content_hash,captured_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?)`, "legacy", x.Source, x.Session["id"], x.Turns[0].ID, x.Turns[0].Status, 1, 2, string(legacy), "old-hash", 3); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`DELETE FROM health WHERE key='payload_format'`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.Pending("test", 10)
	if err != nil || len(rows) != 1 {
		t.Fatalf("%d, %v", len(rows), err)
	}
	var payload map[string]any
	if err = json.Unmarshal([]byte(rows[0].Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["transcript"] != nil || payload["turn"] == nil || rows[0].Hash == "old-hash" {
		t.Fatalf("legacy payload was not migrated: %#v", payload)
	}
	migratedPayload, migratedHash := rows[0].Payload, rows[0].Hash
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows, err = s.Pending("test", 10)
	if err != nil || len(rows) != 1 || rows[0].Payload != migratedPayload || rows[0].Hash != migratedHash {
		t.Fatalf("second open changed legacy migration: %#v, %v", rows, err)
	}
}

func TestConcurrentSpoolsWaitForWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.sqlite")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	var timeout int
	if err = b.db.QueryRow(`PRAGMA busy_timeout`).Scan(&timeout); err != nil || timeout != 5000 {
		t.Fatalf("busy_timeout = %d, %v", timeout, err)
	}
	conn, err := a.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err = conn.ExecContext(context.Background(), `BEGIN IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- b.SetHealth("contended", true) }()
	time.Sleep(50 * time.Millisecond)
	select {
	case err = <-done:
		t.Fatalf("contending write returned before lock release: %v", err)
	default:
	}
	if _, err = conn.ExecContext(context.Background(), `ROLLBACK`); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatalf("contending write failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("contending write did not resume")
	}
}

func TestCodexUsesNativeUUIDv7TraceIdentity(t *testing.T) {
	const turnID = "01900000-0000-7000-8000-000000000123"
	turn := normalizer.Turn{ID: turnID, Status: "completed"}
	if got := traceID("codex-app-server", "thread", turn, 0); got != turnID {
		t.Fatalf("Codex trace ID = %q", got)
	}
	turn.ID = "synthetic-fixture-turn"
	if got := traceID("codex-app-server", "thread", turn, 0); got == turn.ID {
		t.Fatal("non-UUIDv7 Codex ID bypassed deterministic fallback")
	}
}

func TestCodexTraceIdentityReferenceVectors(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "generated", "trace-id-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name        string `json:"name"`
		Mode        string `json:"mode"`
		TimestampMS int64  `json:"timestamp_ms"`
		Provider    string `json:"provider"`
		SessionID   string `json:"session_id"`
		TurnID      string `json:"turn_id"`
		Expected    string `json:"expected"`
	}
	if err = json.Unmarshal(b, &vectors); err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, vector := range vectors {
		if vector.Provider != "codex" {
			continue
		}
		turn := normalizer.Turn{ID: vector.TurnID, StartedAt: vector.TimestampMS, Status: "completed"}
		if got := traceID("codex-app-server", vector.SessionID, turn, 0); got != vector.Expected {
			t.Errorf("%s: got %s, want %s", vector.Name, got, vector.Expected)
		}
		checked++
	}
	if checked < 2 {
		t.Fatalf("only %d Codex identity paths checked", checked)
	}
}

func TestOpenMigratesCodexTraceIDsAndQueuesNativeExport(t *testing.T) {
	const (
		oldID    = "01900000-0000-7000-8000-000000000999"
		nativeID = "01900000-0000-7000-8000-000000000123"
	)
	path := filepath.Join(t.TempDir(), "spool.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(TurnPayload{SchemaVersion: 1, Source: "codex-app-server", Session: map[string]any{"id": "thread"}, Turn: normalizer.Turn{ID: nativeID, Status: "completed"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO turns(trace_uuid,provider,session_id,turn_id,turn_status,started_at_ms,completed_at_ms,payload_json,content_hash,captured_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?)`, oldID, "codex-app-server", "thread", nativeID, "completed", 1, 2, string(payload), "hash", 3); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`INSERT INTO exports(trace_uuid,exporter,exported_at_ms,content_hash) VALUES(?,?,?,?)`, oldID, "opik", 4, "hash"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`DELETE FROM health WHERE key='codex_trace_identity'`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var oldTurns, nativeTurns, oldExports int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM turns WHERE trace_uuid=?`, oldID).Scan(&oldTurns)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM turns WHERE trace_uuid=?`, nativeID).Scan(&nativeTurns)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM exports WHERE trace_uuid=?`, oldID).Scan(&oldExports)
	if oldTurns != 0 || nativeTurns != 1 || oldExports != 0 {
		t.Fatalf("migration counts old=%d native=%d exports=%d", oldTurns, nativeTurns, oldExports)
	}
	pending, err := s.Pending("opik", 10)
	if err != nil || len(pending) != 1 || pending[0].TraceUUID != nativeID {
		t.Fatalf("native export not queued: %#v, %v", pending, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	pending, err = s.Pending("opik", 10)
	if err != nil || len(pending) != 1 || pending[0].TraceUUID != nativeID {
		t.Fatalf("second open changed Codex identity migration: %#v, %v", pending, err)
	}
}

func TestPruneRetainsProtectedTracesAndCountsWithoutPayloads(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "spool.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, id := range []string{"delete", "keep"} {
		if _, err = s.db.Exec(`INSERT INTO turns(trace_uuid,provider,session_id,turn_id,turn_status,started_at_ms,completed_at_ms,payload_json,content_hash,captured_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, "provider", "session", id, "completed", 1, 2, `{"turn":{}}`, "hash", 1); err != nil {
			t.Fatal(err)
		}
	}
	if count, countErr := s.PendingCount("jsondir"); countErr != nil || count != 2 {
		t.Fatalf("pending count = %d, %v", count, countErr)
	}
	pruned, err := s.Prune(time.Now(), map[string]bool{"keep": true})
	if err != nil || pruned != 1 {
		t.Fatalf("pruned = %d, %v", pruned, err)
	}
	var remaining string
	if err = s.db.QueryRow(`SELECT trace_uuid FROM turns`).Scan(&remaining); err != nil || remaining != "keep" {
		t.Fatalf("remaining = %q, %v", remaining, err)
	}
}

func TestPermanentExportFailureRetriesOnContentOrTargetChange(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "spool.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	row := Row{TraceUUID: "trace", Provider: "p", SessionID: "session", TurnID: "turn", Status: "completed", Payload: `{}`, Hash: "hash-1"}
	if _, err = s.db.Exec(`INSERT INTO turns VALUES(?,?,?,?,?,?,?,?,?,?)`, row.TraceUUID, row.Provider, row.SessionID, row.TurnID, row.Status, 1, 2, row.Payload, row.Hash, 1); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordExportFailure("opik", "target-a", row, fmt.Errorf("permanent"), true, time.Now()); err != nil {
		t.Fatal(err)
	}
	if pending, pendingErr := s.PendingFor("opik", "target-a", 10); pendingErr != nil || len(pending) != 0 {
		t.Fatalf("same failure was not quarantined: %#v, %v", pending, pendingErr)
	}
	if pending, pendingErr := s.PendingFor("opik", "target-b", 10); pendingErr != nil || len(pending) != 1 {
		t.Fatalf("target change did not retry: %#v, %v", pending, pendingErr)
	}
	if _, err = s.db.Exec(`UPDATE turns SET content_hash='hash-2' WHERE trace_uuid='trace'`); err != nil {
		t.Fatal(err)
	}
	if pending, pendingErr := s.PendingFor("opik", "target-a", 10); pendingErr != nil || len(pending) != 1 {
		t.Fatalf("content change did not retry: %#v, %v", pending, pendingErr)
	}
}
