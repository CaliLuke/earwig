package spool

// Metadata queries must not walk the multi-megabyte payloads in turns. These
// small covering indexes serve export planning, session inspection and status.
// Existing spools build them once; subsequent opens only check their existence.
func (s *Spool) ensureMetadataIndexes() error {
	_, err := s.db.Exec(`
		CREATE INDEX IF NOT EXISTS turns_export_candidates
		ON turns(started_at_ms, trace_uuid, content_hash);
		CREATE INDEX IF NOT EXISTS turns_session_metadata
		ON turns(provider, session_id, started_at_ms, trace_uuid, captured_at_ms);
	`)
	return err
}
