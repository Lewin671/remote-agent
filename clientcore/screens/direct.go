package screens

import (
	"math"
	"strconv"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/sharedfiles"
	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
	"github.com/Lewin671/remote-agent/clientcore/transport"
	"github.com/Lewin671/remote-agent/shared/linkcore"
)

// BuildDirectRouteReport converts a transport.Status snapshot and preference flags
// into the shared DirectRouteReport proto consumed by machine/session ViewModels.
func BuildDirectRouteReport(status transport.Status, enabled, daemonDisabled bool, diagnosticsCount int) *corev1.DirectRouteReport {
	hasDiagnostics := diagnosticsCount > 0 ||
		status.Reason == transport.ReasonPeerUnsupported ||
		status.Reason == transport.ReasonNoResponse ||
		status.Reason == transport.ReasonNoTraversal ||
		status.Reason == transport.ReasonFailed ||
		status.Reason == transport.ReasonLost

	var state corev1.DirectRouteState
	switch status.State {
	case transport.StateUnsupported:
		state = corev1.DirectRouteState_DIRECT_ROUTE_STATE_UNSUPPORTED
	case transport.StateConnected:
		state = corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED
	case transport.StateConnecting:
		state = corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTING
	default:
		state = corev1.DirectRouteState_DIRECT_ROUTE_STATE_NONE
	}

	var reason corev1.DirectRouteReason
	switch status.Reason {
	case transport.ReasonDisabled:
		reason = corev1.DirectRouteReason_DIRECT_ROUTE_REASON_DISABLED
	case transport.ReasonPeerUnsupported:
		reason = corev1.DirectRouteReason_DIRECT_ROUTE_REASON_PEER_UNSUPPORTED
	case transport.ReasonNoResponse:
		reason = corev1.DirectRouteReason_DIRECT_ROUTE_REASON_NO_RESPONSE
	case transport.ReasonNoTraversal:
		reason = corev1.DirectRouteReason_DIRECT_ROUTE_REASON_NO_TRAVERSAL
	case transport.ReasonFailed:
		reason = corev1.DirectRouteReason_DIRECT_ROUTE_REASON_FAILED
	case transport.ReasonLost:
		reason = corev1.DirectRouteReason_DIRECT_ROUTE_REASON_LOST
	case transport.ReasonAtCap:
		reason = corev1.DirectRouteReason_DIRECT_ROUTE_REASON_AT_CAP
	case transport.ReasonSecurityPending:
		reason = corev1.DirectRouteReason_DIRECT_ROUTE_REASON_SECURITY_PENDING
	case transport.ReasonStarting:
		reason = corev1.DirectRouteReason_DIRECT_ROUTE_REASON_STARTING
	default:
		reason = corev1.DirectRouteReason_DIRECT_ROUTE_REASON_UNSPECIFIED
	}

	readRoute := corev1.DirectPathChoice_DIRECT_PATH_CHOICE_RELAY
	if status.Read == transport.Direct {
		readRoute = corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT
	}
	fileRoute := corev1.DirectPathChoice_DIRECT_PATH_CHOICE_RELAY
	if status.File == transport.Direct {
		fileRoute = corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT
	}
	terminalRoute := corev1.DirectPathChoice_DIRECT_PATH_CHOICE_RELAY
	if status.Terminal == transport.Direct {
		terminalRoute = corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT
	}

	m := status.Measured
	var activeRtt int32
	var hasActiveRtt bool
	if status.Read == transport.Direct {
		if m.HasReadMs[transport.Direct] {
			hasActiveRtt = true
			activeRtt = int32(math.Round(m.ReadMs[transport.Direct]))
		}
	} else if m.HasReadMs[transport.Relay] {
		hasActiveRtt = true
		activeRtt = int32(math.Round(m.ReadMs[transport.Relay]))
	}

	return &corev1.DirectRouteReport{
		Enabled:            enabled,
		Capable:            status.State != transport.StateUnsupported,
		DaemonDisabled:     daemonDisabled || (status.Reason == transport.ReasonDisabled && enabled),
		State:              state,
		Reason:             reason,
		FileRoute:          fileRoute,
		ReadRoute:          readRoute,
		TerminalRoute:      terminalRoute,
		HasActiveRttMs:     hasActiveRtt,
		ActiveRttMs:        activeRtt,
		HasDirectFileMibps: m.HasFileMiBps[transport.Direct],
		DirectFileMibps:    m.FileMiBps[transport.Direct],
		HasRelayFileMibps:  m.HasFileMiBps[transport.Relay],
		RelayFileMibps:     m.FileMiBps[transport.Relay],
		HasDirectReadMs:    m.HasReadMs[transport.Direct],
		DirectReadMs:       int32(math.Round(m.ReadMs[transport.Direct])),
		HasRelayReadMs:     m.HasReadMs[transport.Relay],
		RelayReadMs:        int32(math.Round(m.ReadMs[transport.Relay])),
		Warming:            false,
		Compared:           status.ReadSlow || status.FileSlow,
		HasDiagnostics:     hasDiagnostics,
		ReadSlow:           status.ReadSlow,
		FileSlow:           status.FileSlow,
		RetryScheduled:     status.RetryScheduled,
	}
}

