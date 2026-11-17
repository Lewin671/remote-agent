package corebind

import (
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

func dispatchEvent(t *testing.T, c *Core, ev *corev1.Event) *corev1.EffectBatch {
	t.Helper()
	b, err := ev.Marshal()
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	effBytes, err := c.Dispatch(b)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var batch corev1.EffectBatch
	if err := batch.Unmarshal(effBytes); err != nil {
		t.Fatalf("unmarshal effects: %v", err)
	}
	return &batch
}

func TestCoreRoundTrip(t *testing.T) {
	c := New()
	defer c.Close()

	startBatch := dispatchEvent(t, c, &corev1.Event{
		NowMs:  1_700_000_000_000,
		MonoMs: 1_000,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{
				Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START,
			},
		},
	})
	var tokenOpID uint64
	var hasRender bool
	for _, e := range startBatch.Effects {
		if et := e.GetEnsureToken(); et != nil {
			tokenOpID = et.OpId
		}
		if e.GetRender() != nil {
			hasRender = true
		}
	}
	if !hasRender || tokenOpID == 0 {
		t.Fatalf("expected render and ensure_token effects, got %+v", startBatch.Effects)
	}

	tokBatch := dispatchEvent(t, c, &corev1.Event{
		NowMs: 1_700_000_000_100,
		Event: &corev1.Event_TokenResult{
			TokenResult: &corev1.TokenResultEvent{
				OpId:  tokenOpID,
				Token: "tok-1",
			},
		},
	})
	var legID uint64
	for _, e := range tokBatch.Effects {
		if os := e.GetOpenStream(); os != nil {
			legID = os.LegId
		}
	}
	if legID == 0 {
		t.Fatalf("expected open_stream effect, got %+v", tokBatch.Effects)
	}

	readyBatch := dispatchEvent(t, c, &corev1.Event{
		NowMs: 1_700_000_000_200,
		Event: &corev1.Event_StreamReady{
			StreamReady: &corev1.StreamReadyEvent{
				LegId:        legID,
				ServerTimeMs: 1_700_000_000_200,
			},
		},
	})
	var fetchOpID uint64
	for _, e := range readyBatch.Effects {
		if rpc := e.GetRpc(); rpc != nil && rpc.GetFetchLists() != nil {
			fetchOpID = rpc.OpId
		}
	}
	if fetchOpID == 0 {
		t.Fatalf("expected fetch_lists rpc effect, got %+v", readyBatch.Effects)
	}

	dispatchEvent(t, c, &corev1.Event{
		NowMs: 1_700_000_000_300,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId: fetchOpID,
				Machines: []*corev1.PlainMachine{
					{
						MachineId: "m1",
						Online:    true,
						Meta: &v1.MachineMeta{
							Hostname:        "DevBook.local",
							ProtocolVersion: 16,
							Agents: []*v1.AgentInfo{
								{Agent: v1.Agent_AGENT_CODEX, DefaultMode: v1.ApprovalMode_APPROVAL_MODE_ASK, DefaultModeSource: v1.DefaultModeSource_DEFAULT_MODE_SOURCE_CLI_CONFIG},
							},
						},
					},
				},
			},
		},
	})

	scBytes, _ := (&corev1.Screen{Screen: &corev1.Screen_Home{Home: &corev1.HomeScreenRequest{}}}).Marshal()
	vmBytes, err := c.View(scBytes)
	if err != nil {
		t.Fatalf("View: %v", err)
	}
	var vm corev1.ViewModel
	if err := vm.Unmarshal(vmBytes); err != nil {
		t.Fatalf("unmarshal ViewModel: %v", err)
	}
	if vm.GetHome() == nil || !vm.GetHome().CanStart {
		t.Fatalf("unexpected HomeViewModel: %+v", vm.GetHome())
	}
}
