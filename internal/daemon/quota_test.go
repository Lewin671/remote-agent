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
	// An idle session repeats the limits of its last reply: they do not
	// replace newer ones, nor renew the time.
	later := now.Add(5 * time.Minute)
	h.d.statusLine("s2", []byte(`{"rate_limits":{"five_hour":{"used_percentage":86,"resets_at":1790774400},"seven_day":{"used_percentage":3,"resets_at":1790960400}}}`), later)
	h.d.statusLine("s1", []byte(`{"rate_limits":{"five_hour":{"used_percentage":80,"resets_at":1790774400}}}`), later.Add(5*time.Minute))
	q = claudeQuota(h.d)
	if w := q[0].Windows; len(w) != 2 || w[0].GetUsedPercent() != 86 || w[1].GetUsedPercent() != 3 || q[0].ObservedMs != later.UnixMilli() {
		t.Fatalf("stale report taken: %v", q)
	}
	// A window that has reset since is newer whatever its usage.
	h.d.statusLine("s1", []byte(`{"rate_limits":{"five_hour":{"used_percentage":1,"resets_at":1790792400}}}`), later.Add(6*time.Minute))
	if w := claudeQuota(h.d)[0].Windows; len(w) != 2 || w[0].GetUsedPercent() != 1 || w[0].ResetsAtMs != 1790792400000 || w[1].GetUsedPercent() != 3 {
		t.Fatalf("after a reset: %v", w)
	}
	// A restart keeps the record of the same account...
	d2 := New(Config{})
	d2.agents[pb.Agent_AGENT_CLAUDE] = claude.New("/bin/true", home)
	d2.loadQuotas()
	if q := claudeQuota(d2); len(q) != 1 || q[0].GetWindows()[0].GetUsedPercent() != 1 {
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

// An idle session repeats the limits of its last reply. After another
// account logged in, those are still the first account's: they are not
// taken for the new one's, and do not outweigh what the new one reports.
func TestClaudeQuotaOfAnEarlierLoginIsNotTheNewAccounts(t *testing.T) {
	h := newHarness(t, Config{})
	home := t.TempDir()
	h.d.agents[pb.Agent_AGENT_CLAUDE] = claude.New("/bin/true", home)
	writeAccount(t, home, "acct-1")
	now := time.Now()
	spent := []byte(`{"rate_limits":{"five_hour":{"used_percentage":100,"resets_at":1790774400}}}`)
	h.d.statusLine("old", spent, now)
	first := claudeQuota(h.d)[0].Account

	writeAccount(t, home, "acct-2")
	h.d.statusLine("old", spent, now.Add(time.Minute))
	if q := claudeQuota(h.d); len(q) != 1 || q[0].State != pb.QuotaState_QUOTA_STATE_WAITING {
		t.Fatalf("the first account's limits shown for the second: %v", q)
	}
	h.d.statusLine("new", []byte(`{"rate_limits":{"five_hour":{"used_percentage":4,"resets_at":1790774400}}}`), now.Add(2*time.Minute))
	h.d.statusLine("old", spent, now.Add(3*time.Minute))
	q := claudeQuota(h.d)
	if len(q) != 1 || q[0].Account == first || q[0].Account == "" || q[0].Windows[0].GetUsedPercent() != 4 {
		t.Fatalf("after the second account reported: %v", q)
	}
	// Once the old session has a reply under the new login, its limits
	// change and count again.
	h.d.statusLine("old", []byte(`{"rate_limits":{"five_hour":{"used_percentage":9,"resets_at":1790774400}}}`), now.Add(4*time.Minute))
	if q := claudeQuota(h.d); q[0].Windows[0].GetUsedPercent() != 9 {
		t.Fatalf("a new reply's limits ignored: %v", q)
	}
}
