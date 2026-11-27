package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/latency"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/internal/test/testrelay"
	"github.com/Lewin671/remote-agent/clientcore/sync"
	"github.com/Lewin671/remote-agent/clientcore/text"
	stdcorev1 "github.com/Lewin671/remote-agent/gen/remoteagent/core/v1"
	"google.golang.org/protobuf/proto"
)

func viewSettingsVM(t *testing.T, c *Core) *corev1.SettingsViewModel {
	t.Helper()
	vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_Settings{Settings: &corev1.SettingsScreenRequest{}}})
	if err != nil {
		t.Fatalf("View(Settings): %v", err)
	}
	return vm.GetSettings()
}

func findMachineCard(t *testing.T, vm *corev1.SettingsViewModel, id string) *corev1.MachineListCardView {
	t.Helper()
	for _, m := range vm.GetMachines() {
		if m != nil && m.MachineId == id {
			return m
		}
	}
	t.Fatalf("machine %q not found in SettingsViewModel", id)
	return nil
}

func collectDispatchedCommands(batch *corev1.EffectBatch) []*corev1.DispatchCommandEffect {
	var out []*corev1.DispatchCommandEffect
	for _, ef := range batch.GetEffects() {
		if d := ef.GetDispatchCommand(); d != nil {
			out = append(out, d)
		}
	}
	return out
}

func findConnCheckTimer(batch *corev1.EffectBatch, prefix, machineID string) *corev1.ScheduleTimerEffect {
	for _, ef := range batch.GetEffects() {
		st := ef.GetScheduleTimer()
		if st != nil && st.Kind == corev1.TimerKind_TIMER_KIND_UI_TRANSIENT &&
			strings.HasPrefix(st.Key, prefix) && strings.HasSuffix(st.Key, ":"+machineID) {
			return st
		}
	}
	return nil
}

func decodeCommand(t *testing.T, raw []byte) *v1.Command {
	t.Helper()
	var cmd v1.Command
	if err := cmd.Unmarshal(raw); err != nil {
		t.Fatalf("unmarshal command: %v", err)
	}
	return &cmd
}

func probeResultOutcome(dc *corev1.DispatchCommandEffect, direct bool, elapsedMs float64, processUs, queueUs, executeUs int64) *corev1.CommandOutcomeEvent {
	return &corev1.CommandOutcomeEvent{
		RequestId:         dc.RequestId,
		AttemptId:         dc.AttemptId,
		Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
		Signed:            true,
		HashesMatch:       true,
		AttemptBound:      true,
		OuterMatchesInner: true,
		OuterOutcome:      "executed",
		OuterReason:       "completed",
		RouteWasDirect:    direct,
		ElapsedMs:         elapsedMs,
		RawResult: testrelay.MustRaw(&v1.CommandResult{
			Outcome:         "executed",
			Reason:          "completed",
			ResultAvailable: true,
			Kind: &v1.CommandResult_ProbeLatency{
				ProbeLatency: &v1.ProbeLatencyResponse{
					ProcessUs: processUs,
					QueueUs:   queueUs,
					ExecuteUs: executeUs,
				},
			},
		}),
	}
}

func probeInvalidBodyOutcome(dc *corev1.DispatchCommandEffect, direct bool) *corev1.CommandOutcomeEvent {
	return &corev1.CommandOutcomeEvent{
		RequestId:         dc.RequestId,
		AttemptId:         dc.AttemptId,
		Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
		Signed:            true,
		HashesMatch:       true,
		AttemptBound:      true,
		OuterMatchesInner: true,
		OuterOutcome:      "rejected",
		OuterReason:       "invalid_body",
		RouteWasDirect:    direct,
		RawResult: testrelay.MustRaw(&v1.CommandResult{
			ErrorCode: "invalid_argument",
			Outcome:   "rejected",
			Reason:    "invalid_body",
		}),
	}
}

