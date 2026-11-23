package machine

import (
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func keyOf(t *corev1.Text) string {
	if t == nil {
		return ""
	}
	if t.Key != "" {
		return t.Key
	}
	return t.Literal
}

func TestBinaryVsDecimalSizes(t *testing.T) {
	// Memory/attachment binary: 11.2 * 1024^3
	mem := FormatBinarySize(uint64(2 * gibBytes))
	if keyOf(mem) != text.KeyMachineStatusSizeGb {
		t.Fatalf("expected size_gb for 2 GiB, got %v", mem)
	}
	small := FormatAttachmentSize(50 * 1024)
	if keyOf(small) != text.KeySettingsLessThan01Mib {
		t.Fatalf("expected less_than_0_1_mib for 50 KiB, got %v", small)
	}
	zero := FormatAttachmentSize(0)
	if keyOf(zero) != text.KeyMachineStatusSizeMb {
		t.Fatalf("expected size_mb 0 for 0 bytes, got %v", zero)
	}

	// Disk decimal: 9_960_000_000 -> "10" GB (>= 9.95 rounds without trailing .0)
	d := FormatDecimalSize(9_960_000_000)
	if arg := d.Args[0].Value.(*corev1.TextArg_StrValue).StrValue; arg != "10" {
		t.Fatalf("expected 9.96e9 decimal to format as '10', got %q", arg)
	}
}

func TestRingsAndCards(t *testing.T) {
	load := 3.2
	pos := 0.25
	swap := uint64(1300 * mibBytes)
	agentB := uint64(4_800_000_000)
	st := &v1.MachineStatus{
		ObservedMs:             20_000,
		CpuPercent:             23.4,
		Cores:                  10,
		LoadOne:                &load,
		MemoryUsedBytes:        8 * gibBytes,
		MemoryTotalBytes:       16 * gibBytes,
		MemoryPressure:         v1.ResourcePressure_RESOURCE_PRESSURE_WARNING,
		MemoryPressurePosition: &pos,
		SwapUsedBytes:          &swap,
		AgentBytes:             &agentB,
		Disk: &v1.DiskStatus{
			TotalBytes:     100 * gbBytes,
			AvailableBytes: 20 * gbBytes,
			Pressure:       v1.ResourcePressure_RESOURCE_PRESSURE_CRITICAL,
		},
	}

	offlineRings := BuildRings(false, st)
	if offlineRings[0].HasValue || keyOf(offlineRings[0].ValueText) != "—" {
		t.Fatalf("offline ring should be blank dash, got %+v", offlineRings[0])
	}

	rings := BuildRings(true, st)
	if !rings[0].HasValue || keyOf(rings[0].ValueText) != "23%" || rings[0].Tone != corev1.MachineRingTone_MACHINE_RING_TONE_NORMAL {
		t.Fatalf("unexpected CPU ring %+v", rings[0])
	}
	if rings[1].Tone != corev1.MachineRingTone_MACHINE_RING_TONE_WARN {
		t.Fatalf("expected WARN tone on memory ring, got %v", rings[1].Tone)
	}
	if rings[2].Tone != corev1.MachineRingTone_MACHINE_RING_TONE_CRITICAL || rings[2].Fraction < 0.79 || rings[2].Fraction > 0.81 {
		t.Fatalf("expected CRITICAL tone and ~0.8 used fraction on disk ring, got %+v", rings[2])
	}

	disk := BuildDiskSection(st)
	if !disk.ShowActions || keyOf(disk.Detail) != text.KeyMachineStatusDiskCriticalHint || disk.Detail2 != nil {
		t.Fatalf("critical disk should show critical hint and hide detail2, got %+v", disk)
	}

	mem := BuildMemorySection(st, nil)
	if mem.Pressure != corev1.MachinePressureTone_MACHINE_PRESSURE_TONE_HIGH || !mem.HasPressurePosition || mem.PressurePosition != 0.25 {
		t.Fatalf("unexpected memory section %+v", mem)
	}
}

func TestStableUsageHysteresisAndChartFallback(t *testing.T) {
	rows := []*v1.ResourceUsage{
		{Id: "a", CpuPercent: 10.0, MemoryBytes: 100 * mibBytes},
		{Id: "b", CpuPercent: 11.5, MemoryBytes: 120 * mibBytes}, // within 2% CPU hysteresis -> keep "a" first
		{Id: "c", CpuPercent: 14.0, MemoryBytes: 200 * mibBytes}, // >2% above b and a -> floats to top
	}
	ordered := StableUsage([]string{"a", "b", "c"}, rows, false)
	if ordered[0].Id != "c" || ordered[1].Id != "a" || ordered[2].Id != "b" {
		t.Fatalf("unexpected hysteresis order: %s, %s, %s", ordered[0].Id, ordered[1].Id, ordered[2].Id)
	}

	// Chart falls back to single point when samples are all before curveSinceMs
	st := &v1.MachineStatus{
		ObservedMs: 50_000,
		CpuPercent: 42,
		Samples: []*v1.MachineSample{
			{TimeMs: 40_000, CpuPercent: 10},
		},
	}
	chart := BuildChart(st, true, 45_000, false, 0, true)
	if len(chart.Points) != 1 || chart.Points[0].TimeMs != 50_000 || chart.Points[0].Value != 42 {
		t.Fatalf("expected single recovery fallback point, got %+v", chart.Points)
	}
	if !chart.HasScrub || keyOf(chart.ScrubAge) != text.KeyMachineStatusAxisNow {
		t.Fatalf("expected scrub on single latest point to show axis_now, got %+v", chart)
	}
}

func TestDiskRemindersSort(t *testing.T) {
	yes := true
	machines := []*lists.Machine{
		{
			ID: "m-warn-small", Online: true, Reachable: &yes,
			Meta: &v1.MachineMeta{
				Hostname:     "WarnSmall",
				Capabilities: &v1.MachineCapabilities{MachineStatus: true},
				DiskStatus:   &v1.DiskStatus{AvailableBytes: 5_000_000_000},
			},
		},
		{
			ID: "m-crit", Online: true, Reachable: &yes,
			Meta: &v1.MachineMeta{
				Hostname:     "Crit",
				Capabilities: &v1.MachineCapabilities{MachineStatus: true},
				DiskStatus:   &v1.DiskStatus{AvailableBytes: 1_500_000_000},
			},
		},
		{
			ID: "m-ok", Online: true, Reachable: &yes,
			Meta: &v1.MachineMeta{
				Hostname:     "OK",
				Capabilities: &v1.MachineCapabilities{MachineStatus: true},
				DiskStatus:   &v1.DiskStatus{AvailableBytes: 12_000_000_000},
			},
		},
	}
	rems := BuildDiskReminders(machines)
	if len(rems) != 2 || rems[0].MachineId != "m-crit" || !rems[0].Critical || rems[1].MachineId != "m-warn-small" {
		t.Fatalf("unexpected reminders: %+v", rems)
	}
}

func TestDirectConnectionAndClientsAndUpdates(t *testing.T) {
	// Direct state with active RTT
	cg := BuildConnectionGroup(true, true, &corev1.DirectRouteReport{
		Enabled:        true,
		Capable:        true,
		State:          corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED,
		ReadRoute:      corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT,
		HasActiveRttMs: true,
		ActiveRttMs:    14,
	})
	if cg.Stage != corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_DIRECT || keyOf(cg.MetricText) != text.KeyTransportRttMs {
		t.Fatalf("unexpected direct connection view: %+v", cg)
	}

	// Failed direct shows diagnostics entry ("为什么没连上") without orange warning tone
	failed := BuildConnectionGroup(true, true, &corev1.DirectRouteReport{
		Enabled: true,
		Capable: true,
		State:   corev1.DirectRouteState_DIRECT_ROUTE_STATE_NONE,
		Reason:  corev1.DirectRouteReason_DIRECT_ROUTE_REASON_NO_TRAVERSAL,
	})
	if failed.Stage != corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_RELAY_FAILED || failed.Tone != corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED || !failed.ShowDiagnosticsEntry {
		t.Fatalf("failed direct should expose diagnostics entry with muted tone, got %+v", failed)
	}

	// Single-channel direct & connected relay match v63 5-state convergence rules.
	readOnlyDirect := BuildConnectionGroup(true, true, &corev1.DirectRouteReport{
		Enabled:   true,
		Capable:   true,
		State:     corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED,
		ReadRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT,
		FileRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_RELAY,
	})
	if keyOf(readOnlyDirect.DetailText) != text.KeyTransportUseRead {
		t.Fatalf("expected UseRead for read-only direct (with relay file), got %q", keyOf(readOnlyDirect.DetailText))
	}
	readOnlyUnspecified := BuildConnectionGroup(true, true, &corev1.DirectRouteReport{
		Enabled:   true,
		Capable:   true,
		State:     corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED,
		ReadRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT,
	})
	if keyOf(readOnlyUnspecified.DetailText) != text.KeyTransportUseRead {
		t.Fatalf("expected UseRead for read-only direct (unspecified file), got %q", keyOf(readOnlyUnspecified.DetailText))
	}
	fileOnlyDirect := BuildConnectionGroup(true, true, &corev1.DirectRouteReport{
		Enabled:   true,
		Capable:   true,
		State:     corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED,
		ReadRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_RELAY,
		FileRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT,
	})
	if keyOf(fileOnlyDirect.DetailText) != text.KeyTransportUseFile {
		t.Fatalf("expected UseFile for file-only direct, got %q", keyOf(fileOnlyDirect.DetailText))
	}
	bothDirect := BuildConnectionGroup(true, true, &corev1.DirectRouteReport{
		Enabled:   true,
		Capable:   true,
		State:     corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED,
		ReadRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT,
		FileRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT,
	})
	if bothDirect.DetailText != nil {
		t.Fatalf("expected nil DetailText when both channels are direct, got %+v", bothDirect.DetailText)
	}
	connectedRelay := BuildConnectionGroup(true, true, &corev1.DirectRouteReport{
		Enabled:   true,
		Capable:   true,
		State:     corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED,
		ReadRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_RELAY,
		FileRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_RELAY,
	})
	if connectedRelay.Stage != corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_RELAY || keyOf(connectedRelay.DetailText) != text.KeyTransportReasonChannelPending {
		t.Fatalf("expected RELAY with ChannelPending DetailText without slow fallback, got stage=%v detail=%+v", connectedRelay.Stage, connectedRelay.DetailText)
	}

	readFallback := BuildConnectionGroup(true, true, &corev1.DirectRouteReport{
		Enabled:   true,
		Capable:   true,
		State:     corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED,
		ReadRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_RELAY,
		FileRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT,
		ReadSlow:  true,
	})
	if keyOf(readFallback.DetailText) != text.KeyTransportUseFile {
		t.Fatalf("expected UseFile when file is direct and read fell back, got %q", keyOf(readFallback.DetailText))
	}

	fileFallback := BuildConnectionGroup(true, true, &corev1.DirectRouteReport{
		Enabled:   true,
		Capable:   true,
		State:     corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED,
		ReadRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT,
		FileRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_RELAY,
		FileSlow:  true,
	})
	if keyOf(fileFallback.DetailText) != text.KeyTransportUseRead {
		t.Fatalf("expected UseRead when read is direct and file fell back, got %q", keyOf(fileFallback.DetailText))
	}

	bothSlow := BuildConnectionGroup(true, true, &corev1.DirectRouteReport{
		Enabled:   true,
		Capable:   true,
		State:     corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED,
		ReadRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_RELAY,
		FileRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_RELAY,
		ReadSlow:  true,
		FileSlow:  true,
	})
	if bothSlow.Stage != corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_RELAY || keyOf(bothSlow.DetailText) != text.KeyTransportDirectSlow {
		t.Fatalf("expected DirectSlow when connected but both channels fell back, got %+v", bothSlow)
	}

	for _, tc := range []struct {
		name   string
		report *corev1.DirectRouteReport
		want   string
		diag   bool
	}{
		{"daemon_disabled", &corev1.DirectRouteReport{Enabled: true, Capable: true, DaemonDisabled: true, State: corev1.DirectRouteState_DIRECT_ROUTE_STATE_NONE, Reason: corev1.DirectRouteReason_DIRECT_ROUTE_REASON_DAEMON_DISABLED}, text.KeyTransportDetailDaemonDisabled, false},
		{"at_cap", &corev1.DirectRouteReport{Enabled: true, Capable: true, State: corev1.DirectRouteState_DIRECT_ROUTE_STATE_NONE, Reason: corev1.DirectRouteReason_DIRECT_ROUTE_REASON_AT_CAP}, text.KeyTransportReasonAtCapDetail, false},
		{"security_pending", &corev1.DirectRouteReport{Enabled: true, Capable: true, State: corev1.DirectRouteState_DIRECT_ROUTE_STATE_NONE, Reason: corev1.DirectRouteReason_DIRECT_ROUTE_REASON_SECURITY_PENDING}, text.KeyTransportReasonSecurityPending, false},
		{"starting", &corev1.DirectRouteReport{Enabled: true, Capable: true, State: corev1.DirectRouteState_DIRECT_ROUTE_STATE_NONE, Reason: corev1.DirectRouteReason_DIRECT_ROUTE_REASON_STARTING}, text.KeyTransportReasonStarting, false},
		{"unspecified_when_enabled", &corev1.DirectRouteReport{Enabled: true, Capable: true, State: corev1.DirectRouteState_DIRECT_ROUTE_STATE_NONE}, text.KeyTransportReasonStarting, false},
		{"peer_unsupported", &corev1.DirectRouteReport{Enabled: true, Capable: true, State: corev1.DirectRouteState_DIRECT_ROUTE_STATE_UNSUPPORTED}, text.KeyTransportReasonPeerUnsupported, true},
		{"no_response", &corev1.DirectRouteReport{Enabled: true, Capable: true, State: corev1.DirectRouteState_DIRECT_ROUTE_STATE_NONE, Reason: corev1.DirectRouteReason_DIRECT_ROUTE_REASON_NO_RESPONSE}, text.KeyTransportReasonNoResponse, true},
		{"failed", &corev1.DirectRouteReport{Enabled: true, Capable: true, State: corev1.DirectRouteState_DIRECT_ROUTE_STATE_NONE, Reason: corev1.DirectRouteReason_DIRECT_ROUTE_REASON_FAILED}, text.KeyTransportReasonFailed, true},
	} {
		got := BuildConnectionGroup(true, true, tc.report)
		if keyOf(got.DetailText) != tc.want || got.ShowDiagnosticsEntry != tc.diag {
			t.Fatalf("%s: expected detail=%q diag=%v, got detail=%q diag=%v", tc.name, tc.want, tc.diag, keyOf(got.DetailText), got.ShowDiagnosticsEntry)
		}
	}

	if row := BuildConnCheckDirectRow(false, true, &corev1.DirectRouteReport{Enabled: true, Capable: true}); row.Visible {
		t.Fatalf("offline machine should hide ConnCheck direct row")
	}
	if row := BuildConnCheckDirectRow(true, true, &corev1.DirectRouteReport{Enabled: true, Capable: false}); row.Visible {
		t.Fatalf("incapable client should hide ConnCheck direct row")
	}
	if row := BuildConnCheckDirectRow(true, false, &corev1.DirectRouteReport{Enabled: false, Capable: true, State: corev1.DirectRouteState_DIRECT_ROUTE_STATE_NONE, Reason: corev1.DirectRouteReason_DIRECT_ROUTE_REASON_DISABLED}); !row.Visible || keyOf(row.StatusText) != text.KeyTransportSummaryOff || row.DetailText != nil {
		t.Fatalf("unexpected disabled ConnCheck direct row: %+v", row)
	}
	if row := BuildConnCheckDirectRow(true, true, &corev1.DirectRouteReport{Enabled: true, Capable: true, State: corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED, ReadRoute: corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT, HasActiveRttMs: true, ActiveRttMs: 14}); !row.Visible || keyOf(row.StatusText) != text.KeyTransportStatusDirect || row.MetricText != nil {
		t.Fatalf("unexpected connected ConnCheck direct row (no speed/latency allowed in v63): %+v", row)
	}
	if row := BuildConnCheckDirectRow(true, true, &corev1.DirectRouteReport{Enabled: true, Capable: true, State: corev1.DirectRouteState_DIRECT_ROUTE_STATE_NONE, Reason: corev1.DirectRouteReason_DIRECT_ROUTE_REASON_SECURITY_PENDING}); !row.Visible || keyOf(row.StatusText) != text.KeyTransportStatusRelay || keyOf(row.DetailText) != text.KeyTransportReasonSecurityPending {
		t.Fatalf("unexpected security-pending ConnCheck direct row: %+v", row)
	}
	if row := BuildConnCheckDirectRow(true, true, &corev1.DirectRouteReport{Enabled: true, Capable: true, State: corev1.DirectRouteState_DIRECT_ROUTE_STATE_NONE, Reason: corev1.DirectRouteReason_DIRECT_ROUTE_REASON_AT_CAP}); !row.Visible || keyOf(row.DetailText) != text.KeyTransportReasonAtCap {
		t.Fatalf("unexpected at-cap ConnCheck direct row: %+v", row)
	}

	// Client sort puts self first, then LastSeen desc
	sorted := SortClients([]*corev1.SyncDeviceView{
		{DeviceId: "d1", LastSeenMs: 500},
		{DeviceId: "self", LastSeenMs: 100},
		{DeviceId: "d2", LastSeenMs: 900},
	}, "self")
	if sorted[0].DeviceId != "self" || sorted[1].DeviceId != "d2" || sorted[2].DeviceId != "d1" {
		t.Fatalf("unexpected client sort order: %+v", sorted)
	}

	// Semver & release picker
	pick := PickUpgradeRelease("0.32.0", "stable", []*corev1.ReleaseCandidateInfo{
		{Version: "v0.33.0-rc1", Prerelease: true},
		{Version: "v0.32.5"},
		{Version: "v0.33.0", Draft: true},
	})
	if pick == nil || pick.Version != "v0.32.5" {
		t.Fatalf("expected v0.32.5 stable release, got %+v", pick)
	}
}

func TestMachineStatusRawValuesUnitsAndTopUsageBars(t *testing.T) {
	status := &v1.MachineStatus{
		CpuPercent:       42.4,
		MemoryUsedBytes:  12_884_901_888, // 12.0 GiB
		MemoryTotalBytes: 34_359_738_368, // 32.0 GiB
		Disk: &v1.DiskStatus{
			AvailableBytes: 512_000_000, // 512 MB (< 1 GB)
			TotalBytes:     500_000_000_000,
			Pressure:       v1.ResourcePressure_RESOURCE_PRESSURE_CRITICAL,
		},
	}
	cpu := BuildCpuSection(status, nil)
	if cpu.ValueText != "42" || cpu.Unit != "%" || cpu.Value != 42.4 {
		t.Fatalf("unexpected CPU section raw values: %+v", cpu)
	}
	mem := BuildMemorySection(status, nil)
	if mem.ValueText != "12.0" || mem.Unit != "GB" || mem.UsedBytes != status.MemoryUsedBytes || mem.TotalBytes != status.MemoryTotalBytes {
		t.Fatalf("unexpected Memory section raw values: %+v", mem)
	}
	disk := BuildDiskSection(status)
	if disk.ValueText != "512" || disk.Unit != "MB" || disk.AvailableBytes != 512_000_000 {
		t.Fatalf("unexpected sub-GB Disk section raw values: %+v", disk)
	}
	status.Disk.AvailableBytes = 24_000_000_000
	diskGB := BuildDiskSection(status)
	if diskGB.ValueText != "24" || diskGB.Unit != "GB" {
		t.Fatalf("unexpected GB Disk section raw values: %+v", diskGB)
	}

	rows, _ := BuildUsageRows(nil, []*v1.ResourceUsage{
		{Id: "a", Executable: "node", CpuPercent: 80, MemoryBytes: 2 * gibBytes},
		{Id: "b", Executable: "go", CpuPercent: 20, MemoryBytes: 4 * gibBytes},
	}, true, nil, lists.Calendar{})
	if len(rows) != 2 {
		t.Fatalf("expected 2 usage rows, got %d", len(rows))
	}
	if rows[0].CpuBarPercent != 100 || rows[0].MemoryBarPercent != 50 || rows[1].CpuBarPercent != 25 || rows[1].MemoryBarPercent != 100 {
		t.Fatalf("unexpected normalized usage bar percents: r0=(%d,%d) r1=(%d,%d)",
			rows[0].CpuBarPercent, rows[0].MemoryBarPercent, rows[1].CpuBarPercent, rows[1].MemoryBarPercent)
	}
	if rows[0].GetCpuValueText().GetLiteral() != "80%" || rows[0].GetMemoryValueText() == nil {
		t.Fatalf("expected formatted cpu/memory values on usage row: %+v", rows[0])
	}
}
