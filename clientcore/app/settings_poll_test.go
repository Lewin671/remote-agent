package app

import (
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/internal/test/testrelay"
)

func TestMachineStatusWatchStartsPollOnceMachineBecomesOnline(t *testing.T) {
	c := New()
	defer c.Close()

	c.Dispatch(&corev1.Event{
		NowMs: 1000,
		Event: &corev1.Event_EntropyProvided{
			EntropyProvided: &corev1.EntropyProvided{
				RandomIds:  []string{"id-1", "id-2", "id-3", "id-4", "id-5", "id-6", "id-7", "id-8", "id-9", "id-10"},
				UnitFloats: []float64{0.5, 0.5, 0.5, 0.5},
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1000,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					IsMember:          true,
					SelfDeviceId:      "dev-1",
					AuthorityDeviceId: "dev-1",
					LeaseEndMs:        99999999,
					TrustedMachineIds: []string{"m-1"},
				},
			},
		},
	})
	startBatch := c.Dispatch(&corev1.Event{
		NowMs: 1000,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
		},
	})
	legID := testrelay.FindOpenStreamLeg(startBatch.Effects)

	// Register an active watch before StreamReady / lists populate the machine.
	initial := c.Dispatch(&corev1.Event{
		NowMs: 1005,
		Event: &corev1.Event_MachineStatusWatch{
			MachineStatusWatch: &corev1.MachineStatusWatchEvent{
				MachineId: "m-1",
				Visible:   true,
				Mode:      corev1.MachinePollMode_MACHINE_POLL_MODE_OVERVIEW,
			},
		},
	})
	for _, eff := range initial.Effects {
		if dc := eff.GetDispatchCommand(); dc != nil {
			t.Fatalf("expected no command dispatched while machine missing, got %v", dc)
		}
	}

	c.Dispatch(&corev1.Event{
		NowMs: 1030,
		Event: &corev1.Event_StreamReady{
			StreamReady: &corev1.StreamReadyEvent{LegId: legID, ServerTimeMs: 1030},
		},
	})

	batch := c.Dispatch(&corev1.Event{
		NowMs: 1040,
		Event: testrelay.DemoMachineEvent(
			testrelay.DemoMachine("m-1", true, 0, &v1.MachineMeta{
				Hostname:        "MacBook",
				ProtocolVersion: 17,
				Capabilities:    &v1.MachineCapabilities{MachineStatus: true},
			}),
		),
	})

	var dispatched bool
	for _, eff := range batch.Effects {
		if dc := eff.GetDispatchCommand(); dc != nil && dc.MachineId == "m-1" {
			var cmd v1.Command
			if err := cmd.Unmarshal(dc.CommandBytes); err == nil && cmd.GetGetMachineStatus() != nil {
				dispatched = true
			}
		}
	}
	if !dispatched {
		t.Fatal("expected ListsFetched to start GetMachineStatus poll for pre-registered active watch")
	}
}

func TestMachineStatusWatchBeforeConnectedStartsPollOnStreamReady(t *testing.T) {
	c := New()
	defer c.Close()

	c.Dispatch(&corev1.Event{
		NowMs: 1000,
		Event: &corev1.Event_EntropyProvided{
			EntropyProvided: &corev1.EntropyProvided{
				RandomIds:  []string{"id-1", "id-2", "id-3", "id-4", "id-5", "id-6", "id-7", "id-8", "id-9", "id-10"},
				UnitFloats: []float64{0.5, 0.5, 0.5, 0.5},
			},
		},
	})
	c.Dispatch(&corev1.Event{
		NowMs: 1000,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					IsMember:          true,
					SelfDeviceId:      "dev-1",
					AuthorityDeviceId: "dev-1",
					LeaseEndMs:        99999999,
					TrustedMachineIds: []string{"m-1"},
				},
				Machines: []*corev1.DemoMachine{
					{
						MachineId: "m-1",
						Online:    true,
						RawMeta: testrelay.MustRaw(&v1.MachineMeta{
							Hostname:        "MacBook",
							ProtocolVersion: 17,
							Capabilities:    &v1.MachineCapabilities{MachineStatus: true},
						}),
					},
				},
			},
		},
	})
	startBatch := c.Dispatch(&corev1.Event{
		NowMs: 1000,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
		},
	})
	legID := testrelay.FindOpenStreamLeg(startBatch.Effects)

	netBatch := c.Dispatch(&corev1.Event{
		NowMs: 1001,
		Event: &corev1.Event_NetworkChanged{
			NetworkChanged: &corev1.NetworkChangedEvent{Signature: "wifi", Available: true},
		},
	})
	if nextLeg := testrelay.FindOpenStreamLeg(netBatch.Effects); nextLeg != 0 {
		legID = nextLeg
	}

	// Register active watch while cached machine looks online, before StreamReady connects.
	watchBatch := c.Dispatch(&corev1.Event{
		NowMs: 1005,
		Event: &corev1.Event_MachineStatusWatch{
			MachineStatusWatch: &corev1.MachineStatusWatchEvent{
				MachineId: "m-1",
				Visible:   true,
				Mode:      corev1.MachinePollMode_MACHINE_POLL_MODE_OVERVIEW,
			},
		},
	})
	for _, eff := range watchBatch.Effects {
		if dc := eff.GetDispatchCommand(); dc != nil {
			t.Fatalf("expected no poll command while still connecting, got %v", dc)
		}
	}

	readyBatch := c.Dispatch(&corev1.Event{
		NowMs: 1030,
		Event: &corev1.Event_StreamReady{
			StreamReady: &corev1.StreamReadyEvent{LegId: legID, ServerTimeMs: 1030},
		},
	})

	var dispatched bool
	for _, eff := range readyBatch.Effects {
		if dc := eff.GetDispatchCommand(); dc != nil && dc.MachineId == "m-1" {
			var cmd v1.Command
			if err := cmd.Unmarshal(dc.CommandBytes); err == nil && cmd.GetGetMachineStatus() != nil {
				dispatched = true
			}
		}
	}
	if !dispatched {
		t.Fatal("expected StreamReady to start GetMachineStatus poll for pre-registered cached-online watch")
	}
}