func TestConnCheckRelayThreeEchoesMedianAndWork(t *testing.T) {
	c := bootstrapReadyApp(t, []*corev1.DemoMachine{
		testrelay.DemoMachine("m-relay", true, 0, &v1.MachineMeta{Hostname: "VPS-dev", ProtocolVersion: sync.PeerVersion}),
		testrelay.DemoMachine("m-off", false, 0, &v1.MachineMeta{Hostname: "Mac mini", ProtocolVersion: sync.PeerVersion}),
	}, nil)
	now := int64(1_791_078_061_000)

	// Mark server working, then start conncheck.
	c.Dispatch(&corev1.Event{
		NowMs: now,
		Event: &corev1.Event_ConnCheckStatus{
			ConnCheckStatus: &corev1.ConnCheckStatusEvent{
				ServerState: corev1.ConnProbeState_CONN_PROBE_STATE_WORKING,
				LatencyMs:   86,
			},
		},
	})
	startBatch := c.Dispatch(&corev1.Event{
		NowMs: now + 10,
		Event: &corev1.Event_ConnCheckStart{ConnCheckStart: &corev1.ConnCheckStartEvent{}},
	})
	vm := viewSettingsVM(t, c)
	if !vm.GetConnCheck().GetMachinesBusy() {
		t.Fatalf("expected machines_busy=true while measuring")
	}
	if findMachineCard(t, vm, "m-off").GetLatency() != nil {
		t.Fatalf("expected offline machine to have nil latency")
	}
	card := findMachineCard(t, vm, "m-relay")
	if card.GetLatency().GetState() != corev1.MachineLatencyState_MACHINE_LATENCY_STATE_MEASURING {
		t.Fatalf("expected MEASURING state, got %+v", card.GetLatency())
	}

	// Drive 3 sequential echoes: wire-daemon = 310-10=300, 290-8=282, 350-5=345 -> median 300ms.
	dcs := collectDispatchedCommands(startBatch)
	if len(dcs) != 1 || !dcs[0].ForceRelay {
		t.Fatalf("expected 1 force-relay echo dispatch, got %+v", dcs)
	}
	echoInputs := []struct {
		elapsedMs float64
		procUs    int64
	}{
		{310, 10_000}, // 300ms
		{290, 8_000},  // 282ms
		{350, 5_000},  // 345ms
	}
	var workDC *corev1.DispatchCommandEffect
	curNow := now + 10
	for i, ei := range echoInputs {
		cmd := decodeCommand(t, dcs[0].CommandBytes)
		if cmd.GetProbeLatency() == nil || cmd.GetProbeLatency().GetWork() {
			t.Fatalf("echo #%d expected probe_latency(work=false), got %+v", i, cmd)
		}
		curNow += int64(ei.elapsedMs)
		b := c.Dispatch(&corev1.Event{
			NowMs: curNow,
			Event: &corev1.Event_CommandOutcome{
				CommandOutcome: probeResultOutcome(dcs[0], false, ei.elapsedMs, ei.procUs, 1_000, 0),
			},
		})
		dcs = collectDispatchedCommands(b)
		if i < 2 {
			if len(dcs) != 1 {
				t.Fatalf("after echo #%d expected next echo dispatch, got %d", i, len(dcs))
			}
		} else {
			if len(dcs) != 1 {
				t.Fatalf("after 3rd echo expected work dispatch, got %d", len(dcs))
			}
			workDC = dcs[0]
		}
	}

	// Verify intermediate MEASURING state now has network = 300 ms and computer = —.
	vm = viewSettingsVM(t, c)
	card = findMachineCard(t, vm, "m-relay")
	if card.GetLatency().GetState() != corev1.MachineLatencyState_MACHINE_LATENCY_STATE_MEASURING ||
		card.GetLatency().GetSegments()[0].GetValueText().GetArgs()[0].GetStrValue() != "300" ||
		card.GetLatency().GetSegments()[1].GetValueText().GetKey() != text.KeyConnectionLatencyPending {
		t.Fatalf("unexpected intermediate measuring view: %+v", card.GetLatency())
	}

	workCmd := decodeCommand(t, workDC.CommandBytes)
	if workCmd.GetProbeLatency() == nil || !workCmd.GetProbeLatency().GetWork() || !workDC.ForceRelay {
		t.Fatalf("expected force-relay probe_latency(work=true), got %+v force=%v", workCmd, workDC.ForceRelay)
	}

	// Work probe finishes in 2140ms with computer 1790ms (execute 1770ms > queue 10ms).
	curNow += 2140
	c.Dispatch(&corev1.Event{
		NowMs: curNow,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: probeResultOutcome(workDC, false, 2090, 1_790_000, 10_000, 1_770_000),
		},
	})
	vm = viewSettingsVM(t, c)
	if vm.GetConnCheck().GetMachinesBusy() {
		t.Fatalf("expected machines_busy=false after completion")
	}
	lat := findMachineCard(t, vm, "m-relay").GetLatency()
	if lat.State != corev1.MachineLatencyState_MACHINE_LATENCY_STATE_DONE ||
		!lat.Slow ||
		lat.GetValueText().GetKey() != text.KeyConnectionLatencyMsSlow ||
		lat.GetValueText().GetArgs()[0].GetStrValue() != "2,140" ||
		len(lat.Segments) != 2 ||
		lat.Segments[0].Slow ||
		lat.Segments[0].GetValueText().GetArgs()[0].GetStrValue() != "300" ||
		!lat.Segments[1].Slow ||
		lat.Segments[1].GetValueText().GetArgs()[0].GetStrValue() != "1,790" ||
		lat.Segments[1].GetDetailText().GetKey() != text.KeyConnectionLatencyMostlyRunning ||
		lat.Segments[1].GetDetailText().GetArgs()[0].GetStrValue() != "1,770" {
		t.Fatalf("unexpected DONE relay view: %+v", lat)
	}
}

