package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
)

type planAcceptance struct {
	ctx         context.Context
	client      *remoteclient.Client
	view        *remoteclient.Session
	sid, socket string
	currentName string
	events      func(uint64) []*pb.Event
	wait        func(string, func() bool) error
	send        func(string) error
	mode        func(pb.ApprovalMode) error
	sequence    func() uint64
	resolve     func(*pb.Event, bool) error
	refresh     func() bool
}

func (r *planAcceptance) pane() string { return "ra-" + r.sid[:8] + ":" }
func (r *planAcceptance) keys(keys ...string) error {
	return exec.Command("tmux", append([]string{"-L", r.socket, "send-keys", "-t", r.pane()}, keys...)...).Run()
}
func (r *planAcceptance) screen() (string, error) {
	b, e := exec.Command("tmux", "-L", r.socket, "capture-pane", "-p", "-e", "-t", r.pane()).Output()
	return string(b), e
}
func (r *planAcceptance) captureFixture(name string) error {
	dir := os.Getenv("RA_AGY_EVIDENCE_DIR")
	if dir == "" {
		return nil
	}
	screen, err := r.screen()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, name), []byte(screen), 0600)
}
func (r *planAcceptance) handleApprovals() {
	done := map[string]bool{}
	evs := r.events(0)
	for _, e := range evs {
		if a := e.GetApprovalResolved(); a != nil {
			done[a.ApprovalId] = true
		}
	}
	for _, e := range evs {
		if a := e.GetApprovalRequested(); a != nil && !done[a.ApprovalId] {
			_ = r.resolve(e, r.allowedFixtureOperation(a))
		}
	}
}
func (r *planAcceptance) waitEvent(after uint64, what string, match func(*pb.Event) bool) (*pb.Event, error) {
	var found *pb.Event
	err := r.wait(what, func() bool {
		r.handleApprovals()
		for _, e := range r.events(after) {
			if match(e) {
				found = e
				return true
			}
		}
		return false
	})
	return found, err
}
func (r *planAcceptance) idle() error {
	return r.wait("native idle after plan step", func() bool {
		r.handleApprovals()
		if !r.refresh() || r.view.Info.Status != pb.SessionStatus_SESSION_STATUS_IDLE || r.view.Info.PendingApprovals != 0 {
			return false
		}
		screen, e := r.screen()
		if e != nil {
			return false
		}
		a := r.nativeScreen(screen)
		return a.ComposerReady && a.DraftEmpty && !a.Busy && a.Popup == ""
	})
}
func (r *planAcceptance) nativeScreen(s string) agent.Screen {
	// UI observation only; no test fixture or synthetic model response is used.
	return analyzeNative(s)
}
func (r *planAcceptance) planSequence() uint64 {
	var seq uint64
	pending := false
	for _, e := range r.events(0) {
		if e.GetPlanProposed() != nil {
			seq = e.Seq
			pending = false
		}
		if state := e.GetSessionState(); state != nil {
			if !state.PlanOpen {
				seq = 0
				pending = false
			} else if seq == 0 && !pending {
				seq = e.Seq
			}
		}
		if e.GetPlanAnswered() != nil {
			seq = 0
			pending = true
		}
	}
	return seq
}
func (r *planAcceptance) answer(choice pb.PlanChoice) error {
	screen, err := r.screen()
	if err != nil {
		return err
	}
	hash := sha256.Sum256(append([]byte("ra2/plan-screen\x00"), []byte(agent.StripANSI(screen))...))
	_, err = r.client.Do(r.ctx, *r.view, &pb.Command{Kind: &pb.Command_AnswerPlan{AnswerPlan: &pb.AnswerPlanRequest{SessionId: r.sid, Choice: choice, ObservedEventSeq: r.planSequence(), ObservedScreenHash: hash[:]}}})
	return err
}
func (r *planAcceptance) openReview() error {
	screen, err := r.screen()
	if err != nil {
		return err
	}
	if r.nativeScreen(screen).Popup != "artifacts" && r.nativeScreen(screen).Popup != "plan" {
		if err := r.keys("C-r"); err != nil {
			return err
		}
	}
	if err := r.wait("native artifact list", func() bool { s, e := r.screen(); return e == nil && strings.Contains(agent.StripANSI(s), "Artifacts") }); err != nil {
		return err
	}
	if r.currentName == "" {
		return nil
	}
	for i := 0; i < 40; i++ {
		s, e := r.screen()
		if e != nil {
			return e
		}
		plain := agent.StripANSI(s)
		if at := strings.LastIndex(plain, "Artifacts"); at >= 0 {
			plain = plain[at:]
		}
		for _, line := range strings.Split(plain, "\n") {
			trim := strings.TrimSpace(line)
			if (strings.HasPrefix(trim, "› ") || strings.HasPrefix(trim, "> ")) && strings.Contains(trim, r.currentName) {
				return nil
			}
		}
		if e := r.keys("Down"); e != nil {
			return e
		}
		time.Sleep(60 * time.Millisecond)
	}
	return fmt.Errorf("native artifact %s is not selectable", r.currentName)
}
func (r *planAcceptance) closeReview() error {
	s, e := r.screen()
	if e != nil {
		return e
	}
	if r.nativeScreen(s).Popup == "artifacts" || r.nativeScreen(s).Popup == "plan" {
		if e := r.keys("Escape"); e != nil {
			return e
		}
	}
	return nil
}
func (r *planAcceptance) propose(name string) (*pb.Event, error) {
	r.currentName = name
	if err := r.idle(); err != nil {
		return nil, err
	}
	if err := r.mode(pb.ApprovalMode_APPROVAL_MODE_PLAN); err != nil {
		return nil, err
	}
	seq := r.sequence()
	prompt := fmt.Sprintf("Create a short implementation plan artifact named %s using write_file with ArtifactMetadata RequestFeedback true. Its only proposed change is to add hello.txt containing hello. Stop after presenting the artifact. Do not implement anything until I approve it. When approved, implement with write_file only; do not run shell commands.", name)
	if err := r.send(prompt); err != nil {
		return nil, err
	}
	plan, err := r.waitEvent(seq, "specific plan artifact "+name, func(e *pb.Event) bool { return e.GetPlanProposed() != nil })
	if err != nil {
		return nil, err
	}
	if _, err := r.waitEvent(seq, "plan presentation turn completed", func(e *pb.Event) bool { return e.GetTurnEnded() != nil }); err != nil {
		return nil, err
	}
	return plan, nil
}
func (r *planAcceptance) run() error {
	if _, err := r.propose("acceptance_approve_plan.md"); err != nil {
		return err
	}
	if err := r.openReview(); err != nil {
		return err
	}
	if err := r.captureFixture("plan-approve-pending.txt"); err != nil {
		return err
	}
	seq := r.sequence()
	if err := r.answer(pb.PlanChoice_PLAN_CHOICE_IMPLEMENT); err != nil {
		return fmt.Errorf("encrypted plan approval: %w", err)
	}
	if _, err := r.waitEvent(seq, "approved plan event", func(e *pb.Event) bool { return e.GetPlanAnswered() != nil }); err != nil {
		return err
	}
	_ = r.closeReview()
	if err := r.idle(); err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(r.view.Info.Cwd, "hello.txt"))
	if err != nil || strings.TrimSpace(string(data)) != "hello" {
		return fmt.Errorf("approved plan did not create the expected isolated file")
	}
	fmt.Println("PASS specific plan artifact approved through encrypted Forward; native screen hash observed by debug capture")

	if _, err := r.propose("acceptance_reject_plan.md"); err != nil {
		return err
	}
	if err := r.openReview(); err != nil {
		return err
	}
	_ = r.captureFixture("plan-reject-pending.txt")
	seq = r.sequence()
	if err := r.answer(pb.PlanChoice_PLAN_CHOICE_KEEP_PLANNING); err != nil {
		return fmt.Errorf("encrypted plan rejection: %w", err)
	}
	if _, err := r.waitEvent(seq, "rejected plan event", func(e *pb.Event) bool { return e.GetPlanAnswered() != nil }); err != nil {
		return err
	}
	_ = r.closeReview()
	// Feedback is a subsequent human Send, admitted only after the native
	// rejection was consumed and its PlanAnswered was durably acknowledged.
	if err := r.send("Feedback on the rejected plan: make no further changes and reply exactly REJECT-FEEDBACK-ACK without tools."); err != nil {
		return err
	}
	if _, err := r.waitEvent(seq, "post-rejection feedback reply", func(e *pb.Event) bool { return strings.Contains(e.GetAgentMessage().GetText(), "REJECT-FEEDBACK-ACK") }); err != nil {
		return err
	}
	if err := r.idle(); err != nil {
		return err
	}
	fmt.Println("PASS native plan rejection precedes the separate follow-up feedback message")

	// Local first answer: use native keys, then prove the old remote action has
	// gone stale without sending another y/n to the next screen.
	if _, err := r.propose("acceptance_local_first_plan.md"); err != nil {
		return err
	}
	if err := r.openReview(); err != nil {
		return err
	}
	seq = r.sequence()
	if err := r.keys("y"); err != nil {
		return err
	}
	if _, err := r.waitEvent(seq, "local plan completion", func(e *pb.Event) bool { return e.GetPlanAnswered() != nil }); err != nil {
		return err
	}
	_ = r.captureFixture("plan-answered-locally.txt")
	if err := r.answer(pb.PlanChoice_PLAN_CHOICE_IMPLEMENT); err == nil {
		return fmt.Errorf("already locally answered plan accepted a remote answer")
	}
	_ = r.closeReview()
	if err := r.idle(); err != nil {
		return err
	}
	fmt.Println("PASS locally answered plan withdraws the old remote action")

	if _, err := r.propose("acceptance_edit_plan.md"); err != nil {
		return err
	}
	if err := r.openReview(); err != nil {
		return err
	}
	_ = r.captureFixture("plan-before-native-edit.txt")
	_ = r.closeReview()
	// Revise one presented artifact through the real CLI, without requesting
	// feedback again. No direct write under the user's agy directory is used.
	editSeq := r.sequence()
	if err := r.send("Revise acceptance_edit_plan.md in place: add the sentence REVISION-AFTER-PRESENTATION. Use edit_file or write_file with RequestFeedback false, and do not create any new feedback request. Then stop."); err != nil {
		return err
	}
	if _, err := r.waitEvent(editSeq, "artifact revision by native CLI", func(e *pb.Event) bool { return e.GetToolCall() != nil }); err != nil {
		return err
	}
	if _, err := r.waitEvent(editSeq, "artifact edit completed", func(e *pb.Event) bool { return e.GetTurnEnded() != nil }); err != nil {
		return err
	}
	if err := r.openReview(); err != nil {
		return err
	}
	_ = r.captureFixture("plan-edited-after-presentation.txt")
	if err := r.answer(pb.PlanChoice_PLAN_CHOICE_IMPLEMENT); err == nil {
		return fmt.Errorf("old presented revision remained remotely actionable")
	}
	_ = r.closeReview()
	fmt.Println("PASS edited artifacts do not reuse the presented revision's remote approval")
	// Clear this pending request locally; a fresh proposal is required for
	// remote actions after an unpresented revision.
	if err := r.openReview(); err != nil {
		return err
	}
	if err := r.keys("n"); err != nil {
		return err
	}
	_ = r.closeReview()
	if err := r.idle(); err != nil {
		return err
	}
	r.currentName = ""
	// Two simultaneously pending artifact identities must not be guessed.
	if err := r.mode(pb.ApprovalMode_APPROVAL_MODE_PLAN); err != nil {
		return err
	}
	seq = r.sequence()
	if err := r.send("Create two short plan artifacts acceptance_multi_a.md and acceptance_multi_b.md. Use write_file with ArtifactMetadata RequestFeedback true for EACH. Present both for review; do not implement anything."); err != nil {
		return err
	}
	var proposals []*pb.Event
	if err := r.wait("two pending plan artifacts", func() bool {
		r.handleApprovals()
		proposals = nil
		for _, e := range r.events(seq) {
			if e.GetPlanProposed() != nil {
				proposals = append(proposals, e)
			}
		}
		return len(proposals) >= 2
	}); err != nil {
		return err
	}
	if _, err := r.waitEvent(seq, "multiple-plan turn completed", func(e *pb.Event) bool { return e.GetTurnEnded() != nil }); err != nil {
		return err
	}
	if err := r.openReview(); err != nil {
		return err
	}
	_ = r.captureFixture("plan-multiple-artifacts.txt")
	if err := r.answer(pb.PlanChoice_PLAN_CHOICE_IMPLEMENT); err == nil {
		return fmt.Errorf("ambiguous multiple artifacts received a remote approval")
	}
	_ = r.closeReview()
	fmt.Println("PASS multiple pending artifacts stay terminal-only")

	// Leave pending artifacts to the native user; the test session is stopped
	// during cleanup and no ordinary account or conversation is touched.
	return nil
}

// Only operations needed for these synthetic fixtures are automatically
// answered. A real CLI remains responsible for presenting its native menu.
func (r *planAcceptance) allowedFixtureOperation(a *pb.ApprovalRequested) bool {
	if a.Command != "" {
		return a.Command == "pwd" || a.Command == "ls" || a.Command == "ls -la" || a.Command == "ls -l" || a.Command == "ls -a"
	}
	var in struct {
		Path string `json:"Path"`
	}
	if json.Unmarshal([]byte(a.ToolInputJson), &in) != nil || in.Path == "" {
		return false
	}
	path := in.Path
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.view.Info.Cwd, path)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return false
	}
	work, err := filepath.EvalSymlinks(r.view.Info.Cwd)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(work, parent)
	if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
		return true
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(r.view.Info.RolloutPath)))
	root, err = filepath.EvalSymlinks(root)
	return err == nil && parent == root && filepath.Ext(path) == ".md"
}
