// Package normalizer implements Earwig's provider-independent transcript contract.
package normalizer

type Transcript struct {
	SchemaVersion int            `json:"schema_version"`
	Source        string         `json:"source"`
	Capture       Capture        `json:"capture"`
	Session       map[string]any `json:"session"`
	Turns         []Turn         `json:"turns"`
}

type Capture struct {
	Fidelity   string     `json:"fidelity"`
	Redactions Redactions `json:"redactions"`
}

type Redactions struct {
	CommandSecretPatterns bool   `json:"command_secret_patterns"`
	BinaryPayloads        string `json:"binary_payloads"`
}

type Turn struct {
	ID                    string           `json:"id"`
	Status                string           `json:"status"`
	StartedAt             any              `json:"started_at"`
	CompletedAt           any              `json:"completed_at"`
	DurationMS            any              `json:"duration_ms"`
	UserMessages          []map[string]any `json:"user_messages"`
	AssistantMessages     []map[string]any `json:"assistant_messages"`
	FinalAnswer           any              `json:"final_answer"`
	Trajectory            []map[string]any `json:"trajectory"`
	FollowingUserMessages []map[string]any `json:"following_user_messages"`
	Error                 *any             `json:"error,omitempty"`
}

func str(m map[string]any, k string) string { x, _ := m[k].(string); return x }
func arr(v any) []any                       { x, _ := v.([]any); return x }
func obj(v any) map[string]any              { x, _ := v.(map[string]any); return x }

func link(turns []Turn) {
	for i := range turns {
		if i+1 >= len(turns) {
			continue
		}
		for _, m := range turns[i+1].UserMessages {
			if m["synthetic"] == true {
				continue
			}
			turns[i].FollowingUserMessages = append(turns[i].FollowingUserMessages, map[string]any{"id": m["id"], "content": m["content"]})
		}
	}
}
