package screens

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

// BuildHome constructs the HomeViewModel using an optional ended-list data window (endedLimit).
// endedLimit == 0 defaults to lists.EndedPage (30); endedLimit < 0 returns all held ended items.
func BuildHome(st *State, endedLimit int) *corev1.HomeViewModel {
	if st == nil {
		st = NewState()
	}
	dw := displayWorld(st)
	projRes, homeData := lists.BuildProjectsAndHome(&dw, st.RememberedScope, st.RememberedScopeToken, nil, st.Scope, st.ProjectSearch, st.HomeFilterQuery, st.Cal, lists.HomeShown{
		AllAttention:      true,
		AllRunning:        true,
		Ended:             endedLimit,
		SkipAttentionCard: true,
	})

	scope := homeData.ResolvedScope
	var scopeTitle *corev1.Text
	if scope.AllProjects {
		scopeTitle = text.Msg(text.KeyHomeAllProjects)
	} else if scope.Name != "" {
		scopeTitle = text.Plain(scope.Name)
	} else {
		scopeTitle = text.Msg(text.KeyProjectsUnknown)
	}

	canStart := false
	for _, m := range dw.Machines {
		if lists.MachineReachable(m) {
			canStart = true
			break
		}
	}

	vm := &corev1.HomeViewModel{
		ScopeSelectable:   len(projRes.Listed) > 1,
		ScopeKey:          scope.Key,
		ScopeTitle:        scopeTitle,
		ScopeShortPath:    scope.ShortPath,
		Connection:        buildConnectionStatus(st),
		Headline:          buildHomeHeadlineView(homeData.Headline, st.World.LiveLoaded, st.SessionListErrorText),
		Empty:             buildHomeEmptyView(homeData.Empty, st),
		CanFilter:         homeData.ShowFilter,
		FilterQuery:       st.HomeFilterQuery,
		WaitingTotal:      homeData.WaitingTotal,
		LiveTotal:         homeData.RunningTotal,
		EndedTotal:        homeData.EndedTotal,
		TimelineTotal:     homeData.TimelineTotal,
		CanStart:          canStart,
		SelectedSessionId: st.HomeSelectedSessionID,
		HasMoreEnded:      homeData.HasMoreEnded,
	}

	if st.HomeFilterQuery != "" {
		if homeData.MatchedSize == 0 {
			vm.NoMatches = !homeData.WantAll
			if vm.NoMatches {
				vm.FilterSummary = text.Msg(text.KeyHomeNoMatches, text.Str("query", st.HomeFilterQuery))
			}
		} else if homeData.WantAll {
			vm.FilterSummary = text.Msg(text.KeyHomeMatchesLoading, text.Int("count", int64(homeData.MatchedSize)), text.Str("query", st.HomeFilterQuery))
		} else {
			vm.FilterSummary = text.Msg(text.KeyHomeMatches, text.Int("count", int64(homeData.MatchedSize)), text.Str("query", st.HomeFilterQuery))
		}
	}

	var curGroup *corev1.EndedGroupView
	for _, row := range homeData.Items {
		switch row.Kind {
		case lists.HomeRowAttention:
			if vm.TopAttentionSessionId == "" && row.Item != nil {
				vm.TopAttentionSessionId = row.Item.ID
				if row.Item.RawStatus == lists.StatusApproval {
					vm.TopAttentionActionable = true
					vm.TopAttentionCard = buildAttentionCardView(row.Item, st)
				}
			}
			vm.Attention = append(vm.Attention, buildAttentionRowView(row.Item, homeData.Meta))
		case lists.HomeRowRunning:
			vm.Running = append(vm.Running, buildSessionRow(row.Item, homeData.Meta, st.Cal, st.HomeSelectedSessionID == row.Item.ID))
		case lists.HomeRowSectionLabel:
			if row.EndedGroup != nil {
				gk, label := mapEndedGroup(row.EndedGroup, st.Cal)
				curGroup = &corev1.EndedGroupView{
					GroupKey:  row.EndedGroup.Key,
					GroupKind: gk,
					Label:     label,
				}
				vm.EndedGroups = append(vm.EndedGroups, curGroup)
			}
		case lists.HomeRowEnded:
			if curGroup != nil {
				curGroup.Items = append(curGroup.Items, buildEndedRow(row.Item, homeData.Meta, st.Cal, st.HomeSelectedSessionID == row.Item.ID))
			}
		case lists.HomeRowTail:
			vm.EndedTail = buildTailRow(row.Tail, st.SessionListErrorText)
		}
	}

	vm.ScopeMenu = make([]*corev1.ScopeRowView, 0, len(projRes.ScopeRows))
	for _, sr := range projRes.ScopeRows {
		if sr.Scope.AllProjects {
			vm.ScopeMenu = append(vm.ScopeMenu, &corev1.ScopeRowView{
				Key:          "",
				Name:         text.Msg(text.KeyHomeAllProjects),
				Subtitle:     text.Msg(text.KeyProjectsCount, text.Int("count", int64(sr.ProjectCount))),
				WaitingCount: sr.WaitingCount,
				LiveCount:    sr.LiveCount,
				Checked:      sr.Current,
				AllProjects:  true,
			})
			continue
		}
		var nameText *corev1.Text
		if sr.Scope.Name != "" {
			nameText = text.Plain(sr.Scope.Name)
		} else {
			nameText = text.Msg(text.KeyProjectsUnknown)
		}
		allOffline := false
		if sr.Project != nil {
			allOffline = sr.Project.AllOffline
		}
		vm.ScopeMenu = append(vm.ScopeMenu, &corev1.ScopeRowView{
			Key:          sr.Scope.Key,
			Name:         nameText,
			ShortPath:    sr.Scope.ShortPath,
			Subtitle:     projectSubtitleText(sr.Project, st.Cal, true),
			WaitingCount: sr.WaitingCount,
			LiveCount:    sr.LiveCount,
			AllOffline:   allOffline,
			Checked:      sr.Current,
			AllProjects:  false,
		})
	}

	vm.SidebarProjects = buildProjectListing(projRes, st, true)
	return vm
}

