package daemon

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/antigravity"
	"github.com/Lewin671/remote-agent/internal/i18n"
)

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time          { return c.now }
func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

func writeFakeDaemonAgy(t *testing.T, logDir, countFile, modeFile string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "agy")
	body := fmt.Sprintf(`#!/bin/sh
if [ "$1" = "--version" ]; then
	echo "1.5.10"
	exit 0
fi
n=0
if [ -f %q ]; then
	n=$(cat %q)
fi
n=$((n + 1))
printf '%%s' "$n" > %q

# Every agy invocation (including /usage itself) creates a cli-*.log file.
mkdir -p %q
printf 'I1117 10:00:00.000 server_oauth.go:221] applyAuthResult: email=probe-user@example.com\n' > %q/cli-probe-${n}.log

mode=$(cat %q 2>/dev/null || echo ok)
case "$mode" in
ok)
	cat <<'EOF'
{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"Gemini Models","buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":0.6,"reset_time":"2026-11-17T17:00:00Z"},{"id":"gemini-weekly","window":"weekly","remaining_fraction":0.8,"reset_time":"2026-11-23T17:00:00Z"}]}]}}}
EOF
	;;
recovered)
	cat <<'EOF'
{"status":"SUCCESS","command":{"name":"usage","data":{"groups":[{"name":"Gemini Models","buckets":[{"id":"gemini-5h","window":"5h","remaining_fraction":0.7,"reset_time":"2026-11-17T17:00:00Z"},{"id":"gemini-weekly","window":"weekly","remaining_fraction":0.8,"reset_time":"2026-11-23T17:00:00Z"}]}]}}}
EOF
	;;
not_logged_in)
	open https://accounts.example.com/oauth
	exit 1
	;;
unsupported)
	echo '{"status":"SUCCESS","response":"no command"}'
	;;
esac
`, countFile, countFile, countFile, logDir, logDir, modeFile)
	if err := os.WriteFile(script, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return script
}

