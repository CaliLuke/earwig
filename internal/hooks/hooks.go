// Package hooks is the only package allowed to change Claude settings.
package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const ManagedArgument = "--managed-by-earwig"

var events = []string{"PreCompact", "Stop", "SessionEnd"}

func SettingsPath() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".claude", "settings.json")
}

// BackupPath is retained for compatibility with installs made by older
// versions. New installs do not use snapshots: settings are merged and
// managed hook entries are removed surgically.
func BackupPath() string { return SettingsPath() + ".earwig-backup" }

func command(binary string) string {
	return strconv.Quote(binary) + " hook claude " + ManagedArgument
}

func hookGroup(binary string) map[string]any {
	return map[string]any{
		"hooks": []any{map[string]any{
			"type":    "command",
			"command": command(binary),
		}},
	}
}

func Desired(binary string) map[string]any {
	h := map[string]any{}
	for _, event := range events {
		h[event] = []any{hookGroup(binary)}
	}
	return map[string]any{"hooks": h}
}

func Preview(binary string) (string, error) {
	b, e := json.MarshalIndent(Desired(binary), "", "  ")
	return string(b), e
}

func Install(binary string) error { return installAt(SettingsPath(), binary) }

func installAt(path, binary string) error {
	x, err := readSettings(path)
	if err != nil {
		return err
	}
	hooks, err := objectField(x, "hooks")
	if err != nil {
		return err
	}
	for _, event := range events {
		groups, err := arrayField(hooks, event)
		if err != nil {
			return err
		}
		groups, err = filterManaged(event, groups)
		if err != nil {
			return err
		}
		hooks[event] = append(groups, hookGroup(binary))
	}
	x["hooks"] = hooks
	return writeSettings(path, x)
}

func Remove() error { return removeAt(SettingsPath()) }

func removeAt(path string) error {
	x, err := readSettings(path)
	if err != nil {
		return err
	}
	rawHooks, exists := x["hooks"]
	if !exists {
		return nil
	}
	hooks, ok := rawHooks.(map[string]any)
	if !ok {
		return fmt.Errorf("Claude settings hooks must be an object")
	}
	for event, rawGroups := range hooks {
		groups, ok := rawGroups.([]any)
		if !ok {
			return fmt.Errorf("Claude settings hooks.%s must be an array", event)
		}
		kept, err := filterManaged(event, groups)
		if err != nil {
			return err
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	if len(hooks) == 0 {
		delete(x, "hooks")
	} else {
		x["hooks"] = hooks
	}
	return writeSettings(path, x)
}

// SessionID reads the documented Claude hook JSON payload. The session ID is
// never interpolated into a shell command; it remains data until the sweep.
func SessionID(r io.Reader) (string, error) {
	var input struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&input); err != nil {
		return "", fmt.Errorf("read Claude hook input: %w", err)
	}
	if input.SessionID == "" {
		return "", fmt.Errorf("Claude hook input omitted session_id")
	}
	return input.SessionID, nil
}

func readSettings(path string) (map[string]any, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var x map[string]any
	if err := json.Unmarshal(b, &x); err != nil {
		return nil, fmt.Errorf("Claude settings are not valid JSON: %w", err)
	}
	if x == nil {
		x = map[string]any{}
	}
	return x, nil
}

func objectField(parent map[string]any, key string) (map[string]any, error) {
	v, ok := parent[key]
	if !ok {
		return map[string]any{}, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("Claude settings %s must be an object", key)
	}
	return m, nil
}

func arrayField(parent map[string]any, key string) ([]any, error) {
	v, ok := parent[key]
	if !ok {
		return []any{}, nil
	}
	a, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("Claude settings hooks.%s must be an array", key)
	}
	return a, nil
}

func isManagedCommand(v any) bool {
	m, ok := v.(map[string]any)
	if !ok || m["type"] != "command" {
		return false
	}
	c, _ := m["command"].(string)
	return strings.HasSuffix(c, " hook claude "+ManagedArgument) || strings.HasSuffix(c, " sweep --provider claude --session \"$CLAUDE_SESSION_ID\"")
}

func filterManaged(event string, groups []any) ([]any, error) {
	kept := make([]any, 0, len(groups))
	for _, rawGroup := range groups {
		group, ok := rawGroup.(map[string]any)
		if !ok {
			kept = append(kept, rawGroup)
			continue
		}
		rawCommands, ok := group["hooks"]
		if !ok {
			kept = append(kept, rawGroup)
			continue
		}
		commands, ok := rawCommands.([]any)
		if !ok {
			return nil, fmt.Errorf("Claude settings hooks.%s[].hooks must be an array", event)
		}
		remaining := make([]any, 0, len(commands))
		for _, entry := range commands {
			if !isManagedCommand(entry) {
				remaining = append(remaining, entry)
			}
		}
		if len(remaining) > 0 {
			group["hooks"] = remaining
			kept = append(kept, group)
		}
	}
	return kept, nil
}

func writeSettings(path string, x map[string]any) error {
	b, err := json.MarshalIndent(x, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".settings.json.earwig-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
