package screens

import (
	"math"
	"strconv"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/machine"
	"github.com/Lewin671/remote-agent/clientcore/domain/quota"
	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	"github.com/Lewin671/remote-agent/clientcore/domain/usage"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

// MachineSettingsExtra holds per-machine transient and UI-owned settings state in Core.
type MachineSettingsExtra struct {
	SelectedAgent      v1.Agent
	ImageStorage       *v1.ImageStorageUsage
	SavePending        bool
	SaveUnknown        bool
	CleaningImages     bool
	SaveError          *corev1.Text
	ToggleSaving       map[v1.Agent]bool
	TogglePendingValue map[v1.Agent]bool
	ToggleBaseRevision uint64
	ToggleBanner       *corev1.Text
}

// NotificationPrefs holds client-local notification preferences and push registration state.
type NotificationPrefs struct {
	Supported       bool
	Permission      corev1.NotificationPermissionState
	PushState       corev1.PushRegistrationState
	NotifyAttention bool
	NotifyTurnEnded bool
	NotifyScheduled bool
	ShowContent     bool
	ErrorDetail     string
}

// ConnCheckState holds shell-reported relay connectivity check telemetry.
type ConnCheckState struct {
	ServerState corev1.ConnProbeState
	LatencyMs   int32
	LastFailure string
}

// UpdateState holds shell-reported client release check/download state.
type UpdateState struct {
	CurrentVersion          string
	ProtocolVersion         uint32
	Channel                 string
	Checking                bool
	AvailableVersion        string
	DownloadedVersion       string
	DownloadProgressPercent int32
	ErrorMessage            string
	Releases                []*corev1.ReleaseCandidateInfo
}

// SettingsExtra holds all screen & controller state owned by the Settings / Machine / Devices domain.
type SettingsExtra struct {
	Active                  bool
	Machines                map[string]*MachineSettingsExtra
	PollFeeds               map[string]*MachinePollFeed
	PendingCleanupMachineID string
	RenameSaving            bool
	RenameError             *corev1.Text
	Notifications           NotificationPrefs
	Theme                   corev1.ThemePreference
	ConnCheck               ConnCheckState
	Update                  UpdateState
}

// NewSettingsExtra initializes default SettingsExtra state matching shell defaults.
func NewSettingsExtra() *SettingsExtra {
	return &SettingsExtra{
		Machines:  make(map[string]*MachineSettingsExtra),
		PollFeeds: make(map[string]*MachinePollFeed),
		Notifications: NotificationPrefs{
			Supported:       true,
			Permission:      corev1.NotificationPermissionState_NOTIFICATION_PERMISSION_STATE_UNSPECIFIED,
			PushState:       corev1.PushRegistrationState_PUSH_REGISTRATION_STATE_UNSPECIFIED,
			NotifyAttention: true,
			NotifyTurnEnded: true,
			NotifyScheduled: true,
			ShowContent:     true,
		},
		Theme: corev1.ThemePreference_THEME_PREFERENCE_AUTO,
		ConnCheck: ConnCheckState{
			ServerState: corev1.ConnProbeState_CONN_PROBE_STATE_NOT_CHECKED,
		},
	}
}

func (s *SettingsExtra) EnsureMachine(id string) *MachineSettingsExtra {
	if s.Machines == nil {
		s.Machines = make(map[string]*MachineSettingsExtra)
	}
	e := s.Machines[id]
	if e == nil {
		e = &MachineSettingsExtra{}
		s.Machines[id] = e
	}
	return e
}

func (s *SettingsExtra) EnsureFeed(id string) *MachinePollFeed {
	if s.PollFeeds == nil {
		s.PollFeeds = make(map[string]*MachinePollFeed)
	}
	f := s.PollFeeds[id]
	if f == nil {
		f = &MachinePollFeed{
			Tab: corev1.MachineUsageTab_MACHINE_USAGE_TAB_SESSIONS,
		}
		s.PollFeeds[id] = f
	}
	return f
}

func machineDirectInputs(st *State, _ *SettingsExtra, machineID string) (bool, *corev1.DirectRouteReport) {
	if st != nil && st.DirectReportFn != nil {
		if enabled, report, ok := st.DirectReportFn(machineID); ok {
			return enabled, report
		}
	}
	return true, nil
}

type settingsMachinesSorted struct {
	list    []*lists.Machine
	removed bool
}

func (s settingsMachinesSorted) rank(m *lists.Machine) int {
	if m.Archived || s.removed {
		return 2
	}
	if lists.MachineReachable(m) {
		return 0
	}
	return 1
}

func (s settingsMachinesSorted) Len() int      { return len(s.list) }
func (s settingsMachinesSorted) Swap(i, j int) { s.list[i], s.list[j] = s.list[j], s.list[i] }
func (s settingsMachinesSorted) Less(i, j int) bool {
	ri, rj := s.rank(s.list[i]), s.rank(s.list[j])
	if ri != rj {
		return ri < rj
	}
	ni := strings.ToLower(lists.MachineName(s.list[i], s.list[i].ID))
	nj := strings.ToLower(lists.MachineName(s.list[j], s.list[j].ID))
	if ni != nj {
		return ni < nj
	}
	return s.list[i].ID < s.list[j].ID
}

func isReadDirectActive(online, unlinked, archived bool, mismatch int32, report *corev1.DirectRouteReport) bool {
	return online && !unlinked && !archived && mismatch == 0 &&
		report != nil &&
		report.State == corev1.DirectRouteState_DIRECT_ROUTE_STATE_CONNECTED &&
		report.ReadRoute == corev1.DirectPathChoice_DIRECT_PATH_CHOICE_DIRECT
}

func isNativeAppPlatform(st *State) bool {
	if st == nil || st.PlatformAgentFn == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(st.PlatformAgentFn())) {
	case "android", "ios":
		return true
	default:
		return false
	}
}

