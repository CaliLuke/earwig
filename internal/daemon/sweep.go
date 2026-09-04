package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/CaliLuke/earwig/internal/config"
	"github.com/CaliLuke/earwig/internal/exporter"
	"github.com/CaliLuke/earwig/internal/normalizer"
	"github.com/CaliLuke/earwig/internal/provider"
	"github.com/CaliLuke/earwig/internal/spool"
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
		providerCtx, cancel := context.WithTimeout(ctx, provider.DefaultClaudeTimeout())
		e := s.claude(providerCtx, o, now)
		cancel()
		if e != nil {
			first = e
			s.note("claude_error", e.Error())
		} else {
			s.note("claude_error", nil)
		}
	}
	if s.Config.Codex && (o.Provider == "" || o.Provider == "codex") {
		if e := s.codex(ctx, o, now); e != nil {
			if first == nil {
				first = e
			}
			s.note("codex_error", e.Error())
		} else {
			s.note("codex_error", nil)
		}
	}
	if s.Config.OMP && (o.Provider == "" || o.Provider == "omp") {
		if e := s.omp(o, now); e != nil {
			if first == nil {
				first = e
			}
			s.note("omp_error", e.Error())
		} else {
			s.note("omp_error", nil)
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
	s.note("last_sweep_completed", time.Now())
	if first == nil {
		s.note("last_sweep_success", time.Now())
		s.note("last_sweep_error", nil)
	} else {
		s.note("last_sweep_error", first.Error())
	}
	return first
}
func (s *Sweeper) exporters() []exporter.Exporter {
	x := []exporter.Exporter{}
	if s.Config.JSONDir != "" {
		x = append(x, exporter.JSONDir{Dir: s.Config.JSONDir})
	}
	if s.Config.OpikURL != "" {
		x = append(x, exporter.Opik{URL: s.Config.OpikURL, ProjectName: s.Config.OpikProject})
	}
	return x
}
func (s *Sweeper) claude(ctx context.Context, o SweepOptions, now time.Time) error {
	r := provider.ClaudeReader{Helper: s.Config.ClaudeHelper}
	var readErrors []error
	if len(s.Config.WorkspaceRoots) == 0 {
		return nil
	}
	list, e := r.List(ctx)
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
		if o.Session == "" {
			changed, planErr := s.Spool.SessionNeedsSweep("claude-code-agent-sdk", v.ID, v.LastModified)
			if planErr != nil {
				return planErr
			}
			if !changed {
				continue
			}
		}
		info, msg, e := r.Read(ctx, v.ID, v.CWD)
		if e != nil {
			readErrors = append(readErrors, fmt.Errorf("read Claude session %s: %w", v.ID, e))
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
			_ = s.Spool.MarkGapWarned(t.Source, v.ID)
		}
	}
	return errors.Join(readErrors...)
}
func (s *Sweeper) codex(ctx context.Context, o SweepOptions, now time.Time) error {
	var readErrors []error
	if len(s.Config.WorkspaceRoots) == 0 {
		return nil
	}
	workingDirectory, e := os.UserHomeDir()
	if e != nil {
		return e
	}
	c, e := provider.StartCodex(ctx, workingDirectory)
	if e != nil {
		return e
	}
	defer c.Close()
	list, e := c.List(ctx)
	if e != nil {
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
		if o.Session == "" {
			modified := item["updatedAt"]
			if modified == nil {
				modified = item["updated_at"]
			}
			changed, planErr := s.Spool.SessionNeedsSweep("codex-app-server", id, modified)
			if planErr != nil {
				return planErr
			}
			if !changed {
				continue
			}
		}
		thread, e := c.Read(ctx, id)
		if e != nil {
			readErrors = append(readErrors, fmt.Errorf("read Codex session %s: %w", id, e))
			continue
		}
		t, e := normalizer.NormalizeCodex(thread)
		if e == nil {
			_, e = s.Spool.UpsertTranscript(t, now)
		}
		if e != nil {
			return e
		}
	}
	return errors.Join(readErrors...)
}

func (s *Sweeper) omp(o SweepOptions, now time.Time) error {
	if len(s.Config.WorkspaceRoots) == 0 {
		return nil
	}
	list, listErr := provider.ListOMP(s.Config.OMPSessionsPath)
	var readErrors []error
	for _, item := range list {
		if o.Session != "" && item.ID != o.Session {
			continue
		}
		if !config.Allowed(s.Config, item.CWD) {
			continue
		}
		if o.Session == "" {
			changed, planErr := s.Spool.SessionNeedsSweep("omp-session-file", item.ID, item.LastModified)
			if planErr != nil {
				return planErr
			}
			if !changed {
				continue
			}
		}
		file, err := provider.ReadOMP(item.Path)
		if err != nil {
			readErrors = append(readErrors, fmt.Errorf("read OMP session %s: %w", item.ID, err))
			continue
		}
		transcript, err := normalizer.NormalizeOMP(file.Header, file.Entries, file.LastModified)
		if err == nil {
			_, err = s.Spool.UpsertTranscript(transcript, now)
		}
		if err != nil {
			return err
		}
	}
	return errors.Join(append(readErrors, listErr)...)
}

// gap is intentionally the one documented predicate, without heuristic inference.
func (s *Sweeper) gap(t normalizer.Transcript) bool {
	if t.Source != "claude-code-agent-sdk" {
		return false
	}
	sid, _ := t.Session["id"].(string)
	state, found, err := s.Spool.Session(t.Source, sid)
	if !found {
		return false
	}
	if err != nil {
		s.note("gap_check_error_"+sid, err.Error())
		return false
	}
	swept := state.LastSweptMS
	compactions, _ := t.Session["compactions"].([]any)
	for _, v := range compactions {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
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
