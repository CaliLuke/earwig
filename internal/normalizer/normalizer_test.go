package normalizer

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T, name string) map[string]any {
	t.Helper()
	b, e := os.ReadFile(filepath.Join("..", "..", "testdata", "fixtures", name))
	if e != nil {
		t.Fatal(e)
	}
	var x map[string]any
	if e = json.Unmarshal(b, &x); e != nil {
		t.Fatal(e)
	}
	return x
}
func TestFixtureParityClaude(t *testing.T) {
	x := fixture(t, "claude.json")
	got, e := NormalizeClaude(obj(x["info"]), arr(x["messages"]))
	if e != nil {
		t.Fatal(e)
	}
	assertCanonicalJSON(t, got, generatedFixture(t)["claude"])
}
func TestFixtureParityCodex(t *testing.T) {
	x := fixture(t, "codex.json")
	got, e := NormalizeCodex(obj(x["thread"]))
	if e != nil {
		t.Fatal(e)
	}
	assertCanonicalJSON(t, got, generatedFixture(t)["codex"])
}

func generatedFixture(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "generated", "normalized.json"))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal(b, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func assertCanonicalJSON(t *testing.T, got, want any) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var gotValue any
	if err = json.Unmarshal(gotJSON, &gotValue); err != nil {
		t.Fatal(err)
	}
	gotJSON, err = json.Marshal(gotValue)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("canonical transcript mismatch\n got: %s\nwant: %s", gotJSON, wantJSON)
	}
}

func TestDeterministicTraceIDReferenceVectors(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "generated", "trace-id-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name        string `json:"name"`
		Mode        string `json:"mode"`
		TimestampMS int64  `json:"timestamp_ms"`
		Provider    string `json:"provider"`
		SessionID   string `json:"session_id"`
		TurnID      string `json:"turn_id"`
		Expected    string `json:"expected"`
	}
	if err = json.Unmarshal(b, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) < 10 {
		t.Fatalf("only %d trace-ID vectors", len(vectors))
	}
	for _, vector := range vectors {
		if vector.Mode != "deterministic" {
			continue
		}
		got := DeterministicUUIDv7(vector.TimestampMS, vector.Provider, vector.SessionID, vector.TurnID)
		if got != vector.Expected {
			t.Errorf("%s: got %s, want %s", vector.Name, got, vector.Expected)
		}
	}
}
func TestUUIDv7Layout(t *testing.T) {
	id := DeterministicUUIDv7(1784244000000, "codex", "session", "turn")
	if id[14] != '7' || id[19] < '8' || id[19] > 'b' {
		t.Fatal(id)
	}
	if id != DeterministicUUIDv7(1784244000000, "codex", "session", "turn") {
		t.Fatal("not stable")
	}
}
