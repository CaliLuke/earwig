package normalizer

import (
	"encoding/json"
	"strings"
)

type ompWork struct {
	id                        string
	started, ended            any
	users, assist, trajectory []map[string]any
	tools                     map[string]int
	stop, errorMessage        string
}

func newOMPWork() *ompWork {
	return &ompWork{
		users:      []map[string]any{},
		assist:     []map[string]any{},
		trajectory: []map[string]any{},
		tools:      map[string]int{},
	}
}

func (work *ompWork) observe(entry map[string]any) {
	if work.id == "" {
		work.id = str(entry, "id")
	}
	if timestamp, ok := entry["timestamp"].(string); ok {
		if work.started == nil {
			work.started = timestamp
		}
		work.ended = timestamp
	}
}

func (work *ompWork) hasResponse() bool {
	return len(work.assist)+len(work.trajectory) > 0
}

func (work *ompWork) done(last bool) Turn {
	status := "interrupted"
	switch work.stop {
	case "stop":
		status = "completed"
	case "length", "contentFilter", "error":
		status = "failed"
	default:
		if work.errorMessage != "" {
			status = "failed"
		} else if last {
			status = "in_flight"
		}
	}
	var errorValue *any
	if work.errorMessage != "" {
		var value any = work.errorMessage
		errorValue = &value
	}
	return makeTurn(work.id, status, work.started, work.ended, work.users, work.assist, work.trajectory, errorValue)
}

func ompActiveEntries(entries []any) []map[string]any {
	byID := make(map[string]map[string]any, len(entries))
	leafID := ""
	for _, value := range entries {
		entry := obj(value)
		if id := str(entry, "id"); id != "" {
			byID[id] = entry
			leafID = id
		}
	}
	path := make([]map[string]any, 0, len(byID))
	seen := make(map[string]bool, len(byID))
	for leafID != "" && !seen[leafID] {
		entry := byID[leafID]
		if entry == nil {
			break
		}
		seen[leafID] = true
		path = append(path, entry)
		leafID = str(entry, "parentId")
	}
	for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
		path[left], path[right] = path[right], path[left]
	}
	return path
}

func ompUserContent(blocks []any) []map[string]any {
	content := []map[string]any{}
	for _, value := range blocks {
		block := obj(value)
		switch typ := str(block, "type"); typ {
		case "text":
			content = append(content, map[string]any{"type": "text", "text": text(block["text"], maxTextBytes, "OMP IMPORTER")})
		case "image":
			content = append(content, map[string]any{"type": "image", "mime_type": valueOrNil(block, "mimeType"), "source": "[BINARY PAYLOAD OMITTED]"})
		default:
			content = append(content, map[string]any{"type": orUnknown(typ)})
		}
	}
	return content
}

func ompToolInput(name string, value any) any {
	decoded := value
	if encoded, ok := value.(string); ok {
		if err := json.Unmarshal([]byte(encoded), &decoded); err != nil {
			return text(encoded, 64*1024, "OMP IMPORTER")
		}
	}
	decoded = deep(decoded, 0, "OMP IMPORTER")
	if name == "bash" {
		if input := obj(decoded); input != nil {
			input["command"] = RedactCommand(input["command"], "OMP IMPORTER")
		}
	}
	return decoded
}

func ompToolResult(blocks []any) string {
	parts := []string{}
	for _, value := range blocks {
		block := obj(value)
		switch str(block, "type") {
		case "text":
			parts = append(parts, str(block, "text"))
		case "image":
			parts = append(parts, "[BINARY PAYLOAD OMITTED]")
		}
	}
	return text(strings.Join(parts, "\n"), 64*1024, "OMP IMPORTER")
}

