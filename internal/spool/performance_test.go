package spool

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPendingForFiltersBeforeLimit(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "spool.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	rows := []Row{
		{TraceUUID: "exported", StartedMS: 1, Hash: "a", Payload: `{"text":"already sent"}`},
		{TraceUUID: "quarantined", StartedMS: 2, Hash: "b", Payload: `{"text":"invalid"}`},
		{TraceUUID: "eligible", StartedMS: 3, Hash: "c", Payload: `{"text":"send me"}`},
		{TraceUUID: "later", StartedMS: 4, Hash: "d", Payload: `{"text":"later"}`},
	}
	for _, row := range rows {
		if err = s.StoreRow(row); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.MarkExported("test", rows[:1], time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordExportFailure("test", "target", rows[1], fmt.Errorf("invalid payload"), true, time.Now()); err != nil {
		t.Fatal(err)
	}
	pending, err := s.PendingFor("test", "target", 1)
	if err != nil || len(pending) != 1 || pending[0] != rows[2] {
		t.Fatalf("pending = %+v, %v", pending, err)
	}
}

func TestOpenAddsMetadataIndexesToExistingSpool(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spool.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	row := Row{TraceUUID: "existing", Hash: "hash", Payload: `{"text":"preserve me"}`}
	if err = s.StoreRow(row); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`DROP INDEX turns_export_candidates; DROP INDEX turns_session_metadata`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.PendingFor("test", "target", 1)
	if err != nil || len(got) != 1 || got[0] != row {
		t.Fatalf("migration changed stored turn: %+v, %v", got, err)
	}
}

// Run with -bench=BenchmarkExportPlanning -benchmem. Large already-exported
// payloads expose accidental table scans even when the pending queue is empty.
func BenchmarkExportPlanning(b *testing.B) {
	s, err := Open(filepath.Join(b.TempDir(), "spool.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	payload := `{"text":"` + strings.Repeat("payload ", 8192) + `"}`
	rows := make([]Row, 256)
	for i := range rows {
		rows[i] = Row{TraceUUID: fmt.Sprint(i), Hash: "hash", StartedMS: int64(i), Payload: payload}
		if err = s.StoreRow(rows[i]); err != nil {
			b.Fatal(err)
		}
	}
	if err = s.MarkExported("test", rows, time.Now()); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		pending, err := s.PendingFor("test", "test", 500)
		if err != nil || len(pending) != 0 {
			b.Fatalf("pending = %d, %v", len(pending), err)
		}
	}
}
