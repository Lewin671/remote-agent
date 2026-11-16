package screens

import (
	"slices"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

// BuildProject constructs the ProjectViewModel for project detail (with endedLimit window) or management.
func BuildProject(st *State, projectKey string, manage bool, endedLimit int) *corev1.ProjectViewModel {
	if st == nil {
		st = NewState()
	}
	if manage || projectKey == "" {
		return &corev1.ProjectViewModel{
			Kind:   corev1.ProjectViewKind_PROJECT_VIEW_KIND_MANAGE,
			Manage: buildManageProjectsView(st),
		}
	}
	return &corev1.ProjectViewModel{
		Kind:   corev1.ProjectViewKind_PROJECT_VIEW_KIND_DETAIL,
		Detail: buildProjectDetailView(st, projectKey, endedLimit),
	}
}

func buildProjectDetailView(st *State, key string, endedLimit int) *corev1.ProjectDetailView {
	page := lists.BuildProjectPage(&st.World, key, endedLimit, st.Cal)

	name := lists.ProjectName(key)
	var nameText *corev1.Text
	if name != "" {
		nameText = text.Plain(name)
	} else {
		nameText = text.Msg(text.KeyProjectsUnknown)
	}

	var subParts []*corev1.Text
	if page.ShortPath != "" {
		subParts = append(subParts, text.Plain(page.ShortPath))
	}
	if len(page.MachineNames) > 0 {
		nameTexts := make([]*corev1.Text, 0, len(page.MachineNames))
		for _, mn := range page.MachineNames {
			nameTexts = append(nameTexts, text.Plain(mn))
		}
		subParts = append(subParts, text.Join(text.KeyCommonListSeparator, nameTexts...))
	}
	subtitle := text.JoinLiteral(" · ", subParts...)

	activeRows := make([]*corev1.SessionRowView, 0, len(page.Active))
	for _, item := range page.Active {
		activeRows = append(activeRows, buildSessionRow(item, page.Meta, st.Cal, st.HomeSelectedSessionID == item.ID))
	}

	endedRows := make([]*corev1.EndedRowView, 0, len(page.Ended))
	for _, item := range page.Ended {
		endedRows = append(endedRows, buildEndedRow(item, page.Meta, st.Cal, st.HomeSelectedSessionID == item.ID))
	}

	var tail *corev1.TailRowView
	if page.Tail != nil {
		tail = buildTailRow(page.Tail)
	} else if page.EndedTotal > int32(len(page.Ended)) {
		tail = &corev1.TailRowView{
			Kind: corev1.TailKind_TAIL_KIND_MORE_ENDED,
			Text: text.Msg(text.KeyHomeTotal, text.Int("count", int64(page.EndedTotal))),
		}
	}

	var emptyNote *corev1.Text
	if len(page.Active) == 0 && page.EndedTotal == 0 {
		emptyNote = text.Msg(text.KeyProjectsNoSessions)
	}

	pendingPath := st.PrefsPendingPath[key]
	var noticeErr *corev1.Text
	if errMsg := st.PrefsSaveErrorByPath[key]; errMsg != "" {
		noticeErr = text.Msg(text.KeyCommonSaveFailed, text.Str("reason", errMsg))
	}
	noticeText := text.Msg(text.KeyProjectsRemoved)
	if page.IsHidden && pendingPath {
		noticeText = text.Msg(text.KeyProjectsAdding)
	}

	var saveStatus *corev1.Text
	if pendingPath && !page.IsHidden {
		saveStatus = text.Msg(text.KeyCommonSaving)
	} else if !text.IsEmpty(noticeErr) {
		saveStatus = noticeErr
	}

	var settingsErr *corev1.Text
	if errMsg := st.PrefsSettingsError[key]; errMsg != "" {
		settingsErr = text.Msg(text.KeyProjectsSettingsFailed, text.Str("reason", errMsg))
	} else if st.PrefsSavingSettings[key] {
		settingsErr = text.Msg(text.KeyProjectsSavingSettings)
	}

	canEdit := st.World.Connection == lists.ConnConnected && !st.World.AccountControlLost && !st.World.AccountClockSkewed

	return &corev1.ProjectDetailView{
		Key:            key,
		Name:           nameText,
		ShortPath:      page.ShortPath,
		Subtitle:       subtitle,
		MachineIds:     page.MachineIDs,
		MachineNames:   page.MachineNames,
		AnyOnline:      page.AnyOnline,
		ActiveCount:    int32(len(page.Active)),
		EndedTotal:     page.EndedTotal,
		Active:         activeRows,
		Ended:          endedRows,
		CanLoadEarlier: page.WantPage,
		Tail:           tail,
		EmptyNote:      emptyNote,
		IsHidden:       page.IsHidden,
		IsPinned:       page.IsPinned,
		HiddenNotice: &corev1.HiddenProjectNoticeView{
			Active:        page.IsHidden,
			Text:          noticeText,
			ActionEnabled: canEdit && !pendingPath,
			Busy:          pendingPath,
			Error:         noticeErr,
		},
		SavingSettings: st.PrefsSavingSettings[key],
		SettingsError:  settingsErr,
		CanStart:       page.AnyOnline,
		CanPin:         canEdit && !page.IsHidden && !pendingPath,
		CanRemove:      canEdit && !page.IsHidden && !pendingPath,
		SaveStatus:     saveStatus,
		HasMoreEnded:   page.HasMoreEnded,
	}
}

func buildManageProjectsView(st *State) *corev1.ManageProjectsView {
	res := lists.BuildProjects(&st.World, st.ProjectSearch, st.RememberedScope, st.RememberedScopeToken, nil, st.Scope)
	canEdit := st.World.Connection == lists.ConnConnected && !st.World.AccountControlLost && !st.World.AccountClockSkewed
	var blocked *corev1.Text
	if st.World.Connection != lists.ConnConnected {
		blocked = text.Msg(text.KeyProjectsConnectToEdit)
	}
	var saveErr *corev1.Text
	if st.PrefsGlobalError != "" {
		saveErr = text.Msg(text.KeyProjectsSaveFailedReason, text.Str("reason", st.PrefsGlobalError))
	}
	return &corev1.ManageProjectsView{
		Listing:          buildProjectListing(res, st, true),
		CanEdit:          canEdit,
		BlockedReason:    blocked,
		ChangedElsewhere: st.ProjectChangedElsewhere,
		Syncing:          st.World.PrefsSyncing,
		SaveError:        saveErr,
		DirPicker:        buildDirPickerView(st),
		ProjectCount:     int32(len(res.Listed)),
	}
}

func buildDirPickerView(st *State) *corev1.DirPickerView {
	dp := st.DirPicker
	var sorted []*lists.Machine
	for _, m := range st.World.Machines {
		if m != nil {
			sorted = append(sorted, m)
		}
	}
	slices.SortFunc(sorted, func(a, b *lists.Machine) int {
		oa := lists.MachineReachable(a)
		ob := lists.MachineReachable(b)
		if oa != ob {
			if oa {
				return -1
			}
			return 1
		}
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})

	machines := make([]*corev1.DirPickerMachineView, 0, len(sorted))
	for _, m := range sorted {
		online := lists.MachineReachable(m)
		var stText *corev1.Text
		if online {
			stText = text.Msg(text.KeyCommonOnline)
		} else {
			stText = text.Msg(text.KeyCommonOffline)
		}
		var home string
		if m.Meta != nil {
			home = m.Meta.Home
		}
		machines = append(machines, &corev1.DirPickerMachineView{
			MachineId:  m.ID,
			Name:       lists.MachineName(m, m.ID),
			Online:     online,
			StatusText: stText,
			Home:       home,
		})
	}

	rawCrumbs := lists.PathCrumbs(dp.CurrentPath)
	crumbs := make([]*corev1.DirCrumbView, 0, len(rawCrumbs))
	for _, seg := range rawCrumbs {
		crumbs = append(crumbs, &corev1.DirCrumbView{
			Label: text.Plain(seg),
			Path:  seg,
		})
	}

	var errText *corev1.Text
	if dp.Error != "" {
		errText = text.Msg(text.KeyProjectsDirectoryFailed, text.Str("reason", dp.Error))
	}

	canConfirm := dp.Open && dp.CurrentPath != "" && !dp.Loading && dp.Error == ""
	var confirmLabel *corev1.Text
	if dp.CurrentPath != "" {
		confirmLabel = text.Msg(text.KeyProjectsUseDirectory, text.Str("name", lists.ProjectName(dp.CurrentPath)))
	}

	return &corev1.DirPickerView{
		Open:         dp.Open,
		ForNewTask:   dp.ForNewTask,
		Machines:     machines,
		MachineId:    dp.MachineID,
		CurrentPath:  dp.CurrentPath,
		Crumbs:       crumbs,
		ParentPath:   dp.ParentPath,
		Dirs:         dp.Dirs,
		Loading:      dp.Loading,
		Error:        errText,
		ConfirmLabel: confirmLabel,
		CanConfirm:   canConfirm,
	}
}
