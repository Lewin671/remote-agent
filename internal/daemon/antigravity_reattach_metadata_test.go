package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/antigravity"
	"github.com/Lewin671/remote-agent/internal/eventlog"
)

func TestAgyReattachCatalogPrecedesFirstNativeModelObservation(t *testing.T) {
	t.Parallel()
	r := newRestoreHarness(t)
	bin := filepath.Join(t.TempDir(), "agy")
	script := "#!/bin/sh\n[ \"$1\" = models ] || exit 1\nprintf '%s\\n' 'gemini-3.8-flash-low Gemini 3.8 Flash (Low)' 'claude-sonnet-5-5-low Claude Sonnet 5.5 (Low)'\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a := antigravity.New(bin, t.TempDir())
	r.d.agents[a.Kind()] = a
	m := meta{ID: "00000000000000ab", Agent: a.Kind(), CodexSessionID: "conv"}
	dir := r.save(m, true)
	if err := os.WriteFile(filepath.Join(dir, antigravity.SettingsFile), []byte(`{"toolPermission":"request-review"}`), 0600); err != nil {
		t.Fatal(err)
	}
	log, err := eventlog.Open(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	err = log.Append(m.ID, []*pb.Event{{Kind: &pb.Event_TurnContext{TurnContext: &pb.TurnContext{Model: "gemini-3.8-flash", Effort: "low", ApprovalPolicy: "request-review", PermissionMode: "default"}}}}, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.restore(); err != nil {
		t.Fatal(err)
	}
	r.d.statusLine(m.ID, []byte(`{"product":"antigravity","conversation_id":"conv","agent_state":"idle","model":{"id":"Claude Sonnet 5.5 (Low)","effort":"low"}}`), time.Now())
	s := r.session(m.ID)
	if got := s.info().Model; got != "claude-sonnet-5-5" {
		t.Fatalf("first reattached report lost actual model: %q", got)
	}
	// A metadata outage/mode-only report cannot durably erase that identity.
	s.recordStatusLine(&agent.StatusLine{CycleMode: "default", IdleKnown: true, Context: &pb.TurnContext{ApprovalPolicy: "request-review", PermissionMode: "default"}}, time.Now().Add(time.Second))
	if got := s.info(); got.Model != "claude-sonnet-5-5" || got.Effort != "low" {
		t.Fatalf("incomplete observation erased model: %v", got)
	}
}

func TestAgyStartupStatusLineDoesNotLogBogusDefaultModelBeforeFlagsApply(t *testing.T) {
	t.Parallel()
	h := agyHarness(t)
	bin := filepath.Join(t.TempDir(), "agy")
	script := "#!/bin/sh\n[ \"$1\" = models ] || exit 1\nprintf '%s\\n' 'gemini-4-neon-high Gemini 4 Neon (High)' 'gemini-4-hydrogen-medium Gemini 4 Hydrogen (Medium)'\n"
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	a := antigravity.New(bin, t.TempDir())
	if _, err := a.Models(); err != nil {
		t.Fatal(err)
	}
	h.d.agents[a.Kind()] = a
	h.s.ag = a
	h.s.parser = a.NewParser()
	h.s.meta.CodexSessionID = ""
	if err := os.WriteFile(filepath.Join(h.s.dir, antigravity.SettingsFile), []byte(`{"toolPermission":"always-proceed"}`), 0600); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	// Tick 1: pre-catalog startup statusLine emits global default display name with context_window_size: 0.
	h.d.statusLine(h.s.meta.ID, []byte(`{"product":"antigravity","agent_state":"idle","model":{"id":"Gemini 4 Neon (High)","display_name":"Gemini 4 Neon (High)"},"context_window":{"context_window_size":0},"sandbox":{"enabled":false}}`), now)
	// Tick 2: real requested model & effort arrive ~2s later with context_window_size: 1048576.
	h.d.statusLine(h.s.meta.ID, []byte(`{"product":"antigravity","agent_state":"idle","model":{"id":"Gemini 4 Hydrogen (Medium)","display_name":"Gemini 4 Hydrogen (Medium)","effort":"medium"},"context_window":{"context_window_size":1048576},"sandbox":{"enabled":false}}`), now.Add(2*time.Second))

	var contexts []*pb.TurnContext
	for _, ev := range h.events() {
		if tc := ev.GetTurnContext(); tc != nil {
			contexts = append(contexts, tc)
		}
	}
	if len(contexts) != 2 {
		t.Fatalf("expected 2 TurnContext events, got %d: %+v", len(contexts), contexts)
	}
	if contexts[0].Model != "" || contexts[0].Effort != "" {
		t.Fatalf("first TurnContext must suppress unresolved startup model, got %+v", contexts[0])
	}
	if contexts[1].Model != "gemini-4-hydrogen" || contexts[1].Effort != "medium" {
		t.Fatalf("second TurnContext = %+v, want gemini-4-hydrogen/medium", contexts[1])
	}
	if got := h.s.info(); got.Model != "gemini-4-hydrogen" || got.Effort != "medium" {
		t.Fatalf("session info model=%q effort=%q, want gemini-4-hydrogen/medium", got.Model, got.Effort)
	}
}