// BuildSessionDirectChip constructs SessionHeaderView.DirectChip from a machine's DirectRouteReport.
func BuildSessionDirectChip(report *corev1.DirectRouteReport, computerName *corev1.Text, online, unlinked, exited bool) *corev1.SessionDirectChipView {
	if !online || unlinked || exited || report == nil || !report.Capable || report.State != corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED {
		return nil
	}
	readDirect := report.ReadRoute == corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT
	fileDirect := report.FileRoute == corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT
	terminalDirect := report.TerminalRoute == corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT
	active := readDirect || fileDirect || terminalDirect
	if !active {
		return nil
	}

	nameArg := computerName
	if nameArg == nil {
		nameArg = text.Msg(text.KeyCommonUnknownComputer)
	}

	var title, body, foot *corev1.Text
	if report.HasActiveRttMs && report.ActiveRttMs >= 0 {
		title = text.Msg(text.KeyTransportPopoverDirectTitleRtt,
			text.ArgText("name", nameArg),
			text.Str("ms", strconv.FormatInt(int64(report.ActiveRttMs), 10)),
		)
	} else {
		title = text.Msg(text.KeyTransportPopoverDirectTitle, text.ArgText("name", nameArg))
	}
	body = text.Msg(text.KeyTransportPopoverDirectBody)
	switch {
	case (readDirect || terminalDirect) && fileDirect:
		foot = text.Msg(text.KeyTransportPopoverStable)
	case readDirect || terminalDirect:
		foot = text.Msg(text.KeyTransportPopoverReadOnly)
	default:
		foot = text.Msg(text.KeyTransportPopoverFileOnly)
	}

	return &corev1.SessionDirectChipView{
		Visible:        true,
		Active:         true,
		ReadDirect:     readDirect,
		FileDirect:     fileDirect,
		TerminalDirect: terminalDirect,
		LabelText:      text.Msg(text.KeyTransportDirect),
		PopoverTitle:   title,
		PopoverBody:    body,
		PopoverFoot:    foot,
	}
}

// BuildDirectDiagnosticsView constructs the localized DirectDiagnosticsViewModel
// (summary cards, expert groups, and privacy-safe copyable report lines) from Coordinator state.
func BuildDirectDiagnosticsView(
	machineID string,
	status transport.Status,
	enabled bool,
	daemonDisabled bool,
	rounds []*transport.RoundData,
	blockedAt int64,
	blocked bool,
	nowMs int64,
	platformAgent string,
) *corev1.DirectDiagnosticsViewModel {
	report := BuildDirectRouteReport(status, enabled, daemonDisabled, len(rounds))

	var chosen *transport.RoundData
	for _, r := range rounds {
		if r != nil && (r.End != transport.EndNone || r.HasEstablished) {
			chosen = r
			break
		}
	}
	if chosen == nil && len(rounds) > 0 {
		chosen = rounds[0]
	}

	_ = blockedAt
	connected := status.State == transport.StateConnected
	verdict := transport.VerdictOf(chosen, blocked)
	if !connected && verdict == transport.VerdictConnected {
		verdict = transport.VerdictLost
	} else if connected && verdict != transport.VerdictConnected {
		verdict = transport.VerdictConnected
	}
	verdictText := diagVerdictText(verdict)

	var groups []*corev1.DiagGroupView
	if chosen != nil {
		pc, att, _ := transport.LeadPc(chosen)
		groups = diagAddressGroups(pc, att)
	}
	resGroup := diagResultGroup(rounds, chosen, connected, nowMs)

	peerOutdated := !connected && (status.Reason == transport.ReasonPeerUnsupported || verdict == transport.VerdictPeerRefused)
	noReply := !connected && !peerOutdated && (status.Reason == transport.ReasonNoResponse || verdict == transport.VerdictNoReply)
	lost := !connected && !peerOutdated && !noReply && (status.Reason == transport.ReasonLost || verdict == transport.VerdictLost)
	probeBlocked := !connected && !peerOutdated && !noReply && !lost && (verdict == transport.VerdictNoSTUN || verdict == transport.VerdictNoLocal)
	natBlocked := !connected && !peerOutdated && !noReply && !lost && !probeBlocked &&
		(status.Reason == transport.ReasonNoTraversal ||
			verdict == transport.VerdictAllFailed ||
			verdict == transport.VerdictIPv6Silent ||
			verdict == transport.VerdictNoRemote ||
			verdict == transport.VerdictDTLS ||
			verdict == transport.VerdictChannel ||
			verdict == transport.VerdictConfirm)

	dropSummary := diagRoundDropSummary(chosen)

	var titleText, bodyText *corev1.Text
	switch {
	case connected:
		titleText = text.Msg(text.KeyTransportDiagConnectedTitle)
		bodyText = text.Msg(text.KeyTransportDiagConnectedBody)
	case peerOutdated:
		titleText = text.Msg(text.KeyTransportReasonPeerUnsupported)
		bodyText = text.Msg(text.KeyTransportDiagExplainPeerOld)
	case noReply:
		titleText = text.Msg(text.KeyTransportDiagTitleNoReply)
		bodyText = text.Msg(text.KeyTransportDiagExplainNoReply)
	case lost:
		titleText = text.Msg(text.KeyTransportDiagTitleLost)
		if dropSummary != nil {
			bodyText = text.Msg(text.KeyTransportDiagExplainLostDetail, text.ArgText("summary", dropSummary))
		} else {
			bodyText = text.Msg(text.KeyTransportDiagExplainLost)
		}
	case probeBlocked:
		titleText = text.Msg(text.KeyTransportDiagTitleProbeBlocked)
		bodyText = text.Msg(text.KeyTransportDiagExplainProbeBlocked)
	case natBlocked:
		titleText = text.Msg(text.KeyTransportReasonNoTraversal)
		bodyText = text.Msg(text.KeyTransportDiagExplainNat)
	default:
		titleText = text.Msg(text.KeyTransportDiagTitleGeneric)
		bodyText = text.Msg(text.KeyTransportDiagExplainGeneric)
	}

	trying := status.State == transport.StateConnecting
	supported := status.State != transport.StateUnsupported

	var peerCheck *corev1.PlainDiagCheckView
	switch {
	case peerOutdated:
		peerCheck = &corev1.PlainDiagCheckView{
			Mark:       corev1.DiagCheckMark_DIAG_CHECK_MARK_OFF,
			LabelText:  text.Msg(text.KeyTransportDiagCheckPeer),
			DetailText: text.Msg(text.KeyTransportDiagPeerOldDesc),
			StatusText: text.Msg(text.KeyTransportDiagStatusPeerOld),
		}
	case noReply:
		peerCheck = &corev1.PlainDiagCheckView{
			Mark:       corev1.DiagCheckMark_DIAG_CHECK_MARK_OFF,
			LabelText:  text.Msg(text.KeyTransportDiagCheckPeer),
			DetailText: text.Msg(text.KeyTransportDiagPeerNoReplyDesc),
			StatusText: text.Msg(text.KeyTransportDiagStatusNoReply),
		}
	default:
		peerCheck = &corev1.PlainDiagCheckView{
			Mark:       corev1.DiagCheckMark_DIAG_CHECK_MARK_ONLINE,
			LabelText:  text.Msg(text.KeyTransportDiagCheckPeer),
			StatusText: text.Msg(text.KeyTransportDiagStatusOk),
		}
	}

	var pathCheck *corev1.PlainDiagCheckView
	switch {
	case connected:
		pathCheck = &corev1.PlainDiagCheckView{
			Mark:       corev1.DiagCheckMark_DIAG_CHECK_MARK_ONLINE,
			LabelText:  text.Msg(text.KeyTransportDiagCheckPath),
			StatusText: text.Msg(text.KeyTransportDiagStatusConnected),
		}
	case trying:
		pathCheck = &corev1.PlainDiagCheckView{
			Mark:       corev1.DiagCheckMark_DIAG_CHECK_MARK_RUN,
			LabelText:  text.Msg(text.KeyTransportDiagCheckPath),
			StatusText: text.Msg(text.KeyTransportDiagStatusChecking),
		}
	case peerOutdated || noReply:
		pathCheck = &corev1.PlainDiagCheckView{
			Mark:       corev1.DiagCheckMark_DIAG_CHECK_MARK_OFF,
			LabelText:  text.Msg(text.KeyTransportDiagCheckPath),
			StatusText: text.Msg(text.KeyTransportDiagStatusNotStarted),
		}
	case lost:
		pathCheck = &corev1.PlainDiagCheckView{
			Mark:       corev1.DiagCheckMark_DIAG_CHECK_MARK_OFF,
			LabelText:  text.Msg(text.KeyTransportDiagCheckPath),
			DetailText: dropSummary,
			StatusText: text.Msg(text.KeyTransportDiagStatusDropped),
		}
	case probeBlocked:
		pathCheck = &corev1.PlainDiagCheckView{
			Mark:       corev1.DiagCheckMark_DIAG_CHECK_MARK_OFF,
			LabelText:  text.Msg(text.KeyTransportDiagCheckPath),
			DetailText: text.Msg(text.KeyTransportDiagPathUdpBlocked),
			StatusText: text.Msg(text.KeyTransportDiagStatusBlocked),
		}
	case natBlocked:
		pathCheck = &corev1.PlainDiagCheckView{
			Mark:       corev1.DiagCheckMark_DIAG_CHECK_MARK_OFF,
			LabelText:  text.Msg(text.KeyTransportDiagCheckPath),
			DetailText: text.Msg(text.KeyTransportDiagPathNat),
			StatusText: text.Msg(text.KeyTransportDiagStatusBlocked),
		}
	default:
		pathCheck = &corev1.PlainDiagCheckView{
			Mark:       corev1.DiagCheckMark_DIAG_CHECK_MARK_OFF,
			LabelText:  text.Msg(text.KeyTransportDiagCheckPath),
			DetailText: text.Msg(text.KeyTransportDiagPathFailed),
			StatusText: text.Msg(text.KeyTransportDiagStatusBlocked),
		}
	}

	var retryCheck *corev1.PlainDiagCheckView
	if peerOutdated {
		retryCheck = &corev1.PlainDiagCheckView{
			Mark:       corev1.DiagCheckMark_DIAG_CHECK_MARK_OFF,
			LabelText:  text.Msg(text.KeyTransportDiagCheckRetry),
			DetailText: text.Msg(text.KeyTransportDiagRetryWaitDesc),
			StatusText: text.Msg(text.KeyTransportDiagStatusWaitUpdate),
		}
	} else {
		retryCheck = &corev1.PlainDiagCheckView{
			Mark:       corev1.DiagCheckMark_DIAG_CHECK_MARK_ONLINE,
			LabelText:  text.Msg(text.KeyTransportDiagCheckRetry),
			DetailText: text.Msg(text.KeyTransportDiagRetryAutoDesc),
			StatusText: text.Msg(text.KeyTransportDiagStatusEnabled),
		}
	}

	reportLines := buildDiagReportLines(verdictText, rounds, connected, nowMs, platformAgent)

	return &corev1.DirectDiagnosticsViewModel{
		MachineId:    machineID,
		Supported:    supported,
		Enabled:      enabled,
		Connected:    connected,
		PeerOutdated: peerOutdated,
		Trying:       trying,
		CanRetry:     supported && enabled && !connected && !peerOutdated && !trying,
		TitleText:    titleText,
		BodyText:     bodyText,
		VerdictText:  verdictText,
		Checks:       []*corev1.PlainDiagCheckView{peerCheck, pathCheck, retryCheck},
		Groups:       groups,
		ResultGroup:  resGroup,
		ReportLines:  reportLines,
		Report:       report,
	}
}

