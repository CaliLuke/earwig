// Package normalizer implements Earwig's provider-independent transcript contract.
package normalizer

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const maxTextBytes = 512 * 1024

var secretAssignment = regexp.MustCompile(`(?i)\b([A-Za-z0-9_]*(?:password|passwd|secret|token|cookie|authorization|api[_-]?key|credential)[A-Za-z0-9_]*)=(?:'[^']*'|"[^"]*"|\S+)`)
var authorization = regexp.MustCompile(`(?i)(authorization\s*:\s*(?:bearer|basic)\s+)\S+`)

// Go's RE2 intentionally has no backreferences; preserving the surrounding
// quote is not material to the capture contract, while redacting the value is.
var headerSecret = regexp.MustCompile(`(?i)((?:-H|--header)[= ]\s*["']?[A-Za-z0-9-]*(?:key|token|auth|secret|cookie)[A-Za-z0-9-]*\s*:\s*)[^"'\n]+`)
var flagSecret = regexp.MustCompile(`(?i)(--?(?:password|passwd|token|secret|api-?key|access-?key|auth)(?:[= ]))\s*(?:'[^']*'|"[^"]*"|\S+)`)

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

func capture() Capture { return Capture{"full", Redactions{true, "omitted"}} }
func text(v any, limit int, marker string) string {
	s, _ := v.(string)
	if limit <= 0 {
		limit = maxTextBytes
	}
	if len([]byte(s)) <= limit {
		return s
	}
	b := []byte(s)
	return string(b[:limit]) + "\n[TRUNCATED BY " + marker + "]"
}
func RedactCommand(v any, marker string) string {
	s := text(v, 64*1024, marker)
	s = secretAssignment.ReplaceAllString(s, "$1=[REDACTED]")
	s = authorization.ReplaceAllString(s, "$1[REDACTED]")
	s = headerSecret.ReplaceAllString(s, "$1[REDACTED]")
	return flagSecret.ReplaceAllString(s, "$1[REDACTED]")
}
func deep(v any, depth int, marker string) any {
	if depth >= 8 {
		switch v.(type) {
		case map[string]any, []any:
			return "[DEPTH LIMIT REACHED]"
		}
	}
	switch x := v.(type) {
	case string:
		return text(x, 64*1024, marker)
	case []any:
		o := make([]any, len(x))
		for i := range x {
			o[i] = deep(x[i], depth+1, marker)
		}
		return o
	case map[string]any:
		o := map[string]any{}
		for k, v := range x {
			o[k] = deep(v, depth+1, marker)
		}
		return o
	default:
		if v == nil {
			return nil
		}
		return v
	}
}
func parseMS(v any, fallback int64) int64 {
	switch x := v.(type) {
	case string:
		t, e := time.Parse(time.RFC3339Nano, x)
		if e == nil {
			return t.UnixMilli()
		}
	case float64:
		return int64(x)
	case int64:
		return x
	case int:
		return int64(x)
	}
	return fallback
}
func str(m map[string]any, k string) string { x, _ := m[k].(string); return x }
func arr(v any) []any                       { x, _ := v.([]any); return x }
func obj(v any) map[string]any              { x, _ := v.(map[string]any); return x }

// DeterministicUUIDv7 exactly follows Auto-K's hash and UUIDv7 bit layout.
func DeterministicUUIDv7(timestampMS int64, parts ...string) string {
	h := sha256.Sum256([]byte(strings.Join(parts, "\x1f")))
	ms := uint64(timestampMS)
	if timestampMS < 0 {
		ms = 0
	}
	ms &= 0xffffffffffff
	ra := binary.BigEndian.Uint16(h[0:2]) & 0x0fff
	rb := binary.BigEndian.Uint64(h[2:10]) & 0x3fffffffffffffff
	hi := (ms << 16) | (0x7 << 12) | uint64(ra)
	lo := (uint64(0x2) << 62) | rb
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", hi>>32, (hi>>16)&0xffff, hi&0xffff, lo>>48, (lo & 0xffffffffffff))
}
func TraceID(provider, sessionID string, t Turn, fallback int64) string {
	return DeterministicUUIDv7(parseMS(t.StartedAt, fallback), provider, sessionID, t.ID)
}
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
