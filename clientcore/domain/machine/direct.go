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
		ToggleVisible:        report.Capable,
		ToggleEnabled:        online && report.Capable && !report.DaemonDisabled,
		ToggleChecked:        checked,
		ShowDiagnosticsEntry: report.HasDiagnostics,
	}

	if !report.Capable {
		view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_UNSUPPORTED
		view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED
		view.StatusText = text.Msg(text.KeyConnectionUnavailable)
		view.FooterText = text.Msg(text.KeyTransportFooter)
		return view
	}

	if !online {
		view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_OFFLINE
		view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED
		view.StatusText = text.Msg(text.KeySettingsOffline)
		view.FooterText = text.Msg(text.KeyTransportFooter)
		return view
	}

	view.FileSpeedText = formatFileSpeeds(report)
	view.ReadLatencyText = formatReadLatencies(report)
	view.MetricText = activeRttText(report)

	if !checked || report.Reason == corev1.DirectRouteReason_DIRECT_ROUTE_REASON_DISABLED {
		view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_DISABLED
		view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED
		view.StatusText = text.Msg(text.KeyTransportStatusRelay)
		view.FooterText = text.Msg(text.KeyTransportSummaryOff)
		return view
	}

	if report.DaemonDisabled || report.Reason == corev1.DirectRouteReason_DIRECT_ROUTE_REASON_DAEMON_DISABLED {
		view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_DISABLED
		view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED
		view.StatusText = text.Msg(text.KeyTransportStatusRelay)
		view.DetailText = text.Msg(text.KeyTransportReasonNoResponse)
		view.FooterText = text.Msg(text.KeyTransportSummaryOff)
		return view
	}

	switch report.State {
	case corev1.DirectRouteState_DIRECT_ROUTE_STATE_UNSUPPORTED:
		view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_UNSUPPORTED
		view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED
		view.StatusText = text.Msg(text.KeyTransportUnavailable, text.ArgText("reason", text.Msg(text.KeyTransportReasonPeerUnsupported)))
		view.DetailText = text.Msg(text.KeyTransportReasonPeerUnsupported)
		view.FooterText = text.Msg(text.KeyTransportFooter)

	case corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTING:
		view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_CONNECTING
		view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED
		view.StatusText = text.Msg(text.KeyTransportStatusConnecting)
		view.FooterText = text.Msg(text.KeyTransportFooter)

	case corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED:
		view.FooterText = text.Msg(text.KeyTransportFooter)
		readDirect := report.ReadRoute == corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT
		fileDirect := report.FileRoute == corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT
		if readDirect || fileDirect {
			view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_DIRECT
			view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_LIVE
			view.StatusText = text.Msg(text.KeyTransportStatusDirect)
			// Match web/android: only label the active single purpose unless explicit slow-fallback signals exist.
			if fileDirect && !readDirect {
				view.DetailText = text.Msg(text.KeyTransportUseFile)
			} else if readDirect && !fileDirect {
				view.DetailText = text.Msg(text.KeyTransportUseRead)
			}
			return view
		}
		view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_RELAY
		view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED
		view.StatusText = text.Msg(text.KeyTransportStatusRelay)

	default:
		view.FooterText = text.Msg(text.KeyTransportFooter)
		switch report.Reason {
		case corev1.DirectRouteReason_DIRECT_ROUTE_REASON_AT_CAP:
			view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_RELAY
			view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED
			view.StatusText = text.Msg(text.KeyTransportStatusRelay)
		case corev1.DirectRouteReason_DIRECT_ROUTE_REASON_PEER_UNSUPPORTED:
			view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_UNSUPPORTED
			view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED
			view.StatusText = text.Msg(text.KeyTransportUnavailable, text.ArgText("reason", text.Msg(text.KeyTransportReasonPeerUnsupported)))
			view.DetailText = text.Msg(text.KeyTransportReasonPeerUnsupported)
			view.FooterText = text.Msg(text.KeyTransportFooter)
		case corev1.DirectRouteReason_DIRECT_ROUTE_REASON_NO_RESPONSE,
			corev1.DirectRouteReason_DIRECT_ROUTE_REASON_NO_TRAVERSAL,
			corev1.DirectRouteReason_DIRECT_ROUTE_REASON_FAILED:
			view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_RELAY_FAILED
			view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_WARN
			view.StatusText = text.Msg(text.KeyTransportStatusRelay)
			view.DetailText = directFailureReasonText(report.Reason)
			view.ShowDiagnosticsEntry = true
		default:
			view.Stage = corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_RELAY
			view.Tone = corev1.ConnectionGroupTone_CONNECTION_GROUP_TONE_MUTED
			view.StatusText = text.Msg(text.KeyTransportStatusRelay)
		}
	}

	return view
}

func directFailureReasonText(r corev1.DirectRouteReason) *corev1.Text {
	switch r {
	case corev1.DirectRouteReason_DIRECT_ROUTE_REASON_NO_RESPONSE:
		return text.Msg(text.KeyTransportReasonNoResponse)
	case corev1.DirectRouteReason_DIRECT_ROUTE_REASON_NO_TRAVERSAL:
		return text.Msg(text.KeyTransportReasonNoTraversal)
	case corev1.DirectRouteReason_DIRECT_ROUTE_REASON_FAILED:
		return text.Msg(text.KeyTransportReasonFailed)
	default:
		return nil
	}
}

func activeRttText(r *corev1.DirectRouteReport) *corev1.Text {
	if r.HasActiveRttMs && r.ActiveRttMs >= 0 {
		if r.ReadRoute == corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT {
			return text.Msg(text.KeyTransportReadLatencyDirect, text.Str("direct", strconv.FormatInt(int64(r.ActiveRttMs), 10)))
		}
		return text.Msg(text.KeyTransportReadLatencyRelay, text.Str("relay", strconv.FormatInt(int64(r.ActiveRttMs), 10)))
	}
	if r.ReadRoute == corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT && r.HasDirectReadMs {
		return text.Msg(text.KeyTransportReadLatencyDirect, text.Str("direct", strconv.FormatInt(int64(r.DirectReadMs), 10)))
	}
	if r.HasRelayReadMs && r.State != corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED {
		return text.Msg(text.KeyTransportReadLatencyRelay, text.Str("relay", strconv.FormatInt(int64(r.RelayReadMs), 10)))
	}
	if r.ReadRoute == corev1.DirectPathChoice_DIRECT_PATH_CHOICE_RELAY && r.HasRelayReadMs {
		return text.Msg(text.KeyTransportReadLatencyRelay, text.Str("relay", strconv.FormatInt(int64(r.RelayReadMs), 10)))
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
