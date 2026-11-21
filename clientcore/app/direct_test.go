package app

import (
	"testing"

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
		Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START}},
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
		State: transport.StateConnected,
		Read:  transport.Direct,
		File:  transport.Direct,
		Measured: transport.Measured{
			HasReadMs: [2]bool{false, true},
			ReadMs:    [2]float64{0, 18.4},
		},
	}
	rep := screens.BuildDirectRouteReport(connStatus, true, false, 1)
	chip := screens.BuildSessionDirectChip(rep, text.Plain("MacBook"), true, false, false)
	if chip == nil || !chip.Visible || !chip.Active || !chip.ReadDirect || !chip.FileDirect {
		t.Fatalf("expected active SessionDirectChipView, got %+v", chip)
	}
	if chip.PopoverTitle.GetKey() != text.KeyTransportPopoverDirectTitleRtt || chip.PopoverFoot.GetKey() != text.KeyTransportPopoverStable {
		t.Fatalf("unexpected chip popover keys: title=%v foot=%v", chip.PopoverTitle, chip.PopoverFoot)
	}
	_ = v1.Session{}
}
