package spool

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"

	"github.com/CaliLuke/earwig/internal/normalizer"
)

// Spool owns Earwig's durable SQLite state. The database handle is private so
// callers cannot bypass the package's capture, export, and retention rules.
type Spool struct {
	db   *sql.DB
	Path string
}

type Row struct {
	TraceUUID, Provider, SessionID, TurnID, Status, Payload, Hash string
	StartedMS, CompletedMS, CapturedMS                            int64
}

type ExportFailure struct {
	TraceUUID, Error string
	Permanent        bool
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
	s := &Spool{db: db, Path: path}
	_, e = db.Exec(`PRAGMA busy_timeout=5000; PRAGMA journal_mode=WAL; CREATE TABLE IF NOT EXISTS sessions(provider TEXT, session_id TEXT, cwd TEXT, summary TEXT, last_modified_ms INTEGER, last_swept_ms INTEGER, compaction_count INTEGER DEFAULT 0, gap_warned INTEGER DEFAULT 0, PRIMARY KEY(provider,session_id)); CREATE TABLE IF NOT EXISTS turns(trace_uuid TEXT PRIMARY KEY, provider TEXT, session_id TEXT, turn_id TEXT, turn_status TEXT, started_at_ms INTEGER, completed_at_ms INTEGER, payload_json TEXT, content_hash TEXT, captured_at_ms INTEGER); CREATE TABLE IF NOT EXISTS exports(trace_uuid TEXT, exporter TEXT, exported_at_ms INTEGER, content_hash TEXT, PRIMARY KEY(trace_uuid,exporter)); CREATE TABLE IF NOT EXISTS export_failures(trace_uuid TEXT, exporter TEXT, target_key TEXT, failed_at_ms INTEGER, content_hash TEXT, error TEXT, permanent INTEGER, PRIMARY KEY(trace_uuid,exporter)); CREATE TABLE IF NOT EXISTS health(key TEXT PRIMARY KEY,value_json TEXT);`)
	if e != nil {
		_ = db.Close()
		return nil, e
	}
	if e = s.migrateLegacyPayloads(); e != nil {
		_ = db.Close()
		return nil, e
	}
	if e = s.migrateCodexTraceIDs(); e != nil {
		_ = db.Close()
		return nil, e
	}
	if e = s.ensureTurnSearch(); e != nil {
		_ = db.Close()
		return nil, e
	}
	if e = os.Chmod(path, 0600); e != nil {
		_ = db.Close()
		return nil, e
	}
	return s, nil
}

func (s *Spool) Close() error { return s.db.Close() }
