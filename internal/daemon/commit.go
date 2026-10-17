package daemon

import (
	"fmt"
	"log/slog"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// appendLocked is the event commit boundary. Hold mu; projections and
// parser replay may only change after it succeeds. A failed write must be
// visible even when the same storage cannot save a SessionState event.
func (s *session) appendLocked(evs []*pb.Event, path string, offset int64) error {
	err := s.log.Append(s.meta.ID, evs, path, offset)
	if err != nil || len(evs) > 0 {
		s.noteStorageLocked(err)
	}
	if err != nil {
		return fmt.Errorf("record session events: %w", err)
	}
	return nil
}

func (s *session) noteStorageLocked(err error) {
	if err == nil {
		if s.storageFault != "" {
			s.storageFault = ""
			s.clearStorageDiagnostic()
		}
		return
	}
	first := s.storageFault == ""
	s.storageFault = "session storage failure; recent changes may not have been recorded"
	if first || s.storageWarnAt.IsZero() || time.Since(s.storageWarnAt) >= time.Minute {
		// Diagnostic export is best-effort: its directory is separate from the
		// session log, but a full filesystem may prevent both writes. The log
		// remains the fallback; never put this state in shared Session metadata.
		saved := s.saveStorageDiagnostic() == nil
		if s.storageWarnAt.IsZero() || time.Since(s.storageWarnAt) >= time.Minute {
			slog.Error("session storage failure", "session", s.meta.ID, "err", errClass(err), "local_diagnostic_saved", saved)
			s.storageWarnAt = time.Now()
		}
	}
}
