package exporter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/CaliLuke/earwig/internal/spool"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Exporter interface {
	Name() string
	Export([]spool.Row) error
	Health() error
}

type targetKeyer interface{ TargetKey() string }

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

func isPermanent(err error) bool {
	var target permanentError
	return errors.As(err, &target)
}

type JSONDir struct{ Dir string }

func (j JSONDir) Name() string      { return "jsondir" }
func (j JSONDir) TargetKey() string { return filepath.Clean(j.Dir) }
func (j JSONDir) Health() error     { return os.MkdirAll(j.Dir, 0700) }
func (j JSONDir) Export(rows []spool.Row) error {
	if e := j.Health(); e != nil {
		return e
	}
	for _, r := range rows {
		d := filepath.Join(j.Dir, r.Provider, r.SessionID)
		if e := os.MkdirAll(d, 0700); e != nil {
			return e
		}
		if e := os.WriteFile(filepath.Join(d, r.TraceUUID+".json"), append([]byte(r.Payload), '\n'), 0600); e != nil {
			return e
		}
	}
	return nil
}

type Opik struct {
	URL         string
	ProjectName string
	Client      *http.Client
}

func (o Opik) Name() string { return "opik" }
func (o Opik) TargetKey() string {
	return strings.TrimRight(o.URL, "/") + "\x1f" + o.projectName()
}
func (o Opik) projectName() string {
	if o.ProjectName != "" {
		return o.ProjectName
	}
	return "earwig"
}

func (o Opik) httpClient(timeout time.Duration) *http.Client {
	var client http.Client
	if o.Client != nil {
		client = *o.Client
	} else {
		client.Timeout = timeout
	}
	previousRedirectCheck := client.CheckRedirect
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if err := loopback(request.URL.String()); err != nil {
			return fmt.Errorf("reject Opik redirect: %w", err)
		}
		if previousRedirectCheck != nil {
			return previousRedirectCheck(request, via)
		}
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		return nil
	}
	return &client
}

func loopback(raw string) error {
	u, e := url.Parse(raw)
	if e != nil {
		return e
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("exporter URL must be HTTP(S)")
	}
	if u.User != nil {
		return fmt.Errorf("exporter URL must not contain credentials")
	}
	h := u.Hostname()
	if h == "localhost" {
		return nil
	}
	ip := net.ParseIP(h)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("exporter URL must be loopback")
	}
	return nil
}
func (o Opik) Health() error {
	if e := loopback(o.URL); e != nil {
		return e
	}
	c := o.httpClient(5 * time.Second)
	r, e := c.Get(o.URL)
	if e != nil {
		return e
	}
	r.Body.Close()
	if r.StatusCode >= 500 {
		return fmt.Errorf("Opik health: %s", r.Status)
	}
	return nil
}
func (o Opik) Export(rows []spool.Row) error {
	if e := loopback(o.URL); e != nil {
		return e
	}
	c := o.httpClient(15 * time.Second)
	for _, r := range rows {
		var p spool.TurnPayload
		if e := json.Unmarshal([]byte(r.Payload), &p); e != nil {
			return permanentError{fmt.Errorf("invalid spooled turn payload: %w", e)}
		}
		project := o.projectName()
		mapped := mapTurnPayload(p, r)
		create := map[string]any{
			"id":           r.TraceUUID,
			"thread_id":    r.SessionID,
			"name":         providerTraceName(r.Provider),
			"start_time":   time.UnixMilli(r.StartedMS).UTC().Format(time.RFC3339Nano),
			"end_time":     time.UnixMilli(r.CompletedMS).UTC().Format(time.RFC3339Nano),
			"input":        mapped["input"],
			"output":       mapped["output"],
			"metadata":     mapped["metadata"],
			"tags":         []string{"auto-checkpoint", "inbox", r.Provider, r.Status},
			"project_name": project,
			"source":       "sdk",
		}
		status, responseBody, e := o.request(c, http.MethodPost, "/api/v1/private/traces", create)
		if e != nil {
			return e
		}
		if status == http.StatusConflict {
			update := map[string]any{
				"thread_id":    r.SessionID,
				"end_time":     create["end_time"],
				"input":        create["input"],
				"output":       create["output"],
				"metadata":     create["metadata"],
				"project_name": project,
				"source":       "sdk",
			}
			status, responseBody, e = o.request(c, http.MethodPatch, "/api/v1/private/traces/"+url.PathEscape(r.TraceUUID), update)
			if e != nil {
				return e
			}
			if status == http.StatusConflict {
				return permanentError{fmt.Errorf("Opik trace %s belongs to a different project; configure opik_project to match the existing trace (Auto-K uses autok-agent-evals): %s", r.TraceUUID, strings.TrimSpace(string(responseBody)))}
			}
		}
		if status < 200 || status >= 300 {
			err := fmt.Errorf("Opik export: HTTP %d: %s", status, string(responseBody))
			if status >= 400 && status < 500 {
				return permanentError{err}
			}
			return err
		}
	}
	return nil
}