func buildMachineListCards(st *State, syncView *corev1.SyncStateView, machines []*lists.Machine, extra *SettingsExtra, includeSummary bool) ([]*corev1.MachineListCardView, map[string]bool) {
	sorted := make([]*lists.Machine, 0, len(machines))
	for _, m := range machines {
		if m != nil {
			sorted = append(sorted, m)
		}
	}
	clientVer := st.World.ClientVersion
	removed := st.Removed || syncView.GetRemoved()
	sortutil.SortInterface(settingsMachinesSorted{list: sorted, removed: removed})

	var inDir map[string]bool
	var directReady map[string]bool
	if syncView != nil {
		if len(syncView.Devices) > 0 {
			inDir = make(map[string]bool, len(syncView.Devices))
			for _, d := range syncView.Devices {
				if d != nil && d.DeviceId != "" {
					inDir[d.DeviceId] = true
				}
			}
		}
		if len(syncView.Machines) > 0 {
			directReady = make(map[string]bool, len(syncView.Machines))
			for _, sm := range syncView.Machines {
				if sm != nil && sm.MachineId != "" && sm.DirectReady {
					directReady[sm.MachineId] = true
				}
			}
		}
	}

	identities := make([]machine.MachineIdentity, 0, len(sorted))
	for _, m := range sorted {
		identities = append(identities, machine.MachineIdentity{
			ID:   m.ID,
			Name: lists.MachineName(m, m.ID),
		})
	}
	disambiguators := machine.BuildDisambiguators(identities)

	out := make([]*corev1.MachineListCardView, 0, len(sorted))
	pingDirect := make(map[string]bool, len(sorted))
	for _, m := range sorted {
		online := lists.MachineReachable(m)
		unlinked := lists.MachineUnlinked(m, &st.World)
		archived := m.Archived || removed
		mismatch := lists.ProtocolMismatchDirection(m, clientVer)
		dimmed := archived || !online

		appearance := corev1.MachineCardAppearance_MACHINE_CARD_APPEARANCE_NORMAL
		if archived {
			appearance = corev1.MachineCardAppearance_MACHINE_CARD_APPEARANCE_REMOVED
		} else if !online {
			appearance = corev1.MachineCardAppearance_MACHINE_CARD_APPEARANCE_OFFLINE
		}

		var statusTxt *corev1.Text
		var statusDot corev1.MachineStatusDot
		var alerts []*corev1.MachineCardAlert
		var quotaItems []*corev1.MachineCardQuotaItem
		var secondaryNote *corev1.Text

		if archived {
			statusDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_UNSPECIFIED
			statusTxt = text.Msg(text.KeySettingsStatusRemoved)
			secondaryNote = text.Msg(text.KeySettingsMachineSubRemoved)
		} else if online && mismatch != 0 {
			statusDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_ORANGE_STATIC
			statusTxt = text.Msg(text.KeySettingsStatusNeedsUpdate)
			alertKey := text.KeySettingsMachineSubVersionMismatch
			if mismatch > 0 {
				alertKey = text.KeySettingsMachineSubVersionMismatchClient
			}
			alerts = append(alerts, &corev1.MachineCardAlert{
				Kind: corev1.MachineAlertKind_MACHINE_ALERT_KIND_PROTOCOL_MISMATCH,
				Text: text.Msg(alertKey),
				Warn: true,
			})
		} else if online {
			statusDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_GREEN_LIVE
			statusTxt = text.Msg(text.KeyCommonOnline)
			if m.Meta != nil && m.Meta.Capabilities != nil && m.Meta.Capabilities.MachineStatus &&
				m.Meta.DiskStatus != nil && m.Meta.DiskStatus.AvailableBytes < machine.DiskReminderThresholdBytes {
				alerts = append(alerts, &corev1.MachineCardAlert{
					Kind: corev1.MachineAlertKind_MACHINE_ALERT_KIND_DISK_LOW,
					Text: text.Msg(text.KeySettingsMachineSubDiskAlert, text.ArgText("size", machine.FormatDecimalSize(m.Meta.DiskStatus.AvailableBytes))),
					Warn: true,
				})
			}
			if includeSummary {
				for _, qs := range quota.MachineCardSummaries(m, st.Cal.NowMs) {
					r := qs.SummaryRow
					if r == nil || !r.HasPercent || r.Stale || r.PastReset {
						continue
					}
					warn := r.Tone == corev1.QuotaTone_QUOTA_TONE_WARN || r.Tone == corev1.QuotaTone_QUOTA_TONE_FULL
					rem := r.RemainingPercent
					var valTxt *corev1.Text
					if r.Exhausted {
						rem = 0
						valTxt = text.Msg(text.KeyQuotaExhaustedQuota)
					} else {
						valTxt = text.Msg(
							text.KeyQuotaLeft,
							text.Str("value", strconv.FormatInt(int64(rem), 10)),
						)
					}
					quotaItems = append(quotaItems, &corev1.MachineCardQuotaItem{
						Agent:            qs.Agent,
						AgentName:        qs.AgentName,
						RemainingPercent: rem,
						Exhausted:        r.Exhausted,
						Warn:             warn,
						ValueText:        valTxt,
					})
				}
			}
		} else {
			statusDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_GRAY_STATIC
			if m.LastSeenMs > 0 {
				statusTxt = formatRelativeTime(m.LastSeenMs, st.Cal)
			}
			if statusTxt == nil {
				statusTxt = text.Msg(text.KeySettingsOffline)
			}
		}

		name := lists.MachineName(m, m.ID)
		disambiguator := disambiguators[m.ID]
		var disambiguatorA11y *corev1.Text
		if disambiguator != "" {
			disambiguatorA11y = text.Msg(text.KeySettingsMachineDisambiguatorA11Y, text.Str("value", disambiguator))
		}
		card := &corev1.MachineListCardView{
			MachineId:         m.ID,
			Name:              name,
			Online:            online,
			Member:            !archived,
			ProtocolMismatch:  mismatch,
			StatusText:        statusTxt,
			StatusDot:         statusDot,
			LastSeenMs:        m.LastSeenMs,
			InDeviceDirectory: inDir[m.ID],
			DirectReady:       directReady[m.ID],
			Disambiguator:     disambiguator,
			Dimmed:            dimmed,
			Alerts:            alerts,
			QuotaItems:        quotaItems,
			SecondaryNote:     secondaryNote,
			Appearance:        appearance,
			MetaSeparator:     "·",
			DisambiguatorA11Y: disambiguatorA11y,
		}
		if includeSummary {
			directPref, directReport := machineDirectInputs(st, extra, m.ID)
			card.DirectRow = machine.BuildConnCheckDirectRow(online && !unlinked && !archived, directPref, directReport)
			readDirect := isReadDirectActive(online, unlinked, archived, mismatch, directReport)
			card.DirectActive = readDirect
			if readDirect {
				pingDirect[m.ID] = true
			}
		}
		var metaSegments []*corev1.MachineCardMetaSegment
		if !text.IsEmpty(statusTxt) {
			metaSegments = append(metaSegments, &corev1.MachineCardMetaSegment{Text: statusTxt})
		}
		if card.DirectActive {
			metaSegments = append(metaSegments, &corev1.MachineCardMetaSegment{
				Text: text.Msg(text.KeyTransportDirect),
				Icon: corev1.MachineCardMetaIcon_MACHINE_CARD_META_ICON_DIRECT,
			})
		} else if includeSummary && online && !unlinked && !archived && mismatch == 0 {
			// Online but reads go through the relay (direct off, still
			// connecting, or fell back): name the route so it is never ambiguous.
			metaSegments = append(metaSegments, &corev1.MachineCardMetaSegment{
				Text: text.Msg(text.KeyTransportCardRelay),
			})
		}
		if !text.IsEmpty(secondaryNote) {
			metaSegments = append(metaSegments, &corev1.MachineCardMetaSegment{Text: secondaryNote})
		}
		card.MetaSegments = metaSegments
		out = append(out, card)
	}
	return out, pingDirect
}

func buildClientRows(st *State, syncView *corev1.SyncStateView) (rows []*corev1.ClientRowView, unfinished []*corev1.ClientRowView) {
	selfID := syncView.GetSelfDeviceId()
	authID := syncView.GetAuthorityDeviceId()
	sorted := machine.SortClients(syncView.GetDevices(), selfID)

	for _, d := range sorted {
		name := machine.ClientName(d)
		if name == "" {
			name = d.DeviceId
		}
		isSelf := d.DeviceId == selfID
		isAuth := d.DeviceId == authID
		member := d.Trusted || isSelf || isAuth
		incomplete := ((member && !d.Registered) || (syncView.GetSecurityLoaded() && !d.Trusted)) && !isSelf && !isAuth

		var role *corev1.Text
		switch {
		case isSelf:
			role = text.Msg(text.KeySettingsCurrent)
		case isAuth:
			role = text.Msg(text.KeySettingsManagingClient)
		default:
			role = text.Msg(text.KeySettingsPairedClient)
		}

		var lastUsed *corev1.Text
		switch {
		case isSelf:
			lastUsed = text.Msg(text.KeySettingsNow)
		case d.LastSeenMs <= 0:
			lastUsed = text.Msg(text.KeySettingsNoRecord)
		default:
			lastUsed = formatRelativeTime(d.LastSeenMs, st.Cal)
		}

		row := &corev1.ClientRowView{
			DeviceId:          d.DeviceId,
			DisplayName:       name,
			Description:       machine.DefaultClientName(d),
			ShortId:           machine.ShortID(d.DeviceId),
			IsSelf:            isSelf,
			IsAuthority:       isAuth,
			Member:            member,
			PairingIncomplete: incomplete,
			RoleLabel:         role,
			LastUsedText:      lastUsed,
			AddedText:         formatCalendarDay(d.CreatedMs, st.Cal),
			Kind:              d.Kind,
		}
		if incomplete {
			unfinished = append(unfinished, row)
		} else if d.Kind != v1.DeviceKind_DEVICE_KIND_MACHINE {
			rows = append(rows, row)
		}
	}
	return rows, unfinished
}

