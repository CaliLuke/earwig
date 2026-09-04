package normalizer

import (
	"strings"
	"testing"
)

func TestNormalizeOMPBuildsActiveBranchTurns(t *testing.T) {
	header := map[string]any{
		"id":        "019fb35e-cd1f-72c1-b95f-f265043655ec",
		"timestamp": "2026-09-03T10:00:00Z",
		"cwd":       "/work/project",
		"title":     "OMP capture",
		"version":   float64(3),
	}
	entries := []any{
		map[string]any{"type": "model_change", "id": "model001", "parentId": nil, "timestamp": "2026-09-03T10:00:00Z", "model": "openai-codex/gpt-5.6-sol"},
		ompMessage("user0001", "model001", "2026-09-03T10:00:01Z", map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "run it"}}}),
		ompMessage("oldfinal", "user0001", "2026-09-03T10:00:02Z", map[string]any{"role": "assistant", "provider": "openai-codex", "model": "gpt-5.6-sol", "stopReason": "stop", "content": []any{map[string]any{"type": "text", "text": "abandoned answer"}}}),
		ompMessage("toolcall", "user0001", "2026-09-03T10:00:03Z", map[string]any{"role": "assistant", "provider": "openai-codex", "model": "gpt-5.6-sol", "stopReason": "toolUse", "content": []any{map[string]any{"type": "thinking", "thinking": "checking"}, map[string]any{"type": "toolCall", "id": "call-1", "name": "bash", "arguments": `{"command":"TOKEN=secret echo ok"}`}}}),
		ompMessage("toolres1", "toolcall", "2026-09-03T10:00:04Z", map[string]any{"role": "toolResult", "toolCallId": "call-1", "toolName": "bash", "isError": false, "content": []any{map[string]any{"type": "text", "text": "ok"}}}),
		map[string]any{"type": "custom_message", "id": "custom01", "parentId": "toolres1", "timestamp": "2026-09-03T10:00:05Z", "customType": "notice", "content": "notice", "display": true},
		ompMessage("answer01", "custom01", "2026-09-03T10:00:06Z", map[string]any{"role": "assistant", "provider": "openai-codex", "model": "gpt-5.6-sol", "stopReason": "stop", "content": []any{map[string]any{"type": "text", "text": "done"}}}),
		map[string]any{"type": "compaction", "id": "compact1", "parentId": "answer01", "timestamp": "2026-09-03T10:00:07Z", "summary": "summary"},
		ompMessage("user0002", "compact1", "2026-09-03T10:00:08Z", map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "again"}}}),
		ompMessage("answer02", "user0002", "2026-09-03T10:00:09Z", map[string]any{"role": "assistant", "provider": "openai-codex", "model": "gpt-5.6-sol", "stopReason": "length", "errorMessage": "token limit", "content": []any{map[string]any{"type": "text", "text": "partial"}}}),
	}

	transcript, err := NormalizeOMP(header, entries, 1788429609000)
	if err != nil {
		t.Fatal(err)
	}
	if transcript.Source != "omp-session-file" || transcript.Session["name"] != "OMP capture" {
		t.Fatalf("transcript metadata = %#v", transcript)
	}
	if transcript.Session["model"] != "gpt-5.6-sol" || transcript.Session["model_provider"] != "openai-codex" {
		t.Fatalf("model metadata = %#v", transcript.Session)
	}
	if len(transcript.Session["compactions"].([]any)) != 1 {
		t.Fatalf("compactions = %#v", transcript.Session["compactions"])
	}
	if len(transcript.Turns) != 2 {
		t.Fatalf("turns = %#v", transcript.Turns)
	}
	first := transcript.Turns[0]
	if first.ID != "user0001" || first.Status != "completed" || first.FinalAnswer.(map[string]any)["text"] != "done" {
		t.Fatalf("first turn = %#v", first)
	}
	for _, message := range first.AssistantMessages {
		if message["text"] == "abandoned answer" {
			t.Fatalf("inactive branch was normalized: %#v", first.AssistantMessages)
		}
	}
	if len(first.Trajectory) != 2 {
		t.Fatalf("trajectory = %#v", first.Trajectory)
	}
	tool := first.Trajectory[0]
	if tool["status"] != "completed" || tool["result"] != "ok" || tool["duration_ms"] != int64(1000) {
		t.Fatalf("tool trajectory = %#v", tool)
	}
	command := tool["input"].(map[string]any)["command"].(string)
	if strings.Contains(command, "secret") || command != "TOKEN=[REDACTED] echo ok" {
		t.Fatalf("command was not redacted: %q", command)
	}
	if got := first.FollowingUserMessages[0]["id"]; got != "user0002" {
		t.Fatalf("following user = %#v", first.FollowingUserMessages)
	}
	second := transcript.Turns[1]
	if second.Status != "failed" || second.FinalAnswer.(map[string]any)["text"] != "partial" || second.Error == nil {
		t.Fatalf("second turn = %#v", second)
	}
}

func ompMessage(id, parentID, timestamp string, message map[string]any) map[string]any {
	return map[string]any{"type": "message", "id": id, "parentId": parentID, "timestamp": timestamp, "message": message}
}
