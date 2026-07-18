package spool

import (
	"database/sql"
	"errors"
	"time"
)

func (s *Spool) queryRows(query string, args ...any) ([]Row, error) {
	rows, e := s.db.Query(query, args...)
	if e != nil {
		return nil, e
	}
	defer func() { _ = rows.Close() }()
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

func (s *Spool) Pending(exporter string, limit int) ([]Row, error) {
	return s.queryRows(`SELECT t.trace_uuid,t.provider,t.session_id,t.turn_id,t.turn_status,t.started_at_ms,t.completed_at_ms,t.payload_json,t.content_hash,t.captured_at_ms FROM turns t LEFT JOIN exports e ON e.trace_uuid=t.trace_uuid AND e.exporter=? WHERE e.trace_uuid IS NULL OR e.content_hash<>t.content_hash ORDER BY t.started_at_ms LIMIT ?`, exporter, limit)
}

// PendingFor excludes only permanent failures for the same payload and
// exporter target. A content change or target configuration change makes the
// row eligible automatically.
func (s *Spool) PendingFor(exporter, targetKey string, limit int) ([]Row, error) {
	return s.queryRows(`SELECT t.trace_uuid,t.provider,t.session_id,t.turn_id,t.turn_status,t.started_at_ms,t.completed_at_ms,t.payload_json,t.content_hash,t.captured_at_ms FROM turns t LEFT JOIN exports e ON e.trace_uuid=t.trace_uuid AND e.exporter=? LEFT JOIN export_failures f ON f.trace_uuid=t.trace_uuid AND f.exporter=? WHERE (e.trace_uuid IS NULL OR e.content_hash<>t.content_hash) AND NOT (COALESCE(f.permanent,0)=1 AND f.content_hash=t.content_hash AND f.target_key=?) ORDER BY t.started_at_ms LIMIT ?`, exporter, exporter, targetKey, limit)
}

func (s *Spool) PendingCount(exporter string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM turns t LEFT JOIN exports e ON e.trace_uuid=t.trace_uuid AND e.exporter=? WHERE e.trace_uuid IS NULL OR e.content_hash<>t.content_hash`, exporter).Scan(&count)
	return count, err
}

func (s *Spool) MarkExported(exporter string, rows []Row, now time.Time) error {
	tx, e := s.db.Begin()
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback() }()
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
	_, e := s.db.Exec(`INSERT INTO export_failures(trace_uuid,exporter,target_key,failed_at_ms,content_hash,error,permanent) VALUES(?,?,?,?,?,?,?) ON CONFLICT(trace_uuid,exporter) DO UPDATE SET target_key=excluded.target_key,failed_at_ms=excluded.failed_at_ms,content_hash=excluded.content_hash,error=excluded.error,permanent=excluded.permanent`, row.TraceUUID, exporter, targetKey, now.UnixMilli(), row.Hash, message, permanent)
	return e
}

func (s *Spool) ActivePermanentFailures(exporter, targetKey string, limit int) ([]ExportFailure, error) {
	rows, err := s.db.Query(`SELECT f.trace_uuid,f.error FROM export_failures f JOIN turns t ON t.trace_uuid=f.trace_uuid LEFT JOIN exports e ON e.trace_uuid=t.trace_uuid AND e.exporter=f.exporter WHERE f.exporter=? AND f.target_key=? AND f.permanent=1 AND f.content_hash=t.content_hash AND (e.trace_uuid IS NULL OR e.content_hash<>t.content_hash) ORDER BY f.failed_at_ms DESC LIMIT ?`, exporter, targetKey, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
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

func (s *Spool) ExportedAt(traceID, exporter string) (int64, bool, error) {
	var exportedAt int64
	err := s.db.QueryRow(`SELECT exported_at_ms FROM exports WHERE trace_uuid=? AND exporter=?`, traceID, exporter).Scan(&exportedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return exportedAt, err == nil, err
}

func (s *Spool) ExportCount(exporter string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM exports WHERE exporter=?`, exporter).Scan(&count)
	return count, err
}

func (s *Spool) ExportFailure(traceID, exporter string) (ExportFailure, bool, error) {
	var failure ExportFailure
	var permanent int
	failure.TraceUUID = traceID
	err := s.db.QueryRow(`SELECT error,permanent FROM export_failures WHERE trace_uuid=? AND exporter=?`, traceID, exporter).Scan(&failure.Error, &permanent)
	if errors.Is(err, sql.ErrNoRows) {
		return ExportFailure{}, false, nil
	}
	failure.Permanent = permanent != 0
	return failure, err == nil, err
}
