package daemon

import (
	"context"
	"log/slog"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// A session that has had nothing going on for a while has its CLI closed,
// so finished conversations do not pile up as running ones and hold their
// processes. Resuming it continues the conversation.
const (
	defaultIdleCloseMinutes = 6 * 60
	minIdleCloseMinutes     = 60
	maxIdleCloseMinutes     = 7 * 24 * 60
)

var idleCloseInterval = 30 * time.Second

// idleCloseAfter is how long a session must be idle to be closed; zero
// when closing idle sessions is turned off.
func (d *Daemon) idleCloseAfter() time.Duration {
	n := d.settings().IdleCloseMinutes
	if n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Minute
}

// observeActivity notes events that show the session in use. Usage and
// context updates can come from a status line refresh, not from work.
func (s *session) observeActivity(e *pb.Event) {
	switch e.Kind.(type) {
	case *pb.Event_TokenUsage, *pb.Event_TurnContext:
		return
	}
	s.activeAt = time.Now()
}

// idleLocked reports whether the session is open with nothing going on
// that closing it could lose: no turn, no pending request or input, no
// command possibly left running in the background, and not a scheduled
// run's session, which its task closes. Its idle clock restarts otherwise.
func (s *session) idleLocked(now time.Time) bool {
	if s.meta.Exited || s.closing || s.meta.TaskOwned || s.scheduledTask != "" || s.backgroundPossible || s.busyLocked() {
		s.activeAt = now
		return false
	}
	if s.activeAt.IsZero() {
		s.activeAt = now
	}
	return true
}

// terminalActivity is when someone last typed into the session's terminal
// through tmux: on this computer, or a client's terminal view.
func (s *session) terminalActivity(ctx context.Context) time.Time {
	t, ok := s.term.(interface {
		ClientActivity(context.Context) (time.Time, error)
	})
	if !ok {
		return time.Time{}
	}
	at, err := t.ClientActivity(ctx)
	if err != nil {
		// Unknown: keep the session open this time.
		return time.Now()
	}
	return at
}

// idleDue reports whether the session has been idle for after, counting
// typing in its terminal as activity.
func (s *session) idleDue(ctx context.Context, after time.Duration, now time.Time) bool {
	s.mu.Lock()
	due := s.idleLocked(now) && now.Sub(s.activeAt) >= after
	s.mu.Unlock()
	if !due {
		return false
	}
	typed := s.terminalActivity(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if typed.After(s.activeAt) {
		s.activeAt = typed
	}
	return s.idleLocked(now) && now.Sub(s.activeAt) >= after
}

// claimIdleClose is idleDue under lifecycleMu and ctlMu with the transcript
// read, marking the session closing in the same step so that no input is
// taken in between.
func (s *session) claimIdleClose(ctx context.Context, after time.Duration) bool {
	if after <= 0 || !s.readRollout() {
		return false
	}
	now := time.Now()
	if !s.idleDue(ctx, after, now) {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.idleLocked(now) || now.Sub(s.activeAt) < after {
		return false
	}
	s.closing = true
	return true
}

func (d *Daemon) startIdleCloseLoop(ctx context.Context) func() {
	ctx, cancel := context.WithCancel(ctx)
	var closers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer closers.Wait()
		closing := map[string]bool{}
		var closingMu sync.Mutex
		ticker := time.NewTicker(idleCloseInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			for _, s := range d.idleClosures(ctx) {
				id := s.meta.ID
				closingMu.Lock()
				if closing[id] {
					closingMu.Unlock()
					continue
				}
				closing[id] = true
				closingMu.Unlock()
				closers.Go(func() {
					defer func() { closingMu.Lock(); delete(closing, id); closingMu.Unlock() }()
					if err := s.killFor(ctx, exitIdle, d.idleCloseAfter()); err != nil {
						slog.Warn("close idle CLI", "session", id, "err", errClass(err))
					}
				})
			}
		}
	}()
	return func() { cancel(); <-done }
}

// idleClosures are the sessions idle for longer than the setting. Every
// open session is looked at, so that busy ones restart their idle clocks.
func (d *Daemon) idleClosures(ctx context.Context) []*session {
	after := d.idleCloseAfter()
	active := map[string]bool{}
	d.scheduleMu.Lock()
	for _, t := range d.tasks {
		if t.Active != nil && t.Active.SessionID != "" {
			active[t.Active.SessionID] = true
		}
	}
	d.scheduleMu.Unlock()
	d.mu.Lock()
	sessions := make([]*session, 0, len(d.sessions))
	for _, s := range d.sessions {
		sessions = append(sessions, s)
	}
	d.mu.Unlock()
	now := time.Now()
	var out []*session
	for _, s := range sessions {
		s.mu.Lock()
		idle := s.idleLocked(now)
		if active[s.meta.ID] {
			s.activeAt, idle = now, false
		}
		s.mu.Unlock()
		if !idle || after == 0 {
			continue
		}
		if s.idleDue(ctx, after, now) {
			out = append(out, s)
		}
	}
	return out
}
