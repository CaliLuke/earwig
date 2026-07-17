package normalizer

import "strings"

const compactPrefix = "This session is being continued from a previous conversation"
const interruptPrefix = "[Request interrupted"

func claudeBlocks(v any) []any {
	if s, ok := v.(string); ok {
		return []any{map[string]any{"type": "text", "text": s}}
	}
	return arr(v)
}
func claudeFirstText(bs []any) string {
	for _, b := range bs {
		m := obj(b)
		if str(m, "type") == "text" {
			return str(m, "text")
		}
	}
	return ""
}
func synthetic(s string) bool {
	s = strings.TrimLeft(s, " \t\n\r")
	for _, p := range []string{"<command-message>", "<command-name>", "<command-args>", "<local-command-stdout>", "<local-command-stderr>", "<task-notification>", "<system-reminder>", interruptPrefix} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}
func userContent(bs []any) []map[string]any {
	out := []map[string]any{}
	for _, b := range bs {
		m := obj(b)
		typ := str(m, "type")
		switch typ {
		case "text":
			out = append(out, map[string]any{"type": "text", "text": text(m["text"], maxTextBytes, "CLAUDE IMPORTER")})
		case "image":
			out = append(out, map[string]any{"type": "image", "source": "[BINARY PAYLOAD OMITTED]"})
		case "tool_result":
		default:
			out = append(out, map[string]any{"type": or(typ, "unknown")})
		}
	}
	return out
}
func or(a, b string) string {
	if a == "" {
		return b
	}
	return a
}
func final(messages []map[string]any, status string) any {
	if status != "completed" && status != "failed" {
		return nil
	}
	var last map[string]any
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i]["kind"] == "text" && messages[i]["text"] != "" {
			last = messages[i]
			break
		}
	}
	if last == nil {
		return nil
	}
	same := []string{}
	for _, m := range messages {
		if m["kind"] == "text" && m["id"] == last["id"] && m["text"] != "" {
			same = append(same, m["text"].(string))
		}
	}
	if len(same) > 1 {
		c := map[string]any{}
		for k, v := range last {
			c[k] = v
		}
		c["text"] = strings.Join(same, "\n")
		return c
	}
	return last
}

type claudeWork struct {
	id                        string
	started, ended            any
	users, assist, trajectory []map[string]any
	index                     map[string]int
	stop                      string
	interrupted               bool
}

func newClaudeTurn() *claudeWork {
	return &claudeWork{
		users:      []map[string]any{},
		assist:     []map[string]any{},
		trajectory: []map[string]any{},
		index:      map[string]int{},
	}
}
func (w *claudeWork) observe(e map[string]any) {
	if w.id == "" {
		w.id = str(e, "uuid")
	}
	if _, ok := e["timestamp"].(string); ok {
		if w.started == nil {
			w.started = e["timestamp"]
		}
		w.ended = e["timestamp"]
	}
}
func (w *claudeWork) has() bool { return len(w.users)+len(w.assist)+len(w.trajectory) > 0 }
func (w *claudeWork) done(last bool) Turn {
	status := "interrupted"
	if w.interrupted {
		status = "interrupted"
	} else if len(w.assist) == 0 && len(w.trajectory) == 0 {
		status = "in_flight"
	} else if w.stop == "end_turn" || w.stop == "stop_sequence" {
		status = "completed"
	} else if w.stop == "refusal" || w.stop == "max_tokens" || w.stop == "model_context_window_exceeded" {
		status = "failed"
	} else if last {
		status = "in_flight"
	}
	d := any(nil)
	if w.started != nil && w.ended != nil {
		d = parseMS(w.ended, 0) - parseMS(w.started, 0)
	}
	return Turn{ID: w.id, Status: status, StartedAt: w.started, CompletedAt: w.ended, DurationMS: d, UserMessages: w.users, AssistantMessages: w.assist, FinalAnswer: final(w.assist, status), Trajectory: w.trajectory, FollowingUserMessages: []map[string]any{}}
}

