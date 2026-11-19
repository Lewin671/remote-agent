package machine

import (
	"strconv"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

// BuildConnectionGroup renders the Connection group ViewModel from the shell's latest route status report.
func BuildConnectionGroup(online bool, prefEnabled bool, report *corev1.DirectRouteReport) *corev1.ConnectionGroupView {
	if report == nil {
		report = &corev1.DirectRouteReport{
			Enabled: prefEnabled,
			Capable: true,
			State:   corev1.DirectRouteState_DIRECT_ROUTE_STATE_UNSPECIFIED,
		}
	}
	checked := prefEnabled
	if report.State != corev1.DirectRouteState_DIRECT_ROUTE_STATE_UNSPECIFIED {
		checked = report.Enabled
	}

	view := &corev1.ConnectionGroupView{
		ToggleVisible: true,
		ToggleEnabled: online && report.Capable && !report.DaemonDisabled,
		ToggleChecked: report.Capable && !report.DaemonDisabled && checked,
		FooterText:    text.Msg(text.KeyTransportFooter),
	}

	defer populateConnectionRowSummary(view, report)

	if !report.Capable {
		view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_UNSUPPORTED
		view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED
		view.StatusText = text.Msg(text.KeyConnectionUnavailable)
		view.DetailText = text.Msg(text.KeyTransportDetailRelayOnly)
		return view
	}

	if !online {
		view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_OFFLINE
		view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED
		view.ToggleChecked = checked
		view.StatusText = text.Msg(text.KeySettingsOffline)
		return view
	}

	view.FileSpeedText = formatFileSpeeds(report)
	view.ReadLatencyText = formatReadLatencies(report)
	view.MetricText = activeRttText(report)

	if !checked || report.Reason == corev1.DirectRouteReason_DIRECT_ROUTE_REASON_DISABLED {
		view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_DISABLED
		view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED
		view.ToggleChecked = false
		view.StatusText = text.Msg(text.KeyTransportStatusRelay)
		return view
	}

	if report.DaemonDisabled || report.Reason == corev1.DirectRouteReason_DIRECT_ROUTE_REASON_DAEMON_DISABLED {
		view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_DISABLED
		view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED
		view.ToggleChecked = false
		view.StatusText = text.Msg(text.KeyTransportStatusDaemonDisabled)
		view.DetailText = text.Msg(text.KeyTransportDetailDaemonDisabled)
		return view
	}

	switch report.State {
	case corev1.DirectRouteState_DIRECT_ROUTE_STATE_UNSUPPORTED:
		view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_RELAY_FAILED
		view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED
		view.StatusText = text.Msg(text.KeyTransportStatusRelay)
		view.DetailText = text.Msg(text.KeyTransportReasonPeerUnsupported)
		view.ShowDiagnosticsEntry = true

	case corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTING:
		view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_CONNECTING
		view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED
		view.StatusText = text.Msg(text.KeyTransportStatusConnecting)

	case corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED:
		readDirect := report.ReadRoute == corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT
		fileDirect := report.FileRoute == corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT
		if readDirect || fileDirect {
			view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_DIRECT
			view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_LIVE
			view.StatusText = text.Msg(text.KeyTransportStatusDirect)
			if fileDirect && !readDirect {
				view.DetailText = text.Msg(text.KeyTransportUseFile)
			} else if readDirect && !fileDirect {
				view.DetailText = text.Msg(text.KeyTransportUseRead)
			}
			return view
		}
		if isFailureDirectReason(report.Reason) {
			view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_RELAY_FAILED
			view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED
			view.StatusText = text.Msg(text.KeyTransportStatusRelay)
			view.DetailText = directReasonText(report.Reason)
			view.ShowDiagnosticsEntry = true
			return view
		}
		view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_RELAY
		view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED
		view.StatusText = text.Msg(text.KeyTransportStatusRelay)
		switch {
		case report.ReadSlow || report.FileSlow:
			view.DetailText = text.Msg(text.KeyTransportDirectSlow)
		case report.Reason == corev1.DirectRouteReason_DIRECT_ROUTE_REASON_AT_CAP:
			view.DetailText = text.Msg(text.KeyTransportReasonAtCapDetail)
		case directReasonText(report.Reason) != nil:
			view.DetailText = directReasonText(report.Reason)
		default:
			view.DetailText = text.Msg(text.KeyTransportReasonChannelPending)
		}

	default:
		switch report.Reason {
		case corev1.DirectRouteReason_DIRECT_ROUTE_REASON_PEER_UNSUPPORTED,
			corev1.DirectRouteReason_DIRECT_ROUTE_REASON_NO_RESPONSE,
			corev1.DirectRouteReason_DIRECT_ROUTE_REASON_NO_TRAVERSAL,
			corev1.DirectRouteReason_DIRECT_ROUTE_REASON_FAILED:
			view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_RELAY_FAILED
			view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED
			view.StatusText = text.Msg(text.KeyTransportStatusRelay)
			view.DetailText = directReasonText(report.Reason)
			view.ShowDiagnosticsEntry = true
		case corev1.DirectRouteReason_DIRECT_ROUTE_REASON_AT_CAP:
			view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_RELAY
			view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED
			view.StatusText = text.Msg(text.KeyTransportStatusRelay)
			view.DetailText = text.Msg(text.KeyTransportReasonAtCapDetail)
		default:
			view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_RELAY
			view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED
			view.StatusText = text.Msg(text.KeyTransportStatusRelay)
			if r := directReasonText(report.Reason); r != nil {
				view.DetailText = r
			} else {
				view.DetailText = text.Msg(text.KeyTransportReasonStarting)
			}
		}
	}

	return view
}

// BuildConnCheckDirectRow renders the direct-path status row shown under an online computer in ConnCheck.
func BuildConnCheckDirectRow(online bool, prefEnabled bool, report *corev1.DirectRouteReport) *corev1.ConnCheckDirectRowView {
	if report == nil {
		report = &corev1.DirectRouteReport{
			Enabled: prefEnabled,
			Capable: true,
			State:   corev1.DirectRouteState_DIRECT_ROUTE_STATE_UNSPECIFIED,
		}
	}
	cg := BuildConnectionGroup(online, prefEnabled, report)
	if !online || !report.Capable {
		return &corev1.ConnCheckDirectRowView{
			Visible: false,
			Stage:   cg.Stage,
			Tone:    cg.Tone,
		}
	}
	status := cg.StatusText
	detail := cg.DetailText
	switch cg.Stage {
	case corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_DISABLED:
		if !report.DaemonDisabled && report.Reason != corev1.DirectRouteReason_DIRECT_ROUTE_REASON_DAEMON_DISABLED {
			status = text.Msg(text.KeyTransportSummaryOff)
			detail = nil
		}
	}
	if report.Reason == corev1.DirectRouteReason_DIRECT_ROUTE_REASON_AT_CAP && cg.Stage == corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_RELAY {
		detail = text.Msg(text.KeyTransportReasonAtCap)
	}
	return &corev1.ConnCheckDirectRowView{
		Visible:    true,
		Stage:      cg.Stage,
		Tone:       cg.Tone,
		LabelText:  text.Msg(text.KeyTransportDirect),
		StatusText: status,
		DetailText: detail,
	}
}

func isFailureDirectReason(r corev1.DirectRouteReason) bool {
	switch r {
	case corev1.DirectRouteReason_DIRECT_ROUTE_REASON_PEER_UNSUPPORTED,
		corev1.DirectRouteReason_DIRECT_ROUTE_REASON_NO_RESPONSE,
		corev1.DirectRouteReason_DIRECT_ROUTE_REASON_NO_TRAVERSAL,
		corev1.DirectRouteReason_DIRECT_ROUTE_REASON_FAILED:
		return true
	default:
		return false
	}
}

func directReasonText(r corev1.DirectRouteReason) *corev1.Text {
	switch r {
	case corev1.DirectRouteReason_DIRECT_ROUTE_REASON_NO_RESPONSE:
		return text.Msg(text.KeyTransportReasonNoResponse)
	case corev1.DirectRouteReason_DIRECT_ROUTE_REASON_NO_TRAVERSAL:
		return text.Msg(text.KeyTransportReasonNoTraversal)
	case corev1.DirectRouteReason_DIRECT_ROUTE_REASON_FAILED:
		return text.Msg(text.KeyTransportReasonFailed)
	case corev1.DirectRouteReason_DIRECT_ROUTE_REASON_PEER_UNSUPPORTED:
		return text.Msg(text.KeyTransportReasonPeerUnsupported)
	case corev1.DirectRouteReason_DIRECT_ROUTE_REASON_AT_CAP:
		return text.Msg(text.KeyTransportReasonAtCap)
	case corev1.DirectRouteReason_DIRECT_ROUTE_REASON_SECURITY_PENDING:
		return text.Msg(text.KeyTransportReasonSecurityPending)
	case corev1.DirectRouteReason_DIRECT_ROUTE_REASON_STARTING:
		return text.Msg(text.KeyTransportReasonStarting)
	case corev1.DirectRouteReason_DIRECT_ROUTE_REASON_DAEMON_DISABLED:
		return text.Msg(text.KeyTransportReasonDaemonDisabled)
	default:
		return nil
	}
}

func activeRttText(r *corev1.DirectRouteReport) *corev1.Text {
	if r.HasActiveRttMs && r.ActiveRttMs >= 0 {
		return text.Msg(text.KeyTransportRttMs, text.Str("ms", strconv.FormatInt(int64(r.ActiveRttMs), 10)))
	}
	if r.ReadRoute == corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT && r.HasDirectReadMs {
		return text.Msg(text.KeyTransportRttMs, text.Str("ms", strconv.FormatInt(int64(r.DirectReadMs), 10)))
	}
	if r.HasRelayReadMs && r.State != corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED {
		return text.Msg(text.KeyTransportRttMs, text.Str("ms", strconv.FormatInt(int64(r.RelayReadMs), 10)))
	}
	if r.ReadRoute == corev1.DirectPathChoice_DIRECT_PATH_CHOICE_RELAY && r.HasRelayReadMs {
		return text.Msg(text.KeyTransportRttMs, text.Str("ms", strconv.FormatInt(int64(r.RelayReadMs), 10)))
	}
	return nil
}

func formatFileSpeeds(r *corev1.DirectRouteReport) *corev1.Text {
	switch {
	case r.HasDirectFileMibps && r.HasRelayFileMibps:
		return text.Msg(
			text.KeyTransportFileSpeedBoth,
			text.Str("direct", formatMiBps(r.DirectFileMibps)),
			text.Str("relay", formatMiBps(r.RelayFileMibps)),
		)
	case r.HasDirectFileMibps:
		return text.Msg(text.KeyTransportFileSpeedDirect, text.Str("direct", formatMiBps(r.DirectFileMibps)))
	case r.HasRelayFileMibps:
		return text.Msg(text.KeyTransportFileSpeedRelay, text.Str("relay", formatMiBps(r.RelayFileMibps)))
	default:
		return nil
	}
}

func formatReadLatencies(r *corev1.DirectRouteReport) *corev1.Text {
	switch {
	case r.HasDirectReadMs && r.HasRelayReadMs:
		return text.Msg(
			text.KeyTransportReadLatencyBoth,
			text.Str("direct", strconv.FormatInt(int64(r.DirectReadMs), 10)),
			text.Str("relay", strconv.FormatInt(int64(r.RelayReadMs), 10)),
		)
	case r.HasDirectReadMs:
		return text.Msg(text.KeyTransportReadLatencyDirect, text.Str("direct", strconv.FormatInt(int64(r.DirectReadMs), 10)))
	case r.HasRelayReadMs:
		return text.Msg(text.KeyTransportReadLatencyRelay, text.Str("relay", strconv.FormatInt(int64(r.RelayReadMs), 10)))
	default:
		return nil
	}
}

func formatMiBps(v float64) string {
	if v >= 10 {
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

func populateConnectionRowSummary(view *corev1.ConnectionGroupView, report *corev1.DirectRouteReport) {
	if view == nil {
		return
	}
	switch view.Stage {
	case corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_UNSUPPORTED:
		view.RowValueText = text.Msg(text.KeyTransportRowUnavailable)
	case corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_OFFLINE:
		view.RowDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_GRAY_STATIC
		view.RowValueText = text.Msg(text.KeySettingsOffline)
	case corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_DISABLED:
		view.RowValueText = text.Msg(text.KeyTransportRowOff)
		if report != nil && (report.DaemonDisabled || report.Reason == corev1.DirectRouteReason_DIRECT_ROUTE_REASON_DAEMON_DISABLED) {
			view.RowSubtitleText = view.DetailText
		}
	case corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_DIRECT:
		view.RowDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_GREEN_LIVE
		if ms := activeRttMillisString(report, true); ms != "" {
			view.RowValueText = text.Msg(text.KeyTransportRowUsingDirectMs, text.Str("ms", ms))
		} else {
			view.RowValueText = text.Msg(text.KeyTransportRowUsingDirect)
		}
		view.RowSubtitleText = view.DetailText
	case corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_CONNECTING:
		view.RowSpinning = true
		view.RowValueText = text.Msg(text.KeyTransportStatusConnecting)
	default:
		view.RowDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_GRAY_STATIC
		if ms := activeRttMillisString(report, false); ms != "" {
			view.RowValueText = text.Msg(text.KeyTransportRowRelayMs, text.Str("ms", ms))
		} else {
			view.RowValueText = text.Msg(text.KeyTransportRowRelay)
		}
		view.RowSubtitleText = view.DetailText
	}
}

func activeRttMillisString(r *corev1.DirectRouteReport, preferDirect bool) string {
	if r == nil {
		return ""
	}
	if preferDirect && r.HasDirectReadMs && r.DirectReadMs >= 0 {
		return strconv.FormatInt(int64(r.DirectReadMs), 10)
	}
	if !preferDirect && r.HasRelayReadMs && r.RelayReadMs >= 0 {
		return strconv.FormatInt(int64(r.RelayReadMs), 10)
	}
	if r.HasActiveRttMs && r.ActiveRttMs >= 0 {
		return strconv.FormatInt(int64(r.ActiveRttMs), 10)
	}
	if r.HasDirectReadMs && r.DirectReadMs >= 0 {
		return strconv.FormatInt(int64(r.DirectReadMs), 10)
	}
	if r.HasRelayReadMs && r.RelayReadMs >= 0 {
		return strconv.FormatInt(int64(r.RelayReadMs), 10)
	}
	return ""
}
