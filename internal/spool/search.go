package spool

// ensureTurnSearch creates a trigram FTS5 index for new and changed turns.
// Existing unindexed turns use a read-only fallback so opening a large spool
// never blocks the daemon on a one-time index build.
func (s *Spool) ensureTurnSearch() error {
	_, err := s.db.Exec(`
		CREATE VIRTUAL TABLE IF NOT EXISTS turn_search USING fts5(
			trace_uuid UNINDEXED,
			provider UNINDEXED,
			session_id UNINDEXED,
			content,
			tokenize='trigram'
		);
		CREATE TRIGGER IF NOT EXISTS turn_search_insert AFTER INSERT ON turns BEGIN
			INSERT INTO turn_search(rowid,trace_uuid,provider,session_id,content)
			VALUES (new.rowid,new.trace_uuid,new.provider,new.session_id,new.payload_json);
		END;
		CREATE TRIGGER IF NOT EXISTS turn_search_delete AFTER DELETE ON turns BEGIN
			DELETE FROM turn_search WHERE rowid=old.rowid;
		END;
		CREATE TRIGGER IF NOT EXISTS turn_search_update
		AFTER UPDATE OF trace_uuid,provider,session_id,payload_json ON turns BEGIN
			DELETE FROM turn_search WHERE rowid=old.rowid;
			INSERT INTO turn_search(rowid,trace_uuid,provider,session_id,content)
			VALUES (new.rowid,new.trace_uuid,new.provider,new.session_id,new.payload_json);
		END;
	`)
	return err
}
