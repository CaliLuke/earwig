package spool

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestListSessionsOrdersFiltersCountsAndNormalizesActivity(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "spool.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	for _, values := range [][]any{
		{"codex-app-server", "codex-new", "/workspace/new", "Ship new CLI", 1785428000, 3100, 0, 0},
		{"claude-code-agent-sdk", "claude-gap", "/workspace/gap", "Investigate capture gap", 1785427000000, 2100, 1, 1},
		{"codex-app-server", "codex-old", "/workspace/old", "Oldest title", 1785426000, 1100, 0, 0},
	} {
		if _, err = s.db.Exec(`
			INSERT INTO sessions(
				provider, session_id, cwd, summary, last_modified_ms,
				last_swept_ms, compaction_count, gap_warned
			) VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		`, values...); err != nil {
			t.Fatal(err)
		}
	}
	for _, values := range [][]any{
		{"trace-new-1", "codex-app-server", "codex-new", "turn-1", "completed", 1, 2, `{}`, "a", 5000},
		{"trace-new-2", "codex-app-server", "codex-new", "turn-2", "completed", 3, 4, `{}`, "b", 6000},
		{"trace-gap", "claude-code-agent-sdk", "claude-gap", "turn-1", "completed", 1, 2, `{}`, "c", 4000},
	} {
		if _, err = s.db.Exec(`INSERT INTO turns VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, values...); err != nil {
			t.Fatal(err)
		}
	}

	got, stats, err := s.ListSessions(SessionFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 3 || stats.GapWarnings != 1 || len(got) != 2 {
		t.Fatalf("stats=%#v sessions=%d: %#v", stats, len(got), got)
	}
	if got[0].SessionID != "codex-new" || got[0].Turns != 2 ||
		got[0].LastActivityMS != 1785428000000 || got[0].LastCapturedMS != 6000 {
		t.Fatalf("newest session: %#v", got[0])
	}
	if got[1].SessionID != "claude-gap" || !got[1].HasGapWarning || got[1].Compactions != 1 {
		t.Fatalf("gap session: %#v", got[1])
	}

	got, stats, err = s.ListSessions(SessionFilter{
		Provider:        "claude-code-agent-sdk",
		Project:         "gap",
		Search:          "CAPTURE",
		GapWarningsOnly: true,
		Limit:           20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1 || stats.GapWarnings != 1 || len(got) != 1 || got[0].SessionID != "claude-gap" {
		t.Fatalf("filtered sessions stats=%#v: %#v", stats, got)
	}

	got, stats, err = s.ListSessions(SessionFilter{Project: "/workspace/new", Limit: 20})
	if err != nil || stats.Total != 1 || got[0].SessionID != "codex-new" {
		t.Fatalf("full project path stats=%#v sessions=%#v err=%v", stats, got, err)
	}
}

func TestFindSessionResolvesExactAndUniquePrefixes(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "spool.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, id := range []string{"019fabcd-one", "019fabcd-two", "claude-unique"} {
		if _, err = s.db.Exec(`
			INSERT INTO sessions(provider, session_id, cwd, summary, last_modified_ms, last_swept_ms)
			VALUES('codex-app-server', ?, '/workspace/project', ?, 1785428000, 1785429000000)
		`, id, id+" title"); err != nil {
			t.Fatal(err)
		}
	}

	session, err := s.FindSession("claude-u")
	if err != nil || session.SessionID != "claude-unique" {
		t.Fatalf("unique lookup: %#v, %v", session, err)
	}
	session, err = s.FindSession("019fabcd-one")
	if err != nil || session.SessionID != "019fabcd-one" || session.IDPrefix != "019fabcd-o" {
		t.Fatalf("exact lookup: %#v, %v", session, err)
	}
	if _, err = s.FindSession("019fabcd"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous lookup error: %v", err)
	}
	if _, err = s.FindSession("missing"); err == nil || !strings.Contains(err.Error(), "no captured session") {
		t.Fatalf("missing lookup error: %v", err)
	}
}

func TestListSessionsRejectsUnsafeLimit(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "spool.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, _, err = s.ListSessions(SessionFilter{}); err == nil {
		t.Fatal("zero limit accepted")
	}
	if _, _, err = s.ListSessions(SessionFilter{Limit: 1001}); err == nil {
		t.Fatal("oversized limit accepted")
	}
}

func TestSessionTimestampNormalizesCodexUnixSeconds(t *testing.T) {
	const seconds = int64(1785428633)
	if got := sessionTimestamp("codex-app-server", seconds); got != seconds*1000 {
		t.Fatalf("Codex timestamp = %d", got)
	}
	if got := sessionTimestamp("claude-code-agent-sdk", seconds); got != seconds {
		t.Fatalf("Claude timestamp changed to %d", got)
	}
}
