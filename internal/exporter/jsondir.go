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
	if err := j.Health(); err != nil {
		return err
	}
	for _, row := range rows {
		if err := j.WriteRow(row); err != nil {
			return err
		}
	}
	return nil
}

// WriteRow writes one captured turn. Call Health first when an empty export
// must still create and validate the destination directory.
func (j JSONDir) WriteRow(row spool.Row) error {
	directory := filepath.Join(j.Dir, row.Provider, row.SessionID)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(directory, row.TraceUUID+".json"), append([]byte(row.Payload), '\n'), 0600)
}
