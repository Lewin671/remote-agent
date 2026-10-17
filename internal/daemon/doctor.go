package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// Doctor checks, against the installed CLI, everything remote-agent relies
// on in its screen and transcript: it starts a real session in dir and
// goes through recognizing the input box, drafts, sending, parsing,
// switching model and approval mode, interrupting, and the terminal view.
// The daemon must be running (Run) with its own state directory and tmux
// socket. Startup prompts for dir are answered the way a local user would.
// It uses a small model at low effort to spend little. The caller marks
// the version verified when it passes.
func (d *Daemon) Doctor(ctx context.Context, kind pb.Agent, dir string, out io.Writer) (bool, error) {
	ag, err := d.knownAgent(kind)
	if err != nil {
		return false, err
	}
	chk, isCheckable := ag.(agent.Checkable)
	if !isCheckable {
		return false, fmt.Errorf("%s: %w", ag.Name(), agent.ErrUnsupported)
	}
	ok := true
	var s *session
	step := func(name string, fn func() error) bool {
		fmt.Fprintf(out, "  %-34s ", name)
		start := time.Now()
		err := fn()
		if err != nil {
			ok = false
			fmt.Fprintf(out, "✗ %v\n", err)
			if s != nil {
				// What the analysis made of the screen, without its text.
				if scr, _, cerr := s.screenNow(); cerr == nil {
					fmt.Fprintf(out, "      screen: input box %v, empty %v, popup %q, footer %q\n", scr.ComposerReady, scr.DraftEmpty, scr.Popup, scr.Footer)
				}
			}
			return false
		}
		fmt.Fprintf(out, "✓ %s\n", time.Since(start).Round(100*time.Millisecond))
		return true
	}

	version := ag.Version()
	if !step(ag.Name()+" --version", func() error {
		if version == "" {
			return fmt.Errorf("cannot run %s --version", ag.Bin())
		}
		fmt.Fprintf(out, "%s ", version)
		return nil
	}) {
		return false, nil
	}

	model, effort, other, otherEffort := chk.DoctorModels()
	if !step("start a session", func() error {
		var err error
		s, err = d.create(&pb.CreateSessionRequest{Agent: ag.Kind(), Cwd: dir, Model: model, Effort: effort, Mode: pb.ApprovalMode_APPROVAL_MODE_ASK})
		return err
	}) {
		return false, nil
	}
	defer s.kill()
	tmuxName := s.meta.Tmux
	keys := func(k ...string) {
		_, _ = d.cfg.Tmux.Run(append([]string{"send-keys", "-t", "=" + tmuxName + ":"}, k...)...)
	}

	// until polls the screen, answering startup prompts, until cond has
	// held for three polls in a row: a single frame can be a passing one.
	until := func(what string, limit time.Duration, cond func(agent.Screen) bool) error {
		deadline := time.Now().Add(limit)
		held := 0
		for time.Now().Before(deadline) {
			if err := ctx.Err(); err != nil {
				return err
			}
			scr, _, err := s.screenNow()
			if err == nil {
				for i, k := range chk.StartupKeys(scr.Popup) {
					if i > 0 {
						time.Sleep(300 * time.Millisecond)
					}
					keys(k)
				}
				if cond(scr) {
					if held++; held >= 3 {
						return nil
					}
				} else {
					held = 0
				}
			}
			time.Sleep(500 * time.Millisecond)
		}
		return fmt.Errorf("timed out waiting for %s", what)
	}
	// event waits for an event after seq that match accepts.
	event := func(after uint64, limit time.Duration, match func(*pb.Event) bool) (*pb.Event, error) {
		deadline := time.Now().Add(limit)
		for time.Now().Before(deadline) {
			s.readRollout()
			var found *pb.Event
			if err := scanEventsSince(s.log, after, func(e *pb.Event) bool {
				if match(e) {
					found = e
					return false
				}
				return true
			}); err != nil {
				return nil, err
			}
			if found != nil {
				return found, nil
			}
			time.Sleep(300 * time.Millisecond)
		}
		return nil, errors.New("timed out")
	}

	if !step("recognize the empty input box", func() error {
		err := until("the input box", 90*time.Second, func(scr agent.Screen) bool { return scr.ComposerReady && scr.DraftEmpty })
		if err != nil {
			return fmt.Errorf("%w; the screen analysis may not match this %s version", err, ag.Name())
		}
		return nil
	}) {
		return false, nil
	}

	step("tell a local draft apart", func() error {
		keys("-l", "doctor draft")
		err := until("the draft", 10*time.Second, func(scr agent.Screen) bool { return scr.ComposerReady && !scr.DraftEmpty })
		for range len("doctor draft") {
			keys("BSpace")
		}
		if err != nil {
			return errors.New("a draft in the input box looks empty; remote input could be mixed into it")
		}
		return until("the empty input box again", 10*time.Second, func(scr agent.Screen) bool { return scr.ComposerReady && scr.DraftEmpty })
	})

	step("send a message and read the reply", func() error {
		seq := s.log.LastSeq()
		if _, err := s.send("Reply with exactly DOCTOR-OK and nothing else.", ""); err != nil {
			return err
		}
		if _, err := event(seq, 30*time.Second, func(e *pb.Event) bool {
			return e.GetInputState().GetStatus() == pb.InputStatus_INPUT_STATUS_SUBMITTED
		}); err != nil {
			return errors.New("the message was not confirmed as submitted")
		}
		for _, want := range []struct {
			what  string
			match func(*pb.Event) bool
		}{
			{"the turn context", func(e *pb.Event) bool { return e.GetTurnContext().GetModel() != "" }},
			{"the reply", func(e *pb.Event) bool { return strings.Contains(e.GetAgentMessage().GetText(), "DOCTOR-OK") }},
			{"token usage", func(e *pb.Event) bool { return e.GetTokenUsage().GetContextWindow() > 0 }},
			{"the end of the turn", func(e *pb.Event) bool { return e.GetTurnEnded() != nil }},
		} {
			if _, err := event(seq, 60*time.Second, want.match); err != nil {
				return fmt.Errorf("no %s in the transcript; its format may have changed", want.what)
			}
		}
		return until("the input box after the turn", 20*time.Second, func(scr agent.Screen) bool { return scr.ComposerReady && scr.DraftEmpty })
	})

	step("switch model and effort", func() error {
		if err := s.setModel(other, otherEffort); err != nil {
			return err
		}
		return s.setModel(model, effort)
	})

	step("switch approval mode and Plan mode", func() error {
		for _, m := range chk.DoctorModes() {
			if err := s.setApprovalMode(m); err != nil {
				return fmt.Errorf("%s: %w", m, err)
			}
		}
		return nil
	})

	step("interrupt a running turn", func() error {
		seq := s.log.LastSeq()
		// Claude Code 2.1.300 blocks a standalone `sleep` (docs/claude-code-notes.md
		// §10); the until-loop its own message suggests keeps a command
		// running until it is interrupted, which is what this step needs.
		if _, err := s.send("Run exactly this shell command and nothing else: until [ -f remote-agent-doctor-never ]; do sleep 2; done", ""); err != nil {
			return err
		}
		if _, err := event(seq, 60*time.Second, func(e *pb.Event) bool { return e.GetToolStarted() != nil }); err != nil {
			return errors.New("the command did not start")
		}
		if ok, err := s.interrupt(); err != nil || !ok {
			return fmt.Errorf("interrupt: %v", err)
		}
		if _, err := event(seq, 20*time.Second, func(e *pb.Event) bool {
			return e.GetTurnEnded().GetOutcome() == pb.TurnOutcome_TURN_OUTCOME_ABORTED
		}); err != nil {
			return errors.New("the turn did not end as interrupted")
		}
		return nil
	})

	step("capture the terminal view", func() error {
		t, err := d.OpenTerminal(s.meta.ID)
		if err != nil {
			return err
		}
		defer t.Close()
		snap, err := t.Snapshot()
		if err != nil {
			return err
		}
		// The snapshot positions text with cursor moves, so only look for
		// the input box's prompt mark: Codex's › or Claude Code's ❯.
		if snap.Cols <= 0 || !strings.ContainsAny(agent.StripANSI(string(snap.Data)), "›❯") {
			return errors.New("the snapshot does not show the input box")
		}
		return nil
	})

	return ok, nil
}
