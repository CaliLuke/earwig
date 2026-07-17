package spool

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/CaliLuke/earwig/internal/normalizer"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"time"
)

type Spool struct {
	DB   *sql.DB
	Path string
}
type Row struct {
	TraceUUID, Provider, SessionID, TurnID, Status, Payload, Hash string
	StartedMS, CompletedMS, CapturedMS                            int64
}

// TurnPayload is the durable/exported unit. It deliberately excludes the
// transcript's turns array and mutable session fields, so activity later in a
// session cannot rewrite every turn already captured.
type TurnPayload struct {
	SchemaVersion int                `json:"schema_version"`
	Source        string             `json:"source"`
	Capture       normalizer.Capture `json:"capture"`
	Session       map[string]any     `json:"session"`
	Turn          normalizer.Turn    `json:"turn"`
}

func DefaultPath() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".local", "share", "earwig", "spool.sqlite")
}
func Open(path string) (*Spool, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	s := &Spool{db, path}
	_, e = db.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE IF NOT EXISTS sessions(provider TEXT, session_id TEXT, cwd TEXT, summary TEXT, last_modified_ms INTEGER, last_swept_ms INTEGER, compaction_count INTEGER DEFAULT 0, gap_warned INTEGER DEFAULT 0, PRIMARY KEY(provider,session_id)); CREATE TABLE IF NOT EXISTS turns(trace_uuid TEXT PRIMARY KEY, provider TEXT, session_id TEXT, turn_id TEXT, turn_status TEXT, started_at_ms INTEGER, completed_at_ms INTEGER, payload_json TEXT, content_hash TEXT, captured_at_ms INTEGER); CREATE TABLE IF NOT EXISTS exports(trace_uuid TEXT, exporter TEXT, exported_at_ms INTEGER, content_hash TEXT, PRIMARY KEY(trace_uuid,exporter)); CREATE TABLE IF NOT EXISTS health(key TEXT PRIMARY KEY,value_json TEXT);`)
	if e != nil {
		db.Close()
		return nil, e
	}
	if e = s.migrateLegacyPayloads(); e != nil {
		db.Close()
		return nil, e
	}
	if e = os.Chmod(path, 0600); e != nil {
		db.Close()
		return nil, e
	}
	return s, nil
}

// migrateLegacyPayloads rewrites rows created before per-turn envelopes were
// introduced. It operates in small batches because the legacy representation
// may contain a multi-megabyte transcript in every row.
func (s *Spool) migrateLegacyPayloads() error {
	type migration struct {
		id, payload, hash string
	}
	var format string
	if err := s.DB.QueryRow(`SELECT value_json FROM health WHERE key='payload_format'`).Scan(&format); err == nil && format == `"turn-v1"` {
		return nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for {
		rows, err := s.DB.Query(`SELECT trace_uuid,payload_json FROM turns WHERE payload_json LIKE '{"transcript":%' LIMIT 50`)
		if err != nil {
			return err
		}
		batch := []migration{}
		for rows.Next() {
			var id, legacyJSON string
			if err = rows.Scan(&id, &legacyJSON); err != nil {
				rows.Close()
				return err
			}
			var legacy struct {
				Transcript normalizer.Transcript `json:"transcript"`
				Turn       normalizer.Turn       `json:"turn"`
			}
			if err = json.Unmarshal([]byte(legacyJSON), &legacy); err != nil {
				rows.Close()
				return err
			}
			payload := TurnPayload{SchemaVersion: legacy.Transcript.SchemaVersion, Source: legacy.Transcript.Source, Capture: legacy.Transcript.Capture, Session: sessionHeader(legacy.Transcript.Session), Turn: legacy.Turn}
			b, marshalErr := json.Marshal(payload)
			if marshalErr != nil {
				rows.Close()
				return marshalErr
			}
			batch = append(batch, migration{id: id, payload: string(b), hash: hash(b)})
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(batch) == 0 {
			return s.SetHealth("payload_format", "turn-v1")
		}
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		for _, item := range batch {
			if _, err = tx.Exec(`UPDATE turns SET payload_json=?,content_hash=? WHERE trace_uuid=?`, item.payload, item.hash, item.id); err != nil {
				tx.Rollback()
				return err
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
}
func (s *Spool) Close() error { return s.DB.Close() }
func hash(b []byte) string    { x := sha256.Sum256(b); return hex.EncodeToString(x[:]) }
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
		id := normalizer.TraceID(map[string]string{"claude-code-agent-sdk": "claude-code", "codex-app-server": "codex"}[t.Source], sid, turn, fallback)
		h := hash(b)
		r, e := s.DB.Exec(`INSERT INTO turns(trace_uuid,provider,session_id,turn_id,turn_status,started_at_ms,completed_at_ms,payload_json,content_hash,captured_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(trace_uuid) DO UPDATE SET payload_json=excluded.payload_json,content_hash=excluded.content_hash,captured_at_ms=excluded.captured_at_ms,turn_status=excluded.turn_status WHERE turns.content_hash<>excluded.content_hash`, id, t.Source, sid, turn.ID, turn.Status, normalizerTimestamp(turn.StartedAt), normalizerTimestamp(turn.CompletedAt), string(b), h, now.UnixMilli())
		if e != nil {
			return n, e
		}
		x, _ := r.RowsAffected()
		n += int(x)
	}
	_, e := s.DB.Exec(`INSERT INTO sessions(provider,session_id,cwd,summary,last_modified_ms,last_swept_ms,compaction_count) VALUES(?,?,?,?,?,?,?) ON CONFLICT(provider,session_id) DO UPDATE SET cwd=excluded.cwd,summary=excluded.summary,last_modified_ms=excluded.last_modified_ms,last_swept_ms=excluded.last_swept_ms,compaction_count=excluded.compaction_count`, t.Source, sid, t.Session["cwd"], sessionSummary(t.Session), sessionLastModified(t), now.UnixMilli(), lenAny(t.Session["compactions"]))
	return n, e
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
	err := s.DB.QueryRow(`SELECT last_modified_ms FROM sessions WHERE provider=? AND session_id=?`, provider, sessionID).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return candidate > stored, nil
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
func (s *Spool) Pending(exporter string, limit int) ([]Row, error) {
	rows, e := s.DB.Query(`SELECT t.trace_uuid,t.provider,t.session_id,t.turn_id,t.turn_status,t.started_at_ms,t.completed_at_ms,t.payload_json,t.content_hash,t.captured_at_ms FROM turns t LEFT JOIN exports e ON e.trace_uuid=t.trace_uuid AND e.exporter=? WHERE e.trace_uuid IS NULL OR e.content_hash<>t.content_hash ORDER BY t.started_at_ms LIMIT ?`, exporter, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Row{}
	for rows.Next() {
		var r Row
		if e = rows.Scan(&r.TraceUUID, &r.Provider, &r.SessionID, &r.TurnID, &r.Status, &r.StartedMS, &r.CompletedMS, &r.Payload, &r.Hash, &r.CapturedMS); e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Spool) MarkExported(exporter string, rows []Row, now time.Time) error {
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	for _, r := range rows {
		if _, e = tx.Exec(`INSERT INTO exports(trace_uuid,exporter,exported_at_ms,content_hash) VALUES(?,?,?,?) ON CONFLICT(trace_uuid,exporter) DO UPDATE SET exported_at_ms=excluded.exported_at_ms,content_hash=excluded.content_hash`, r.TraceUUID, exporter, now.UnixMilli(), r.Hash); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Spool) SetHealth(k string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = s.DB.Exec(`INSERT INTO health(key,value_json)VALUES(?,?) ON CONFLICT(key)DO UPDATE SET value_json=excluded.value_json`, k, string(b))
	return e
}
func (s *Spool) GetHealth(k string) (string, error) {
	var v string
	e := s.DB.QueryRow(`SELECT value_json FROM health WHERE key=?`, k).Scan(&v)
	return v, e
}
func (s *Spool) Stats() (int, int, error) {
	var total, recent int
	e := s.DB.QueryRow(`SELECT COUNT(*),SUM(CASE WHEN captured_at_ms>? THEN 1 ELSE 0 END) FROM turns`, time.Now().Add(-24*time.Hour).UnixMilli()).Scan(&total, &recent)
	return total, recent, e
}
func (s *Spool) Prune(before time.Time) (int64, error) {
	r, e := s.DB.Exec(`DELETE FROM turns WHERE captured_at_ms<?`, before.UnixMilli())
	if e != nil {
		return 0, e
	}
	return r.RowsAffected()
}
