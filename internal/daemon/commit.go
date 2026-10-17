package daemon

import (
	"fmt"
	"log/slog"
	"strings"
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
			s.log.Poke()
		}
		return
	}
	if s.storageWarnAt.IsZero() || time.Since(s.storageWarnAt) >= time.Minute {
		slog.Error("session storage failure", "session", s.meta.ID, "err", errClass(err))
		s.storageWarnAt = time.Now()
	}
	if s.storageFault == "" {
		s.storageFault = "session storage failure; recent changes may not have been recorded"
		s.log.Poke()
	}
}

func joinDetail(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "; ")
}
