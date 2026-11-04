package antigravity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

func planRevisionMatches(facts *agent.PlanFacts) bool {
	if facts == nil || facts.Path == "" || facts.Revision == "" || !filepath.IsAbs(facts.Path) {
		return false
	}
	file, err := os.Open(facts.Path)
	if err != nil {
		return false
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 2<<20 {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(file, 2<<20+1))
	if err != nil || len(data) > 2<<20 {
		return false
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) == facts.Revision
}

// PlanScreen accepts only the named artifact with an explicit review action.
// A generic artifact count, an empty list, or a different revision is not a plan.
func (a *Adapter) PlanScreen(styled string, facts *agent.PlanFacts) bool {
	if !planRevisionMatches(facts) {
		return false
	}
	plain := StripANSI(styled)
	if !strings.Contains(plain, "Artifacts") || !strings.Contains(plain, filepath.Base(facts.Path)) {
		return false
	}
	lower := strings.ToLower(plain)
	return strings.Contains(lower, "approve") && strings.Contains(lower, "reject")
}
func (a *Adapter) AnswerPlan(t agent.Term, choice pb.PlanChoice, facts *agent.PlanFacts) error {
	if choice != pb.PlanChoice_PLAN_CHOICE_IMPLEMENT && choice != pb.PlanChoice_PLAN_CHOICE_KEEP_PLANNING {
		return agent.ErrUnsupported
	}
	if !planRevisionMatches(facts) {
		return errors.New("plan artifact revision changed or is ambiguous; handle in terminal")
	}
	styled, err := t.Capture()
	if err != nil {
		return err
	}
	if !a.PlanScreen(styled, facts) {
		scr := a.Analyze(styled)
		if !scr.ComposerReady || !scr.DraftEmpty || scr.Busy || scr.Popup != "" || !strings.Contains(StripANSI(styled), "/artifact to review") {
			return agent.ErrNoPlan
		}
		if err := t.Write([]byte("\x12")); err != nil {
			return err
		}
		styled, err = agent.WaitFor(t, "specific artifact review", func(s string) bool { return a.PlanScreen(s, facts) })
		if err != nil {
			return err
		}
	}
	// The selected row must name this artifact, not merely another entry on a
	// multiple-artifact screen. No navigation or guessed selection is attempted.
	selected := false
	plain := StripANSI(styled)
	if i := strings.LastIndex(plain, "Artifacts"); i >= 0 {
		plain = plain[i:]
	}
	for _, line := range strings.Split(plain, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "› ") || strings.HasPrefix(trim, "> ") {
			if strings.Contains(trim, filepath.Base(facts.Path)) {
				selected = true
			} else {
				return agent.ErrNoPlan
			}
		}
	}
	if !selected {
		return agent.ErrNoPlan
	}
	if !planRevisionMatches(facts) {
		return agent.ErrNoPlan
	}
	key := "y"
	if choice == pb.PlanChoice_PLAN_CHOICE_KEEP_PLANNING {
		key = "n"
	}
	if err := t.Write([]byte(key)); err != nil {
		return agent.ErrDeliveryUnconfirmed
	}
	_, err = agent.WaitFor(t, "plan review consumption", func(screen string) bool { return !a.PlanScreen(screen, facts) })
	if err != nil {
		return agent.ErrDeliveryUnconfirmed
	}
	return nil
}