func displayWorld(st *State) lists.World {
	if st == nil || !st.DisplayGrace {
		return st.World
	}
	w := st.World
	w.Connection = lists.ConnConnected
	unlinked := false
	w.Unlinked = &unlinked
	if len(st.World.Machines) > 0 {
		machines := make([]*lists.Machine, len(st.World.Machines))
		for i, m := range st.World.Machines {
			if m == nil {
				continue
			}
			mc := *m
			reachable := mc.Online || (m.Reachable != nil && *m.Reachable)
			mUnlinked := false
			mc.Reachable = &reachable
			mc.Unlinked = &mUnlinked
			machines[i] = &mc
		}
		w.Machines = machines
	}
	return w
}

func buildHomeHeadlineView(h *lists.HomeHeadlineInfo, liveLoaded bool, errText *corev1.Text) *corev1.HomeHeadlineView {
	if h == nil {
		return &corev1.HomeHeadlineView{
			Kind: corev1.HeadlineKind_HEADLINE_KIND_EMPTY,
		}
	}
	v := &corev1.HomeHeadlineView{
		SubError:      h.SubError,
		SubPath:       h.SubPath,
		DetailsAction: h.Details,
		Unlinked:      h.Unlinked,
	}
	runningSub := func(run, idle int32) *corev1.Text {
		switch {
		case run > 0 && idle > 0:
			return text.Msg(text.KeyHomeRunningAndIdle, text.Int("count", int64(run)), text.Int("idle", int64(idle)))
		case run > 0:
			return text.Msg(text.KeyHomeRunningCount, text.Int("count", int64(run)))
		case idle > 0:
			return text.Msg(text.KeyHomeOtherIdle, text.Int("count", int64(idle)))
		default:
			return nil
		}
	}
	switch h.Kind {
	case lists.HeadlineNoNetwork:
		v.Kind = corev1.HeadlineKind_HEADLINE_KIND_ERROR
		v.Title = text.Msg(text.KeyConnectionNoNetwork)
		if h.Kept {
			v.Sub = text.Msg(text.KeyConnectionPreviousBelow)
		} else {
			v.Sub = text.Msg(text.KeyConnectionNetworkRequired)
		}
	case lists.HeadlineConnecting:
		v.Kind = corev1.HeadlineKind_HEADLINE_KIND_ERROR
		v.Title = text.Msg(text.KeyConnectionConnecting)
		if h.Kept {
			v.Sub = text.Msg(text.KeyConnectionPreviousBelow)
		}
	case lists.HeadlineReconnecting:
		v.Kind = corev1.HeadlineKind_HEADLINE_KIND_ERROR
		v.Title = text.Msg(text.KeyConnectionReconnecting)
		if h.Kept || liveLoaded {
			v.Sub = text.Msg(text.KeyConnectionPreviousBelow)
		}
	case lists.HeadlineNoServer:
		v.Kind = corev1.HeadlineKind_HEADLINE_KIND_ERROR
		v.Title = text.Msg(text.KeyConnectionNoServer)
		if h.SubError && hasReason(errText, h.RawError) {
			v.Sub = text.Msg(text.KeyHomeListFailed, reasonArg("reason", errText, h.RawError))
		} else if h.Kept {
			v.Sub = text.Msg(text.KeyConnectionPreviousBelow)
		}
	case lists.HeadlineWaiting:
		v.Kind = corev1.HeadlineKind_HEADLINE_KIND_WAITING
		v.HighlightCount = h.WaitingCount
		v.HighlightArg = "count"
		v.Title = text.Msg(text.KeyHomeWaitingCount, text.Int("count", int64(h.WaitingCount)))
		v.Sub = runningSub(h.RunningCount, h.IdleCount)
	case lists.HeadlineRunning:
		v.Kind = corev1.HeadlineKind_HEADLINE_KIND_RUNNING
		v.Title = text.Msg(text.KeyHomeRunningCount, text.Int("count", int64(h.RunningCount)))
		if h.IdleCount > 0 {
			v.Sub = text.Msg(text.KeyHomeOtherIdle, text.Int("count", int64(h.IdleCount)))
		}
	case lists.HeadlineEmptyProject:
		v.Kind = corev1.HeadlineKind_HEADLINE_KIND_EMPTY
		v.Title = text.Msg(text.KeyHomeProjectNoSessions)
		v.Sub = text.Plain(h.ProjectShortPath)
	case lists.HeadlineUnreadable:
		v.Kind = corev1.HeadlineKind_HEADLINE_KIND_ERROR
		v.Title = text.Msg(text.KeyHomeSessionsUnavailable)
		if hasReason(errText, h.RawError) {
			v.Sub = text.Msg(text.KeyHomeListFailed, reasonArg("reason", errText, h.RawError))
		}
	case lists.HeadlineNoSessions:
		v.Kind = corev1.HeadlineKind_HEADLINE_KIND_EMPTY
		v.Title = text.Msg(text.KeyHomeNoSessions)
	case lists.HeadlineNothingWaits:
		v.Kind = corev1.HeadlineKind_HEADLINE_KIND_EMPTY
		v.Title = text.Msg(text.KeyHomeNothingWaiting)
		if h.IdleCount > 0 {
			v.Sub = text.Msg(text.KeyHomeIdleCount, text.Int("count", int64(h.IdleCount)))
		}
	}
	return v
}

