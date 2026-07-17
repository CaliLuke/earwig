package spool

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/CaliLuke/earwig/internal/normalizer"
	"github.com/google/uuid"
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

type ExportFailure struct {
	TraceUUID, Error string
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
	// PRAGMAs are connection-local. Keep one connection per process so WAL and
	// the writer wait policy apply to every operation, including hook sweeps
	// racing the resident daemon.
	db.SetMaxOpenConns(1)
	s := &Spool{db, path}
	_, e = db.Exec(`PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL; CREATE TABLE IF NOT EXISTS sessions(provider TEXT, session_id TEXT, cwd TEXT, summary TEXT, last_modified_ms INTEGER, last_swept_ms INTEGER, compaction_count INTEGER DEFAULT 0, gap_warned INTEGER DEFAULT 0, PRIMARY KEY(provider,session_id)); CREATE TABLE IF NOT EXISTS turns(trace_uuid TEXT PRIMARY KEY, provider TEXT, session_id TEXT, turn_id TEXT, turn_status TEXT, started_at_ms INTEGER, completed_at_ms INTEGER, payload_json TEXT, content_hash TEXT, captured_at_ms INTEGER); CREATE TABLE IF NOT EXISTS exports(trace_uuid TEXT, exporter TEXT, exported_at_ms INTEGER, content_hash TEXT, PRIMARY KEY(trace_uuid,exporter)); CREATE TABLE IF NOT EXISTS export_failures(trace_uuid TEXT, exporter TEXT, target_key TEXT, failed_at_ms INTEGER, content_hash TEXT, error TEXT, permanent INTEGER, PRIMARY KEY(trace_uuid,exporter)); CREATE TABLE IF NOT EXISTS health(key TEXT PRIMARY KEY,value_json TEXT);`)
	if e != nil {
		db.Close()
		return nil, e
	}
	if e = s.migrateLegacyPayloads(); e != nil {
		db.Close()
		return nil, e
	}
	if e = s.migrateCodexTraceIDs(); e != nil {
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
		id := traceID(t.Source, sid, turn, fallback)
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

// migrateCodexTraceIDs converges rows captured by early Earwig versions with
// Auto-K's native Codex turn UUID policy. Claude IDs remain deterministic
// UUIDv7 values because its native message IDs are UUIDv4.
func (s *Spool) migrateCodexTraceIDs() error {
	var policy string
	if err := s.DB.QueryRow(`SELECT value_json FROM health WHERE key='codex_trace_identity'`).Scan(&policy); err == nil && policy == `"native-v1"` {
		return nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rows, err := s.DB.Query(`SELECT trace_uuid,payload_json FROM turns WHERE provider='codex-app-server'`)
	if err != nil {
		return err
	}
	type change struct{ oldID, newID string }
	changes := []change{}
	for rows.Next() {
		var oldID, payloadJSON string
		if err = rows.Scan(&oldID, &payloadJSON); err != nil {
			rows.Close()
			return err
		}
		var payload TurnPayload
		if err = json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
			rows.Close()
			return err
		}
		if native, ok := nativeCodexTraceID(payload.Turn.ID); ok && native != oldID {
			changes = append(changes, change{oldID: oldID, newID: native})
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range changes {
		// The old deterministic UUID may already exist remotely; do not carry its
		// export checkpoint to the native UUID. The renamed row must be exported
		// once under its compatibility identity.
		if _, err = tx.Exec(`DELETE FROM exports WHERE trace_uuid=?`, item.oldID); err != nil {
			return err
		}
		if _, err = tx.Exec(`DELETE FROM export_failures WHERE trace_uuid=?`, item.oldID); err != nil {
			return err
		}
		var exists int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM turns WHERE trace_uuid=?`, item.newID).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			if _, err = tx.Exec(`DELETE FROM turns WHERE trace_uuid=?`, item.oldID); err != nil {
				return err
			}
		} else if _, err = tx.Exec(`UPDATE turns SET trace_uuid=? WHERE trace_uuid=?`, item.newID, item.oldID); err != nil {
			return err
		}
	}
	b, _ := json.Marshal("native-v1")
	if _, err = tx.Exec(`INSERT INTO health(key,value_json)VALUES('codex_trace_identity',?) ON CONFLICT(key)DO UPDATE SET value_json=excluded.value_json`, string(b)); err != nil {
		return err
	}
	return tx.Commit()
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

// PendingFor excludes only permanent failures for the same payload and
// exporter target. A content change or target configuration change makes the
// row eligible automatically.
func (s *Spool) PendingFor(exporter, targetKey string, limit int) ([]Row, error) {
	rows, e := s.DB.Query(`SELECT t.trace_uuid,t.provider,t.session_id,t.turn_id,t.turn_status,t.started_at_ms,t.completed_at_ms,t.payload_json,t.content_hash,t.captured_at_ms FROM turns t LEFT JOIN exports e ON e.trace_uuid=t.trace_uuid AND e.exporter=? LEFT JOIN export_failures f ON f.trace_uuid=t.trace_uuid AND f.exporter=? WHERE (e.trace_uuid IS NULL OR e.content_hash<>t.content_hash) AND NOT (COALESCE(f.permanent,0)=1 AND f.content_hash=t.content_hash AND f.target_key=?) ORDER BY t.started_at_ms LIMIT ?`, exporter, exporter, targetKey, limit)
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

func (s *Spool) PendingCount(exporter string) (int, error) {
	var count int
	err := s.DB.QueryRow(`SELECT COUNT(*) FROM turns t LEFT JOIN exports e ON e.trace_uuid=t.trace_uuid AND e.exporter=? WHERE e.trace_uuid IS NULL OR e.content_hash<>t.content_hash`, exporter).Scan(&count)
	return count, err
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
		if _, e = tx.Exec(`DELETE FROM export_failures WHERE trace_uuid=? AND exporter=?`, r.TraceUUID, exporter); e != nil {
			return e
		}
	}
	return tx.Commit()
}

func (s *Spool) RecordExportFailure(exporter, targetKey string, row Row, err error, permanent bool, now time.Time) error {
	message := err.Error()
	if len(message) > 4096 {
		message = message[:4096]
	}
	_, e := s.DB.Exec(`INSERT INTO export_failures(trace_uuid,exporter,target_key,failed_at_ms,content_hash,error,permanent) VALUES(?,?,?,?,?,?,?) ON CONFLICT(trace_uuid,exporter) DO UPDATE SET target_key=excluded.target_key,failed_at_ms=excluded.failed_at_ms,content_hash=excluded.content_hash,error=excluded.error,permanent=excluded.permanent`, row.TraceUUID, exporter, targetKey, now.UnixMilli(), row.Hash, message, permanent)
	return e
}

func (s *Spool) ActivePermanentFailures(exporter, targetKey string, limit int) ([]ExportFailure, error) {
	rows, err := s.DB.Query(`SELECT f.trace_uuid,f.error FROM export_failures f JOIN turns t ON t.trace_uuid=f.trace_uuid LEFT JOIN exports e ON e.trace_uuid=t.trace_uuid AND e.exporter=f.exporter WHERE f.exporter=? AND f.target_key=? AND f.permanent=1 AND f.content_hash=t.content_hash AND (e.trace_uuid IS NULL OR e.content_hash<>t.content_hash) ORDER BY f.failed_at_ms DESC LIMIT ?`, exporter, targetKey, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	failures := []ExportFailure{}
	for rows.Next() {
		var failure ExportFailure
		if err = rows.Scan(&failure.TraceUUID, &failure.Error); err != nil {
			return nil, err
		}
		failures = append(failures, failure)
	}
	return failures, rows.Err()
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
	e := s.DB.QueryRow(`SELECT COUNT(*),COALESCE(SUM(CASE WHEN captured_at_ms>? THEN 1 ELSE 0 END),0) FROM turns`, time.Now().Add(-24*time.Hour).UnixMilli()).Scan(&total, &recent)
	return total, recent, e
}
func (s *Spool) ExportedTraceIDsBefore(exporter string, before time.Time) ([]string, error) {
	rows, err := s.DB.Query(`SELECT t.trace_uuid FROM turns t JOIN exports e ON e.trace_uuid=t.trace_uuid AND e.exporter=? WHERE t.captured_at_ms<?`, exporter, before.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Spool) Prune(before time.Time, protected map[string]bool) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TEMP TABLE IF NOT EXISTS prune_protected(trace_uuid TEXT PRIMARY KEY); DELETE FROM prune_protected; CREATE TEMP TABLE IF NOT EXISTS prune_delete(trace_uuid TEXT PRIMARY KEY); DELETE FROM prune_delete`); err != nil {
		return 0, err
	}
	for id := range protected {
		if _, err = tx.Exec(`INSERT INTO prune_protected(trace_uuid) VALUES(?)`, id); err != nil {
			return 0, err
		}
	}
	if _, err = tx.Exec(`INSERT INTO prune_delete(trace_uuid) SELECT trace_uuid FROM turns WHERE captured_at_ms<? AND NOT EXISTS (SELECT 1 FROM prune_protected p WHERE p.trace_uuid=turns.trace_uuid)`, before.UnixMilli()); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`DELETE FROM exports WHERE EXISTS (SELECT 1 FROM prune_delete p WHERE p.trace_uuid=exports.trace_uuid)`); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`DELETE FROM export_failures WHERE EXISTS (SELECT 1 FROM prune_delete p WHERE p.trace_uuid=export_failures.trace_uuid)`); err != nil {
		return 0, err
	}
	result, err := tx.Exec(`DELETE FROM turns WHERE EXISTS (SELECT 1 FROM prune_delete p WHERE p.trace_uuid=turns.trace_uuid)`)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return count, nil
}
