package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CaliLuke/earwig/internal/hooks"
	"github.com/CaliLuke/earwig/internal/spool"
)

func TestHookCapturesAndExportsWithoutListingSessions(t *testing.T) {
	for _, event := range []string{"PreCompact", "Stop", "SessionEnd"} {
		t.Run(event, func(t *testing.T) {
			dir := t.TempDir()
			fixture, err := filepath.Abs("../../testdata/fixtures/claude.json")
			if err != nil {
				t.Fatal(err)
			}
			helper := filepath.Join(dir, "reader")
			const id = "0aaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
			// Fail if capture enumerates sessions or drops the supplied workspace.
			script := fmt.Sprintf("#!/bin/sh\n[ \"$#\" = 4 ] && [ \"$1\" = read ] && [ \"$2\" = %s ] && [ \"$3\" = --dir ] && [ \"$4\" = /tmp/earwig ] || exit 7\ncat %q\n", id, fixture)
			if err = os.WriteFile(helper, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(dir, "config.toml")
			cfg := fmt.Sprintf("workspace_roots = [\"/tmp/earwig\"]\nclaude = true\ncodex = false\nomp = false\nclaude_helper = %q\nspool_path = %q\njsondir = %q\n", helper, filepath.Join(dir, "spool.sqlite"), filepath.Join(dir, "json"))
			if err = os.WriteFile(configPath, []byte(cfg), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("EARWIG_CONFIG", configPath)
			input, _ := json.Marshal(map[string]string{"session_id": id, "cwd": "/tmp/earwig", "hook_event_name": event})
			var stderr bytes.Buffer
			runHook([]string{"claude", hooks.ManagedArgument}, bytes.NewReader(input), &stderr)
			if stderr.Len() != 0 {
				t.Fatalf("hook failed: %s", &stderr)
			}
			store, err := spool.Open(filepath.Join(dir, "spool.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			total, _, err := store.Stats()
			if err != nil || total != 2 {
				t.Fatalf("captured turns = %d, %v", total, err)
			}
			exported, err := store.ExportCount("jsondir")
			if err != nil || exported != 2 {
				t.Fatalf("synchronous exports = %d, %v", exported, err)
			}
		})
	}
}

func TestHookRejectsRelativeWorkspace(t *testing.T) {
	// Invalid input must be rejected before configuration or provider access.
	t.Setenv("EARWIG_CONFIG", t.TempDir())
	var stderr bytes.Buffer
	runHook([]string{"claude", hooks.ManagedArgument}, strings.NewReader(`{"session_id":"0aaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee","cwd":"relative"}`), &stderr)
	if !strings.Contains(stderr.String(), "cwd must be absolute") {
		t.Fatalf("invalid workspace accepted: %s", &stderr)
	}
}
