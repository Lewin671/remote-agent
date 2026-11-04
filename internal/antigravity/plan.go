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
	if facts == nil || facts.Path == "" || facts.Revision == "" || facts.Root == "" || !filepath.IsAbs(facts.Path) {
		return false
	}
	root, err := filepath.EvalSymlinks(facts.Root)
	if err != nil {
		return false
	}
	path, err := filepath.EvalSymlinks(facts.Path)
	if err != nil || filepath.Dir(path) != root {
		return false
	}
	file, err := os.Open(path)
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
func planPanel(styled string) string {
	plain := StripANSI(styled)
	start := -1
	for _, header := range []string{"Action required (", "Artifacts"} {
		if at := strings.LastIndex(plain, "\n"+header); at >= 0 && at+1 > start {
			start = at + 1
		}
		if strings.HasPrefix(plain, header) {
			start = 0
		}
	}
	if start < 0 {
		return ""
	}
	return plain[start:]
}
func (a *Adapter) PlanScreen(styled string, facts *agent.PlanFacts) bool {
	if !planRevisionMatches(facts) {
		return false
	}
	panel := planPanel(styled)
	if !strings.HasPrefix(panel, "Action required (") || !strings.Contains(panel, "y/n Approve/reject") || !strings.Contains(panel, "esc Done") {
		return false
	}
	for _, line := range strings.Split(panel, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "› ") || strings.HasPrefix(trim, "> ") {
			return strings.Contains(trim, filepath.Base(facts.Path)) && strings.Contains(trim, "approve reject")
		}
	}
	return false
}
func (a *Adapter) AnswerPlan(t agent.Term, choice pb.PlanChoice, facts *agent.PlanFacts, observed func() bool) error {
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
	// PlanScreen binds the selected action-required row, not a similarly
	// named artifact elsewhere in the list or chat history.
	if !a.PlanScreen(styled, facts) {
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
	_, err = agent.WaitFor(t, "plan review consumption", func(screen string) bool { return observed != nil && observed() || !a.PlanScreen(screen, facts) })
	if err != nil {
		return agent.ErrDeliveryUnconfirmed
	}
	return nil
}
