package screens

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

var weekdayCodes = [7]string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

func mapTone(t lists.SessionTone) corev1.SessionTone {
	switch t {
	case lists.ToneWait:
		return corev1.SessionTone_SESSION_TONE_WARN
	case lists.ToneRun:
		return corev1.SessionTone_SESSION_TONE_LIVE
	default:
		return corev1.SessionTone_SESSION_TONE_NEUTRAL
	}
}

func mapStatus(s lists.SessionDisplayStatus) corev1.SessionDisplayStatus {
	switch s {
	case lists.StatusStarting:
		return corev1.SessionDisplayStatus_SESSION_DISPLAY_STATUS_STARTING
	case lists.StatusRunning:
		return corev1.SessionDisplayStatus_SESSION_DISPLAY_STATUS_RUNNING
	case lists.StatusIdle:
		return corev1.SessionDisplayStatus_SESSION_DISPLAY_STATUS_IDLE
	case lists.StatusApproval:
		return corev1.SessionDisplayStatus_SESSION_DISPLAY_STATUS_APPROVAL
	case lists.StatusQuestion:
		return corev1.SessionDisplayStatus_SESSION_DISPLAY_STATUS_QUESTION
	case lists.StatusPlan:
		return corev1.SessionDisplayStatus_SESSION_DISPLAY_STATUS_PLAN
	case lists.StatusPopup:
		return corev1.SessionDisplayStatus_SESSION_DISPLAY_STATUS_POPUP
	case lists.StatusOffline:
		return corev1.SessionDisplayStatus_SESSION_DISPLAY_STATUS_OFFLINE
	case lists.StatusEnded:
		return corev1.SessionDisplayStatus_SESSION_DISPLAY_STATUS_ENDED
	case lists.StatusUnlinked:
		return corev1.SessionDisplayStatus_SESSION_DISPLAY_STATUS_UNLINKED
	default:
		return corev1.SessionDisplayStatus_SESSION_DISPLAY_STATUS_UNSPECIFIED
	}
}

func statusText(st lists.SessionDisplayStatus) *corev1.Text {
	switch st {
	case lists.StatusStarting:
		return text.Msg(text.KeyListStateStarting)
	case lists.StatusRunning:
		return text.Msg(text.KeyListStateRunning)
	case lists.StatusIdle:
		return text.Msg(text.KeyListStateIdle)
	case lists.StatusApproval:
		return text.Msg(text.KeyListStateApproval)
	case lists.StatusQuestion:
		return text.Msg(text.KeyListStateQuestion)
	case lists.StatusPlan:
		return text.Msg(text.KeyListStatePlan)
	case lists.StatusPopup:
		return text.Msg(text.KeyListStatePopup)
	case lists.StatusOffline:
		return text.Msg(text.KeyListStateOffline)
	case lists.StatusEnded:
		return text.Msg(text.KeyListStateEnded)
	case lists.StatusUnlinked:
		return text.Msg(text.KeyListStateUnlinked)
	case lists.StatusLoading:
		return text.Msg(text.KeyCommonLoading)
	default:
		return nil
	}
}

func sessionTitleText(item *lists.SessionItem) *corev1.Text {
	if item == nil {
		return text.Msg(text.KeyListStateNewSession)
	}
	if item.ResolvedTitle != "" {
		return text.Plain(item.ResolvedTitle)
	}
	return text.Msg(text.KeyListStateNewSession)
}

func formatClockPair(hhmm string) (string, string) {
	for i := 0; i < len(hhmm); i++ {
		if hhmm[i] == ':' {
			return hhmm[:i], hhmm[i+1:]
		}
	}
	return hhmm, ""
}