func buildHomeEmptyView(e *lists.HomeEmptyInfo, st *State) *corev1.HomeEmptyView {
	if e == nil {
		return &corev1.HomeEmptyView{Kind: corev1.HomeEmptyKind_HOME_EMPTY_KIND_NONE}
	}
	v := &corev1.HomeEmptyView{}
	switch e.Kind {
	case lists.HomeEmptyLoading:
		v.Kind = corev1.HomeEmptyKind_HOME_EMPTY_KIND_NONE
		v.Message = text.Msg(text.KeyCommonLoading)
	case lists.HomeEmptyNoMachines:
		if !st.World.IsManager && hasReason(st.ManagerNameText, st.World.ManagerName) {
			v.Kind = corev1.HomeEmptyKind_HOME_EMPTY_KIND_NO_MACHINES_MEMBER
			v.Message = text.Msg(text.KeyHomeAddViaManager, reasonArg("name", st.ManagerNameText, st.World.ManagerName))
		} else {
			v.Kind = corev1.HomeEmptyKind_HOME_EMPTY_KIND_NO_MACHINES
			v.Command = "remote-agent account join"
			v.Message = text.Msg(text.KeyHomeNoMachinesSetup, text.Str("command", v.Command))
			v.CanAddDevice = true
		}
	case lists.HomeEmptyUnlinked:
		if st.World.Connection == lists.ConnNoNetwork {
			v.Kind = corev1.HomeEmptyKind_HOME_EMPTY_KIND_NO_NETWORK
			v.Message = text.Msg(text.KeyConnectionNoNetworkEmpty)
		} else {
			v.Kind = corev1.HomeEmptyKind_HOME_EMPTY_KIND_LIST_FAILED
			v.Message = text.Msg(text.KeyHomeSessionsUnavailable)
			v.CanRetry = true
		}
	case lists.HomeEmptyNoneOnline:
		v.Kind = corev1.HomeEmptyKind_HOME_EMPTY_KIND_NO_ONLINE
		v.Command = "remote-agent daemon"
		v.Message = text.Msg(text.KeyHomeNoOnlineComputer, text.Str("command", v.Command))
	case lists.HomeEmptyReady:
		v.Kind = corev1.HomeEmptyKind_HOME_EMPTY_KIND_READY_TO_START
		v.ReadyMachineId = e.ReadyMachineID
		v.ReadyMachineName = e.ReadyMachineName
		v.Message = text.Msg(text.KeyHomeOnlineStart, text.Str("name", e.ReadyMachineName))
		v.CanStartFirst = true
	case lists.HomeEmptyScope:
		v.Kind = corev1.HomeEmptyKind_HOME_EMPTY_KIND_SCOPED_PROJECT
		v.Message = text.Msg(text.KeyHomeEmptyProjectStart)
	default:
		v.Kind = corev1.HomeEmptyKind_HOME_EMPTY_KIND_NONE
	}
	return v
}