func formatCalendarDay(ms int64, cal lists.Calendar) *corev1.Text {
	if ms <= 0 {
		return text.Msg(text.KeySettingsNoRecord)
	}
	y, m, d, _, _, _ := lists.CivilFromUnixMs(ms, cal.UTCOffsetMinutes)
	if cal.NowMs > 0 {
		cy, _, _, _, _, _ := lists.CivilFromUnixMs(cal.NowMs, cal.UTCOffsetMinutes)
		if cy == y {
			return text.Msg(text.KeyTimeMonthDay, text.Int("month", int64(m)), text.Int("day", int64(d)))
		}
	}
	return text.Msg(text.KeyTimeCalendarDateYear, text.Int("year", int64(y)), text.Int("month", int64(m)), text.Int("day", int64(d)))
}

func buildNotificationsView(n NotificationPrefs) *corev1.NotificationsView {
	v := &corev1.NotificationsView{
		Supported:       n.Supported,
		Permission:      n.Permission,
		PushState:       n.PushState,
		NotifyAttention: n.NotifyAttention,
		NotifyTurnEnded: n.NotifyTurnEnded,
		NotifyScheduled: n.NotifyScheduled,
		ShowContent:     n.ShowContent,
		ErrorDetail:     n.ErrorDetail,
	}
	hasIssue := n.Supported && (n.Permission == corev1.NotificationPermissionState_NOTIFICATION_PERMISSION_STATE_NEEDS_HOME_SCREEN ||
		n.Permission == corev1.NotificationPermissionState_NOTIFICATION_PERMISSION_STATE_DENIED ||
		n.PushState == corev1.PushRegistrationState_PUSH_REGISTRATION_STATE_FAILED)
	switch {
	case hasIssue:
		v.SummaryWarn = true
		v.SummaryText = text.Msg(text.KeySettingsNotificationsSummaryUnreachable)
	case !n.Supported || n.PushState == corev1.PushRegistrationState_PUSH_REGISTRATION_STATE_DISABLED ||
		(!n.NotifyAttention && !n.NotifyTurnEnded && !n.NotifyScheduled):
		v.SummaryText = text.Msg(text.KeySettingsNotificationsSummaryOff)
	case n.NotifyAttention && !n.NotifyTurnEnded && !n.NotifyScheduled:
		v.SummaryText = text.Msg(text.KeySettingsNotificationsSummaryPendingOnly)
	default:
		v.SummaryText = text.Msg(text.KeySettingsNotificationsSummaryOn)
	}

	if !n.Supported {
		v.StatusText = text.Msg(text.KeySettingsThisBrowserDoesNotSupportPushNotifications)
		return v
	}
	if n.Permission == corev1.NotificationPermissionState_NOTIFICATION_PERMISSION_STATE_NEEDS_HOME_SCREEN {
		v.StatusText = text.Msg(text.KeySettingsCannotReceive)
		v.HintText = text.Msg(text.KeySettingsIphonePushHelp)
		return v
	}
	if n.Permission == corev1.NotificationPermissionState_NOTIFICATION_PERMISSION_STATE_DENIED {
		v.StatusText = text.Msg(text.KeySettingsCannotReceive)
		v.HintText = text.Msg(text.KeySettingsNotificationPermissionHelp)
		return v
	}
	switch n.PushState {
	case corev1.PushRegistrationState_PUSH_REGISTRATION_STATE_DISABLED:
		v.StatusText = text.Msg(text.KeySettingsSessionAlertsAreOffYouWillNotBeNotified)
	case corev1.PushRegistrationState_PUSH_REGISTRATION_STATE_FAILED:
		v.StatusText = text.Msg(text.KeySettingsSubscriptionError, reasonArg("reason", nil, n.ErrorDetail))
	default:
		v.StatusText = text.Msg(text.KeySettingsEnabled)
	}
	return v
}

func buildAppearanceView(theme corev1.ThemePreference) *corev1.AppearanceView {
	if theme == corev1.ThemePreference_THEME_PREFERENCE_UNSPECIFIED {
		theme = corev1.ThemePreference_THEME_PREFERENCE_AUTO
	}
	var lbl *corev1.Text
	switch theme {
	case corev1.ThemePreference_THEME_PREFERENCE_LIGHT:
		lbl = text.Msg(text.KeySettingsLight)
	case corev1.ThemePreference_THEME_PREFERENCE_DARK:
		lbl = text.Msg(text.KeySettingsDark)
	default:
		lbl = text.Msg(text.KeySettingsThemeAuto)
	}
	return &corev1.AppearanceView{
		Theme: theme,
		Label: lbl,
	}
}

func buildConnCheckSummary(st *State, syncView *corev1.SyncStateView, cc ConnCheckState, machines []*corev1.MachineListCardView, pingDirect map[string]bool) *corev1.ConnCheckSummaryView {
	connState := mapSessionConnState(st.World.Connection)
	isConnecting := st.World.Connection == lists.ConnConnecting || st.World.Connection == lists.ConnReconnecting
	networkAvailable := st.World.Connection != lists.ConnNoNetwork
	if syncView != nil {
		networkAvailable = syncView.GetNetworkAvailable()
	}
	var onlineMembers, directMembers int
	var hasUnsupportedDirect bool
	for _, m := range machines {
		if m != nil && m.Online && m.Member {
			onlineMembers++
			if pingDirect[m.MachineId] {
				directMembers++
			}
			if m.DirectRow != nil && m.DirectRow.Stage == corev1.ConnectionGroupStage_CONNECTION_GROUP_STAGE_UNSUPPORTED {
				hasUnsupportedDirect = true
			}
		}
	}
	var footerTxt *corev1.Text
	switch {
	case onlineMembers > 0 && directMembers == onlineMembers:
		footerTxt = text.Msg(text.KeyConnectionLatencyFooterDirect)
	case directMembers > 0:
		footerTxt = text.Msg(text.KeyConnectionLatencyFooterMixed)
	default:
		footerTxt = text.Msg(text.KeyConnectionLatencyIsOneRoundTripTheServerRowMeasures)
	}
	if hasUnsupportedDirect {
		unsupportedKey := text.KeyTransportConnFooterUnsupportedBrowser
		if isNativeAppPlatform(st) {
			unsupportedKey = text.KeyTransportConnFooterUnsupportedApp
		}
		footerTxt = text.Msg(
			text.KeyCommonSentenceJoin,
			text.ArgText("first", footerTxt),
			text.ArgText("second", text.Msg(unsupportedKey)),
		)
	}
	out := &corev1.ConnCheckSummaryView{
		ServerState:       cc.ServerState,
		LatencyMs:         cc.LatencyMs,
		ConnState:         connState,
		NetworkAvailable:  networkAvailable,
		LivePushConnected: st.LivePushConnected || syncView.GetLivePushConnected(),
		EverOnline:        syncView.GetEverOnline(),
		Failures:          syncView.GetConnectFailures(),
		ConnStartedAtMs:   syncView.GetConnectionStartedAtMs(),
		ConnStep:          syncView.GetConnectionStep(),
		DisplayGrace:      st.DisplayGrace || syncView.GetDisplayGrace(),
		IsConnecting:      isConnecting,
		FooterText:        footerTxt,
	}
	if st != nil && !st.ConnectionReason.IsZero() {
		out.LastFailureText = ReasonText(st.ConnectionReason)
	} else if cc.LastFailure != "" {
		out.LastFailureText = text.Plain(cc.LastFailure)
	}

	switch st.World.Connection {
	case lists.ConnNoNetwork:
		out.StatusWord = text.Msg(text.KeyConnectionUnavailable)
		out.BannerText = text.Msg(text.KeyConnectionNoNetwork)
	case lists.ConnConnecting:
		out.StatusWord = text.Msg(text.KeyConnectionConnectingConnCheckLabel)
		out.BannerText = text.Msg(text.KeyConnectionConnectingConnCheck)
	case lists.ConnReconnecting:
		out.StatusWord = text.Msg(text.KeyConnectionReconnectingConnCheckLabel)
		out.BannerText = text.Msg(text.KeyConnectionReconnectingConnCheck)
	case lists.ConnOffline:
		out.StatusWord = text.Msg(text.KeyConnectionUnreachable)
		out.BannerText = text.Msg(text.KeyConnectionNoServer)
	default:
		switch cc.ServerState {
		case corev1.ConnProbeState_CONN_PROBE_STATE_CHECKING:
			out.StatusWord = text.Msg(text.KeyConnectionChecking)
		case corev1.ConnProbeState_CONN_PROBE_STATE_WORKING:
			out.StatusWord = text.Msg(text.KeyConnectionWorking)
			if cc.LatencyMs > 0 {
				out.SummaryText = text.Msg(text.KeyTransportReadLatencyRelay, text.Str("relay", strconv.FormatInt(int64(cc.LatencyMs), 10)))
			}
		case corev1.ConnProbeState_CONN_PROBE_STATE_SLOW:
			out.StatusWord = text.Msg(text.KeyConnectionSlow)
			if cc.LatencyMs > 0 {
				out.SummaryText = text.Msg(text.KeyTransportReadLatencyRelay, text.Str("relay", strconv.FormatInt(int64(cc.LatencyMs), 10)))
			}
		case corev1.ConnProbeState_CONN_PROBE_STATE_UNAVAILABLE:
			out.StatusWord = text.Msg(text.KeyConnectionUnavailable)
		default:
			out.StatusWord = text.Msg(text.KeyConnectionConnected)
		}
	}
	return out
}