// NormalizeOMP maps the documented OMP session JSONL entry model.
func NormalizeOMP(header map[string]any, entries []any, lastModified int64) (Transcript, error) {
	var turns []*ompWork
	var current *ompWork
	compactions := []any{}
	model := ""
	modelProvider := ""
	closeTurn := func() {
		if current != nil && (len(current.users) > 0 || current.hasResponse()) {
			turns = append(turns, current)
		}
		current = nil
	}

	for _, entry := range ompActiveEntries(entries) {
		switch str(entry, "type") {
		case "model_change":
			if changed := str(entry, "model"); changed != "" {
				model = changed
			}
		case "compaction":
			compactions = append(compactions, deep(entry, 0, "OMP IMPORTER"))
		case "custom_message":
			if current == nil {
				continue
			}
			current.observe(entry)
			current.trajectory = append(current.trajectory, map[string]any{
				"id":          valueOrNil(entry, "id"),
				"type":        "custom_message",
				"custom_type": valueOrNil(entry, "customType"),
				"content":     text(entry["content"], 64*1024, "OMP IMPORTER"),
				"display":     valueOrNil(entry, "display"),
			})
		case "message":
			message := obj(entry["message"])
			switch str(message, "role") {
			case "user":
				if current != nil && current.hasResponse() {
					closeTurn()
				}
				if current == nil {
					current = newOMPWork()
				}
				current.observe(entry)
				current.users = append(current.users, map[string]any{
					"id":      valueOrNil(entry, "id"),
					"content": ompUserContent(arr(message["content"])),
				})
			case "assistant":
				if current == nil {
					current = newOMPWork()
				}
				current.observe(entry)
				if value := str(message, "model"); value != "" {
					model = value
				}
				if value := str(message, "provider"); value != "" {
					modelProvider = value
				}
				if value := str(message, "stopReason"); value != "" {
					current.stop = value
				}
				if value := str(message, "errorMessage"); value != "" {
					current.errorMessage = value
				}
				for _, value := range arr(message["content"]) {
					block := obj(value)
					switch str(block, "type") {
					case "thinking":
						current.assist = append(current.assist, map[string]any{"id": valueOrNil(entry, "id"), "kind": "thinking", "text": text(block["thinking"], maxTextBytes, "OMP IMPORTER"), "provider": valueOrNil(message, "provider"), "model": valueOrNil(message, "model")})
					case "text":
						current.assist = append(current.assist, map[string]any{"id": valueOrNil(entry, "id"), "kind": "text", "text": text(block["text"], maxTextBytes, "OMP IMPORTER"), "provider": valueOrNil(message, "provider"), "model": valueOrNil(message, "model")})
					case "toolCall":
						callID := str(block, "id")
						tool := str(block, "name")
						trajectory := map[string]any{"id": valueOrNil(block, "id"), "type": "tool_use", "tool": orUnknown(tool), "status": "pending", "requested_at": valueOrNil(entry, "timestamp"), "duration_ms": nil, "input": ompToolInput(tool, block["arguments"]), "result": nil}
						current.tools[callID] = len(current.trajectory)
						current.trajectory = append(current.trajectory, trajectory)
					}
				}
			case "toolResult":
				if current == nil {
					continue
				}
				current.observe(entry)
				callID := str(message, "toolCallId")
				if index, ok := current.tools[callID]; ok {
					trajectory := current.trajectory[index]
					if message["isError"] == true {
						trajectory["status"] = "error"
					} else {
						trajectory["status"] = "completed"
					}
					trajectory["result"] = ompToolResult(arr(message["content"]))
					trajectory["duration_ms"] = parseMS(entry["timestamp"], 0) - parseMS(trajectory["requested_at"], 0)
				}
			}
		}
	}
	closeTurn()

	out := make([]Turn, len(turns))
	for index, turn := range turns {
		out[index] = turn.done(index == len(turns)-1)
	}
	link(out)
	session := map[string]any{
		"id":                     valueOrNil(header, "id"),
		"name":                   text(header["title"], 4*1024, "OMP IMPORTER"),
		"cwd":                    valueOrNil(header, "cwd"),
		"created_at":             valueOrNil(header, "timestamp"),
		"last_modified":          lastModified,
		"version":                valueOrNil(header, "version"),
		"additional_directories": deep(header["additionalDirectories"], 0, "OMP IMPORTER"),
		"parent_session":         valueOrNil(header, "parentSession"),
		"model":                  nil,
		"model_provider":         nil,
		"compactions":            compactions,
	}
	if model != "" {
		session["model"] = model
	}
	if modelProvider != "" {
		session["model_provider"] = modelProvider
	}
	return Transcript{1, "omp-session-file", capture(), session, out}, nil
}
