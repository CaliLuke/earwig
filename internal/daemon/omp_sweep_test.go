package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CaliLuke/earwig/internal/config"
	"github.com/CaliLuke/earwig/internal/spool"
)

func TestOMPSweepCapturesChangedCompletedTurns(t *testing.T) {
	dir := t.TempDir()
	workspace := filepath.Join(dir, "workspace")
	sessionsRoot := filepath.Join(dir, "omp", "sessions", "bucket")
	if err := os.MkdirAll(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sessionsRoot, 0700); err != nil {
		t.Fatal(err)
	}
	const sessionID = "019fb35e-cd1f-72c1-b95f-f265043655ec"
	sessionPath := filepath.Join(sessionsRoot, "session.jsonl")
	initial := `{"type":"session","version":3,"id":"` + sessionID + `","timestamp":"2026-09-03T10:00:00Z","cwd":"` + workspace + `","title":"OMP sweep"}
{"type":"message","id":"user0001","parentId":null,"timestamp":"2026-09-03T10:00:01Z","message":{"role":"user","content":[{"type":"text","text":"one"}]}}
{"type":"message","id":"answer01","parentId":"user0001","timestamp":"2026-09-03T10:00:02Z","message":{"role":"assistant","provider":"openai-codex","model":"gpt-5.6-sol","stopReason":"stop","content":[{"type":"text","text":"first"}]}}
`
	if err := os.WriteFile(sessionPath, []byte(initial), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := spool.Open(filepath.Join(dir, "spool.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sweeper := &Sweeper{Config: config.Config{WorkspaceRoots: []string{dir}, OMP: true, OMPSessionsPath: filepath.Dir(sessionsRoot)}, Spool: store}

	for range 2 {
		if err = sweeper.Sweep(context.Background(), SweepOptions{Provider: "omp"}); err != nil {
			t.Fatal(err)
		}
	}
	assertOMPSessionTurns(t, store, 1)

	appendFile, err := os.OpenFile(sessionPath, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	more := `{"type":"message","id":"user0002","parentId":"answer01","timestamp":"2026-09-03T10:00:03Z","message":{"role":"user","content":[{"type":"text","text":"two"}]}}
{"type":"message","id":"answer02","parentId":"user0002","timestamp":"2026-09-03T10:00:04Z","message":{"role":"assistant","provider":"openai-codex","model":"gpt-5.6-sol","stopReason":"stop","content":[{"type":"text","text":"second"}]}}
`
	if _, err = appendFile.WriteString(more); err != nil {
		_ = appendFile.Close()
		t.Fatal(err)
	}
	if err = appendFile.Close(); err != nil {
		t.Fatal(err)
	}
	changed := time.Now().Add(time.Second)
	if err = os.Chtimes(sessionPath, changed, changed); err != nil {
		t.Fatal(err)
	}
	if err = sweeper.Sweep(context.Background(), SweepOptions{Provider: "omp"}); err != nil {
		t.Fatal(err)
	}
	assertOMPSessionTurns(t, store, 2)
}

func assertOMPSessionTurns(t *testing.T, store *spool.Spool, want int) {
	t.Helper()
	sessions, stats, err := store.ListSessions(spool.SessionFilter{Provider: "omp-session-file", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Total != 1 || len(sessions) != 1 || sessions[0].Turns != want || sessions[0].Summary != "OMP sweep" {
		t.Fatalf("sessions = %#v, stats = %#v, want %d turns", sessions, stats, want)
	}
}