func buildClientUpdateView(u UpdateState, clientProto uint32) *corev1.ClientUpdateView {
	protoVer := u.ProtocolVersion
	if protoVer == 0 {
		protoVer = clientProto
	}
	v := &corev1.ClientUpdateView{
		CurrentVersion:          u.CurrentVersion,
		ProtocolVersion:         protoVer,
		Channel:                 u.Channel,
		Checking:                u.Checking,
		AvailableVersion:        u.AvailableVersion,
		DownloadedVersion:       u.DownloadedVersion,
		DownloadProgressPercent: u.DownloadProgressPercent,
	}
	if u.AvailableVersion == "" && u.CurrentVersion != "" && len(u.Releases) > 0 {
		if pick := machine.PickUpgradeRelease(u.CurrentVersion, u.Channel, u.Releases); pick != nil {
			v.AvailableVersion = pick.Version
			v.ProtocolChanged = pick.ProtocolChanged
		}
	}
	v.HasUpdate = v.AvailableVersion != "" || v.DownloadedVersion != ""
	if u.ErrorMessage != "" {
		v.ErrorText = text.Plain(u.ErrorMessage)
	}
	switch {
	case u.Checking:
		v.StatusText = text.Msg(text.KeySettingsChecking)
	case v.HasUpdate:
		ver := v.DownloadedVersion
		if ver == "" {
			ver = v.AvailableVersion
		}
		v.StatusText = text.Plain(ver)
	case u.ErrorMessage == "":
		v.StatusText = text.Msg(text.KeySettingsUpToDate)
	}
	return v
}

// BuildSettings builds the top-level Settings screen ViewModel.
func BuildSettings(st *State, syncView *corev1.SyncStateView, extra *SettingsExtra) *corev1.SettingsViewModel {
	if extra == nil {
		extra = NewSettingsExtra()
	}
	removed := st.Removed || syncView.GetRemoved()
	unlinked := removed || syncView.GetUnlinked() || lists.WorldUnlinked(&st.World)
	isMgr := syncView.GetIsManager() || st.World.IsManager
	authName := machine.AuthorityName(syncView, st.World.Machines)
	online := st.World.Connection == lists.ConnConnected

	allClients, unfinished := buildClientRows(st, syncView)
	recent := allClients
	hasMore := false
	if len(allClients) > 4 {
		recent = allClients[:4]
		hasMore = true
	} else if len(unfinished) > 0 {
		hasMore = true
	}

	otherClients := 0
	for _, c := range allClients {
		if !c.IsSelf {
			otherClients++
		}
	}

	machineCards, pingDirect := buildMachineListCards(st, syncView, st.World.Machines, extra, true)

	vm := &corev1.SettingsViewModel{
		Online:            online,
		ClockSkewed:       st.World.AccountClockSkewed || syncView.GetAccountClockSkewed(),
		Unlinked:          unlinked,
		HasAccount:        syncView.GetSelfDeviceId() != "",
		IsManager:         isMgr,
		AuthorityName:     authName,
		RecoveryPending:   syncView.GetRecoveryPending(),
		RecoveryReadyMs:   syncView.GetRecoveryReadyMs(),
		Machines:          machineCards,
		CanAddDevice:      isMgr && !unlinked,
		RecentClients:     recent,
		HasMoreClients:    hasMore,
		TotalDevicesCount: int32(len(st.World.Machines) + len(allClients)),
		Notifications:     buildNotificationsView(extra.Notifications),
		Appearance:        buildAppearanceView(extra.Theme),
		Update:            buildClientUpdateView(extra.Update, st.World.ClientVersion),
		ConnCheck:         buildConnCheckSummary(st, syncView, extra.ConnCheck, machineCards, pingDirect),
		DiskReminders:     machine.BuildDiskReminders(st.World.Machines),
		QuotaBanner:       quota.Banner(st.World.Machines, st.Cal.NowMs),
		CanExitAccount:    syncView.GetSelfDeviceId() != "",
		ExitNeedsTransfer: isMgr && otherClients > 0,
	}

	if vm.ClockSkewed {
		vm.ClockSkewBannerText = text.Msg(text.KeyErrorsThisDeviceSClockIsBehindTheAccountStateRemote)
	}
	if removed {
		vm.UnlinkedBannerText = text.Msg(text.KeySettingsThisDeviceWasRemovedFromTheAccount)
	}
	if vm.RecoveryPending {
		if isMgr {
			vm.RecoveryBannerText = text.Msg(text.KeySecuritySomeoneIsRequestingManagementTakeoverWithARecoveryCode)
		} else {
			vm.RecoveryBannerText = text.Msg(text.KeySettingsRecoveryWaitingBanner)
		}
	}
	if len(vm.Machines) > 2 {
		vm.MachineCountText = text.Msg(text.KeySettingsMachineCount, text.Int("count", int64(len(vm.Machines))))
	}
	vm.UnfinishedPairingCount = int32(len(unfinished))
	totalClients := int64(len(allClients))
	if len(unfinished) > 0 {
		vm.ClientsSummaryWarn = true
		if totalClients > 0 {
			vm.ClientsSummaryText = text.Msg(
				text.KeySettingsClientsSummaryPending,
				text.Int("count", totalClients),
				text.Int("pending", int64(len(unfinished))),
			)
		} else {
			vm.ClientsSummaryText = text.Msg(text.KeySettingsClientsSummaryPendingOnly, text.Int("pending", int64(len(unfinished))))
		}
	} else if totalClients <= 1 {
		vm.ClientsSummaryText = text.Msg(text.KeySettingsClientsSummarySelfOnly)
	} else {
		vm.ClientsSummaryText = text.Msg(text.KeySettingsClientsSummaryWithOthers, text.Int("count", totalClients))
	}

	if !isMgr && authName != "" {
		vm.AddDeviceHint = text.Msg(text.KeySettingsAddDeviceFooterHint, text.Str("name", authName))
	}
	if len(vm.Machines) == 0 {
		if waitingForMachineList(st) {
			vm.MachinesLoading = true
			vm.EmptyMachinesText = text.Msg(text.KeyCommonLoading)
		} else {
			vm.EmptyMachinesText = text.Msg(text.KeySettingsEmptyComputersTitle)
			if isMgr {
				vm.EmptyMachinesBody = text.Msg(text.KeySettingsEmptyComputersAuthorityBody)
			} else if authName != "" {
				vm.EmptyMachinesBody = text.Msg(text.KeySettingsEmptyComputersMemberBody, text.Str("name", authName))
			}
		}
	}

	var secSummary *corev1.Text
	if isMgr {
		secSummary = text.Msg(text.KeySecurityThisDeviceManagesTheAccount)
	} else {
		secSummary = text.Msg(text.KeySettingsSecuritySummaryNonAuthority, text.Str("name", authName))
	}
	vm.Security = &corev1.SecuritySummaryView{
		IsManager:           isMgr,
		Unlinked:            unlinked,
		AuthorityDeviceId:   syncView.GetAuthorityDeviceId(),
		AuthorityName:       authName,
		RecoveryPending:     syncView.GetRecoveryPending(),
		RecoveryReadyMs:     syncView.GetRecoveryReadyMs(),
		SummaryText:         secSummary,
		RecoveryNotice:      vm.RecoveryBannerText,
		HasHandoffOffer:     syncView.GetHasHandoffOffer(),
		RecoveryStatusStale: syncView.GetRecoveryStatusStale(),
		LeaseEndMs:          syncView.GetLeaseEndMs(),
		HandoffCandidates:   buildHandoffCandidates(syncView),
	}
	return vm
}