func buildAttentionCardView(item *lists.SessionItem, st *State) *corev1.AttentionCardView {
	if item == nil {
		return nil
	}
	p := lists.PendingAction{}
	if item.Session != nil {
		p = item.Session.Pending
	}
	isCmd := p.ApprovalCommand != ""
	var kindText *corev1.Text
	if isCmd {
		kindText = text.Msg(text.KeyListStateRequestCommandPlain)
	} else {
		kindText = text.Msg(text.KeyListStateRequestPermissionPlain)
	}
	var body string
	if isCmd {
		body = lists.ApprovalDisplay(p.ApprovalCommand)
	} else if p.ApprovalToolInput != "" {
		body = lists.ApprovalDisplay(p.ApprovalToolInput)
	}
	var whereParts []*corev1.Text
	if item.ProjectName != "" {
		whereParts = append(whereParts, text.Plain(item.ProjectName))
	}
	if item.MachineName != "" {
		whereParts = append(whereParts, text.Plain(item.MachineName))
	}
	var timeText *corev1.Text
	if item.MinutesLeft > 0 {
		rem := text.Msg(text.KeyHomeIosApprovalRemaining, text.Int("count", item.MinutesLeft))
		timeText = text.Msg(text.KeyApprovalTimeoutNote, text.ArgText("remaining", rem))
	}
	var alwaysKind corev1.AlwaysAllowKind
	var alwaysArg string
	var alwaysLabel *corev1.Text
	if st != nil {
		for _, m := range st.World.Machines {
			if m.ID == item.MachineID && m.Meta != nil && m.Meta.GetCapabilities().GetApprovalAlways() {
				if isCmd {
					alwaysKind = corev1.AlwaysAllowKind_ALWAYS_ALLOW_KIND_COMMAND_PREFIX
				} else {
					alwaysKind = corev1.AlwaysAllowKind_ALWAYS_ALLOW_KIND_TOOL_CATEGORY
					alwaysArg = lists.ApprovalDisplay(p.ApprovalToolName)
				}
				alwaysLabel = text.Msg(text.KeyApprovalAlwaysAllow)
				break
			}
		}
	}
	return &corev1.AttentionCardView{
		SessionId:   item.ID,
		MachineId:   item.MachineID,
		ApprovalId:  p.ApprovalID,
		Title:       sessionTitleText(item),
		KindText:    kindText,
		Where:       text.JoinLiteral(" · ", whereParts...),
		Time:        timeText,
		Hot:         item.MinutesLeft > 0 && item.MinutesLeft <= 2,
		Body:        body,
		ToolName:    lists.ApprovalDisplay(p.ApprovalToolName),
		IsCommand:   isCmd,
		Truncated:   p.ApprovalTruncated,
		AlwaysKind:  alwaysKind,
		AlwaysArg:   alwaysArg,
		AlwaysLabel: alwaysLabel,
	}
}

