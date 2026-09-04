package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestCodexListDiscoversAllDirectoriesWithPagination(t *testing.T) {
	client := &CodexClient{
		in:        make(chan []byte),
		responses: map[float64]chan map[string]any{},
		next:      1,
	}
	requests := make(chan map[string]any, 2)
	serverErrors := make(chan error, 1)
	go func() {
		defer close(requests)
		for page := 0; page < 2; page++ {
			body := <-client.in
			var request map[string]any
			if err := json.Unmarshal(body, &request); err != nil {
				serverErrors <- err
				return
			}
			requests <- request
			id, _ := request["id"].(float64)
			result := map[string]any{
				"data": []any{map[string]any{"id": fmt.Sprintf("thread-%d", page)}},
			}
			if page == 0 {
				result["nextCursor"] = "page-2"
			}
			client.mu.Lock()
			response := client.responses[id]
			client.mu.Unlock()
			response <- map[string]any{"result": result}
		}
	}()

	items, err := client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("listed %d threads, want 2", len(items))
	}
	page := 0
	for request := range requests {
		if request["method"] != "thread/list" {
			t.Fatalf("method = %#v, want thread/list", request["method"])
		}
		params, _ := request["params"].(map[string]any)
		if _, found := params["cwd"]; found {
			t.Fatalf("thread/list used exact cwd filter: %#v", params)
		}
		cursor, hasCursor := params["cursor"]
		if page == 0 && hasCursor {
			t.Fatalf("first page unexpectedly used cursor: %#v", params)
		}
		if page == 1 && cursor != "page-2" {
			t.Fatalf("second page cursor = %#v, want page-2", cursor)
		}
		page++
	}
	if page != 2 {
		t.Fatalf("sent %d thread/list requests, want 2", page)
	}
	select {
	case err = <-serverErrors:
		t.Fatal(err)
	default:
	}
}

func TestCodexReadPaginatesTurnsAndItems(t *testing.T) {
	const threadID = "019fb35e-cd1f-72c1-b95f-f265043655ec"
	client := &CodexClient{
		in:        make(chan []byte),
		responses: map[float64]chan map[string]any{},
		next:      1,
	}
	requests := make(chan map[string]any, 5)
	results := []map[string]any{
		{"thread": map[string]any{"id": threadID, "turns": []any{}}},
		{"data": []any{map[string]any{"id": "turn-1", "status": "completed", "items": []any{}}}, "nextCursor": "turn-page-2"},
		{"data": []any{map[string]any{"id": "turn-2", "status": "completed", "items": []any{}}}},
		{"data": []any{
			map[string]any{"turnId": "turn-1", "item": map[string]any{"id": "item-1"}},
			map[string]any{"turnId": "turn-2", "item": map[string]any{"id": "item-2"}},
		}, "nextCursor": "item-page-2"},
		{"data": []any{map[string]any{"turnId": "turn-1", "item": map[string]any{"id": "item-3"}}}},
	}
	go func() {
		defer close(requests)
		for _, result := range results {
			body := <-client.in
			var request map[string]any
			if err := json.Unmarshal(body, &request); err != nil {
				return
			}
			requests <- request
			id, _ := request["id"].(float64)
			client.mu.Lock()
			response := client.responses[id]
			client.mu.Unlock()
			response <- map[string]any{"result": result}
		}
	}()

	thread, err := client.Read(context.Background(), threadID)
	if err != nil {
		t.Fatal(err)
	}
	turns, _ := thread["turns"].([]any)
	if len(turns) != 2 {
		t.Fatalf("turn count = %d, want 2", len(turns))
	}
	wantItemIDs := [][]string{{"item-1", "item-3"}, {"item-2"}}
	for index, rawTurn := range turns {
		turn, _ := rawTurn.(map[string]any)
		items, _ := turn["items"].([]any)
		var itemIDs []string
		for _, rawItem := range items {
			item, _ := rawItem.(map[string]any)
			itemID, _ := item["id"].(string)
			itemIDs = append(itemIDs, itemID)
		}
		if fmt.Sprint(itemIDs) != fmt.Sprint(wantItemIDs[index]) {
			t.Fatalf("turn %d item IDs = %v, want %v", index, itemIDs, wantItemIDs[index])
		}
	}

	var sent []map[string]any
	for request := range requests {
		sent = append(sent, request)
	}
	wantMethods := []string{"thread/read", "thread/turns/list", "thread/turns/list", "thread/items/list", "thread/items/list"}
	if len(sent) != len(wantMethods) {
		t.Fatalf("request count = %d, want %d", len(sent), len(wantMethods))
	}
	for index, want := range wantMethods {
		if sent[index]["method"] != want {
			t.Fatalf("request %d method = %v, want %s", index, sent[index]["method"], want)
		}
	}
	readParams, _ := sent[0]["params"].(map[string]any)
	if readParams["includeTurns"] != false {
		t.Fatalf("thread/read includeTurns = %v, want false", readParams["includeTurns"])
	}
	turnPageOne, _ := sent[1]["params"].(map[string]any)
	turnPageTwo, _ := sent[2]["params"].(map[string]any)
	itemPageOne, _ := sent[3]["params"].(map[string]any)
	itemPageTwo, _ := sent[4]["params"].(map[string]any)
	if turnPageOne["itemsView"] != "notLoaded" || turnPageOne["sortDirection"] != "asc" || turnPageOne["cursor"] != nil {
		t.Fatalf("first turn page params = %#v", turnPageOne)
	}
	if turnPageTwo["cursor"] != "turn-page-2" {
		t.Fatalf("second turn page cursor = %v", turnPageTwo["cursor"])
	}
	if itemPageOne["sortDirection"] != "asc" || itemPageOne["cursor"] != nil {
		t.Fatalf("first item page params = %#v", itemPageOne)
	}
	if itemPageTwo["cursor"] != "item-page-2" {
		t.Fatalf("second item page cursor = %v", itemPageTwo["cursor"])
	}
}

