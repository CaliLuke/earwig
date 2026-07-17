package daemon

import (
	"context"
	"fmt"
	"github.com/CaliLuke/earwig/internal/config"
	"github.com/CaliLuke/earwig/internal/exporter"
	"github.com/CaliLuke/earwig/internal/normalizer"
	"github.com/CaliLuke/earwig/internal/provider"
	"github.com/CaliLuke/earwig/internal/spool"
	"log"
	"time"
)

type SweepOptions struct{ Session, Provider string }
type Sweeper struct {
	Config config.Config
	Spool  *spool.Spool
	Log    *log.Logger
}

func (s *Sweeper) note(k string, v any) { _ = s.Spool.SetHealth(k, v) }
func (s *Sweeper) Sweep(ctx context.Context, o SweepOptions) error {
	now := time.Now()
	s.note("last_sweep_started", now)
	var first error
	if s.Config.Claude && (o.Provider == "" || o.Provider == "claude") {
		if e := s.claude(ctx, o, now); e != nil {
			first = e
			s.note("claude_error", e.Error())
		}
	}
	if s.Config.Codex && (o.Provider == "" || o.Provider == "codex") {
		if e := s.codex(ctx, o, now); e != nil {
			if first == nil {
				first = e
			}
			s.note("codex_error", e.Error())
		}
	}
	for _, e := range s.exporters() {
		if err := exporter.Drain(s.Spool, e); err != nil {
			s.note("exporter_"+e.Name()+"_behind", err.Error())
			if s.Log != nil {
				s.Log.Printf("exporter %s behind: %v", e.Name(), err)
			}
		} else {
			s.note("exporter_"+e.Name()+"_behind", nil)
		}
	}
	s.note("last_sweep_success", time.Now())
	return first
}
func (s *Sweeper) exporters() []exporter.Exporter {
	x := []exporter.Exporter{}
	if s.Config.JSONDir != "" {
		x = append(x, exporter.JSONDir{Dir: s.Config.JSONDir})
	}
	if s.Config.OpikURL != "" {
		x = append(x, exporter.Opik{URL: s.Config.OpikURL})
	}
	return x
}
func (s *Sweeper) claude(ctx context.Context, o SweepOptions, now time.Time) error {
	r := provider.ClaudeReader{Helper: s.Config.ClaudeHelper}
	for _, root := range s.Config.WorkspaceRoots {
		list, e := r.List(ctx, root)
		if e != nil {
			return e
		}
		for _, v := range list {
			if o.Session != "" && v.ID != o.Session {
				continue
			}
			if !config.Allowed(s.Config, v.CWD) {
				continue
			}
			info, msg, e := r.Read(ctx, v.ID, v.CWD)
			if e != nil {
				continue
			}
			t, e := normalizer.NormalizeClaude(info, msg)
			if e != nil {
				return e
			}
			gapped := s.gap(t)
			if _, e = s.Spool.UpsertTranscript(t, now); e != nil {
				return e
			}
			if gapped {
				_, _ = s.Spool.DB.Exec(`UPDATE sessions SET gap_warned=1 WHERE provider=? AND session_id=?`, t.Source, t.Session["id"])
			}
		}
	}
	return nil
}
func (s *Sweeper) codex(ctx context.Context, o SweepOptions, now time.Time) error {
	for _, root := range s.Config.WorkspaceRoots {
		c, e := provider.StartCodex(ctx, root)
		if e != nil {
			return e
		}
		list, e := c.List(ctx, root)
		if e != nil {
			c.Close()
			return e
		}
		for _, item := range list {
			id, _ := item["id"].(string)
			cwd, _ := item["cwd"].(string)
			if o.Session != "" && id != o.Session {
				continue
			}
			if !config.Allowed(s.Config, cwd) {
				continue
			}
			thread, e := c.Read(ctx, id)
			if e != nil {
				continue
			}
			t, e := normalizer.NormalizeCodex(thread)
			if e == nil {
				_, e = s.Spool.UpsertTranscript(t, now)
			}
			if e != nil {
				c.Close()
				return e
			}
		}
		c.Close()
	}
	return nil
}

// gap is intentionally the one documented predicate, without heuristic inference.
func (s *Sweeper) gap(t normalizer.Transcript) bool {
	if t.Source != "claude-code-agent-sdk" {
		return false
	}
	sid, _ := t.Session["id"].(string)
	var swept int64
	_ = s.Spool.DB.QueryRow(`SELECT last_swept_ms FROM sessions WHERE provider=? AND session_id=?`, t.Source, sid).Scan(&swept)
	for _, v := range t.Session["compactions"].([]any) {
		m := v.(map[string]any)
		if normalizerMS(m["timestamp"]) > swept {
			msg := fmt.Sprintf("GAP WARNING: Claude session %s compacted at %v after last sweep %d", sid, m["timestamp"], swept)
			s.note("gap_"+sid, msg)
			if s.Log != nil {
				s.Log.Print(msg)
			}
			return true
		}
	}
	return false
}
func normalizerMS(v any) int64 {
	if x, ok := v.(string); ok {
		z, e := time.Parse(time.RFC3339Nano, x)
		if e == nil {
			return z.UnixMilli()
		}
	}
	return 0
}
