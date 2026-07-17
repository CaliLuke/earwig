package spool

import (
	"encoding/json"
	"github.com/CaliLuke/earwig/internal/normalizer"
	"path/filepath"
	"testing"
	"time"
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
	if n, e := s.UpsertTranscript(x, time.Now()); e != nil || n != 1 {
		t.Fatalf("%d %v", n, e)
	}
	if n, e := s.UpsertTranscript(x, time.Now()); e != nil || n != 0 {
		t.Fatalf("%d %v", n, e)
	}
	x.Turns[0].Status = "failed"
	if n, e := s.UpsertTranscript(x, time.Now()); e != nil || n != 1 {
		t.Fatalf("%d %v", n, e)
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
	if n, err := s.UpsertTranscript(x, time.Now()); err != nil || n != 0 {
		t.Fatalf("mutable session metadata rewrote turn: %d, %v", n, err)
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
	if _, err = s.DB.Exec(`INSERT INTO turns(trace_uuid,provider,session_id,turn_id,turn_status,started_at_ms,completed_at_ms,payload_json,content_hash,captured_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?)`, "legacy", x.Source, x.Session["id"], x.Turns[0].ID, x.Turns[0].Status, 1, 2, string(legacy), "old-hash", 3); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`DELETE FROM health WHERE key='payload_format'`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
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
}
