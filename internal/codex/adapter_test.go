package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

func TestFlagArgs(t *testing.T) {
	args := []string{"-m", "gpt-6-luna", "--search", "-c", `a="b c"`, "fix the bug", "--profile", "work", "--add-dir=/x", "-s", "read-only", "--model=o", "--", "rest"}
	for _, c := range []struct {
		drop map[string]bool
		want []string
	}{
		{nil, []string{"-m", "gpt-6-luna", "--search", "-c", `a="b c"`, "--profile", "work", "--add-dir=/x", "-s", "read-only", "--model=o"}},
		{map[string]bool{"-m": true, "--model": true, "-s": true}, []string{"--search", "-c", `a="b c"`, "--profile", "work", "--add-dir=/x"}},
	} {
		if got := FlagArgs(args, c.drop, valueFlags); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("drop %v: got %q", c.drop, got)
		}
	}
}

func TestExactMode(t *testing.T) {
	for _, c := range []struct {
		ctx  *pb.TurnContext
		want pb.ApprovalMode
	}{
		{&pb.TurnContext{ApprovalsReviewer: "user", ApprovalPolicy: "on-request", Sandbox: "workspace-write"}, pb.ApprovalMode_APPROVAL_MODE_ASK},
		{&pb.TurnContext{ApprovalsReviewer: "auto_review", ApprovalPolicy: "on-request", Sandbox: "workspace-write"}, pb.ApprovalMode_APPROVAL_MODE_AUTO},
		{&pb.TurnContext{ApprovalsReviewer: "user", ApprovalPolicy: "never", Sandbox: "danger-full-access"}, pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS},
		{&pb.TurnContext{ApprovalsReviewer: "user", ApprovalPolicy: "on-request", Sandbox: "read-only"}, pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED},
		{&pb.TurnContext{ApprovalsReviewer: "user", ApprovalPolicy: "untrusted", Sandbox: "workspace-write"}, pb.ApprovalMode_APPROVAL_MODE_UNSPECIFIED},
	} {
		if got := exactMode(c.ctx); got != c.want {
			t.Errorf("%v: got %v, want %v", c.ctx, got, c.want)
		}
	}
}

// Resuming continues the conversation with the options it started with,
// minus the prompt, and the settings it last ran with.
func TestResumeArgv(t *testing.T) {
	a := New("codex", t.TempDir(), "")
	argv, err := a.Argv(&agent.Launch{
		Args:      []string{"-m", "gpt-6-sol", "-s", "read-only", "fix it"},
		SessionID: "019a",
		Resume:    true,
		Context:   &pb.TurnContext{Model: "gpt-6-luna", Effort: "high", ApprovalsReviewer: "auto_review", ApprovalPolicy: "on-request", Sandbox: "workspace-write"},
	})
	if err != nil {
		t.Fatal(err)
	}
	j := strings.Join(argv, " ")
	if argv[0] != "resume" || argv[len(argv)-1] != "019a" || strings.Contains(j, "fix it") || strings.Contains(j, "gpt-6-sol") ||
		!strings.Contains(j, "-m gpt-6-luna") || !strings.Contains(j, `approvals_reviewer="auto_review"`) || strings.Contains(j, "read-only") {
		t.Fatalf("argv %q", argv)
	}
	// Nothing was sent: start afresh with the same options.
	argv, _ = a.Argv(&agent.Launch{Args: []string{"-m", "gpt-6-sol"}, Resume: true})
	if argv[0] == "resume" || !strings.Contains(strings.Join(argv, " "), "-m gpt-6-sol") {
		t.Fatalf("argv %q", argv)
	}
}

func TestManagedMCPOnStartAndResume(t *testing.T) {
	a := New("codex", t.TempDir(), "")
	for _, resume := range []bool{false, true} {
		l := &agent.Launch{Exe: "/path with spaces/remote-agent", RemoteSession: "remote-id", Args: []string{"-m", "gpt-6-luna"}, Resume: resume, SessionID: "cli-id"}
		argv, err := a.Argv(l)
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(argv, "\n")
		if !strings.Contains(joined, `mcp_servers.remote_agent={command="/path with spaces/remote-agent",args=["mcp","--session","remote-id"],env_vars=["REMOTE_AGENT_HOME"],enabled=true}`) {
			t.Fatalf("missing MCP: %v", argv)
		}
		if strings.Contains(joined, "instructions") || strings.Contains(joined, "system_prompt") {
			t.Fatal("injected instructions")
		}
	}
}

func TestModelsIncludesConfiguredDefaultAndRereadsCatalog(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.toml"), []byte(`model = "gpt-6.1-sol"`), 0600)
	cache := filepath.Join(home, "models_cache.json")
	os.WriteFile(cache, []byte(`{"models":[{"slug":"gpt-6-sol","visibility":"list"}]}`), 0600)
	a := New("codex", home, "")
	m, err := a.Models()
	if err != nil || m.DefaultModel != "gpt-6.1-sol" || m.Models[0].Slug != "gpt-6.1-sol" {
		t.Fatalf("configured model missing: %v, %v", m, err)
	}
	os.WriteFile(cache, []byte(`{"models":[{"slug":"gpt-6.1-sol","display_name":"GPT-6.1-Sol","visibility":"hide","default_reasoning_level":"low","supported_reasoning_levels":[{"effort":"low"},{"effort":"medium"},{"effort":"high"},{"effort":"xhigh"},{"effort":"max"},{"effort":"ultra"}]}]}`), 0600)
	m, err = a.Models()
	if err != nil || len(m.Models) != 1 || m.Models[0].DisplayName != "GPT-6.1-Sol" || len(m.Models[0].Efforts) != 6 {
		t.Fatalf("catalog not refreshed: %v, %v", m, err)
	}
}