func diagAppendGroup(lines *[]*corev1.Text, g *corev1.DiagGroupView, indent string) {
	*lines = append(*lines, text.JoinLiteral("", text.Plain(indent), g.TitleText))
	for _, row := range g.Rows {
		*lines = append(*lines, text.JoinLiteral("", text.Plain(indent+"  "), row.LabelText, text.Plain(": "), row.ValueText))
	}
}

func buildDiagReportLines(verdictText *corev1.Text, rounds []*transport.RoundData, connected bool, nowMs int64, platformAgent string) []*corev1.Text {
	lines := []*corev1.Text{
		text.Msg(text.KeyTransportDiagReportTitle),
		text.Msg(text.KeyTransportDiagReportVerdict, text.ArgText("verdict", verdictText)),
	}
	if platformAgent != "" {
		lines = append(lines, text.Msg(text.KeyTransportDiagReportBrowser, text.Str("value", platformAgent)))
	}
	for i, r := range rounds {
		if r == nil {
			continue
		}
		ageMs := diagNonNegDiff(nowMs, r.StartedAt)
		live := connected && i == 0
		headLeft := text.Msg(text.KeyTransportDiagReportRound,
			text.Int("n", int64(r.N)),
			text.ArgText("age", diagAgeText(ageMs)),
		)
		headRight := diagRoundSummaryText(r, live, nowMs)
		lines = append(lines, &corev1.Text{Literal: ""}, text.JoinLiteral(" · ", headLeft, headRight))
		for _, row := range diagSignalRows(r) {
			lines = append(lines, text.JoinLiteral("", text.Plain("  "), row.LabelText, text.Plain(": "), row.ValueText))
		}
		for i := range r.Pcs {
			pc := &r.Pcs[i]
			best := transport.BestAttempt(pc)
			stage := transport.StageOf(pc, best)
			useTxt := diagUseText(pc.Use)
			lines = append(lines, text.JoinLiteral("",
				text.Plain("  "),
				useTxt,
				text.Plain(" · "),
				text.Msg(text.KeyTransportDiagStage),
				text.Plain(": "),
				diagStageText(stage),
			))
			for _, g := range diagAddressGroups(pc, best) {
				diagAppendGroup(&lines, g, "    ")
			}
		}
	}
	return lines
}

