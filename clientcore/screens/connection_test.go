package screens

import (
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func TestBuildConnectionBannerAndDetails(t *testing.T) {
	tests := []struct {
		name            string
		sv              *world.SyncState
		wantConnected   bool
		wantBannerKind  corev1.ConnBannerKind
		wantVisible     bool
		wantErrorTone   bool
		wantShowDetails bool
		wantTextKey     string
		wantFailedKey   string
		wantDetailKey   string
		wantShowDisconn bool
	}{
		{
			name: "connected hides banner",
			sv: &world.SyncState{
				Connection:       corev1.ConnState_CONN_STATE_CONNECTED,
				NetworkAvailable: true,
				MachinesListed:   true,
				SessionsListed:   true,
			},
			wantConnected: true,
			wantVisible:   false,
		},
		{
			name: "display grace hides banner while reconnecting",
			sv: &world.SyncState{
				Connection:       corev1.ConnState_CONN_STATE_RECONNECTING,
				NetworkAvailable: true,
				DisplayGrace:     true,
				LiveLoaded:       true,
			},
			wantConnected: false,
			wantVisible:   false,
		},
		{
			name: "no network empty",
			sv: &world.SyncState{
				Connection:       corev1.ConnState_CONN_STATE_NO_NETWORK,
				NetworkAvailable: false,
			},
			wantBannerKind: corev1.ConnBannerKind_CONN_BANNER_KIND_NO_NETWORK,
			wantVisible:    true,
			wantTextKey:    text.KeyConnectionNoNetworkEmpty,
		},
		{
			name: "no network with kept data",
			sv: &world.SyncState{
				Connection:       corev1.ConnState_CONN_STATE_NO_NETWORK,
				NetworkAvailable: false,
				MachinesListed:   true,
				SessionsListed:   true,
			},
			wantBannerKind: corev1.ConnBannerKind_CONN_BANNER_KIND_NO_NETWORK,
			wantVisible:    true,
			wantTextKey:    text.KeyConnectionNoNetworkPrevious,
		},
		{
			name: "no network without listed sessions is still empty per buildSyncConnBanner",
			sv: &world.SyncState{
				Connection:       corev1.ConnState_CONN_STATE_NO_NETWORK,
				NetworkAvailable: false,
			},
			wantBannerKind: corev1.ConnBannerKind_CONN_BANNER_KIND_NO_NETWORK,
			wantVisible:    true,
			wantTextKey:    text.KeyConnectionNoNetworkEmpty,
		},
		{
			name: "no server cold failure has error tone and failed text",
			sv: &world.SyncState{
				Connection:       corev1.ConnState_CONN_STATE_OFFLINE,
				NetworkAvailable: true,
				ConnectionReason: text.Msg(text.KeyErrorsConnectionTimedOut),
			},
			wantBannerKind:  corev1.ConnBannerKind_CONN_BANNER_KIND_NO_SERVER,
			wantVisible:     true,
			wantErrorTone:   true,
			wantShowDetails: true,
			wantTextKey:     text.KeyConnectionServerRetrying,
			wantFailedKey:   text.KeyConnectionServerFailed,
			wantDetailKey:   text.KeyConnectionServerDetails,
		},
		{
			name: "no server with kept data omits error tone and failed text",
			sv: &world.SyncState{
				Connection:       corev1.ConnState_CONN_STATE_OFFLINE,
				NetworkAvailable: true,
				MachinesListed:   true,
				SessionsListed:   true,
				SecurityLoaded:   true,
				DisconnectedAtMs: 1_700_000_000_000,
				ConnectionReason: text.Msg(text.KeyConnectionNoServer),
			},
			wantBannerKind:  corev1.ConnBannerKind_CONN_BANNER_KIND_NO_SERVER,
			wantVisible:     true,
			wantErrorTone:   false,
			wantShowDetails: true,
			wantTextKey:     text.KeyConnectionServerRetrying,
			wantDetailKey:   text.KeyConnectionServerDetails,
			wantShowDisconn: true,
		},
		{
			name: "connecting cold start",
			sv: &world.SyncState{
				Connection:       corev1.ConnState_CONN_STATE_CONNECTING,
				NetworkAvailable: true,
			},
			wantBannerKind: corev1.ConnBannerKind_CONN_BANNER_KIND_WAITING,
			wantVisible:    true,
			wantTextKey:    text.KeyConnectionConnecting,
		},
		{
			name: "reconnecting before live loaded still says connecting per buildSyncConnBanner",
			sv: &world.SyncState{
				Connection:       corev1.ConnState_CONN_STATE_RECONNECTING,
				NetworkAvailable: true,
				SecurityLoaded:   true,
			},
			wantBannerKind: corev1.ConnBannerKind_CONN_BANNER_KIND_WAITING,
			wantVisible:    true,
			wantTextKey:    text.KeyConnectionConnecting,
		},
		{
			name: "reconnecting after live loaded",
			sv: &world.SyncState{
				Connection:       corev1.ConnState_CONN_STATE_RECONNECTING,
				NetworkAvailable: true,
				LiveLoaded:       true,
			},
			wantBannerKind: corev1.ConnBannerKind_CONN_BANNER_KIND_WAITING,
			wantVisible:    true,
			wantTextKey:    text.KeyConnectionReconnecting,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := NewState(nil)
			st.Cal.NowMs = 1_700_000_060_000
			vm := BuildConnection(st, tc.sv)
			syncBanner := buildSyncConnBanner(tc.sv, nil)
			if vm.Connected != tc.wantConnected {
				t.Fatalf("Connected = %v, want %v", vm.Connected, tc.wantConnected)
			}
			if !tc.wantVisible {
				if vm.Banner != nil && vm.Banner.Visible {
					t.Fatalf("expected hidden banner, got %+v", vm.Banner)
				}
				if syncBanner != nil && syncBanner.Visible {
					t.Fatalf("expected syncBanner hidden, got %+v", syncBanner)
				}
				return
			}
			if vm.Banner == nil || !vm.Banner.Visible {
				t.Fatalf("expected visible banner")
			}
			if syncBanner == nil || !syncBanner.Visible {
				t.Fatalf("expected syncBanner visible alongside ConnectionViewModel.Banner")
			}
			if vm.Banner.ErrorTone != syncBanner.ErrorTone || vm.Banner.KeptData != syncBanner.KeptData || vm.Banner.Text.GetKey() != syncBanner.Text.GetKey() {
				t.Fatalf("ConnectionBanner (%+v) diverged from SyncConnBanner (%+v)", vm.Banner, syncBanner)
			}
			if vm.Banner.Kind != tc.wantBannerKind {
				t.Fatalf("Banner.Kind = %v, want %v", vm.Banner.Kind, tc.wantBannerKind)
			}
			if vm.Banner.ErrorTone != tc.wantErrorTone {
				t.Fatalf("Banner.ErrorTone = %v, want %v", vm.Banner.ErrorTone, tc.wantErrorTone)
			}
			if vm.Banner.ShowDetails != tc.wantShowDetails {
				t.Fatalf("Banner.ShowDetails = %v, want %v", vm.Banner.ShowDetails, tc.wantShowDetails)
			}
			if got := vm.Banner.Text.GetKey(); got != tc.wantTextKey {
				t.Fatalf("Banner.Text.Key = %q, want %q", got, tc.wantTextKey)
			}
			if got := vm.Banner.FailedText.GetKey(); got != tc.wantFailedKey {
				t.Fatalf("Banner.FailedText.Key = %q, want %q", got, tc.wantFailedKey)
			}
			if got := vm.Banner.DetailText.GetKey(); got != tc.wantDetailKey {
				t.Fatalf("Banner.DetailText.Key = %q, want %q", got, tc.wantDetailKey)
			}
			if vm.ServerDetails.ShowDisconnectedAt != tc.wantShowDisconn {
				t.Fatalf("ServerDetails.ShowDisconnectedAt = %v, want %v", vm.ServerDetails.ShowDisconnectedAt, tc.wantShowDisconn)
			}
		})
	}
}

