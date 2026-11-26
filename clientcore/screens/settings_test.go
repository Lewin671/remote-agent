package screens

import (
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func TestBuildDevicesAndSettingsExcludeMachinesFromClientRows(t *testing.T) {
	st := NewState(nil)
	st.Cal = lists.Calendar{NowMs: 1_700_000_000_000}
	st.World.Connection = lists.ConnConnected
	st.World.Machines = []*lists.Machine{
		{ID: "m1", Online: true, Meta: &v1.MachineMeta{Hostname: "BuildBox"}},
	}

	sv := &world.SyncState{
		SelfDeviceID:      "c1",
		AuthorityDeviceID: "c1",
		IsManager:         true,
		Devices: []*world.SyncDevice{
			{
				DeviceID:   "m1",
				Kind:       v1.DeviceKind_DEVICE_KIND_MACHINE,
				Trusted:    true,
				Registered: true,
				Meta:       &v1.DeviceMeta{Name: "BuildBox"},
			},
			{
				DeviceID:   "m-incomplete",
				Kind:       v1.DeviceKind_DEVICE_KIND_MACHINE,
				Trusted:    true,
				Registered: false,
				Meta:       &v1.DeviceMeta{Name: "JoiningMac"},
			},
			{
				DeviceID:   "c1",
				Kind:       v1.DeviceKind_DEVICE_KIND_CLIENT,
				Trusted:    true,
				Registered: true,
				LastSeenMs: 1_700_000_000_000,
				Meta:       &v1.DeviceMeta{Platform: "web", Description: "Chrome on macOS"},
			},
		},
	}

	dvm := BuildDevices(st, sv, nil)
	if len(dvm.Clients) != 1 || dvm.Clients[0].DeviceId != "c1" {
		t.Fatalf("expected only client c1 in Clients, got %+v", dvm.Clients)
	}
	if len(dvm.UnfinishedPairings) != 1 || dvm.UnfinishedPairings[0].DeviceId != "m-incomplete" || !dvm.UnfinishedPairings[0].Member || !dvm.UnfinishedPairings[0].PairingIncomplete {
		t.Fatalf("expected incomplete trusted machine in UnfinishedPairings, got %+v", dvm.UnfinishedPairings)
	}
	detail := BuildClientDetail(st, sv, nil, "m-incomplete")
	if !detail.Found || !detail.Member || !detail.CanRemove {
		t.Fatalf("expected unfinished machine pairing detail to be removable, got %+v", detail)
	}

	// Unfinished pairing with no Meta yet must still have a non-empty DisplayName
	// (falling back to ShortId) so detail header and removal dialog never render blank.
	sv.Devices = append(sv.Devices, &world.SyncDevice{
		DeviceID:   "4b5cb72cf559ecbf3593590a77aafc04",
		Kind:       v1.DeviceKind_DEVICE_KIND_CLIENT,
		Trusted:    true,
		Registered: false,
	})
	unnamedDetail := BuildClientDetail(st, sv, nil, "4b5cb72cf559ecbf3593590a77aafc04")
	if !unnamedDetail.Found || unnamedDetail.DisplayName != "…fc04" || unnamedDetail.ShortId != "…fc04" {
		t.Fatalf("expected unnamed unfinished pairing DisplayName to fall back to ShortId, got %+v", unnamedDetail)
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
			wantEmptyKey:    text.KeySettingsEmptyComputersTitle,
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
			wantEmptyKey:    text.KeySettingsEmptyComputersTitle,
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
			wantEmptyKey:    text.KeySettingsEmptyComputersTitle,
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
			wantEmptyKey:    text.KeySettingsEmptyComputersTitle,
			wantMissingKey:  text.KeySettingsDeviceNotFound,
			wantQuotaKey:    text.KeySettingsDeviceNotFound,
			wantPollState:   corev1.MachinePollState_MACHINE_POLL_STATE_UNAVAILABLE,
			wantStatusTitle: text.KeyMachineStatusUnavailable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := NewState(nil)
			st.World.Connection = tc.conn
			st.World.MachinesListed = tc.listed
			unlinkedFlag := tc.unlinked
			st.World.Unlinked = &unlinkedFlag
			sv := &world.SyncState{Unlinked: tc.unlinked}

			svm := BuildSettings(st, sv, nil)
			if svm.MachinesLoading != tc.wantLoading {
				t.Fatalf("Settings MachinesLoading=%v want %v", svm.MachinesLoading, tc.wantLoading)
			}
			if svm.GetEmptyMachinesText().GetKey() != tc.wantEmptyKey {
				t.Fatalf("Settings EmptyMachinesText=%q want %q", svm.GetEmptyMachinesText().GetKey(), tc.wantEmptyKey)
			}

			wantDevicesEmpty := tc.wantEmptyKey
			if wantDevicesEmpty == text.KeySettingsEmptyComputersTitle {
				wantDevicesEmpty = text.KeySettingsNoComputersYet
			}
			dvm := BuildDevices(st, sv, nil)
			if dvm.MachinesLoading != tc.wantLoading {
				t.Fatalf("Devices MachinesLoading=%v want %v", dvm.MachinesLoading, tc.wantLoading)
			}
			if dvm.GetEmptyComputersText().GetKey() != wantDevicesEmpty {
				t.Fatalf("Devices EmptyComputersText=%q want %q", dvm.GetEmptyComputersText().GetKey(), wantDevicesEmpty)
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

func ptrF64(v float64) *float64 { return &v }

func TestSettingsRedesignProjections(t *testing.T) {
	now := int64(1_791_633_600_000)
	st := NewState(nil)
	st.Cal = lists.Calendar{NowMs: now, TodayStartMs: now - 10_800_000}
	st.World.Connection = lists.ConnConnected
	st.World.ClientVersion = 17
	st.World.MachinesListed = true
	st.World.Machines = []*lists.Machine{
		{
			ID:     "m-online",
			Online: true,
			Meta: &v1.MachineMeta{
				Hostname:        "BuildBox",
				ProtocolVersion: 17,
				Capabilities:    &v1.MachineCapabilities{MachineStatus: true, CliSwitches: true, UsageStats: true},
				DiskStatus:      &v1.DiskStatus{AvailableBytes: 3_100_000_000, TotalBytes: 500_000_000_000},
				Agents: []*v1.AgentInfo{
					{Agent: v1.Agent_AGENT_CODEX, Version: "0.90.0", Verified: true},
					{Agent: v1.Agent_AGENT_CLAUDE, Version: "2.1.0", Verified: true},
					{Agent: v1.Agent_AGENT_ANTIGRAVITY, Version: "1.4.0", Verified: true},
				},
				DisabledAgents: []v1.Agent{v1.Agent_AGENT_ANTIGRAVITY},
				Quotas: []*v1.AgentQuota{
					{
						Agent: v1.Agent_AGENT_CODEX, State: v1.QuotaState_QUOTA_STATE_OK, ObservedMs: now - 60_000,
						Windows: []*v1.QuotaWindow{{Name: "primary", UsedPercent: ptrF64(82), ResetsAtMs: now + 3_600_000}},
					},
					{
						Agent: v1.Agent_AGENT_CLAUDE, State: v1.QuotaState_QUOTA_STATE_OK, ObservedMs: now - 60_000,
						Windows: []*v1.QuotaWindow{{Name: "seven_day", UsedPercent: ptrF64(46), ResetsAtMs: now + 3_600_000}},
					},
					{
						Agent: v1.Agent_AGENT_ANTIGRAVITY, State: v1.QuotaState_QUOTA_STATE_OK, ObservedMs: now - 60_000,
						Windows: []*v1.QuotaWindow{{Name: "gemini-5h", UsedPercent: ptrF64(90), ResetsAtMs: now + 3_600_000}},
					},
				},
			},
		},
		{
			ID:     "m-mismatch",
			Online: true,
			Meta: &v1.MachineMeta{
				Hostname:        "OldLab",
				ProtocolVersion: 15,
			},
		},
		{
			ID:         "m-offline",
			Online:     false,
			LastSeenMs: now - 7_200_000,
			Meta: &v1.MachineMeta{
				Hostname:        "SleepBox",
				ProtocolVersion: 17,
				Quotas: []*v1.AgentQuota{
					{
						Agent: v1.Agent_AGENT_CODEX, State: v1.QuotaState_QUOTA_STATE_OK, ObservedMs: now - 60_000,
						Windows: []*v1.QuotaWindow{{Name: "primary", UsedPercent: ptrF64(95), ResetsAtMs: now + 3_600_000}},
					},
				},
			},
		},
	}

	st.World.Machines = append(st.World.Machines, &lists.Machine{
		ID:       "m-removed",
		Archived: true,
		Meta:     &v1.MachineMeta{Hostname: "ArchivedMac", ProtocolVersion: 16},
	})

	sv := &world.SyncState{
		SelfDeviceID:      "c-self",
		AuthorityDeviceID: "c-auth",
		IsManager:         false,
		RecoveryPending:   true,
		Devices: []*world.SyncDevice{
			{DeviceID: "c-self", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, Trusted: true, Registered: true},
			{DeviceID: "c-auth", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, Trusted: true, Registered: true, Meta: &v1.DeviceMeta{Name: "Pixel 9"}},
			{DeviceID: "c-pending", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, Trusted: true, Registered: false},
		},
	}

	extra := NewSettingsExtra()
	extra.Notifications.Permission = corev1.NotificationPermissionState_NOTIFICATION_PERMISSION_STATE_DENIED

	vm := BuildSettings(st, sv, extra)
	if vm.GetMachineCountText().GetKey() != text.KeySettingsMachineCount {
		t.Fatalf("expected machine_count_text when >2 machines, got %+v", vm.GetMachineCountText())
	}
	if !vm.ClientsSummaryWarn || vm.UnfinishedPairingCount != 1 || vm.GetClientsSummaryText().GetKey() != text.KeySettingsClientsSummaryPending {
		t.Fatalf("unexpected client summary: warn=%v pending=%d text=%+v", vm.ClientsSummaryWarn, vm.UnfinishedPairingCount, vm.GetClientsSummaryText())
	}
	if !vm.Notifications.SummaryWarn || vm.Notifications.GetSummaryText().GetKey() != text.KeySettingsNotificationsSummaryUnreachable {
		t.Fatalf("unexpected notifications summary: warn=%v text=%+v", vm.Notifications.SummaryWarn, vm.Notifications.GetSummaryText())
	}
	if vm.GetRecoveryBannerText().GetKey() != text.KeySettingsRecoveryWaitingBanner {
		t.Fatalf("expected non-manager recovery banner, got %+v", vm.GetRecoveryBannerText())
	}
	if vm.Security.GetSummaryText().GetKey() != text.KeySettingsSecuritySummaryNonAuthority {
		t.Fatalf("expected concise non-authority summary, got %+v", vm.Security.GetSummaryText())
	}

	byID := map[string]*corev1.MachineListCardView{}
	for _, mc := range vm.Machines {
		byID[mc.MachineId] = mc
	}

	on := byID["m-online"]
	if on.StatusDot != corev1.MachineStatusDot_MACHINE_STATUS_DOT_GREEN_LIVE || on.Dimmed || len(on.Alerts) != 1 || len(on.QuotaItems) != 2 {
		t.Fatalf("unexpected online card: dot=%v dimmed=%v alerts=%+v quotas=%+v", on.StatusDot, on.Dimmed, on.Alerts, on.QuotaItems)
	}
	if on.Alerts[0].Kind != corev1.MachineAlertKind_MACHINE_ALERT_KIND_DISK_LOW || on.Alerts[0].GetText().GetKey() != text.KeySettingsMachineSubDiskAlert || !on.Alerts[0].Warn {
		t.Fatalf("expected disk warn alert first, got %+v", on.Alerts[0])
	}
	if !on.QuotaItems[0].Warn || on.QuotaItems[0].RemainingPercent != 18 || on.QuotaItems[1].Warn || on.QuotaItems[1].RemainingPercent != 54 {
		t.Fatalf("expected Codex 18%% warn=true and Claude 54%% warn=false, got %+v", on.QuotaItems)
	}

	mm := byID["m-mismatch"]
	if mm.Dimmed || mm.StatusDot != corev1.MachineStatusDot_MACHINE_STATUS_DOT_ORANGE_STATIC || mm.GetStatusText().GetKey() != text.KeySettingsStatusNeedsUpdate {
		t.Fatalf("unexpected mismatch card: %+v", mm)
	}
	if len(mm.Alerts) != 1 || mm.Alerts[0].Kind != corev1.MachineAlertKind_MACHINE_ALERT_KIND_PROTOCOL_MISMATCH || mm.Alerts[0].GetText().GetKey() != text.KeySettingsMachineSubVersionMismatch {
		t.Fatalf("expected daemon mismatch alert, got %+v", mm.Alerts)
	}

	off := byID["m-offline"]
	if !off.Dimmed || off.StatusDot != corev1.MachineStatusDot_MACHINE_STATUS_DOT_GRAY_STATIC || len(off.Alerts) != 0 || len(off.QuotaItems) != 0 || off.GetStatusText().GetKey() != text.KeyTimeHoursAgo {
		t.Fatalf("unexpected offline card (must be dimmed & omit stale quotas): %+v", off)
	}

	rem := byID["m-removed"]
	if !rem.Dimmed || rem.StatusDot != corev1.MachineStatusDot_MACHINE_STATUS_DOT_UNSPECIFIED || rem.GetStatusText().GetKey() != text.KeySettingsStatusRemoved || rem.GetSecondaryNote().GetKey() != text.KeySettingsMachineSubRemoved {
		t.Fatalf("unexpected removed card: %+v", rem)
	}

	// Notification summary table-driven check
	notifCases := []struct {
		name     string
		prefs    NotificationPrefs
		wantKey  string
		wantWarn bool
	}{
		{"ios_unsupported_calm", NotificationPrefs{Supported: false, NotifyAttention: true}, text.KeySettingsNotificationsSummaryOff, false},
		{"all_off", NotificationPrefs{Supported: true}, text.KeySettingsNotificationsSummaryOff, false},
		{"pending_only", NotificationPrefs{Supported: true, NotifyAttention: true}, text.KeySettingsNotificationsSummaryPendingOnly, false},
		{"all_on", NotificationPrefs{Supported: true, NotifyAttention: true, NotifyTurnEnded: true}, text.KeySettingsNotificationsSummaryOn, false},
		{"push_failed", NotificationPrefs{Supported: true, NotifyAttention: true, PushState: corev1.PushRegistrationState_PUSH_REGISTRATION_STATE_FAILED}, text.KeySettingsNotificationsSummaryUnreachable, true},
	}
	for _, tc := range notifCases {
		nv := buildNotificationsView(tc.prefs)
		if nv.GetSummaryText().GetKey() != tc.wantKey || nv.SummaryWarn != tc.wantWarn {
			t.Fatalf("notif %s: got (%q, %v), want (%q, %v)", tc.name, nv.GetSummaryText().GetKey(), nv.SummaryWarn, tc.wantKey, tc.wantWarn)
		}
	}
}

func TestMachineSettingsRedesignProjections(t *testing.T) {
	now := int64(1_791_633_600_000)
	st := NewState(nil)
	st.Cal = lists.Calendar{NowMs: now}
	st.World.Connection = lists.ConnConnected
	st.World.ClientVersion = 17
	st.World.Machines = []*lists.Machine{
		{
			ID:     "1234567890abcdef",
			Online: true,
			Meta: &v1.MachineMeta{
				Hostname:        "Studio",
				ProtocolVersion: 17,
				Capabilities:    &v1.MachineCapabilities{MachineStatus: true, CliSwitches: true, UsageStats: true},
				DiskStatus:      &v1.DiskStatus{AvailableBytes: 3_000_000_000, TotalBytes: 100_000_000_000},
				Agents: []*v1.AgentInfo{
					{Agent: v1.Agent_AGENT_CODEX, Version: "0.90.0", Verified: true},
					{Agent: v1.Agent_AGENT_CLAUDE, Version: "2.1.0", Verified: false},
				},
				DisabledAgents: []v1.Agent{v1.Agent_AGENT_CLAUDE},
				Quotas: []*v1.AgentQuota{
					{
						Agent: v1.Agent_AGENT_CODEX, State: v1.QuotaState_QUOTA_STATE_OK, ObservedMs: now - 10_000,
						Windows: []*v1.QuotaWindow{{Name: "primary", UsedPercent: ptrF64(100), ResetsAtMs: now + 3_000_000}},
					},
				},
				Projects: []*v1.ProjectSummary{
					{Path: "/code/app", LatestCreatedMs: now - 1000},
					{Path: "/code/hidden", LatestCreatedMs: now - 2000},
				},
			},
		},
	}
	st.World.Prefs = &v1.ProjectPrefs{
		Hidden: []*v1.ProjectPref{{Path: "/code/hidden"}},
	}
	st.MachineSettings["1234567890abcdef"] = &SettingsCacheEntry{
		Loaded: true,
		Settings: &v1.DaemonSettings{
			DefaultModel:          "gpt-6-astra",
			DefaultMode:           v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS,
			ImageUploadQuotaBytes: 500 * 1024 * 1024,
		},
	}
	st.UsageSnapshots[UsageCacheKey{MachineID: "1234567890abcdef", Preset: v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS}] = &UsageSnapshotEntry{
		Live: &v1.UsageStatsResponse{
			Series: []*v1.UsageSeries{
				{Agent: v1.Agent_AGENT_CODEX, Model: "m1", Current: &v1.UsagePeriodSummary{UsageRequestCount: 2, OutputTokens: 1_500_000}},
				{Agent: v1.Agent_AGENT_CLAUDE, Model: "c1", Current: &v1.UsagePeriodSummary{UsageRequestCount: 3, OutputTokens: 9_000_000}}, // disabled -> excluded
			},
		},
	}

	extra := NewSettingsExtra()
	mExtra := extra.EnsureMachine("1234567890abcdef")
	mExtra.ImageStorage = &v1.ImageStorageUsage{
		UploadBytes:           400 * 1024 * 1024,
		CacheBytes:            50 * 1024 * 1024,
		EmergencyReserveBytes: 32 * 1024 * 1024,
		ReclaimableBytes:      100 * 1024 * 1024,
	}
	st.DirectReportFn = func(machineID string) (bool, *corev1.DirectRouteReport, bool) {
		if machineID != "1234567890abcdef" {
			return false, nil, false
		}
		return true, &corev1.DirectRouteReport{
			Enabled:        true,
			Capable:        true,
			State:          corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED,
			Reason:         corev1.DirectRouteReason_DIRECT_ROUTE_REASON_NO_TRAVERSAL,
			HasRelayReadMs: true,
			RelayReadMs:    184,
		}, true
	}

	sv := &world.SyncState{
		SelfDeviceID:      "c-self",
		AuthorityDeviceID: "c-auth",
		IsManager:         false,
		Devices: []*world.SyncDevice{
			{DeviceID: "c-auth", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, Trusted: true, Registered: true, Meta: &v1.DeviceMeta{Name: "Pixel 9"}},
		},
	}

	vm := BuildMachineSettings(st, sv, extra, "1234567890abcdef")
	if vm.ShortId != "…90abcdef" {
		t.Fatalf("expected 8-char ShortId, got %q", vm.ShortId)
	}
	if !vm.StatusCardWarn || vm.GetStatusCardBadge().GetKey() != text.KeySettingsStatusCardDiskTight {
		t.Fatalf("expected disk tight badge, got warn=%v badge=%+v", vm.StatusCardWarn, vm.GetStatusCardBadge())
	}
	if vm.UsageTotalTokens != 1_500_000 {
		t.Fatalf("expected UsageTotalTokens=1500000 (excluding disabled Claude), got %d", vm.UsageTotalTokens)
	}
	if len(vm.QuotaRows) != 1 || !vm.QuotaRows[0].SummaryPrimaryWarn {
		t.Fatalf("expected 1 exhausted Codex quota row with warn=true, got %+v", vm.QuotaRows)
	}
	if len(vm.CliVersions) != 2 {
		t.Fatalf("expected 2 CliVersions, got %d", len(vm.CliVersions))
	}
	codexCli := vm.CliVersions[0]
	if !codexCli.DefaultModeDanger || codexCli.GetDefaultModeLabel().GetKey() != text.KeyModesFullAccess {
		t.Fatalf("expected Codex DefaultModeDanger=true full_access, got %+v", codexCli)
	}
	var hasFullChoice bool
	for _, mc := range codexCli.GetDefaults().GetModeChoices() {
		if mc.Mode == v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS && mc.Danger {
			hasFullChoice = true
		}
	}
	if !hasFullChoice {
		t.Fatalf("expected FULL_ACCESS danger option in Codex defaults")
	}
	if vm.Connection.GetRowValueText().GetKey() != text.KeyTransportRowRelayMs {
		t.Fatalf("expected direct row relay_ms, got %+v", vm.Connection.GetRowValueText())
	}
	if !vm.AttachmentStorage.SummaryWarn || vm.AttachmentStorage.ReservePercent == 0 || vm.AttachmentStorage.GetSummaryText().GetKey() != text.KeySettingsStorageSummaryReserve {
		t.Fatalf("expected attachment storage reserve warn, got %+v", vm.AttachmentStorage)
	}
	if vm.ProjectCount != 1 {
		t.Fatalf("expected ProjectCount=1 excluding hidden, got %d", vm.ProjectCount)
	}
	if vm.Management.GetExplanationText().GetKey() != text.KeySettingsRemoveNonAuthorityFooter {
		t.Fatalf("expected non-authority explanation footer, got %+v", vm.Management.GetExplanationText())
	}

	projVM := BuildProject(st, "", true, 0, "1234567890abcdef")
	if projVM.GetManage().GetProjectCount() != 1 || projVM.GetManage().GetFilterMachineId() != "1234567890abcdef" {
		t.Fatalf("expected filtered ManageProjects count=1, got %+v", projVM.GetManage())
	}

	// Verify loading placeholders ("—") when usage snapshots and image storage stats haven't arrived yet.
	clear(st.UsageSnapshots)
	loadingVM := BuildMachineSettings(st, sv, nil, "1234567890abcdef")
	if loadingVM.GetUsageEmptyText().GetLiteral() != "—" {
		t.Fatalf("expected UsageEmptyText='—' while snapshots missing, got %+v", loadingVM.GetUsageEmptyText())
	}
	if loadingVM.GetAttachmentStorage().GetSummaryText().GetLiteral() != "—" {
		t.Fatalf("expected AttachmentStorage.SummaryText='—' while storage missing, got %+v", loadingVM.GetAttachmentStorage().GetSummaryText())
	}

	// Verify Step 2 core fields on Settings / Devices / ConnCheck / MachineSettings.
	sv.DevicesLoadFailed = true
	sv.DevicesErrorText = text.Plain("devices rpc failed")
	sv.HasHandoffOffer = true
	sv.RecoveryStatusStale = true
	sv.LeaseEndMs = now + 600_000
	sv.NetworkAvailable = true
	sv.EverOnline = true
	sv.ConnectFailures = 2
	sv.ConnectionStartedAtMs = now - 5_000
	sv.ConnectionStep = text.Plain("step")
	sv.DirectReadyMachines = map[string]bool{"1234567890abcdef": true}
	sv.Devices = append(sv.Devices, &world.SyncDevice{DeviceID: "1234567890abcdef", Kind: v1.DeviceKind_DEVICE_KIND_MACHINE, Trusted: true, Registered: true})
	setVM := BuildSettings(st, sv, nil)
	if !setVM.Security.HasHandoffOffer || !setVM.Security.RecoveryStatusStale || setVM.Security.LeaseEndMs != now+600_000 {
		t.Fatalf("unexpected SecuritySummaryView fields: %+v", setVM.Security)
	}
	if !setVM.ConnCheck.NetworkAvailable || !setVM.ConnCheck.EverOnline || setVM.ConnCheck.Failures != 2 || setVM.ConnCheck.ConnStartedAtMs != now-5_000 {
		t.Fatalf("unexpected ConnCheckSummaryView fields: %+v", setVM.ConnCheck)
	}
	if len(setVM.Machines) == 0 || !setVM.Machines[0].InDeviceDirectory || !setVM.Machines[0].DirectReady || setVM.Machines[0].Disambiguator != "" {
		t.Fatalf("expected InDeviceDirectory, DirectReady, and no Disambiguator on a uniquely named machine card, got %+v", setVM.Machines)
	}
	devVM := BuildDevices(st, sv, nil)
	if !devVM.LoadFailed || devVM.GetErrorText().GetLiteral() != "devices rpc failed" || devVM.LeaseEndMs != now+600_000 {
		t.Fatalf("unexpected DevicesViewModel fields: %+v", devVM)
	}
	if !loadingVM.AppOnline {
		t.Fatalf("expected MachineSettingsViewModel.AppOnline=true when connected")
	}
}

func TestBuildSettingsHandoffCandidates(t *testing.T) {
	st := NewState(nil)
	st.World.Connection = lists.ConnConnected
	key := make([]byte, 32)
	key[0] = 7
	sv := &world.SyncState{
		SelfDeviceID:      "self-1111",
		AuthorityDeviceID: "self-1111",
		IsManager:         true,
		SecurityLoaded:    true,
		Devices: []*world.SyncDevice{
			{
				DeviceID:   "self-1111",
				Kind:       v1.DeviceKind_DEVICE_KIND_CLIENT,
				Trusted:    true,
				LastSeenMs: 500,
				Meta:       &v1.DeviceMeta{Name: "Manager", Platform: "android", Description: "Android 16 · Pixel 9", HandoffRecoveryEd: key},
			},
			{
				DeviceID:   "machine-2222",
				Kind:       v1.DeviceKind_DEVICE_KIND_MACHINE,
				Trusted:    true,
				LastSeenMs: 450,
				Meta:       &v1.DeviceMeta{Name: "Box", HandoffRecoveryEd: key},
			},
			{
				DeviceID:   "client-untrusted",
				Kind:       v1.DeviceKind_DEVICE_KIND_CLIENT,
				Trusted:    false,
				LastSeenMs: 420,
				Meta:       &v1.DeviceMeta{Name: "Revoked", Platform: "android", HandoffRecoveryEd: key},
			},
			{
				DeviceID:   "client-no-offer",
				Kind:       v1.DeviceKind_DEVICE_KIND_CLIENT,
				Trusted:    true,
				LastSeenMs: 400,
				Meta:       &v1.DeviceMeta{Name: "NoOffer", Platform: "ios", Description: "iOS 18 · iPhone 16"},
			},
			{
				DeviceID:   "client-aaaa",
				Kind:       v1.DeviceKind_DEVICE_KIND_CLIENT,
				Trusted:    true,
				LastSeenMs: 300,
				Meta:       &v1.DeviceMeta{Platform: "android", Description: "Android 16 · Pixel 9", HandoffRecoveryEd: key},
			},
			{
				DeviceID:   "client-bbbb",
				Kind:       v1.DeviceKind_DEVICE_KIND_CLIENT,
				Trusted:    true,
				LastSeenMs: 250,
				Meta:       &v1.DeviceMeta{Platform: "ios", Description: "iOS 18 · iPhone 15", HandoffRecoveryEd: key},
			},
			{
				DeviceID:   "client-cccc",
				Kind:       v1.DeviceKind_DEVICE_KIND_CLIENT,
				Trusted:    true,
				LastSeenMs: 200,
				Meta:       &v1.DeviceMeta{Platform: "web", Description: "Chrome on macOS", HandoffRecoveryEd: key},
			},
			{
				DeviceID:   "client-dddd",
				Kind:       v1.DeviceKind_DEVICE_KIND_CLIENT,
				Trusted:    true,
				LastSeenMs: 150,
				Meta:       &v1.DeviceMeta{Platform: "web", HandoffRecoveryEd: key},
			},
			{
				DeviceID:   "client-eeee",
				Kind:       v1.DeviceKind_DEVICE_KIND_CLIENT,
				Trusted:    true,
				LastSeenMs: 120,
				Meta:       &v1.DeviceMeta{Platform: "web", HandoffRecoveryEd: key},
			},
			{
				DeviceID:   "client-ffff",
				Kind:       v1.DeviceKind_DEVICE_KIND_CLIENT,
				Trusted:    true,
				LastSeenMs: 80,
				Meta:       &v1.DeviceMeta{HandoffRecoveryEd: key},
			},
			{
				DeviceID:   "client-9999",
				Kind:       v1.DeviceKind_DEVICE_KIND_CLIENT,
				Trusted:    true,
				LastSeenMs: 50,
				Meta:       &v1.DeviceMeta{Name: "Work Phone", Platform: "android", Description: "Android 16 · Pixel 9", HandoffRecoveryEd: key},
			},
		},
	}

	vm := BuildSettings(st, sv, nil)
	got := vm.GetSecurity().GetHandoffCandidates()
	if len(got) != 7 {
		t.Fatalf("expected 7 trusted handoff candidates, got %d: %+v", len(got), got)
	}
	// client-aaaa shares base "Android · Pixel 9" with self-1111 (named) and client-9999 (named) -> disambiguated with last4 (no ellipsis).
	if got[0].DeviceId != "client-aaaa" || got[0].DisplayName != "Android · Pixel 9 · aaaa" || got[0].GetDisplayNameText().GetSeparatorLiteral() != " · " {
		t.Fatalf("unexpected candidate[0]: %+v", got[0])
	}
	// client-bbbb is unique iOS model -> "iPhone 15".
	if got[1].DeviceId != "client-bbbb" || got[1].DisplayName != "iPhone 15" || got[1].GetDisplayNameText().GetLiteral() != "iPhone 15" {
		t.Fatalf("unexpected candidate[1]: %+v", got[1])
	}
	// client-cccc is unique Web description -> "Chrome · macOS".
	if got[2].DeviceId != "client-cccc" || got[2].DisplayName != "Chrome · macOS" || got[2].GetDisplayNameText().GetLiteral() != "Chrome · macOS" {
		t.Fatalf("unexpected candidate[2]: %+v", got[2])
	}
	// client-dddd and client-eeee are two unnamed Web browsers -> KeySecurityBrowser + " · dddd" / " · eeee".
	if got[3].DeviceId != "client-dddd" || len(got[3].GetDisplayNameText().GetJoin()) != 2 || got[3].GetDisplayNameText().GetJoin()[0].GetKey() != text.KeySecurityBrowser || got[3].GetDisplayNameText().GetJoin()[1].GetLiteral() != "dddd" {
		t.Fatalf("unexpected candidate[3]: %+v", got[3])
	}
	if got[4].DeviceId != "client-eeee" || len(got[4].GetDisplayNameText().GetJoin()) != 2 || got[4].GetDisplayNameText().GetJoin()[0].GetKey() != text.KeySecurityBrowser || got[4].GetDisplayNameText().GetJoin()[1].GetLiteral() != "eeee" {
		t.Fatalf("unexpected candidate[4]: %+v", got[4])
	}
	// client-ffff has empty platform -> unique KeySecurityClient (must not collide with Browser).
	if got[5].DeviceId != "client-ffff" || got[5].GetDisplayNameText().GetKey() != text.KeySecurityClient {
		t.Fatalf("unexpected candidate[5]: %+v", got[5])
	}
	// client-9999 has custom Name -> never gets suffix.
	if got[6].DeviceId != "client-9999" || got[6].DisplayName != "Work Phone" || got[6].GetDisplayNameText().GetLiteral() != "Work Phone" {
		t.Fatalf("unexpected candidate[6]: %+v", got[6])
	}
}

func TestOfflineNetworkDoesNotShowRemovedBannerOrRemovedMachines(t *testing.T) {
	now := int64(1_700_000_000_000)
	unlinkedTrue := true
	st := NewState(nil)
	st.Cal = lists.Calendar{NowMs: now}
	st.World.Connection = lists.ConnNoNetwork
	st.World.MachinesListed = true
	st.World.Unlinked = &unlinkedTrue
	st.Removed = false
	st.World.Machines = []*lists.Machine{
		{
			ID:         "m-active",
			Online:     false,
			Unlinked:   &unlinkedTrue,
			Archived:   false,
			LastSeenMs: now - 60_000,
			Meta:       &v1.MachineMeta{Hostname: "WorkMac", ProtocolVersion: 17},
		},
		{
			ID:       "m-archived",
			Online:   false,
			Unlinked: &unlinkedTrue,
			Archived: true,
			Meta:     &v1.MachineMeta{Hostname: "OldMac", ProtocolVersion: 17},
		},
	}
	sv := &world.SyncState{
		SelfDeviceID: "c1",
		Unlinked:     true,
		Removed:      false,
	}

	svm := BuildSettings(st, sv, nil)
	if !svm.Unlinked {
		t.Fatalf("expected Settings.Unlinked=true while network is down")
	}
	if svm.GetUnlinkedBannerText() != nil {
		t.Fatalf("offline network must not show removed-from-account banner, got %+v", svm.GetUnlinkedBannerText())
	}
	byID := map[string]*corev1.MachineListCardView{}
	for _, mc := range svm.Machines {
		byID[mc.MachineId] = mc
	}
	if !byID["m-active"].Member || byID["m-active"].GetStatusText().GetKey() == text.KeySettingsStatusRemoved {
		t.Fatalf("active machine must not be marked removed while phone is offline: %+v", byID["m-active"])
	}
	if byID["m-archived"].Member || byID["m-archived"].GetStatusText().GetKey() != text.KeySettingsStatusRemoved {
		t.Fatalf("archived machine must still be marked removed: %+v", byID["m-archived"])
	}

	mvm := BuildMachineSettings(st, sv, nil, "m-active")
	if !mvm.Member || mvm.GetRemovedBanner() != nil {
		t.Fatalf("active machine detail must not show removed banner while phone is offline: %+v", mvm)
	}

	// Contrast: when the device is actually removed from the account, the banner IS shown.
	st.Removed = true
	sv.Removed = true
	svmRemoved := BuildSettings(st, sv, nil)
	if svmRemoved.GetUnlinkedBannerText().GetKey() != text.KeySettingsThisDeviceWasRemovedFromTheAccount {
		t.Fatalf("removed account must show removed-from-account banner, got %+v", svmRemoved.GetUnlinkedBannerText())
	}
}

// Disambiguator (last 4 chars without ellipsis) appears only when two computers share a name.
func TestMachineCardDisambiguatorOnlyForDuplicateNames(t *testing.T) {
	now := int64(1_791_633_600_000)
	st := NewState(nil)
	st.Cal = lists.Calendar{NowMs: now, TodayStartMs: now - 10_800_000}
	st.World.Connection = lists.ConnConnected
	st.World.ClientVersion = 17
	st.World.MachinesListed = true
	mk := func(id, host string) *lists.Machine {
		return &lists.Machine{ID: id, Online: true, Meta: &v1.MachineMeta{
			Hostname: host, ProtocolVersion: 17,
			Quotas: []*v1.AgentQuota{{
				Agent: v1.Agent_AGENT_CLAUDE, State: v1.QuotaState_QUOTA_STATE_OK, ObservedMs: now - 60_000,
				Windows: []*v1.QuotaWindow{{Name: "seven_day", UsedPercent: ptrF64(30), ResetsAtMs: now + 3_600_000}},
			}},
		}}
	}
	st.World.Machines = []*lists.Machine{
		mk("aaaaaaaa111174c7", "Mac"),
		mk("bbbbbbbb2222a91e", "Mac"),
		mk("cccccccc33333333", "Studio"),
	}
	vm := BuildSettings(st, &world.SyncState{}, nil)
	if len(vm.Machines) != 3 {
		t.Fatalf("want 3 machine cards, got %d", len(vm.Machines))
	}
	wantDisambig := map[string]string{
		"aaaaaaaa111174c7": "74c7",
		"bbbbbbbb2222a91e": "a91e",
		"cccccccc33333333": "",
	}
	for _, c := range vm.Machines {
		if c.Disambiguator != wantDisambig[c.MachineId] {
			t.Fatalf("%s (%s): disambiguator=%q want %q", c.MachineId, c.Name, c.Disambiguator, wantDisambig[c.MachineId])
		}
		if len(c.QuotaItems) != 1 || c.QuotaItems[0].RemainingPercent != 70 || c.QuotaItems[0].GetValueText().GetKey() != text.KeyQuotaLeft {
			t.Fatalf("%s: unexpected quota items: %+v", c.MachineId, c.QuotaItems)
		}
	}

	// NewSession machine choices use the exact same duplicate-name disambiguator rule.
	ns := BuildNewSession(st, "")
	for _, ch := range ns.GetModelPanel().GetMachineChoices() {
		if ch.Disambiguator != wantDisambig[ch.MachineId] {
			t.Fatalf("new_session choice %s: disambiguator=%q want %q", ch.MachineId, ch.Disambiguator, wantDisambig[ch.MachineId])
		}
	}

	t.Run("case-insensitive trimmed names and last-4 suffix collision extension", func(t *testing.T) {
		st2 := NewState(nil)
		st2.Cal = lists.Calendar{NowMs: now}
		st2.World.Connection = lists.ConnConnected
		st2.World.ClientVersion = 17
		st2.World.Machines = []*lists.Machine{
			{ID: "aaaa1234", Online: true, Meta: &v1.MachineMeta{Hostname: "MacBook", ProtocolVersion: 17}},
			{ID: "bbbb1234", Online: true, Meta: &v1.MachineMeta{Hostname: "  macbook  ", ProtocolVersion: 17}},
			{ID: "xxab9999", Online: true, Meta: &v1.MachineMeta{Hostname: "Studio", ProtocolVersion: 17}},
			{ID: "yyab9999", Online: true, Meta: &v1.MachineMeta{Hostname: "STUDIO", ProtocolVersion: 17}},
			{ID: "unique01", Online: true, Meta: &v1.MachineMeta{Hostname: "UniqueBox", ProtocolVersion: 17}},
		}
		want := map[string]string{
			"aaaa1234": "a1234",
			"bbbb1234": "b1234",
			"xxab9999": "xab9999",
			"yyab9999": "yab9999",
			"unique01": "",
		}
		vm2 := BuildSettings(st2, &world.SyncState{}, nil)
		for _, c := range vm2.Machines {
			if c.Disambiguator != want[c.MachineId] {
				t.Fatalf("settings %s: disambiguator=%q want %q", c.MachineId, c.Disambiguator, want[c.MachineId])
			}
			if want[c.MachineId] != "" && c.GetDisambiguatorA11Y().GetKey() != text.KeySettingsMachineDisambiguatorA11Y {
				t.Fatalf("settings %s: expected disambiguator_a11y key, got %+v", c.MachineId, c.DisambiguatorA11Y)
			}
			if want[c.MachineId] == "" && c.DisambiguatorA11Y != nil {
				t.Fatalf("settings %s: expected nil disambiguator_a11y, got %+v", c.MachineId, c.DisambiguatorA11Y)
			}
		}
		ns2 := BuildNewSession(st2, "")
		for _, ch := range ns2.GetModelPanel().GetMachineChoices() {
			if ch.Disambiguator != want[ch.MachineId] {
				t.Fatalf("new_session %s: disambiguator=%q want %q", ch.MachineId, ch.Disambiguator, want[ch.MachineId])
			}
		}
	})
}

func TestMachineCardPlanBStatesAndConnCheckFooter(t *testing.T) {
	now := int64(1_791_633_600_000)

	t.Run("protocol mismatch distinguishes daemon-older vs client-older", func(t *testing.T) {
		cases := []struct {
			name        string
			machineVer  uint32
			wantAlert   string
			wantDirSign int32
		}{
			{"daemon older", 15, text.KeySettingsMachineSubVersionMismatch, -1},
			{"client older", 19, text.KeySettingsMachineSubVersionMismatchClient, 1},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				st := NewState(nil)
				st.Cal = lists.Calendar{NowMs: now}
				st.World.Connection = lists.ConnConnected
				st.World.ClientVersion = 17
				st.World.Machines = []*lists.Machine{{
					ID: "m1", Online: true,
					Meta: &v1.MachineMeta{Hostname: "Lab", ProtocolVersion: tc.machineVer},
				}}
				vm := BuildSettings(st, &world.SyncState{}, nil)
				c := vm.Machines[0]
				if c.ProtocolMismatch != tc.wantDirSign || c.Dimmed {
					t.Fatalf("mismatch=%d dimmed=%v want (%d, false)", c.ProtocolMismatch, c.Dimmed, tc.wantDirSign)
				}
				if len(c.Alerts) != 1 || c.Alerts[0].Kind != corev1.MachineAlertKind_MACHINE_ALERT_KIND_PROTOCOL_MISMATCH || c.Alerts[0].GetText().GetKey() != tc.wantAlert {
					t.Fatalf("unexpected alerts: %+v", c.Alerts)
				}
				if len(c.QuotaItems) != 0 || c.DirectActive {
					t.Fatalf("mismatched machine must not show quotas or direct_active: %+v", c)
				}

				mvm := BuildMachineSettings(st, &world.SyncState{}, nil, "m1")
				if mvm.GetOfflineOrMismatchBanner().GetKey() != tc.wantAlert {
					t.Fatalf("MachineSettings banner=%q want %q", mvm.GetOfflineOrMismatchBanner().GetKey(), tc.wantAlert)
				}
			})
		}
	})

	t.Run("quota exhausted sets 0 percent, exhausted=true, warn=true, and exhausted value_text", func(t *testing.T) {
		st := NewState(nil)
		st.Cal = lists.Calendar{NowMs: now}
		st.World.Connection = lists.ConnConnected
		st.World.ClientVersion = 17
		st.World.Machines = []*lists.Machine{{
			ID: "m-ex", Online: true,
			Meta: &v1.MachineMeta{
				Hostname: "Work", ProtocolVersion: 17,
				Quotas: []*v1.AgentQuota{
					{
						Agent: v1.Agent_AGENT_CODEX, State: v1.QuotaState_QUOTA_STATE_OK, ObservedMs: now - 30_000,
						Windows: []*v1.QuotaWindow{{Name: "primary", UsedPercent: ptrF64(88), ResetsAtMs: now + 3_600_000}},
					},
					{
						Agent: v1.Agent_AGENT_CLAUDE, State: v1.QuotaState_QUOTA_STATE_OK, ObservedMs: now - 30_000,
						Windows: []*v1.QuotaWindow{{Name: "seven_day", UsedPercent: ptrF64(100), ResetsAtMs: now + 3_600_000}},
					},
				},
			},
		}}
		vm := BuildSettings(st, &world.SyncState{}, nil)
		qs := vm.Machines[0].QuotaItems
		if len(qs) != 2 {
			t.Fatalf("want 2 quota items, got %+v", qs)
		}
		if qs[0].Agent != v1.Agent_AGENT_CODEX || qs[0].RemainingPercent != 12 || !qs[0].Warn || qs[0].Exhausted || qs[0].GetValueText().GetKey() != text.KeyQuotaLeft {
			t.Fatalf("unexpected codex quota item: %+v", qs[0])
		}
		if qs[1].Agent != v1.Agent_AGENT_CLAUDE || qs[1].RemainingPercent != 0 || !qs[1].Warn || !qs[1].Exhausted || qs[1].GetValueText().GetKey() != text.KeyQuotaExhaustedQuota {
			t.Fatalf("unexpected claude exhausted quota item: %+v", qs[1])
		}
	})

	t.Run("direct_active and connCheck footer reflect active routes", func(t *testing.T) {
		directReport := &corev1.DirectRouteReport{
			Enabled:   true,
			Capable:   true,
			State:     corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED,
			ReadRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT,
			FileRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT,
		}
		relayReport := &corev1.DirectRouteReport{
			Enabled:   true,
			Capable:   true,
			State:     corev1.DirectRouteState_DIRECT_ROUTE_STATE_NONE,
			Reason:    corev1.DirectRouteReason_DIRECT_ROUTE_REASON_NO_TRAVERSAL,
			ReadRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_RELAY,
			FileRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_RELAY,
		}
		backoffReport := &corev1.DirectRouteReport{
			Enabled:   true,
			Capable:   true,
			State:     corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED,
			ReadRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_RELAY,
			FileRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_RELAY,
			ReadSlow:  true,
		}
		fileOnlyReport := &corev1.DirectRouteReport{
			Enabled:   true,
			Capable:   true,
			State:     corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED,
			ReadRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_RELAY,
			FileRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT,
		}

		cases := []struct {
			name          string
			reports       map[string]*corev1.DirectRouteReport
			directReady   bool
			m1ProtoVer    uint32
			wantDirect    map[string]bool
			wantFooterKey string
		}{
			{
				name:          "all online direct",
				reports:       map[string]*corev1.DirectRouteReport{"m1": directReport, "m2": directReport},
				wantDirect:    map[string]bool{"m1": true, "m2": true, "m3": false},
				wantFooterKey: text.KeyConnectionLatencyFooterDirect,
			},
			{
				name:          "mixed direct and relay",
				reports:       map[string]*corev1.DirectRouteReport{"m1": directReport, "m2": relayReport},
				wantDirect:    map[string]bool{"m1": true, "m2": false, "m3": false},
				wantFooterKey: text.KeyConnectionLatencyFooterMixed,
			},
			{
				name:          "all relay",
				reports:       map[string]*corev1.DirectRouteReport{"m1": relayReport, "m2": relayReport},
				wantDirect:    map[string]bool{"m1": false, "m2": false, "m3": false},
				wantFooterKey: text.KeyConnectionLatencyIsOneRoundTripTheServerRowMeasures,
			},
			{
				name:          "direct ready channel in read backoff uses relay",
				reports:       map[string]*corev1.DirectRouteReport{"m1": backoffReport, "m2": backoffReport},
				directReady:   true,
				wantDirect:    map[string]bool{"m1": false, "m2": false, "m3": false},
				wantFooterKey: text.KeyConnectionLatencyIsOneRoundTripTheServerRowMeasures,
			},
			{
				name:          "direct ready without route report stays relay",
				reports:       map[string]*corev1.DirectRouteReport{},
				directReady:   true,
				wantDirect:    map[string]bool{"m1": false, "m2": false, "m3": false},
				wantFooterKey: text.KeyConnectionLatencyIsOneRoundTripTheServerRowMeasures,
			},
			{
				name:          "file-only direct does not enable direct_active or direct footer",
				reports:       map[string]*corev1.DirectRouteReport{"m1": fileOnlyReport, "m2": fileOnlyReport},
				wantDirect:    map[string]bool{"m1": false, "m2": false, "m3": false},
				wantFooterKey: text.KeyConnectionLatencyIsOneRoundTripTheServerRowMeasures,
			},
			{
				name:          "protocol mismatch excludes machine from direct_active and direct footer",
				reports:       map[string]*corev1.DirectRouteReport{"m1": directReport, "m2": relayReport},
				m1ProtoVer:    15,
				wantDirect:    map[string]bool{"m1": false, "m2": false, "m3": false},
				wantFooterKey: text.KeyConnectionLatencyIsOneRoundTripTheServerRowMeasures,
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				st := NewState(nil)
				st.Cal = lists.Calendar{NowMs: now}
				st.World.Connection = lists.ConnConnected
				st.World.ClientVersion = 17
				m1Ver := tc.m1ProtoVer
				if m1Ver == 0 {
					m1Ver = 17
				}
				st.World.Machines = []*lists.Machine{
					{ID: "m1", Online: true, Meta: &v1.MachineMeta{Hostname: "Alpha", ProtocolVersion: m1Ver}},
					{ID: "m2", Online: true, Meta: &v1.MachineMeta{Hostname: "Beta", ProtocolVersion: 17}},
					{ID: "m3", Online: false, Meta: &v1.MachineMeta{Hostname: "Gamma", ProtocolVersion: 17}},
				}
				st.DirectReportFn = func(id string) (bool, *corev1.DirectRouteReport, bool) {
					r, ok := tc.reports[id]
					return true, r, ok
				}
				syncView := &world.SyncState{}
				if tc.directReady {
					syncView.DirectReadyMachines = map[string]bool{
						"m1": true,
						"m2": true,
					}
				}
				vm := BuildSettings(st, syncView, nil)
				if vm.GetConnCheck().GetFooterText().GetKey() != tc.wantFooterKey {
					t.Fatalf("footer=%q want %q", vm.GetConnCheck().GetFooterText().GetKey(), tc.wantFooterKey)
				}
				for _, m := range vm.Machines {
					if m.DirectActive != tc.wantDirect[m.MachineId] {
						t.Fatalf("%s: DirectActive=%v want %v", m.MachineId, m.DirectActive, tc.wantDirect[m.MachineId])
					}
					if m.DirectActive {
						if len(m.MetaSegments) != 2 || m.MetaSegments[1].Icon != corev1.MachineCardMetaIcon_MACHINE_CARD_META_ICON_DIRECT || m.MetaSegments[1].GetText().GetKey() != text.KeyTransportDirect {
							t.Fatalf("%s: expected direct meta segment, got %+v", m.MachineId, m.MetaSegments)
						}
					}
					hasRelay := false
					for _, seg := range m.MetaSegments {
						if seg.GetText().GetKey() == text.KeyTransportCardRelay {
							hasRelay = true
						}
					}
					wantRelay := m.MachineId != "m3" && !m.DirectActive && !(m.MachineId == "m1" && tc.m1ProtoVer != 0)
					if hasRelay != wantRelay {
						t.Fatalf("%s: relay meta segment=%v want %v (%+v)", m.MachineId, hasRelay, wantRelay, m.MetaSegments)
					}
				}
			})
		}
	})

	t.Run("appearance, meta segments, and unsupported connCheck footer come from Core", func(t *testing.T) {
		unsupportedReport := &corev1.DirectRouteReport{
			Enabled: false,
			Capable: false,
			State:   corev1.DirectRouteState_DIRECT_ROUTE_STATE_UNSUPPORTED,
		}
		for _, tc := range []struct {
			platform string
			wantKey  string
		}{
			{"ios", text.KeyTransportConnFooterUnsupportedApp},
			{"android", text.KeyTransportConnFooterUnsupportedApp},
			{"Mozilla/5.0", text.KeyTransportConnFooterUnsupportedBrowser},
		} {
			st := NewState(nil)
			st.Cal = lists.Calendar{NowMs: now}
			st.World.Connection = lists.ConnConnected
			st.World.ClientVersion = 17
			st.World.Machines = []*lists.Machine{
				{ID: "m-on", Online: true, Meta: &v1.MachineMeta{Hostname: "On", ProtocolVersion: 17}},
				{ID: "m-off", Online: false, LastSeenMs: now - 3_600_000, Meta: &v1.MachineMeta{Hostname: "Off", ProtocolVersion: 17}},
				{ID: "m-rem", Online: false, Archived: true, Meta: &v1.MachineMeta{Hostname: "Rem", ProtocolVersion: 17}},
			}
			st.PlatformAgentFn = func() string { return tc.platform }
			st.DirectReportFn = func(id string) (bool, *corev1.DirectRouteReport, bool) {
				if id == "m-on" {
					return false, unsupportedReport, true
				}
				return false, nil, false
			}
			vm := BuildSettings(st, &world.SyncState{}, nil)
			if got := vm.GetConnCheck().GetFooterText(); got.GetKey() != text.KeyCommonSentenceJoin || len(got.Args) != 2 || got.Args[1].GetTextValue().GetKey() != tc.wantKey {
				t.Fatalf("platform=%s: unexpected unsupported footer %+v", tc.platform, got)
			}
			byID := make(map[string]*corev1.MachineListCardView, len(vm.Machines))
			for _, m := range vm.Machines {
				byID[m.MachineId] = m
			}
			if byID["m-on"].Appearance != corev1.MachineCardAppearance_MACHINE_CARD_APPEARANCE_NORMAL {
				t.Fatalf("m-on appearance=%v", byID["m-on"].Appearance)
			}
			if byID["m-off"].Appearance != corev1.MachineCardAppearance_MACHINE_CARD_APPEARANCE_OFFLINE {
				t.Fatalf("m-off appearance=%v", byID["m-off"].Appearance)
			}
			if byID["m-rem"].Appearance != corev1.MachineCardAppearance_MACHINE_CARD_APPEARANCE_REMOVED || len(byID["m-rem"].MetaSegments) != 2 || byID["m-rem"].MetaSeparator != "·" {
				t.Fatalf("m-rem appearance/meta=%+v", byID["m-rem"])
			}
		}
	})
}