func diagAddressGroups(pc *transport.PcData, a *transport.AttemptData) []*corev1.DiagGroupView {
	dash := text.Plain("—")
	if a == nil {
		checkRows := []*corev1.DiagRowView{{LabelText: text.Msg(text.KeyTransportDiagNoAttempt), ValueText: dash}}
		checkRows = append(checkRows, diagPcOutcomeRows(pc)...)
		return []*corev1.DiagGroupView{
			{TitleText: text.Msg(text.KeyTransportDiagGLocal), Rows: []*corev1.DiagRowView{{LabelText: text.Msg(text.KeyTransportDiagNoAttempt), ValueText: dash}}},
			{TitleText: text.Msg(text.KeyTransportDiagGPeer), Rows: []*corev1.DiagRowView{{LabelText: text.Msg(text.KeyTransportDiagNoAttempt), ValueText: dash}}},
			{TitleText: text.Msg(text.KeyTransportDiagGChecks), Rows: checkRows},
		}
	}

	hidden := a.Local.CountClass(transport.AddrMDNS)
	localRows := []*corev1.DiagRowView{
		{LabelText: text.Msg(text.KeyTransportDiagIpv4), ValueText: diagFamilyText(a.Local, transport.Family4)},
		{LabelText: text.Msg(text.KeyTransportDiagIpv6), ValueText: diagFamilyText(a.Local, transport.Family6)},
	}
	if hidden > 0 {
		localRows = append(localRows, &corev1.DiagRowView{
			LabelText: text.Msg(text.KeyTransportDiagHidden),
			ValueText: text.Msg(text.KeyTransportDiagCount, text.Int("count", int64(hidden))),
		})
	}
	if len(a.Stun) > 0 {
		parts := make([]*corev1.Text, 0, len(a.Stun))
		for _, e := range a.Stun {
			parts = append(parts, text.Msg(text.KeyTransportDiagStunError,
				text.Int("code", int64(e.Code)),
				text.ArgText("family", diagFamilyWord(e.Family)),
				text.Int("count", int64(e.N)),
			))
		}
		localRows = append(localRows, &corev1.DiagRowView{
			LabelText: text.Msg(text.KeyTransportDiagStunErrors),
			ValueText: text.JoinLiteral(" · ", parts...),
		})
	}
	if gather := diagTimeline(a, transport.StateGather); gather != nil {
		localRows = append(localRows, &corev1.DiagRowView{
			LabelText: text.Msg(text.KeyTransportDiagGather),
			ValueText: gather,
		})
	}

	peerRows := []*corev1.DiagRowView{
		{LabelText: text.Msg(text.KeyTransportDiagIpv4), ValueText: diagFamilyText(a.Remote, transport.Family4)},
		{LabelText: text.Msg(text.KeyTransportDiagIpv6), ValueText: diagFamilyText(a.Remote, transport.Family6)},
	}
	if pc != nil && len(pc.Rejects) > 0 {
		parts := make([]*corev1.Text, 0, len(pc.Rejects))
		for _, rej := range pc.Rejects {
			kind, reason, _ := strings.Cut(rej.Key, ":")
			parts = append(parts, text.Msg(text.KeyTransportDiagRejValue,
				text.ArgText("kind", diagRejectKind(kind)),
				text.Str("reason", reason),
				text.Int("count", int64(rej.N)),
			))
		}
		peerRows = append(peerRows, &corev1.DiagRowView{
			LabelText: text.Msg(text.KeyTransportDiagRejected),
			ValueText: text.JoinLiteral(" · ", parts...),
		})
	}

	var checkRows []*corev1.DiagRowView
	if a.Sampled && len(a.Pairs) > 0 {
		checkRows = diagPairRows(a)
	} else {
		checkRows = []*corev1.DiagRowView{{LabelText: text.Msg(text.KeyTransportDiagNotSampled), ValueText: dash}}
	}
	if activeRow := diagSelectedPairRow(a); activeRow != nil {
		checkRows = append(checkRows, activeRow)
	}
	if ice := diagTimeline(a, transport.StateICE); ice != nil {
		checkRows = append(checkRows, &corev1.DiagRowView{LabelText: text.Msg(text.KeyTransportDiagIce), ValueText: ice})
	}
	dtlsVal := diagLastState(a, transport.StateDTLS)
	if dtlsVal == nil {
		dtlsVal = diagLastState(a, transport.StateConn)
	}
	if dtlsVal == nil {
		dtlsVal = dash
	}
	dcVal := diagLastState(a, transport.StateDC)
	if dcVal == nil {
		dcVal = dash
	}
	checkRows = append(checkRows,
		&corev1.DiagRowView{LabelText: text.Msg(text.KeyTransportDiagDtls), ValueText: dtlsVal},
		&corev1.DiagRowView{LabelText: text.Msg(text.KeyTransportDiagChannel), ValueText: dcVal},
	)
	checkRows = append(checkRows, diagPcOutcomeRows(pc)...)

	return []*corev1.DiagGroupView{
		{TitleText: text.Msg(text.KeyTransportDiagGLocal), Rows: localRows},
		{TitleText: text.Msg(text.KeyTransportDiagGPeer), Rows: peerRows},
		{TitleText: text.Msg(text.KeyTransportDiagGChecks), Rows: checkRows},
	}
}

type diagPairSum struct {
	lf, rf         transport.Family
	ok, fail, wait int
	sent, got      int
	hasRTT         bool
	rttMs          int64
}

