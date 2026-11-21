package screens

import (
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
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
			wantQuotaKey:    text.KeySettingsThisDeviceWasRemovedFromTheAccount,
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
			sv := &corev1.SyncStateView{Unlinked: tc.unlinked}

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

	unlinkedTrue := true
	st.World.Machines = append(st.World.Machines, &lists.Machine{
		ID:       "m-removed",
		Unlinked: &unlinkedTrue,
		Meta:     &v1.MachineMeta{Hostname: "ArchivedMac", ProtocolVersion: 16},
	})

	sv := &corev1.SyncStateView{
		SelfDeviceId:      "c-self",
		AuthorityDeviceId: "c-auth",
		IsManager:         false,
		RecoveryPending:   true,
		Devices: []*corev1.SyncDeviceView{
			{DeviceId: "c-self", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, Trusted: true},
			{DeviceId: "c-auth", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, Trusted: true, Meta: &v1.DeviceMeta{Name: "Pixel 9"}},
			{DeviceId: "c-pending", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, Trusted: false},
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
	if on.StatusDot != corev1.MachineStatusDot_MACHINE_STATUS_DOT_GREEN_LIVE || len(on.SubtitleParts) != 3 {
		t.Fatalf("unexpected online card: dot=%v parts=%+v", on.StatusDot, on.SubtitleParts)
	}
	if on.SubtitleParts[0].GetText().GetKey() != text.KeySettingsMachineSubDiskAlert || !on.SubtitleParts[0].Warn {
		t.Fatalf("expected disk warn part first, got %+v", on.SubtitleParts[0])
	}
	if !on.SubtitleParts[1].Warn || on.SubtitleParts[2].Warn {
		t.Fatalf("expected Codex 18%% warn=true and Claude 54%% warn=false, got %+v", on.SubtitleParts)
	}

	mm := byID["m-mismatch"]
	if !mm.StatusWarn || mm.StatusDot != corev1.MachineStatusDot_MACHINE_STATUS_DOT_ORANGE_STATIC || mm.GetStatusText().GetKey() != text.KeySettingsStatusNeedsUpdate {
		t.Fatalf("unexpected mismatch card: %+v", mm)
	}

	off := byID["m-offline"]
	if off.StatusDot != corev1.MachineStatusDot_MACHINE_STATUS_DOT_GRAY_STATIC || len(off.SubtitleParts) != 0 || off.GetStatusText().GetKey() != text.KeyTimeHoursAgo {
		t.Fatalf("unexpected offline card (must omit prefix & stale quotas): %+v", off)
	}

	rem := byID["m-removed"]
	if rem.GetStatusText().GetKey() != text.KeySettingsStatusRemoved || len(rem.SubtitleParts) != 1 || rem.SubtitleParts[0].Warn {
		t.Fatalf("unexpected removed card: %+v", rem)
	}

	// Notification summary table-driven check
	notifCases := []struct {
		name      string
		prefs     NotificationPrefs
		wantKey   string
		wantWarn  bool
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

	sv := &corev1.SyncStateView{
		SelfDeviceId:      "c-self",
		AuthorityDeviceId: "c-auth",
		IsManager:         false,
		Devices: []*corev1.SyncDeviceView{
			{DeviceId: "c-auth", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, Trusted: true, Meta: &v1.DeviceMeta{Name: "Pixel 9"}},
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
}