func TestConnCheckDirectNormalSlowQueueAndRerouted(t *testing.T) {
	c := bootstrapReadyApp(t, []*corev1.DemoMachine{
		testrelay.DemoMachine("m-direct", true, 0, &v1.MachineMeta{Hostname: "MacBook", ProtocolVersion: sync.PeerVersion}),
		testrelay.DemoMachine("m-reroute", true, 0, &v1.MachineMeta{Hostname: "Studio", ProtocolVersion: sync.PeerVersion}),
	}, nil)
	now := int64(1_791_078_061_000)

	// Seed direct reports with active RTTs (demo/golden path).
	c.Dispatch(&corev1.Event{
		NowMs: now,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				DirectReports: []*corev1.DemoDirectReport{
					{
						MachineId: "m-direct",
						Enabled:   true,
						Report: &corev1.DirectRouteReport{
							Enabled:        true,
							Capable:        true,
							State:          corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED,
							ReadRoute:      corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT,
							HasActiveRttMs: true,
							ActiveRttMs:    8,
						},
					},
					{
						MachineId: "m-reroute",
						Enabled:   true,
						Report: &corev1.DirectRouteReport{
							Enabled:        true,
							Capable:        true,
							State:          corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED,
							ReadRoute:      corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT,
							HasActiveRttMs: true,
							ActiveRttMs:    7,
						},
					},
				},
			},
		},
	})

	startBatch := c.Dispatch(&corev1.Event{
		NowMs: now + 10,
		Event: &corev1.Event_ConnCheckStart{ConnCheckStart: &corev1.ConnCheckStartEvent{}},
	})
	dcs := collectDispatchedCommands(startBatch)
	if len(dcs) != 2 {
		t.Fatalf("expected 2 immediate work probes for direct machines, got %d", len(dcs))
	}
	byMachine := map[string]*corev1.DispatchCommandEffect{}
	for _, dc := range dcs {
		if dc.ForceRelay {
			t.Fatalf("direct work probe for %s must not force relay", dc.MachineId)
		}
		cmd := decodeCommand(t, dc.CommandBytes)
		if cmd.GetProbeLatency() == nil || !cmd.GetProbeLatency().GetWork() {
			t.Fatalf("direct machine %s must skip echo and send work probe directly", dc.MachineId)
		}
		byMachine[dc.MachineId] = dc
	}

	// m-direct completes over direct: total 1250ms, network 8ms, computer 1190ms (queued 1150ms).
	c.Dispatch(&corev1.Event{
		NowMs: now + 10 + 1250,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: probeResultOutcome(byMachine["m-direct"], true, 1210, 1_190_000, 1_150_000, 25_000),
		},
	})
	// m-reroute started direct, settled over relay after 3420ms total with winning relay attempt elapsed 374ms and computer 64ms.
	c.Dispatch(&corev1.Event{
		NowMs: now + 10 + 3420,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: probeResultOutcome(byMachine["m-reroute"], false, 374, 64_000, 10_000, 50_000),
		},
	})

	vm := viewSettingsVM(t, c)
	latDirect := findMachineCard(t, vm, "m-direct").GetLatency()
	if !latDirect.Slow ||
		latDirect.Segments[0].Slow ||
		latDirect.Segments[0].GetValueText().GetArgs()[0].GetStrValue() != "8" ||
		!latDirect.Segments[1].Slow ||
		latDirect.Segments[1].GetDetailText().GetKey() != text.KeyConnectionLatencyMostlyQueued ||
		latDirect.Segments[1].GetDetailText().GetArgs()[0].GetStrValue() != "1,150" {
		t.Fatalf("unexpected direct slow-queued view: %+v", latDirect)
	}

	latReroute := findMachineCard(t, vm, "m-reroute").GetLatency()
	if !latReroute.Slow ||
		latReroute.GetSummaryOverrideText().GetKey() != text.KeyConnectionLatencyRerouted ||
		len(latReroute.Segments) != 3 ||
		latReroute.Segments[0].Slow ||
		latReroute.Segments[0].GetValueText().GetArgs()[0].GetStrValue() != "310" ||
		latReroute.Segments[1].Slow ||
		latReroute.Segments[1].GetValueText().GetArgs()[0].GetStrValue() != "64" ||
		!latReroute.Segments[2].Slow ||
		latReroute.Segments[2].GetLabelText().GetKey() != text.KeyConnectionLatencyWaitDirect ||
		latReroute.Segments[2].GetValueText().GetArgs()[0].GetStrValue() != "3,046" {
		t.Fatalf("unexpected rerouted view: %+v", latReroute)
	}
}