func diagFindPairSum(order *[]diagPairSum, lf, rf transport.Family) int {
	for i := range *order {
		if (*order)[i].lf == lf && (*order)[i].rf == rf {
			return i
		}
	}
	*order = append(*order, diagPairSum{lf: lf, rf: rf})
	return len(*order) - 1
}

func diagPairRows(a *transport.AttemptData) []*corev1.DiagRowView {
	var order []diagPairSum
	for _, p := range a.Pairs {
		idx := diagFindPairSum(&order, p.LocalFamily, p.RemoteFamily)
		g := &order[idx]
		g.ok += p.OK
		g.fail += p.Failed
		g.wait += p.Waiting
		g.sent += p.Sent
		g.got += p.Got
		if p.HasRTT && (!g.hasRTT || p.RTTMs < g.rttMs) {
			g.hasRTT, g.rttMs = true, p.RTTMs
		}
	}
	rows := make([]*corev1.DiagRowView, 0, len(order))
	for _, g := range order {
		var parts []*corev1.Text
		if g.ok > 0 {
			parts = append(parts, text.Msg(text.KeyTransportDiagPairOk, text.Int("count", int64(g.ok))))
		}
		if g.fail > 0 {
			parts = append(parts, text.Msg(text.KeyTransportDiagPairFailed, text.Int("count", int64(g.fail))))
		}
		if g.wait > 0 {
			parts = append(parts, text.Msg(text.KeyTransportDiagPairWait, text.Int("count", int64(g.wait))))
		}
		if g.hasRTT {
			parts = append(parts, text.Msg(text.KeyTransportDiagRtt, text.Int("ms", g.rttMs)))
		}
		parts = append(parts, text.Msg(text.KeyTransportDiagPairTraffic, text.Int("sent", int64(g.sent)), text.Int("got", int64(g.got))))
		rows = append(rows, &corev1.DiagRowView{
			LabelText: text.Msg(text.KeyTransportDiagPair,
				text.ArgText("local", diagFamilyWord(g.lf)),
				text.ArgText("remote", diagFamilyWord(g.rf)),
			),
			ValueText: text.JoinLiteral(" · ", parts...),
		})
	}
	return rows
}

func diagResultGroup(rounds []*transport.RoundData, chosen *transport.RoundData, connected bool, nowMs int64) *corev1.DiagGroupView {
	var rows []*corev1.DiagRowView
	if chosen != nil {
		_, _, stage := transport.LeadPc(chosen)
		if stage != transport.StageReady {
			rows = append(rows, &corev1.DiagRowView{
				LabelText: text.Msg(text.KeyTransportDiagStage),
				ValueText: diagStageText(stage),
			})
		}
		rows = append(rows, diagSignalRows(chosen)...)
	}
	for i, r := range rounds {
		if r == nil {
			continue
		}
		ageMs := diagNonNegDiff(nowMs, r.StartedAt)
		live := connected && i == 0
		rows = append(rows, &corev1.DiagRowView{
			LabelText: diagAgeText(ageMs),
			ValueText: diagRoundSummaryText(r, live, nowMs),
		})
	}
	return &corev1.DiagGroupView{
		TitleText: text.Msg(text.KeyTransportDiagGResult),
		Rows:      rows,
	}
}

func diagSignalRows(r *transport.RoundData) []*corev1.DiagRowView {
	if r == nil {
		return nil
	}
	var parts []*corev1.Text
	if r.Unsupported {
		parts = append(parts, text.Msg(text.KeyTransportDiagSigRefused))
	}
	if r.Limited > 0 {
		parts = append(parts, text.Msg(text.KeyTransportDiagSigLimited, text.Int("count", int64(r.Limited))))
	}
	if r.Offline > 0 {
		parts = append(parts, text.Msg(text.KeyTransportDiagSigOffline, text.Int("count", int64(r.Offline))))
	}
	if len(parts) == 0 {
		return nil
	}
	return []*corev1.DiagRowView{{
		LabelText: text.Msg(text.KeyTransportDiagSignal),
		ValueText: text.JoinLiteral(" · ", parts...),
	}}
}

type diagCandRow struct {
	k transport.CandKind
	n int
}

type diagCandRows []diagCandRow

func diagCandTypeRank(ct transport.CandType) int {
	switch ct {
	case transport.CandHost:
		return 0
	case transport.CandSrflx:
		return 1
	case transport.CandPrflx:
		return 2
	case transport.CandRelay:
		return 3
	default:
		return 4
	}
}

func (r diagCandRows) Len() int      { return len(r) }
func (r diagCandRows) Swap(i, j int) { r[i], r[j] = r[j], r[i] }
func (r diagCandRows) Less(i, j int) bool {
	ri, rj := diagCandTypeRank(r[i].k.Type), diagCandTypeRank(r[j].k.Type)
	if ri != rj {
		return ri < rj
	}
	return r[i].k.Class < r[j].k.Class
}

func diagFamilyText(t transport.Tally, fam transport.Family) *corev1.Text {
	var rows []diagCandRow
	for k, n := range t {
		if n > 0 && k.Family == fam && k.Class != transport.AddrMDNS {
			rows = append(rows, diagCandRow{k: k, n: n})
		}
	}
	if len(rows) == 0 {
		return text.Msg(text.KeyTransportDiagNone)
	}
	sortutil.SortInterface(diagCandRows(rows))
	parts := make([]*corev1.Text, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, text.Msg(text.KeyTransportDiagCand,
			text.ArgText("kind", diagTypeWord(r.k.Type)),
			text.Int("count", int64(r.n)),
			text.ArgText("scope", diagRangeWord(r.k.Class)),
		))
	}
	return text.JoinLiteral(" · ", parts...)
}

