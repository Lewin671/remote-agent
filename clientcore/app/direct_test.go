package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/machine"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
	"github.com/Lewin671/remote-agent/clientcore/text"
	"github.com/Lewin671/remote-agent/clientcore/transport"
)

func TestAppDirectDiagnosticsViewAndPreferences(t *testing.T) {
	c := New()
	defer c.Close()

	now := int64(1_791_078_000_000)
	acct := "00000000000000000000000000000001"
	c.Dispatch(&corev1.Event{
		NowMs:     now,
		MonoMs:    now,
		RandomIds: []string{"r1", "r2", "r3", "r4", "r5", "r6", "r7", "r8"},
		Event: &corev1.Event_TransportConfig{
			TransportConfig: &corev1.TransportConfigEvent{
				Supported:       true,
				Account:         acct,
				ProtocolVersion: 17,
				PlatformAgent:   "TestBrowser/1.0",
			},
		},
	})

	// Seed machine & session so MachineSettings and Session views build cleanly.
	_, _ = c.m.sync.HandleEvent(&corev1.Event{
		NowMs:  now,
		MonoMs: now,
		Event:  &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START}},
	})
	// Seed world machine and session directly via ListsFetched isn't needed for DirectDiagnostics screen;
	// verify DirectDiagnosticsScreen builds localized checks and report lines via View().
	vm, err := c.View(&corev1.Screen{
		Screen: &corev1.Screen_DirectDiagnostics{
			DirectDiagnostics: &corev1.DirectDiagnosticsScreenRequest{MachineId: "m-1"},
		},
	})
	if err != nil {
		t.Fatalf("View(DirectDiagnostics): %v", err)
	}
	dd := vm.GetDirectDiagnostics()
	if dd == nil || dd.MachineId != "m-1" || !dd.Supported || !dd.Enabled {
		t.Fatalf("unexpected DirectDiagnosticsViewModel: %+v", dd)
	}
	if len(dd.Checks) != 3 || len(dd.ReportLines) < 2 {
		t.Fatalf("expected 3 checks and >=2 report lines, got checks=%d lines=%d", len(dd.Checks), len(dd.ReportLines))
	}
	if dd.Checks[0].GetLabelText().GetKey() != text.KeyTransportDiagCheckPeer {
		t.Fatalf("unexpected check[0] key: %+v", dd.Checks[0].GetLabelText())
	}

	// UpdateDirectPreference routes through Coordinator and emits DirectPreferenceEffect + DirectDiagnostics target.
	batch := c.Dispatch(&corev1.Event{
		NowMs:  now + 100,
		MonoMs: now + 100,
		Event: &corev1.Event_UpdateDirectPreference{
			UpdateDirectPreference: &corev1.UpdateDirectPreferenceEvent{MachineId: "m-1", Enabled: false},
		},
	})
	var sawPrefEff, sawDiagRender bool
	for _, eff := range batch.GetEffects() {
		if dp := eff.GetDirectPreference(); dp != nil && dp.MachineId == "m-1" && !dp.Enabled {
			sawPrefEff = true
		}
		if r := eff.GetRender(); r != nil {
			for _, tgt := range r.Screens {
				if tgt == corev1.ScreenTarget_SCREEN_TARGET_DIRECT_DIAGNOSTICS {
					sawDiagRender = true
				}
			}
		}
	}
	if !sawPrefEff || !sawDiagRender {
		t.Fatalf("expected DirectPreferenceEffect (%v) and SCREEN_TARGET_DIRECT_DIAGNOSTICS (%v)", sawPrefEff, sawDiagRender)
	}

	vm2, err := c.View(&corev1.Screen{
		Screen: &corev1.Screen_DirectDiagnostics{
			DirectDiagnostics: &corev1.DirectDiagnosticsScreenRequest{MachineId: "m-1"},
		},
	})
	if err != nil || vm2.GetDirectDiagnostics().GetEnabled() {
		t.Fatalf("expected Enabled=false after UpdateDirectPreference, got %+v (err=%v)", vm2.GetDirectDiagnostics(), err)
	}

	// Verify BuildDirectDiagnosticsView cases (peer outdated, no reply, probe blocked, nat blocked) & BuildSessionDirectChip.
	rOutdated := []*transport.RoundData{{N: 1, StartedAt: now - 2000, EndedAt: 500, End: transport.EndPeerUnsupported, Unsupported: true}}
	dOutdated := screens.BuildDirectDiagnosticsView("m-1", transport.Status{State: transport.StateNone, Reason: transport.ReasonPeerUnsupported}, true, false, rOutdated, 0, false, now, "Chrome/131")
	if !dOutdated.PeerOutdated || dOutdated.CanRetry || dOutdated.TitleText.GetKey() != text.KeyTransportReasonPeerUnsupported {
		t.Fatalf("unexpected peer outdated diag: %+v", dOutdated)
	}

	rNoReply := []*transport.RoundData{{N: 1, StartedAt: now - 3000, EndedAt: 10000, End: transport.EndNoResponse}}
	dNoReply := screens.BuildDirectDiagnosticsView("m-1", transport.Status{State: transport.StateNone, Reason: transport.ReasonNoResponse}, true, false, rNoReply, 0, false, now, "Chrome/131")
	if dNoReply.TitleText.GetKey() != text.KeyTransportDiagTitleNoReply || !dNoReply.CanRetry {
		t.Fatalf("unexpected no_reply diag: %+v", dNoReply)
	}

	connStatus := transport.Status{
		State:    transport.StateConnected,
		Read:     transport.Direct,
		File:     transport.Direct,
		Terminal: transport.Direct,
		Measured: transport.Measured{
			HasReadMs: [2]bool{false, true},
			ReadMs:    [2]float64{0, 18.4},
		},
	}
	rep := screens.BuildDirectRouteReport(connStatus, true, false, 1)
	chip := screens.BuildSessionDirectChip(rep, text.Plain("MacBook"), true, false, false)
	if chip == nil || !chip.Visible || !chip.Active || !chip.ReadDirect || !chip.FileDirect || !chip.TerminalDirect {
		t.Fatalf("expected active SessionDirectChipView with TerminalDirect, got %+v", chip)
	}
	if chip.PopoverTitle.GetKey() != text.KeyTransportPopoverDirectTitleRtt || chip.PopoverFoot.GetKey() != text.KeyTransportPopoverStable {
		t.Fatalf("unexpected chip popover keys: title=%v foot=%v", chip.PopoverTitle, chip.PopoverFoot)
	}
	// Phase 3: terminal-only direct route still activates the top-bar direct chip.
	termOnlyRep := screens.BuildDirectRouteReport(transport.Status{
		State:    transport.StateConnected,
		Read:     transport.Relay,
		File:     transport.Relay,
		Terminal: transport.Direct,
	}, true, false, 0)
	termOnlyChip := screens.BuildSessionDirectChip(termOnlyRep, text.Plain("MacBook"), true, false, false)
	if termOnlyChip == nil || !termOnlyChip.Visible || !termOnlyChip.Active || !termOnlyChip.TerminalDirect || termOnlyChip.ReadDirect || termOnlyChip.FileDirect {
		t.Fatalf("expected terminal-only direct chip active, got %+v", termOnlyChip)
	}
	_ = v1.Session{}
}

