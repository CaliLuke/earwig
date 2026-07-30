package spool

import "fmt"

// SessionFilter controls inspection of captured session metadata.
type SessionFilter struct {
	Provider        string
	GapWarningsOnly bool
	Limit           int
}

// CapturedSession is the metadata Earwig can safely show without loading turn
// payloads. Provider contains the canonical source name stored in the spool.
type CapturedSession struct {
	Provider       string `json:"source"`
	SessionID      string `json:"session_id"`
	CWD            string `json:"workspace"`
	Summary        string `json:"summary,omitempty"`
	LastCapturedMS int64  `json:"last_captured_ms"`
	LastSweptMS    int64  `json:"last_swept_ms"`
	Turns          int    `json:"turns"`
	Compactions    int    `json:"compactions"`
	HasGapWarning  bool   `json:"gap_warning"`
}

// ListSessions returns recently captured sessions and the total number matching
// the filter. Turn payloads are deliberately excluded from this query.
func (s *Spool) ListSessions(filter SessionFilter) ([]CapturedSession, int, error) {
	if filter.Limit < 1 || filter.Limit > 1000 {
		return nil, 0, fmt.Errorf("session limit must be between 1 and 1000")
	}
	gapsOnly := 0
	if filter.GapWarningsOnly {
		gapsOnly = 1
	}
	var total int
	err := s.db.QueryRow(`
		SELECT COUNT(*)
		FROM sessions
		WHERE (? = '' OR provider = ?)
		  AND (? = 0 OR gap_warned = 1)
	`, filter.Provider, filter.Provider, gapsOnly).Scan(&total)
	if err != nil {
		return nil, 0, err
	}
	rows, err := s.db.Query(`
		SELECT
			s.provider,
			s.session_id,
			COALESCE(s.cwd, ''),
			COALESCE(s.summary, ''),
			COALESCE(MAX(t.captured_at_ms), s.last_swept_ms, s.last_modified_ms, 0) AS last_captured_ms,
			COALESCE(s.last_swept_ms, 0),
			COUNT(t.trace_uuid),
			COALESCE(s.compaction_count, 0),
			COALESCE(s.gap_warned, 0)
		FROM sessions s
		LEFT JOIN turns t
		  ON t.provider = s.provider AND t.session_id = s.session_id
		WHERE (? = '' OR s.provider = ?)
		  AND (? = 0 OR s.gap_warned = 1)
		GROUP BY
			s.provider, s.session_id, s.cwd, s.summary, s.last_swept_ms,
			s.last_modified_ms, s.compaction_count, s.gap_warned
		ORDER BY last_captured_ms DESC, s.session_id
		LIMIT ?
	`, filter.Provider, filter.Provider, gapsOnly, filter.Limit)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	sessions := make([]CapturedSession, 0)
	for rows.Next() {
		var session CapturedSession
		var gapWarning int
		if err = rows.Scan(
			&session.Provider,
			&session.SessionID,
			&session.CWD,
			&session.Summary,
			&session.LastCapturedMS,
			&session.LastSweptMS,
			&session.Turns,
			&session.Compactions,
			&gapWarning,
		); err != nil {
			return nil, 0, err
		}
		session.HasGapWarning = gapWarning != 0
		sessions = append(sessions, session)
	}
	return sessions, total, rows.Err()
}
