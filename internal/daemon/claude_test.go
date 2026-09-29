package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

// Claude Code 2.1.284's input box and permission prompt (claude-code-notes §4, §6).
const (
	ccRule   = "\x1b[38;5;244m────────────────────────────────────────────────────"
	ccIdle   = ccRule + "\n\x1b[39m❯ \x1b[7m \x1b[0m\n" + ccRule + "\n   Haiku 4.5\n  ⏸ manual mode on · ← for agents\n"
	ccPrompt = "⏺ Bash(touch /w/x)\n" + ccRule + "\n Bash command\n   touch /w/x\n Do you want to proceed?\n" +
		" \x1b[38;5;153m❯\x1b[39m 1. Yes\n   2. Yes, and always allow access to /w from this project\n   3. No\n Esc to cancel · Tab to amend\n"
)

func claudeHarness(t *testing.T, cfg Config) *harness {
	h := newHarness(t, cfg)
	m := h.s.meta
	m.Agent = pb.Agent_AGENT_CLAUDE
	h.s.log.Close()
	h.term.setScreen(ccIdle)
	h.open(m)
	return h
}

func claudePermission(tool, input string) *agent.Hook {
	return &agent.Hook{HookEventName: "PermissionRequest", SessionID: "cc-1", ToolName: tool, ToolInput: json.RawMessage(input)}
}