func loadTransportZhCN(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "i18n", "messages", "transport.json"))
	if err != nil {
		t.Fatalf("read transport.json: %v", err)
	}
	var entries []struct {
		Key  string `json:"key"`
		ZhCN any    `json:"zh-CN"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("unmarshal transport.json: %v", err)
	}
	out := make(map[string]string, len(entries))
	for _, e := range entries {
		if s, ok := e.ZhCN.(string); ok {
			out[e.Key] = s
		}
	}
	// Include time.just_now from common/time if needed.
	out["time.just_now"] = "刚刚"
	return out
}

func renderTextZh(dict map[string]string, tx *corev1.Text) string {
	if tx == nil {
		return ""
	}
	if len(tx.Join) > 0 {
		parts := make([]string, 0, len(tx.Join))
		for _, part := range tx.Join {
			parts = append(parts, renderTextZh(dict, part))
		}
		sep := tx.SeparatorLiteral
		if sep == "" && tx.SeparatorKey != "" {
			if s, ok := dict[tx.SeparatorKey]; ok {
				sep = s
			}
		}
		return strings.Join(parts, sep)
	}
	if tx.Key == "" {
		return tx.Literal
	}
	tmpl, ok := dict[tx.Key]
	if !ok {
		return "[" + tx.Key + "]"
	}
	for _, a := range tx.Args {
		var val string
		switch v := a.Value.(type) {
		case *corev1.TextArg_StrValue:
			val = v.StrValue
		case *corev1.TextArg_IntValue:
			val = strconv.FormatInt(v.IntValue, 10)
		case *corev1.TextArg_TextValue:
			val = renderTextZh(dict, v.TextValue)
		}
		tmpl = strings.ReplaceAll(tmpl, "{"+a.Name+"}", val)
	}
	return tmpl
}

func TestDirectDiagnosticsConnectedThenDropped(t *testing.T) {
	dict := loadTransportZhCN(t)
	now := int64(1_791_078_010_000)

	// Build a realistic "connected then dropped by probe timeout" round via Engine.
	e := transport.NewEngine(true)
	e.Handle(now-6000, transport.Entropy{Units: []float64{0.5, 0.5, 0.5, 0.5}})
	e.Handle(now-6000, transport.MainLeg{Ready: true})
	e.Handle(now-6000, transport.SecuritySynced{
		Member:        true,
		Account:       "acct",
		Head:          "head-1",
		StartedAt:     now - 6000,
		LeaseNotAfter: now + 60_000,
		Machines:      []string{"m-1"},
	})
	roundsBefore, _, _ := e.Diagnostics("m-1")
	if len(roundsBefore) != 1 {
		t.Fatalf("expected 1 round started, got %d", len(roundsBefore))
	}
	rid := transport.RoundID(1)
	e.Handle(now-5800, transport.DiagAttempt{Round: rid, Use: transport.UseRead})
	e.Handle(now-5700, transport.DiagCandidate{Round: rid, Use: transport.UseRead, Remote: false, Line: "candidate:1 1 udp 1 192.168.1.20 54321 typ host"})
	e.Handle(now-5700, transport.DiagCandidate{Round: rid, Use: transport.UseRead, Remote: true, Line: "candidate:1 1 udp 1 2001:db8:2::9 54321 typ host"})
	e.Handle(now-5600, transport.DiagState{Round: rid, Use: transport.UseRead, Kind: transport.StateICE, Value: "checking"})
	e.Handle(now-5300, transport.DiagState{Round: rid, Use: transport.UseRead, Kind: transport.StateICE, Value: "connected"})
	e.Handle(now-5200, transport.DiagState{Round: rid, Use: transport.UseRead, Kind: transport.StateDTLS, Value: "connected"})
	e.Handle(now-5100, transport.DiagState{Round: rid, Use: transport.UseRead, Kind: transport.StateDC, Value: "open"})
	e.Handle(now-5000, transport.PathReady{Round: rid, Use: transport.UseRead, Path: 11})

	// 4.2s later, 3 consecutive probes go unanswered -> DiagPathEnd + PathClosed.
	dropAt := now - 800
	e.Handle(dropAt, transport.DiagPathEnd{
		Round:  rid,
		Use:    transport.UseRead,
		Cause:  transport.CloseProbeTimeout,
		Probes: transport.ProbeStats{Sent: 5, Echoed: 2, Lost: 3, Missed: 3},
	})
	e.Handle(dropAt, transport.PathClosed{Round: rid, Use: transport.UseRead})

	// Immediately after drop (retry scheduled in the future):
	stRetry := e.Status(dropAt, "m-1")
	if stRetry.State != transport.StateNone || stRetry.Reason != transport.ReasonLost || !stRetry.RetryScheduled {
		t.Fatalf("expected StateNone/ReasonLost/RetryScheduled=true, got %+v", stRetry)
	}
	repRetry := screens.BuildDirectRouteReport(stRetry, true, false, 1)
	cgRetry := machine.BuildConnectionGroup(true, true, repRetry)
	statusLineRetry := renderTextZh(dict, cgRetry.StatusText) + " · " + renderTextZh(dict, cgRetry.DetailText)
	if renderTextZh(dict, cgRetry.DetailText) != "直连断了，稍后会自动再试" {
		t.Fatalf("unexpected lost+retry detail: %q", statusLineRetry)
	}

	// After retry window elapses (RetryScheduled == false):
	stNoRetry := e.Status(now, "m-1")
	if stNoRetry.State != transport.StateNone || stNoRetry.Reason != transport.ReasonLost || stNoRetry.RetryScheduled {
		t.Fatalf("expected StateNone/ReasonLost/RetryScheduled=false at now, got %+v", stNoRetry)
	}
	repNoRetry := screens.BuildDirectRouteReport(stNoRetry, true, false, 1)
	cgNoRetry := machine.BuildConnectionGroup(true, true, repNoRetry)
	if renderTextZh(dict, cgNoRetry.DetailText) != "直连断开了" {
		t.Fatalf("unexpected lost+no-retry detail: %q", renderTextZh(dict, cgNoRetry.DetailText))
	}

	rounds, blockedAt, blocked := e.Diagnostics("m-1")
	dd := screens.BuildDirectDiagnosticsView("m-1", stRetry, true, false, rounds, blockedAt, blocked, dropAt, "Android/RemoteAgent")
	if dd.Connected {
		t.Fatal("diagnostics card MUST NOT report Connected when status.State != StateConnected")
	}
	cardTitle := renderTextZh(dict, dd.TitleText)
	cardBody := renderTextZh(dict, dd.BodyText)
	if strings.Contains(cardTitle, "已恢复直连") || strings.Contains(cardBody, "已恢复直连") {
		t.Fatalf("card must never say 已恢复直连 when dropped: title=%q body=%q", cardTitle, cardBody)
	}
	if cardTitle != "直连连上后又断开了" {
		t.Fatalf("unexpected card title: %q", cardTitle)
	}
	if !strings.Contains(cardBody, "连上 4.2 秒后断开 · 探测连续 3 次无回应") {
		t.Fatalf("expected survival + probe timeout cause in card body, got %q", cardBody)
	}
	if len(dd.Checks) < 2 || renderTextZh(dict, dd.Checks[1].StatusText) != "已断开" || !strings.Contains(renderTextZh(dict, dd.Checks[1].DetailText), "连上 4.2 秒后断开 · 探测连续 3 次无回应") {
		t.Fatalf("unexpected path check: status=%q detail=%q", renderTextZh(dict, dd.Checks[1].StatusText), renderTextZh(dict, dd.Checks[1].DetailText))
	}

	var reportOut []string
	for _, line := range dd.ReportLines {
		reportOut = append(reportOut, renderTextZh(dict, line))
	}
	joined := strings.Join(reportOut, "\n")
	for _, want := range []string{
		"结论：直连连上后又断开了",
		"连上 4.2 秒后断开 · 探测连续 3 次无回应",
		"结局: 连上 4.2 秒后断开 · 探测连续 3 次无回应",
		"存活探测: 发出 5 · 回声 2 · 丢失 3",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("copied report missing %q:\n%s", want, joined)
		}
	}
	for _, forbidden := range []string{"结论：直连已连上", "已恢复直连", "192.168.1.20", "2001:db8:2::9", "54321"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("copied report contains forbidden string %q:\n%s", forbidden, joined)
		}
	}

	// Defensive check: even if a stale round has HasEstablished && End==EndNone while status.State==StateNone,
	// BuildDirectDiagnosticsView must never claim Connected or "已恢复直连" or "已连上".
	staleRound := []*transport.RoundData{{
		N:              2,
		StartedAt:      now - 5000,
		Established:    1000,
		HasEstablished: true,
		End:            transport.EndNone,
	}}
	ddStale := screens.BuildDirectDiagnosticsView("m-1", transport.Status{State: transport.StateNone, Reason: transport.ReasonFailed}, true, false, staleRound, 0, false, now, "Android")
	if ddStale.Connected || ddStale.TitleText.GetKey() == text.KeyTransportDiagConnectedTitle || ddStale.VerdictText.GetKey() == text.KeyTransportDiagVerdictConnected {
		t.Fatalf("stale round must never claim connected when status.State!=StateConnected: %+v", ddStale)
	}
	t.Logf("STATUS_LINE_RETRY: %s", statusLineRetry)
	t.Logf("STATUS_LINE_NO_RETRY: %s · %s", renderTextZh(dict, cgNoRetry.StatusText), renderTextZh(dict, cgNoRetry.DetailText))
	t.Logf("CARD_TITLE: %s", cardTitle)
	t.Logf("CARD_BODY: %s", cardBody)
	t.Logf("COPIED_REPORT:\n%s", joined)
}
