package spool

import (
	"database/sql"
	"fmt"
	"strings"
)

const sessionSelect = `
	SELECT
		s.provider,
		s.session_id,
		COALESCE(s.cwd, ''),
		COALESCE(s.summary, ''),
		CASE
			WHEN s.provider = 'codex-app-server'
			  AND s.last_modified_ms > 0
			  AND s.last_modified_ms < 100000000000
			THEN s.last_modified_ms * 1000
			ELSE COALESCE(s.last_modified_ms, 0)
		END AS last_activity_ms,
		COALESCE((
			SELECT MAX(t.captured_at_ms)
			FROM turns t
			WHERE t.provider = s.provider AND t.session_id = s.session_id
		), 0) AS last_captured_ms,
		COALESCE(s.last_swept_ms, 0),
		(
			SELECT COUNT(*)
			FROM turns t
			WHERE t.provider = s.provider AND t.session_id = s.session_id
		) AS turns,
		COALESCE(s.compaction_count, 0),
		COALESCE(s.gap_warned, 0)
	FROM sessions s
`

// SessionFilter controls inspection of captured session metadata.
type SessionFilter struct {
	Provider        string
	Project         string
	Search          string
	GapWarningsOnly bool
	MinTurns        int
	Limit           int
}

type SessionListStats struct {
	Total       int `json:"total"`
	GapWarnings int `json:"gap_warnings"`
}

// CapturedSession is the metadata Earwig can safely show without loading turn
// payloads. Provider contains the canonical source name stored in the spool.
type CapturedSession struct {
	Provider       string `json:"source"`
	SessionID      string `json:"session_id"`
	CWD            string `json:"workspace"`
	Summary        string `json:"summary,omitempty"`
	LastActivityMS int64  `json:"last_activity_ms"`
	LastCapturedMS int64  `json:"last_captured_ms"`
	LastSweptMS    int64  `json:"last_swept_ms"`
	Turns          int    `json:"turns"`
	Compactions    int    `json:"compactions"`
	HasGapWarning  bool   `json:"gap_warning"`
	IDPrefix       string `json:"-"`
}

// ListSessions returns recently active sessions and summary counts matching
// the filter. Turn payloads are deliberately excluded from this query.
func (s *Spool) ListSessions(filter SessionFilter) ([]CapturedSession, SessionListStats, error) {
	if filter.Limit < 1 || filter.Limit > 1000 {
		return nil, SessionListStats{}, fmt.Errorf("session limit must be between 1 and 1000")
	}
	if filter.MinTurns < 0 {
		return nil, SessionListStats{}, fmt.Errorf("minimum turn count must not be negative")
	}
	where, args := sessionFilterSQL(filter)
	var stats SessionListStats
	err := s.db.QueryRow(`
		SELECT COUNT(*), COALESCE(SUM(CASE WHEN s.gap_warned = 1 THEN 1 ELSE 0 END), 0)
		FROM sessions s
		WHERE `+where, args...).Scan(&stats.Total, &stats.GapWarnings)
	if err != nil {
		return nil, SessionListStats{}, err
	}
	rows, err := s.db.Query(
		sessionSelect+`
		WHERE `+where+`
		ORDER BY last_activity_ms DESC, s.session_id
		LIMIT ?`,
		append(args, filter.Limit)...,
	)
	if err != nil {
		return nil, SessionListStats{}, err
	}
	defer func() { _ = rows.Close() }()
	sessions, err := scanSessions(rows)
	if err != nil {
		return nil, SessionListStats{}, err
	}
	if err = s.setUniquePrefixes(sessions, 8); err != nil {
		return nil, SessionListStats{}, err
	}
	return sessions, stats, nil
}

// FindSession resolves an exact session ID or a globally unique prefix.
func (s *Spool) FindSession(prefix string) (CapturedSession, error) {
	rows, err := s.db.Query(`
		SELECT provider, session_id
		FROM sessions
		WHERE session_id = ? OR SUBSTR(session_id, 1, LENGTH(?)) = ?
		ORDER BY CASE WHEN session_id = ? THEN 0 ELSE 1 END, session_id
		LIMIT 2
	`, prefix, prefix, prefix, prefix)
	if err != nil {
		return CapturedSession{}, err
	}
	defer func() { _ = rows.Close() }()
	type key struct{ provider, sessionID string }
	var keys []key
	for rows.Next() {
		var item key
		if err = rows.Scan(&item.provider, &item.sessionID); err != nil {
			return CapturedSession{}, err
		}
		keys = append(keys, item)
	}
	if err = rows.Err(); err != nil {
		return CapturedSession{}, err
	}
	if len(keys) == 0 {
		return CapturedSession{}, fmt.Errorf("no captured session matches %q", prefix)
	}
	exact := keys[0].sessionID == prefix
	if len(keys) > 1 && (!exact || keys[1].sessionID == prefix) {
		return CapturedSession{}, fmt.Errorf("session ID prefix %q is ambiguous; use more characters", prefix)
	}
	row := s.db.QueryRow(
		sessionSelect+" WHERE s.provider = ? AND s.session_id = ?",
		keys[0].provider,
		keys[0].sessionID,
	)
	session, err := scanSession(row)
	if err != nil {
		return CapturedSession{}, err
	}
	sessions := []CapturedSession{session}
	if err = s.setUniquePrefixes(sessions, 8); err != nil {
		return CapturedSession{}, err
	}
	return sessions[0], nil
}

