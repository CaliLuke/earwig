package provider

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestClaudeReaderCancellationKillsHelperProcessGroup(t *testing.T) {
	helper := filepath.Join(t.TempDir(), "hanging-helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nsleep 10\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := (ClaudeReader{Helper: helper}).List(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("List error = %v, want context deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("helper process group survived cancellation for %v", elapsed)
	}
}