// Claude Code shows its own prompt while the hook waits. Answered there
// first, the client's card is withdrawn and the hook prints nothing.
func TestClaudeApprovalAnsweredInTerminal(t *testing.T) {
	h := claudeHarness(t, Config{ApprovalTimeout: time.Minute})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.s.start(ctx)
	eventually(t, "idle", func() bool { return h.s.info().Status == pb.SessionStatus_SESSION_STATUS_IDLE })

	out := make(chan []byte, 1)
	go func() {
		b, _ := h.s.hook(ctx, claudePermission("Bash", `{"command":"touch /w/x","description":"Create x"}`))
		out <- b
	}()
	eventually(t, "the card", func() bool { return h.s.info().PendingApprovals == 1 })
	h.term.setScreen(ccPrompt)
	eventually(t, "the prompt seen", func() bool {
		h.s.mu.Lock()
		defer h.s.mu.Unlock()
		for _, a := range h.s.approvals {
			return a.seen
		}
		return false
	})
	// Its own prompt for a forwarded request does not block the session.
	if st := h.s.info().Status; st != pb.SessionStatus_SESSION_STATUS_IDLE {
		t.Fatalf("status %v", st)
	}
	h.term.setScreen(ccIdle)
	select {
	case b := <-out:
		if b != nil {
			t.Fatalf("hook printed %s", b)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the hook still waits")
	}
	var got pb.ApprovalDecision
	for _, e := range h.events() {
		if r := e.GetApprovalResolved(); r != nil {
			got = r.Decision
		}
	}
	if got != pb.ApprovalDecision_APPROVAL_DECISION_TERMINAL {
		t.Fatalf("decision %v", got)
	}
}

// A request still waiting when the turn ends was settled in the terminal.
func TestClaudeApprovalWithdrawnAtTurnEnd(t *testing.T) {
	h := claudeHarness(t, Config{ApprovalTimeout: time.Minute})
	path := filepath.Join(h.dir, "rollout.jsonl")
	h.writeRollout(`{"type":"user","promptId":"p1","origin":{"kind":"human"},"permissionMode":"default","message":{"role":"user","content":"go"}}`)
	h.s.bindRollout("cc-1", path)
	out := make(chan []byte, 1)
	go func() {
		b, _ := h.s.hook(context.Background(), claudePermission("Write", `{"file_path":"/w/a.txt","content":"x"}`))
		out <- b
	}()
	eventually(t, "the card", func() bool { return h.s.info().PendingApprovals == 1 })
	h.writeRollout(`{"type":"user","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user for tool use]"}]}}`)
	h.s.readRollout()
	select {
	case b := <-out:
		if b != nil {
			t.Fatalf("hook printed %s", b)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the hook still waits")
	}
}

// AskUserQuestion and ExitPlanMode ask for permission too; they are a
// question and a plan, not approvals.
func TestClaudeQuestionIsNotAnApproval(t *testing.T) {
	h := claudeHarness(t, Config{ApprovalTimeout: time.Minute})
	for _, tool := range []string{"AskUserQuestion", "ExitPlanMode"} {
		out, err := h.s.hook(context.Background(), claudePermission(tool, `{}`))
		if err != nil || out != nil {
			t.Fatalf("%s: %s %v", tool, out, err)
		}
	}
	for _, e := range h.events() {
		if e.GetApprovalRequested() != nil {
			t.Fatal("forwarded as an approval")
		}
	}
	info := h.s.info()
	if info.Agent != pb.Agent_AGENT_CLAUDE {
		t.Fatalf("agent %v", info.Agent)
	}
}

// A mode switched in the terminal is recorded from the screen.
func TestClaudeModeFromScreen(t *testing.T) {
	h := claudeHarness(t, Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.s.start(ctx)
	eventually(t, "manual", func() bool { return h.s.info().Mode == pb.ApprovalMode_APPROVAL_MODE_ASK })
	h.term.setScreen(strings.Replace(ccIdle, "⏸ manual mode on", "⏸ plan mode on (shift+tab to cycle)", 1))
	eventually(t, "plan", func() bool {
		i := h.s.info()
		return i.Mode == pb.ApprovalMode_APPROVAL_MODE_PLAN && i.PermissionMode == "plan"
	})
	h.term.setScreen(strings.Replace(ccIdle, "⏸ manual mode on", "⏵⏵ accept edits on (shift+tab to cycle)", 1))
	eventually(t, "accept edits", func() bool {
		i := h.s.info()
		return i.Mode == pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED && i.PermissionMode == "acceptEdits"
	})
}

// Machines list the CLIs installed, and Claude Code's settings defaults
// apply to its sessions.
func TestInstalledAgentsAndClaudeDefaults(t *testing.T) {
	h := newHarness(t, Config{CodexBin: "/nonexistent/codex"})
	if a := h.d.MachineMeta().Agents; len(a) != 1 || a[0].Agent != pb.Agent_AGENT_CODEX {
		t.Fatalf("agents %v", a)
	}
	d := New(Config{CodexBin: "/nonexistent/codex", ClaudeBin: "/nonexistent/claude", ClaudeHome: t.TempDir()})
	if a := d.MachineMeta().Agents; len(a) != 2 || a[1].Agent != pb.Agent_AGENT_CLAUDE {
		t.Fatalf("agents %v", a)
	}
	if _, err := h.d.updateSettings(&pb.DaemonSettings{Claude: &pb.AgentDefaults{Effort: "ultra"}}); err == nil {
		t.Fatal("accepted an effort Claude Code does not have")
	}
	if _, err := h.d.updateSettings(&pb.DaemonSettings{DefaultModel: "gpt-6-luna", Claude: &pb.AgentDefaults{Model: "haiku", Mode: pb.ApprovalMode_APPROVAL_MODE_ASK}}); err != nil {
		t.Fatal(err)
	}
	args, err := h.d.sessionArgs(h.d.adapter(pb.Agent_AGENT_CLAUDE), &pb.CreateSessionRequest{Agent: pb.Agent_AGENT_CLAUDE})
	if err != nil || strings.Join(args, " ") != "--model haiku --permission-mode default" {
		t.Fatalf("claude defaults %q %v", args, err)
	}
	args, _ = h.d.sessionArgs(h.d.adapter(pb.Agent_AGENT_CODEX), &pb.CreateSessionRequest{})
	if strings.Join(args, " ") != "-m gpt-6-luna" {
		t.Fatalf("codex defaults %q", args)
	}
}

// A question the transcript already had is not reported again by the
// PreToolUse hook, nor the other way round.
func TestClaudeQuestionReportedOnce(t *testing.T) {
	h := claudeHarness(t, Config{})
	path := filepath.Join(h.dir, "rollout.jsonl")
	h.writeRollout(`{"type":"user","promptId":"p1","origin":{"kind":"human"},"message":{"role":"user","content":"ask"}}`,
		`{"type":"assistant","message":{"id":"m1","model":"claude-haiku-4-5-20251001","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t1","name":"AskUserQuestion","input":{"questions":[{"question":"Q?","options":[{"label":"A"}]}]}}]}}`)
	h.s.bindRollout("cc-1", path)
	pre := func(id, q string) {
		if _, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "PreToolUse", PromptID: "p1", TranscriptPath: path, ToolName: "AskUserQuestion", ToolUseID: id,
			ToolInput: json.RawMessage(`{"questions":[{"question":"` + q + `","options":[{"label":"A"}]}]}`)}); err != nil {
			t.Fatal(err)
		}
	}
	pre("t1", "Q?") // transcript first
	pre("t2", "R?") // hook first
	h.writeRollout(`{"type":"assistant","message":{"id":"m2","model":"claude-haiku-4-5-20251001","stop_reason":"tool_use","content":[{"type":"tool_use","id":"t2","name":"AskUserQuestion","input":{"questions":[{"question":"R?","options":[{"label":"A"}]}]}}]}}`)
	h.s.readRollout()
	n := map[string]int{}
	for _, e := range h.events() {
		if q := e.GetQuestionAsked(); q != nil {
			n[q.QuestionId]++
		}
	}
	if n["t1"] != 1 || n["t2"] != 1 {
		t.Fatalf("questions %v", n)
	}
}
