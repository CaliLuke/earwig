package exporter

import (
	"encoding/json"
	"fmt"
	"github.com/CaliLuke/earwig/internal/normalizer"
	"github.com/CaliLuke/earwig/internal/spool"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoopbackGuard(t *testing.T) {
	if loopback("https://example.com") == nil {
		t.Fatal("allowed remote URL")
	}
	if loopback("http://127.0.0.1:5173") != nil {
		t.Fatal("rejected loopback")
	}
}
func TestJSONDirIdempotentDrain(t *testing.T) {
	s, e := spool.Open(filepath.Join(t.TempDir(), "s.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	_, e = s.DB.Exec(`INSERT INTO turns VALUES('trace','p','session','turn','completed',1,2,'{}','hash',1)`)
	if e != nil {
		t.Fatal(e)
	}
	j := JSONDir{Dir: t.TempDir()}
	if e = Drain(s, j); e != nil {
		t.Fatal(e)
	}
	if e = Drain(s, j); e != nil {
		t.Fatal(e)
	}
}

func TestOpikMapsReviewFieldsAndPatchesConflict(t *testing.T) {
	payload, err := json.Marshal(spool.TurnPayload{
		SchemaVersion: 1,
		Source:        "claude-code-agent-sdk",
		Capture:       normalizer.Capture{Fidelity: "full"},
		Session:       map[string]any{"id": "session", "cwd": "/tmp/project"},
		Turn: normalizer.Turn{
			ID:                    "turn-1",
			Status:                "completed",
			DurationMS:            float64(12),
			UserMessages:          []map[string]any{{"content": "question"}},
			AssistantMessages:     []map[string]any{{"text": "answer"}},
			FinalAnswer:           map[string]any{"text": "answer"},
			Trajectory:            []map[string]any{{"tool": "Read"}},
			FollowingUserMessages: []map[string]any{{"content": "next"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	requests := []struct {
		method, path string
		body         map[string]any
	}{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		requests = append(requests, struct {
			method, path string
			body         map[string]any
		}{r.Method, r.URL.Path, body})
		if r.Method == http.MethodPost {
			http.Error(w, "Trace already exists", http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	row := spool.Row{TraceUUID: "01900000-0000-7000-8000-000000000001", Provider: "claude-code-agent-sdk", SessionID: "session", TurnID: "turn-1", Status: "completed", Payload: string(payload), StartedMS: 1, CompletedMS: 2, CapturedMS: 3}
	if err = (Opik{URL: server.URL, ProjectName: "autok-agent-evals"}).Export([]spool.Row{row}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].method != http.MethodPost || requests[1].method != http.MethodPatch || requests[1].path != "/api/v1/private/traces/"+row.TraceUUID {
		t.Fatalf("unexpected requests: %#v", requests)
	}
	for i, request := range requests {
		if request.body["project_name"] != "autok-agent-evals" || request.body["input"] == nil || request.body["output"] == nil || request.body["metadata"] == nil {
			t.Fatalf("request %d missing mapped fields: %#v", i, request.body)
		}
	}
	if _, exists := requests[0].body["start_time"]; !exists {
		t.Fatal("create omitted start_time")
	}
	if _, exists := requests[1].body["start_time"]; exists {
		t.Fatal("update included immutable start_time")
	}
	for _, curationField := range []string{"name", "tags"} {
		if _, exists := requests[1].body[curationField]; exists {
			t.Fatalf("update clobbers upstream %s: %#v", curationField, requests[1].body)
		}
	}
}

func TestOpikProtectedTraceIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/kept") {
			_, _ = io.WriteString(w, `{"tags":["inbox","kept"]}`)
			return
		}
		_, _ = io.WriteString(w, `{"tags":["inbox"]}`)
	}))
	defer server.Close()
	protected, err := (Opik{URL: server.URL}).ProtectedTraceIDs([]string{"kept", "ordinary"})
	if err != nil {
		t.Fatal(err)
	}
	if !protected["kept"] || protected["ordinary"] {
		t.Fatalf("protected traces = %#v", protected)
	}
}

func TestOpikProjectMismatchIsPermanentAndActionable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "project name does not match the existing trace", http.StatusConflict)
	}))
	defer server.Close()
	payload, _ := json.Marshal(spool.TurnPayload{Source: "claude-code-agent-sdk", Turn: normalizer.Turn{ID: "turn", Status: "completed"}})
	err := (Opik{URL: server.URL, ProjectName: "earwig"}).Export([]spool.Row{{TraceUUID: "01900000-0000-7000-8000-000000000001", Provider: "claude-code-agent-sdk", SessionID: "session", Status: "completed", Payload: string(payload)}})
	if err == nil || !isPermanent(err) || !strings.Contains(err.Error(), "opik_project") || !strings.Contains(err.Error(), "autok-agent-evals") {
		t.Fatalf("project mismatch was not actionable/permanent: %v", err)
	}
}

type isolatingExporter struct {
	target, poison string
	seen           []string
}

func (e *isolatingExporter) Name() string      { return "isolated" }
func (e *isolatingExporter) TargetKey() string { return e.target }
func (e *isolatingExporter) Health() error     { return nil }
func (e *isolatingExporter) Export(rows []spool.Row) error {
	e.seen = append(e.seen, rows[0].TraceUUID)
	if rows[0].TraceUUID == e.poison {
		return permanentError{fmt.Errorf("project mismatch")}
	}
	return nil
}

func TestDrainQuarantinesPermanentRowAndContinues(t *testing.T) {
	s, err := spool.Open(filepath.Join(t.TempDir(), "s.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i, id := range []string{"poison", "good-1", "good-2"} {
		if _, err = s.DB.Exec(`INSERT INTO turns VALUES(?,?,?,?,?,?,?,?,?,?)`, id, "p", "session", id, "completed", i+1, i+2, `{}`, "hash", 1); err != nil {
			t.Fatal(err)
		}
	}
	exporter := &isolatingExporter{target: "project-a", poison: "poison"}
	if err = Drain(s, exporter); err == nil || !strings.Contains(err.Error(), "quarantined") {
		t.Fatalf("quarantine was not surfaced: %v", err)
	}
	if got := strings.Join(exporter.seen, ","); got != "poison,good-1,good-2" {
		t.Fatalf("poisoned row blocked later rows: %s", got)
	}
	if pending, countErr := s.PendingFor(exporter.Name(), exporter.TargetKey(), 10); countErr != nil || len(pending) != 0 {
		t.Fatalf("active pending rows = %#v, %v", pending, countErr)
	}
	if count, countErr := s.PendingCount(exporter.Name()); countErr != nil || count != 1 {
		t.Fatalf("visible pending count = %d, %v", count, countErr)
	}

	// A target change releases the quarantine and lets corrected configuration
	// retry the exact same content.
	exporter.target = "project-b"
	exporter.poison = ""
	if err = Drain(s, exporter); err != nil {
		t.Fatal(err)
	}
	if exporter.seen[len(exporter.seen)-1] != "poison" {
		t.Fatalf("target change did not retry poison row: %#v", exporter.seen)
	}
	if count, countErr := s.PendingCount(exporter.Name()); countErr != nil || count != 0 {
		t.Fatalf("pending after recovery = %d, %v", count, countErr)
	}
}
