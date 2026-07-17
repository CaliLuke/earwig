package normalizer

func codexUserContent(a []any) []map[string]any {
	o := []map[string]any{}
	for _, v := range a {
		m := obj(v)
		typ := str(m, "type")
		switch typ {
		case "text":
			o = append(o, map[string]any{"type": "text", "text": text(m["text"], maxTextBytes, "CODEX IMPORTER")})
		case "image":
			o = append(o, map[string]any{"type": "image", "detail": valueOrNil(m, "detail"), "source": "[BINARY PAYLOAD OMITTED]"})
		case "localImage":
			o = append(o, map[string]any{"type": "localImage", "detail": valueOrNil(m, "detail"), "path": valueOrNil(m, "path")})
		case "skill", "mention":
			o = append(o, map[string]any{"type": typ, "name": valueOrNil(m, "name"), "path": valueOrNil(m, "path")})
		default:
			o = append(o, map[string]any{"type": or(typ, "unknown")})
		}
	}
	return o
}
func codexTrajectory(m map[string]any) map[string]any {
	base := map[string]any{"id": valueOrNil(m, "id"), "type": or(str(m, "type"), "unknown")}
	switch str(m, "type") {
	case "commandExecution":
		base["status"] = valueOrNil(m, "status")
		base["command"] = RedactCommand(m["command"], "CODEX IMPORTER")
		base["cwd"] = valueOrNil(m, "cwd")
		base["duration_ms"] = valueOrNil(m, "durationMs")
		base["exit_code"] = valueOrNil(m, "exitCode")
		base["output"] = text(m["aggregatedOutput"], 64*1024, "CODEX IMPORTER")
	case "fileChange":
		base["status"] = valueOrNil(m, "status")
		base["changes"] = deep(m["changes"], 0, "CODEX IMPORTER")
	case "mcpToolCall":
		for k, from := range map[string]string{"status": "status", "server": "server", "tool": "tool", "duration_ms": "durationMs", "arguments": "arguments", "result": "result", "error": "error"} {
			if from == "arguments" || from == "result" || from == "error" {
				base[k] = deep(m[from], 0, "CODEX IMPORTER")
			} else {
				base[k] = valueOrNil(m, from)
			}
		}
	case "dynamicToolCall":
		for k, from := range map[string]string{"status": "status", "namespace": "namespace", "tool": "tool", "success": "success", "duration_ms": "durationMs"} {
			base[k] = valueOrNil(m, from)
		}
		base["arguments"] = deep(m["arguments"], 0, "CODEX IMPORTER")
	case "collabAgentToolCall":
		base["status"] = valueOrNil(m, "status")
		base["tool"] = valueOrNil(m, "tool")
		base["model"] = valueOrNil(m, "model")
		base["receiver_thread_ids"] = valueOrNil(m, "receiverThreadIds")
		base["prompt"] = text(m["prompt"], 64*1024, "CODEX IMPORTER")
	case "subAgentActivity":
		base["kind"] = valueOrNil(m, "kind")
		base["agent_thread_id"] = valueOrNil(m, "agentThreadId")
	case "webSearch":
		base["query"] = text(m["query"], 16*1024, "CODEX IMPORTER")
	case "plan":
		base["text"] = text(m["text"], 64*1024, "CODEX IMPORTER")
	case "imageView":
		base["path"] = valueOrNil(m, "path")
	case "imageGeneration":
		base["status"] = valueOrNil(m, "status")
		base["saved_path"] = valueOrNil(m, "savedPath")
	case "contextCompaction", "enteredReviewMode", "exitedReviewMode", "sleep":
		base["status"] = valueOrNil(m, "status")
	default:
		return obj(deep(m, 0, "CODEX IMPORTER"))
	}
	return base
}

// NormalizeCodex maps a thread/read result from codex app-server.
func NormalizeCodex(thread map[string]any) (Transcript, error) {
	turns := []Turn{}
	for _, raw := range arr(thread["turns"]) {
		t := obj(raw)
		users := []map[string]any{}
		assist := []map[string]any{}
		traj := []map[string]any{}
		for _, item := range arr(t["items"]) {
			m := obj(item)
			switch str(m, "type") {
			case "userMessage":
				users = append(users, map[string]any{"id": valueOrNil(m, "id"), "content": codexUserContent(arr(m["content"]))})
			case "agentMessage":
				assist = append(assist, map[string]any{"id": valueOrNil(m, "id"), "phase": valueOrNil(m, "phase"), "text": text(m["text"], maxTextBytes, "CODEX IMPORTER")})
			default:
				traj = append(traj, codexTrajectory(m))
			}
		}
		var f any
		for i := len(assist) - 1; i >= 0; i-- {
			if assist[i]["phase"] == "final_answer" {
				f = assist[i]
				break
			}
		}
		if f == nil && str(t, "status") == "completed" && len(assist) > 0 {
			f = assist[len(assist)-1]
		}
		var errorValue any
		if _, ok := t["error"]; ok {
			errorValue = deep(t["error"], 0, "CODEX IMPORTER")
		}
		turns = append(turns, Turn{ID: str(t, "id"), Status: str(t, "status"), StartedAt: valueOrNil(t, "startedAt"), CompletedAt: valueOrNil(t, "completedAt"), DurationMS: valueOrNil(t, "durationMs"), UserMessages: users, AssistantMessages: assist, FinalAnswer: f, Trajectory: traj, FollowingUserMessages: []map[string]any{}, Error: &errorValue})
		/*func() any {
			if _, ok := t["error"]; ok {
				return deep(t["error"], 0, "CODEX IMPORTER")
			}
			return nil
		}()*/
	}
	link(turns)
	session := map[string]any{"id": valueOrNil(thread, "id"), "session_id": valueOrNil(thread, "sessionId"), "name": valueOrNil(thread, "name"), "preview": text(thread["preview"], 4*1024, "CODEX IMPORTER"), "cwd": valueOrNil(thread, "cwd"), "source": valueOrNil(thread, "source"), "model_provider": valueOrNil(thread, "modelProvider"), "cli_version": valueOrNil(thread, "cliVersion"), "status": valueOrNil(thread, "status"), "created_at": valueOrNil(thread, "createdAt"), "updated_at": valueOrNil(thread, "updatedAt"), "forked_from_id": valueOrNil(thread, "forkedFromId"), "parent_thread_id": valueOrNil(thread, "parentThreadId")}
	return Transcript{1, "codex-app-server", capture(), session, turns}, nil
}
