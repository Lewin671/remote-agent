package daemon

import (
	"context"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// backgroundClearGrace is how long a ScreenBackground CLI (Claude Code) must
// stay stably idle with no live background work and no new events before
// backgroundPossible is cleared. It bridges the gap between a background task
// disappearing from the footer and Claude Code starting its task-notification
// turn.
const backgroundClearGrace = 3 * time.Second

func (s *session) tracksBackground() bool {
	return s.ag.Capabilities().ConversationIdentity || s.ag.Capabilities().ScreenBackground
}

func (s *session) resetBackgroundClearLocked() {
	s.backgroundClearSince = time.Time{}
	s.backgroundClearSeq = 0
	s.backgroundClearObs = 0
}

func (s *session) clearBackgroundLocked() {
	s.backgroundPossible = false
	idle := false
	s.meta.BackgroundPossible = &idle
	s.meta.BackgroundIdleSeq = s.log.LastSeq()
	s.resetBackgroundClearLocked()
	s.noteStorageLocked(s.meta.save(s.dir))
}

func (s *session) observeBackground(e *pb.Event) {
	switch e.Kind.(type) {
	case *pb.Event_TokenUsage, *pb.Event_TurnContext:
	default:
		s.backgroundActivitySeq = e.Seq
	}
	if !s.tracksBackground() {
		return
	}
	if call := e.GetToolCall(); call != nil && call.Background != pb.ToolBackground_TOOL_BACKGROUND_UNSPECIFIED && e.Seq > s.meta.BackgroundIdleSeq {
		s.backgroundPossible = true
		value := true
		s.meta.BackgroundPossible = &value
		s.resetBackgroundClearLocked()
	}
}

func (s *session) needsBackgroundScreenCheck() bool {
	if !s.ag.Capabilities().ScreenBackground {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.backgroundPossible && !s.running && !s.driving && !s.meta.Exited
}

// checkBackgroundScreen captures the terminal outside s.mu/scheduleMu and
// advances the screen-based background clear check for ScreenBackground CLIs.
func (s *session) checkBackgroundScreen(ctx context.Context, now time.Time) {
	if !s.needsBackgroundScreenCheck() {
		return
	}
	styled, err := s.term.CaptureContext(ctx)
	if err != nil {
		s.mu.Lock()
		s.resetBackgroundClearLocked()
		s.mu.Unlock()
		return
	}
	s.settleBackgroundClear(ctx, s.ag.Analyze(styled), now)
}

// settleBackgroundClear clears backgroundPossible for ScreenBackground CLIs
// only after multiple captures spanning backgroundClearGrace show an idle
// composer with no live background work and no new transcript events, verified
// by draining the rollout and re-capturing the screen under readMu.
// Never call under s.mu or scheduleMu; lock order is readMu -> mu.
func (s *session) settleBackgroundClear(ctx context.Context, scr agent.Screen, now time.Time) {
	if !s.ag.Capabilities().ScreenBackground {
		return
	}
	clearScreen := func(scr agent.Screen) bool {
		return scr.ComposerReady && !scr.Busy && scr.Popup == "" && !scr.Background
	}
	s.mu.Lock()
	if !s.backgroundPossible || s.running || s.driving || s.meta.Exited || !clearScreen(scr) {
		s.resetBackgroundClearLocked()
		s.mu.Unlock()
		return
	}
	seq := s.backgroundActivitySeq
	if s.backgroundClearSince.IsZero() || s.backgroundClearSeq != seq || now.Before(s.backgroundClearSince) {
		s.backgroundClearSince = now
		s.backgroundClearSeq = seq
		s.backgroundClearObs = 1
		s.mu.Unlock()
		return
	}
	s.backgroundClearObs++
	ready := s.backgroundClearObs >= 2 && !now.Before(s.backgroundClearSince.Add(backgroundClearGrace))
	s.mu.Unlock()
	if !ready {
		return
	}
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if !s.readRolloutLocked() {
		return
	}
	fresh, err := s.term.CaptureContext(ctx)
	if err != nil {
		s.mu.Lock()
		s.resetBackgroundClearLocked()
		s.mu.Unlock()
		return
	}
	freshScr := s.ag.Analyze(fresh)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.backgroundPossible || s.running || s.driving || s.meta.Exited || s.backgroundActivitySeq != seq || !clearScreen(freshScr) {
		s.resetBackgroundClearLocked()
		return
	}
	s.clearBackgroundLocked()
}