func TestConnCheckUnsupportedOutdatedDirectAndRelay(t *testing.T) {
	c := bootstrapReadyApp(t, []*corev1.DemoMachine{
		testrelay.DemoMachine("m-direct", true, 0, &v1.MachineMeta{Hostname: "MacBook", ProtocolVersion: sync.PeerVersion}),
		testrelay.DemoMachine("m-relay", true, 0, &v1.MachineMeta{Hostname: "Lab", ProtocolVersion: sync.PeerVersion}),
	}, nil)
	now := int64(1_791_078_061_000)

	c.Dispatch(&corev1.Event{
		NowMs: now,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				DirectReports: []*corev1.DemoDirectReport{
					{
						MachineId: "m-direct",
						Enabled:   true,
						Report: &corev1.DirectRouteReport{
							Enabled:        true,
							Capable:        true,
							State:          corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED,
							ReadRoute:      corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT,
							HasActiveRttMs: true,
							ActiveRttMs:    6,
						},
					},
				},
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: now + 5,
		Event: &corev1.Event_ConnCheckStatus{
			ConnCheckStatus: &corev1.ConnCheckStatusEvent{ServerState: corev1.ConnProbeState_CONN_PROBE_STATE_WORKING, LatencyMs: 50},
		},
	})
	startBatch := c.Dispatch(&corev1.Event{
		NowMs: now + 10,
		Event: &corev1.Event_ConnCheckStart{ConnCheckStart: &corev1.ConnCheckStartEvent{}},
	})
	dcs := collectDispatchedCommands(startBatch)
	if len(dcs) != 2 {
		t.Fatalf("expected 2 probes, got %d", len(dcs))
	}
	for _, dc := range dcs {
		b := c.Dispatch(&corev1.Event{
			NowMs: now + 40,
			Event: &corev1.Event_CommandOutcome{
				CommandOutcome: probeInvalidBodyOutcome(dc, dc.MachineId == "m-direct"),
			},
		})
		if followUps := collectDispatchedCommands(b); len(followUps) != 0 {
			t.Fatalf("unsupported machine %s must not dispatch follow-up probes, got %+v", dc.MachineId, followUps)
		}
	}

	vm := viewSettingsVM(t, c)
	latDirect := findMachineCard(t, vm, "m-direct").GetLatency()
	if latDirect.State != corev1.MachineLatencyState_MACHINE_LATENCY_STATE_UNSUPPORTED ||
		latDirect.GetValueText().GetKey() != text.KeyConnectionLatencyOutdated ||
		len(latDirect.Segments) != 1 ||
		latDirect.Segments[0].GetValueText().GetArgs()[0].GetStrValue() != "6" ||
		latDirect.GetNoteText().GetKey() != text.KeyConnectionLatencyOutdatedComputer {
		t.Fatalf("unexpected direct UNSUPPORTED view: %+v", latDirect)
	}
	latRelay := findMachineCard(t, vm, "m-relay").GetLatency()
	if latRelay.State != corev1.MachineLatencyState_MACHINE_LATENCY_STATE_UNSUPPORTED ||
		latRelay.GetValueText().GetKey() != text.KeyConnectionLatencyOutdated ||
		len(latRelay.Segments) != 0 ||
		latRelay.GetNoteText().GetKey() != text.KeyConnectionLatencyOutdatedAll {
		t.Fatalf("unexpected relay UNSUPPORTED view: %+v", latRelay)
	}
}

