package daemon

import (
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
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
