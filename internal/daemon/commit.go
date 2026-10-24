package daemon

import (
	"errors"
	"fmt"
	"log/slog"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/eventlog"
	"github.com/Lewin671/remote-agent/internal/tail"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	errEventCommit   = errors.New("record session events")
	errQueueCommit   = errors.New("save input queue")
	errQueueAccepted = errors.New("input queue saved but event could not be recorded; do not resend with a new request ID")
)

// queueAccepted distinguishes a recording failure after durable acceptance
// from rejection. It must only be used at queue-operation boundaries: an
// arbitrary event-commit error does not prove that anything was accepted.
func queueAccepted(err error) bool {
	return err == nil || errors.Is(err, errQueueAccepted)
}

// appendLocked commits and projects events exactly once. Hold mu; parser
// Replay remains explicit at callers holding readMu (parsed events must not
// be replayed twice). Failure is visible even when the same storage cannot
// save a SessionState event.
func (s *session) appendLocked(evs []*pb.Event, path string, offset int64) error {
	if path == "" {
		now := timestamppb.Now()
		for _, e := range evs {
			if e.Time == nil {
				e.Time = now
			}
		}
	}
	_, err := s.appendBatchesLocked([]eventlog.Batch{{Events: evs, Rollout: path, Offset: offset}})
	return err
}

func (s *session) appendBatchesLocked(batches []eventlog.Batch) (int, error) {
	s.annotateScheduled(batches)
	n, err := s.log.AppendGroup(s.meta.ID, batches)
	hasEvents := false
	for _, batch := range batches[:n] {
		hasEvents = hasEvents || len(batch.Events) > 0
	}
	if err != nil || hasEvents {
		s.noteStorageLocked(err)
	}
	if err != nil {
		return 0, fmt.Errorf("%w: %w", errEventCommit, err)
	}
	for _, batch := range batches[:n] {
		for _, e := range batch.Events {
			s.observe(e)
		}
	}
	return n, nil
}

type transcriptBatch struct {
	batch   eventlog.Batch
	consume func()
}

// commitTranscript publishes only successful groups. A byte cap can split
// the prepared lines into smaller groups; failure leaves earlier groups
// committed, just as an earlier single-line append was kept before.
func (s *session) commitTranscript(group []transcriptBatch) error {
	batches := make([]eventlog.Batch, len(group))
	for i, line := range group {
		batches[i] = line.batch
	}
	for len(batches) > 0 {
		s.mu.Lock()
		n, err := s.appendBatchesLocked(batches)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		for _, line := range group[:n] {
			for _, e := range line.batch.Events {
				if st := e.GetInputState(); st != nil {
					delete(s.unproven, st.RequestId)
				}
			}
			line.consume()
		}
		s.mu.Unlock()
		for _, batch := range batches[:n] {
			s.afterTranscript(batch.Events)
		}
		batches, group = batches[n:], group[n:]
	}
	return nil
}

// retryTranscript discards parser state from the uncommitted group. Hold
// readMu; Replay and Restore run at the start of the next read.
func (s *session) retryTranscript(path string) {
	checkpointPath, off := s.log.Checkpoint()
	if checkpointPath != path {
		off = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	off = s.rolloutOffset(path, off)
	s.reader = tail.NewReader(path, off)
	s.parserReplayPending = true
	s.parserReplayPath, s.parserReplayOffset = path, off
}

func (s *session) afterTranscript(evs []*pb.Event) {
	if len(evs) > 0 {
		// A previous failure may have left a held outcome owing.
		s.settleReceived()
	}
	for _, e := range evs {
		if m := e.GetUserMessage(); m != nil {
			s.received(m.RequestId)
			for _, ref := range m.Images {
				if ref.UploadId != "" && ref.ImageId != "" {
					s.d.uploads.embedded(ref.UploadId)
				}
			}
		}
		if e.GetTurnEnded() != nil {
			// Claude retracts terminal approvals when the turn ends;
			// Codex may still have an escalated command awaiting its hook.
			if s.ag.Kind() == pb.Agent_AGENT_CLAUDE {
				s.retractApprovals()
			}
			s.afterTurn()
		}
		if c := e.GetToolCall(); c != nil {
			s.retractFor(c)
		}
	}
}

// saveQueueLocked persists the new canonical queue, then attempts its events.
// Once saved it always wakes delivery, even when recording a notification
// fails. saved distinguishes that case from a failed queue save: callers
// must not undo memory that already became durable. Hold mu, or be the sole
// owner while restoring before the session starts.
func (s *session) saveQueueLocked(evs ...*pb.Event) (saved bool, err error) {
	s.trackPendingInput()
	if err := s.queue.saveWith(s.dir, s.d.writeStateFile); err != nil {
		s.noteStorageLocked(err)
		if fileReplaced(err) {
			s.claimQueuedHumanOwnershipLocked()
			s.poke()
			return true, fmt.Errorf("%w: %w: %w", errQueueAccepted, errQueueCommit, err)
		}
		return false, fmt.Errorf("%w: %w", errQueueCommit, err)
	}
	s.claimQueuedHumanOwnershipLocked()
	defer s.poke()
	if len(evs) > 0 {
		if err := s.appendLocked(evs, "", 0); err != nil {
			return true, fmt.Errorf("%w: %w", errQueueAccepted, err)
		}
	}
	return true, nil
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