func formatEndedTime(ms int64, s *lists.Session, g *lists.EndedGroup, cal lists.Calendar) *corev1.Text {
	if ms <= 0 {
		return nil
	}
	hhmm := ""
	if s != nil {
		hhmm = s.UpdatedLocalHhmm
	}
	if hhmm == "" {
		hhmm = lists.FormatHHMM(ms, cal.UTCOffsetMinutes)
	}
	if g == nil {
		eg := lists.EndedGroupFor(ms, cal, 0, 0)
		g = &eg
	}
	if g.Kind == lists.GroupToday {
		h, m := formatClockPair(hhmm)
		return text.Msg(text.KeyTimeTodayClock, text.Str("value", h), text.Str("detail", m))
	}
	if g.Kind == lists.GroupYesterday {
		return text.Msg(text.KeyTimeDayClock, text.ArgText("day", text.Msg(text.KeyTimeYesterday)), text.Str("clock", hhmm))
	}
	if g.Kind == lists.GroupWeek {
		idx := int(g.WeekdayIndex)
		if idx < 0 || idx >= len(weekdayCodes) {
			idx = 0
		}
		day := text.Msg(text.KeyTimeWeekday, text.Str("day", weekdayCodes[idx]))
		return text.Msg(text.KeyTimeDayClock, text.ArgText("day", day), text.Str("clock", hhmm))
	}
	yr, mo, d, _, _, _ := lists.CivilFromUnixMs(ms, cal.UTCOffsetMinutes)
	if s != nil && s.EndedLocalYear > 0 && s.EndedLocalMonth > 0 {
		yr = int(s.EndedLocalYear)
		mo = int(s.EndedLocalMonth)
	}
	if cal.NowYear > 0 && int32(yr) != cal.NowYear {
		return text.Msg(text.KeyTimeCalendarDateYear, text.Int("year", int64(yr)), text.Int("month", int64(mo)), text.Int("day", int64(d)))
	}
	return text.Msg(text.KeyTimeDateTime, text.Int("month", int64(mo)), text.Int("day", int64(d)), text.Str("time", hhmm))
}

func formatRelativeTime(ms int64, cal lists.Calendar) *corev1.Text {
	if ms <= 0 || cal.NowMs <= 0 {
		return nil
	}
	diff := cal.NowMs - ms
	if diff < 60_000 {
		return text.Msg(text.KeyTimeJustNow)
	}
	mins := diff / 60_000
	if mins < 60 {
		return text.Msg(text.KeyTimeMinutesAgo, text.Int("count", mins))
	}
	if cal.TodayStartMs > 0 && ms >= cal.TodayStartMs {
		return text.Msg(text.KeyTimeHoursAgo, text.Int("count", mins/60))
	}
	if cal.YesterdayStartMs > 0 && ms >= cal.YesterdayStartMs {
		return text.Msg(text.KeyTimeYesterday)
	}
	if cal.WeekStartMs > 0 && ms >= cal.WeekStartMs {
		g := lists.EndedGroupFor(ms, cal, 0, 0)
		idx := int(g.WeekdayIndex)
		if idx < 0 || idx >= len(weekdayCodes) {
			idx = 0
		}
		return text.Msg(text.KeyTimeWeekday, text.Str("day", weekdayCodes[idx]))
	}
	yr, mo, d, _, _, _ := lists.CivilFromUnixMs(ms, cal.UTCOffsetMinutes)
	if cal.NowYear > 0 && int32(yr) != cal.NowYear {
		return text.Msg(text.KeyTimeCalendarDateYear, text.Int("year", int64(yr)), text.Int("month", int64(mo)), text.Int("day", int64(d)))
	}
	return text.Msg(text.KeyTimeMonthDay, text.Int("month", int64(mo)), text.Int("day", int64(d)))
}

