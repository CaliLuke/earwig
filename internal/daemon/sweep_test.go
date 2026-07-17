package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/CaliLuke/earwig/internal/config"
	"github.com/CaliLuke/earwig/internal/spool"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeSweepReadsOnlyNewOrChangedSessions(t *testing.T) {
	dir := t.TempDir()
	mtimePath := filepath.Join(dir, "mtime")
	logPath := filepath.Join(dir, "reads")
	helperPath := filepath.Join(dir, "claude-helper")
	if err := os.WriteFile(mtimePath, []byte("100\n"), 0600); err != nil {
		t.Fatal(err)
	}
	rootJSON, _ := json.Marshal(dir)
	const sessionID = "0aaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	script := fmt.Sprintf(`#!/bin/sh
mtime=$(sed -n '1p' %q)
if [ "$1" = "list" ]; then
  printf '[{"id":"%s","cwd":%s,"last_modified":%%s}]\n' "$mtime"
  exit 0
fi
printf 'read\n' >> %q
printf '{"info":{"sessionId":"%s","cwd":%s,"createdAt":1,"lastModified":%%s},"messages":[]}\n' "$mtime"
`, mtimePath, sessionID, rootJSON, logPath, sessionID, rootJSON)
	if err := os.WriteFile(helperPath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	s, err := spool.Open(filepath.Join(dir, "spool.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sw := &Sweeper{Config: config.Config{WorkspaceRoots: []string{dir}, Claude: true, ClaudeHelper: helperPath}, Spool: s}

	for i := 0; i < 2; i++ {
		if err = sw.Sweep(context.Background(), SweepOptions{Provider: "claude"}); err != nil {
			t.Fatal(err)
		}
	}
	assertReadCount(t, logPath, 1)

	if err = os.WriteFile(mtimePath, []byte("101\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = sw.Sweep(context.Background(), SweepOptions{Provider: "claude"}); err != nil {
		t.Fatal(err)
	}
	assertReadCount(t, logPath, 2)

	// An explicit hook/session sweep bypasses mtime planning so an event cannot
	// be lost to provider timestamp granularity.
	if err = sw.Sweep(context.Background(), SweepOptions{Provider: "claude", Session: sessionID}); err != nil {
		t.Fatal(err)
	}
	assertReadCount(t, logPath, 3)
}

func assertReadCount(t *testing.T, path string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		b, err := os.ReadFile(path)
		if err == nil {
			got := len(strings.Fields(string(b)))
			if got == want {
				return
			}
			if got > want {
				t.Fatalf("read count = %d, want %d", got, want)
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("read count did not reach %d", want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
