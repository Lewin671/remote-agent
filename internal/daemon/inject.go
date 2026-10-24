package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Writing queued messages into the CLI (architecture §6, §17).
//
// A message is written while local keys are held, only into an empty input
// box. Images go first: each file path is pasted on its own and must turn
// into a new "[Image #N]" in the input box before the next; then the text
// and Enter. Every stage is saved before the write it leads to, so that
// after a restart the daemon knows what the input box may hold. If an
// image is not taken, nothing is submitted: the input box is emptied with
// Ctrl-U if it holds only what was pasted, and the message ends FAILED;
// if that cannot be verified it ends NEEDS_TERMINAL. Neither is retried.

// Holding local keys: the hold is renewed while a message is written, but
// the whole write is bounded, so that a stuck daemon or CLI never keeps
// the user out of their own terminal for long.
const (
	injectBase     = 10 * time.Second // text-only messages and cleanup
	injectPerImage = 8 * time.Second
	imageWait      = 6 * time.Second // for one "[Image #N]" to appear
	unrecognized   = time.Second     // the path showing as text this long
	clearWait      = 2 * time.Second
	// imageStartDelay is how long a new session's input box must have
	// been ready before images are pasted into it.
	imageStartDelay = 3 * time.Second
)

// conversionWindow is how long, after a restart, an image pasted before it
// may take to turn into a placeholder (Claude Code converts in the
// background: about a second for 9 MB).
var conversionWindow = 10 * time.Second

// recentInputs bounds the submitted messages remembered for matching the
// CLI's record of them, which may come late (Codex writes a message sent
// during a turn after the next tool call).
const recentInputs = 20

// lease holds local input and renews the hold until end, or until the
// deadline, after which the PTY host lets the hold lapse.
type lease struct {
	c        control
	deadline time.Time
	stop     chan struct{}
	done     sync.WaitGroup

	mu sync.Mutex
	ok time.Time // last hold the PTY host confirmed
}

func holdLease(c control, total time.Duration) (*lease, error) {
	if err := c.Hold(holdFor); err != nil {
		return nil, err
	}
	l := &lease{c: c, deadline: time.Now().Add(total), stop: make(chan struct{}), ok: time.Now()}
	l.done.Add(1)
	go func() {
		defer l.done.Done()
		t := time.NewTicker(holdFor / 3)
		defer t.Stop()
		for {
			select {
			case <-l.stop:
				return
			case <-t.C:
				if time.Until(l.deadline) <= 0 {
					return
				}
				at := time.Now()
				if c.Hold(min(holdFor, time.Until(l.deadline)+time.Second)) == nil {
					l.mu.Lock()
					l.ok = at
					l.mu.Unlock()
				}
			}
		}
	}()
	return l, nil
}

// holding reports whether local keys are surely still held: the last
// confirmed hold has not run out (with a second to spare), and the total
// has not passed. Every write to the terminal requires it.
func (l *lease) holding() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return time.Since(l.ok) < holdFor-time.Second && time.Now().Before(l.deadline)
}

// left is how long the lease still holds for sure: what remains of the
// total, less a margin for a cleanup.
func (l *lease) left() time.Duration { return time.Until(l.deadline) - clearWait }

func (l *lease) end() {
	close(l.stop)
	l.done.Wait()
	_ = l.c.Release()
}

// inputOutcome is what one attempt at writing the head message came to.
type inputOutcome int

const (
	notWritten inputOutcome = iota // the terminal was not ready; try later
	written                        // the Enter went out; settle it
	finished                       // failed before Enter and reported
)

// enqueue accepts a message and saves the queue before returning, so an
// accepted message survives a daemon restart. It returns the queue
// position; a retried request ID is accepted once (position 0).
func (s *session) enqueue(text, requestID string, images []queuedImage, origin *pb.Origin) (int, error) {
	return s.enqueueMode(text, requestID, images, origin, pb.SendMode_SEND_MODE_STEER)
}

// enqueueMode is enqueue with a delivery mode.
func (s *session) enqueueMode(text, requestID string, images []queuedImage, origin *pb.Origin, mode pb.SendMode) (int, error) {
	return s.enqueueInput(text, requestID, images, origin, mode, nil, nil)
}

