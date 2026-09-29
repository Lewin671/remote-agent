package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func composer(footer string) string {
	return "• done\n\x1b[1m›\x1b[0m \x1b[2mAsk Codex to do anything\x1b[0m\n  " + footer + "\n  ? for shortcuts\n"
}

// Menus as codex-cli 0.157.1 draws them (plain parts).
const (
	modelMenu = "  Select Model and Effort\n" +
		"  1. GPT-6-Astra (default)  Frontier intelligence.\n" +
		"\x1b[1;7m› 2. GPT-6-Sol (current)    Workhorse model.\n\x1b[0m" +
		"  3. GPT-6-Luna             Fast and affordable.\n" +
		"  enter select · esc back\n"
	permissionsMenu = "  Update Model Permissions\n" +
		"  1. Ask for approval          Read and edit workspace files\n" +
		"\x1b[1;7m› 2. Approve for me (current)  Only ask for unsafe actions\n\x1b[0m" +
		"  3. Full Access               Use with caution\n" +
		"  enter select · esc back\n"
	fullAccessConfirm = "  Enable full access?\n" +
		"\x1b[1;7m› 1. Yes, continue anyway  Apply full access for this session\n\x1b[0m" +
		"  2. Cancel                Go back\n" +
		"  enter select · esc back\n"
)

func effortMenu(cursor int) string {
	labels := []string{"Low", "Medium (default)", "High", "Extra high", "More reasoning…"}
	var b strings.Builder
	b.WriteString("  Select Reasoning Level for GPT-6-Luna\n")
	for i, l := range labels {
		if i == cursor {
			b.WriteString("\x1b[1;7m› ")
		} else {
			b.WriteString("  ")
		}
		b.WriteString(string(rune('1'+i)) + ". " + l + "   Description\n")
		if i == cursor {
			b.WriteString("\x1b[0m")
		}
	}
	b.WriteString("  enter default · s session · esc back\n")
	return b.String()
}

// fakeCodex reacts to keys the way Codex's menus do.
func fakeCodex(footer *string) func(f *fakeTerm, b string) {
	typed := ""
	cursor := 1
	return func(f *fakeTerm, b string) {
		switch {
		case b == "/model" || b == "/permissions" || b == "/compact":
			typed = b
		case b == "\r" && typed == "/model":
			f.screen = modelMenu
		case b == "\r" && typed == "/permissions":
			f.screen = permissionsMenu
		case b == "\r" && typed == "/compact":
			f.screen = "\x1b[1m›\x1b[0m compacting\n  " + *footer + "\n"
		case strings.Contains(f.screen, "Select Model and Effort") && b == "3":
			cursor = 1
			f.screen = effortMenu(cursor)
		case strings.Contains(f.screen, "Select Reasoning Level") && b == keyDown:
			cursor++
			f.screen = effortMenu(cursor)
		case strings.Contains(f.screen, "Select Reasoning Level") && b == "s":
			level := []string{"low", "medium", "high", "xhigh"}[cursor]
			*footer = "GPT-6-Luna " + level + " · /tmp/w"
			f.screen = composer(*footer)
		case strings.Contains(f.screen, "Update Model Permissions") && b == "3":
			f.screen = fullAccessConfirm
		case strings.Contains(f.screen, "Update Model Permissions") && (b == "1" || b == "2"):
			f.screen = "• Permission selection requested: X\n" + composer(*footer)
		case strings.Contains(f.screen, "Enable full access?") && b == "1":
			f.screen = "• Permission selection requested: Full Access\n" + composer(*footer)
		case b == keyBTab:
			if strings.HasSuffix(*footer, "Plan mode") {
				*footer = strings.TrimSuffix(*footer, "   Plan mode")
			} else {
				*footer += "   Plan mode"
			}
			f.screen = composer(*footer)
		case b == keyEsc:
			f.screen = composer(*footer)
		}
	}
}

func controlHarness(t *testing.T) (*harness, *string) {
	h := newHarness(t, Config{})
	footer := "GPT-6-Sol medium · /tmp/w"
	h.term.mu.Lock()
	h.term.screen = composer(footer)
	h.term.onWrite = fakeCodex(&footer)
	h.term.mu.Unlock()
	return h, &footer
}

func lastSettings(h *harness) *pb.SettingsChanged {
	var last *pb.SettingsChanged
	h.s.log.Each(func(e *pb.Event) {
		if c := e.GetSettingsChanged(); c != nil {
			last = c
		}
	})
	return last
}

func TestSetModel(t *testing.T) {
	h, footer := controlHarness(t)
	if err := h.s.setModel("gpt-6-luna", "high", "GPT-6-Luna"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*footer, "GPT-6-Luna high") {
		t.Fatalf("footer %q", *footer)
	}
	w := h.term.written()
	want := []string{"/model", "\r", "3", keyDown, "s"}
	if strings.Join(w, "|") != strings.Join(want, "|") {
		t.Fatalf("keys %q, want %q", w, want)
	}
	if c := lastSettings(h); c == nil || c.Model != "gpt-6-luna" || c.Effort != "high" {
		t.Fatalf("settings event %v", c)
	}
	if info := h.s.info(); info.Model != "gpt-6-luna" || info.Effort != "high" {
		t.Fatalf("info %v %v", info.Model, info.Effort)
	}
}