func buildHandoffCandidates(syncView *corev1.SyncStateView) []*corev1.HandoffCandidateView {
	if syncView == nil {
		return nil
	}
	selfID := syncView.GetSelfDeviceId()
	sorted := machine.SortClients(syncView.GetDevices(), selfID)
	var out []*corev1.HandoffCandidateView
	for _, d := range sorted {
		if d == nil || d.DeviceId == "" || d.DeviceId == selfID || d.Kind != v1.DeviceKind_DEVICE_KIND_CLIENT {
			continue
		}
		if syncView.GetSecurityLoaded() && !d.GetTrusted() {
			continue
		}
		if d.Meta == nil || len(d.Meta.HandoffRecoveryEd) != 32 {
			continue
		}
		out = append(out, &corev1.HandoffCandidateView{
			DeviceId:        d.DeviceId,
			DisplayName:     machine.ReadableClientPlainName(d, sorted),
			DisplayNameText: machine.ReadableClientNameText(d, sorted),
		})
	}
	return out
}

// BuildMachineSettings builds the per-machine details & daemon configuration ViewModel.
func BuildMachineSettings(st *State, syncView *corev1.SyncStateView, extra *SettingsExtra, machineID string) *corev1.MachineSettingsViewModel {
	if extra == nil {
		extra = NewSettingsExtra()
	}
	m := findMachineByID(st, machineID)
	removed := st.Removed || syncView.GetRemoved()
	unlinked := removed || syncView.GetUnlinked() || lists.WorldUnlinked(&st.World)
	isMgr := syncView.GetIsManager() || st.World.IsManager
	authID := syncView.GetAuthorityDeviceId()
	onlineWorld := st.World.Connection == lists.ConnConnected

	vm := &corev1.MachineSettingsViewModel{
		MachineId: machineID,
		Unlinked:  unlinked,
		AppOnline: onlineWorld,
		IsManager: isMgr,
	}
	if m == nil {
		vm.Name = machine.ShortID(machineID)
		vm.ShortId = machine.ShortMachineID(machineID)
		if !unlinked && waitingForMachineList(st) {
			vm.Loading = true
			vm.OfflineOrMismatchBanner = text.Msg(text.KeyCommonLoading)
		} else {
			vm.OfflineOrMismatchBanner = text.Msg(text.KeySettingsDeviceNotFound)
		}
		return vm
	}

	vm.Found = true
	vm.Name = lists.MachineName(m, machineID)
	vm.ShortId = machine.ShortMachineID(machineID)
	vm.Member = !m.Archived && !removed
	vm.Online = lists.MachineReachable(m)
	vm.ProtocolMismatch = lists.ProtocolMismatchDirection(m, st.World.ClientVersion)
	vm.Editable = vm.Member && vm.Online && !unlinked && vm.ProtocolMismatch == 0

	if !vm.Member {
		vm.RemovedBanner = text.Msg(text.KeySettingsThisDeviceWasRemovedFromTheAccount)
	} else if !vm.Online {
		vm.OfflineOrMismatchBanner = text.Msg(text.KeySettingsComputerSettingsOffline, text.Str("name", vm.Name))
	} else if vm.ProtocolMismatch < 0 {
		vm.OfflineOrMismatchBanner = text.Msg(text.KeySettingsMachineSubVersionMismatch)
	} else if vm.ProtocolMismatch > 0 {
		vm.OfflineOrMismatchBanner = text.Msg(text.KeySettingsMachineSubVersionMismatchClient)
	}

	// Top bar dot + subtitle
	var statusWord *corev1.Text
	switch {
	case !vm.Member:
		vm.HeaderDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_GRAY_STATIC
		statusWord = text.Msg(text.KeySettingsStatusRemoved)
	case vm.ProtocolMismatch != 0:
		vm.HeaderDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_ORANGE_STATIC
		statusWord = text.Msg(text.KeySettingsStatusNeedsUpdate)
	case vm.Online:
		vm.HeaderDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_GREEN_LIVE
		statusWord = text.Msg(text.KeyCommonOnline)
	default:
		vm.HeaderDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_GRAY_STATIC
		statusWord = text.Msg(text.KeySettingsOffline)
	}
	if !vm.Member {
		vm.HeaderSubtitle = text.Msg(
			text.KeySettingsHeaderSubtitlePair,
			text.ArgText("left", statusWord),
			text.ArgText("right", text.Msg(text.KeySettingsMachineSubRemoved)),
		)
	} else {
		var subParts []*corev1.Text
		subParts = append(subParts, statusWord)
		if !vm.Online && m.LastSeenMs > 0 {
			if rel := formatRelativeTime(m.LastSeenMs, st.Cal); rel != nil {
				subParts = append(subParts, rel)
			}
		}
		if m.Meta != nil && m.Meta.DaemonVersion != "" {
			subParts = append(subParts, text.Msg(text.KeySettingsDaemonVersionTag, text.Str("version", m.Meta.DaemonVersion)))
		}
		vm.HeaderSubtitle = text.JoinLiteral(" · ", subParts...)
	}

	// Overview telemetry rings & card header
	if m.Meta != nil && m.Meta.Capabilities != nil && m.Meta.Capabilities.MachineStatus {
		vm.ShowStatusEntry = true
		var feed *MachinePollFeed
		if extra.PollFeeds != nil {
			feed = extra.PollFeeds[machineID]
		}
		var shown *v1.MachineStatus
		if vm.Online && feed != nil {
			shown = feed.Last
		}
		vm.StatusRings = machine.BuildRings(vm.Online && shown != nil, shown)
		diskTight := (shown != nil && shown.Disk != nil &&
			(shown.Disk.Pressure == v1.ResourcePressure_RESOURCE_PRESSURE_WARNING ||
				shown.Disk.Pressure == v1.ResourcePressure_RESOURCE_PRESSURE_CRITICAL)) ||
			(m.Meta.DiskStatus != nil && m.Meta.DiskStatus.AvailableBytes < machine.DiskReminderThresholdBytes)
		switch {
		case !vm.Member:
			vm.StatusCardDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_GRAY_STATIC
			vm.StatusCardBadge = text.Msg(text.KeySettingsStatusRemoved)
		case vm.ProtocolMismatch != 0:
			vm.StatusCardDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_GRAY_STATIC
			vm.StatusCardBadge = text.Msg(text.KeySettingsStatusCardPaused)
		case !vm.Online:
			vm.StatusCardDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_GRAY_STATIC
			vm.StatusCardBadge = text.Msg(text.KeySettingsOffline)
		case diskTight:
			vm.StatusCardDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_ORANGE_STATIC
			vm.StatusCardBadge = text.Msg(text.KeySettingsStatusCardDiskTight)
			vm.StatusCardWarn = true
		default:
			vm.StatusCardDot = corev1.MachineStatusDot_MACHINE_STATUS_DOT_GREEN_LIVE
			vm.StatusCardBadge = text.Msg(text.KeyMachineStatusLive)
		}
	}

	// Account quota rows + 7d usage summary row
	vm.QuotaRows = quota.BuildCliSections(m, v1.Agent_AGENT_UNSPECIFIED, st.Cal.NowMs)
	usageCap := m.Meta != nil && m.Meta.Capabilities != nil && m.Meta.Capabilities.UsageStats
	vm.ShowUsageEntry = true
	if !vm.Member || vm.ProtocolMismatch != 0 || !usageCap {
		vm.UsageEmptyText = text.Plain("—")
	} else {
		snap7, cached7, _, _ := lookupUsageSnapshot(st, machineID, v1.UsageRangePreset_USAGE_RANGE_PRESET_SEVEN_DAYS, 0, 0, vm.Online)
		tot := sumEnabledUsageTokens(m.Meta, snap7)
		if tot > 0 {
			vm.UsageTotalTokens = tot
			vm.UsageTokensText = usage.FormatTokensText(tot, st.UILocale)
			vm.UsageCachedOffline = cached7
		} else {
			snap30, cached30, _, _ := lookupUsageSnapshot(st, machineID, v1.UsageRangePreset_USAGE_RANGE_PRESET_THIRTY_DAYS, 0, 0, vm.Online)
			if tot30 := sumEnabledUsageTokens(m.Meta, snap30); tot30 > 0 {
				vm.UsageTotalTokens = tot30
				vm.UsageTokensText = usage.FormatTokensText(tot30, st.UILocale)
				vm.UsageFallbackThirtyDays = true
				vm.UsageCachedOffline = cached30
			} else if snap7 != nil || snap30 != nil {
				vm.UsageEmptyText = text.Msg(text.KeySettingsUsageSummaryEmpty)
			} else {
				vm.UsageEmptyText = text.Plain("—")
			}
		}
	}

	// Direct connection group
	mExtra := extra.Machines[machineID]
	directPref, directReport := machineDirectInputs(st, extra, machineID)
	vm.Connection = machine.BuildConnectionGroup(vm.Online && vm.Member, directPref, directReport)

	// Daemon Settings & Cache (needed before building per-CLI defaults summaries)
	sEntry := st.MachineSettings[machineID]
	if sEntry != nil {
		vm.SettingsLoading = sEntry.Loading
		if sEntry.ErrorText != nil {
			vm.SettingsError = sEntry.ErrorText
		} else if sEntry.Error != "" {
			vm.SettingsError = text.Msg(text.KeySettingsReadError, text.Str("reason", sEntry.Error))
		}
	}
	if mExtra != nil {
		vm.SavePending = mExtra.SavePending
		vm.SaveUnknown = mExtra.SaveUnknown
		vm.SaveError = mExtra.SaveError
	}

	var ds *v1.DaemonSettings
	if sEntry != nil && sEntry.Settings != nil {
		ds = sEntry.Settings
	}

	// CLI versions, switches & per-CLI defaults
	versions := lists.MachineVersions(m.Meta)
	disabledMap := lists.MachineDisabledSet(m.Meta)
	if disabledMap == nil {
		disabledMap = make(map[v1.Agent]bool)
	}
	if mExtra != nil {
		for ag, en := range mExtra.TogglePendingValue {
			if mExtra.ToggleSaving[ag] {
				disabledMap[ag] = !en
			}
		}
	}
	installedAgents := lists.MachineInstalledAgents(m.Meta)
	enabledCount := 0
	for _, a := range installedAgents {
		if !disabledMap[a] {
			enabledCount++
		}
	}
	for _, info := range versions {
		if info == nil {
			continue
		}
		eff := lists.EffectiveAgent(info.Agent)
		cliName := lists.AgentInfo(info.Agent).Name
		var unv *corev1.Text
		if !info.Verified {
			vm.HasUnverifiedCli = true
			unv = text.Msg(text.KeySettingsUnverified)
		}
		isEnabled := !disabledMap[eff]
		isLocked := enabledCount <= 1 && isEnabled
		isSaving := mExtra != nil && mExtra.ToggleSaving[eff]
		cliDef := buildAgentDefaultsView(st, m, ds, eff)
		defModeLbl, defModeDanger := summarizeDefaultMode(st, m, eff, cliDef)
		vm.CliVersions = append(vm.CliVersions, &corev1.CliVersionView{
			Agent:             info.Agent,
			Name:              cliName,
			Version:           info.Version,
			Verified:          info.Verified,
			UnverifiedLabel:   unv,
			Enabled:           isEnabled,
			LockedLastEnabled: isLocked,
			Saving:            isSaving,
			DefaultModelLabel: cliDef.SelectedModelLabel,
			DefaultModeLabel:  defModeLbl,
			DefaultModeDanger: defModeDanger,
			DisabledLabel:     text.Msg(text.KeySettingsCliDisabled),
			Defaults:          cliDef,
		})
	}
	leaseExpired := syncView != nil && syncView.GetSecurityLoaded() && syncView.GetLeaseEndMs() > 0 && st.Cal.NowMs > 0 && st.Cal.NowMs >= syncView.GetLeaseEndMs()
	hasCliSwitchesCap := m.Meta != nil && m.Meta.Capabilities != nil && m.Meta.Capabilities.CliSwitches
	vm.ShowCliSwitches = hasCliSwitchesCap && len(installedAgents) >= 2
	vm.CliSwitchesLeaseExpired = leaseExpired
	vm.CliSwitchesEditable = vm.Editable && !leaseExpired
	if vm.ShowCliSwitches {
		if !vm.Online && vm.Member {
			vm.CliSwitchesDisabledReason = text.Msg(text.KeySettingsCliSwitchesOfflineReason, text.Str("name", vm.Name))
		}
		if leaseExpired {
			vm.CliSwitchesFooter = text.Msg(text.KeySettingsCliSwitchesLeaseExpiredFooter)
		} else {
			vm.CliSwitchesFooter = text.Msg(text.KeySettingsCliSwitchesFooter)
		}
		if mExtra != nil && mExtra.ToggleBanner != nil {
			vm.CliSwitchesConflictBanner = mExtra.ToggleBanner
		}
	}

	// Available CLIs for per-CLI defaults
	agents := lists.MachineAgents(m.Meta)
	if len(agents) == 0 {
		agents = []v1.Agent{v1.Agent_AGENT_CODEX}
	}
	vm.AvailableAgents = agents
	selAgent := agents[0]
	if mExtra != nil && mExtra.SelectedAgent != v1.Agent_AGENT_UNSPECIFIED {
		for _, a := range agents {
			if a == mExtra.SelectedAgent {
				selAgent = a
				break
			}
		}
	}
	vm.SelectedAgent = selAgent
	vm.AgentDefaults = buildAgentDefaultsView(st, m, ds, selAgent)

	var timeoutSec int32
	var idleMin int32
	var imgQuotaBytes int64
	if ds != nil {
		timeoutSec = ds.ApprovalTimeoutSeconds
		idleMin = ds.IdleCloseMinutes
		imgQuotaBytes = ds.ImageUploadQuotaBytes
	}
	vm.SelectedApprovalTimeoutSeconds = timeoutSec
	vm.ApprovalTimeoutChoices = machine.BuildTimeoutChoices(timeoutSec)
	vm.SelectedIdleCloseMinutes = machine.IdleCloseValue(idleMin)
	vm.IdleCloseChoices = machine.BuildIdleCloseChoices(idleMin)
	vm.AttachmentStorage = buildAttachmentStorageView(imgQuotaBytes, mExtra, vm.Member && vm.ProtocolMismatch == 0)

	// Projects on machine: align count with ManageProjects(machineId) filter
	for _, p := range machine.CollectMachineProjects(m, st.World.Sessions) {
		vm.Projects = append(vm.Projects, &corev1.MachineProjectView{
			ProjectKey:     p.Path,
			Name:           p.Name,
			Path:           p.Path,
			LastActiveText: formatRelativeTime(p.LastActiveMs, st.Cal),
		})
	}
	allProjRes := lists.BuildProjects(&st.World, "", st.RememberedScope, st.RememberedScopeToken, nil, st.Scope)
	filteredProj := filterProjectsByMachine(allProjRes, machineID)
	vm.ProjectCount = int32(len(filteredProj.Listed))
	if vm.ProjectCount > 0 {
		vm.ProjectCountText = text.Msg(text.KeySettingsProjectCountValue, text.Int("count", int64(vm.ProjectCount)))
	}

	// Machine management availability
	isAuth := m.ID == authID
	vm.Management = &corev1.MachineManagementActionsView{
		IsAuthorityDevice: isAuth,
		CanRejoin:         !vm.Member && isMgr && onlineWorld,
		CanDeleteRecords:  !vm.Member && isMgr,
		CanRemove:         vm.Member && isMgr && !isAuth && onlineWorld,
	}
	if isAuth {
		vm.Management.ExplanationText = text.Msg(text.KeySecurityThisDeviceManagesTheAccount)
	} else if !isMgr {
		if authName := machine.AuthorityName(syncView, st.World.Machines); authName != "" {
			vm.Management.ExplanationText = text.Msg(text.KeySettingsRemoveNonAuthorityFooter, text.Str("name", authName))
		}
	}
	return vm
}

