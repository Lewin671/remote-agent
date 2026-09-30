package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/claude"
)

func claudeQuota(d *Daemon) []*pb.AgentQuota {
	var out []*pb.AgentQuota
	for _, q := range d.MachineMeta().Quotas {
		if q.Agent == pb.Agent_AGENT_CLAUDE {
			out = append(out, q)
		}
	}
	return out
}

func writeAccount(t *testing.T, home, account string) {
	t.Helper()
	os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"oauthAccount":{"accountUuid":"`+account+`","organizationUuid":"org","organizationType":"claude_max"}}`), 0o600)
}

func TestClaudeQuotaFromStatusLine(t *testing.T) {
	h := newHarness(t, Config{})
	home := t.TempDir()
	h.d.agents[pb.Agent_AGENT_CLAUDE] = claude.New("/bin/true", home)
	writeAccount(t, home, "acct-1")

	if q := claudeQuota(h.d); len(q) != 1 || q[0].State != pb.QuotaState_QUOTA_STATE_WAITING {
		t.Fatalf("before any report: %v", q)
	}
	now := time.Now()
	// Before the first reply there are no limits: nothing changes.
	h.d.statusLine("s1", []byte(`{"model":{"id":"x"}}`), now)
	if q := claudeQuota(h.d); q[0].State != pb.QuotaState_QUOTA_STATE_WAITING {
		t.Fatal("no limits taken as limits")
	}
	h.d.statusLine("s1", []byte(`{"rate_limits":{"five_hour":{"used_percentage":80,"resets_at":1790774400},"seven_day":{"resets_at":1790960400}}}`), now)
	q := claudeQuota(h.d)
	if len(q) != 1 || q[0].State != pb.QuotaState_QUOTA_STATE_OK || q[0].Plan != "claude_max" || q[0].Login != pb.QuotaLogin_QUOTA_LOGIN_SUBSCRIPTION || q[0].Account == "" || q[0].ObservedMs != now.UnixMilli() {
		t.Fatalf("quota %v", q)
	}
	w := q[0].Windows
	if len(w) != 2 || w[0].Name != "five_hour" || w[0].GetUsedPercent() != 80 || w[0].ResetsAtMs != 1790774400000 || w[1].UsedPercent != nil {
		t.Fatalf("windows %v", w)
	}
	// An unchanged report within a minute does not renew the time.
	h.d.statusLine("s2", []byte(`{"rate_limits":{"five_hour":{"used_percentage":80,"resets_at":1790774400},"seven_day":{"resets_at":1790960400}}}`), now.Add(10*time.Second))
	if claudeQuota(h.d)[0].ObservedMs != now.UnixMilli() {
		t.Fatal("renewed too soon")
	}
	// A restart keeps the record of the same account...
	d2 := New(Config{})
	d2.agents[pb.Agent_AGENT_CLAUDE] = claude.New("/bin/true", home)
	d2.loadQuotas()
	if q := claudeQuota(d2); len(q) != 1 || q[0].GetWindows()[0].GetUsedPercent() != 80 {
		t.Fatalf("after restart: %v", q)
	}
	// ...but not another account's.
	writeAccount(t, home, "acct-2")
	d3 := New(Config{})
	d3.agents[pb.Agent_AGENT_CLAUDE] = claude.New("/bin/true", home)
	d3.loadQuotas()
	if q := claudeQuota(d3); len(q) != 1 || q[0].State != pb.QuotaState_QUOTA_STATE_WAITING {
		t.Fatalf("other account: %v", q)
	}
	// A report under the new account drops the old one's record.
	h.d.statusLine("s1", []byte(`{"rate_limits":{"five_hour":{"used_percentage":5}}}`), now.Add(time.Hour))
	if q := claudeQuota(h.d); len(q) != 1 || q[0].GetWindows()[0].GetUsedPercent() != 5 {
		t.Fatalf("account change: %v", q)
	}
	// Without a known account, sessions are kept apart.
	os.Remove(filepath.Join(home, ".claude.json"))
	h.d.statusLine("s3", []byte(`{"rate_limits":{"five_hour":{"used_percentage":7}}}`), now.Add(2*time.Hour))
	found := false
	for _, q := range claudeQuota(h.d) {
		if q.SourceSessionId == "s3" && q.Account == "" && q.Login == pb.QuotaLogin_QUOTA_LOGIN_UNSPECIFIED {
			found = true
		}
	}
	if !found {
		t.Fatal("unknown account's report not kept by session")
	}
}