func TestSetModelRefusesUnknownScreens(t *testing.T) {
	h, _ := controlHarness(t)
	if err := h.s.setModel("gpt-9", "high", "GPT-9"); err == nil {
		t.Fatal("picked a model the menu does not list")
	}
	// The menu was left with Esc and nothing else was chosen.
	for _, k := range h.term.written() {
		if k == "s" || k == "\r\r" {
			t.Fatalf("keys %q", h.term.written())
		}
	}
	// A draft in the input box is never typed over.
	h.term.setScreen("\x1b[1m›\x1b[0m my draft\n  GPT-6-Sol medium\n")
	n := len(h.term.written())
	if err := h.s.setModel("gpt-6-luna", "high", "GPT-6-Luna"); !errors.Is(err, errNotReady) {
		t.Fatalf("with a draft: %v", err)
	}
	if len(h.term.written()) != n {
		t.Fatal("typed over a draft")
	}
}

func TestSetApprovalMode(t *testing.T) {
	h, footer := controlHarness(t)
	if err := h.s.setApprovalMode(pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS); err != nil {
		t.Fatal(err)
	}
	if w := strings.Join(h.term.written(), "|"); w != "/permissions|\r|3|1" {
		t.Fatalf("keys %q", w)
	}
	if info := h.s.info(); info.Sandbox != "danger-full-access" || info.ApprovalPolicy != "never" {
		t.Fatalf("info %v", info)
	}

	// Plan turns Plan mode on and leaves the permissions alone.
	if err := h.s.setApprovalMode(pb.ApprovalMode_APPROVAL_MODE_PLAN); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(*footer, "Plan mode") {
		t.Fatalf("footer %q", *footer)
	}
	// Any other mode leaves Plan mode first.
	if err := h.s.setApprovalMode(pb.ApprovalMode_APPROVAL_MODE_ASK); err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(*footer, "Plan mode") {
		t.Fatal("still in Plan mode")
	}
	if info := h.s.info(); info.ApprovalsReviewer != "user" || info.CollaborationMode != "default" {
		t.Fatalf("info %v", info)
	}
}

func TestAnswerPlan(t *testing.T) {
	h, _ := controlHarness(t)
	if err := h.s.answerPlan(pb.PlanChoice_PLAN_CHOICE_IMPLEMENT); !errors.Is(err, errNoPlan) {
		t.Fatalf("no plan showing: %v", err)
	}
	h.term.setScreen("  Implement this plan?\n\x1b[1;7m› 1. Yes, implement this plan  Switch\n\x1b[0m  2. Yes, clear context  Fresh\n  3. No, stay in Plan mode  Continue\n")
	if err := h.s.answerPlan(pb.PlanChoice_PLAN_CHOICE_KEEP_PLANNING); err != nil {
		t.Fatal(err)
	}
	if w := h.term.written(); len(w) != 1 || w[0] != "3" {
		t.Fatalf("keys %q", w)
	}
}

func TestListModelsAndDirectories(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "models_cache.json"), []byte(`{"models":[
		{"slug":"b","display_name":"B","visibility":"list","priority":5,"default_reasoning_level":"low","supported_reasoning_levels":[{"effort":"low"}]},
		{"slug":"hidden","display_name":"H","visibility":"hide","priority":1},
		{"slug":"a","display_name":"A","visibility":"list","priority":2,"supported_reasoning_levels":[{"effort":"low"},{"effort":"high"}]}]}`), 0o600)
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("model = \"a\"\n[profiles.x]\nmodel = \"b\"\n"), 0o600)
	d := New(Config{CodexHome: home})
	res, err := d.listModels()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Models) != 2 || res.Models[0].Slug != "a" || res.DefaultModel != "a" || len(res.Models[0].Efforts) != 2 {
		t.Fatalf("models %v", res)
	}

	root := t.TempDir()
	for _, n := range []string{"zeta", "Alpha", ".hidden"} {
		os.Mkdir(filepath.Join(root, n), 0o700)
	}
	os.WriteFile(filepath.Join(root, "file"), nil, 0o600)
	dirs, err := listDirectories(root)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(dirs.Dirs, ",") != "Alpha,zeta" || dirs.Parent != filepath.Dir(root) {
		t.Fatalf("dirs %v", dirs)
	}
	if _, err := listDirectories("relative/path"); err == nil {
		t.Fatal("accepted a relative path")
	}
}

func TestSettingsAndSessionArgs(t *testing.T) {
	h := newHarness(t, Config{})
	d := h.d
	if _, err := d.updateSettings(&pb.DaemonSettings{ApprovalTimeoutSeconds: 5}); err == nil {
		t.Fatal("accepted a 5s timeout")
	}
	st, err := d.updateSettings(&pb.DaemonSettings{ApprovalTimeoutSeconds: 300, DefaultModel: "gpt-6-sol", DefaultEffort: "high", DefaultMode: pb.ApprovalMode_APPROVAL_MODE_AUTO})
	if err != nil || st.ApprovalTimeoutSeconds != 300 {
		t.Fatalf("%v %v", st, err)
	}
	if d.approvalTimeout().Seconds() != 300 {
		t.Fatal("timeout not applied")
	}
	args, err := d.sessionArgs(&pb.CreateSessionRequest{})
	if err != nil || !strings.Contains(strings.Join(args, " "), `-m gpt-6-sol -c model_reasoning_effort="high" -c approvals_reviewer="auto_review"`) {
		t.Fatalf("defaults: %q %v", args, err)
	}
	args, _ = d.sessionArgs(&pb.CreateSessionRequest{Model: "gpt-6-luna", Mode: pb.ApprovalMode_APPROVAL_MODE_FULL_ACCESS})
	if j := strings.Join(args, " "); !strings.Contains(j, "-m gpt-6-luna") || !strings.Contains(j, `sandbox_mode="danger-full-access"`) {
		t.Fatalf("explicit: %q", args)
	}
	if _, err := d.sessionArgs(&pb.CreateSessionRequest{Model: "x; rm -rf /"}); err == nil {
		t.Fatal("accepted a bad model name")
	}
}
