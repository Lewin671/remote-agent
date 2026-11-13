package daemon

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/agent"
	"github.com/Lewin671/remote-agent/internal/antigravity"
	"github.com/Lewin671/remote-agent/internal/claude"
)

func TestClaudeQuotaSessionFreshness(t *testing.T) {
	t.Parallel()
	type step struct {
		session      string
		five, seven  float64
		resetOffset  int64 // seconds added to the five-hour reset
		restart      bool
		want         [2]float64
		wantReset    int64
		observedStep int
	}
	for _, tt := range []struct {
		name  string
		steps []step
	}{
		{"same session usage can fall", []step{
			{session: "A", five: 41, seven: 97, want: [2]float64{41, 97}, observedStep: 0},
			{session: "A", five: 41, seven: 70, want: [2]float64{41, 70}, observedStep: 1},
		}},
		{"idle session cannot undo a fresh report", []step{
			{session: "A", five: 41, seven: 97, want: [2]float64{41, 97}, observedStep: 0},
			{session: "B", five: 30, seven: 60, want: [2]float64{41, 97}, observedStep: 0},
			{session: "A", five: 41, seven: 70, want: [2]float64{41, 70}, observedStep: 2},
			{session: "B", five: 30, seven: 60, want: [2]float64{41, 70}, observedStep: 2},
			{session: "B", five: 30, seven: 60, want: [2]float64{41, 70}, observedStep: 2},
			{session: "B", five: 30, seven: 61, want: [2]float64{30, 61}, observedStep: 5},
		}},
		{"restart needs a changed report", []step{
			{session: "A", five: 41, seven: 97, want: [2]float64{41, 97}, observedStep: 0},
			{restart: true, session: "A", five: 30, seven: 60, want: [2]float64{41, 97}, observedStep: 0},
			{session: "A", five: 30, seven: 60, want: [2]float64{41, 97}, observedStep: 0},
			{session: "A", five: 30, seven: 61, want: [2]float64{30, 61}, observedStep: 3},
		}},
		{"idle repetition cannot restore an older higher value", []step{
			{session: "A", five: 41, seven: 97, want: [2]float64{41, 97}, observedStep: 0},
			{session: "B", five: 30, seven: 60, want: [2]float64{41, 97}, observedStep: 0},
			{session: "A", five: 20, seven: 50, want: [2]float64{20, 50}, observedStep: 2},
			{session: "B", five: 30, seven: 60, want: [2]float64{20, 50}, observedStep: 2},
		}},
		{"one changed window refreshes the whole report", []step{
			{session: "A", five: 41, seven: 97, want: [2]float64{41, 97}, observedStep: 0},
			{session: "B", five: 30, seven: 60, want: [2]float64{41, 97}, observedStep: 0},
			{session: "B", five: 31, seven: 60, want: [2]float64{31, 60}, observedStep: 2},
		}},
		{"first report can advance a reset", []step{
			{session: "A", five: 97, seven: 97, want: [2]float64{97, 97}, observedStep: 0},
			{session: "B", five: 1, seven: 60, resetOffset: 18000, want: [2]float64{1, 97}, wantReset: 18000, observedStep: 1},
		}},
		{"changed report cannot undo a later reset", []step{
			{session: "A", five: 97, seven: 97, want: [2]float64{97, 97}, observedStep: 0},
			{session: "B", five: 1, seven: 97, resetOffset: 18000, want: [2]float64{1, 97}, wantReset: 18000, observedStep: 1},
			{session: "A", five: 98, seven: 70, want: [2]float64{1, 70}, wantReset: 18000, observedStep: 2},
		}},
		{"changed session confirms the current account snapshot", []step{
			{session: "A", five: 41, seven: 97, want: [2]float64{41, 97}, observedStep: 0},
			{session: "B", five: 30, seven: 60, want: [2]float64{41, 97}, observedStep: 0},
			{session: "B", five: 41, seven: 97, want: [2]float64{41, 97}, observedStep: 2},
			{session: "B", five: 41, seven: 97, want: [2]float64{41, 97}, observedStep: 2},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home, dir := t.TempDir(), t.TempDir()
			writeAccount(t, home, "quota-account")
			newDaemon := func() *Daemon {
				d := New(dir, Config{})
				d.agents[pb.Agent_AGENT_CLAUDE] = claude.New("/bin/true", home)
				d.loadQuotas()
				return d
			}
			d := newDaemon()
			now := time.Now()
			for i, s := range tt.steps {
				if s.restart {
					d = newDaemon()
				}
				raw := fmt.Sprintf(`{"rate_limits":{"five_hour":{"used_percentage":%g,"resets_at":%d},"seven_day":{"used_percentage":%g,"resets_at":1790960400}}}`, s.five, 1790774400+s.resetOffset, s.seven)
				reportClaudeQuota(t, d, s.session, []byte(raw), now.Add(time.Duration(i)*time.Minute))
				q := claudeQuota(d)[0]
				if len(q.Windows) != 2 || q.Windows[0].GetUsedPercent() != s.want[0] || q.Windows[1].GetUsedPercent() != s.want[1] || q.Windows[0].ResetsAtMs != (1790774400+s.wantReset)*1000 || q.ObservedMs != now.Add(time.Duration(s.observedStep)*time.Minute).UnixMilli() {
					t.Fatalf("step %d: got %v; want usage %v, reset offset %d, observation step %d", i, q, s.want, s.wantReset, s.observedStep)
				}
			}
		})
	}
}

