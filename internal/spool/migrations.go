package spool

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/CaliLuke/earwig/internal/normalizer"
)

// migrateLegacyPayloads rewrites rows created before per-turn envelopes were
// introduced. It operates in small batches because the legacy representation
// may contain a multi-megabyte transcript in every row.
func (s *Spool) migrateLegacyPayloads() error {
	type migration struct {
		id, payload, hash string
	}
	var format string
	if err := s.db.QueryRow(`SELECT value_json FROM health WHERE key='payload_format'`).Scan(&format); err == nil && format == `"turn-v1"` {
		return nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	for {
		batch, err := func() ([]migration, error) {
			rows, queryErr := s.db.Query(`SELECT trace_uuid,payload_json FROM turns WHERE payload_json LIKE '{"transcript":%' LIMIT 50`)
			if queryErr != nil {
				return nil, queryErr
			}
			defer func() { _ = rows.Close() }()
			batch := []migration{}
			for rows.Next() {
				var id, legacyJSON string
				if scanErr := rows.Scan(&id, &legacyJSON); scanErr != nil {
					return nil, scanErr
				}
				var legacy struct {
					Transcript normalizer.Transcript `json:"transcript"`
					Turn       normalizer.Turn       `json:"turn"`
				}
				if unmarshalErr := json.Unmarshal([]byte(legacyJSON), &legacy); unmarshalErr != nil {
					return nil, unmarshalErr
				}
				payload := TurnPayload{SchemaVersion: legacy.Transcript.SchemaVersion, Source: legacy.Transcript.Source, Capture: legacy.Transcript.Capture, Session: sessionHeader(legacy.Transcript.Session), Turn: legacy.Turn}
				b, marshalErr := json.Marshal(payload)
				if marshalErr != nil {
					return nil, marshalErr
				}
				batch = append(batch, migration{id: id, payload: string(b), hash: hash(b)})
			}
			return batch, rows.Err()
		}()
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return s.SetHealth("payload_format", "turn-v1")
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		for _, item := range batch {
			if _, err = tx.Exec(`UPDATE turns SET payload_json=?,content_hash=? WHERE trace_uuid=?`, item.payload, item.hash, item.id); err != nil {
				_ = tx.Rollback()
				return err
			}
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
}

// migrateCodexTraceIDs converges rows captured by early Earwig versions with
// Earwig's native Codex turn UUID policy. Claude IDs remain deterministic
// UUIDv7 values because its native message IDs are UUIDv4.
func (s *Spool) migrateCodexTraceIDs() error {
	var policy string
	if err := s.db.QueryRow(`SELECT value_json FROM health WHERE key='codex_trace_identity'`).Scan(&policy); err == nil && policy == `"native-v1"` {
		return nil
	} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	type change struct{ oldID, newID string }
	changes, err := func() ([]change, error) {
		rows, queryErr := s.db.Query(`SELECT trace_uuid,payload_json FROM turns WHERE provider='codex-app-server'`)
		if queryErr != nil {
			return nil, queryErr
		}
		defer func() { _ = rows.Close() }()
		changes := []change{}
		for rows.Next() {
			var oldID, payloadJSON string
			if scanErr := rows.Scan(&oldID, &payloadJSON); scanErr != nil {
				return nil, scanErr
			}
			var payload TurnPayload
			if unmarshalErr := json.Unmarshal([]byte(payloadJSON), &payload); unmarshalErr != nil {
				return nil, unmarshalErr
			}
			if native, ok := nativeCodexTraceID(payload.Turn.ID); ok && native != oldID {
				changes = append(changes, change{oldID: oldID, newID: native})
			}
		}
		return changes, rows.Err()
	}()
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
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