func (s *session) enqueueUploaded(text, requestID string, ids []string, origin *pb.Origin, mode pb.SendMode) (int, error) {
	return s.enqueueInput(text, requestID, nil, origin, mode, nil, func(id string) ([]queuedImage, error) {
		if len(ids) == 0 {
			return nil, nil
		}
		owner, err := imageOwner(origin)
		if err != nil {
			return nil, err
		}
		return s.d.uploads.take(ids, owner, s.meta.ID, id)
	})
}

func (s *session) enqueueInput(text, requestID string, images []queuedImage, origin *pb.Origin, mode pb.SendMode, reserved *inputReservation, take func(string) ([]queuedImage, error)) (int, error) {
	return s.enqueueInputContext(context.Background(), text, requestID, images, origin, mode, reserved, take)
}
func (s *session) enqueueInputContext(ctx context.Context, text, requestID string, images []queuedImage, origin *pb.Origin, mode pb.SendMode, reserved *inputReservation, take func(string) ([]queuedImage, error)) (int, error) {
	if !humanOrigin(origin) {
		return s.enqueueInputReady(text, requestID, images, origin, mode, reserved, take)
	}
	// The lifecycle lock gives admission and automatic shutdown one order.
	// Never hold it while asking resume to acquire that same lock.
	for {
		s.lifecycleMu.Lock()
		s.mu.Lock()
		resume := s.meta.Exited && s.meta.ScheduledTaskID != ""
		s.mu.Unlock()
		if !resume {
			break
		}
		s.lifecycleMu.Unlock()
		if _, err := s.d.resumeContextBy(ctx, s.meta.ID, origin); err != nil && !errors.Is(err, errRunning) {
			return 0, err
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
	defer s.lifecycleMu.Unlock()
	return s.enqueueInputReady(text, requestID, images, origin, mode, reserved, take)
}
func (s *session) enqueueInputReady(text, requestID string, images []queuedImage, origin *pb.Origin, mode pb.SendMode, reserved *inputReservation, take func(string) ([]queuedImage, error)) (int, error) {
	if mode == pb.SendMode_SEND_MODE_UNSPECIFIED {
		mode = pb.SendMode_SEND_MODE_STEER
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.meta.Exited || s.closing {
		return 0, errExited
	}
	if s.queueLost {
		return 0, errQueueUnreadable
	}
	if requestID == "" {
		requestID = newID(8)
	} else if s.queue.seen(requestID) {
		s.poke()
		return 0, nil
	}
	if reserved == nil {
		var err error
		reserved, err = s.d.reserveInput(s.meta.ID, s.queue, text)
		if err != nil {
			return 0, err
		}
	}
	defer reserved.release()
	if take != nil {
		var err error
		images, err = take(requestID)
		if err != nil {
			return 0, err
		}
	}
	prevQueue, prevSeen := s.queue.Queue, s.queue.Seen
	pos := s.queue.insert(input{Text: text, RequestID: requestID, Images: images, Origin: storeOrigin(origin), Stage: pb.InputStage_INPUT_STAGE_WAITING, Mode: mode})
	s.queue.remember(requestID)
	reserved.commit(s)
	st := &pb.InputState{RequestId: requestID, Status: pb.InputStatus_INPUT_STATUS_QUEUED, Stage: pb.InputStage_INPUT_STAGE_WAITING, Text: text, Mode: mode}
	for _, im := range images {
		st.ImageIds = append(st.ImageIds, im.ID)
	}
	saved, err := s.saveQueueLocked(withOrigin(&pb.Event{Kind: &pb.Event_InputState{InputState: st}}, origin))
	if !saved {
		s.queue.Queue, s.queue.Seen = prevQueue, prevSeen
		s.trackPendingInput()
		if take != nil {
			s.d.uploads.untake(images, s.meta.ID, requestID)
		}
		return 0, err
	}
	if err != nil {
		return pos, fmt.Errorf("message %s queued at position %d and may still be sent: %w", requestID, pos, err)
	}
	return pos, nil
}

// send queues text from this computer.
func (s *session) send(text, requestID string) (int, error) {
	return s.enqueue(text, requestID, nil, originLocal)
}

// injectLoop submits queued input one message at a time, in order.
func (s *session) injectLoop(ctx context.Context) {
	retry := time.NewTimer(time.Hour)
	defer retry.Stop()
	for {
		s.mu.Lock()
		var next input
		ok := len(s.queue.Queue) > 0 && !s.meta.Exited
		held := false
		if ok {
			next = s.queue.Queue[0]
			held = s.holdsLocked(next)
		}
		s.mu.Unlock()
		if held {
			// A queue message waits for the turn to end; a paused one
			// for the user. Check again now and then: the fallback of
			// holdsLocked needs time to pass.
			retry.Reset(time.Second)
			goto wait
		}
		if ok {
			outcome := written
			var err error
			switch {
			case next.Inflight != nil:
				// Written, possibly by a previous daemon: settle it
				// without ever writing it again.
			case next.Stage == pb.InputStage_INPUT_STAGE_PASTING_IMAGES || next.Stage == pb.InputStage_INPUT_STAGE_IMAGES_RECOGNIZED:
				// A previous daemon stopped while pasting images.
				outcome, err = s.recoverPaste(next)
			default:
				outcome, err = s.inject(next)
			}
			if err != nil && !errors.Is(err, errEventCommit) && !errors.Is(err, errQueueCommit) {
				slog.Warn("inject", "session", s.meta.ID, "err", errClass(err))
			}
			switch outcome {
			case notWritten:
				retry.Reset(s.d.cfg.RetryInterval)
				goto wait
			case finished:
				if s.heads(next.RequestID) {
					// Its outcome could not be logged: once more later.
					retry.Reset(s.d.cfg.RetryInterval)
					goto wait
				}
				continue
			}
			// The mark was made on the queue's copy. If the head is no
			// longer this message as marked, the session exited meanwhile
			// and settled it.
			s.mu.Lock()
			marked := len(s.queue.Queue) > 0 && s.queue.Queue[0].RequestID == next.RequestID && s.queue.Queue[0].Inflight != nil
			if marked {
				next = s.queue.Queue[0]
			}
			s.mu.Unlock()
			if !marked {
				continue
			}
			if !s.settle(ctx, next) {
				return // ctx done; it stays inflight for the next daemon
			}
			if s.heads(next.RequestID) {
				retry.Reset(s.d.cfg.RetryInterval)
				goto wait
			}
			continue
		}
	wait:
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-retry.C:
		}
	}
}

// inject tries once to write the head message. It leaves the message
// queued when the terminal is not ready: a popup is open or the local user
// has an unsent draft, which is never overwritten or merged.
func (s *session) inject(in input) (inputOutcome, error) {
	s.ctlMu.Lock()
	defer s.ctlMu.Unlock()
	c, err := s.term.Control()
	if err != nil {
		return notWritten, err
	}
	defer c.Close()
	l, err := holdLease(c, injectBase+time.Duration(len(in.Images))*injectPerImage)
	if err != nil {
		return notWritten, err
	}
	// Local keys typed after the release reach the CLI after the Enter,
	// in the fresh input box.
	defer l.end()
	time.Sleep(settle)
	styled, err := s.term.Capture()
	if err != nil {
		return notWritten, err
	}
	scr := s.ag.Analyze(styled)
	if !scr.ComposerReady || !scr.DraftEmpty {
		s.noteWait(scr)
		return notWritten, nil
	}
	// While the CLI starts, its input box can flash before a startup
	// dialog covers it; wait until the status check has seen a settled
	// screen.
	s.mu.Lock()
	starting := s.status == pb.SessionStatus_SESSION_STATUS_STARTING
	// Just after it starts, Codex has once taken a pasted image path as
	// plain text; images wait until the input box has been up a moment.
	young := len(in.Images) > 0 && !s.readySince.IsZero() && time.Since(s.readySince) < imageStartDelay
	s.mu.Unlock()
	if starting || young {
		return notWritten, nil
	}
	s.noteWait(scr)
	// The status line under the empty input box tells where the box ends
	// once it holds several lines.
	footer := scr.Footer
	var got []int32
	if len(in.Images) > 0 {
		if err := s.setStage(in.RequestID, pb.InputStage_INPUT_STAGE_PASTING_IMAGES, nil); err != nil {
			if errors.Is(err, errHeadChanged) {
				return notWritten, nil // canceled meanwhile
			}
			return notWritten, err
		}
		for _, im := range in.Images {
			if l.left() < imageWait || !l.holding() {
				return s.abandon(c, l, in, got, footer, false, "timeout"), nil
			}
			if err := c.Write(agent.PastePath(im.Path)); err != nil {
				return s.abandon(c, l, in, got, footer, false, "write_failed"), err
			}
			n, reason := s.waitPlaceholder(got, im.Path, footer)
			if reason != "" {
				return s.abandon(c, l, in, got, footer, false, reason), nil
			}
			got = append(got, n)
			if err := s.setStage(in.RequestID, pb.InputStage_INPUT_STAGE_PASTING_IMAGES, got); err != nil {
				return s.abandon(c, l, in, got, footer, false, "write_failed"), err
			}
		}
		if err := s.setStage(in.RequestID, pb.InputStage_INPUT_STAGE_IMAGES_RECOGNIZED, got); err != nil {
			return s.abandon(c, l, in, got, footer, false, "write_failed"), err
		}
		in.Indexes = got
	}
	if !l.holding() {
		// Local keys may have got in: nothing more is written.
		if len(got) > 0 {
			return s.abandon(c, l, in, got, footer, false, "timeout"), nil
		}
		return notWritten, nil
	}
	hist := s.newHistory(-1)
	if err := s.markInflight(hist.offset(), in.RequestID); err != nil {
		if errors.Is(err, errHeadChanged) {
			return notWritten, nil
		}
		if len(got) > 0 {
			return s.abandon(c, l, in, got, footer, false, "write_failed"), err
		}
		return notWritten, err
	}
	// Saving the mark may have taken a while: the input box and the hold
	// are checked once more right before the Enter. The hold comes last:
	// looking at the screen can itself outlast it, and the PTY host lets
	// anyone write once nobody holds.
	if !s.boxHolds(got, footer) || !l.holding() {
		if len(got) > 0 {
			s.unmarkInflight(in.RequestID, pb.InputStage_INPUT_STAGE_IMAGES_RECOGNIZED)
			return s.abandon(c, l, in, got, footer, false, "timeout"), nil
		}
		s.unmarkInflight(in.RequestID, pb.InputStage_INPUT_STAGE_WAITING)
		return notWritten, nil
	}
	if err := c.Write(s.ag.Paste(in.Text)); err != nil {
		// A failed reply does not prove the bytes were not written, so
		// the message is only settled from the CLI's records, never
		// resent.
		return written, err
	}
	return written, nil
}

// waitPlaceholder waits for the input box to show one more image
// placeholder than it did, and nothing else, after a file path was pasted.
// It returns the new placeholder's N, or why not.
func (s *session) waitPlaceholder(before []int32, path, footer string) (int32, string) {
	deadline := time.Now().Add(imageWait)
	var pathSince time.Time
	for {
		styled, err := s.term.Capture()
		if err == nil {
			text, ok := s.ag.ComposerText(styled, footer)
			// Only what was pasted is there, with no line breaks of its
			// own: lines are a narrow terminal wrapping it.
			text = strings.ReplaceAll(text, "\n", "")
			nums := agent.ImagePlaceholders(text)
			rest, _ := agent.StripPlaceholders(text, nums)
			rest = strings.TrimSpace(rest)
			switch {
			case !ok:
				// A popup or menu took the screen.
				return 0, "input_changed"
			case rest == "" && len(nums) == len(before)+1 && slices.Equal(nums[:len(before)], before):
				return nums[len(before)], ""
			case rest == "" && len(nums) == len(before) && slices.Equal(nums, before):
				// Not converted yet (Claude Code converts in the
				// background).
				pathSince = time.Time{}
			case rest != "" && strings.Contains(path, rest) || strings.HasSuffix(rest, path):
				// The path shows as text: the CLI did not take the file.
				if pathSince.IsZero() {
					pathSince = time.Now()
				} else if time.Since(pathSince) >= unrecognized {
					return 0, "image_not_recognized"
				}
			default:
				return 0, "input_changed"
			}
		}
		if time.Now().After(deadline) {
			return 0, "timeout"
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// abandon ends a message that failed before its Enter: it empties the
// input box if it holds only what was pasted for the message, and reports
// the message FAILED, or NEEDS_TERMINAL when the input box could not be
// verified to be back to empty. extra allows placeholders that were not
// yet recorded (after a restart). The caller still holds local input.
func (s *session) abandon(c control, l *lease, in input, got []int32, footer string, extra bool, reason string) inputOutcome {
	status := pb.InputStatus_INPUT_STATUS_NEEDS_TERMINAL
	if s.clearPasted(c, l, in, got, footer, extra, reason == "timeout") {
		status = pb.InputStatus_INPUT_STATUS_FAILED
	}
	slog.Warn("message not submitted", "session", s.meta.ID, "request", in.RequestID, "reason", reason, "status", status.String())
	s.finishInput(in, status, reason)
	return finished
}

// clearPasted empties the input box if it holds only this message's image
// placeholders and file paths, and reports whether it is verified empty
// afterwards. After a timeout an empty box does not count: the CLI may
// still be converting an image and add its placeholder later. Nothing is
// written unless local keys are still held after the box was looked at.
func (s *session) clearPasted(c control, l *lease, in input, got []int32, footer string, extra, timedOut bool) bool {
	if !l.holding() {
		return false
	}
	styled, err := s.term.Capture()
	if err != nil {
		return false
	}
	text, ok := s.ag.ComposerText(styled, footer)
	if !ok {
		return false
	}
	// A line break is not something this message pasted: Ctrl-U deletes
	// one line only, and another line may be the user's.
	if strings.Contains(text, "\n") {
		return false
	}
	if text == "" {
		return !timedOut
	}
	rest, all := agent.StripPlaceholders(text, got)
	if !all {
		return false
	}
	if extra {
		nums := agent.ImagePlaceholders(rest)
		if len(got)+len(nums) > len(in.Images) {
			return false
		}
		rest, _ = agent.StripPlaceholders(rest, nums)
	}
	for _, im := range in.Images {
		rest = strings.ReplaceAll(rest, im.Path, "")
	}
	if strings.TrimSpace(rest) != "" {
		return false // something else is in the input box: not ours to delete
	}
	if !l.holding() {
		return false
	}
	if err := c.Write(s.ag.ClearInput()); err != nil {
		return false
	}
	deadline := time.Now().Add(clearWait)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		if styled, err := s.term.Capture(); err == nil {
			if text, ok := s.ag.ComposerText(styled, footer); ok && text == "" {
				return !timedOut
			}
		}
	}
	return false
}

// recoverPaste settles a message a previous daemon was pasting images for
// when it stopped. It is never written again, and nothing is deleted: the
// hold ended with the old daemon, so what the input box holds may be the
// user's (who may have cleared it and pasted images of their own, whose
// placeholders Codex numbers alike). The CLI may also still be converting
// an image the old daemon pasted. The message is FAILED only if the box
// stays empty for the whole conversion window; anything else leaves it to
// the terminal. Later messages wait meanwhile.
func (s *session) recoverPaste(in input) (inputOutcome, error) {
	s.ctlMu.Lock()
	defer s.ctlMu.Unlock()
	c, err := s.term.Control()
	if err != nil {
		return notWritten, err
	}
	defer c.Close()
	l, err := holdLease(c, conversionWindow+injectBase)
	if err != nil {
		return notWritten, err
	}
	defer l.end()
	status := pb.InputStatus_INPUT_STATUS_FAILED
	deadline := time.Now().Add(conversionWindow)
	for {
		styled, err := s.term.Capture()
		var text string
		ok := false
		if err == nil {
			text, ok = s.ag.ComposerText(styled, "")
		}
		if !ok || text != "" {
			status = pb.InputStatus_INPUT_STATUS_NEEDS_TERMINAL
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	slog.Warn("message not submitted", "session", s.meta.ID, "request", in.RequestID, "reason", "restarted", "status", status.String())
	s.finishInput(in, status, "restarted")
	return finished, nil
}

// setStage saves how far the head message has got and reports it.
func (s *session) setStage(requestID string, stage pb.InputStage, indexes []int32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.meta.Exited || len(s.queue.Queue) == 0 || s.queue.Queue[0].RequestID != requestID || s.holdsLocked(s.queue.Queue[0]) {
		return errHeadChanged
	}
	prev := s.queue.Queue[0]
	// A stage which precedes a terminal write is not durable until its event
	// is committed. In particular a failed first PASTING_IMAGES event must
	// leave WAITING, rather than send untouched input through recoverPaste.
	if stage != prev.Stage {
		if err := s.appendLocked([]*pb.Event{{Kind: &pb.Event_InputState{InputState: &pb.InputState{
			RequestId: requestID, Status: pb.InputStatus_INPUT_STATUS_QUEUED, Stage: stage,
		}}}}, "", 0); err != nil {
			return err
		}
	}
	if stage == pb.InputStage_INPUT_STAGE_PASTING_IMAGES {
		if err := s.d.uploads.expose(s.queue.Queue[0].Images); err != nil {
			return err
		}
	}
	s.queue.Queue[0].Stage, s.queue.Queue[0].Indexes = stage, slices.Clone(indexes)
	if saved, err := s.saveQueueLocked(); err != nil {
		if !saved {
			s.queue.Queue[0] = prev
		}
		return err
	}
	return nil
}

// finishInput reports the head message's outcome and removes it.
func (s *session) finishInput(in input, status pb.InputStatus, reason string) {
	outcome := &pb.Event{Kind: &pb.Event_InputState{InputState: &pb.InputState{RequestId: in.RequestID, Status: status, Reason: reason}}}
	if !s.popInput(in.RequestID, outcome) {
		return // the session exited and reports it, or it stays for another try
	}
	s.d.uploads.releaseRefs(in.Images, s.meta.ID, in.RequestID)
}

// heads reports whether requestID is the head message.
func (s *session) heads(requestID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue.Queue) > 0 && s.queue.Queue[0].RequestID == requestID
}

// settle waits for evidence that the written head message was submitted,
// records the outcome and removes it from the queue. It returns false if
// ctx ended first.
func (s *session) settle(ctx context.Context, in input) bool {
	hist := s.newHistory(*in.Inflight)
	deadline := time.Now().Add(s.d.cfg.SubmitTimeout)
	status := pb.InputStatus_INPUT_STATUS_UNCONFIRMED
	for time.Now().Before(deadline) {
		s.readRollout()
		s.mu.Lock()
		cliID := s.meta.CodexSessionID
		s.mu.Unlock()
		if hist.submitted(in.matches, cliID) || s.loggedUserMessage(in) {
			status = pb.InputStatus_INPUT_STATUS_SUBMITTED
			break
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
	if status == pb.InputStatus_INPUT_STATUS_UNCONFIRMED && s.unconfirm(in) {
		return true
	}
	if s.heldByCLI(in) {
		s.hold(in)
		return true
	}
	s.finishInput(in, pb.InputStatus_INPUT_STATUS_SUBMITTED, "")
	return true
}

// unconfirm reports the head message UNCONFIRMED and remembers that, so
// that a record of it coming later still confirms it. It returns false,
// reporting nothing, if the record came meanwhile. No record is read in
// between: either it was logged before, or it finds the message
// remembered as unconfirmed.
func (s *session) unconfirm(in input) bool {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if s.loggedUserMessage(in) {
		return false
	}
	slog.Warn("injected message not confirmed; not sending it again", "session", s.meta.ID, "request", in.RequestID)
	s.mu.Lock()
	s.unproven[in.RequestID] = true
	s.mu.Unlock()
	s.finishInput(in, pb.InputStatus_INPUT_STATUS_UNCONFIRMED, "")
	return true
}

// matches reports whether text the CLI recorded is this message: with
// this message's image placeholders taken out, the same text. Both the
// text and the images are evidence: a record without the placeholders
// does not count for a message with images.
func (in input) matches(recorded string) bool {
	if len(in.Images) > 0 {
		stripped, ok := agent.StripPlaceholders(recorded, in.Indexes)
		if !ok || len(in.Indexes) != len(in.Images) {
			return false
		}
		recorded = stripped
	}
	return normalizeText(recorded) == normalizeText(in.Text)
}

// matchesMessage reports whether a parsed user message is this message,
// its images included.
func (in input) matchesMessage(m *pb.UserMessage) bool {
	if !in.matches(m.Text) {
		return false
	}
	return len(in.pairImages(m)) == len(in.Images)
}

// pairImages maps this message's images to the refs of a parsed user
// message: by the file path (Codex records it) or the placeholder number.
func (in input) pairImages(m *pb.UserMessage) map[int]*pb.ImageRef {
	out := map[int]*pb.ImageRef{}
	for i, im := range in.Images {
		for _, ref := range m.Images {
			if ref.Path != "" && filepath.Clean(ref.Path) == filepath.Clean(im.Path) ||
				ref.Path == "" && i < len(in.Indexes) && ref.CliIndex == in.Indexes[i] {
				out[i] = ref
				break
			}
		}
	}
	return out
}

// loggedUserMessage reports whether the message was logged as a user
// message after it was written.
func (s *session) loggedUserMessage(in input) bool {
	found := false
	if err := scanEventsSince(s.log, in.InflightSeq, func(e *pb.Event) bool {
		if m := e.GetUserMessage(); m != nil && in.matchesMessage(m) {
			found = true
			return false
		}
		return true
	}); err != nil {
		slog.Warn("scan events for submitted input", "request", in.RequestID, "err", errClass(err))
	}
	return found
}

// annotateUserMessage ties a parsed user message to the message that was
// written for it, recording its origin, request ID and the upload ID of
// each image. Each written message is matched once. When several could be
// the one and they came from different places, none is named. A message
// nothing was written for came from the local terminal. It returns what
// uses the match up, run once the event is logged. Callers hold s.mu.
func (s *session) annotateUserMessage(e *pb.Event, m *pb.UserMessage) func() {
	type cand struct {
		in   input
		head bool
		i    int
	}
	var cands []cand
	inflight := false
	if len(s.queue.Queue) > 0 && s.queue.Queue[0].Inflight != nil {
		inflight = true
		if in := s.queue.Queue[0]; !in.matched && in.matchesMessage(m) {
			cands = append(cands, cand{in: in, head: true})
		}
	}
	for i, in := range s.recent {
		if in.matchesMessage(m) {
			cands = append(cands, cand{in: in, i: i})
		}
	}
	if len(cands) == 0 {
		if !inflight {
			withOrigin(e, originLocal)
		}
		return nil
	}
	for _, c := range cands[1:] {
		if !proto.Equal(c.in.Origin.pb(), cands[0].in.Origin.pb()) {
			return nil // cannot tell whose it is
		}
	}
	// The oldest of the candidates is taken: remembered ones before the
	// head, in the order they were written.
	pick := cands[0]
	if pick.head && len(cands) > 1 {
		pick = cands[1]
	}
	in := pick.in
	m.RequestId = in.RequestID
	for i, ref := range in.pairImages(m) {
		ref.UploadId = in.Images[i].ID
	}
	withOrigin(e, in.Origin.pb())
	id, head := in.RequestID, pick.head
	return func() {
		if head {
			if len(s.queue.Queue) > 0 && s.queue.Queue[0].RequestID == id {
				s.queue.Queue[0].matched = true
				return
			}
		}
		// Popped meanwhile, or remembered: take it out of the memory.
		for i, r := range s.recent {
			if r.RequestID == id {
				s.recent = append(s.recent[:i:i], s.recent[i+1:]...)
				return
			}
		}
	}
}

// rememberLocked keeps a written message a while, to match the CLI's
// record of it. Callers hold s.mu.
func (s *session) rememberLocked(in input) {
	s.recent = append(s.recent, in)
	if len(s.recent) > recentInputs {
		delete(s.unproven, s.recent[0].RequestID)
		s.recent = s.recent[1:]
	}
}

// confirmLate reports a message SUBMITTED that was reported UNCONFIRMED
// before the CLI recorded it (Codex records a message when its turn
// starts, which may take longer than the wait). The state goes into the
// batch just before the user message that proves it, the order of a
// message confirmed in time: clients tell by it who sent a message.
func (s *session) confirmLate(evs []*pb.Event) []*pb.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.unproven) == 0 {
		return evs
	}
	out := make([]*pb.Event, 0, len(evs)+1)
	done := map[string]bool{}
	for _, e := range evs {
		if id := e.GetUserMessage().GetRequestId(); id != "" && s.unproven[id] && !done[id] {
			done[id] = true
			out = append(out, &pb.Event{Time: timestamppb.Now(), Kind: &pb.Event_InputState{InputState: &pb.InputState{
				RequestId: id, Status: pb.InputStatus_INPUT_STATUS_SUBMITTED,
			}}})
		}
		out = append(out, e)
	}
	return out
}

// popInput logs the outcome of the head message, if it is requestID, and
// removes it; it reports whether it did. A message that was written is
// remembered a while, to match the CLI's record of it.
func (s *session) popInput(requestID string, outcome *pb.Event) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.meta.Exited || len(s.queue.Queue) == 0 || s.queue.Queue[0].RequestID != requestID {
		return false
	}
	if !s.logOutcomeLocked(outcome) {
		return false
	}
	if in := s.queue.Queue[0]; in.Inflight != nil && !in.matched {
		s.rememberLocked(in)
	}
	s.queue.Queue = s.queue.Queue[1:]
	s.saveSettledLocked()
	return true
}

// A message leaves the queue or the held list in two steps: its outcome is
// logged, then queue.json is saved without it. In that order, a daemon
// that stops in between comes back to a message whose outcome is logged
// and drops it (dropSettled); the other way round, the message would be
// gone with clients forever shown it waiting. Both steps happen under one
// hold of s.mu.

// logOutcomeLocked logs the outcomes of messages about to be removed.
// Nothing is removed if it fails. Callers hold s.mu.
func (s *session) logOutcomeLocked(outcomes ...*pb.Event) bool {
	now := timestamppb.Now()
	for _, e := range outcomes {
		if e.Time == nil {
			e.Time = now
		}
	}
	// All outcomes use the same commit/projection boundary, including the
	// inferred TurnEnded passed by abortTurnLocked.
	if err := s.appendLocked(outcomes, "", 0); err != nil {
		return false
	}
	return true
}

// saveSettledLocked saves the queue once messages whose outcomes are
// logged were removed. If that fails they stay removed: the log has the
// last word, and the next start puts the file right. Callers hold s.mu.
func (s *session) saveSettledLocked() {
	// The final outcome is already durable even if queue cleanup fails.
	defer s.poke()
	_, _ = s.saveQueueLocked()
}

// markInflight records, before the head message's text and Enter are
// written, where the submit log and the event log stood.
func (s *session) markInflight(offset int64, requestID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Nothing can cancel or move this head between preparing the mark and
	// committing it. Both records must be durable before any terminal write.
	if s.meta.Exited || len(s.queue.Queue) == 0 || s.queue.Queue[0].RequestID != requestID || s.holdsLocked(s.queue.Queue[0]) {
		return errHeadChanged
	}
	prev := s.queue.Queue[0]
	s.queue.Queue[0].Inflight = &offset
	s.queue.Queue[0].InflightSeq = s.log.LastSeq()
	s.queue.Queue[0].Stage = pb.InputStage_INPUT_STAGE_SUBMITTING
	e := &pb.Event{Time: timestamppb.Now(), Kind: &pb.Event_InputState{InputState: &pb.InputState{
		RequestId: requestID, Status: pb.InputStatus_INPUT_STATUS_QUEUED, Stage: pb.InputStage_INPUT_STAGE_SUBMITTING,
	}}}
	// Append first: if it fails, the on-disk queue still has the original
	// pre-write state. Rolling back a saved mark could itself fail on a full
	// disk, falsely making an untouched message look written after restart.
	if err := s.appendLocked([]*pb.Event{e}, "", 0); err != nil {
		s.queue.Queue[0] = prev
		return err
	}
	if saved, err := s.saveQueueLocked(); err != nil {
		if !saved {
			s.queue.Queue[0] = prev
		}
		return err
	}
	// Queue messages after this one wait for the turn it lands in. Do not
	// advance this gate for an attempt that never reached the terminal.
	s.gate, s.gateAt = s.turnEnds+1, time.Now()
	return nil
}

// boxHolds reports whether the input box holds exactly the placeholders
// got (or nothing, without images).
func (s *session) boxHolds(got []int32, footer string) bool {
	styled, err := s.term.Capture()
	if err != nil {
		return false
	}
	text, ok := s.ag.ComposerText(styled, footer)
	if !ok {
		return false
	}
	text = strings.ReplaceAll(text, "\n", "")
	rest, all := agent.StripPlaceholders(text, got)
	return all && strings.TrimSpace(rest) == "" && len(agent.ImagePlaceholders(text)) == len(got)
}

// unmarkInflight takes back an inflight mark when nothing was written
// after it.
func (s *session) unmarkInflight(requestID string, stage pb.InputStage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue.Queue) == 0 || s.queue.Queue[0].RequestID != requestID {
		return
	}
	s.queue.Queue[0].Inflight, s.queue.Queue[0].InflightSeq, s.queue.Queue[0].Stage = nil, 0, stage
	_, _ = s.saveQueueLocked()
}
