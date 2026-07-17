package normalizer

import (
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
	want := fixture(t, "expected.json")["claude"].(map[string]any)
	if got.Source != "claude-code-agent-sdk" || got.Capture.Fidelity != "full" || len(got.Turns) != 2 {
		t.Fatalf("contract drift: %#v", got)
	}
	for i, turn := range got.Turns {
		w := want["turns"].([]any)[i].(map[string]any)
		if turn.ID != w["id"] || turn.Status != w["status"] || turn.FinalAnswer.(map[string]any)["text"] != w["final"] {
			t.Fatalf("turn %d mismatch: %#v", i, turn)
		}
		id := TraceID("claude-code", got.Session["id"].(string), turn, 0)
		if id != want["trace_ids"].([]any)[i] {
			t.Fatalf("trace %s", id)
		}
	}
	if got.Turns[0].Trajectory[0]["input"].(map[string]any)["command"] != "API_TOKEN=[REDACTED] echo ok" {
		t.Fatal("secret not redacted")
	}
	if len(got.Session["compactions"].([]any)) != 1 {
		t.Fatal("compaction lost")
	}
}
func TestFixtureParityCodex(t *testing.T) {
	x := fixture(t, "codex.json")
	got, e := NormalizeCodex(obj(x["thread"]))
	if e != nil {
		t.Fatal(e)
	}
	want := fixture(t, "expected.json")["codex"].(map[string]any)
	if got.Source != "codex-app-server" || len(got.Turns) != 2 {
		t.Fatal("contract drift")
	}
	for i, turn := range got.Turns {
		w := want["turns"].([]any)[i].(map[string]any)
		if turn.ID != w["id"] || turn.Status != w["status"] {
			t.Fatal("turn mismatch")
		}
		if id := TraceID("codex", got.Session["id"].(string), turn, 0); id != want["trace_ids"].([]any)[i] {
			t.Fatalf("trace %s", id)
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