func TestConnCheckTimeoutsServerUnavailableAndStopCancel(t *testing.T) {
	t.Run("echo_5s_timeout_and_work_15s_timeout", func(t *testing.T) {
		c := bootstrapReadyApp(t, []*corev1.DemoMachine{
			testrelay.DemoMachine("m-echo-to", true, 0, &v1.MachineMeta{Hostname: "Studio", ProtocolVersion: sync.PeerVersion}),
			testrelay.DemoMachine("m-work-to", true, 0, &v1.MachineMeta{Hostname: "VPS-dev", ProtocolVersion: sync.PeerVersion}),
		}, nil)
		now := int64(1_791_078_061_000)

		c.Dispatch(&corev1.Event{
			NowMs: now,
			Event: &corev1.Event_SeedDemoWorld{
				SeedDemoWorld: &corev1.SeedDemoWorldEvent{
					DirectReports: []*corev1.DemoDirectReport{
						{
							MachineId: "m-work-to",
							Enabled:   true,
							Report: &corev1.DirectRouteReport{
								Enabled:        true,
								Capable:        true,
								State:          corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED,
								ReadRoute:      corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT,
								HasActiveRttMs: true,
								ActiveRttMs:    520, // slow direct network preserved on work timeout
							},
						},
					},
				},
			},
		})
		c.Dispatch(&corev1.Event{
			NowMs: now + 5,
			Event: &corev1.Event_ConnCheckStatus{
				ConnCheckStatus: &corev1.ConnCheckStatusEvent{ServerState: corev1.ConnProbeState_CONN_PROBE_STATE_WORKING, LatencyMs: 40},
			},
		})
		startBatch := c.Dispatch(&corev1.Event{
			NowMs: now + 10,
			Event: &corev1.Event_ConnCheckStart{ConnCheckStart: &corev1.ConnCheckStartEvent{}},
		})
		echoTimer := findConnCheckTimer(startBatch, connCheckEchoTimerPrefix, "m-echo-to")
		workTimer := findConnCheckTimer(startBatch, connCheckWorkTimerPrefix, "m-work-to")
		if echoTimer == nil || echoTimer.DelayMs != latency.EchoTimeoutMs {
			t.Fatalf("expected 5s echo timer, got %+v", echoTimer)
		}
		if workTimer == nil || workTimer.DelayMs != latency.WorkTimeoutMs {
			t.Fatalf("expected 15s work timer, got %+v", workTimer)
		}

		// Fire echo 5s timeout -> cancels echo, marks FAILED with Over 5 s stuck.
		echoFired := c.Dispatch(&corev1.Event{
			NowMs: now + 10 + latency.EchoTimeoutMs,
			Event: &corev1.Event_TimerFired{
				TimerFired: &corev1.TimerFiredEvent{
					TimerId: echoTimer.TimerId,
					Kind:    echoTimer.Kind,
					Key:     echoTimer.Key,
				},
			},
		})
		var canceledEcho bool
		for _, ef := range echoFired.GetEffects() {
			if ef.GetCancelDispatch() != nil {
				canceledEcho = true
			}
		}
		if !canceledEcho {
			t.Fatalf("expected CancelDispatchEffect on echo timeout")
		}

		// Fire work 15s timeout -> cancels work, marks FAILED with network slow + Over 15 s stuck.
		workFired := c.Dispatch(&corev1.Event{
			NowMs: now + 10 + latency.WorkTimeoutMs,
			Event: &corev1.Event_TimerFired{
				TimerFired: &corev1.TimerFiredEvent{
					TimerId: workTimer.TimerId,
					Kind:    workTimer.Kind,
					Key:     workTimer.Key,
				},
			},
		})
		var canceledWork bool
		for _, ef := range workFired.GetEffects() {
			if ef.GetCancelDispatch() != nil {
				canceledWork = true
			}
		}
		if !canceledWork {
			t.Fatalf("expected CancelDispatchEffect on work timeout")
		}

		vm := viewSettingsVM(t, c)
		latEcho := findMachineCard(t, vm, "m-echo-to").GetLatency()
		if latEcho.State != corev1.MachineLatencyState_MACHINE_LATENCY_STATE_FAILED ||
			latEcho.GetValueText().GetKey() != text.KeyConnectionLatencyNoReply ||
			!latEcho.Segments[0].Stuck || latEcho.Segments[0].Slow ||
			latEcho.Segments[0].GetValueText().GetArgs()[0].GetIntValue() != 5 ||
			latEcho.Segments[1].GetValueText().GetKey() != text.KeyConnectionNotChecked {
			t.Fatalf("unexpected echo timeout view: %+v", latEcho)
		}

		latWork := findMachineCard(t, vm, "m-work-to").GetLatency()
		if latWork.State != corev1.MachineLatencyState_MACHINE_LATENCY_STATE_FAILED ||
			latWork.GetValueText().GetKey() != text.KeyConnectionLatencyComputerNoReply ||
			!latWork.Segments[0].Slow ||
			!latWork.Segments[1].Stuck || latWork.Segments[1].Slow ||
			latWork.Segments[1].GetValueText().GetArgs()[0].GetIntValue() != 15 {
			t.Fatalf("unexpected work timeout view: %+v", latWork)
		}
	})

	t.Run("server_unavailable_skips_relay_keeps_direct", func(t *testing.T) {
		c := bootstrapReadyApp(t, []*corev1.DemoMachine{
			testrelay.DemoMachine("m-direct", true, 0, &v1.MachineMeta{Hostname: "MacBook", ProtocolVersion: sync.PeerVersion}),
			testrelay.DemoMachine("m-relay", true, 0, &v1.MachineMeta{Hostname: "VPS-dev", ProtocolVersion: sync.PeerVersion}),
		}, nil)
		now := int64(1_791_078_061_000)

		c.Dispatch(&corev1.Event{
			NowMs: now,
			Event: &corev1.Event_SeedDemoWorld{
				SeedDemoWorld: &corev1.SeedDemoWorldEvent{
					DirectReports: []*corev1.DemoDirectReport{
						{
							MachineId: "m-direct",
							Enabled:   true,
							Report: &corev1.DirectRouteReport{
								Enabled:        true,
								Capable:        true,
								State:          corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED,
								ReadRoute:      corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT,
								HasActiveRttMs: true,
								ActiveRttMs:    6,
							},
						},
					},
				},
			},
		})
		// Start while server is CHECKING -> direct starts immediately, relay waits for server status.
		c.Dispatch(&corev1.Event{
			NowMs: now + 5,
			Event: &corev1.Event_ConnCheckStatus{
				ConnCheckStatus: &corev1.ConnCheckStatusEvent{ServerState: corev1.ConnProbeState_CONN_PROBE_STATE_CHECKING},
			},
		})
		startBatch := c.Dispatch(&corev1.Event{
			NowMs: now + 10,
			Event: &corev1.Event_ConnCheckStart{ConnCheckStart: &corev1.ConnCheckStartEvent{}},
		})
		dcs := collectDispatchedCommands(startBatch)
		if len(dcs) != 1 || dcs[0].MachineId != "m-direct" {
			t.Fatalf("expected only direct machine dispatched while server checking, got %+v", dcs)
		}

		// Server reports UNAVAILABLE -> relay machine transitions to NOT_CHECKED, direct unaffected.
		c.Dispatch(&corev1.Event{
			NowMs: now + 100,
			Event: &corev1.Event_ConnCheckStatus{
				ConnCheckStatus: &corev1.ConnCheckStatusEvent{ServerState: corev1.ConnProbeState_CONN_PROBE_STATE_UNAVAILABLE},
			},
		})
		vm := viewSettingsVM(t, c)
		if got := findMachineCard(t, vm, "m-relay").GetLatency().GetState(); got != corev1.MachineLatencyState_MACHINE_LATENCY_STATE_NOT_CHECKED {
			t.Fatalf("expected relay machine NOT_CHECKED when server unavailable, got %v", got)
		}
		if got := findMachineCard(t, vm, "m-direct").GetLatency().GetState(); got != corev1.MachineLatencyState_MACHINE_LATENCY_STATE_MEASURING {
			t.Fatalf("expected direct machine still MEASURING when server unavailable, got %v", got)
		}
	})

	t.Run("stop_and_logout_cancel_inflight_and_ignore_late_outcomes", func(t *testing.T) {
		c := bootstrapReadyApp(t, []*corev1.DemoMachine{
			testrelay.DemoMachine("m-relay", true, 0, &v1.MachineMeta{Hostname: "VPS-dev", ProtocolVersion: sync.PeerVersion}),
		}, nil)
		now := int64(1_791_078_061_000)

		c.Dispatch(&corev1.Event{
			NowMs: now,
			Event: &corev1.Event_ConnCheckStatus{
				ConnCheckStatus: &corev1.ConnCheckStatusEvent{ServerState: corev1.ConnProbeState_CONN_PROBE_STATE_WORKING, LatencyMs: 50},
			},
		})
		startBatch := c.Dispatch(&corev1.Event{
			NowMs: now + 10,
			Event: &corev1.Event_ConnCheckStart{ConnCheckStart: &corev1.ConnCheckStartEvent{}},
		})
		dcs := collectDispatchedCommands(startBatch)
		if len(dcs) != 1 {
			t.Fatalf("expected 1 echo command, got %d", len(dcs))
		}

		stopBatch := c.Dispatch(&corev1.Event{
			NowMs: now + 20,
			Event: &corev1.Event_ConnCheckStop{ConnCheckStop: &corev1.ConnCheckStopEvent{}},
		})
		var sawCancel bool
		for _, ef := range stopBatch.GetEffects() {
			if ef.GetCancelDispatch() != nil && ef.GetCancelDispatch().RequestId == dcs[0].RequestId {
				sawCancel = true
			}
		}
		if !sawCancel {
			t.Fatalf("expected ConnCheckStop to cancel in-flight command")
		}

		// Late outcome after stop must not dispatch follow-up or flip state to DONE.
		lateBatch := c.Dispatch(&corev1.Event{
			NowMs: now + 50,
			Event: &corev1.Event_CommandOutcome{
				CommandOutcome: probeResultOutcome(dcs[0], false, 30, 5_000, 1_000, 0),
			},
		})
		if len(collectDispatchedCommands(lateBatch)) != 0 {
			t.Fatalf("late outcome after ConnCheckStop must not dispatch follow-up")
		}
		vm := viewSettingsVM(t, c)
		if vm.GetConnCheck().GetMachinesBusy() {
			t.Fatalf("expected machines_busy=false after ConnCheckStop")
		}
		if got := findMachineCard(t, vm, "m-relay").GetLatency().GetState(); got != corev1.MachineLatencyState_MACHINE_LATENCY_STATE_NOT_CHECKED {
			t.Fatalf("expected NOT_CHECKED after stop, got %v", got)
		}

		// Restart then external logout -> cancels cleanly.
		startBatch2 := c.Dispatch(&corev1.Event{
			NowMs: now + 60,
			Event: &corev1.Event_ConnCheckStart{ConnCheckStart: &corev1.ConnCheckStartEvent{}},
		})
		dcs2 := collectDispatchedCommands(startBatch2)
		if len(dcs2) != 1 {
			t.Fatalf("expected 1 echo command on restart, got %d", len(dcs2))
		}
		logoutBatch := c.Dispatch(&corev1.Event{
			NowMs: now + 70,
			Event: &corev1.Event_ExternalLogout{ExternalLogout: &corev1.ExternalLogoutEvent{}},
		})
		var sawLogoutCancel bool
		for _, ef := range logoutBatch.GetEffects() {
			if ef.GetCancelDispatch() != nil && ef.GetCancelDispatch().RequestId == dcs2[0].RequestId {
				sawLogoutCancel = true
			}
		}
		if !sawLogoutCancel {
			t.Fatalf("expected ExternalLogout to cancel in-flight conncheck command")
		}
	})

	t.Run("direct_zero_samples_waits_up_to_3s_then_runs_work", func(t *testing.T) {
		c := bootstrapReadyApp(t, []*corev1.DemoMachine{
			testrelay.DemoMachine("m-direct-nosamp", true, 0, &v1.MachineMeta{Hostname: "MacBook", ProtocolVersion: sync.PeerVersion}),
		}, nil)
		now := int64(1_791_078_061_000)

		c.Dispatch(&corev1.Event{
			NowMs: now,
			Event: &corev1.Event_SeedDemoWorld{
				SeedDemoWorld: &corev1.SeedDemoWorldEvent{
					DirectReports: []*corev1.DemoDirectReport{
						{
							MachineId: "m-direct-nosamp",
							Enabled:   true,
							Report: &corev1.DirectRouteReport{
								Enabled:   true,
								Capable:   true,
								State:     corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED,
								ReadRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT,
							},
						},
					},
				},
			},
		})
		startBatch := c.Dispatch(&corev1.Event{
			NowMs: now + 10,
			Event: &corev1.Event_ConnCheckStart{ConnCheckStart: &corev1.ConnCheckStartEvent{}},
		})
		if len(collectDispatchedCommands(startBatch)) != 0 {
			t.Fatalf("expected wait for direct probe sample before dispatching work")
		}
		sampleTimer := findConnCheckTimer(startBatch, connCheckSampleTimerPrefix, "m-direct-nosamp")
		if sampleTimer == nil || sampleTimer.DelayMs != latency.DirectSampleWaitMs {
			t.Fatalf("expected 3s sample wait timer, got %+v", sampleTimer)
		}
		// Fire 3s timer -> dispatches work probe even without probe sample.
		fired := c.Dispatch(&corev1.Event{
			NowMs: now + 10 + latency.DirectSampleWaitMs,
			Event: &corev1.Event_TimerFired{
				TimerFired: &corev1.TimerFiredEvent{
					TimerId: sampleTimer.TimerId,
					Kind:    sampleTimer.Kind,
					Key:     sampleTimer.Key,
				},
			},
		})
		dcs := collectDispatchedCommands(fired)
		if len(dcs) != 1 {
			t.Fatalf("expected work probe dispatched after 3s sample wait, got %d", len(dcs))
		}
		c.Dispatch(&corev1.Event{
			NowMs: now + 10 + latency.DirectSampleWaitMs + 42,
			Event: &corev1.Event_CommandOutcome{
				CommandOutcome: probeResultOutcome(dcs[0], true, 30, 12_000, 1_000, 10_000),
			},
		})
		vm := viewSettingsVM(t, c)
		lat := findMachineCard(t, vm, "m-direct-nosamp").GetLatency()
		if lat.State != corev1.MachineLatencyState_MACHINE_LATENCY_STATE_DONE || lat.Slow || len(lat.Segments) != 2 {
			t.Fatalf("unexpected direct no-sample fallback view: %+v", lat)
		}
	})
}

