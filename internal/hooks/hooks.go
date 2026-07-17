// Package hooks is the only package allowed to change Claude settings.
package hooks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func SettingsPath() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".claude", "settings.json")
}
func BackupPath() string { return SettingsPath() + ".earwig-backup" }
func Desired(binary string) map[string]any {
	command := binary + " sweep --provider claude --session \"$CLAUDE_SESSION_ID\""
	return map[string]any{"hooks": map[string]any{"PreCompact": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}}, "Stop": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}}, "SessionEnd": []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}}}}
}
func Preview(binary string) (string, error) {
	b, e := json.MarshalIndent(Desired(binary), "", "  ")
	return string(b), e
}
func Install(binary string) error {
	p := SettingsPath()
	old, e := os.ReadFile(p)
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		return e
	}
	if e := os.WriteFile(BackupPath(), old, 0600); e != nil {
		return e
	}
	var x map[string]any
	if len(old) > 0 {
		if e := json.Unmarshal(old, &x); e != nil {
			return fmt.Errorf("Claude settings are not valid JSON: %w", e)
		}
	}
	if x == nil {
		x = map[string]any{}
	}
	x["hooks"] = Desired(binary)["hooks"]
	b, e := json.MarshalIndent(x, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(p, append(b, '\n'), 0600)
}
func Remove() error {
	b, e := os.ReadFile(BackupPath())
	if e != nil {
		return e
	}
	if e = os.WriteFile(SettingsPath(), b, 0600); e != nil {
		return e
	}
	return os.Remove(BackupPath())
}
func Same(a, b []byte) bool { return bytes.Equal(a, b) }