func (o Opik) request(client *http.Client, method, path string, body any) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, strings.TrimRight(o.URL, "/")+path, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	responseBody, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	res.Body.Close()
	return res.StatusCode, responseBody, nil
}

// ProtectedTraceIDs returns traces curated upstream with a retention tag.
// Prune treats lookup failures as fatal so local evidence is never deleted
// when upstream retention state cannot be verified.
func (o Opik) ProtectedTraceIDs(ids []string) (map[string]bool, error) {
	if err := loopback(o.URL); err != nil {
		return nil, err
	}
	client := o.httpClient(15 * time.Second)
	protected := map[string]bool{}
	for _, id := range ids {
		status, body, err := o.request(client, http.MethodGet, "/api/v1/private/traces/"+url.PathEscape(id), nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("Opik retention lookup %s: HTTP %d: %s", id, status, string(body))
		}
		var trace struct {
			Tags []string `json:"tags"`
		}
		if err = json.Unmarshal(body, &trace); err != nil {
			return nil, fmt.Errorf("Opik retention lookup %s: %w", id, err)
		}
		for _, tag := range trace.Tags {
			if strings.EqualFold(tag, "kept") || strings.EqualFold(tag, "promoted") {
				protected[id] = true
				break
			}
		}
	}
	return protected, nil
}

func mapTurnPayload(p spool.TurnPayload, row spool.Row) map[string]any {
	metadata := map[string]any{
		"source":                  p.Source,
		"source_session":          p.Session,
		"source_turn_id":          p.Turn.ID,
		"source_turn_status":      p.Turn.Status,
		"duration_ms":             p.Turn.DurationMS,
		"trajectory":              p.Turn.Trajectory,
		"following_user_messages": p.Turn.FollowingUserMessages,
		"capture":                 p.Capture,
		"error":                   nil,
		"captured_at_ms":          row.CapturedMS,
	}
	if p.Turn.Error != nil {
		metadata["error"] = *p.Turn.Error
	}
	return map[string]any{
		"input":    map[string]any{"user_messages": p.Turn.UserMessages},
		"output":   map[string]any{"assistant_messages": p.Turn.AssistantMessages, "final_answer": p.Turn.FinalAnswer},
		"metadata": metadata,
	}
}

func providerTraceName(provider string) string {
	if provider == "codex-app-server" {
		return "codex-turn"
	}
	if provider == "claude-code-agent-sdk" {
		return "claude-turn"
	}
	return "earwig-turn"
}
func Drain(s *spool.Spool, e Exporter) error {
	targetKey := e.Name()
	if keyed, ok := e.(targetKeyer); ok {
		targetKey = keyed.TargetKey()
	}
	rows, err := s.PendingFor(e.Name(), targetKey, 500)
	if err != nil {
		return err
	}
	succeeded := []spool.Row{}
	failures := []error{}
	for _, row := range rows {
		exportErr := e.Export([]spool.Row{row})
		if exportErr == nil {
			succeeded = append(succeeded, row)
			continue
		}
		permanent := isPermanent(exportErr)
		if recordErr := s.RecordExportFailure(e.Name(), targetKey, row, exportErr, permanent, time.Now()); recordErr != nil {
			failures = append(failures, fmt.Errorf("record %s failure for trace %s: %w", e.Name(), row.TraceUUID, recordErr))
			break
		}
		if permanent {
			continue
		}
		failures = append(failures, fmt.Errorf("%s trace %s: %w", e.Name(), row.TraceUUID, exportErr))
		break
	}
	if len(succeeded) > 0 {
		if err = s.MarkExported(e.Name(), succeeded, time.Now()); err != nil {
			failures = append(failures, err)
		}
	}
	quarantined, err := s.ActivePermanentFailures(e.Name(), targetKey, 10)
	if err != nil {
		failures = append(failures, err)
	} else if len(quarantined) > 0 {
		first := quarantined[0]
		failures = append(failures, fmt.Errorf("%s has quarantined traces; most recent is %s: %s", e.Name(), first.TraceUUID, first.Error))
	}
	return errors.Join(failures...)
}
