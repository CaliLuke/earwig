package hooks

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInstallRemoveRoundTripIsByteIdentical(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	original := map[string]any{
		"env": map[string]any{"ORG_POLICY": "enabled"},
		"hooks": map[string]any{
			"PreCompact": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "/opt/org/precompact"}}}},
			"Stop":       []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "/opt/org/stop"}}}},
			"SessionEnd": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "/opt/org/end"}}}},
		},
	}
	if err := writeSettings(path, original); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = installAt(path, "/opt/earwig"); err != nil {
		t.Fatal(err)
	}
	if err = installAt(path, "/opt/earwig"); err != nil {
		t.Fatal(err)
	}
	if err = removeAt(path); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("install/remove changed fixture bytes\nbefore: %s\nafter: %s", before, after)
	}
}

func TestInvalidSettingsAreRefusedWithoutWrites(t *testing.T) {
	for name, contents := range map[string]string{
		"invalid-json":       `{"hooks":`,
		"hooks-not-object":   `{"hooks":[]}`,
		"event-not-array":    `{"hooks":{"Stop":{}}}`,
		"commands-not-array": `{"hooks":{"Stop":[{"hooks":{}}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			before := sha256.Sum256([]byte(contents))
			if err := installAt(path, "/opt/earwig"); err == nil {
				t.Fatal("invalid settings were accepted")
			}
			afterBytes, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if after := sha256.Sum256(afterBytes); after != before {
				t.Fatal("invalid settings were modified")
			}
		})
	}
}

func TestPublicSettingsPathCanBeConfinedToFixtureHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".claude", "settings.json")
	if got := SettingsPath(); got != want {
		t.Fatalf("settings path escaped fixture home: %q", got)
	}
}

func readObject(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var x map[string]any
	if err = json.Unmarshal(b, &x); err != nil {
		t.Fatal(err)
	}
	return x
}

func managedCount(x map[string]any) int {
	count := 0
	hooks, _ := x["hooks"].(map[string]any)
	for _, rawGroups := range hooks {
		groups, _ := rawGroups.([]any)
		for _, group := range groups {
			m, _ := group.(map[string]any)
			commands, _ := m["hooks"].([]any)
			for _, entry := range commands {
				if isManagedCommand(entry) {
					count++
				}
			}
		}
	}
	return count
}

func TestInstallMergesAndRemoveIsSurgical(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	original := `{
  "env": {"ORG_POLICY": "enabled"},
  "hooks": {
    "Stop": [{"matcher": "", "hooks": [{"type": "command", "command": "/opt/org/security-hook"}]}],
    "SessionStart": [{"hooks": [{"type": "command", "command": "/opt/org/start-hook"}]}]
  }
}`
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}

	if err := installAt(path, "/Applications/Earwig App/earwig"); err != nil {
		t.Fatal(err)
	}
	if err := installAt(path, "/Applications/Earwig App/earwig"); err != nil {
		t.Fatal(err)
	}
	installed := readObject(t, path)
	if installed["env"].(map[string]any)["ORG_POLICY"] != "enabled" {
		t.Fatal("non-hook settings were changed")
	}
	if managedCount(installed) != len(events) {
		t.Fatalf("managed hook count = %d", managedCount(installed))
	}
	if !strings.Contains(stringMustJSON(t, installed), "/opt/org/security-hook") {
		t.Fatal("organization hook was removed")
	}

	// A setting edited after install must survive removal; no snapshot restore
	// is allowed to roll it back.
	installed["theme"] = "dark"
	b, _ := json.Marshal(installed)
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err := removeAt(path); err != nil {
		t.Fatal(err)
	}
	removed := readObject(t, path)
	if removed["theme"] != "dark" || managedCount(removed) != 0 {
		t.Fatalf("surgical removal failed: %#v", removed)
	}
	encoded := stringMustJSON(t, removed)
	for _, want := range []string{"/opt/org/security-hook", "/opt/org/start-hook"} {
		if !strings.Contains(encoded, want) {
			t.Fatalf("lost existing hook %s", want)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("settings mode = %v", info.Mode().Perm())
	}
}

func TestRemovePreservesOtherCommandsInSameGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	group := hookGroup("/usr/local/bin/earwig")
	group["hooks"] = append(group["hooks"].([]any), map[string]any{"type": "command", "command": "/opt/org/also-stop"})
	x := map[string]any{"hooks": map[string]any{"Stop": []any{group}}}
	if err := writeSettings(path, x); err != nil {
		t.Fatal(err)
	}
	if err := removeAt(path); err != nil {
		t.Fatal(err)
	}
	got := stringMustJSON(t, readObject(t, path))
	if strings.Contains(got, ManagedArgument) || !strings.Contains(got, "/opt/org/also-stop") {
		t.Fatal(got)
	}
}

func TestInstallReplacesLegacyShellExpandedHook(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	legacy := map[string]any{"hooks": map[string]any{"Stop": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": `"/old/earwig" sweep --provider claude --session "$CLAUDE_SESSION_ID"`}}}}}}
	if err := writeSettings(path, legacy); err != nil {
		t.Fatal(err)
	}
	if err := installAt(path, "/new/earwig"); err != nil {
		t.Fatal(err)
	}
	got := stringMustJSON(t, readObject(t, path))
	if strings.Contains(got, "CLAUDE_SESSION_ID") || managedCount(readObject(t, path)) != len(events) {
		t.Fatalf("legacy hook was not upgraded: %s", got)
	}
}

func TestSessionIDReadsHookJSON(t *testing.T) {
	id := "0aaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	got, err := SessionID(strings.NewReader(`{"session_id":"` + id + `","hook_event_name":"Stop"}`))
	if err != nil || got != id {
		t.Fatalf("%q, %v", got, err)
	}
}

func stringMustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
