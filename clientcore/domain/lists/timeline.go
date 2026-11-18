package lists

import (
	"cmp"
	"crypto/hmac"
	"crypto/sha256"
	"slices"
	"strconv"
	"strings"

	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

const (
	// AttentionMax is the default maximum collapsed attention rows on mobile Home.
	AttentionMax = 3
	// RunningMax is the default maximum collapsed running rows on mobile Home.
	RunningMax = 5
	// EndedPage is the default page step for revealing ended sessions.
	EndedPage = 30
	// FilterOver shows the title filter input when total scoped sessions exceed this threshold.
	FilterOver = 8
)

// SessionItem captures all domain facts needed to render one session or attention row.
type SessionItem struct {
	Session        *Session
	ID             string
	MachineID      string
	RawStatus      SessionDisplayStatus
	ShownStatus    SessionDisplayStatus
	Tone           SessionTone
	NeedsAttention bool
	Exited         bool
	Forked         bool
	Agent          v1.Agent
	Title          string
	HasCustomTitle bool
	ResolvedTitle  string
	ProjectKey     string
	ProjectName    string
	MachineName    string
	PlainPreview   string
	StatusDetail   string
	HasPopupTitle  bool
	OutsideScope   bool
	CreatedMs      int64
	EndedMs        int64
	AttentionMs    int64
	UpdatedMs      int64
	EndedGroup     *EndedGroup
	ContextPercent *int32
	Mode           v1.ApprovalMode
	RawMode        string
	MinutesLeft    int64
}

// HomeEmptyKind identifies why the home timeline has no rows.
type HomeEmptyKind uint8

const (
	HomeEmptyNone HomeEmptyKind = iota
	HomeEmptyLoading
	HomeEmptyNoMachines
	HomeEmptyUnlinked
	HomeEmptyNoneOnline
	HomeEmptyReady
	HomeEmptyScope
)

// HomeEmptyInfo describes the empty state on Home.
type HomeEmptyInfo struct {
	Kind             HomeEmptyKind
	ReadyMachineID   string
	ReadyMachineName string
}

// HomeHeadlineKind classifies the Home header summary state.
type HomeHeadlineKind uint8

const (
	HeadlineNone HomeHeadlineKind = iota
	HeadlineNoNetwork
	HeadlineConnecting
	HeadlineReconnecting
	HeadlineNoServer
	HeadlineWaiting
	HeadlineRunning
	HeadlineEmptyProject
	HeadlineUnreadable
	HeadlineNoSessions
	HeadlineNothingWaits
)

// HomeHeadlineInfo carries domain counts and flags for building HomeHeadlineView.
type HomeHeadlineInfo struct {
	Kind             HomeHeadlineKind
	WaitingCount     int32
	RunningCount     int32
	IdleCount        int32
	RawError         string
	ProjectShortPath string
	Kept             bool
	SubError         bool
	SubPath          bool
	Details          bool
	Unlinked         bool
}

// HomeTailKind classifies the bottom tail indicator of Home and Project ended lists.
type HomeTailKind uint8

const (
	TailNone HomeTailKind = iota
	TailEarlierOffline
	TailEarlierListFailed
	TailEarlierFailed
	TailLoadingEarlier
	TailFilterNoMatches
	TailFilterMatches
	TailTotal
	TailTotalLoadEarlier
)

// HomeTailInfo carries domain facts for rendering a list tail row.
type HomeTailInfo struct {
	Kind     HomeTailKind
	Count    int32
	Query    string
	RawError string
	Retry    bool
}

// HomeRowKind classifies rows in the Home list.
type HomeRowKind uint8

const (
	HomeRowAttentionCard HomeRowKind = iota + 1
	HomeRowAttention
	HomeRowMoreAttention
	HomeRowSectionLabel
	HomeRowRunning
	HomeRowMoreRunning
	HomeRowEnded
	HomeRowTail
)

// HomeRowData holds one ordered row of the Home list.
type HomeRowData struct {
	RowID      string
	Kind       HomeRowKind
	Item       *SessionItem
	LabelKey   string
	EndedGroup *EndedGroup
	MoreCount  int32
	Tail       *HomeTailInfo
	First      bool
	Last       bool
}

// HomeShown configures collapse/expansion visibility for Home list construction.
type HomeShown struct {
	AllAttention      bool
	AllRunning        bool
	Ended             int
	SkipAttentionCard bool
}

// HomeData is the domain projection for the Home screen.
type HomeData struct {
	ResolvedScope  Scope
	Headline       *HomeHeadlineInfo
	Empty          *HomeEmptyInfo
	Items          []*HomeRowData
	Meta           RowMeta
	ShowFilter     bool
	WantAll        bool
	WantPage       bool
	Grows          bool
	HasMoreEnded   bool
	EndedHeld      int32
	AttentionTotal int32
	RunningTotal   int32
	TimelineTotal  int32
	EndedTotal     int32
	MatchedSize    int32
	WaitingTotal   int32
}

// ProjectPageData is the domain projection for one Project detail page.
type ProjectPageData struct {
	Project      *ProjectSummary
	Active       []*SessionItem
	Ended        []*SessionItem
	Meta         RowMeta
	EndedTotal   int32
	HasMoreEnded bool
	ShortPath    string
	MachineNames []string
	MachineIDs   []string
	AnyOnline    bool
	IsHidden     bool
	IsPinned     bool
	WantPage     bool
	Tail         *HomeTailInfo
}

func defaultScopeFromListed(listed []*ProjectSummary) Scope {
	if len(listed) == 1 {
		p := listed[0]
		return Scope{Key: p.Key, Name: p.Name, ShortPath: p.ShortPath}
	}
	return Scope{AllProjects: true}
}

// ResolveScope resolves a remembered home scope against currently listed projects.
func ResolveScope(remembered *Scope, listed []*ProjectSummary) Scope {
	if remembered == nil {
		return defaultScopeFromListed(listed)
	}
	if remembered.AllProjects {
		if len(listed) == 1 {
			return defaultScopeFromListed(listed)
		}
		return Scope{AllProjects: true}
	}
	for _, p := range listed {
		if p != nil && p.Key == remembered.Key {
			return Scope{Key: p.Key, Name: p.Name, ShortPath: p.ShortPath}
		}
	}
	return defaultScopeFromListed(listed)
}

func hmacSHA256(key, msg []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(msg)
	return mac.Sum(nil)
}

// EncodeScope encodes scope into its persisted HMAC hex token.
func EncodeScope(scope Scope, key []byte) string {
	if scope.AllProjects {
		return "all"
	}
	const hexDigits = "0123456789abcdef"
	sum := hmacSHA256(key, []byte(scope.Key))
	var buf [64]byte
	for i, b := range sum {
		buf[i*2] = hexDigits[b>>4]
		buf[i*2+1] = hexDigits[b&0xf]
	}
	return "project:" + string(buf[:len(sum)*2])
}

// DecodeScope resolves a persisted scope token using hmacKey against listed projects, or nil if unknown.
func DecodeScope(stored string, key []byte, listed []*ProjectSummary) *Scope {
	if stored == "" {
		return nil
	}
	if stored == "all" {
		return &Scope{AllProjects: true}
	}
	if len(key) > 0 {
		for _, p := range listed {
			if p != nil {
				cand := Scope{Key: p.Key, Name: p.Name, ShortPath: p.ShortPath}
				if EncodeScope(cand, key) == stored {
					return &cand
				}
			}
		}
	}
	return nil
}

// OutsideScope reports whether s belongs to a different project than scope.
func OutsideScope(s *Session, scope Scope) bool {
	if scope.AllProjects || s == nil {
		return false
	}
	return ProjectKey(s) != scope.Key
}

// ComputeRowMeta computes row-level display flags (ShowMachine, ShowAgent, ShowProject) across sessions.
func ComputeRowMeta(w *World, sessions []*Session, includeProject bool) RowMeta {
	machines := make(map[string]bool)
	agents := make(map[v1.Agent]bool)
	projects := make(map[string]bool)
	for _, s := range sessions {
		if s == nil {
			continue
		}
		machines[s.MachineID] = true
		agents[SessionAgent(s)] = true
		if includeProject {
			projects[ProjectKey(s)] = true
		}
	}
	return RowMeta{
		ShowMachine: (w != nil && len(w.Machines) > 1) || len(machines) > 1,
		ShowAgent:   len(agents) > 1,
		ShowProject: includeProject && len(projects) > 1,
	}
}

// ActivityCounts returns global (waitingCount, liveCount) across all sessions.
func ActivityCounts(sessions []*Session) (waiting int32, live int32) {
	for _, s := range sessions {
		if s == nil {
			continue
		}
		if NeedsAttention(s) {
			waiting++
		} else if (s.Info == nil || !s.Info.TaskOwned) && !Exited(s) {
			live++
		}
	}
	return waiting, live
}

// CivilFromUnixMs computes local (year, month, day, weekdayMon0, hour, minute) using fixed offsetMinutes (no tzdata).
func CivilFromUnixMs(ms int64, offsetMinutes int32) (year, month, day, weekdayMon0, hour, minute int) {
	localSec := ms/1000 + int64(offsetMinutes)*60
	days := localSec / 86400
	rem := localSec % 86400
	if rem < 0 {
		days--
		rem += 86400
	}
	hour = int(rem / 3600)
	minute = int((rem % 3600) / 60)
	weekdayMon0 = int(((days+3)%7 + 7) % 7)

	z := days + 719468
	var era int64
	if z >= 0 {
		era = z / 146097
	} else {
		era = (z - 146096) / 146097
	}
	doe := z - era*146097
	yoe := (doe - doe/1460 + doe/36524 - doe/146096) / 365
	y := yoe + era*400
	doy := doe - (365*yoe + yoe/4 - yoe/100)
	mp := (5*doy + 2) / 153
	day = int(doy - (153*mp+2)/5 + 1)
	if mp < 10 {
		month = int(mp) + 3
	} else {
		month = int(mp) - 9
	}
	if month <= 2 {
		y++
	}
	year = int(y)
	return
}

func midnightMsFromDays(days int64, offsetMinutes int32) int64 {
	return (days*86400 - int64(offsetMinutes)*60) * 1000
}

// FormatHHMM formats ms in host local offset as "H:MM".
func FormatHHMM(ms int64, offsetMinutes int32) string {
	_, _, _, _, h, m := CivilFromUnixMs(ms, offsetMinutes)
	minStr := strconv.Itoa(m)
	if m < 10 {
		minStr = "0" + minStr
	}
	return strconv.Itoa(h) + ":" + minStr
}

// ResolveTitle returns (plainTitle, hasCustomTitle, resolvedFallbackTitle) for s.
func ResolveTitle(s *Session, cal Calendar) (plainTitle string, hasCustom bool, resolved string) {
	if s == nil || s.Info == nil {
		return "", false, ""
	}
	t := strings.TrimSpace(Plain(s.Info.Title))
	if t != "" {
		return t, true, t
	}
	proj := ProjectName(s.Info.Cwd)
	return "", false, proj
}

// TitleMatches reports whether session s's resolved title matches query (case-insensitive substring).
func TitleMatches(s *Session, query string, cal Calendar) bool {
	q := strings.TrimSpace(query)
	if q == "" {
		return true
	}
	_, _, resolved := ResolveTitle(s, cal)
	return strings.Contains(strings.ToLower(resolved), strings.ToLower(q))
}

// MonthFor resolves local (year, month) for ms using host-supplied calendar month boundaries,
// falling back to fixed UTCOffsetMinutes when no month boundaries were provided.
func MonthFor(ms int64, cal Calendar) (year, month int32, ok bool) {
	if len(cal.Months) > 0 {
		var best MonthStart
		found := false
		for _, m := range cal.Months {
			if m.StartMs <= ms && (!found || m.StartMs > best.StartMs) {
				best = m
				found = true
			}
		}
		if found {
			return best.Year, best.Month, true
		}
		return 0, 0, false
	}
	yr, mo, _, _, _, _ := CivilFromUnixMs(ms, cal.UTCOffsetMinutes)
	return int32(yr), int32(mo), true
}

// EndedGroupFor computes the local calendar group (Today, Yesterday, Week, Month) for endedMs.
func EndedGroupFor(endedMs int64, cal Calendar) EndedGroup {
	todayStart := cal.TodayStartMs
	yesterdayStart := cal.YesterdayStartMs
	weekStart := cal.WeekStartMs
	if todayStart == 0 || yesterdayStart == 0 || weekStart == 0 {
		localSec := cal.NowMs/1000 + int64(cal.UTCOffsetMinutes)*60
		days := localSec / 86400
		if localSec%86400 < 0 {
			days--
		}
		todayStart = midnightMsFromDays(days, cal.UTCOffsetMinutes)
		yesterdayStart = midnightMsFromDays(days-1, cal.UTCOffsetMinutes)
		weekStart = midnightMsFromDays(days-6, cal.UTCOffsetMinutes)
	}

	_, _, _, weekdayMon0, _, _ := CivilFromUnixMs(endedMs, cal.UTCOffsetMinutes)
	year, month, ok := MonthFor(endedMs, cal)
	if !ok {
		yr, mo, _, _, _, _ := CivilFromUnixMs(endedMs, cal.UTCOffsetMinutes)
		year = int32(yr)
		month = int32(mo)
	}

	g := EndedGroup{
		Year:         year,
		Month:        month,
		WeekdayIndex: int32(weekdayMon0),
		RepMs:        endedMs,
	}
	switch {
	case endedMs >= todayStart:
		g.Kind = GroupToday
		g.Key = "today"
	case endedMs >= yesterdayStart:
		g.Kind = GroupYesterday
		g.Key = "yesterday"
	case endedMs >= weekStart:
		g.Kind = GroupWeek
		g.Key = "week"
	default:
		g.Kind = GroupMonth
		g.Key = "month:" + strconv.Itoa(int(year)) + "-" + strconv.Itoa(int(month))
	}
	return g
}

func buildSessionItem(
	w *World,
	machinesByID map[string]*Machine,
	s *Session,
	scope Scope,
	cal Calendar,
) *SessionItem {
	m := machinesByID[s.MachineID]
	rawSt := StatusOf(s, m)
	unlinked := MachineUnlinked(m, w)
	shownSt := ShownStatus(s, m, unlinked)
	plainTitle, hasCustom, resolvedTitle := ResolveTitle(s, cal)
	projKey := ProjectKey(s)
	mode, rawMode := SessionMode(s)

	var plainPreview, statusDetail string
	if s.Info != nil {
		plainPreview = Plain(s.Info.Preview)
		statusDetail = s.Info.StatusDetail
	} else if s.Preview != "" {
		plainPreview = Plain(s.Preview)
	}

	var endedGroup *EndedGroup
	endedMsVal := EndedMs(s)
	if Exited(s) {
		g := EndedGroupFor(endedMsVal, cal)
		endedGroup = &g
	}
	var minsLeft int64
	if s.Pending.ApprovalDeadlineMs > 0 && cal.NowMs > 0 {
		minsLeft = MinutesLeft(s.Pending.ApprovalDeadlineMs, cal.NowMs)
	}

	return &SessionItem{
		Session:        s,
		ID:             s.ID,
		MachineID:      s.MachineID,
		RawStatus:      rawSt,
		ShownStatus:    shownSt,
		Tone:           ToneOf(shownSt),
		NeedsAttention: NeedsAttention(s),
		Exited:         Exited(s),
		Forked:         Forked(s),
		Agent:          SessionAgent(s),
		Title:          plainTitle,
		HasCustomTitle: hasCustom,
		ResolvedTitle:  resolvedTitle,
		ProjectKey:     projKey,
		ProjectName:    ProjectName(projKey),
		MachineName:    MachineName(m, s.MachineID),
		PlainPreview:   plainPreview,
		StatusDetail:   statusDetail,
		HasPopupTitle:  statusDetail != "",
		OutsideScope:   OutsideScope(s, scope),
		CreatedMs:      CreatedMs(s),
		EndedMs:        endedMsVal,
		AttentionMs:    AttentionMs(s),
		UpdatedMs:      s.UpdatedMs,
		EndedGroup:     endedGroup,
		ContextPercent: ContextPercent(s),
		Mode:           mode,
		RawMode:        rawMode,
		MinutesLeft:    minsLeft,
	}
}

// AccountEmptyInfo computes the account-level empty state when no sessions or projects exist.
func AccountEmptyInfo(w *World) *HomeEmptyInfo {
	if w == nil || len(w.Machines) == 0 {
		return &HomeEmptyInfo{Kind: HomeEmptyNoMachines}
	}
	if WorldUnlinked(w) {
		allUnlinked := true
		for _, m := range w.Machines {
			if !MachineUnlinked(m, w) {
				allUnlinked = false
				break
			}
		}
		if allUnlinked {
			return &HomeEmptyInfo{Kind: HomeEmptyUnlinked}
		}
	}
	var best *Machine
	for _, m := range w.Machines {
		if !MachineReachable(m) {
			continue
		}
		if best == nil || compareUTF16(m.ID, best.ID) < 0 {
			best = m
		}
	}
	if best == nil {
		return &HomeEmptyInfo{Kind: HomeEmptyNoneOnline}
	}
	return &HomeEmptyInfo{
		Kind:             HomeEmptyReady,
		ReadyMachineID:   best.ID,
		ReadyMachineName: MachineName(best, best.ID),
	}
}

type timelineResult struct {
	attention  []*Session
	running    []*Session
	ended      []*Session
	total      int32
	endedTotal int32
	meta       RowMeta
	size       int32
}

func computeTimeline(w *World, allProjects []*projectAgg, scope Scope, query string, cal Calendar) timelineResult {
	var hiddenSet map[string]bool
	if w.Prefs != nil && len(w.Prefs.Hidden) > 0 {
		_, hiddenSet = prefsSets(w.Prefs)
	}

	isAllScope := scope.AllProjects
	inScopePath := func(path string) bool {
		if !isAllScope {
			return path == scope.Key
		}
		return !hiddenSet[path]
	}

	type sortEntry struct {
		s  *Session
		ms int64
	}
	var waiting []*Session
	var runEntries []sortEntry
	var endedEntries []sortEntry

	var firstMach, firstProj string
	var firstAgent v1.Agent
	var multiMach = len(w.Machines) > 1
	var multiAgent, multiProj bool
	var scopedLiveCount int

	trackMeta := func(s *Session) {
		if !multiMach {
			if firstMach == "" {
				firstMach = s.MachineID
			} else if s.MachineID != firstMach {
				multiMach = true
			}
		}
		if !multiAgent {
			ag := SessionAgent(s)
			if firstAgent == v1.Agent_AGENT_UNSPECIFIED {
				firstAgent = ag
			} else if ag != firstAgent {
				multiAgent = true
			}
		}
		if isAllScope && !multiProj {
			pk := ProjectKey(s)
			if firstProj == "" {
				firstProj = pk
			} else if pk != firstProj {
				multiProj = true
			}
		}
	}

	for _, s := range w.Sessions {
		if s == nil {
			continue
		}
		if NeedsAttention(s) {
			waiting = append(waiting, s)
			if isAllScope || ProjectKey(s) == scope.Key {
				scopedLiveCount++
				trackMeta(s)
			}
			continue
		}
		if s.Info != nil && s.Info.TaskOwned {
			continue
		}
		if !Exited(s) {
			if isAllScope || ProjectKey(s) == scope.Key {
				scopedLiveCount++
				trackMeta(s)
				runEntries = append(runEntries, sortEntry{s: s, ms: CreatedMs(s)})
			}
		} else if inScopePath(ProjectKey(s)) {
			trackMeta(s)
			endedEntries = append(endedEntries, sortEntry{s: s, ms: EndedMs(s)})
		}
	}

	if len(waiting) > 1 {
		slices.SortFunc(waiting, CompareAttention)
	}
	if len(runEntries) > 1 {
		slices.SortFunc(runEntries, func(a, b sortEntry) int {
			if c := cmp.Compare(b.ms, a.ms); c != 0 {
				return c
			}
			return compareUTF16(a.s.ID, b.s.ID)
		})
	}
	if len(endedEntries) > 1 {
		slices.SortFunc(endedEntries, func(a, b sortEntry) int {
			if c := cmp.Compare(b.ms, a.ms); c != 0 {
				return c
			}
			return compareUTF16(a.s.ID, b.s.ID)
		})
	}

	running := make([]*Session, len(runEntries))
	for i, e := range runEntries {
		running[i] = e.s
	}
	ended := make([]*Session, len(endedEntries))
	for i, e := range endedEntries {
		ended[i] = e.s
	}

	var summed int32
	for _, p := range allProjects {
		if inScopePath(p.key) {
			summed += p.ended
		}
	}
	endedTotal := max(summed, int32(len(ended)))
	total := int32(scopedLiveCount) + endedTotal

	var fWaiting, fRunning, fEnded []*Session
	if strings.TrimSpace(query) == "" {
		fWaiting = waiting
		fRunning = running
		fEnded = ended
	} else {
		for _, s := range waiting {
			if TitleMatches(s, query, cal) {
				fWaiting = append(fWaiting, s)
			}
		}
		for _, s := range running {
			if TitleMatches(s, query, cal) {
				fRunning = append(fRunning, s)
			}
		}
		for _, s := range ended {
			if TitleMatches(s, query, cal) {
				fEnded = append(fEnded, s)
			}
		}
	}

	return timelineResult{
		attention:  fWaiting,
		running:    fRunning,
		ended:      fEnded,
		total:      total,
		endedTotal: endedTotal,
		meta: RowMeta{
			ShowMachine: multiMach,
			ShowAgent:   multiAgent,
			ShowProject: multiProj,
		},
		size: int32(len(fWaiting) + len(fRunning) + len(fEnded)),
	}
}

func computeHomeHeadline(
	w *World,
	machinesByID map[string]*Machine,
	scope Scope,
	tl timelineResult,
	listErr string,
) *HomeHeadlineInfo {
	if w == nil {
		return nil
	}
	kept := w.LiveLoaded || len(w.Sessions) > 0
	switch w.Connection {
	case ConnNoNetwork:
		return &HomeHeadlineInfo{Kind: HeadlineNoNetwork, Kept: kept, Unlinked: true}
	case ConnConnecting:
		return &HomeHeadlineInfo{Kind: HeadlineConnecting, Kept: kept, Unlinked: true}
	case ConnReconnecting:
		kind := HeadlineConnecting
		if w.LiveLoaded {
			kind = HeadlineReconnecting
		}
		return &HomeHeadlineInfo{Kind: kind, Kept: kept, Unlinked: true}
	case ConnOffline:
		return &HomeHeadlineInfo{
			Kind:     HeadlineNoServer,
			RawError: w.SessionListError,
			Kept:     kept,
			SubError: !kept,
			Details:  true,
			Unlinked: true,
		}
	}
	if !w.LiveLoaded && len(w.Sessions) == 0 {
		return nil
	}

	var runningCount, idleCount int32
	for _, s := range tl.running {
		switch ToneOf(StatusOf(s, machinesByID[s.MachineID])) {
		case ToneRun:
			runningCount++
		case ToneIdle:
			idleCount++
		}
	}

	if len(tl.attention) > 0 {
		return &HomeHeadlineInfo{
			Kind:         HeadlineWaiting,
			WaitingCount: int32(len(tl.attention)),
			RunningCount: runningCount,
			IdleCount:    idleCount,
		}
	}
	if runningCount > 0 {
		return &HomeHeadlineInfo{
			Kind:         HeadlineRunning,
			RunningCount: runningCount,
			IdleCount:    idleCount,
		}
	}
	if !scope.AllProjects && tl.total == 0 {
		return &HomeHeadlineInfo{
			Kind:             HeadlineEmptyProject,
			ProjectShortPath: ShortPath(scope.Key),
			SubPath:          true,
		}
	}
	if len(w.Sessions) == 0 {
		if !w.SessionsListed && listErr != "" {
			return &HomeHeadlineInfo{Kind: HeadlineUnreadable, RawError: listErr}
		}
		return &HomeHeadlineInfo{Kind: HeadlineNoSessions}
	}
	return &HomeHeadlineInfo{
		Kind:      HeadlineNothingWaits,
		IdleCount: idleCount,
	}
}

// EarlierTailInfo computes the pagination footer state when more sessions may live on the server.
func EarlierTailInfo(w *World) *HomeTailInfo {
	sessErr := ""
	if w != nil {
		sessErr = w.SessionListError
	}
	online := w != nil && w.Connection == ConnConnected
	retry := online && sessErr != ""
	if !online {
		return &HomeTailInfo{Kind: TailEarlierOffline, Retry: retry}
	}
	if sessErr != "" && !w.SessionsListed {
		return &HomeTailInfo{Kind: TailEarlierListFailed, RawError: sessErr, Retry: retry}
	}
	if sessErr != "" {
		return &HomeTailInfo{Kind: TailEarlierFailed, RawError: sessErr, Retry: retry}
	}
	return &HomeTailInfo{Kind: TailLoadingEarlier, Retry: retry}
}

// BuildProjectsAndHome computes both ProjectsViewResult and HomeData in a single pass over w.
func BuildProjectsAndHome(
	w *World,
	remembered *Scope,
	rememberedToken string,
	hmacKey []byte,
	currentScope *Scope,
	projectSearch string,
	query string,
	cal Calendar,
	shown HomeShown,
) (*ProjectsViewResult, *HomeData) {
	if w == nil {
		return &ProjectsViewResult{
				DefaultScope:  Scope{AllProjects: true},
				ResolvedScope: Scope{AllProjects: true},
			}, &HomeData{
				ResolvedScope: Scope{AllProjects: true},
				Empty:         &HomeEmptyInfo{Kind: HomeEmptyLoading},
			}
	}
	allProjects, machinesByID := aggregateProjects(w)
	projRes := buildProjectsFromAgg(w, allProjects, projectSearch, remembered, rememberedToken, hmacKey, currentScope)
	homeData := buildHomeWithAgg(w, allProjects, machinesByID, &projRes.ResolvedScope, false, query, cal, shown, projRes.WaitingTotal)
	return projRes, homeData
}

// BuildHome builds the Home timeline rows, empty state, headline, and pagination signals.
func BuildHome(w *World, scope *Scope, resolveScope bool, query string, cal Calendar, shown HomeShown) *HomeData {
	data := &HomeData{ResolvedScope: Scope{AllProjects: true}}
	if w == nil {
		data.Empty = &HomeEmptyInfo{Kind: HomeEmptyLoading}
		return data
	}
	allProjects, machinesByID := aggregateProjects(w)
	return buildHomeWithAgg(w, allProjects, machinesByID, scope, resolveScope, query, cal, shown, -1)
}

func buildHomeWithAgg(
	w *World,
	allProjects []*projectAgg,
	machinesByID map[string]*Machine,
	scope *Scope,
	resolveScope bool,
	query string,
	cal Calendar,
	shown HomeShown,
	knownWaitingTotal int32,
) *HomeData {
	data := &HomeData{ResolvedScope: Scope{AllProjects: true}}
	var effScope Scope
	if scope == nil || resolveScope {
		pinnedOrder, hiddenSet := prefsSets(w.Prefs)
		byKey := make(map[string]*projectAgg, len(allProjects))
		for _, p := range allProjects {
			byKey[p.key] = p
		}
		var listed []*ProjectSummary
		pinnedSeen := make(map[string]bool)
		for _, path := range pinnedOrder {
			if !hiddenSet[path] && !pinnedSeen[path] {
				if p, ok := byKey[path]; ok {
					pinnedSeen[path] = true
					listed = append(listed, toProjectSummary(p, true, false))
				}
			}
		}
		for _, p := range allProjects {
			if !hiddenSet[p.key] && !pinnedSeen[p.key] {
				listed = append(listed, toProjectSummary(p, false, false))
			}
		}
		effScope = ResolveScope(scope, listed)
	} else {
		effScope = *scope
	}
	data.ResolvedScope = effScope

	shownEnded := shown.Ended
	if shownEnded == 0 {
		shownEnded = EndedPage
	} else if shownEnded < 0 {
		shownEnded = int(^uint(0) >> 1)
	}

	trimmedQuery := strings.TrimSpace(query)
	filtering := trimmedQuery != ""

	tl := computeTimeline(w, allProjects, effScope, query, cal)
	headlineTL := tl
	if filtering {
		headlineTL = computeTimeline(w, allProjects, effScope, "", cal)
	}
	data.Headline = computeHomeHeadline(w, machinesByID, effScope, headlineTL, w.SessionListError)

	moreSess := MoreSessions(w)
	wantAll := filtering && moreSess
	wantPage := !filtering && NeedsEndedPage(w, tl.ended, shownEnded, int(tl.endedTotal))

	anyProject := len(allProjects) > 0
	if !w.LiveLoaded && len(w.Sessions) == 0 {
		data.Empty = &HomeEmptyInfo{Kind: HomeEmptyLoading}
	} else if len(w.Sessions) == 0 && !anyProject {
		data.Empty = AccountEmptyInfo(w)
	} else if !effScope.AllProjects && tl.total == 0 && !filtering {
		data.Empty = &HomeEmptyInfo{Kind: HomeEmptyScope}
	}

	var items []*HomeRowData
	var card *Session
	if !shown.SkipAttentionCard && len(tl.attention) > 0 {
		first := tl.attention[0]
		if StatusOf(first, machinesByID[first.MachineID]) == StatusApproval {
			card = first
		}
	}
	if card != nil {
		items = append(items, &HomeRowData{
			RowID: "s:" + card.ID,
			Kind:  HomeRowAttentionCard,
			Item:  buildSessionItem(w, machinesByID, card, effScope, cal),
		})
	}
	waiting := tl.attention
	if card != nil {
		waiting = tl.attention[1:]
	}
	attRows := waiting
	if !shown.AllAttention && !filtering && len(attRows) > AttentionMax {
		attRows = waiting[:AttentionMax]
	}
	hiddenAtt := len(waiting) - len(attRows)
	for i, s := range attRows {
		items = append(items, &HomeRowData{
			RowID: "s:" + s.ID,
			Kind:  HomeRowAttention,
			Item:  buildSessionItem(w, machinesByID, s, effScope, cal),
			First: i == 0,
			Last:  i == len(attRows)-1 && hiddenAtt == 0,
		})
	}
	if hiddenAtt > 0 {
		items = append(items, &HomeRowData{
			RowID:     "more:attention",
			Kind:      HomeRowMoreAttention,
			MoreCount: int32(hiddenAtt),
		})
	}

	if len(tl.running) > 0 {
		items = append(items, &HomeRowData{
			RowID:    "h:running",
			Kind:     HomeRowSectionLabel,
			LabelKey: "running",
		})
		runRows := tl.running
		if !shown.AllRunning && !filtering && len(runRows) > RunningMax {
			runRows = tl.running[:RunningMax]
		}
		for i, s := range runRows {
			items = append(items, &HomeRowData{
				RowID: "s:" + s.ID,
				Kind:  HomeRowRunning,
				Item:  buildSessionItem(w, machinesByID, s, effScope, cal),
				Last:  i == len(runRows)-1,
			})
		}
		if len(runRows) < len(tl.running) {
			items = append(items, &HomeRowData{
				RowID:     "more:running",
				Kind:      HomeRowMoreRunning,
				MoreCount: int32(len(tl.running) - len(runRows)),
			})
		}
	}

	endedSlice := tl.ended
	if len(endedSlice) > shownEnded {
		endedSlice = tl.ended[:shownEnded]
	}
	var groupKeys []string
	groupInfos := make(map[string]EndedGroup)
	groupSessions := make(map[string][]*Session)
	for _, s := range endedSlice {
		g := EndedGroupFor(EndedMs(s), cal)
		if _, exists := groupInfos[g.Key]; !exists {
			groupKeys = append(groupKeys, g.Key)
			groupInfos[g.Key] = g
		}
		groupSessions[g.Key] = append(groupSessions[g.Key], s)
	}
	for _, gk := range groupKeys {
		g := groupInfos[gk]
		items = append(items, &HomeRowData{
			RowID:      "h:" + gk,
			Kind:       HomeRowSectionLabel,
			LabelKey:   gk,
			EndedGroup: &g,
		})
		sessList := groupSessions[gk]
		for i, s := range sessList {
			items = append(items, &HomeRowData{
				RowID: "s:" + s.ID,
				Kind:  HomeRowEnded,
				Item:  buildSessionItem(w, machinesByID, s, effScope, cal),
				First: i == 0,
				Last:  i == len(sessList)-1,
			})
		}
	}

	heldMore := len(endedSlice) < len(tl.ended)
	more := heldMore || (!filtering && moreSess && int32(len(tl.ended)) < tl.endedTotal)

	var tailInfo *HomeTailInfo
	if filtering {
		if !wantAll {
			if tl.size == 0 {
				tailInfo = &HomeTailInfo{Kind: TailFilterNoMatches, Query: trimmedQuery}
			} else {
				tailInfo = &HomeTailInfo{Kind: TailFilterMatches, Count: tl.size, Query: trimmedQuery}
			}
		}
	} else if tl.endedTotal > EndedPage {
		if more {
			tailInfo = &HomeTailInfo{Kind: TailTotalLoadEarlier, Count: tl.endedTotal}
		} else {
			tailInfo = &HomeTailInfo{Kind: TailTotal, Count: tl.endedTotal}
		}
	}

	listFailed := !w.SessionsListed && w.SessionListError != ""
	if (len(items) > 0 || listFailed || filtering) && (tailInfo != nil || more || wantAll || wantPage || listFailed) {
		if listFailed || ((wantAll || wantPage) && (tailInfo == nil || w.SessionListError != "")) {
			items = append(items, &HomeRowData{
				RowID: "tail",
				Kind:  HomeRowTail,
				Tail:  EarlierTailInfo(w),
			})
		} else {
			items = append(items, &HomeRowData{
				RowID: "tail",
				Kind:  HomeRowTail,
				Tail:  tailInfo,
			})
		}
	}

	waitingTotal := knownWaitingTotal
	if waitingTotal < 0 {
		waitingTotal = 0
		for _, s := range w.Sessions {
			if NeedsAttention(s) {
				waitingTotal++
			}
		}
	}

	data.Items = items
	data.ShowFilter = tl.total > FilterOver || query != ""
	data.WantAll = wantAll
	data.WantPage = wantPage
	data.Grows = more && shownEnded <= len(tl.ended)
	data.HasMoreEnded = more
	data.Meta = tl.meta
	data.EndedHeld = int32(len(tl.ended))
	data.AttentionTotal = int32(len(tl.attention))
	data.RunningTotal = int32(len(tl.running))
	data.TimelineTotal = tl.total
	data.EndedTotal = tl.endedTotal
	data.MatchedSize = tl.size
	data.WaitingTotal = waitingTotal
	return data
}

// BuildProjectPage computes active and windowed ended session rows and project metadata for key.
func BuildProjectPage(w *World, key string, endedLimit int, cal Calendar) *ProjectPageData {
	if w == nil {
		return &ProjectPageData{ShortPath: ShortPath(key)}
	}
	res := &ProjectPageData{
		ShortPath: ShortPath(key),
		IsHidden:  IsHidden(w.Prefs, key),
		IsPinned:  IsPinned(w.Prefs, key),
	}
	allAgg, machinesByID := aggregateProjects(w)

	var sessions []*Session
	for _, s := range w.Sessions {
		if s == nil {
			continue
		}
		if (s.Info == nil || !s.Info.TaskOwned) && ProjectKey(s) == key {
			sessions = append(sessions, s)
		}
	}
	slices.SortFunc(sessions, CompareSession)

	var pr *projectAgg
	for _, p := range allAgg {
		if p.key == key {
			pr = p
			break
		}
	}

	var active, ended []*Session
	for _, s := range sessions {
		if Exited(s) {
			ended = append(ended, s)
		} else {
			active = append(active, s)
		}
	}

	endedTotal := int32(len(ended))
	if pr != nil && pr.ended > endedTotal {
		endedTotal = pr.ended
	}
	limit := endedLimit
	if limit == 0 {
		limit = EndedPage
	} else if limit < 0 || limit > len(ended) {
		limit = len(ended)
	}
	if limit > len(ended) {
		limit = len(ended)
	}
	endedSlice := ended[:limit]

	scope := Scope{Key: key}
	res.Active = make([]*SessionItem, 0, len(active))
	for _, s := range active {
		res.Active = append(res.Active, buildSessionItem(w, machinesByID, s, scope, cal))
	}
	res.Ended = make([]*SessionItem, 0, len(endedSlice))
	for _, s := range endedSlice {
		res.Ended = append(res.Ended, buildSessionItem(w, machinesByID, s, scope, cal))
	}
	res.Meta = ComputeRowMeta(w, sessions, false)
	res.EndedTotal = endedTotal
	if pr != nil {
		res.MachineNames = slices.Clone(pr.machineNames)
		res.MachineIDs = slices.Clone(pr.machineIDs)
		res.AnyOnline = pr.anyOnline
		res.Project = toProjectSummary(pr, res.IsPinned, res.IsHidden)
	}
	moreLocal := len(endedSlice) < len(ended)
	moreServer := MoreSessions(w) && int32(len(ended)) < endedTotal
	res.HasMoreEnded = moreLocal || moreServer
	res.WantPage = NeedsEndedPage(w, ended, max(limit, 3), int(endedTotal))
	if res.WantPage || w.SessionListError != "" || (!w.SessionsListed && endedTotal > 0) {
		res.Tail = EarlierTailInfo(w)
	} else if moreServer {
		res.Tail = EarlierTailInfo(w)
	}
	return res
}
