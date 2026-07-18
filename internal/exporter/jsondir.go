package exporter

import (
	"os"
	"path/filepath"

	"github.com/CaliLuke/earwig/internal/spool"
)

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
