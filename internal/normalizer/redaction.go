package normalizer

import "regexp"

const maxTextBytes = 512 * 1024

var secretAssignment = regexp.MustCompile(`(?i)\b([A-Za-z0-9_]*(?:password|passwd|secret|token|cookie|authorization|api[_-]?key|credential)[A-Za-z0-9_]*)=(?:'[^']*'|"[^"]*"|\S+)`)
var authorization = regexp.MustCompile(`(?i)(authorization\s*:\s*(?:bearer|basic)\s+)\S+`)

// Go's RE2 intentionally has no backreferences; preserving the surrounding
// quote is not material to the capture contract, while redacting the value is.
var headerSecret = regexp.MustCompile(`(?i)((?:-H|--header)[= ]\s*["']?[A-Za-z0-9-]*(?:key|token|auth|secret|cookie)[A-Za-z0-9-]*\s*:\s*)[^"'\n]+`)
var flagSecret = regexp.MustCompile(`(?i)(--?(?:password|passwd|token|secret|api-?key|access-?key|auth)(?:[= ]))\s*(?:'[^']*'|"[^"]*"|\S+)`)

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