func readProbeCount(t *testing.T, countFile string) int {
	t.Helper()
	b, err := os.ReadFile(countFile)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAgyQuotaStartupAndNoSelfExcitation(t *testing.T) {
	home := t.TempDir()
	logDir := filepath.Join(home, "log")
	countFile := filepath.Join(t.TempDir(), "count")
	modeFile := filepath.Join(t.TempDir(), "mode")
	script := writeFakeDaemonAgy(t, logDir, countFile, modeFile)

	h := newHarness(t, Config{})
	h.d.agents[pb.Agent_AGENT_ANTIGRAVITY] = antigravity.New(script, home)

	clk := &fakeClock{now: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
	m := &agyQuotaMonitor{now: clk.Now}

	// Startup with no saved agy quota probes once, even though the probe
	// itself creates cli-probe-1.log inside logDir.
	h.d.stepAgyQuota(context.Background(), m)
	if got := readProbeCount(t, countFile); got != 1 {
		t.Fatalf("startup probe count = %d, want 1", got)
	}
	q := agyQuota(h.d)
	if len(q) != 1 || q[0].State != pb.QuotaState_QUOTA_STATE_OK || q[0].Account != antigravity.EmailFingerprint("probe-user@example.com") {
		t.Fatalf("unexpected startup quota: %v", q)
	}
	if len(q[0].Windows) != 2 || math.Abs(q[0].Windows[0].GetUsedPercent()-40) > 1e-6 {
		t.Fatalf("unexpected windows: %v", q[0].Windows)
	}

	// Subsequent ticks across 10 minutes must NOT self-trigger on cli-probe-1.log.
	for i := 0; i < 30; i++ {
		clk.Advance(20 * time.Second)
		h.d.stepAgyQuota(context.Background(), m)
	}
	if got := readProbeCount(t, countFile); got != 1 {
		t.Fatalf("self-excitation occurred: probe count = %d, want 1", got)
	}
}

func TestAgyQuotaSkipsStartupWhenSavedAndDebouncesActivity(t *testing.T) {
	home := t.TempDir()
	logDir := filepath.Join(home, "log")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	countFile := filepath.Join(t.TempDir(), "count")
	modeFile := filepath.Join(t.TempDir(), "mode")
	script := writeFakeDaemonAgy(t, logDir, countFile, modeFile)

	h := newHarness(t, Config{})
	ag := antigravity.New(script, home)
	h.d.agents[pb.Agent_AGENT_ANTIGRAVITY] = ag

	// Seed a saved quota record (e.g. from statusline or quota.json).
	clk := &fakeClock{now: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
	used := 10.0
	h.d.quota.record("statusline-seed", &pb.AgentQuota{
		Agent:   pb.Agent_AGENT_ANTIGRAVITY,
		Account: antigravity.EmailFingerprint("seed@example.com"),
		State:   pb.QuotaState_QUOTA_STATE_OK,
		Windows: []*pb.QuotaWindow{{Name: "gemini-5h", UsedPercent: &used, ResetsAtMs: clk.now.Add(time.Hour).UnixMilli()}},
	}, clk.now, false)

	// Pre-existing log file before daemon startup should not trigger.
	preLog := filepath.Join(logDir, "cli-20261117_090000.log")
	if err := os.WriteFile(preLog, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	m := &agyQuotaMonitor{now: clk.Now}
	h.d.stepAgyQuota(context.Background(), m)
	if got := readProbeCount(t, countFile); got != 0 {
		t.Fatalf("expected 0 probes when saved quota exists, got %d", got)
	}

	// External continuous session activity: updates every 15s for 3 minutes.
	extLog := filepath.Join(logDir, "cli-20261117_100100.log")
	for step := 0; step < 12; step++ {
		clk.Advance(15 * time.Second)
		if err := os.WriteFile(extLog, []byte(fmt.Sprintf("line %d\n", step)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(extLog, clk.now, clk.now); err != nil {
			t.Fatal(err)
		}
		h.d.stepAgyQuota(context.Background(), m)
		// Before 3m (step 11 is +165s after step 0), it must not probe.
		if step < 11 && readProbeCount(t, countFile) != 0 {
			t.Fatalf("probed too early during continuous activity at step %d", step)
		}
	}
	// Step 11 is 165s after firstActivity (step 0). Advance 15s more -> 180s (3m).
	clk.Advance(15 * time.Second)
	h.d.stepAgyQuota(context.Background(), m)
	if got := readProbeCount(t, countFile); got != 1 {
		t.Fatalf("expected 1 probe at 3m max continuous interval, got %d", got)
	}

	// Idle after probe -> must not re-trigger.
	for i := 0; i < 6; i++ {
		clk.Advance(10 * time.Second)
		h.d.stepAgyQuota(context.Background(), m)
	}
	if got := readProbeCount(t, countFile); got != 1 {
		t.Fatalf("unexpected probe while idle: %d", got)
	}

	// Short external run with email line -> debounces 20s quiet, then probes.
	shortLog := filepath.Join(logDir, "cli-20261117_101000.log")
	if err := os.WriteFile(shortLog, []byte("I1117 10:10:00.100 server_oauth.go:221] applyAuthResult: email=ext-user@example.com, authMethod=consumer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(shortLog, clk.now, clk.now); err != nil {
		t.Fatal(err)
	}
	h.d.stepAgyQuota(context.Background(), m)
	clk.Advance(19 * time.Second)
	h.d.stepAgyQuota(context.Background(), m)
	if got := readProbeCount(t, countFile); got != 1 {
		t.Fatalf("probed before 20s quiet debounce: %d", got)
	}
	clk.Advance(1 * time.Second)
	h.d.stepAgyQuota(context.Background(), m)
	if got := readProbeCount(t, countFile); got != 2 {
		t.Fatalf("expected probe after 20s quiet, got %d", got)
	}
	q := agyQuota(h.d)
	if len(q) != 1 || q[0].Account != antigravity.EmailFingerprint("probe-user@example.com") || q[0].Login != pb.QuotaLogin_QUOTA_LOGIN_SUBSCRIPTION {
		t.Fatalf("expected the probe login account without duplicates, got %v", q)
	}
}

func TestAgyQuotaUninstalledDoesNotProbe(t *testing.T) {
	home := t.TempDir()
	logDir := filepath.Join(home, "log")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(logDir, "cli-20261117_100000.log"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := newHarness(t, Config{})
	h.d.agents[pb.Agent_AGENT_ANTIGRAVITY] = antigravity.New("", home)
	m := newAgyQuotaMonitor()
	h.d.stepAgyQuota(context.Background(), m)
	if m.baselineReady || m.startupChecked {
		t.Fatal("uninstalled agy should not scan or start monitoring")
	}
}

func TestAgyQuotaErrorsPreserveLastDataAndDistinguishReasons(t *testing.T) {
	home := t.TempDir()
	logDir := filepath.Join(home, "log")
	countFile := filepath.Join(t.TempDir(), "count")
	modeFile := filepath.Join(t.TempDir(), "mode")
	script := writeFakeDaemonAgy(t, logDir, countFile, modeFile)

	h := newHarness(t, Config{})
	ag := antigravity.New(script, home)
	h.d.agents[pb.Agent_AGENT_ANTIGRAVITY] = ag

	// Before any probe or statusline, WAITING carries the agy-specific waiting text.
	waiting := agyQuota(h.d)
	if len(waiting) != 1 || waiting[0].State != pb.QuotaState_QUOTA_STATE_WAITING || waiting[0].Detail != i18n.QuotaAgyWaiting("zh-CN") {
		t.Fatalf("unexpected waiting quota: %v", waiting)
	}

	clk := &fakeClock{now: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
	m := &agyQuotaMonitor{now: clk.Now}

	// Startup probe succeeds (40% used on gemini-5h).
	h.d.stepAgyQuota(context.Background(), m)
	okObs := agyQuota(h.d)[0].ObservedMs

	trigger := func(mode string) {
		if err := os.WriteFile(modeFile, []byte(mode), 0o600); err != nil {
			t.Fatal(err)
		}
		clk.Advance(time.Minute)
		logPath := filepath.Join(logDir, fmt.Sprintf("cli-%d.log", clk.now.Unix()))
		if err := os.WriteFile(logPath, []byte("I1117 10:01:00.000 server_oauth.go:221] applyAuthResult: email=err@example.com\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		h.d.stepAgyQuota(context.Background(), m)
		clk.Advance(agyLogQuietDebounce)
		h.d.stepAgyQuota(context.Background(), m)
	}

	trigger("not_logged_in")
	q := agyQuota(h.d)[0]
	if q.State != pb.QuotaState_QUOTA_STATE_FAILED || q.Detail != i18n.QuotaAgyProbeNotLoggedIn("zh-CN") || len(q.Windows) != 2 || q.ObservedMs != okObs {
		t.Fatalf("not_logged_in did not preserve last success: %v", q)
	}

	trigger("unsupported")
	q = agyQuota(h.d)[0]
	if q.State != pb.QuotaState_QUOTA_STATE_FAILED || q.Detail != i18n.QuotaAgyProbeUnsupported("zh-CN") || len(q.Windows) != 2 || q.ObservedMs != okObs {
		t.Fatalf("unsupported did not preserve last success: %v", q)
	}

	// Recovery with lower usage (credit recovery: 40% -> 30%) must be accepted (monotonic=false).
	trigger("recovered")
	qList := agyQuota(h.d)
	if len(qList) != 1 || qList[0].State != pb.QuotaState_QUOTA_STATE_OK || qList[0].Detail != "" {
		t.Fatalf("did not recover cleanly: %v", qList)
	}
	if math.Abs(qList[0].Windows[0].GetUsedPercent()-30) > 1e-6 {
		t.Fatalf("credit recovery rejected: %v", qList[0].Windows[0])
	}
	if qList[0].Account != antigravity.EmailFingerprint("probe-user@example.com") {
		t.Fatalf("account fingerprint missing after recovery: %v", qList[0])
	}
}
