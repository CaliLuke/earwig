package exporter

import (
	"errors"
	"fmt"
	"time"

	"github.com/CaliLuke/earwig/internal/spool"
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
