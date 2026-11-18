package screens

import (
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

func TestBuildDevicesAndSettingsExcludeMachinesFromClientRows(t *testing.T) {
	st := NewState()
	st.Cal = lists.Calendar{NowMs: 1_700_000_000_000}
	st.World.Connection = lists.ConnConnected
	st.World.Machines = []*lists.Machine{
		{ID: "m1", Online: true, Meta: &v1.MachineMeta{Hostname: "BuildBox"}},
	}

	sv := &corev1.SyncStateView{
		SelfDeviceId:      "c1",
		AuthorityDeviceId: "c1",
		IsManager:         true,
		Devices: []*corev1.SyncDeviceView{
			{
				DeviceId: "m1",
				Kind:     v1.DeviceKind_DEVICE_KIND_MACHINE,
				Trusted:  true,
				Meta:     &v1.DeviceMeta{Name: "BuildBox"},
			},
			{
				DeviceId: "m-incomplete",
				Kind:     v1.DeviceKind_DEVICE_KIND_MACHINE,
				Meta:     &v1.DeviceMeta{Name: "JoiningMac"},
			},
			{
				DeviceId:   "c1",
				Kind:       v1.DeviceKind_DEVICE_KIND_CLIENT,
				Trusted:    true,
				LastSeenMs: 1_700_000_000_000,
				Meta:       &v1.DeviceMeta{Platform: "web", Description: "Chrome on macOS"},
			},
		},
	}

	dvm := BuildDevices(st, sv, nil)
	if len(dvm.Clients) != 1 || dvm.Clients[0].DeviceId != "c1" {
		t.Fatalf("expected only client c1 in Clients, got %+v", dvm.Clients)
	}
	if len(dvm.UnfinishedPairings) != 1 || dvm.UnfinishedPairings[0].DeviceId != "m-incomplete" {
		t.Fatalf("expected incomplete machine in UnfinishedPairings, got %+v", dvm.UnfinishedPairings)
	}

	svm := BuildSettings(st, sv, nil)
	if len(svm.RecentClients) != 1 || svm.RecentClients[0].DeviceId != "c1" {
		t.Fatalf("expected only client c1 in RecentClients, got %+v", svm.RecentClients)
	}
}