func TestBuildHomeBannersAndPrefs(t *testing.T) {
	st := NewState(nil)
	st.World.Connection = lists.ConnConnected
	st.World.AccountClockSkewed = true
	st.World.HasMoreSessions = true
	st.SessionListError = world.Reason{Kind: world.ReasonConnectionTimedOut}
	st.World.PrefsSyncing = true

	vm := BuildHome(st, 30)
	if vm.ListErrorText.GetKey() != text.KeyErrorsConnectionTimedOut {
		t.Fatalf("ListErrorText = %+v", vm.ListErrorText)
	}
	if vm.ClockSkewBannerText.GetKey() != text.KeyErrorsThisDeviceSClockIsBehindTheAccountState {
		t.Fatalf("ClockSkewBannerText = %+v", vm.ClockSkewBannerText)
	}
	if !vm.HasMoreSessions {
		t.Fatal("expected HasMoreSessions = true")
	}
	if vm.PrefsBanner == nil || !vm.PrefsBanner.Visible || !vm.PrefsBanner.Syncing {
		t.Fatalf("expected syncing PrefsBanner, got %+v", vm.PrefsBanner)
	}

	st.World.PrefsSyncing = false
	st.World.PrefsLoadFailed = true
	vm = BuildHome(st, 30)
	if vm.PrefsBanner == nil || !vm.PrefsBanner.Visible || !vm.PrefsBanner.ErrorTone || !vm.PrefsBanner.CanRetry {
		t.Fatalf("expected error PrefsBanner, got %+v", vm.PrefsBanner)
	}

	st.World.Prefs = &v1.ProjectPrefs{Schema: 1}
	vm = BuildHome(st, 30)
	if vm.PrefsBanner != nil {
		t.Fatalf("expected nil PrefsBanner when cached prefs exist, got %+v", vm.PrefsBanner)
	}

	// BuildConnection exposes ClockSkewNotice from the world and clears it when healthy.
	connHealthy := BuildConnection(NewState(nil), &world.SyncState{Connection: corev1.ConnState_CONN_STATE_CONNECTED})
	if connHealthy.ClockSkewNotice != nil {
		t.Fatalf("expected nil ClockSkewNotice when healthy, got %+v", connHealthy.ClockSkewNotice)
	}
	connFromWorld := BuildConnection(st, &world.SyncState{Connection: corev1.ConnState_CONN_STATE_CONNECTED})
	if connFromWorld.ClockSkewNotice.GetKey() != text.KeyErrorsThisDeviceSClockIsBehindTheAccountState {
		t.Fatalf("ClockSkewNotice from world = %+v", connFromWorld.ClockSkewNotice)
	}
}

