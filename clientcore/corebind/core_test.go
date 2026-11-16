package corebind

import (
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

func TestCoreRoundTrip(t *testing.T) {
	c := New()
	defer c.Close()

	ev := &corev1.Event{
		Event: &corev1.Event_ConnectionChanged{
			ConnectionChanged: &corev1.ConnectionChanged{
				State:             corev1.ConnState_CONN_STATE_CONNECTED,
				LivePushConnected: true,
			},
		},
	}
	evBytes, err := ev.Marshal()
	if err != nil {
		t.Fatalf("marshal event: %v", err)
	}
	effBytes, err := c.Dispatch(evBytes)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	var batch corev1.EffectBatch
	if err := batch.Unmarshal(effBytes); err != nil {
		t.Fatalf("unmarshal effects: %v", err)
	}
	if len(batch.Effects) == 0 || batch.Effects[0].GetRender() == nil {
		t.Fatalf("expected render effect, got %+v", batch.Effects)
	}

	evMachines := &corev1.Event{
		Event: &corev1.Event_MachinesUpdated{
			MachinesUpdated: &corev1.MachinesUpdated{
				Listed: true,
				Machines: []*corev1.MachineInput{
					{
						MachineId: "m1",
						Online:    true,
						Meta: &v1.MachineMeta{
							Hostname: "DevBook.local",
							Agents: []*v1.AgentInfo{
								{Agent: v1.Agent_AGENT_CODEX, DefaultMode: v1.ApprovalMode_APPROVAL_MODE_ASK, DefaultModeSource: v1.DefaultModeSource_DEFAULT_MODE_SOURCE_CLI_CONFIG},
							},
						},
					},
				},
			},
		},
	}
	evMachBytes, _ := evMachines.Marshal()
	if _, err := c.Dispatch(evMachBytes); err != nil {
		t.Fatalf("Dispatch machines: %v", err)
	}

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
