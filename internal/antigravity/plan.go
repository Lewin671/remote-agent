package antigravity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
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

// planPanel isolates the active native artifact review from chat history.
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
func (a *Adapter) selectedPlan(styled string, facts *agent.PlanFacts) bool {
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
			name, _ := reviewRow(trim)
			return name == filepath.Base(facts.Path) && strings.Contains(trim, "approve reject")
		}
	}
	return false
}

var pendingArtifactRE = regexp.MustCompile(`^[1-9][0-9]* artifacts? · /artifact to review$`)

// PlanScreen exposes a single transcript-bound feedback request while idle,
// before the user has opened Ctrl-R. The panel is still verified before y/n.
func (a *Adapter) PlanScreen(styled string, facts *agent.PlanFacts) bool {
	if !planRevisionMatches(facts) {
		return false
	}
	if a.selectedPlan(styled, facts) {
		return reviewMatches(styled, facts, "new")
	}
	scr := a.Analyze(styled)
	if !scr.ComposerReady || !scr.DraftEmpty || scr.Busy || scr.Popup != "" {
		return false
	}
	lines, top, _, ok := composerRegion(styled)
	return ok && top > 0 && pendingArtifactRE.MatchString(strings.TrimSpace(StripANSI(lines[top-1])))
}

func reviewRow(line string) (name, status string) {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(strings.TrimPrefix(line, "› "), "> ")
	status = "open"
	for _, prefix := range []struct{ mark, state string }{{"□ new ", "new"}, {"✓ approved ", "approved"}, {"✗ rejected ", "rejected"}} {
		if strings.HasPrefix(line, prefix.mark) {
			line = strings.TrimPrefix(line, prefix.mark)
			status = prefix.state
			break
		}
	}
	name, _, _ = strings.Cut(line, "   ")
	name = strings.TrimSpace(name)
	if strings.HasSuffix(name, ".md") {
		return name, status
	}
	return "", ""
}

func reviewRows(styled string) map[string]string {
	panel := planPanel(styled)
	rows := map[string]string{}
	for _, line := range strings.Split(panel, "\n") {
		if name, status := reviewRow(line); name != "" {
			rows[name] = status
		}
	}
	return rows
}
func reviewMatches(styled string, facts *agent.PlanFacts, wanted string) bool {
	if facts == nil {
		return false
	}
	rows := reviewRows(styled)
	name := filepath.Base(facts.Path)
	if rows[name] != wanted {
		return false
	}
	if facts.Decisions == nil {
		return len(rows) == 1
	} // isolated controller fixtures
	if len(rows) != len(facts.Decisions) {
		return false
	}
	for other, status := range facts.Decisions {
		if other != name && rows[other] != status {
			return false
		}
	}
	return true
}
func submitFocused(styled string) bool {
	for _, line := range strings.Split(styled, "\n") {
		if !strings.Contains(StripANSI(line), "Submit") || !strings.Contains(StripANSI(line), "Dismiss") {
			continue
		}
		for _, run := range agent.Runs(line) {
			if run.Reverse && strings.TrimSpace(run.Text) == "Submit" {
				return true
			}
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
	if !a.selectedPlan(styled, facts) {
		if !a.PlanScreen(styled, facts) {
			return agent.ErrNoPlan
		}
		if err := t.Write([]byte("\x12")); err != nil {
			return err
		}
		styled, err = agent.WaitFor(t, "specific artifact review", func(s string) bool { return a.selectedPlan(s, facts) })
		if err != nil {
			return err
		}
	}
	if !reviewMatches(styled, facts, "new") || !planRevisionMatches(facts) {
		return agent.ErrNoPlan
	}
	key, wanted := "y", "approved"
	if choice == pb.PlanChoice_PLAN_CHOICE_KEEP_PLANNING {
		key, wanted = "n", "rejected"
	}
	if err := t.Write([]byte(key)); err != nil {
		return agent.ErrDeliveryUnconfirmed
	}
	complete := func(screen string) bool {
		if observed != nil {
			return observed()
		}
		scr := a.Analyze(screen)
		return scr.ComposerReady && scr.Popup == "" && !scr.Busy
	}
	// A single artifact may submit immediately. With a list, y/n stages a
	// choice and exposes Submit/Dismiss; no other row may change with ours.
	styled, err = agent.WaitFor(t, "native plan choice or completion", func(s string) bool {
		return complete(s) || reviewMatches(s, facts, wanted) && strings.Contains(planPanel(s), "Submit") && strings.Contains(planPanel(s), "Dismiss")
	})
	if err != nil {
		return agent.ErrDeliveryUnconfirmed
	}
	if complete(styled) {
		return nil
	}
	if !reviewMatches(styled, facts, wanted) || !planRevisionMatches(facts) {
		return agent.ErrDeliveryUnconfirmed
	}
	if !submitFocused(styled) {
		if !strings.Contains(planPanel(styled), "esc Done") {
			return agent.ErrDeliveryUnconfirmed
		}
		if err := t.Write([]byte(agent.KeyEsc)); err != nil {
			return agent.ErrDeliveryUnconfirmed
		}
		styled, err = agent.WaitFor(t, "native review Submit focus", func(s string) bool { return complete(s) || submitFocused(s) })
		if err != nil {
			return agent.ErrDeliveryUnconfirmed
		}
		if complete(styled) {
			return nil
		}
	}
	if !reviewMatches(styled, facts, wanted) || !planRevisionMatches(facts) || !submitFocused(styled) {
		return agent.ErrDeliveryUnconfirmed
	}
	if err := t.Write([]byte(agent.KeyEnter)); err != nil {
		return agent.ErrDeliveryUnconfirmed
	}
	_, err = agent.WaitFor(t, "native plan submission", complete)
	if err != nil {
		return agent.ErrDeliveryUnconfirmed
	}
	return nil
}
