package spool

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/CaliLuke/earwig/internal/normalizer"
)

type SessionState struct {
	LastSweptMS     int64
	CompactionCount int
	GapWarned       bool
}

func hash(b []byte) string { x := sha256.Sum256(b); return hex.EncodeToString(x[:]) }

func (s *Spool) UpsertTranscript(t normalizer.Transcript, now time.Time) (int, error) {
	n := 0
	sid, _ := t.Session["id"].(string)
	fallback := normalizerTimestamp(t.Session["created_at"])
	for _, turn := range t.Turns {
		if turn.Status != "completed" && turn.Status != "failed" {
			continue
		}
		p := TurnPayload{
			SchemaVersion: t.SchemaVersion,
			Source:        t.Source,
			Capture:       t.Capture,
			Session:       sessionHeader(t.Session),
			Turn:          turn,
		}
		b, e := json.Marshal(p)
		if e != nil {
			return n, e
		}
		id := traceID(t.Source, sid, turn, fallback)
		h := hash(b)
		r, e := s.db.Exec(`INSERT INTO turns(trace_uuid,provider,session_id,turn_id,turn_status,started_at_ms,completed_at_ms,payload_json,content_hash,captured_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(trace_uuid) DO UPDATE SET payload_json=excluded.payload_json,content_hash=excluded.content_hash,captured_at_ms=excluded.captured_at_ms,turn_status=excluded.turn_status WHERE turns.content_hash<>excluded.content_hash`, id, t.Source, sid, turn.ID, turn.Status, normalizerTimestamp(turn.StartedAt), normalizerTimestamp(turn.CompletedAt), string(b), h, now.UnixMilli())
		if e != nil {
			return n, e
		}
		x, _ := r.RowsAffected()
		n += int(x)
	}
	_, e := s.db.Exec(`INSERT INTO sessions(provider,session_id,cwd,summary,last_modified_ms,last_swept_ms,compaction_count) VALUES(?,?,?,?,?,?,?) ON CONFLICT(provider,session_id) DO UPDATE SET cwd=excluded.cwd,summary=excluded.summary,last_modified_ms=excluded.last_modified_ms,last_swept_ms=excluded.last_swept_ms,compaction_count=excluded.compaction_count`, t.Source, sid, t.Session["cwd"], sessionSummary(t.Session), sessionLastModified(t), now.UnixMilli(), lenAny(t.Session["compactions"]))
	return n, e
}

// StoreRow persists a fully formed durable row. It is used at import and
// validation boundaries where the canonical trace identity is already known.
func (s *Spool) StoreRow(row Row) error {
	_, err := s.db.Exec(`INSERT INTO turns(trace_uuid,provider,session_id,turn_id,turn_status,started_at_ms,completed_at_ms,payload_json,content_hash,captured_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(trace_uuid) DO UPDATE SET provider=excluded.provider,session_id=excluded.session_id,turn_id=excluded.turn_id,turn_status=excluded.turn_status,started_at_ms=excluded.started_at_ms,completed_at_ms=excluded.completed_at_ms,payload_json=excluded.payload_json,content_hash=excluded.content_hash,captured_at_ms=excluded.captured_at_ms`, row.TraceUUID, row.Provider, row.SessionID, row.TurnID, row.Status, row.StartedMS, row.CompletedMS, row.Payload, row.Hash, row.CapturedMS)
	return err
}

func (s *Spool) Turn(traceID string) (Row, bool, error) {
	var row Row
	err := s.db.QueryRow(`SELECT trace_uuid,provider,session_id,turn_id,turn_status,started_at_ms,completed_at_ms,payload_json,content_hash,captured_at_ms FROM turns WHERE trace_uuid=?`, traceID).Scan(&row.TraceUUID, &row.Provider, &row.SessionID, &row.TurnID, &row.Status, &row.StartedMS, &row.CompletedMS, &row.Payload, &row.Hash, &row.CapturedMS)
	if errors.Is(err, sql.ErrNoRows) {
		return Row{}, false, nil
	}
	return row, err == nil, err
}

func traceID(source, sessionID string, turn normalizer.Turn, fallback int64) string {
	if source == "codex-app-server" {
		if native, ok := nativeCodexTraceID(turn.ID); ok {
			return native
		}
	}
	provider := map[string]string{"claude-code-agent-sdk": "claude-code", "codex-app-server": "codex"}[source]
	return normalizer.TraceID(provider, sessionID, turn, fallback)
}

func nativeCodexTraceID(id string) (string, bool) {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.Version() != 7 || parsed.Variant() != uuid.RFC4122 {
		return "", false
	}
	return parsed.String(), true
}

func sessionHeader(session map[string]any) map[string]any {
	header := map[string]any{}
	for _, key := range []string{"id", "session_id", "cwd", "created_at", "forked_from_id", "parent_thread_id"} {
		if value, ok := session[key]; ok {
			header[key] = value
		}
	}
	return header
}

func sessionSummary(session map[string]any) any {
	if v, ok := session["summary"]; ok {
		return v
	}
	return session["name"]
}

func sessionLastModified(t normalizer.Transcript) int64 {
	if t.Source == "codex-app-server" {
		return normalizerTimestamp(t.Session["updated_at"])
	}
	return normalizerTimestamp(t.Session["last_modified"])
}

// SessionNeedsSweep is the read planner. Unknown timestamps are read
// conservatively; known sessions are read only after their provider-reported
// mtime advances.
func (s *Spool) SessionNeedsSweep(provider, sessionID string, lastModified any) (bool, error) {
	candidate := normalizerTimestamp(lastModified)
	if candidate <= 0 {
		return true, nil
	}
	var stored int64
	err := s.db.QueryRow(`SELECT last_modified_ms FROM sessions WHERE provider=? AND session_id=?`, provider, sessionID).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return candidate > stored, nil
}

func (s *Spool) Session(provider, sessionID string) (SessionState, bool, error) {
	var state SessionState
	var gapWarned int
	err := s.db.QueryRow(`SELECT last_swept_ms,compaction_count,gap_warned FROM sessions WHERE provider=? AND session_id=?`, provider, sessionID).Scan(&state.LastSweptMS, &state.CompactionCount, &gapWarned)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionState{}, false, nil
	}
	state.GapWarned = gapWarned != 0
	return state, err == nil, err
}

func (s *Spool) MarkGapWarned(provider, sessionID string) error {
	_, err := s.db.Exec(`UPDATE sessions SET gap_warned=1 WHERE provider=? AND session_id=?`, provider, sessionID)
	return err
}

func normalizerTimestamp(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(x)
	case int64:
		return x
	case string:
		z, e := time.Parse(time.RFC3339Nano, x)
		if e == nil {
			return z.UnixMilli()
		}
	}
	return 0
}

func lenAny(v any) int {
	if a, ok := v.([]any); ok {
		return len(a)
	}
	return 0
}