func sessionFilterSQL(filter SessionFilter) (string, []any) {
	gapsOnly := 0
	if filter.GapWarningsOnly {
		gapsOnly = 1
	}
	contentSearch := ""
	if filter.Search != "" {
		contentSearch = `"` + strings.ReplaceAll(filter.Search, `"`, `""`) + `"`
	}
	where := `
		(? = '' OR s.provider = ?)
		AND (? = 0 OR s.gap_warned = 1)
		AND (? = 0 OR (
			SELECT COUNT(*) FROM turns counted
			WHERE counted.provider = s.provider AND counted.session_id = s.session_id
		) >= ?)
		AND (
			? = ''
			OR LOWER(COALESCE(s.cwd, '')) = LOWER(?)
			OR SUBSTR(LOWER(COALESCE(s.cwd, '')), -(LENGTH(?) + 1)) = '/' || LOWER(?)
		)
		AND (
			? = ''
			OR INSTR(LOWER(COALESCE(s.summary, '')), LOWER(?)) > 0
			OR INSTR(LOWER(COALESCE(s.cwd, '')), LOWER(?)) > 0
			OR INSTR(LOWER(s.session_id), LOWER(?)) > 0
			OR (? <> '' AND EXISTS (
				SELECT 1 FROM turn_search
				WHERE turn_search.provider = s.provider
					AND turn_search.session_id = s.session_id
					AND turn_search MATCH ?
			))
			OR (? <> '' AND EXISTS (
				SELECT 1 FROM turns unindexed_turn
				WHERE unindexed_turn.provider = s.provider
					AND unindexed_turn.session_id = s.session_id
					AND NOT EXISTS (
						SELECT 1 FROM turn_search indexed_turn
						WHERE indexed_turn.rowid = unindexed_turn.rowid
					)
					AND INSTR(LOWER(unindexed_turn.payload_json), LOWER(?)) > 0
			))
		)
	`
	args := []any{
		filter.Provider, filter.Provider,
		gapsOnly,
		filter.MinTurns, filter.MinTurns,
		filter.Project, filter.Project, filter.Project, filter.Project,
		filter.Search, filter.Search, filter.Search, filter.Search,
		contentSearch, contentSearch,
		filter.Search, filter.Search,
	}
	return where, args
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanSession(row rowScanner) (CapturedSession, error) {
	var session CapturedSession
	var gapWarning int
	err := row.Scan(
		&session.Provider,
		&session.SessionID,
		&session.CWD,
		&session.Summary,
		&session.LastActivityMS,
		&session.LastCapturedMS,
		&session.LastSweptMS,
		&session.Turns,
		&session.Compactions,
		&gapWarning,
	)
	session.HasGapWarning = gapWarning != 0
	return session, err
}

func scanSessions(rows *sql.Rows) ([]CapturedSession, error) {
	sessions := make([]CapturedSession, 0)
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, rows.Err()
}

func (s *Spool) setUniquePrefixes(sessions []CapturedSession, minimum int) error {
	if len(sessions) == 0 {
		return nil
	}
	rows, err := s.db.Query(`SELECT session_id FROM sessions`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	var all []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return err
		}
		all = append(all, id)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	for i := range sessions {
		sessions[i].IDPrefix = shortestUniquePrefix(sessions[i].SessionID, all, minimum)
	}
	return nil
}

func shortestUniquePrefix(id string, all []string, minimum int) string {
	if len(id) <= minimum {
		return id
	}
	for length := minimum; length < len(id); length++ {
		prefix := id[:length]
		matches := 0
		for _, candidate := range all {
			if strings.HasPrefix(candidate, prefix) {
				matches++
			}
		}
		if matches == 1 {
			return prefix
		}
	}
	return id
}

// AcknowledgeGapWarning clears one reviewed capture-gap warning.
func (s *Spool) AcknowledgeGapWarning(provider, sessionID string) (bool, error) {
	result, err := s.db.Exec(`UPDATE sessions SET gap_warned=0 WHERE provider=? AND session_id=? AND gap_warned=1`, provider, sessionID)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed > 0, err
}

// AcknowledgeAllGapWarnings clears all reviewed capture-gap warnings.
func (s *Spool) AcknowledgeAllGapWarnings() (int, error) {
	result, err := s.db.Exec(`UPDATE sessions SET gap_warned=0 WHERE gap_warned=1`)
	if err != nil {
		return 0, err
	}
	changed, err := result.RowsAffected()
	return int(changed), err
}