func claudeQuota(d *Daemon) []*pb.AgentQuota {
	var out []*pb.AgentQuota
	for _, q := range d.MachineMeta().Quotas {
		if q.Agent == pb.Agent_AGENT_CLAUDE {
			out = append(out, q)
		}
	}
	return out
}

func agyQuota(d *Daemon) []*pb.AgentQuota {
	var out []*pb.AgentQuota
	for _, q := range d.MachineMeta().Quotas {
		if q.Agent == pb.Agent_AGENT_ANTIGRAVITY {
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
	reportClaudeQuota(t, h.d, "s1", []byte(`{"model":{"id":"x"}}`), now)
	if q := claudeQuota(h.d); q[0].State != pb.QuotaState_QUOTA_STATE_WAITING {
		t.Fatal("no limits taken as limits")
	}
	reportClaudeQuota(t, h.d, "s1", []byte(`{"rate_limits":{"five_hour":{"used_percentage":80,"resets_at":1790774400},"seven_day":{"resets_at":1790960400}}}`), now)
	q := claudeQuota(h.d)
	if len(q) != 1 || q[0].State != pb.QuotaState_QUOTA_STATE_OK || q[0].Plan != "claude_max" || q[0].Login != pb.QuotaLogin_QUOTA_LOGIN_SUBSCRIPTION || q[0].Account == "" || q[0].ObservedMs != now.UnixMilli() {
		t.Fatalf("quota %v", q)
	}
	w := q[0].Windows
	if len(w) != 2 || w[0].Name != "five_hour" || w[0].GetUsedPercent() != 80 || w[0].ResetsAtMs != 1790774400000 || w[1].UsedPercent != nil {
		t.Fatalf("windows %v", w)
	}
	// Repeated status line values do not renew their observation time.
	reportClaudeQuota(t, h.d, "s2", []byte(`{"rate_limits":{"five_hour":{"used_percentage":80,"resets_at":1790774400},"seven_day":{"resets_at":1790960400}}}`), now.Add(10*time.Second))
	if claudeQuota(h.d)[0].ObservedMs != now.UnixMilli() {
		t.Fatal("renewed unchanged data")
	}
	reportClaudeQuota(t, h.d, "s2", []byte(`{"rate_limits":{"five_hour":{"used_percentage":80,"resets_at":1790774400},"seven_day":{"resets_at":1790960400}}}`), now.Add(40*time.Minute))
	if claudeQuota(h.d)[0].ObservedMs != now.UnixMilli() {
		t.Fatal("idle repetition made old data fresh")
	}
	// An idle session repeats the limits of its last reply: they do not
	// replace newer ones, nor renew the time.
	later := now.Add(5 * time.Minute)
	reportClaudeQuota(t, h.d, "s2", []byte(`{"rate_limits":{"five_hour":{"used_percentage":86,"resets_at":1790774400},"seven_day":{"used_percentage":3,"resets_at":1790960400}}}`), later)
	reportClaudeQuota(t, h.d, "s1", []byte(`{"rate_limits":{"five_hour":{"used_percentage":80,"resets_at":1790774400},"seven_day":{"resets_at":1790960400}}}`), later.Add(5*time.Minute))
	q = claudeQuota(h.d)
	if w := q[0].Windows; len(w) != 2 || w[0].GetUsedPercent() != 86 || w[1].GetUsedPercent() != 3 || q[0].ObservedMs != later.UnixMilli() {
		t.Fatalf("stale report taken: %v", q)
	}
	// A window that has reset since is newer whatever its usage.
	reportClaudeQuota(t, h.d, "s1", []byte(`{"rate_limits":{"five_hour":{"used_percentage":1,"resets_at":1790792400}}}`), later.Add(6*time.Minute))
	if w := claudeQuota(h.d)[0].Windows; len(w) != 2 || w[0].GetUsedPercent() != 1 || w[0].ResetsAtMs != 1790792400000 || w[1].GetUsedPercent() != 3 {
		t.Fatalf("after a reset: %v", w)
	}
	// A restart keeps the record of the same account...
	d2 := New(h.d.dir, Config{})
	d2.agents[pb.Agent_AGENT_CLAUDE] = claude.New("/bin/true", home)
	d2.loadQuotas()
	if q := claudeQuota(d2); len(q) != 1 || q[0].GetWindows()[0].GetUsedPercent() != 1 {
		t.Fatalf("after restart: %v", q)
	}
	// ...but not another account's.
	writeAccount(t, home, "acct-2")
	d3 := New(h.d.dir, Config{})
	d3.agents[pb.Agent_AGENT_CLAUDE] = claude.New("/bin/true", home)
	d3.loadQuotas()
	if q := claudeQuota(d3); len(q) != 1 || q[0].State != pb.QuotaState_QUOTA_STATE_WAITING {
		t.Fatalf("other account: %v", q)
	}
	// A report under the new account drops the old one's record.
	reportClaudeQuota(t, h.d, "s1", []byte(`{"rate_limits":{"five_hour":{"used_percentage":5}}}`), now.Add(time.Hour))
	if q := claudeQuota(h.d); len(q) != 1 || q[0].GetWindows()[0].GetUsedPercent() != 5 {
		t.Fatalf("account change: %v", q)
	}
	// Without a known account, sessions are kept apart.
	os.Remove(filepath.Join(home, ".claude.json"))
	reportClaudeQuota(t, h.d, "s3", []byte(`{"rate_limits":{"five_hour":{"used_percentage":7}}}`), now.Add(2*time.Hour))
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
	reportClaudeQuota(t, h.d, "old", spent, now)
	first := claudeQuota(h.d)[0].Account

	writeAccount(t, home, "acct-2")
	reportClaudeQuota(t, h.d, "old", spent, now.Add(time.Minute))
	if q := claudeQuota(h.d); len(q) != 1 || q[0].State != pb.QuotaState_QUOTA_STATE_WAITING {
		t.Fatalf("the first account's limits shown for the second: %v", q)
	}
	reportClaudeQuota(t, h.d, "new", []byte(`{"rate_limits":{"five_hour":{"used_percentage":4,"resets_at":1790774400}}}`), now.Add(2*time.Minute))
	reportClaudeQuota(t, h.d, "old", spent, now.Add(3*time.Minute))
	q := claudeQuota(h.d)
	if len(q) != 1 || q[0].Account == first || q[0].Account == "" || q[0].Windows[0].GetUsedPercent() != 4 {
		t.Fatalf("after the second account reported: %v", q)
	}
	// Once the old session has a reply under the new login, its limits
	// change and count again.
	reportClaudeQuota(t, h.d, "old", []byte(`{"rate_limits":{"five_hour":{"used_percentage":9,"resets_at":1790774400}}}`), now.Add(4*time.Minute))
	if q := claudeQuota(h.d); q[0].Windows[0].GetUsedPercent() != 9 {
		t.Fatalf("a new reply's limits ignored: %v", q)
	}
}

func TestAgyQuotaFromStatusLine(t *testing.T) {
	h := newHarness(t, Config{})
	home := t.TempDir()
	h.d.agents[pb.Agent_AGENT_ANTIGRAVITY] = antigravity.New("/bin/true", home)

	h.s.ag = h.d.agents[pb.Agent_AGENT_ANTIGRAVITY]
	h.s.meta.NoFullAccess = true
	os.WriteFile(filepath.Join(h.s.dir, antigravity.SettingsFile), []byte(`{"toolPermission":"request-review"}`), 0600)
	h.d.sessions[h.s.meta.ID] = h.s
	if q := agyQuota(h.d); len(q) != 1 || q[0].State != pb.QuotaState_QUOTA_STATE_WAITING {
		t.Fatalf("before any report: %v", q)
	}
	now := time.Now()
	report := []byte(`{
		"email": "test@example.com",
		"plan_tier": "Pro",
		"product":"antigravity",
        "cycle_mode": "auto",
		"quota": {
			"gemini-5h": {"remaining_fraction": 0.85, "reset_time": "2026-09-30T13:20:00Z"},
			"gemini-weekly": {"remaining_fraction": 0.40, "reset_time": "2026-10-02T17:00:00Z"}
		}
	}`)
	h.d.statusLine(h.s.meta.ID, report, now)
	q := agyQuota(h.d)
	if len(q) != 1 || q[0].State != pb.QuotaState_QUOTA_STATE_OK || q[0].Plan != "Pro" || q[0].Login != pb.QuotaLogin_QUOTA_LOGIN_SUBSCRIPTION || q[0].Account == "" || q[0].ObservedMs != now.UnixMilli() {
		t.Fatalf("quota unexpected: %v", q)
	}
	// Check email fingerprint
	expectedFP := antigravity.EmailFingerprint("test@example.com")
	if q[0].Account != expectedFP {
		t.Fatalf("expected account %s, got %s", expectedFP, q[0].Account)
	}
	w := q[0].Windows
	if len(w) != 2 {
		t.Fatalf("expected 2 windows, got %v", w)
	}
	if w[0].Name != "gemini-5h" || math.Abs(w[0].GetUsedPercent()-15) > 0.0001 || w[0].ResetsAtMs != 1790774400000 {
		t.Fatalf("window 0 mismatch: %v", w[0])
	}
	if w[1].Name != "gemini-weekly" || w[1].GetUsedPercent() != 60 || w[1].ResetsAtMs != 1790960400000 {
		t.Fatalf("window 1 mismatch: %v", w[1])
	}
}

func reportClaudeQuota(t *testing.T, d *Daemon, id string, raw []byte, now time.Time) {
	t.Helper()
	source := d.agents[pb.Agent_AGENT_CLAUDE].(agent.StatusLineSource)
	report, err := source.ParseStatusLine(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	if report.Quota != nil {
		d.recordQuota(id, report.Quota, now)
	}
}

func TestQuotaStatusLineFailureAndRecovery(t *testing.T) {
	h := newHarness(t, Config{})
	h.d.agents[pb.Agent_AGENT_ANTIGRAVITY] = antigravity.New("/bin/true", t.TempDir())
	h.s.ag = h.d.agents[pb.Agent_AGENT_ANTIGRAVITY]
	h.d.sessions[h.s.meta.ID] = h.s
	if err := os.WriteFile(filepath.Join(h.s.dir, antigravity.SettingsFile), []byte(`{"toolPermission":"request-review"}`), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	valid := []byte(`{"product":"antigravity","quota":{"gemini-5h":{"remaining_fraction":0.7}}}`)
	h.d.statusLine(h.s.meta.ID, []byte(`{"private":"never expose this",`), now)
	q := agyQuota(h.d)[0]
	if q.State != pb.QuotaState_QUOTA_STATE_FAILED || q.Detail == "" || strings.Contains(q.Detail, "private") || q.ObservedMs != 0 {
		t.Fatalf("failure not safely exposed: %v", q)
	}
	h.d.statusLine(h.s.meta.ID, valid, now)
	q = agyQuota(h.d)[0]
	if q.State != pb.QuotaState_QUOTA_STATE_OK || q.Detail != "" || len(q.Windows) != 1 {
		t.Fatalf("did not recover: %v", q)
	}
	h.d.statusLine(h.s.meta.ID, []byte(`{`), now.Add(time.Minute))
	q = agyQuota(h.d)[0]
	if q.State != pb.QuotaState_QUOTA_STATE_FAILED || len(q.Windows) != 1 || q.ObservedMs != now.UnixMilli() {
		t.Fatalf("lost last successful record: %v", q)
	}
	// A successful status line without quota clears the parse error but does
	// not pretend the previous quota observation is new.
	h.d.statusLine(h.s.meta.ID, []byte(`{"product":"antigravity"}`), now.Add(2*time.Minute))
	q = agyQuota(h.d)[0]
	if q.State != pb.QuotaState_QUOTA_STATE_OK || q.ObservedMs != now.UnixMilli() {
		t.Fatalf("renewed absent data: %v", q)
	}
}
