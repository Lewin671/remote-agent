package daemon

import (
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestContextUpdateDoesNotRenewCodexQuota(t *testing.T) {
	h := newHarness(t, Config{})
	at := time.Now().Add(-time.Hour)
	u := &pb.TokenUsage{TotalTokens: 123, ContextWindow: 10000, PrimaryLimit: &pb.RateLimit{UsedPercent: 29, WindowMinutes: 10080}}
	h.s.usage = u
	h.d.noteLimits(u, at)
	h.s.recordStatusLine(&agent.StatusLine{ContextWindow: 20000}, time.Now())
	if !h.d.limitsAt.Equal(at) {
		t.Fatal("context update renewed account quota")
	}
	if h.s.usage.GetTotalTokens() != 123 || h.s.usage.GetContextWindow() != 20000 {
		t.Fatal("lost context update or token counters")
	}
}

// A `codex exec` run by another agent is not a remote-agent session, yet its
// rollout updates the Codex account limits clients see.
func TestUnmanagedCodexRunUpdatesQuota(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "sessions", "2026", "11", "14")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	rollout := `{"timestamp":"2026-11-14T22:21:14Z","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"limit_id":"codex","primary":{"used_percent":93,"window_minutes":10080,"resets_at":1763602200},"plan_type":"prolite"}}}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "rollout-exec.jsonl"), []byte(rollout), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, Config{Agents: map[pb.Agent]AgentConfig{pb.Agent_AGENT_CODEX: {Bin: "codex", Home: home}}})
	h.d.pollAccountLimits(map[pb.Agent]time.Time{}, time.Now())
	var got *pb.AgentQuota
	for _, q := range h.d.MachineMeta().Quotas {
		if q.Agent == pb.Agent_AGENT_CODEX {
			got = q
		}
	}
	want := time.Date(2026, 11, 14, 22, 21, 14, 0, time.UTC).UnixMilli()
	if got.GetState() != pb.QuotaState_QUOTA_STATE_OK || got.ObservedMs != want || len(got.Windows) != 1 || got.Windows[0].GetUsedPercent() != 93 {
		t.Fatalf("quota = %v", got)
	}
}