func TestSettingsOverviewGoldenConnCheckBreakdown(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "screens", "settings_overview.golden.pb"))
	if err != nil {
		t.Fatalf("read settings_overview.golden.pb: %v", err)
	}
	var bundle stdcorev1.ScreenGoldenFile
	if err := proto.Unmarshal(raw, &bundle); err != nil {
		t.Fatalf("unmarshal golden bundle: %v", err)
	}
	var breakdown *stdcorev1.SettingsViewModel
	for _, step := range bundle.Steps {
		for _, v := range step.Views {
			if v.Name == "connCheckBreakdown" {
				breakdown = v.GetViewModel().GetSettings()
				break
			}
		}
	}
	if breakdown == nil {
		t.Fatal("connCheckBreakdown snapshot missing from settings_overview.golden.pb")
	}
	if breakdown.GetConnCheck().GetMachinesBusy() {
		t.Fatalf("expected connCheckBreakdown machines_busy=false, got true")
	}
	byID := map[string]*stdcorev1.MachineListCardView{}
	for _, m := range breakdown.GetMachines() {
		byID[m.MachineId] = m
	}
	if lat := byID["m-cc-1-ok"].GetLatency(); lat.GetState() != stdcorev1.MachineLatencyState_MACHINE_LATENCY_STATE_DONE || lat.GetSlow() {
		t.Fatalf("m-cc-1-ok expected normal DONE, got %+v", lat)
	}
	if lat := byID["m-cc-2-slow"].GetLatency(); lat.GetState() != stdcorev1.MachineLatencyState_MACHINE_LATENCY_STATE_DONE || !lat.GetSlow() || len(lat.GetSegments()) != 2 || lat.GetSegments()[1].GetDetailText().GetKey() != text.KeyConnectionLatencyMostlyQueued {
		t.Fatalf("m-cc-2-slow expected slow computer queued, got %+v", lat)
	}
	if lat := byID["m-cc-3-reroute"].GetLatency(); lat.GetState() != stdcorev1.MachineLatencyState_MACHINE_LATENCY_STATE_DONE || !lat.GetSlow() || len(lat.GetSegments()) != 3 || lat.GetSummaryOverrideText().GetKey() != text.KeyConnectionLatencyRerouted {
		t.Fatalf("m-cc-3-reroute expected rerouted with 3 segments, got %+v", lat)
	}
	if lat := byID["m-cc-4-outdated"].GetLatency(); lat.GetState() != stdcorev1.MachineLatencyState_MACHINE_LATENCY_STATE_UNSUPPORTED || lat.GetNoteText().GetKey() != text.KeyConnectionLatencyOutdatedAll {
		t.Fatalf("m-cc-4-outdated expected UNSUPPORTED, got %+v", lat)
	}
	if lat := byID["m-cc-5-timeout"].GetLatency(); lat.GetState() != stdcorev1.MachineLatencyState_MACHINE_LATENCY_STATE_FAILED || len(lat.GetSegments()) != 2 || !lat.GetSegments()[1].GetStuck() {
		t.Fatalf("m-cc-5-timeout expected FAILED computer timeout, got %+v", lat)
	}
}
