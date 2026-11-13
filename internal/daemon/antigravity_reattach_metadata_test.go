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