func buildConnectionStatus(st *State) *corev1.ConnectionStatusView {
	view := &corev1.ConnectionStatusView{
		LivePushConnected: st.LivePushConnected,
		Reason:            st.ConnectionReason,
		DisconnectedAtMs:  st.DisconnectedAtMs,
	}
	switch st.World.Connection {
	case lists.ConnConnected:
		view.State = corev1.ConnState_CONN_STATE_CONNECTED
		view.BadgeText = text.Msg(text.KeyConnectionConnected)
		if !st.LivePushConnected {
			if st.ConnectionReason != "" {
				view.DetailText = text.Msg(text.KeyConnectionLivePushIsNotConnectedSessionChangesMayBe, text.Str("value", st.ConnectionReason))
			} else {
				view.DetailText = text.Msg(text.KeyConnectionLivePushIsNotConnected)
			}
		}
	case lists.ConnReconnecting:
		view.State = corev1.ConnState_CONN_STATE_RECONNECTING
		view.BadgeText = text.Msg(text.KeyConnectionReconnectingConnCheck)
		view.DetailText = text.Msg(text.KeyConnectionReconnecting)
	case lists.ConnOffline:
		view.State = corev1.ConnState_CONN_STATE_OFFLINE
		view.BadgeText = text.Msg(text.KeyConnectionUnreachable)
		if st.ConnectionReason != "" {
			view.DetailText = text.Msg(text.KeyConnectionServerDetails, text.Str("reason", st.ConnectionReason))
		} else {
			view.DetailText = text.Msg(text.KeyConnectionNoResponse)
		}
	case lists.ConnNoNetwork:
		view.State = corev1.ConnState_CONN_STATE_NO_NETWORK
		view.BadgeText = text.Msg(text.KeyConnectionUnavailable)
		view.DetailText = text.Msg(text.KeyConnectionThisDeviceHasNoNetworkItWillReconnectWhen)
	default:
		view.State = corev1.ConnState_CONN_STATE_CONNECTING
		view.BadgeText = text.Msg(text.KeyConnectionConnectingConnCheck)
		view.DetailText = text.Msg(text.KeyConnectionConnecting)
	}
	return view
}

func buildSessionRow(item *lists.SessionItem, meta lists.RowMeta, cal lists.Calendar, selected bool) *corev1.SessionRowView {
	title := sessionTitleText(item)
	stText := statusText(item.ShownStatus)
	var timeText *corev1.Text
	if item.NeedsAttention && item.CreatedMs > 0 {
		timeText = formatRelativeTime(item.CreatedMs, cal)
	} else if item.CreatedHHMM != "" {
		timeText = text.Plain(item.CreatedHHMM)
	} else if item.UpdatedMs > 0 {
		timeText = formatRelativeTime(item.UpdatedMs, cal)
	}

	var preview *corev1.Text
	switch item.ShownStatus {
	case lists.StatusPopup:
		if item.Session != nil && item.Session.Pending.PopupPreview != "" {
			preview = text.Plain(item.Session.Pending.PopupPreview)
		} else if item.StatusDetail != "" {
			preview = text.Plain(item.StatusDetail)
		}
	case lists.StatusApproval:
		if item.Session != nil && item.Session.Pending.HasApproval {
			if item.Session.Pending.ApprovalCommand != "" {
				preview = text.Msg(text.KeyListStateRequestCommand, text.Str("command", lists.ApprovalDisplay(item.Session.Pending.ApprovalCommand)))
			} else if item.Session.Pending.ApprovalToolName != "" {
				preview = text.Msg(text.KeyListStateRequestPermission, text.Str("tool", lists.ApprovalDisplay(item.Session.Pending.ApprovalToolName)))
			}
		}
	}
	if text.IsEmpty(preview) {
		if item.PlainPreview != "" {
			preview = text.Plain(item.PlainPreview)
		} else {
			preview = text.Msg(text.KeyListStateNoMessage)
		}
	}

	var metaParts []*corev1.Text
	if item.Forked {
		metaParts = append(metaParts, text.Msg(text.KeyHomeForked))
	}
	if meta.ShowProject && item.ProjectName != "" {
		metaParts = append(metaParts, text.Plain(item.ProjectName))
	}
	if meta.ShowMachine && item.MachineName != "" {
		metaParts = append(metaParts, text.Plain(item.MachineName))
	}
	if item.Session != nil && item.Session.ScheduleName != "" {
		metaParts = append(metaParts, text.Msg(text.KeyHomeScheduledFrequency, text.Str("frequency", item.Session.ScheduleName)))
	}
	metaLine := text.JoinLiteral(" · ", metaParts...)

	var secondLine *corev1.Text
	if item.ShownStatus == lists.StatusRunning || item.ShownStatus == lists.StatusStarting {
		secondLine = preview
	} else if !text.IsEmpty(stText) {
		secondLine = text.JoinLiteral(" · ", stText, preview)
	} else {
		secondLine = preview
	}

	return &corev1.SessionRowView{
		SessionId:     item.ID,
		MachineId:     item.MachineID,
		Title:         title,
		Time:          timeText,
		Status:        mapStatus(item.ShownStatus),
		Tone:          mapTone(item.Tone),
		StatusText:    stText,
		Preview:       preview,
		SecondLine:    secondLine,
		Agent:          item.Agent,
		MultipleAgents: meta.ShowAgent,
		MetaLine:       metaLine,
		Selected:      selected,
		PendingAction: item.NeedsAttention,
	}
}

