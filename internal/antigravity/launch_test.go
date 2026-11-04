package antigravity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
)

func TestLaunchPermissionAndResume(t *testing.T) {
	for _, tc := range []struct {
		name, global string
		mode         pb.ApprovalMode
		want         string
		full         bool
	}{
		{"explicit ask", "always-proceed", pb.ApprovalMode_APPROVAL_MODE_ASK, "request-review", false},
		{"explicit full", "request-review", pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS, "always-proceed", true},
		{"explicit auto", "always-proceed", pb.ApprovalMode_APPROVAL_MODE_AUTO, "request-review", false},
		{"explicit plan", "always-proceed", pb.ApprovalMode_APPROVAL_MODE_PLAN, "request-review", false},
		{"follow full", "always-proceed", 0, "always-proceed", true},
		{"follow strict", "strict", 0, "strict", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			config := filepath.Join(home, "settings.json")
			original := []byte(`{"toolPermission":"` + tc.global + `","statusLine":{"type":"command","command":"cat","padding":2,"refreshInterval":5}}`)
			mustWrite(t, config, original)
			a := launchTestAdapter(t, home)
			args, err := a.Options(nil, "gemini-3.8-flash", "low", tc.mode)
			if err != nil {
				t.Fatal(err)
			}
			if a.FullAccess(args) != tc.full {
				t.Fatal("wrong full-access capability")
			}
			l := &agent.Launch{Dir: t.TempDir(), Cwd: "/workspace/project", Args: args, Exe: "/bin/remote-agent", RemoteSession: "remote-session"}
			argv, err := a.Argv(l)
			if err != nil {
				t.Fatal(err)
			}
			if hasFlag(argv, "--ra-tool-permission") || hasFlag(argv, "--dangerously-skip-permissions") || hasFlag(argv, "--conversation") {
				t.Fatalf("bad argv %v", argv)
			}
			var st map[string]any
			b, _ := os.ReadFile(filepath.Join(l.Dir, SettingsFile))
			if json.Unmarshal(b, &st) != nil || st["toolPermission"] != tc.want {
				t.Fatalf("settings %s", b)
			}
			if got, _ := os.ReadFile(config); string(got) != string(original) {
				t.Fatal("user settings changed")
			}
			hooks, err := os.ReadFile(filepath.Join(l.Dir, AddonDir, ".agents", "hooks.json"))
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range []string{"SessionStart", "PreToolUse", "Stop"} {
				if !strings.Contains(string(hooks), hookCommand+" "+event) { // JSON escapes quotes
					var groups map[string]map[string]json.RawMessage
					if json.Unmarshal(hooks, &groups) != nil || len(groups["remote-agent"][event]) == 0 {
						t.Fatalf("missing hook %s", event)
					}
				}
			}
			mcp, _ := os.ReadFile(filepath.Join(l.Dir, AddonDir, ".agents", "mcp_config.json"))
			if !strings.Contains(string(mcp), "remote_agent") || !strings.Contains(string(mcp), "remote-session") {
				t.Fatal("MCP binding missing")
			}
			mustWrite(t, config, []byte(`{"toolPermission":"always-proceed"}`))
			l.Resume = true
			l.SessionID = "conversation"
			l.FullAccess = tc.full
			l.Context = &pb.TurnContext{PermissionMode: "default", Model: "gemini-3.8-flash", Effort: "low"}
			l.Args = append(l.Args, "--mode", "plan")
			resumed, err := a.Argv(l)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(resumed, "--conversation") || hasFlag(resumed, "--mode") {
				t.Fatalf("resume did not clear old mode %v", resumed)
			}
			b, _ = os.ReadFile(filepath.Join(l.Dir, SettingsFile))
			_ = json.Unmarshal(b, &st)
			if st["toolPermission"] != tc.want {
				t.Fatal("resume changed original policy")
			}
		})
	}
}
func mustWrite(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func TestOptionsAndDefaultMode(t *testing.T) {
	a := launchTestAdapter(t, t.TempDir())
	for _, flag := range ownFlags {
		if _, err := a.Options([]string{flag}, "", "", 0); err == nil {
			t.Fatalf("accepted %s", flag)
		}
	}
	for _, mode := range []pb.ApprovalMode{1, 2, 3, 4} {
		if _, err := a.Options(nil, "gemini-3.8-flash", "low", mode); err != nil {
			t.Fatalf("mode %v: %v", mode, err)
		}
	}
	if _, err := a.Options(nil, "x;touch injected", "", 0); err == nil {
		t.Fatal("unsafe model")
	}
	for _, tc := range []struct {
		policy string
		mode   pb.ApprovalMode
		known  bool
	}{
		{"always-proceed", pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS, true}, {"request-review", pb.ApprovalMode_APPROVAL_MODE_ASK, true}, {"strict", pb.ApprovalMode_APPROVAL_MODE_ASK, true}, {"proceed-in-sandbox", 0, false}, {"", 0, false},
	} {
		mustWrite(t, filepath.Join(a.home, "settings.json"), []byte(`{"toolPermission":"`+tc.policy+`"}`))
		m, k, _ := a.DefaultMode("")
		if m != tc.mode || k != tc.known {
			t.Fatalf("policy %q: %v %v", tc.policy, m, k)
		}
	}
	if _, err := a.Argv(&agent.Launch{ForkOf: "parent"}); err != agent.ErrUnsupported {
		t.Fatal("fork must be unsupported")
	}
}

func launchTestAdapter(t *testing.T, home string) *Adapter {
	t.Helper()
	a := New("agy", home)
	models, err := ParseModels(sampleAgyModelsOutput)
	if err != nil {
		t.Fatal(err)
	}
	a.models.cached, a.models.bin, a.models.fetchedAt = &pb.ListModelsResponse{Models: models}, a.Bin(), time.Now()
	return a
}
