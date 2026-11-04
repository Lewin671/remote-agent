package daemon

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"github.com/Lewin671/remote-agent/internal/agent"
)

var errMCPModeObservation = errors.New("fresh native mode observation is not available yet")

func (s *session) scheduleObservationStale() bool {
	if _, ok := s.ag.(agent.StatusRefresher); !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLineAt.IsZero() || time.Since(s.statusLineAt) > 5*time.Second
}

// Call only outside scheduleMu/readMu: refreshing owns terminal control and
// the status callback needs the transcript lock to publish its observation.
func (s *session) refreshScheduleObservation(parent context.Context) error {
	if !s.scheduleObservationStale() {
		return nil
	}
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	if err := s.refreshNativeStatus(ctx); err != nil {
		return connect.NewError(connect.CodeUnavailable, errMCPModeObservation)
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for s.scheduleObservationStale() {
		select {
		case <-ctx.Done():
			return connect.NewError(connect.CodeUnavailable, errMCPModeObservation)
		case <-ticker.C:
		}
	}
	return nil
}

func (d *Daemon) refreshDueScheduleObservations(ctx context.Context, now time.Time) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	d.scheduleMu.Lock()
	ids := map[string]bool{}
	for _, task := range d.tasks {
		if task.ScopedSession != "" && (task.ManualRequest != nil || task.Status == "waiting_turn" || task.Status == "scheduled" && task.NextRunAt != nil && !now.Before(*task.NextRunAt)) {
			ids[task.ScopedSession] = true
		}
	}
	d.scheduleMu.Unlock()
	for id := range ids {
		if ctx.Err() != nil {
			return
		}
		if source, err := d.exact(id); err == nil {
			_ = source.refreshScheduleObservation(ctx)
		}
	}
}