func buildAgentDefaultsView(st *State, m *lists.Machine, ds *v1.DaemonSettings, agent v1.Agent) *corev1.MachineAgentDefaultsView {
	info := lists.AgentInfo(agent)
	def := lists.DefaultsFor(ds, agent)
	selModel := ""
	selEffort := ""
	selMode := v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED
	if def != nil {
		selModel = def.Model
		selEffort = def.Effort
		selMode = def.Mode
	}

	mEntry := st.MachineModels[MachineAgentKey{MachineID: m.ID, Agent: agent}]
	var models []*v1.ModelInfo
	hasModels := false
	if mEntry != nil && mEntry.Response != nil {
		models = mEntry.Response.Models
		hasModels = true
	}

	pickedModel := lists.FindModel(models, selModel)
	shownModels := lists.ShownModels(models, pickedModel, false)

	var selModelLbl *corev1.Text
	if selModel == "" {
		selModelLbl = text.Msg(text.KeyModelsDefault)
	} else {
		selModelLbl = text.Plain(lists.ModelLabel(models, hasModels, selModel, agent))
	}

	modelChoices := []*corev1.ModelChoiceView{
		{
			Id:        "",
			Label:     text.Msg(text.KeyModelsDefault),
			Selected:  selModel == "",
			IsDefault: true,
		},
	}
	for _, mod := range shownModels {
		if mod == nil {
			continue
		}
		modelChoices = append(modelChoices, &corev1.ModelChoiceView{
			Id:       mod.Slug,
			Label:    text.Plain(lists.ModelLabel(models, hasModels, mod.Slug, agent)),
			Desc:     mod.Description,
			Selected: selModel == mod.Slug,
		})
	}

	effortChoices := []*corev1.EffortChoiceView{
		{
			Id:        "",
			Label:     text.Msg(text.KeyModelsEffortDefault),
			Selected:  selEffort == "",
			IsDefault: true,
		},
	}
	if pickedModel != nil {
		for _, eff := range pickedModel.Efforts {
			effortChoices = append(effortChoices, &corev1.EffortChoiceView{
				Id:       eff,
				Label:    text.Plain(eff),
				Selected: selEffort == eff,
			})
		}
	}

	// Approval modes in Settings: [UNSPECIFIED (follow CLI), ASK, AUTO, FULL_ACCESS]
	var cliDefaultModeLbl *corev1.Text
	dm := lists.DefaultModeOf(m, agent)
	if dm.Mode == v1.ApprovalMode_APPROVAL_MODE_ASK ||
		dm.Mode == v1.ApprovalMode_APPROVAL_MODE_AUTO ||
		dm.Mode == v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS {
		cliDefaultModeLbl = modeNameText(dm.Mode)
	}
	var followLbl *corev1.Text
	if cliDefaultModeLbl != nil && !dm.Varies {
		followLbl = text.Msg(text.KeyModesDefaultNamed, text.ArgText("mode", cliDefaultModeLbl))
	} else {
		followLbl = text.Msg(text.KeySettingsFollowConfiguration, text.Str("value", info.Name))
	}

	autoUnavail := false
	var autoReason *corev1.Text
	if lists.AutoUnavailable(models, hasModels, selModel) {
		autoUnavail = true
		if lists.AllAutoUnavailable(models) {
			autoReason = text.Msg(text.KeyModesAccountNoAuto)
		} else {
			autoReason = text.Msg(text.KeyModesModelNoAuto)
		}
	}

	modeChoices := []*corev1.ModeChoiceView{
		{
			Mode:     v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED,
			Label:    followLbl,
			Selected: selMode == v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED,
		},
		{
			Mode:     v1.ApprovalMode_APPROVAL_MODE_ASK,
			Label:    text.Msg(text.KeyModesAsk),
			Selected: selMode == v1.ApprovalMode_APPROVAL_MODE_ASK,
		},
		{
			Mode:           v1.ApprovalMode_APPROVAL_MODE_AUTO,
			Label:          text.Msg(text.KeyModesAuto),
			Selected:       selMode == v1.ApprovalMode_APPROVAL_MODE_AUTO,
			Disabled:       autoUnavail,
			DisabledReason: autoReason,
		},
		{
			Mode:     v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS,
			Label:    text.Msg(text.KeyModesFullAccess),
			Selected: selMode == v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS,
			Danger:   true,
		},
	}

	return &corev1.MachineAgentDefaultsView{
		Agent:                 agent,
		AgentName:             info.Name,
		SelectedModel:         selModel,
		SelectedModelLabel:    selModelLbl,
		ModelChoices:          modelChoices,
		SelectedEffort:        selEffort,
		EffortChoices:         effortChoices,
		SelectedMode:          selMode,
		ModeChoices:           modeChoices,
		AutoUnavailableNotice: autoReason,
		FooterText:            text.Msg(text.KeySettingsNewProjectDefaultsSettings, text.Str("value", info.Name)),
	}
}