func TestBuildSessionHeaderTerminalAndFrozenAt(t *testing.T) {
	st := NewState(nil)
	st.World.Connection = lists.ConnOffline
	st.DisconnectedAtMs = 1_700_000_050_000
	online := false
	mac := &lists.Machine{ID: "m1", Online: false, Reachable: &online, LastSeenMs: 1_700_000_010_000}
	sess := &lists.Session{ID: "s1", MachineID: "m1", Info: &v1.Session{Id: "s1", Cwd: "/repo"}}
	st.World.Machines = []*lists.Machine{mac}
	st.World.Sessions = []*lists.Session{sess}

	snap := world.SessionSnapshot{
		Found:            true,
		Conn:             lists.ConnOffline,
		Session:          sess,
		Machine:          mac,
		TerminalReady:    false,
		TerminalWaiting:  true,
		DisconnectedAtMs: 1_700_000_050_000,
	}
	vm := BuildSessionView(snap, st, &corev1.SessionScreenRequest{SessionId: "s1"}, nil)
	if vm.Header.TerminalReady || !vm.Header.TerminalWaiting {
		t.Fatalf("unexpected terminal flags: ready=%v waiting=%v", vm.Header.TerminalReady, vm.Header.TerminalWaiting)
	}
	if vm.Header.FrozenAtMs != 1_700_000_010_000 {
		t.Fatalf("FrozenAtMs = %d, want offline_since_ms 1700000010000", vm.Header.FrozenAtMs)
	}

	mac.LastSeenMs = 0
	vm2 := BuildSessionView(snap, st, &corev1.SessionScreenRequest{SessionId: "s1"}, nil)
	if vm2.Header.FrozenAtMs != 1_700_000_050_000 {
		t.Fatalf("FrozenAtMs = %d, want disconnected_at_ms 1700000050000", vm2.Header.FrozenAtMs)
	}
}

func TestBuildHomeSuppressesWaitingHeadlineDuringDisplayGrace(t *testing.T) {
	st := NewState(nil)
	st.World.Connection = lists.ConnConnecting
	st.DisplayGrace = true
	vm := BuildHome(st, 30)
	if vm.Headline != nil && (vm.Headline.IsConnecting || vm.Headline.Title != nil) {
		t.Fatalf("expected waiting headline suppressed during DisplayGrace, got %+v", vm.Headline)
	}

	st.DisplayGrace = false
	vm2 := BuildHome(st, 30)
	if vm2.Headline == nil || !vm2.Headline.IsConnecting || vm2.Headline.Title.GetKey() != text.KeyConnectionConnecting {
		t.Fatalf("expected connecting headline once DisplayGrace ends, got %+v", vm2.Headline)
	}
}