func diagTimeline(a *transport.AttemptData, kind transport.StateKind) *corev1.Text {
	if a == nil {
		return nil
	}
	var evs []transport.StateEvent
	for _, s := range a.States {
		if s.Kind == kind {
			evs = append(evs, s)
		}
	}
	if len(evs) == 0 {
		return nil
	}
	if len(evs) > 6 {
		evs = evs[len(evs)-6:]
	}
	var out []*corev1.Text
	lastKey := ""
	for _, e := range evs {
		word, key := diagStateWord(e.Value)
		if key == lastKey {
			continue
		}
		lastKey = key
		out = append(out, text.Msg(text.KeyTransportDiagAt,
			text.ArgText("state", word),
			text.Str("seconds", formatTenthsSeconds(e.T)),
		))
	}
	return text.JoinLiteral(" → ", out...)
}

func diagLastState(a *transport.AttemptData, kind transport.StateKind) *corev1.Text {
	if a == nil {
		return nil
	}
	var last *transport.StateEvent
	for i := range a.States {
		if a.States[i].Kind == kind {
			last = &a.States[i]
		}
	}
	if last == nil {
		return nil
	}
	word, _ := diagStateWord(last.Value)
	return word
}

func diagStateWord(v string) (*corev1.Text, string) {
	key := text.KeyTransportDiagStateNone
	switch v {
	case "checking", "connecting", "gathering", "closing":
		key = text.KeyTransportDiagStateDoing
	case "connected", "completed", "complete", "open":
		key = text.KeyTransportDiagStateOk
	case "failed":
		key = text.KeyTransportDiagStateFailed
	case "disconnected":
		key = text.KeyTransportDiagStateDropped
	case "closed":
		key = text.KeyTransportDiagStateClosed
	}
	return text.Msg(key), key
}

func diagVerdictText(v transport.Verdict) *corev1.Text {
	switch v {
	case transport.VerdictTrying:
		return text.Msg(text.KeyTransportStatusConnecting)
	case transport.VerdictNone:
		return text.Msg(text.KeyTransportDiagVerdictNone)
	case transport.VerdictConnected:
		return text.Msg(text.KeyTransportDiagVerdictConnected)
	case transport.VerdictLost:
		return text.Msg(text.KeyTransportDiagVerdictLost)
	case transport.VerdictCancelled:
		return text.Msg(text.KeyTransportDiagVerdictCancelled)
	case transport.VerdictNoReply:
		return text.Msg(text.KeyTransportDiagVerdictNoReply)
	case transport.VerdictPeerRefused:
		return text.Msg(text.KeyTransportDiagVerdictPeerRefused)
	case transport.VerdictSignalLimited:
		return text.Msg(text.KeyTransportDiagVerdictSignalLimited)
	case transport.VerdictSignalOffline:
		return text.Msg(text.KeyTransportDiagVerdictSignalOffline)
	case transport.VerdictNoLocal:
		return text.Msg(text.KeyTransportDiagVerdictNoLocal)
	case transport.VerdictNoSTUN:
		return text.Msg(text.KeyTransportDiagVerdictNoStun)
	case transport.VerdictNoRemote:
		return text.Msg(text.KeyTransportDiagVerdictNoRemote)
	case transport.VerdictNoSDP:
		return text.Msg(text.KeyTransportDiagVerdictNoSdp)
	case transport.VerdictIPv6Silent:
		return text.Msg(text.KeyTransportDiagVerdictIpv6Silent)
	case transport.VerdictAllFailed:
		return text.Msg(text.KeyTransportDiagVerdictAllFailed)
	case transport.VerdictUnselected:
		return text.Msg(text.KeyTransportDiagVerdictUnselected)
	case transport.VerdictDTLS:
		return text.Msg(text.KeyTransportDiagVerdictDtls)
	case transport.VerdictChannel:
		return text.Msg(text.KeyTransportDiagVerdictChannel)
	case transport.VerdictConfirm:
		return text.Msg(text.KeyTransportDiagVerdictConfirm)
	case transport.VerdictBudget:
		return text.Msg(text.KeyTransportDiagVerdictBudget)
	case transport.VerdictCreateFailed:
		return text.Msg(text.KeyTransportDiagVerdictCreateFailed)
	default:
		return text.Msg(text.KeyTransportDiagVerdictUnknown)
	}
}

func diagTypeWord(t transport.CandType) *corev1.Text {
	switch t {
	case transport.CandHost:
		return text.Msg(text.KeyTransportDiagTypeHost)
	case transport.CandSrflx:
		return text.Msg(text.KeyTransportDiagTypeSrflx)
	case transport.CandPrflx:
		return text.Msg(text.KeyTransportDiagTypePrflx)
	case transport.CandRelay:
		return text.Msg(text.KeyTransportDiagTypeRelay)
	default:
		return text.Msg(text.KeyTransportDiagTypeOther)
	}
}

func diagRangeWord(c transport.AddrClass) *corev1.Text {
	switch c {
	case transport.AddrPrivate:
		return text.Msg(text.KeyTransportDiagRangePrivate)
	case transport.AddrPublic:
		return text.Msg(text.KeyTransportDiagRangePublic)
	case transport.AddrCGNAT:
		return text.Msg(text.KeyTransportDiagRangeCgnat)
	case transport.AddrLinkLocal:
		return text.Msg(text.KeyTransportDiagRangeLinkLocal)
	case transport.AddrLoopback:
		return text.Msg(text.KeyTransportDiagRangeLoopback)
	case transport.AddrGlobal:
		return text.Msg(text.KeyTransportDiagRangeGlobal)
	case transport.AddrULA:
		return text.Msg(text.KeyTransportDiagRangeUla)
	default:
		return text.Msg(text.KeyTransportDiagRangeOther)
	}
}

func diagFamilyWord(f transport.Family) *corev1.Text {
	switch f {
	case transport.Family4:
		return text.Msg(text.KeyTransportDiagIpv4)
	case transport.Family6:
		return text.Msg(text.KeyTransportDiagIpv6)
	default:
		return text.Msg(text.KeyTransportDiagFamilyUnknown)
	}
}

func diagRejectKind(kind string) *corev1.Text {
	switch kind {
	case "sdp":
		return text.Msg(text.KeyTransportDiagRejSdp)
	case "candidates":
		return text.Msg(text.KeyTransportDiagRejCandidates)
	case "end":
		return text.Msg(text.KeyTransportDiagRejEnd)
	case "confirm":
		return text.Msg(text.KeyTransportDiagRejConfirm)
	case "browser":
		return text.Msg(text.KeyTransportDiagRejBrowser)
	default:
		return text.Msg(text.KeyTransportDiagRejSignal)
	}
}

