package daemon

import (
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

func (s *session) settleIdleCompletion(styled string) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	source, ok := s.parser.(agent.IdleCompletionSource)
	if !ok {
		return
	}
	ready := func(screen string) bool {
		scr := s.ag.Analyze(screen)
		return s.running && !s.driving && !s.meta.Exited && !s.identityPending && !s.identityFailed && s.statusLineIdle && !s.statusLineAt.IsZero() && time.Since(s.statusLineAt) < 10*time.Second && scr.ComposerReady && !scr.Busy && scr.Popup == ""
	}
	s.mu.Lock()
	event := source.IdleCompletion()
	if event == nil || !ready(styled) {
		s.idleCompleteSince = time.Time{}
		s.mu.Unlock()
		return
	}
	if s.idleCompleteSince.IsZero() {
		s.idleCompleteSince = time.Now()
	}
	stable := time.Since(s.idleCompleteSince) >= time.Second
	turn := s.turnID
	s.mu.Unlock()
	if !stable || !s.readRolloutLocked() {
		return
	}
	source, ok = s.parser.(agent.IdleCompletionSource)
	if !ok {
		return
	}
	fresh, err := s.term.Capture()
	if err != nil {
		return
	}
	s.mu.Lock()
	event = source.IdleCompletion()
	valid := event != nil && s.turnID == turn && ready(fresh)
	s.mu.Unlock()
	if !valid {
		return
	}
	event.Origin = originSystem
	if event.GetTurnEnded().Reason == "" {
		event.GetTurnEnded().Reason = "CLI idle after completed transcript response"
	}
	if err := s.emitLocked(false, event); err != nil {
		return
	}
	s.parser.Replay(event)
	s.afterTranscript([]*pb.Event{event})
}
