package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

func TestCommitFailureDoesNotProjectSettings(t *testing.T) {
	h, footer := controlHarness(t)
	h.s.log.Close()
	err := h.s.setModel("gpt-6-luna", "high")
	if err == nil || !strings.Contains(err.Error(), "already applied on the computer") {
		t.Fatalf("error: %v", err)
	}
	if !strings.Contains(*footer, "GPT-6-Luna high") {
		t.Fatal("CLI side effect did not happen")
	}
	if h.s.info().Model == "gpt-6-luna" {
		t.Fatal("uncommitted settings projected")
	}
	if h.s.storageFault == "" {
		t.Fatal("storage fault invisible")
	}
}

func TestCommitFailureModeAndPlanReportSideEffect(t *testing.T) {
	for _, op := range []string{"mode", "plan"} {
		t.Run(op, func(t *testing.T) {
			h, _ := controlHarness(t)
			if op == "plan" {
				h.term.setScreen("  Implement this plan?\n\x1b[1;7m› 1. Yes, implement this plan  Switch\n\x1b[0m  2. Yes, clear context  Fresh\n  3. No, stay in Plan mode  Continue\n")
			}
			h.s.log.Close()
			var err error
			if op == "mode" {
				err = h.s.setApprovalMode(pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS)
			} else {
				err = h.s.answerPlan(pb.PlanChoice_PLAN_CHOICE_IMPLEMENT)
			}
			if err == nil || !strings.Contains(err.Error(), "already applied on the computer") {
				t.Fatalf("error: %v", err)
			}
		})
	}
}

func TestCommitApprovalRequestFailureDoesNotWait(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.log.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := h.s.permission(ctx, &agent.Hook{ToolName: "shell", ToolInput: []byte(`{"command":"true"}`)})
	if err != nil || !strings.Contains(string(out), `"behavior":"deny"`) {
		t.Fatalf("request failure: %v", err)
	}
	if h.s.info().PendingApprovals != 0 {
		t.Fatal("uncommitted request published")
	}
}

func TestCommitApprovalResultFailureLeavesRequestPending(t *testing.T) {
	h := newHarness(t, Config{})
	a := &approval{id: "a", decision: make(chan decision, 1), deadline: time.Now().Add(time.Minute)}
	h.s.approvals[a.id] = a
	h.s.log.Close()
	if err := h.s.resolve(a.id, true, ""); err == nil {
		t.Fatal("false success")
	}
	if a.claimed || h.s.info().PendingApprovals != 1 || len(a.decision) != 0 {
		t.Fatal("uncommitted result reached hook or changed pending state")
	}
}

func TestCommitQueueFailureRetainsAcceptedInput(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.log.Close()
	_, err := h.s.send("one", "r1")
	if !errors.Is(err, errQueueAccepted) {
		t.Fatalf("send: %v", err)
	}
	q, err := loadQueue(h.dir)
	if err != nil || len(q.Queue) != 1 || q.Queue[0].RequestID != "r1" {
		t.Fatalf("queue lost: %v %v", q, err)
	}
	if _, err = h.s.send("one", "r1"); err != nil {
		t.Fatal(err)
	}
	if len(h.s.queue.Queue) != 1 {
		t.Fatal("retry duplicated input")
	}
}

func TestCommitHookFailureDoesNotMarkReported(t *testing.T) {
	h := claudeHarness(t, Config{})
	h.s.log.Close()
	p := &agent.Hook{HookEventName: "PreToolUse", ToolName: "AskUserQuestion", ToolUseID: "q", ToolInput: []byte(`{"questions":[{"question":"Choose","options":[{"label":"A"}]}]}`)}
	if _, err := h.s.hook(context.Background(), p); err == nil {
		t.Fatal("false success")
	}
	e := &pb.Event{Kind: &pb.Event_QuestionAsked{QuestionAsked: &pb.QuestionAsked{QuestionId: "q"}}}
	if h.s.parser.Reported(e) || h.s.info().PendingQuestions != 0 {
		t.Fatal("uncommitted hook projected")
	}
}

func TestCommitStageFailuresStopBeforeTerminalWrite(t *testing.T) {
	for _, stage := range []string{"images", "submit"} {
		t.Run(stage, func(t *testing.T) {
			h := newHarness(t, Config{})
			if _, err := h.s.send("one", "r1"); err != nil {
				t.Fatal(err)
			}
			h.s.log.Close()
			var err error
			if stage == "images" {
				err = h.s.setStage("r1", pb.InputStage_INPUT_STAGE_IMAGES_RECOGNIZED, nil)
			} else {
				err = h.s.markInflight(0, "r1")
			}
			if err == nil {
				t.Fatal("uncommitted stage returned success")
			}
			if len(h.term.written()) != 0 {
				t.Fatal("terminal received input")
			}
		})
	}
}

func TestCommitStopReportsExitDespiteStorageFailure(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.log.Close()
	if err := h.s.kill(); err == nil || !strings.Contains(err.Error(), "already exited") {
		t.Fatalf("stop: %v", err)
	}
	if h.term.Alive() {
		t.Fatal("CLI still alive")
	}
}

func TestCommitResumeInputFailureIsReported(t *testing.T) {
	h := newHarness(t, Config{})
	h.s.queue.Queue = []input{{Text: "one", RequestID: "r1", Paused: true, Mode: pb.SendMode_SEND_MODE_QUEUE}}
	h.s.log.Close()
	if err := h.s.resumeInputBy("r1", originLocal); err == nil {
		t.Fatal("uncommitted resume returned success")
	}
}

func TestCommitRequeuedInputFailureIsReported(t *testing.T) {
	h := newHarness(t, Config{})
	in := input{Text: "one", RequestID: "r1", Paused: true, Mode: pb.SendMode_SEND_MODE_STEER}
	h.s.queue.Queue = []input{in}
	h.s.log.Close()
	if err := h.s.releaseRequeued([]input{in}); err == nil {
		t.Fatal("uncommitted release returned success")
	}
}

func TestCommitApprovalReplyPreservesTurn(t *testing.T) {
	h := newHarness(t, Config{})
	a := &approval{id: "a", turnID: "turn", decision: make(chan decision, 1), deadline: time.Now().Add(time.Minute)}
	h.s.approvals[a.id] = a
	if err := h.s.resolve(a.id, true, ""); err != nil {
		t.Fatal(err)
	}
	evs := h.events()
	if len(evs) != 1 || evs[0].TurnId != "turn" || evs[0].GetApprovalResolved().GetApprovalId() != "a" {
		t.Fatalf("reply: %v", evs)
	}
	if !a.claimed || !(<-a.decision).recorded {
		t.Fatal("reply not committed before handoff")
	}
}
