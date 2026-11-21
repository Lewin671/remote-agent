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

	startBatch := dispatchEvent(t, c, &stdcorev1.Event{
		NowMs:  1_700_000_000_000,
		MonoMs: 1_000,
		Event: &stdcorev1.Event_SyncLifecycle{
			SyncLifecycle: &stdcorev1.SyncLifecycleAction{
				Kind: stdcorev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START,
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

	tokBatch := dispatchEvent(t, c, &stdcorev1.Event{
		NowMs: 1_700_000_000_100,
		Event: &stdcorev1.Event_TokenResult{
			TokenResult: &stdcorev1.TokenResultEvent{
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

	readyBatch := dispatchEvent(t, c, &stdcorev1.Event{
		NowMs: 1_700_000_000_200,
		Event: &stdcorev1.Event_StreamReady{
			StreamReady: &stdcorev1.StreamReadyEvent{
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

	dispatchEvent(t, c, &stdcorev1.Event{
		NowMs: 1_700_000_000_300,
		Event: &stdcorev1.Event_ListsFetched{
			ListsFetched: &stdcorev1.ListsFetchedEvent{
				OpId: fetchOpID,
				Machines: []*stdcorev1.PlainMachine{
					{
						MachineId: "m1",
						Online:    true,
						Meta: &stdv1.MachineMeta{
							Hostname:        "DevBook.local",
							ProtocolVersion: 17,
							Agents: []*stdv1.AgentInfo{
								{Agent: stdv1.Agent_AGENT_CODEX, DefaultMode: stdv1.ApprovalMode_APPROVAL_MODE_ASK, DefaultModeSource: stdv1.DefaultModeSource_DEFAULT_MODE_SOURCE_CLI_CONFIG},
							},
						},
					},
				},
			},
		},
	})

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

func TestCoreSecCallRoundTrip(t *testing.T) {
	idReq, err := proto.Marshal(&stdcorev1.SecCallRequest{
		Call: &stdcorev1.SecCallRequest_DeviceId{
			DeviceId: &stdcorev1.SecDeviceIDCall{EdPublic: make([]byte, 32)},
		},
	})
	if err != nil {
		t.Fatalf("marshal SecCallRequest: %v", err)
	}
	idRespBytes, err := SecCall(idReq)
	if err != nil {
		t.Fatalf("SecCall(DeviceId): %v", err)
	}
	var idResp stdcorev1.SecCallResponse
	if err := proto.Unmarshal(idRespBytes, &idResp); err != nil {
		t.Fatalf("unmarshal SecCallResponse: %v", err)
	}
	if idResp.Error != "" || idResp.Text == "" {
		t.Fatalf("unexpected SecCall(DeviceId) response: %+v", &idResp)
	}

	_, err = SecCall([]byte{0xff, 0xff, 0xff})
	if err == nil {
		t.Fatalf("expected invalid SecCall bytes to fail")
	}
}