func diagStageText(s transport.Stage) *corev1.Text {
	switch s {
	case transport.StageHandshake:
		return text.Msg(text.KeyTransportDiagStageHandshake)
	case transport.StageSDP:
		return text.Msg(text.KeyTransportDiagStageSdp)
	case transport.StageICE:
		return text.Msg(text.KeyTransportDiagStageIce)
	case transport.StageDTLS:
		return text.Msg(text.KeyTransportDiagStageDtls)
	case transport.StageChannel:
		return text.Msg(text.KeyTransportDiagStageChannel)
	case transport.StageConfirm:
		return text.Msg(text.KeyTransportDiagStageConfirm)
	default:
		return text.Msg(text.KeyTransportDiagStageReady)
	}
}

func diagSelectedPairRow(a *transport.AttemptData) *corev1.DiagRowView {
	if a == nil || !a.HasSelectedPair {
		return nil
	}
	sp := a.SelectedPair
	val := text.Msg(text.KeyTransportDiagActivePathValue,
		text.ArgText("localFamily", diagFamilyWord(sp.LocalFamily)),
		text.ArgText("remoteFamily", diagFamilyWord(sp.RemoteFamily)),
		text.ArgText("localType", diagTypeWord(sp.LocalType)),
		text.ArgText("remoteType", diagTypeWord(sp.RemoteType)),
	)
	switches := text.Msg(text.KeyTransportDiagActivePathSwitches, text.Int("count", int64(a.SelectedPairChanges)))
	return &corev1.DiagRowView{
		LabelText: text.Msg(text.KeyTransportDiagActivePath),
		ValueText: text.JoinLiteral(" · ", val, switches),
	}
}

func diagDurationText(ms int64) *corev1.Text {
	if ms < 0 {
		ms = 0
	}
	if ms < 1000 {
		return text.Msg(text.KeyTransportDiagRtt, text.Int("ms", ms))
	}
	return text.Msg(text.KeyTransportDiagSeconds, text.Str("seconds", formatTenthsSeconds(ms)))
}

func diagProbesValueText(p transport.ProbeStats, hasClosed bool) *corev1.Text {
	var onTime *corev1.Text
	if p.Echoed > 0 {
		onTime = text.Msg(text.KeyTransportDiagProbeOntimeMax,
			text.Int("count", int64(p.Echoed)),
			text.ArgText("max", diagDurationText(p.EchoMaxMs)),
		)
	} else {
		onTime = text.Msg(text.KeyTransportDiagProbeOntime, text.Int("count", 0))
	}
	var late *corev1.Text
	if p.Late > 0 {
		late = text.Msg(text.KeyTransportDiagProbeLateMax,
			text.Int("count", int64(p.Late)),
			text.ArgText("max", diagDurationText(p.LateMaxMs)),
		)
	} else {
		late = text.Msg(text.KeyTransportDiagProbeLate, text.Int("count", 0))
	}
	parts := []*corev1.Text{
		text.Msg(text.KeyTransportDiagProbeSent, text.Int("count", int64(p.Sent))),
		onTime,
		late,
		text.Msg(text.KeyTransportDiagProbeUnmatched, text.Int("count", int64(p.Unmatched))),
		text.Msg(text.KeyTransportDiagProbeLost, text.Int("count", int64(p.Lost))),
	}
	if hasClosed && p.HasLastEcho {
		parts = append(parts, text.Msg(text.KeyTransportDiagProbeLastEcho,
			text.Str("seconds", formatTenthsSeconds(p.LastEchoAgoMs)),
		))
	}
	return text.JoinLiteral(" · ", parts...)
}

func diagRxValueText(rx transport.DCRxStats, hasClosed bool) *corev1.Text {
	base := text.Msg(text.KeyTransportDiagRxCount,
		text.Int("count", rx.Frames),
		text.Str("size", sharedfiles.FormatFileSize(rx.Bytes)),
	)
	if hasClosed && rx.HasLastRx {
		last := text.Msg(text.KeyTransportDiagRxLastBeforeDrop,
			text.Str("seconds", formatTenthsSeconds(rx.LastRxAgoMs)),
		)
		return text.JoinLiteral(" · ", base, last)
	}
	return base
}

func diagPcOutcomeRows(pc *transport.PcData) []*corev1.DiagRowView {
	if pc == nil {
		return nil
	}
	var rows []*corev1.DiagRowView
	if outcome := diagPcOutcomeText(pc); outcome != nil {
		rows = append(rows, &corev1.DiagRowView{
			LabelText: text.Msg(text.KeyTransportDiagOutcome),
			ValueText: outcome,
		})
	}
	if pc.Probes.Sent > 0 {
		rows = append(rows, &corev1.DiagRowView{
			LabelText: text.Msg(text.KeyTransportDiagProbes),
			ValueText: diagProbesValueText(pc.Probes, pc.HasClosed),
		})
	}
	if pc.HasMark[transport.MarkReady] || pc.Rx.Frames > 0 {
		rows = append(rows, &corev1.DiagRowView{
			LabelText: text.Msg(text.KeyTransportDiagRx),
			ValueText: diagRxValueText(pc.Rx, pc.HasClosed),
		})
	}
	return rows
}

func diagPcOutcomeText(pc *transport.PcData) *corev1.Text {
	if pc == nil {
		return nil
	}
	if pc.HasMark[transport.MarkReady] && pc.HasClosed {
		secs := formatTenthsSeconds(pc.LivedMs)
		if cause := diagCloseCauseText(pc.CloseCause, pc.Probes.Missed); cause != nil {
			return text.Msg(text.KeyTransportDiagLivedDroppedCause,
				text.Str("seconds", secs),
				text.ArgText("cause", cause),
			)
		}
		return text.Msg(text.KeyTransportDiagLivedDropped, text.Str("seconds", secs))
	}
	return diagCloseCauseText(pc.CloseCause, pc.Probes.Missed)
}

