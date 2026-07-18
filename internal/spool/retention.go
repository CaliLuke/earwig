package spool

import "time"

func (s *Spool) ExportedTraceIDsBefore(exporter string, before time.Time) ([]string, error) {
	rows, err := s.db.Query(`SELECT t.trace_uuid FROM turns t JOIN exports e ON e.trace_uuid=t.trace_uuid AND e.exporter=? WHERE t.captured_at_ms<?`, exporter, before.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
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
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
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
