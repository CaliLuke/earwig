package spool

import (
	"github.com/CaliLuke/earwig/internal/normalizer"
	"path/filepath"
	"testing"
	"time"
)

func sample() normalizer.Transcript {
	return normalizer.Transcript{SchemaVersion: 1, Source: "claude-code-agent-sdk", Capture: normalizer.Capture{Fidelity: "full"}, Session: map[string]any{"id": "0aaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", "created_at": int64(1)}, Turns: []normalizer.Turn{{ID: "u1", Status: "completed", StartedAt: "2026-01-01T00:00:00Z", CompletedAt: "2026-01-01T00:00:01Z", UserMessages: []map[string]any{}, AssistantMessages: []map[string]any{}, Trajectory: []map[string]any{}, FollowingUserMessages: []map[string]any{}}}}
}
func TestUpsertAndRehash(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "spool.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	x := sample()
	if n, e := s.UpsertTranscript(x, time.Now()); e != nil || n != 1 {
		t.Fatalf("%d %v", n, e)
	}
	if n, e := s.UpsertTranscript(x, time.Now()); e != nil || n != 0 {
		t.Fatalf("%d %v", n, e)
	}
	x.Turns[0].Status = "failed"
	if n, e := s.UpsertTranscript(x, time.Now()); e != nil || n != 1 {
		t.Fatalf("%d %v", n, e)
	}
	r, e := s.Pending("test", 10)
	if e != nil || len(r) != 1 {
		t.Fatal(e, len(r))
	}
	if e = s.MarkExported("test", r, time.Now()); e != nil {
		t.Fatal(e)
	}
	r, e = s.Pending("test", 10)
	if e != nil || len(r) != 0 {
		t.Fatal(e, len(r))
	}
}