func buildEndedRow(item *lists.SessionItem, meta lists.RowMeta, cal lists.Calendar, selected bool) *corev1.EndedRowView {
	title := sessionTitleText(item)
	endedTime := formatEndedTime(item.EndedMs, item.Session, item.EndedGroup, cal)
	var rightParts []*corev1.Text
	if meta.ShowProject && item.ProjectName != "" {
		rightParts = append(rightParts, text.Plain(item.ProjectName))
	}
	if meta.ShowMachine && item.MachineName != "" {
		rightParts = append(rightParts, text.Plain(item.MachineName))
	}
	if !text.IsEmpty(endedTime) {
		rightParts = append(rightParts, endedTime)
	}
	return &corev1.EndedRowView{
		SessionId: item.ID,
		MachineId: item.MachineID,
		Title:     title,
		Right:     text.JoinLiteral(" · ", rightParts...),
		Selected:  selected,
	}
}

func buildTailRow(tail *lists.HomeTailInfo) *corev1.TailRowView {
	if tail == nil {
		return nil
	}
	row := &corev1.TailRowView{
		Retry: tail.Retry,
	}
	switch tail.Kind {
	case lists.TailEarlierOffline:
		row.Kind = corev1.TailKind_TAIL_KIND_OFFLINE
		row.Text = text.Msg(text.KeyHomeEarlierOffline)
	case lists.TailEarlierListFailed:
		row.Kind = corev1.TailKind_TAIL_KIND_FAILED
		row.Text = text.Msg(text.KeyHomeListFailedRetry, text.Str("reason", tail.RawError))
	case lists.TailEarlierFailed:
		row.Kind = corev1.TailKind_TAIL_KIND_FAILED
		row.Text = text.Msg(text.KeyHomeEarlierFailedRetry, text.Str("reason", tail.RawError))
	case lists.TailLoadingEarlier:
		row.Kind = corev1.TailKind_TAIL_KIND_LOADING
		row.Text = text.Msg(text.KeyHomeLoadingEarlier)
	case lists.TailFilterNoMatches:
		row.Kind = corev1.TailKind_TAIL_KIND_MORE_ENDED
		row.Text = text.Msg(text.KeyHomeNoMatches, text.Str("query", tail.Query))
	case lists.TailFilterMatches:
		row.Kind = corev1.TailKind_TAIL_KIND_MORE_ENDED
		row.Text = text.Msg(text.KeyHomeMatches, text.Int("count", int64(tail.Count)), text.Str("query", tail.Query))
	case lists.TailTotalLoadEarlier:
		row.Kind = corev1.TailKind_TAIL_KIND_MORE_ON_SERVER
		row.Text = text.Msg(text.KeyHomeTotalLoadEarlier, text.Int("count", int64(tail.Count)))
	case lists.TailTotal:
		row.Kind = corev1.TailKind_TAIL_KIND_MORE_ENDED
		row.Text = text.Msg(text.KeyHomeTotal, text.Int("count", int64(tail.Count)))
	}
	return row
}

