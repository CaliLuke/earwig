package spool

import (
	"encoding/json"
	"time"
)

type SessionStats struct {
	Sessions    int
	Compactions int
	GapWarnings int
}

func (s *Spool) SetHealth(k string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	_, e = s.db.Exec(`INSERT INTO health(key,value_json)VALUES(?,?) ON CONFLICT(key)DO UPDATE SET value_json=excluded.value_json`, k, string(b))
	return e
}

func (s *Spool) GetHealth(k string) (string, error) {
	var v string
	e := s.db.QueryRow(`SELECT value_json FROM health WHERE key=?`, k).Scan(&v)
	return v, e
}

func (s *Spool) Stats() (int, int, error) {
	var total, recent int
	e := s.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(CASE WHEN captured_at_ms>? THEN 1 ELSE 0 END),0) FROM turns`, time.Now().Add(-24*time.Hour).UnixMilli()).Scan(&total, &recent)
	return total, recent, e
}

func (s *Spool) SessionStats() (SessionStats, error) {
	var stats SessionStats
	err := s.db.QueryRow(`SELECT COUNT(*),COALESCE(SUM(compaction_count),0),COALESCE(SUM(CASE WHEN gap_warned=1 THEN 1 ELSE 0 END),0) FROM sessions`).Scan(&stats.Sessions, &stats.Compactions, &stats.GapWarnings)
	return stats, err
}