func summarizeDefaultMode(_ *State, m *lists.Machine, agent v1.Agent, def *corev1.MachineAgentDefaultsView) (*corev1.Text, bool) {
	if def != nil && def.SelectedMode != v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED {
		return modeNameText(def.SelectedMode), def.SelectedMode == v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS
	}
	dm := lists.DefaultModeOf(m, agent)
	if dm.Known && dm.Mode != v1.ApprovalMode_APPROVAL_MODE_UNSPECIFIED && !dm.Varies {
		return modeNameText(dm.Mode), dm.Mode == v1.ApprovalMode_APPROVAL_MODE_FULL_ACCESS
	}
	return text.Msg(text.KeySettingsFollowConfiguration, text.Str("value", lists.AgentInfo(agent).Name)), false
}

func sumEnabledUsageTokens(meta *v1.MachineMeta, snap *v1.UsageStatsResponse) uint64 {
	if snap == nil {
		return 0
	}
	disabled := lists.MachineDisabledSet(meta)
	var total uint64
	for _, s := range snap.Series {
		if s == nil || s.Current == nil || s.Current.UsageRequestCount == 0 {
			continue
		}
		if disabled != nil && disabled[lists.EffectiveAgent(s.Agent)] {
			continue
		}
		total += usage.TotalTokens(s.Current)
	}
	return total
}

func storageRatioPct(b, limitBytes int64) int32 {
	if b <= 0 {
		return 0
	}
	p := int32(math.Round(float64(b) * 100.0 / float64(limitBytes)))
	if p < 2 {
		return 2
	}
	if p > 100 {
		return 100
	}
	return p
}

func storageScaledPct(v int32, scale float64) int32 {
	if v == 0 {
		return 0
	}
	sv := int32(math.Floor(float64(v) * scale))
	if sv < 1 {
		return 1
	}
	return sv
}

func storagePercents(upBytes, preBytes, resBytes, limitBytes int64) (int32, int32, int32) {
	if limitBytes <= 0 {
		return 0, 0, 0
	}
	up, pre, res := storageRatioPct(upBytes, limitBytes), storageRatioPct(preBytes, limitBytes), storageRatioPct(resBytes, limitBytes)
	sum := up + pre + res
	if sum > 100 {
		scale := 100.0 / float64(sum)
		up, pre, res = storageScaledPct(up, scale), storageScaledPct(pre, scale), storageScaledPct(res, scale)
	}
	return up, pre, res
}

