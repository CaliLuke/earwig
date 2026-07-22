package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
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
