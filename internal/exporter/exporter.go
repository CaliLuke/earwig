package exporter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/CaliLuke/earwig/internal/spool"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

type Exporter interface {
	Name() string
	Export([]spool.Row) error
	Health() error
}
type JSONDir struct{ Dir string }

func (j JSONDir) Name() string  { return "jsondir" }
func (j JSONDir) Health() error { return os.MkdirAll(j.Dir, 0700) }
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
	URL    string
	Client *http.Client
}

func (o Opik) Name() string { return "opik" }
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
	c := o.Client
	if c == nil {
		c = &http.Client{Timeout: 5 * time.Second}
	}
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
	c := o.Client
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Second}
	}
	for _, r := range rows {
		var p any
		if e := json.Unmarshal([]byte(r.Payload), &p); e != nil {
			return e
		}
		body := map[string]any{"id": r.TraceUUID, "thread_id": r.SessionID, "name": "earwig-turn", "start_time": time.UnixMilli(r.StartedMS).UTC().Format(time.RFC3339Nano), "end_time": time.UnixMilli(r.CompletedMS).UTC().Format(time.RFC3339Nano), "metadata": p, "tags": []string{"auto-checkpoint", "inbox", r.Provider, r.Status}, "project_name": "earwig"}
		b, _ := json.Marshal(body)
		u := o.URL + "/api/v1/private/traces"
		req, e := http.NewRequest(http.MethodPost, u, bytes.NewReader(b))
		if e != nil {
			return e
		}
		req.Header.Set("Content-Type", "application/json")
		res, e := c.Do(req)
		if e != nil {
			return e
		}
		responseBody, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
		res.Body.Close()
		if res.StatusCode < 200 || res.StatusCode >= 300 {
			return fmt.Errorf("Opik export: %s: %s", res.Status, string(responseBody))
		}
	}
	return nil
}
func Drain(s *spool.Spool, e Exporter) error {
	rows, err := s.Pending(e.Name(), 500)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	if err = e.Export(rows); err != nil {
		return err
	}
	return s.MarkExported(e.Name(), rows, time.Now())
}