// NormalizeClaude maps raw getSessionInfo/getSessionMessages payloads, never on-disk files.
func NormalizeClaude(info map[string]any, messages []any) (Transcript, error) {
	if len(messages) > 50000 {
		return Transcript{}, fmtError("Claude session exceeds the 50000-message import limit")
	}
	var turns []*claudeWork
	compactions := []any{}
	var current *claudeWork
	model := ""
	sys := 0
	close := func() {
		if current != nil && current.has() {
			turns = append(turns, current)
		}
		current = nil
	}
	for _, raw := range messages {
		e := obj(raw)
		if e["parent_agent_id"] != nil {
			continue
		}
		typ := str(e, "type")
		if typ == "system" {
			sys++
			continue
		}
		if typ == "user" {
			bs := claudeBlocks(obj(e["message"])["content"])
			if current != nil {
				for _, b := range bs {
					bm := obj(b)
					if str(bm, "type") != "tool_result" {
						continue
					}
					if n, ok := current.index[str(bm, "tool_use_id")]; ok {
						x := current.trajectory[n]
						x["status"] = map[bool]string{true: "error", false: "completed"}[bm["is_error"] == true]
						content := bm["content"]
						if s, ok := content.(string); ok {
							x["result"] = text(s, 64*1024, "CLAUDE IMPORTER")
						} else if aa, ok := content.([]any); ok {
							ss := []string{}
							for _, q := range aa {
								qm := obj(q)
								if str(qm, "type") == "text" {
									ss = append(ss, str(qm, "text"))
								}
							}
							x["result"] = text(strings.Join(ss, "\n"), 64*1024, "CLAUDE IMPORTER")
						}
						x["duration_ms"] = parseMS(e["timestamp"], 0) - parseMS(x["requested_at"], 0)
					}
				}
			}
			hasInput := false
			for _, b := range bs {
				if str(obj(b), "type") != "tool_result" {
					hasInput = true
				}
			}
			if !hasInput {
				if current != nil {
					current.observe(e)
				}
				continue
			}
			txt := claudeFirstText(bs)
			if strings.HasPrefix(strings.TrimLeft(txt, " \t\n\r"), compactPrefix) {
				close()
				compactions = append(compactions, map[string]any{"id": valueOrNil(e, "uuid"), "timestamp": valueOrNil(e, "timestamp")})
				continue
			}
			intr := strings.HasPrefix(strings.TrimLeft(txt, " \t\n\r"), interruptPrefix)
			if current != nil && (len(current.assist) > 0 || len(current.trajectory) > 0) {
				if intr {
					current.interrupted = true
				}
				close()
			}
			if current == nil {
				current = newClaudeTurn()
			}
			current.observe(e)
			current.users = append(current.users, map[string]any{"id": valueOrNil(e, "uuid"), "synthetic": synthetic(txt), "content": userContent(bs)})
			continue
		}
		if typ == "assistant" {
			if current == nil {
				current = newClaudeTurn()
			}
			current.observe(e)
			msg := obj(e["message"])
			if m := str(msg, "model"); m != "" && m != "<synthetic>" {
				model = m
			}
			if s := str(msg, "stop_reason"); s != "" {
				current.stop = s
			}
			for _, b := range claudeBlocks(msg["content"]) {
				bm := obj(b)
				switch str(bm, "type") {
				case "thinking":
					current.assist = append(current.assist, map[string]any{"id": valueOrNil(e, "uuid"), "api_message_id": valueOrNil(msg, "id"), "kind": "thinking", "text": text(bm["thinking"], maxTextBytes, "CLAUDE IMPORTER")})
				case "text":
					current.assist = append(current.assist, map[string]any{"id": valueOrNil(e, "uuid"), "api_message_id": valueOrNil(msg, "id"), "kind": "text", "text": text(bm["text"], maxTextBytes, "CLAUDE IMPORTER")})
				case "tool_use":
					in := deep(bm["input"], 0, "CLAUDE IMPORTER")
					if str(bm, "name") == "Bash" {
						if im := obj(in); im != nil {
							im["command"] = RedactCommand(im["command"], "CLAUDE IMPORTER")
						}
					}
					x := map[string]any{"id": valueOrNil(bm, "id"), "type": "tool_use", "tool": or(str(bm, "name"), "unknown"), "status": "pending", "requested_at": valueOrNil(e, "timestamp"), "duration_ms": nil, "input": in, "result": nil}
					current.index[str(bm, "id")] = len(current.trajectory)
					current.trajectory = append(current.trajectory, x)
				}
			}
		}
	}
	close()
	out := make([]Turn, len(turns))
	for i, t := range turns {
		out[i] = t.done(i == len(turns)-1)
	}
	link(out)
	session := map[string]any{"id": valueOrNil(info, "sessionId"), "summary": text(info["summary"], 4*1024, "CLAUDE IMPORTER"), "cwd": valueOrNil(info, "cwd"), "git_branch": valueOrNil(info, "gitBranch"), "created_at": valueOrNil(info, "createdAt"), "last_modified": valueOrNil(info, "lastModified"), "model": nil, "compactions": compactions, "compaction_detection": "summary-prefix-best-effort", "opaque_system_markers": sys}
	if model != "" {
		session["model"] = model
	}
	return Transcript{1, "claude-code-agent-sdk", capture(), session, out}, nil
}
func valueOrNil(m map[string]any, k string) any {
	if v, ok := m[k]; ok {
		return v
	}
	return nil
}

type simpleError string

func (e simpleError) Error() string { return string(e) }
func fmtError(s string) error       { return simpleError(s) }
