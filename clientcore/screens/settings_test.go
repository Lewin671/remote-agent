package screens

import (
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
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

func TestMissingOrEmptyMachinesReportLoadingUntilListedOrOffline(t *testing.T) {
	cases := []struct {
		name            string
		conn            lists.ConnectionKind
		listed          bool
		unlinked        bool
		wantLoading     bool
		wantEmptyKey    string
		wantMissingKey  string
		wantQuotaKey    string
		wantPollState   corev1.MachinePollState
		wantStatusTitle string
	}{
		{
			name:            "connecting before machines listed reports loading",
			conn:            lists.ConnConnecting,
			listed:          false,
			wantLoading:     true,
			wantEmptyKey:    text.KeyCommonLoading,
			wantMissingKey:  text.KeyCommonLoading,
			wantQuotaKey:    text.KeyCommonLoading,
			wantPollState:   corev1.MachinePollState_MACHINE_POLL_STATE_LOADING,
			wantStatusTitle: text.KeyCommonLoading,
		},
		{
			name:            "offline before machines listed stops waiting",
			conn:            lists.ConnOffline,
			listed:          false,
			wantLoading:     false,
			wantEmptyKey:    text.KeySettingsNoComputersYet,
			wantMissingKey:  text.KeySettingsDeviceNotFound,
			wantQuotaKey:    text.KeySettingsDeviceNotFound,
			wantPollState:   corev1.MachinePollState_MACHINE_POLL_STATE_UNAVAILABLE,
			wantStatusTitle: text.KeyMachineStatusUnavailable,
		},
		{
			name:            "no network before machines listed stops waiting",
			conn:            lists.ConnNoNetwork,
			listed:          false,
			wantLoading:     false,
			wantEmptyKey:    text.KeySettingsNoComputersYet,
			wantMissingKey:  text.KeySettingsDeviceNotFound,
			wantQuotaKey:    text.KeySettingsDeviceNotFound,
			wantPollState:   corev1.MachinePollState_MACHINE_POLL_STATE_UNAVAILABLE,
			wantStatusTitle: text.KeyMachineStatusUnavailable,
		},
		{
			name:            "listed with zero machines shows real empty and not-found",
			conn:            lists.ConnConnected,
			listed:          true,
			wantLoading:     false,
			wantEmptyKey:    text.KeySettingsNoComputersYet,
			wantMissingKey:  text.KeySettingsDeviceNotFound,
			wantQuotaKey:    text.KeySettingsDeviceNotFound,
			wantPollState:   corev1.MachinePollState_MACHINE_POLL_STATE_UNAVAILABLE,
			wantStatusTitle: text.KeyMachineStatusUnavailable,
		},
		{
			name:            "unlinked world never stays loading",
			conn:            lists.ConnConnected,
			listed:          false,
			unlinked:        true,
			wantLoading:     false,
			wantEmptyKey:    text.KeySettingsNoComputersYet,
			wantMissingKey:  text.KeySettingsDeviceNotFound,
			wantQuotaKey:    text.KeySettingsThisDeviceWasRemovedFromTheAccount,
			wantPollState:   corev1.MachinePollState_MACHINE_POLL_STATE_UNAVAILABLE,
			wantStatusTitle: text.KeyMachineStatusUnavailable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := NewState()
			st.World.Connection = tc.conn
			st.World.MachinesListed = tc.listed
			unlinkedFlag := tc.unlinked
			st.World.Unlinked = &unlinkedFlag
			sv := &corev1.SyncStateView{Unlinked: tc.unlinked}

			svm := BuildSettings(st, sv, nil)
			if svm.MachinesLoading != tc.wantLoading {
				t.Fatalf("Settings MachinesLoading=%v want %v", svm.MachinesLoading, tc.wantLoading)
			}
			if svm.GetEmptyMachinesText().GetKey() != tc.wantEmptyKey {
				t.Fatalf("Settings EmptyMachinesText=%q want %q", svm.GetEmptyMachinesText().GetKey(), tc.wantEmptyKey)
			}

			dvm := BuildDevices(st, sv, nil)
			if dvm.MachinesLoading != tc.wantLoading {
				t.Fatalf("Devices MachinesLoading=%v want %v", dvm.MachinesLoading, tc.wantLoading)
			}
			if dvm.GetEmptyComputersText().GetKey() != tc.wantEmptyKey {
				t.Fatalf("Devices EmptyComputersText=%q want %q", dvm.GetEmptyComputersText().GetKey(), tc.wantEmptyKey)
			}

			mvm := BuildMachineSettings(st, sv, nil, "m-missing")
			if mvm.Loading != tc.wantLoading || mvm.GetOfflineOrMismatchBanner().GetKey() != tc.wantMissingKey {
				t.Fatalf("MachineSettings loading=%v banner=%q want (%v, %q)", mvm.Loading, mvm.GetOfflineOrMismatchBanner().GetKey(), tc.wantLoading, tc.wantMissingKey)
			}

			qvm := BuildQuota(st, sv, "m-missing", 0)
			if qvm.Loading != tc.wantLoading || qvm.GetEmptyText().GetKey() != tc.wantQuotaKey {
				t.Fatalf("Quota loading=%v empty=%q want (%v, %q)", qvm.Loading, qvm.GetEmptyText().GetKey(), tc.wantLoading, tc.wantQuotaKey)
			}

			msvm := BuildMachineStatus(st, nil, "m-missing")
			if msvm.PollState != tc.wantPollState || msvm.GetUnavailableTitle().GetKey() != tc.wantStatusTitle {
				t.Fatalf("MachineStatus poll=%v title=%q want (%v, %q)", msvm.PollState, msvm.GetUnavailableTitle().GetKey(), tc.wantPollState, tc.wantStatusTitle)
			}
		})
	}
}