func buildAttachmentStorageView(quotaBytes int64, extra *MachineSettingsExtra, activeMember bool) *corev1.AttachmentStorageView {
	effLimit := quotaBytes
	if effLimit <= 0 {
		effLimit = machine.DefaultUploadQuotaBytes
	}
	limitLbl := machine.FormatBinarySize(uint64(effLimit))
	v := &corev1.AttachmentStorageView{
		SelectedQuotaBytes: quotaBytes,
		QuotaChoices:       machine.BuildImageQuotaChoices(quotaBytes),
		LimitLabel:         limitLbl,
		FooterText:         text.Msg(text.KeySettingsAttachmentsAreStoredOnThisComputerSessionFilesRemain),
	}
	if !activeMember {
		v.SummaryText = text.Plain("—")
		return v
	}
	if extra == nil || extra.ImageStorage == nil {
		v.SummaryText = text.Plain("—")
		return v
	}
	st := extra.ImageStorage
	v.Loaded = true
	v.Cleaning = extra.CleaningImages
	v.UploadedText = machine.FormatAttachmentSize(st.UploadBytes)
	v.PreviewCacheText = machine.FormatAttachmentSize(st.CacheBytes)

	usedTotal := st.UploadBytes + st.CacheBytes
	usedTxt := machine.FormatAttachmentSize(usedTotal)
	v.UsedTotalText = usedTxt
	v.UploadedPercent, v.PreviewPercent, v.ReservePercent = storagePercents(st.UploadBytes, st.CacheBytes, st.EmergencyReserveBytes, effLimit)

	nearFull := effLimit > 0 && float64(st.UploadBytes+st.EmergencyReserveBytes)/float64(effLimit) >= 0.85
	v.SummaryWarn = st.EmergencyReserveBytes > 0 || nearFull
	if st.EmergencyReserveBytes > 0 {
		v.SummaryText = text.Msg(text.KeySettingsStorageSummaryReserve, text.ArgText("used", usedTxt), text.ArgText("limit", limitLbl))
	} else {
		v.SummaryText = text.Msg(text.KeySettingsStorageSummaryPair, text.ArgText("used", usedTxt), text.ArgText("limit", limitLbl))
	}

	reclaim := st.ReclaimableBytes + st.CacheBytes
	v.CanClear = reclaim > 0 && !extra.CleaningImages
	if st.ReservedBytes > 0 {
		v.UploadedDetail = text.Msg(text.KeySettingsUploading, text.ArgText("value", machine.FormatAttachmentSize(st.ReservedBytes)))
	} else if st.ReclaimableBytes > 0 {
		v.UploadedDetail = text.Msg(text.KeySettingsCanClear, text.ArgText("value", machine.FormatAttachmentSize(st.ReclaimableBytes)))
	}
	if st.EmergencyReserveBytes > 0 {
		v.ShowEmergencyReserve = true
		v.EmergencyReserveText = machine.FormatAttachmentSize(st.EmergencyReserveBytes)
	}
	if reclaim > 0 {
		v.ClearButtonLabel = text.Msg(text.KeySettingsCanFree, text.ArgText("value", machine.FormatAttachmentSize(reclaim)))
	} else {
		v.ClearButtonLabel = text.Msg(text.KeySettingsNothingToClear)
	}
	return v
}

// BuildDevices builds the Devices / Clients list screen ViewModel.
func BuildDevices(st *State, syncView *corev1.SyncStateView, extra *SettingsExtra) *corev1.DevicesViewModel {
	if extra == nil {
		extra = NewSettingsExtra()
	}
	isMgr := syncView.GetIsManager() || st.World.IsManager
	unlinked := syncView.GetUnlinked() || lists.WorldUnlinked(&st.World)
	online := st.World.Connection == lists.ConnConnected
	authName := machine.AuthorityName(syncView, st.World.Machines)
	clients, unfinished := buildClientRows(st, syncView)

	deviceComputers, _ := buildMachineListCards(st, syncView, st.World.Machines, extra, false)
	vm := &corev1.DevicesViewModel{
		IsManager:          isMgr,
		Online:             online,
		Unlinked:           unlinked,
		AuthorityDeviceId:  syncView.GetAuthorityDeviceId(),
		AuthorityName:      authName,
		CanAddDevice:       isMgr && !unlinked,
		Computers:          deviceComputers,
		Clients:            clients,
		UnfinishedPairings: unfinished,
		ErrorText:          syncView.GetDevicesErrorText(),
		ClockSkewed:        st.World.AccountClockSkewed || syncView.GetAccountClockSkewed(),
		RecoveryPending:    syncView.GetRecoveryPending(),
		LeaseEndMs:         syncView.GetLeaseEndMs(),
		LoadFailed:         syncView.GetDevicesLoadFailed(),
	}
	if len(clients) > 0 {
		vm.ClientCountText = text.Msg(text.KeySettingsClientsCount, text.Int("count", int64(len(clients))))
	}
	if len(vm.Computers) == 0 {
		if waitingForMachineList(st) {
			vm.MachinesLoading = true
			vm.EmptyComputersText = text.Msg(text.KeyCommonLoading)
		} else {
			vm.EmptyComputersText = text.Msg(text.KeySettingsNoComputersYet)
		}
	}
	if !isMgr && authName != "" {
		vm.AddDeviceHint = text.Msg(text.KeySecurityAddOnAuthority, text.Str("name", authName))
	}
	return vm
}

// BuildClientDetail builds the single-client details & rename/remove ViewModel.
func BuildClientDetail(st *State, syncView *corev1.SyncStateView, extra *SettingsExtra, deviceID string) *corev1.ClientDetailViewModel {
	if extra == nil {
		extra = NewSettingsExtra()
	}
	selfID := syncView.GetSelfDeviceId()
	authID := syncView.GetAuthorityDeviceId()
	isMgr := syncView.GetIsManager() || st.World.IsManager
	online := st.World.Connection == lists.ConnConnected

	var dev *corev1.SyncDeviceView
	for _, d := range syncView.GetDevices() {
		if d != nil && d.DeviceId == deviceID {
			dev = d
			break
		}
	}

	vm := &corev1.ClientDetailViewModel{
		DeviceId:      deviceID,
		IsSelf:        deviceID == selfID,
		IsAuthority:   deviceID == authID,
		MaxNameLength: machine.MaxClientNameLength,
		RenameSaving:  extra.RenameSaving,
		RenameError:   extra.RenameError,
	}
	if dev == nil {
		vm.DisplayName = machine.ShortID(deviceID)
		vm.ShortId = machine.ShortID(deviceID)
		return vm
	}

	vm.Found = true
	vm.Member = dev.Trusted || vm.IsSelf || vm.IsAuthority
	vm.DefaultName = machine.DefaultClientName(dev)
	if dev.Meta != nil {
		vm.CustomName = dev.Meta.Name
	}
	vm.ShortId = machine.ShortID(deviceID)
	vm.DisplayName = machine.ClientName(dev)
	if vm.DisplayName == "" {
		vm.DisplayName = vm.ShortId
	}
	vm.Description = vm.DefaultName

	if vm.IsSelf {
		vm.RoleLabel = text.Msg(text.KeySettingsCurrent)
		vm.LastUsedText = text.Msg(text.KeySettingsNow)
		vm.ShowExitAction = true
		vm.ManagementHint = text.Msg(text.KeySettingsNamesSyncEncryptedTheServerCannotReadThemLeave)
	} else {
		if vm.IsAuthority {
			vm.RoleLabel = text.Msg(text.KeySettingsManagingClient)
		} else {
			vm.RoleLabel = text.Msg(text.KeySettingsPairedClient)
		}
		if dev.LastSeenMs <= 0 {
			vm.LastUsedText = text.Msg(text.KeySettingsNoRecord)
		} else {
			vm.LastUsedText = formatRelativeTime(dev.LastSeenMs, st.Cal)
		}
		vm.ManagementHint = text.Msg(text.KeySettingsSetTheNameAndNotificationPreferencesOnThatDevice)
	}

	vm.AddedText = formatCalendarDay(dev.CreatedMs, st.Cal)
	vm.CanRename = vm.IsSelf && online && vm.Member
	vm.CanRestoreDefault = vm.CanRename && strings.TrimSpace(vm.CustomName) != ""
	vm.CanRemove = isMgr && !vm.IsSelf && !vm.IsAuthority && online && vm.Member
	return vm
}
