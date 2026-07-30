package spool

import (
	"path/filepath"
	"testing"
)

func TestListSessionsOrdersFiltersAndCounts(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "spool.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	for _, values := range [][]any{
		{"codex-app-server", "codex-new", "/workspace/new", "Newest", 3000, 3100, 0, 0},
		{"claude-code-agent-sdk", "claude-gap", "/workspace/gap", "Gap", 2000, 2100, 1, 1},
		{"codex-app-server", "codex-old", "/workspace/old", "Oldest", 1000, 1100, 0, 0},
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

	got, total, err := s.ListSessions(SessionFilter{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(got) != 2 {
		t.Fatalf("total=%d sessions=%d: %#v", total, len(got), got)
	}
	if got[0].SessionID != "codex-new" || got[0].Turns != 2 || got[0].LastCapturedMS != 6000 {
		t.Fatalf("newest session: %#v", got[0])
	}
	if got[1].SessionID != "claude-gap" || !got[1].HasGapWarning || got[1].Compactions != 1 {
		t.Fatalf("gap session: %#v", got[1])
	}

	got, total, err = s.ListSessions(SessionFilter{
		Provider:        "claude-code-agent-sdk",
		GapWarningsOnly: true,
		Limit:           20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(got) != 1 || got[0].SessionID != "claude-gap" {
		t.Fatalf("filtered sessions total=%d: %#v", total, got)
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