func projectSubtitleText(p *lists.ProjectSummary, cal lists.Calendar, includeActivity bool) *corev1.Text {
	if p == nil {
		return nil
	}
	var parts []*corev1.Text
	if includeActivity {
		switch {
		case p.WaitingCount > 0 && p.LiveCount > 0:
			parts = append(parts, text.Msg(text.KeyProjectsPendingAndLive, text.Int("count", int64(p.WaitingCount)), text.Int("live", int64(p.LiveCount))))
		case p.WaitingCount > 0:
			parts = append(parts, text.Msg(text.KeyProjectsPendingCount, text.Int("count", int64(p.WaitingCount))))
		case p.LiveCount > 0:
			parts = append(parts, text.Msg(text.KeyProjectsLiveCount, text.Int("count", int64(p.LiveCount))))
		}
	}
	if p.AllOffline {
		parts = append(parts, text.Msg(text.KeyProjectsComputerOffline))
	} else if p.UpdatedMs > 0 {
		rel := formatRelativeTime(p.UpdatedMs, cal)
		if !text.IsEmpty(rel) {
			parts = append(parts, text.Msg(text.KeyTimeUsedTime, text.ArgText("time", rel)))
		}
	}
	return text.JoinLiteral(" · ", parts...)
}

func buildProjectRow(p *lists.ProjectSummary, st *State) *corev1.ProjectRowView {
	nameText := text.Plain(p.Name)
	if p.Name == "" {
		nameText = text.Msg(text.KeyProjectsUnknown)
	}
	sub := projectSubtitleText(p, st.Cal, true)
	var saveErr *corev1.Text
	if msg := st.PrefsSaveErrorByPath[p.Key]; msg != "" {
		saveErr = text.Msg(text.KeyCommonSaveFailed, text.Str("reason", msg))
	}
	return &corev1.ProjectRowView{
		Key:          p.Key,
		Name:         nameText,
		ShortPath:    p.ShortPath,
		Subtitle:     sub,
		WaitingCount: p.WaitingCount,
		LiveCount:    p.LiveCount,
		AllOffline:   p.AllOffline,
		Pinned:       p.Pinned,
		Hidden:       p.Hidden,
		Syncing:      st.PrefsPendingPath[p.Key],
		SaveError:    saveErr,
	}
}

func buildProjectListing(res *lists.ProjectsViewResult, st *State, canSearch bool) *corev1.ProjectListingView {
	pinned := make([]*corev1.ProjectRowView, 0, len(res.FilteredPin))
	for _, p := range res.FilteredPin {
		pinned = append(pinned, buildProjectRow(p, st))
	}
	others := make([]*corev1.ProjectRowView, 0, len(res.FilteredOther))
	for _, p := range res.FilteredOther {
		others = append(others, buildProjectRow(p, st))
	}
	searching := st.ProjectSearch != ""
	var emptyMatch *corev1.Text
	if searching && len(pinned) == 0 && len(others) == 0 {
		emptyMatch = text.Msg(text.KeyProjectsSearchEmpty, text.Str("query", st.ProjectSearch))
	}
	return &corev1.ProjectListingView{
		Pinned:       pinned,
		Others:       others,
		Titled:       len(pinned) > 0 && len(others) > 0,
		CanSearch:    canSearch && len(res.Listed) > lists.FilterOver,
		Searching:    searching,
		SearchQuery:  st.ProjectSearch,
		EmptyMatch:   emptyMatch,
		WaitingTotal: res.WaitingTotal,
		Loading:      !st.World.PrefsLoaded && len(res.Listed) == 0 && !st.World.PrefsLoadFailed,
		Syncing:      st.World.PrefsSyncing,
		LoadFailed:   st.World.PrefsLoadFailed,
	}
}