func TestSettingsSecuritySummaryAndMachineManagementFlags(t *testing.T) {
	st := NewState(nil)
	st.World.Connection = lists.ConnConnected
	st.World.Machines = []*lists.Machine{
		{ID: "m-in-dir", Online: true},
		{ID: "m-archived", Online: false, Archived: true},
	}

	// Non-manager client in directory -> CanPrepareHandoff=true, AuthorityNameText populated.
	memberSV := &world.SyncState{
		Connection:        corev1.ConnState_CONN_STATE_CONNECTED,
		SecurityLoaded:    true,
		IsManager:         false,
		SelfDeviceID:      "client-2",
		AuthorityDeviceID: "client-1",
		Devices: []*world.SyncDevice{
			{DeviceID: "client-1", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, Meta: &v1.DeviceMeta{Name: "Main Phone"}},
			{DeviceID: "client-2", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT},
			{DeviceID: "m-in-dir", Kind: v1.DeviceKind_DEVICE_KIND_MACHINE},
		},
	}
	ApplySyncStateTexts(memberSV, nil, 0, world.Reason{})
	settingsVM := BuildSettings(st, memberSV, NewSettingsExtra())
	if !settingsVM.Security.CanPrepareHandoff {
		t.Fatal("expected CanPrepareHandoff=true for member client")
	}
	if settingsVM.Security.AuthorityNameText == nil || settingsVM.Security.AuthorityNameText.GetLiteral() != "Main Phone" {
		t.Fatalf("unexpected AuthorityNameText: %+v", settingsVM.Security.AuthorityNameText)
	}
	if settingsVM.Security.LeaseRenewalAdvice.GetKey() != text.KeySecurityOpenToRenew {
		t.Fatalf("unexpected LeaseRenewalAdvice: %+v", settingsVM.Security.LeaseRenewalAdvice)
	}

	// Manager viewing archived machine (not in device directory) vs in-dir machine -> CanDeleteRecords checks !inDeviceDir.
	mgrSV := &world.SyncState{
		Connection:        corev1.ConnState_CONN_STATE_CONNECTED,
		SecurityLoaded:    true,
		IsManager:         true,
		SelfDeviceID:      "client-1",
		AuthorityDeviceID: "client-1",
		Devices: []*world.SyncDevice{
			{DeviceID: "client-1", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT},
			{DeviceID: "m-in-dir", Kind: v1.DeviceKind_DEVICE_KIND_MACHINE},
		},
	}
	ApplySyncStateTexts(mgrSV, nil, 0, world.Reason{})
	mgrSettings := BuildSettings(st, mgrSV, NewSettingsExtra())
	if mgrSettings.Security.CanPrepareHandoff {
		t.Fatal("expected CanPrepareHandoff=false for manager")
	}
	inDirVM := BuildMachineSettings(st, mgrSV, NewSettingsExtra(), "m-in-dir")
	if inDirVM.Management.CanDeleteRecords {
		t.Fatal("expected CanDeleteRecords=false when machine is still in device directory")
	}
	archivedVM := BuildMachineSettings(st, mgrSV, NewSettingsExtra(), "m-archived")
	if !archivedVM.Management.CanDeleteRecords {
		t.Fatal("expected CanDeleteRecords=true for archived machine not in device directory")
	}
}

func TestSchedulesInitialLoaded(t *testing.T) {
	st := NewState(nil)
	vm := BuildSchedules(st, 30)
	if vm.InitialLoaded {
		t.Fatal("expected InitialLoaded=false before lists/prefs load")
	}
	detailVM := BuildScheduleDetail(st, "m1", "t1", 30)
	if detailVM.InitialLoaded {
		t.Fatal("expected ScheduleDetailView.InitialLoaded=false before lists/prefs load")
	}

	st.World.SessionsListed = true
	vm2 := BuildSchedules(st, 30)
	if !vm2.InitialLoaded {
		t.Fatal("expected InitialLoaded=true once SessionsListed")
	}
	detailVM2 := BuildScheduleDetail(st, "m1", "t1", 30)
	if !detailVM2.InitialLoaded {
		t.Fatal("expected ScheduleDetailView.InitialLoaded=true once SessionsListed")
	}
}

func TestSchedulesSkipMachinesTheCoreDoesNotUse(t *testing.T) {
	st := NewState(nil)
	st.World.MachinesListed = true
	st.World.SessionsListed = true
	st.World.Machines = []*lists.Machine{{ID: "m-removed", Online: true}}
	st.Schedules.MachineUsable = func(string) bool { return false }
	vm := BuildSchedules(st, 30)
	if vm.Loading {
		t.Fatal("a machine without a snapshot because it is untrusted must not hold the list loading")
	}
	if vm.UnsupportedNotice != nil {
		t.Fatal("an untrusted machine alone must not ask to update remote-agent for schedules")
	}
}