// closeCauseRank orders how much a cause tells the user: a specific cause beats
// the generic local-engine close, which beats no cause.
func closeCauseRank(c transport.CloseCause) int {
	switch c {
	case transport.CloseNone:
		return 0
	case transport.CloseEngine:
		return 1
	default:
		return 2
	}
}

func diagRoundDropSummary(r *transport.RoundData) *corev1.Text {
	if r == nil || !r.HasEstablished {
		return nil
	}
	var best *transport.PcData
	for i := range r.Pcs {
		pc := &r.Pcs[i]
		if !pc.HasMark[transport.MarkReady] || !pc.HasClosed {
			continue
		}
		if best == nil || closeCauseRank(pc.CloseCause) > closeCauseRank(best.CloseCause) ||
			closeCauseRank(pc.CloseCause) == closeCauseRank(best.CloseCause) && pc.LivedMs > best.LivedMs {
			best = pc
		}
	}
	if best != nil {
		cause := best.CloseCause
		if cause == transport.CloseNone {
			cause = r.CloseCause
		}
		secs := formatTenthsSeconds(best.LivedMs)
		if causeTxt := diagCloseCauseText(cause, best.Probes.Missed); causeTxt != nil {
			return text.Msg(text.KeyTransportDiagLivedDroppedCause,
				text.Str("seconds", secs),
				text.ArgText("cause", causeTxt),
			)
		}
		return text.Msg(text.KeyTransportDiagLivedDropped, text.Str("seconds", secs))
	}
	if r.End == transport.EndNone && r.CloseCause == transport.CloseNone {
		return nil
	}
	livedMs := max(0, r.EndedAt-r.Established)
	secs := formatTenthsSeconds(livedMs)
	if causeTxt := diagCloseCauseText(r.CloseCause, 0); causeTxt != nil {
		return text.Msg(text.KeyTransportDiagLivedDroppedCause,
			text.Str("seconds", secs),
			text.ArgText("cause", causeTxt),
		)
	}
	if r.EndedAt > 0 || r.Established > 0 {
		return text.Msg(text.KeyTransportDiagLivedDropped, text.Str("seconds", secs))
	}
	return nil
}

func diagCloseCauseText(cause transport.CloseCause, missed int) *corev1.Text {
	switch cause {
	case transport.CloseProbeTimeout:
		n := missed
		if n <= 0 {
			n = linkcore.ProbeMaxMissed
		}
		return text.Msg(text.KeyTransportDiagCloseProbeTimeout, text.Int("count", int64(n)))
	case transport.ClosePCFailed:
		return text.Msg(text.KeyTransportDiagClosePcFailed)
	case transport.ClosePCDisconnected:
		return text.Msg(text.KeyTransportDiagClosePcDisconnected)
	case transport.CloseDCClosed:
		return text.Msg(text.KeyTransportDiagCloseDcClosed)
	case transport.ClosePeerClosed:
		return text.Msg(text.KeyTransportDiagClosePeerClosed)
	case transport.CloseNetworkChanged:
		return text.Msg(text.KeyTransportDiagCloseNetworkChanged)
	case transport.CloseBackground:
		return text.Msg(text.KeyTransportDiagCloseBackground)
	case transport.CloseDisabled:
		return text.Msg(text.KeyTransportDiagCloseDisabled)
	case transport.CloseEngine:
		return text.Msg(text.KeyTransportDiagCloseEngine)
	case transport.CloseTimeout:
		return text.Msg(text.KeyTransportDiagCloseTimeout)
	default:
		return nil
	}
}

func diagRoundSummaryText(r *transport.RoundData, live bool, nowMs int64) *corev1.Text {
	if r.HasEstablished && (r.End != transport.EndNone || !live) {
		if summary := diagRoundDropSummary(r); summary != nil {
			return summary
		}
	}
	took := diagRoundDuration(r, nowMs)
	return text.Msg(text.KeyTransportDiagRound,
		text.ArgText("result", diagResultText(r, live)),
		text.Str("seconds", formatTenthsSeconds(took)),
	)
}

func diagResultText(r *transport.RoundData, live bool) *corev1.Text {
	if r.HasEstablished {
		if r.End != transport.EndNone || !live {
			return text.Msg(text.KeyTransportDiagResultLost)
		}
		return text.Msg(text.KeyTransportDiagResultOk)
	}
	switch r.End {
	case transport.EndNone:
		return text.Msg(text.KeyTransportDiagResultTrying)
	case transport.EndNoResponse:
		return text.Msg(text.KeyTransportDiagResultNoResponse)
	case transport.EndPeerUnsupported:
		return text.Msg(text.KeyTransportDiagResultRefused)
	case transport.EndCancelled:
		return text.Msg(text.KeyTransportDiagResultCancelled)
	case transport.EndRetired:
		return text.Msg(text.KeyTransportDiagResultRetired)
	default:
		return text.Msg(text.KeyTransportDiagResultTimeout)
	}
}

func diagUseText(u transport.Use) *corev1.Text {
	if u == transport.UseRead {
		return text.Msg(text.KeyTransportDiagUseRead)
	}
	return text.Msg(text.KeyTransportDiagUseFile)
}

func diagAgeText(ms int64) *corev1.Text {
	if ms < 60_000 {
		return text.Msg(text.KeyTimeJustNow)
	}
	if ms < 3_600_000 {
		return text.Msg(text.KeyTimeMinutesAgo, text.Int("count", ms/60_000))
	}
	return text.Msg(text.KeyTimeHoursAgo, text.Int("count", ms/3_600_000))
}

func diagNonNegDiff(nowMs, startMs int64) int64 {
	if nowMs <= startMs || startMs == 0 {
		return 0
	}
	return nowMs - startMs
}

func diagRoundDuration(r *transport.RoundData, nowMs int64) int64 {
	if r.End != transport.EndNone {
		return max(0, r.EndedAt)
	}
	if r.HasEstablished {
		return max(0, r.Established)
	}
	return diagNonNegDiff(nowMs, r.StartedAt)
}

func formatTenthsSeconds(ms int64) string {
	if ms <= 0 {
		return "0.0"
	}
	tenths := (ms + 50) / 100
	return strconv.FormatInt(tenths/10, 10) + "." + strconv.FormatInt(tenths%10, 10)
}
