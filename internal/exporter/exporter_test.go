package exporter

import (
	"encoding/json"
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