func buildAttentionRowView(item *lists.SessionItem, meta lists.RowMeta) *corev1.AttentionRowView {
	if item == nil {
		return nil
	}
	title := sessionTitleText(item)
	var kindText *corev1.Text
	var summary *corev1.Text
	var detailParts []*corev1.Text
	p := lists.PendingAction{}
	if item.Session != nil {
		p = item.Session.Pending
	}
	switch item.RawStatus {
	case lists.StatusApproval:
		if p.ApprovalCommand != "" {
			cmd := lists.ApprovalDisplay(p.ApprovalCommand)
			kindText = text.Msg(text.KeyListStateRequestCommandPlain)
			summary = text.Msg(text.KeyListStateRequestCommand, text.Str("command", cmd))
			detailParts = append(detailParts, text.Plain(cmd))
		} else if p.ApprovalToolName != "" {
			tool := lists.ApprovalDisplay(p.ApprovalToolName)
			kindText = text.Msg(text.KeyListStateRequestPermissionPlain)
			summary = text.Msg(text.KeyListStateRequestPermission, text.Str("tool", tool))
			detailParts = append(detailParts, text.Plain(tool))
		} else {
			kindText = text.Msg(text.KeyListStateApproval)
			summary = kindText
		}
	case lists.StatusQuestion:
		agentName := lists.AgentInfo(item.Agent).Name
		if agentName != "" {
			kindText = text.Msg(text.KeyListStateAgentAsking, text.Str("agent", agentName))
		} else {
			kindText = text.Msg(text.KeyListStateQuestion)
		}
		summary = kindText
	case lists.StatusPlan:
		kindText = text.Msg(text.KeyListStatePlan)
		summary = kindText
	case lists.StatusPopup:
		kindText = text.Msg(text.KeyListStatePopup)
		summary = kindText
	default:
		kindText = statusText(item.ShownStatus)
		summary = kindText
	}

	var extra []*corev1.Text
	if (meta.ShowProject || item.OutsideScope) && item.ProjectName != "" {
		extra = append(extra, text.Plain(item.ProjectName))
	}
	if meta.ShowMachine && item.MachineName != "" {
		extra = append(extra, text.Plain(item.MachineName))
	}
	if item.MinutesLeft > 0 {
		extra = append(extra, text.Msg(text.KeyHomeIosApprovalRemaining, text.Int("count", item.MinutesLeft)))
	}
	fullSummary := summary
	if len(extra) > 0 {
		fullSummary = text.JoinLiteral(" · ", append([]*corev1.Text{summary}, extra...)...)
		detailParts = append(detailParts, extra...)
	}
	return &corev1.AttentionRowView{
		SessionId:    item.ID,
		MachineId:    item.MachineID,
		Title:        title,
		KindText:     kindText,
		Summary:      fullSummary,
		Detail:       text.JoinLiteral(" · ", detailParts...),
		OutsideScope: item.OutsideScope,
	}
}

func mapEndedGroup(g *lists.EndedGroup, cal lists.Calendar) (corev1.EndedGroupKind, *corev1.Text) {
	switch g.Kind {
	case lists.GroupToday:
		return corev1.EndedGroupKind_ENDED_GROUP_KIND_TODAY, text.Msg(text.KeyTimeToday)
	case lists.GroupYesterday:
		return corev1.EndedGroupKind_ENDED_GROUP_KIND_YESTERDAY, text.Msg(text.KeyTimeYesterday)
	case lists.GroupWeek:
		if g.RepMs > 0 {
			return corev1.EndedGroupKind_ENDED_GROUP_KIND_LAST_WEEK, text.Time(g.RepMs, corev1.TimestampStyle_TIMESTAMP_STYLE_WEEKDAY)
		}
		idx := int(g.WeekdayIndex)
		if idx < 0 || idx >= len(weekdayCodes) {
			idx = 0
		}
		return corev1.EndedGroupKind_ENDED_GROUP_KIND_LAST_WEEK, text.Msg(text.KeyTimeWeekday, text.Str("day", weekdayCodes[idx]))
	default:
		if g.Year != 0 && (cal.NowYear == 0 || g.Year != cal.NowYear) {
			return corev1.EndedGroupKind_ENDED_GROUP_KIND_YEAR_MONTH, text.Msg(text.KeyTimeYearMonthGroup, text.Int("year", int64(g.Year)), text.Int("month", int64(g.Month)))
		}
		return corev1.EndedGroupKind_ENDED_GROUP_KIND_MONTH, text.Msg(text.KeyTimeMonthGroup, text.Int("month", int64(g.Month)))
	}
}
