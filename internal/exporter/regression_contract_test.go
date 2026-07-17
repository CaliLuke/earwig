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

// These black-box regressions deliberately use only the public exporter/spool
// surface so they can also be run against the pre-fix implementation.
func TestContractPatchDoesNotOwnCurationFields(t *testing.T) {
	var patch map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			w.WriteHeader(http.StatusConflict)
			return
		}
		body, _ := io.ReadAll(request.Body)
		_ = json.Unmarshal(body, &patch)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	row := contractRow("patch-contract", 1000)
	if err := (Opik{URL: server.URL, ProjectName: "project"}).Export([]spool.Row{row}); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"name", "tags"} {
		if _, exists := patch[field]; exists {
			t.Fatalf("PATCH owns reviewer field %q: %#v", field, patch)
		}
	}
}

func TestContractPermanentPoisonDoesNotBlockLaterRows(t *testing.T) {
	poison := contractRow("poison-contract", 1000)
	good := contractRow("good-contract", 2000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			body, _ := io.ReadAll(request.Body)
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			if payload["id"] == poison.TraceUUID {
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, "project name does not match the existing trace")
				return
			}
		}
		if request.Method == http.MethodPatch && strings.Contains(request.URL.Path, poison.TraceUUID) {
			w.WriteHeader(http.StatusConflict)
			_, _ = io.WriteString(w, "project name does not match the existing trace")
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()
	s, err := spool.Open(filepath.Join(t.TempDir(), "spool.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, row := range []spool.Row{poison, good} {
		if _, err = s.DB.Exec(`INSERT INTO turns(trace_uuid,provider,session_id,turn_id,turn_status,started_at_ms,completed_at_ms,payload_json,content_hash,captured_at_ms) VALUES(?,?,?,?,?,?,?,?,?,?)`, row.TraceUUID, row.Provider, row.SessionID, row.TurnID, row.Status, row.StartedMS, row.CompletedMS, row.Payload, row.Hash, row.CapturedMS); err != nil {
			t.Fatal(err)
		}
	}
	if err = Drain(s, Opik{URL: server.URL, ProjectName: "target"}); err == nil || !strings.Contains(err.Error(), "quarantined") {
		t.Fatalf("permanent failure was not surfaced: %v", err)
	}
	var goodExports, poisonFailures int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM exports WHERE trace_uuid=? AND exporter='opik'`, good.TraceUUID).Scan(&goodExports)
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM export_failures WHERE trace_uuid=? AND exporter='opik' AND permanent=1`, poison.TraceUUID).Scan(&poisonFailures)
	if goodExports != 1 || poisonFailures != 1 {
		t.Fatalf("poison blocked drain: good exports=%d poison failures=%d", goodExports, poisonFailures)
	}
}

func contractRow(label string, startedMS int64) spool.Row {
	traceID := normalizer.DeterministicUUIDv7(int64(startedMS), "claude-code", "contract-session", label)
	payload, _ := json.Marshal(spool.TurnPayload{
		SchemaVersion: 1,
		Source:        "claude-code-agent-sdk",
		Session:       map[string]any{"id": "contract-session"},
		Turn: normalizer.Turn{
			ID: label, Status: "completed", UserMessages: []map[string]any{}, AssistantMessages: []map[string]any{}, Trajectory: []map[string]any{}, FollowingUserMessages: []map[string]any{},
		},
	})
	return spool.Row{TraceUUID: traceID, Provider: "claude-code-agent-sdk", SessionID: "contract-session", TurnID: label, Status: "completed", StartedMS: int64(startedMS), CompletedMS: int64(startedMS + 1), Payload: string(payload), Hash: "hash-" + label, CapturedMS: int64(startedMS)}
}
