package daemon

import (
	"log/slog"
	"regexp"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// "Allow, and don't ask again" (architecture §7). Both CLIs' own prompts
// offer it as their second option (Codex: commands with this prefix;
// Claude Code: this directory, or accept edits), but a hook can only allow
// or deny. So the hook stays silent, the CLI shows its prompt, and the
// daemon picks that option there, after checking its wording. A prompt
// without such an option is answered "Yes" for this request only.

const (
	// promptWait bounds the wait for the CLI's prompt after the hook
	// returned, and for it to close after the key.
	promptWait = 10 * time.Second
	closeWait  = 3 * time.Second
)

// optionLine matches a numbered option of a prompt: "› 2. Yes, and ...".
var (
	optionLine = regexp.MustCompile(`^\s*[›❯>]?\s*([0-9])\.\s+(.+?)\s*$`)
	optionKey  = regexp.MustCompile(`\s*\([a-z]+\)$`)
)

// alwaysOption returns the text of the prompt's "don't ask again" option,
// if it has one, and whether the prompt offers a plain yes.
func alwaysOption(plain string) (always string, yes bool) {
	for _, line := range strings.Split(plain, "\n") {
		m := optionLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		text := strings.TrimSpace(m[2])
		// Codex adds the key: "Yes, proceed (y)".
		text = strings.TrimSpace(optionKey.ReplaceAllString(text, ""))
		lower := strings.ToLower(text)
		switch m[1] {
		case "1":
			yes = strings.HasPrefix(lower, "yes")
		case "2":
			if strings.HasPrefix(lower, "yes") {
				always = text
			}
		}
	}
	return always, yes
}

// answerAlways picks the "don't ask again" option in the CLI's prompt for
// approval a and reports how it went.
func (s *session) answerAlways(a *approval, turnID string, origin *pb.Origin) {
	resolved := func(d pb.ApprovalDecision, msg string, o *pb.Origin) {
		s.emit(withOrigin(&pb.Event{TurnId: turnID, Kind: &pb.Event_ApprovalResolved{ApprovalResolved: &pb.ApprovalResolved{
			ApprovalId: a.id, Decision: d, Message: msg,
		}}}, o))
	}
	deadline := time.Now().Add(promptWait)
	for time.Now().Before(deadline) {
		styled, err := s.term.Capture()
		if err != nil {
			break
		}
		scr := s.ag.Analyze(styled)
		plain := agent.StripANSI(styled)
		if scr.PermissionOpen && (a.key == "" || strings.Contains(normalizeText(plain), a.key)) {
			always, yes := alwaysOption(plain)
			key, decision, msg := "2", pb.ApprovalDecision_APPROVAL_DECISION_ALLOW_ALWAYS, always
			if always == "" {
				if !yes {
					break
				}
				key, decision, msg = "1", pb.ApprovalDecision_APPROVAL_DECISION_ALLOW, "remote-agent: the prompt offers no option to stop asking; allowed this request only"
			}
			if err := s.pressInPrompt(key); err != nil {
				slog.Warn("answer permission prompt", "session", s.meta.ID, "err", errClass(err))
				break
			}
			if s.promptCloses(a.key) {
				resolved(decision, msg, origin)
				return
			}
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	// The prompt did not show, or did not take the key: the user answers
	// it in the terminal.
	resolved(pb.ApprovalDecision_APPROVAL_DECISION_CANCELED, "remote-agent: could not answer the CLI's prompt; answer it in the terminal", originSystem)
}

// pressInPrompt writes key into the terminal while local keys are held.
func (s *session) pressInPrompt(key string) error {
	s.ctlMu.Lock()
	defer s.ctlMu.Unlock()
	c, err := s.term.Control()
	if err != nil {
		return err
	}
	defer c.Close()
	if err := c.Hold(holdFor); err != nil {
		return err
	}
	defer func() { _ = c.Release() }()
	return c.Write([]byte(key))
}

// promptCloses waits for the prompt showing key to go away.
func (s *session) promptCloses(key string) bool {
	deadline := time.Now().Add(closeWait)
	for time.Now().Before(deadline) {
		styled, err := s.term.Capture()
		if err != nil {
			return false
		}
		scr := s.ag.Analyze(styled)
		if !scr.PermissionOpen || key != "" && !strings.Contains(normalizeText(agent.StripANSI(styled)), key) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
