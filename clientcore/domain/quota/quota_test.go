package quota

import (
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func f64(v float64) *float64 { return &v }

func keyOf(t *corev1.Text) string {
	if t == nil {
		return ""
	}
	if t.Key != "" {
		return t.Key
	}
	return t.Literal
}

func TestFreshnessBoundaries(t *testing.T) {
	now := int64(1_760_000_000_000)
	if got := Freshness(now-29*msPerMinute, now); got != nil {
		t.Fatalf("<30m should be fresh, got %+v", got)
	}
	if got := keyOf(Freshness(now-30*msPerMinute, now)); got != text.KeyQuotaOldMinutes {
		t.Fatalf("30m expected old_minutes, got %q", got)
	}
	if got := keyOf(Freshness(now-60*msPerMinute, now)); got != text.KeyQuotaOldHours {
		t.Fatalf("60m expected old_hours, got %q", got)
	}
	if got := keyOf(Freshness(0, now)); got != text.KeyQuotaUnknownTime {
		t.Fatalf("0 expected unknown_time, got %q", got)
	}
}

func TestBeijingDayRolloverAndResetClock(t *testing.T) {
	// 2026-11-16 15:30:00 UTC = 2026-11-16 23:30:00 UTC+8
	// 2026-11-16 16:30:00 UTC = 2026-11-17 00:30:00 UTC+8 (tomorrow in Beijing!)
	// Base: 2026-11-16 00:00:00 UTC = 20773 days since epoch? Compute deterministically:
	// 1970-01-01 00:00:00 UTC + 100*86400s:
	baseMidnightUTC := int64(100) * msPerDay
	now := baseMidnightUTC + 15*msPerHour + 30*msPerMinute     // 23:30 UTC+8 same day
	sameDay := baseMidnightUTC + 15*msPerHour + 50*msPerMinute // 23:50 UTC+8 same day
	nextDay := baseMidnightUTC + 16*msPerHour + 30*msPerMinute // 00:30 UTC+8 next day

	if got := Clock(nextDay); got != "00:30" {
		t.Fatalf("Clock(nextDay) = %q, want 00:30", got)
	}
	if got := keyOf(ResetClock(sameDay, now)); got != "23:50" {
		t.Fatalf("same Beijing day expected plain 23:50, got %q", got)
	}
	clkNext := ResetClock(nextDay, now)
	if got := keyOf(clkNext); got != text.KeyTimeDayClock {
		t.Fatalf("next Beijing day expected time.day_clock, got %q", got)
	}
}

func TestBuildWindowThresholdsAndPastReset(t *testing.T) {
	now := int64(1_760_000_000_000)

	normal := BuildWindow(v1.Agent_AGENT_CLAUDE, &v1.QuotaWindow{
		Name:        "five_hour",
		UsedPercent: f64(79.4),
		ResetsAtMs:  now + 45*msPerMinute,
	}, now, false)
	if normal.Tone != corev1.QuotaTone_QUOTA_TONE_NORMAL || normal.RemainingPercent != 21 {
		t.Fatalf("79.4%% should round to 79%% NORMAL (21%% left), got tone=%v rem=%d", normal.Tone, normal.RemainingPercent)
	}

	warn := BuildWindow(v1.Agent_AGENT_CLAUDE, &v1.QuotaWindow{
		Name:        "five_hour",
		UsedPercent: f64(80),
		ResetsAtMs:  now + 45*msPerMinute,
	}, now, false)
	if warn.Tone != corev1.QuotaTone_QUOTA_TONE_WARN || warn.RemainingPercent != 20 {
		t.Fatalf("80%% expected WARN, got tone=%v rem=%d", warn.Tone, warn.RemainingPercent)
	}

	full := BuildWindow(v1.Agent_AGENT_CLAUDE, &v1.QuotaWindow{
		Name:        "seven_day",
		UsedPercent: f64(100),
		ResetsAtMs:  now + 2*msPerHour,
	}, now, false)
	if full.Tone != corev1.QuotaTone_QUOTA_TONE_FULL || !full.Exhausted || keyOf(full.RemainingText) != text.KeyQuotaExhaustedQuota {
		t.Fatalf("100%% expected FULL + exhausted_quota, got %+v", full)
	}

	past := BuildWindow(v1.Agent_AGENT_CODEX, &v1.QuotaWindow{
		Name:        "primary",
		UsedPercent: f64(90),
		ResetsAtMs:  now - 1000,
	}, now, false)
	if !past.PastReset || past.Tone != corev1.QuotaTone_QUOTA_TONE_STALE || keyOf(past.RemainingText) != text.KeyQuotaAwaitingUpdate {
		t.Fatalf("past reset expected STALE + awaiting_update, got %+v", past)
	}
}

func TestAntigravityGroupingMissingOmittedAndBannerPriority(t *testing.T) {
	now := int64(1_760_000_000_000)
	online := true
	m1 := &lists.Machine{
		ID:        "m1",
		Online:    true,
		Reachable: &online,
		Meta: &v1.MachineMeta{
			Hostname: "Mac",
			Agents: []*v1.AgentInfo{
				{Agent: v1.Agent_AGENT_ANTIGRAVITY, Version: "1.0"},
				{Agent: v1.Agent_AGENT_CLAUDE, Version: "2.0"},
			},
			Quotas: []*v1.AgentQuota{
				{
					Agent:      v1.Agent_AGENT_ANTIGRAVITY,
					State:      v1.QuotaState_QUOTA_STATE_OK,
					ObservedMs: now - 5*msPerMinute,
					Windows: []*v1.QuotaWindow{
						{Name: "3p-5h", UsedPercent: f64(85), ResetsAtMs: now + 30*msPerMinute},
						{Name: "gemini-5h", UsedPercent: nil, ResetsAtMs: now + 30*msPerMinute}, // missing pct must be omitted!
						{Name: "gemini-argon-wtu-weekly", UsedPercent: f64(100), ResetsAtMs: now + msPerDay},
					},
				},
			},
		},
	}

	secs := BuildCliSections(m1, v1.Agent_AGENT_UNSPECIFIED, now)
	if len(secs) != 2 {
		t.Fatalf("expected 2 CLI sections (Antigravity + Claude), got %d", len(secs))
	}
	ag := secs[0]
	if len(ag.Accounts) != 1 || len(ag.Accounts[0].Groups) != 2 {
		t.Fatalf("expected 2 antigravity groups (Gemini + 3P), got %+v", ag.Accounts)
	}
	if len(ag.Accounts[0].Groups[0].Rows) != 1 || ag.Accounts[0].Groups[0].Rows[0].WindowName != "gemini-argon-wtu-weekly" {
		t.Fatalf("missing UsedPercent window gemini-5h should be omitted; got %+v", ag.Accounts[0].Groups[0].Rows)
	}
	if !secs[1].Unsupported {
		t.Fatalf("installed Claude without quota entry should be marked unsupported")
	}

	b := Banner([]*lists.Machine{m1}, now)
	if b == nil || b.Severity != corev1.QuotaBannerSeverity_QUOTA_BANNER_SEVERITY_FULL || b.WindowName != "gemini-argon-wtu-weekly" {
		t.Fatalf("banner should prioritize 100%% exhausted over 85%% warn, got %+v", b)
	}
}

func TestAntigravityWaitingDetailUsesAgyCopyWithoutErrorText(t *testing.T) {
	now := int64(1_760_000_000_000)
	online := true
	m := &lists.Machine{
		ID:        "m1",
		Online:    true,
		Reachable: &online,
		Meta: &v1.MachineMeta{
			Hostname: "Mac",
			Agents:   []*v1.AgentInfo{{Agent: v1.Agent_AGENT_ANTIGRAVITY, Version: "1.5.10"}},
			Quotas: []*v1.AgentQuota{
				{Agent: v1.Agent_AGENT_ANTIGRAVITY, State: v1.QuotaState_QUOTA_STATE_WAITING},
			},
		},
	}
	secs := BuildCliSections(m, v1.Agent_AGENT_UNSPECIFIED, now)
	if len(secs) != 1 || len(secs[0].Accounts) != 1 {
		t.Fatalf("unexpected sections: %+v", secs)
	}
	acct := secs[0].Accounts[0]
	if acct.ErrorText != nil || keyOf(acct.StatusText) != text.KeyQuotaAgyWaiting {
		t.Fatalf("expected quota.agy_waiting StatusText with nil ErrorText, got status=%+v err=%+v", acct.StatusText, acct.ErrorText)
	}

	m.Meta.Quotas[0].Detail = "custom daemon waiting detail"
	secs2 := BuildCliSections(m, v1.Agent_AGENT_UNSPECIFIED, now)
	acct2 := secs2[0].Accounts[0]
	if acct2.ErrorText != nil || keyOf(acct2.StatusText) != "custom daemon waiting detail" {
		t.Fatalf("expected daemon Detail as plain StatusText without ErrorText, got status=%+v err=%+v", acct2.StatusText, acct2.ErrorText)
	}
}
