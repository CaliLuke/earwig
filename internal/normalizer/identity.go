package normalizer

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

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

// DeterministicUUIDv7 is Earwig's stable hash and UUIDv7 identity contract.
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
