package corebind

import (
	"testing"

	stdcorev1 "github.com/Lewin671/remote-agent/gen/remoteagent/core/v1"
	stdv1 "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

func dispatchEvent(t *testing.T, c *Core, ev *stdcorev1.Event) *stdcorev1.EffectBatch {
	t.Helper()
	b, err := proto.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	effBytes, err := c.Dispatch(b)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var batch stdcorev1.EffectBatch
	if err := proto.Unmarshal(effBytes, &batch); err != nil {
		t.Fatalf("unmarshal effects: %v", err)
	}
	return &batch
}

func TestCoreRoundTrip(t *testing.T) {
	c := New()
	defer c.Close()

	metaBytes, err := proto.Marshal(&stdv1.MachineMeta{
		Hostname:        "DevBook.local",
		ProtocolVersion: 17,
		Agents: []*stdv1.AgentInfo{
			{Agent: stdv1.Agent_AGENT_CODEX, DefaultMode: stdv1.ApprovalMode_APPROVAL_MODE_ASK, DefaultModeSource: stdv1.DefaultModeSource_DEFAULT_MODE_SOURCE_CLI_CONFIG},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Seed demo world directly; Core handles demo mode natively without emitting RelayRpcEffect.
	dispatchEvent(t, c, &stdcorev1.Event{
		NowMs: 1_700_000_000_000,
		Event: &stdcorev1.Event_SeedDemoWorld{
			SeedDemoWorld: &stdcorev1.SeedDemoWorldEvent{
				Security: &stdcorev1.DemoSecurity{
					IsMember:          true,
					SelfDeviceId:      "dev-1",
					AuthorityDeviceId: "dev-1",
					TrustedMachineIds: []string{"m1"},
				},
				Machines: []*stdcorev1.DemoMachine{{
					MachineId: "m1",
					Online:    true,
					RawMeta:   metaBytes,
				}},
			},
		},
	})

	startBatch := dispatchEvent(t, c, &stdcorev1.Event{
		NowMs:  1_700_000_000_000,
		MonoMs: 1_000,
		Event: &stdcorev1.Event_SyncLifecycle{
			SyncLifecycle: &stdcorev1.SyncLifecycleAction{
				Kind: stdcorev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START,
			},
		},
	})
	var legID uint64
	var hasRender bool
	for _, e := range startBatch.Effects {
		if os := e.GetOpenStream(); os != nil {
			legID = os.LegId
		}
		if e.GetRender() != nil {
			hasRender = true
		}
		if e.GetRelayRpc() != nil || e.GetCacheRead() != nil {
			t.Fatalf("demo mode must not emit relay/cache effects on start, got %+v", e)
		}
	}
	if !hasRender || legID == 0 {
		t.Fatalf("expected render and open_stream effects, got %+v", startBatch.Effects)
	}

	readyBatch := dispatchEvent(t, c, &stdcorev1.Event{
		NowMs: 1_700_000_000_200,
		Event: &stdcorev1.Event_StreamReady{
			StreamReady: &stdcorev1.StreamReadyEvent{
				LegId:        legID,
				ServerTimeMs: 1_700_000_000_200,
			},
		},
	})
	for _, e := range readyBatch.Effects {
		if e.GetRelayRpc() != nil {
			t.Fatalf("demo mode must not emit RelayRpcEffect on stream ready, got %+v", e)
		}
	}

	scBytes, err := proto.Marshal(&stdcorev1.Screen{Screen: &stdcorev1.Screen_Home{Home: &stdcorev1.HomeScreenRequest{}}})
	if err != nil {
		t.Fatalf("marshal Screen: %v", err)
	}
	vmBytes, err := c.View(scBytes)
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	var vm stdcorev1.ViewModel
	if err := proto.Unmarshal(vmBytes, &vm); err != nil {
		t.Fatalf("unmarshal ViewModel: %v", err)
	}
	if vm.GetHome() == nil || !vm.GetHome().CanStart {
		t.Fatalf("unexpected HomeViewModel: %+v", vm.GetHome())
	}
}