func TestCodexRapidChildrenAreWaitedExactlyOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	installCodexTestHelper(t)

	const childCount = 1000
	workingDirectory := t.TempDir()
	for index := 0; index < childCount; index++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		client, err := StartCodex(ctx, workingDirectory)
		if err != nil {
			cancel()
			t.Fatalf("start child %d: %v", index, err)
		}
		command := client.cmd
		client.Close()
		cancel()
		if command.ProcessState == nil {
			t.Fatalf("child %d was terminated but not waited for", index)
		}
	}
}

func TestCodexCancellationWaitsForRunningChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	installCodexTestHelper(t)

	ctx, cancel := context.WithCancel(context.Background())
	client, err := StartCodex(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	client.Close()
	if client.cmd.ProcessState == nil {
		t.Fatal("cancelled child was not waited for")
	}
}

func TestCodexConcurrentCloseAndChildCompletionWaitOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	installCodexTestHelper(t)
	t.Setenv("EARWIG_CODEX_TEST_MODE", "natural-exit")
	client, err := StartCodex(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond)

	var closes sync.WaitGroup
	closes.Add(20)
	for index := 0; index < 20; index++ {
		go func() {
			defer closes.Done()
			client.Close()
		}()
	}
	closes.Wait()
	if client.cmd.ProcessState == nil {
		t.Fatal("naturally exiting child was not waited for")
	}
}

func TestCodexStartupFailureDoesNotCreateAChild(t *testing.T) {
	emptyPath := t.TempDir()
	t.Setenv("PATH", emptyPath)
	client, err := StartCodex(context.Background(), t.TempDir())
	if err == nil || client != nil {
		t.Fatalf("StartCodex = (%v, %v), want executable lookup failure", client, err)
	}
	if !strings.Contains(err.Error(), "executable file not found") {
		t.Fatalf("startup error = %v", err)
	}
}

func TestCodexInitializationFailureReapsStartedChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper is a POSIX shell script")
	}
	installCodexTestHelper(t)
	pidPath := filepath.Join(t.TempDir(), "child.pid")
	t.Setenv("EARWIG_CODEX_TEST_MODE", "crash")
	t.Setenv("EARWIG_CODEX_TEST_PID", pidPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client, err := StartCodex(ctx, t.TempDir())
	if err == nil || client != nil {
		t.Fatalf("StartCodex = (%v, %v), want initialization failure", client, err)
	}
	body, readErr := os.ReadFile(pidPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(body)))
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	if syscall.Kill(pid, 0) == nil {
		t.Fatalf("failed-start child PID %d was not reaped", pid)
	}
}

func installCodexTestHelper(t *testing.T) {
	t.Helper()
	directory := t.TempDir()
	helper := filepath.Join(directory, "codex")
	body := `#!/bin/sh
if [ "$EARWIG_CODEX_TEST_MODE" = "crash" ]; then
  printf '%s\n' "$$" > "$EARWIG_CODEX_TEST_PID"
  exit 17
fi
IFS= read -r request || exit 2
printf '{"id":1,"result":{}}\n'
if [ "$EARWIG_CODEX_TEST_MODE" = "natural-exit" ]; then
  exec sleep 0.05
fi
while IFS= read -r request; do :; done
`
	if err := os.WriteFile(helper, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
}
