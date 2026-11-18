package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/antigravity"
	"github.com/Lewin671/remote-agent/internal/claude"
	"github.com/Lewin671/remote-agent/internal/client"
	"github.com/Lewin671/remote-agent/internal/codex"
	"github.com/Lewin671/remote-agent/internal/protocol"
)

func captureWarnLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestSessionStartHookGuardsUntrustedSources(t *testing.T) {
	t.Run("claude rejects untrusted path and enforces source existence", func(t *testing.T) {
		logs := captureWarnLogs(t)
		claudeHome := t.TempDir()
		h := claudeHarness(t, Config{Agents: map[pb.Agent]AgentConfig{
			pb.Agent_AGENT_CLAUDE: {Bin: "/bin/true", Home: claudeHome},
		}})
		realPath := filepath.Join(claudeHome, "projects", "-tmp-work", "cc-1.jsonl")
		if err := os.MkdirAll(filepath.Dir(realPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(realPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := h.s.hook(context.Background(), &agent.Hook{
			HookEventName:  "SessionStart",
			SessionID:      "cc-1",
			TranscriptPath: realPath,
			Source:         "startup",
		}); err != nil {
			t.Fatal(err)
		}
		if h.s.meta.RolloutPath != realPath {
			t.Fatalf("expected real path %q, got %q", realPath, h.s.meta.RolloutPath)
		}

		// Untrusted path outside ~/.claude/projects/ is ignored with WARN.
		evilPath := "/tmp/ra-pushlat/rollout-unknown.jsonl"
		if _, err := h.s.hook(context.Background(), &agent.Hook{
			HookEventName:  "SessionStart",
			SessionID:      "evil-id",
			TranscriptPath: evilPath,
			Source:         "startup",
		}); err != nil {
			t.Fatal(err)
		}
		if h.s.meta.RolloutPath != realPath || h.s.meta.CodexSessionID != "cc-1" {
			t.Fatalf("untrusted SessionStart mutated session: path=%q id=%q", h.s.meta.RolloutPath, h.s.meta.CodexSessionID)
		}
		if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), evilPath) {
			t.Fatalf("expected WARN with path, got %q", logs.String())
		}

		// Resume to a non-existent file is rejected; startup pre-creation path under projects/ is allowed.
		futurePath := filepath.Join(claudeHome, "projects", "-tmp-work", "cc-2.jsonl")
		if _, err := h.s.hook(context.Background(), &agent.Hook{
			HookEventName:  "SessionStart",
			SessionID:      "cc-2",
			TranscriptPath: futurePath,
			Source:         "resume",
		}); err != nil {
			t.Fatal(err)
		}
		if h.s.meta.RolloutPath != realPath {
			t.Fatalf("resume to missing file should be rejected, got %q", h.s.meta.RolloutPath)
		}
		if _, err := h.s.hook(context.Background(), &agent.Hook{
			HookEventName:  "SessionStart",
			SessionID:      "cc-2",
			TranscriptPath: futurePath,
			Source:         "startup",
		}); err != nil {
			t.Fatal(err)
		}
		if h.s.meta.RolloutPath != futurePath {
			t.Fatalf("startup pre-creation path not bound: got %q want %q", h.s.meta.RolloutPath, futurePath)
		}
	})

	t.Run("codex requires existing file under sessions", func(t *testing.T) {
		logs := captureWarnLogs(t)
		codexHome := t.TempDir()
		h := newHarness(t, Config{Agents: map[pb.Agent]AgentConfig{
			pb.Agent_AGENT_CODEX: {Bin: "/bin/true", Home: codexHome},
		}})
		missingInSessions := filepath.Join(codexHome, "sessions", "2026", "11", "18", "rollout-missing.jsonl")
		if _, err := h.s.hook(context.Background(), &agent.Hook{
			HookEventName:  "SessionStart",
			SessionID:      "codex-1",
			TranscriptPath: missingInSessions,
		}); err != nil {
			t.Fatal(err)
		}
		if h.s.meta.RolloutPath != "" || !strings.Contains(logs.String(), "level=WARN") {
			t.Fatalf("missing codex rollout should be ignored with WARN: path=%q logs=%q", h.s.meta.RolloutPath, logs.String())
		}
		outside := filepath.Join(codexHome, "rollout-outside.jsonl")
		if err := os.WriteFile(outside, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := h.s.hook(context.Background(), &agent.Hook{
			HookEventName:  "SessionStart",
			SessionID:      "codex-1",
			TranscriptPath: outside,
		}); err != nil {
			t.Fatal(err)
		}
		if h.s.meta.RolloutPath != "" {
			t.Fatalf("codex rollout outside sessions/ was bound: %q", h.s.meta.RolloutPath)
		}
		valid := filepath.Join(codexHome, "sessions", "2026", "11", "18", "rollout-1.jsonl")
		if err := os.MkdirAll(filepath.Dir(valid), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(valid, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := h.s.hook(context.Background(), &agent.Hook{
			HookEventName:  "SessionStart",
			SessionID:      "codex-1",
			TranscriptPath: valid,
		}); err != nil {
			t.Fatal(err)
		}
		if h.s.meta.RolloutPath != valid {
			t.Fatalf("valid codex rollout not bound: got %q want %q", h.s.meta.RolloutPath, valid)
		}
	})

	t.Run("tmux pane ancestry rejects hooks from outside pane", func(t *testing.T) {
		logs := captureWarnLogs(t)
		claudeHome := t.TempDir()
		h := claudeHarness(t, Config{Agents: map[pb.Agent]AgentConfig{
			pb.Agent_AGENT_CLAUDE: {Bin: "/bin/true", Home: claudeHome},
		}})
		h.term.panePID = 1000
		parents := map[int]int{5000: 4000, 4000: 1000, 1000: 1, 9000: 8000, 8000: 1}
		h.d.parentPID = func(pid int) (int, bool) {
			ppid, ok := parents[pid]
			return ppid, ok
		}
		validPath := filepath.Join(claudeHome, "projects", "-tmp-work", "cc-pane.jsonl")
		if err := os.MkdirAll(filepath.Dir(validPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(validPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}

		// Outside pane PID 9000 is rejected even with a valid transcript path.
		if _, err := h.s.hook(withHookPeerPID(context.Background(), 9000), &agent.Hook{
			HookEventName:  "SessionStart",
			SessionID:      "cc-pane",
			TranscriptPath: validPath,
		}); err != nil {
			t.Fatal(err)
		}
		if h.s.meta.RolloutPath != "" || !strings.Contains(logs.String(), "outside session tmux pane") {
			t.Fatalf("outside pane hook was not rejected: path=%q logs=%q", h.s.meta.RolloutPath, logs.String())
		}

		// Inside pane PID 5000 (5000 -> 4000 -> 1000) is accepted.
		if _, err := h.s.hook(withHookPeerPID(context.Background(), 5000), &agent.Hook{
			HookEventName:  "SessionStart",
			SessionID:      "cc-pane",
			TranscriptPath: validPath,
		}); err != nil {
			t.Fatal(err)
		}
		if h.s.meta.RolloutPath != validPath {
			t.Fatalf("inside pane hook was not accepted: path=%q", h.s.meta.RolloutPath)
		}
	})

	t.Run("unix socket peer pid enforced end-to-end", func(t *testing.T) {
		logs := captureWarnLogs(t)
		claudeHome := t.TempDir()
		h := claudeHarness(t, Config{Agents: map[pb.Agent]AgentConfig{
			pb.Agent_AGENT_CLAUDE: {Bin: "/bin/true", Home: claudeHome},
		}})
		h.d.sessions[h.s.meta.ID] = h.s

		dir, err := os.MkdirTemp("/tmp", "ra-peer-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(dir)
		sock := filepath.Join(dir, "d.sock")
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		mux.Handle(remoteagentv1connect.NewLocalServiceHandler(&handler{d: h.d}, connect.WithInterceptors(protocol.Local())))
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- serveLocal(ctx, ln, mux) }()
		defer func() {
			cancel()
			<-done
		}()

		validPath := filepath.Join(claudeHome, "projects", "-tmp-work", "cc-sock.jsonl")
		if err := os.MkdirAll(filepath.Dir(validPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(validPath, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(map[string]string{
			"hook_event_name": "SessionStart",
			"session_id":      "cc-sock",
			"transcript_path": validPath,
		})

		c := client.New(sock)
		// Unreachable pane PID must reject our process's socket connection.
		h.term.mu.Lock()
		h.term.panePID = os.Getpid() + 100000
		h.term.mu.Unlock()
		if _, err := c.Hook(ctx, connect.NewRequest(&pb.HookRequest{SessionId: h.s.meta.ID, Payload: payload})); err != nil {
			t.Fatal(err)
		}
		if h.s.meta.RolloutPath != "" || !strings.Contains(logs.String(), "outside session tmux pane") {
			t.Fatalf("mismatched peer pid accepted: path=%q logs=%q", h.s.meta.RolloutPath, logs.String())
		}

		// Matching pane PID accepts our process's socket connection.
		h.term.mu.Lock()
		h.term.panePID = os.Getpid()
		h.term.mu.Unlock()
		if _, err := c.Hook(ctx, connect.NewRequest(&pb.HookRequest{SessionId: h.s.meta.ID, Payload: payload})); err != nil {
			t.Fatal(err)
		}
		if h.s.meta.RolloutPath != validPath {
			t.Fatalf("matching peer pid rejected: path=%q", h.s.meta.RolloutPath)
		}
	})
}

func TestRolloutSwitchABANoDuplicateAndNewConversationWorks(t *testing.T) {
	countUserAndAssistant := func(evs []*pb.Event) (users, agents []string) {
		for _, e := range evs {
			if u := e.GetUserMessage(); u != nil {
				users = append(users, u.Text)
			}
			if a := e.GetAgentMessage(); a != nil {
				agents = append(agents, a.Text)
			}
		}
		return users, agents
	}
	appendLines := func(t *testing.T, path string, lines ...string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		for _, l := range lines {
			if _, err := f.WriteString(l + "\n"); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("claude A->B->A across restart and new conversation", func(t *testing.T) {
		home := t.TempDir()
		h := claudeHarness(t, Config{Agents: map[pb.Agent]AgentConfig{
			pb.Agent_AGENT_CLAUDE: {Bin: "/bin/true", Home: home},
		}})
		ccTurn := func(prompt, reply string) []string {
			return []string{
				`{"type":"user","promptId":"p-` + prompt + `","origin":{"kind":"human"},"permissionMode":"default","message":{"role":"user","content":` + mustJSON(prompt) + `}}`,
				`{"type":"assistant","message":{"id":"m-` + reply + `","role":"assistant","content":[{"type":"text","text":` + mustJSON(reply) + `}]}}`,
				`{"type":"system","subtype":"turn_duration","durationMs":10}`,
			}
		}
		pathA := filepath.Join(home, "projects", "-tmp-w", "conv-a.jsonl")
		pathB := filepath.Join(home, "projects", "-tmp-w", "conv-b.jsonl")
		pathC := filepath.Join(home, "projects", "-tmp-w", "conv-c.jsonl")

		appendLines(t, pathA, ccTurn("u1", "a1")...)
		if _, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "SessionStart", SessionID: "conv-a", TranscriptPath: pathA, Source: "startup"}); err != nil {
			t.Fatal(err)
		}
		h.s.readRollout()

		// Switch to B (/clear).
		appendLines(t, pathB, ccTurn("u2", "a2")...)
		if _, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "SessionStart", SessionID: "conv-b", TranscriptPath: pathB, Source: "clear"}); err != nil {
			t.Fatal(err)
		}
		h.s.readRollout()

		// Switch back to A (resume). Must not duplicate u1/a1.
		if _, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "SessionStart", SessionID: "conv-a", TranscriptPath: pathA, Source: "resume"}); err != nil {
			t.Fatal(err)
		}
		h.s.readRollout()
		users, agents := countUserAndAssistant(h.events())
		if len(users) != 2 || len(agents) != 2 {
			t.Fatalf("duplicate events on A->B->A: users=%v agents=%v", users, agents)
		}

		// New lines appended to A after switching back are read once.
		appendLines(t, pathA, ccTurn("u3", "a3")...)
		h.s.readRollout()
		users, agents = countUserAndAssistant(h.events())
		if len(users) != 3 || len(agents) != 3 {
			t.Fatalf("new turn on A not read: users=%v agents=%v", users, agents)
		}

		// Switch to B, restart daemon, then switch back to A: still no duplicate.
		if _, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "SessionStart", SessionID: "conv-b", TranscriptPath: pathB, Source: "resume"}); err != nil {
			t.Fatal(err)
		}
		m := h.s.meta
		_ = h.s.log.Close()
		h.open(m)
		if _, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "SessionStart", SessionID: "conv-a", TranscriptPath: pathA, Source: "resume"}); err != nil {
			t.Fatal(err)
		}
		h.s.readRollout()
		users, agents = countUserAndAssistant(h.events())
		if len(users) != 3 || len(agents) != 3 {
			t.Fatalf("duplicate events after restart A->B->A: users=%v agents=%v", users, agents)
		}

		// Real new conversation C emits its events.
		appendLines(t, pathC, ccTurn("u4", "a4")...)
		if _, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "SessionStart", SessionID: "conv-c", TranscriptPath: pathC, Source: "resume"}); err != nil {
			t.Fatal(err)
		}
		h.s.readRollout()
		users, agents = countUserAndAssistant(h.events())
		if len(users) != 4 || len(agents) != 4 {
			t.Fatalf("new conversation C events missing: users=%v agents=%v", users, agents)
		}
	})

	t.Run("codex A->B->A and new conversation", func(t *testing.T) {
		home := t.TempDir()
		h := newHarness(t, Config{Agents: map[pb.Agent]AgentConfig{
			pb.Agent_AGENT_CODEX: {Bin: "/bin/true", Home: home},
		}})
		cxTurn := func(turn, prompt, reply string) []string {
			return []string{
				`{"timestamp":"2026-11-18T00:00:00Z","type":"event_msg","payload":{"type":"task_started","turn_id":` + mustJSON(turn) + `}}`,
				`{"timestamp":"2026-11-18T00:00:01Z","type":"event_msg","payload":{"type":"item_completed","turn_id":` + mustJSON(turn) + `,"item":{"type":"UserMessage","id":"u-` + turn + `","content":[{"type":"text","text":` + mustJSON(prompt) + `}]}}}`,
				`{"timestamp":"2026-11-18T00:00:02Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","id":` + mustJSON(turn) + `,"content":[{"type":"Text","text":` + mustJSON(reply) + `}],"phase":"final_answer"}}}`,
				`{"timestamp":"2026-11-18T00:00:03Z","type":"event_msg","payload":{"type":"task_complete","turn_id":` + mustJSON(turn) + `}}`,
			}
		}
		pathA := filepath.Join(home, "sessions", "2026", "11", "18", "rollout-a.jsonl")
		pathB := filepath.Join(home, "sessions", "2026", "11", "18", "rollout-b.jsonl")

		appendLines(t, pathA, cxTurn("t1", "u1", "a1")...)
		if _, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "SessionStart", SessionID: "cx-a", TranscriptPath: pathA}); err != nil {
			t.Fatal(err)
		}
		h.s.readRollout()

		appendLines(t, pathB, cxTurn("t2", "u2", "a2")...)
		if _, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "SessionStart", SessionID: "cx-b", TranscriptPath: pathB}); err != nil {
			t.Fatal(err)
		}
		h.s.readRollout()

		if _, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "SessionStart", SessionID: "cx-a", TranscriptPath: pathA}); err != nil {
			t.Fatal(err)
		}
		h.s.readRollout()
		users, agents := countUserAndAssistant(h.events())
		if len(users) != 2 || len(agents) != 2 {
			t.Fatalf("codex duplicate events on A->B->A: users=%v agents=%v", users, agents)
		}

		appendLines(t, pathA, cxTurn("t3", "u3", "a3")...)
		h.s.readRollout()
		users, agents = countUserAndAssistant(h.events())
		if len(users) != 3 || len(agents) != 3 {
			t.Fatalf("codex new turn on A missing: users=%v agents=%v", users, agents)
		}
	})

	t.Run("antigravity A->B->A", func(t *testing.T) {
		home := t.TempDir()
		h := agyHarness(t)
		a := antigravity.New("/bin/true", home)
		h.d.agents[a.Kind()] = a
		h.s.ag = a
		h.s.parser = a.NewParser()
		h.s.meta.CodexSessionID = ""

		agyTurn := func(prompt, reply string) []string {
			return []string{
				`{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","status":"DONE","created_at":"2026-11-18T00:00:00Z","content":` + mustJSON("<USER_REQUEST>"+prompt+"</USER_REQUEST>") + `}`,
				`{"step_index":1,"source":"MODEL","type":"PLANNER_RESPONSE","status":"DONE","created_at":"2026-11-18T00:00:01Z","content":` + mustJSON(reply) + `}`,
			}
		}
		pathA := filepath.Join(home, "brain", "agy-a", ".system_generated", "logs", "transcript.jsonl")
		pathB := filepath.Join(home, "brain", "agy-b", ".system_generated", "logs", "transcript.jsonl")

		appendLines(t, pathA, agyTurn("u1", "a1")...)
		if _, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "SessionStart", SessionID: "agy-a", TranscriptPath: pathA}); err != nil {
			t.Fatal(err)
		}
		h.s.readRollout()

		appendLines(t, pathB, agyTurn("u2", "a2")...)
		if _, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "SessionStart", SessionID: "agy-b", TranscriptPath: pathB}); err != nil {
			t.Fatal(err)
		}
		h.s.readRollout()

		if _, err := h.s.hook(context.Background(), &agent.Hook{HookEventName: "SessionStart", SessionID: "agy-a", TranscriptPath: pathA}); err != nil {
			t.Fatal(err)
		}
		h.s.readRollout()
		var users, replies []string
		for _, e := range h.events() {
			if u := e.GetUserMessage(); u != nil {
				users = append(users, u.Text)
			}
			if a := e.GetAgentMessage(); a != nil && a.Phase != "commentary" {
				replies = append(replies, a.Text)
			}
		}
		if len(users) != 2 || len(replies) != 2 {
			t.Fatalf("antigravity duplicate events on A->B->A: users=%v replies=%v", users, replies)
		}
	})

	// Ensure unused imports don't creep in if claude/codex helpers are only referenced via Config.
	_ = claude.SettingsFile
	_ = codex.SafeValue
}

func mustJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
