package quota

import (
	"math"
	"sort"
	"strconv"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

// BeijingOffsetMinutes is UTC+8 (no DST). All displayed quota times use Beijing time.
const BeijingOffsetMinutes int32 = 8 * 60

const (
	freshMinutesMax    int64 = 30
	warningUsedPercent int32 = 80
	msPerMinute        int64 = 60_000
	msPerHour          int64 = 3_600_000
	msPerDay           int64 = 86_400_000
	beijingOffsetMs    int64 = int64(BeijingOffsetMinutes) * msPerMinute
)

// Descriptor describes how a named quota window is labeled and grouped.
type Descriptor struct {
	Label      *corev1.Text
	GroupLabel *corev1.Text
	PoolName   *corev1.Text
}

var antigravityOrder = []string{
	"gemini-5h",
	"gemini-weekly",
	"gemini-argon-wtu-weekly",
	"3p-5h",
	"3p-weekly",
}

// KnownWindows returns the canonical window order for an agent.
func KnownWindows(agent v1.Agent) []string {
	if agent == v1.Agent_AGENT_ANTIGRAVITY {
		return antigravityOrder
	}
	return lists.AgentInfo(agent).QuotaWindows
}

// WindowDescriptor resolves user-facing label, optional group header, and pool label for a window.
func WindowDescriptor(agent v1.Agent, name string) Descriptor {
	if agent == v1.Agent_AGENT_ANTIGRAVITY {
		switch name {
		case "gemini-5h":
			return Descriptor{
				Label:      text.Msg(text.KeyQuotaNamedHours, text.Str("name", "Gemini"), text.Int("count", 5)),
				GroupLabel: text.Plain("Gemini"),
				PoolName:   text.Plain("Gemini"),
			}
		case "gemini-weekly":
			return Descriptor{
				Label:      text.Msg(text.KeyQuotaNamedDays, text.Str("name", "Gemini"), text.Int("count", 7)),
				GroupLabel: text.Plain("Gemini"),
				PoolName:   text.Plain("Gemini"),
			}
		case "gemini-argon-wtu-weekly":
			return Descriptor{
				Label:      text.Msg(text.KeyQuotaNamedDays, text.Str("name", "Gemini 4 Argon"), text.Int("count", 7)),
				GroupLabel: text.Plain("Gemini"),
				PoolName:   text.Plain("Gemini 4 Argon"),
			}
		case "3p-5h":
			pool := text.Msg(text.KeyQuotaThirdPartyModels)
			return Descriptor{
				Label:      text.Msg(text.KeyQuotaNamedHours, text.ArgText("name", pool), text.Int("count", 5)),
				GroupLabel: text.Msg(text.KeyQuotaThirdPartyModelsClaudeGptOss),
				PoolName:   pool,
			}
		case "3p-weekly":
			pool := text.Msg(text.KeyQuotaThirdPartyModels)
			return Descriptor{
				Label:      text.Msg(text.KeyQuotaNamedDays, text.ArgText("name", pool), text.Int("count", 7)),
				GroupLabel: text.Msg(text.KeyQuotaThirdPartyModelsClaudeGptOss),
				PoolName:   pool,
			}
		default:
			return Descriptor{Label: text.Plain(name)}
		}
	}
	switch name {
	case "primary", "five_hour":
		return Descriptor{Label: text.Msg(text.KeyQuotaHr, text.Int("count", 5))}
	case "secondary", "seven_day":
		return Descriptor{Label: text.Msg(text.KeyQuotaDays, text.Int("count", 7))}
	default:
		return Descriptor{Label: text.Plain(name)}
	}
}

func beijingDayIndex(ms int64) int64 {
	shifted := ms + beijingOffsetMs
	if shifted >= 0 {
		return shifted / msPerDay
	}
	return (shifted - msPerDay + 1) / msPerDay
}

// Clock returns HH:MM in Beijing time (UTC+8).
func Clock(ms int64) string {
	_, _, _, _, h, m := lists.CivilFromUnixMs(ms, BeijingOffsetMinutes)
	return pad2(h) + ":" + pad2(m)
}

// Freshness formats how old a quota observation is when >= 30 minutes or missing.
// Returns nil when the observation is under 30 minutes old.
func Freshness(observedMs, nowMs int64) *corev1.Text {
	if observedMs <= 0 {
		return text.Msg(text.KeyQuotaUnknownTime)
	}
	age := nowMs - observedMs
	if age < 0 {
		age = 0
	}
	mins := age / msPerMinute
	if mins < freshMinutesMax {
		return nil
	}
	if mins < 60 {
		return text.Msg(text.KeyQuotaOldMinutes, text.Int("count", mins))
	}
	return text.Msg(text.KeyQuotaOldHours, text.Int("count", age/msPerHour))
}

// ResetRelative formats the relative countdown until resetsAtMs, or nil if unset/past.
func ResetRelative(resetsAtMs, nowMs int64) *corev1.Text {
	if resetsAtMs <= 0 || resetsAtMs <= nowMs {
		return nil
	}
	mins := (resetsAtMs - nowMs) / msPerMinute
	if mins < 1 {
		return text.Msg(text.KeyQuotaResettingSoon)
	}
	if mins < 60 {
		return text.Msg(text.KeyQuotaResetsInMin, text.Int("count", mins))
	}
	if mins < 24*60 {
		hours := mins / 60
		rem := mins % 60
		return text.Msg(text.KeyQuotaResetsInHrMin, text.Int("count", hours), text.Str("detail", pad2(rem)))
	}
	days := mins / (24 * 60)
	hours := (mins % (24 * 60)) / 60
	return text.Msg(text.KeyQuotaResetsInDaysHr, text.Int("count", days), text.Int("detail", hours))
}

// ResetClock formats reset wall-clock time in Beijing time (today -> HH:MM, tomorrow -> 明天 HH:MM, other -> M/D HH:MM).
func ResetClock(resetsAtMs, nowMs int64) *corev1.Text {
	if resetsAtMs <= 0 || resetsAtMs <= nowMs {
		return nil
	}
	clock := Clock(resetsAtMs)
	today := beijingDayIndex(nowMs)
	target := beijingDayIndex(resetsAtMs)
	if target == today {
		return text.Plain(clock)
	}
	if target == today+1 {
		return text.Msg(text.KeyTimeDayClock, text.ArgText("day", text.Msg(text.KeyTimeTomorrow)), text.Str("clock", clock))
	}
	_, m, d, _, _, _ := lists.CivilFromUnixMs(resetsAtMs, BeijingOffsetMinutes)
	return text.Msg(
		text.KeyTimeDayClock,
		text.ArgText("day", text.Msg(text.KeyTimeMonthDay, text.Int("month", int64(m)), text.Int("day", int64(d)))),
		text.Str("clock", clock),
	)
}

// ReceivedAt formats "received at HH:MM" or with month/day when on a different Beijing calendar day.
func ReceivedAt(observedMs, nowMs int64, ok bool) *corev1.Text {
	if observedMs <= 0 {
		return nil
	}
	clock := Clock(observedMs)
	sameDay := beijingDayIndex(observedMs) == beijingDayIndex(nowMs)
	if ok {
		if sameDay {
			return text.Msg(text.KeyQuotaReceivedSuccess, text.Str("time", clock))
		}
		_, m, d, _, _, _ := lists.CivilFromUnixMs(observedMs, BeijingOffsetMinutes)
		return text.Msg(text.KeyQuotaReceivedSuccessDate, text.Int("month", int64(m)), text.Int("day", int64(d)), text.Str("time", clock))
	}
	if sameDay {
		return text.Msg(text.KeyQuotaReceived, text.Str("time", clock))
	}
	_, m, d, _, _, _ := lists.CivilFromUnixMs(observedMs, BeijingOffsetMinutes)
	return text.Msg(text.KeyQuotaReceivedDate, text.Int("month", int64(m)), text.Int("day", int64(d)), text.Str("time", clock))
}

func usageHintForAgent(agent v1.Agent) *corev1.Text {
	if agent == v1.Agent_AGENT_CLAUDE {
		return text.Msg(text.KeySettingsViewUsageInTheLocalCli)
	}
	return nil
}

// BuildWindow builds a QuotaRowView for a single raw QuotaWindow.
func BuildWindow(agent v1.Agent, w *v1.QuotaWindow, nowMs int64, stale bool) *corev1.QuotaRowView {
	if w == nil {
		return nil
	}
	desc := WindowDescriptor(agent, w.Name)
	row := &corev1.QuotaRowView{
		WindowName: w.Name,
		Label:      desc.Label,
		PoolName:   desc.PoolName,
	}
	if w.UsedPercent == nil {
		row.Tone = corev1.QuotaTone_QUOTA_TONE_STALE
		return row
	}
	used := int32(math.Round(*w.UsedPercent))
	if used < 0 {
		used = 0
	}
	if used > 100 {
		used = 100
	}
	rem := 100 - used
	exhausted := rem <= 0
	pastReset := w.ResetsAtMs > 0 && w.ResetsAtMs <= nowMs

	row.HasPercent = true
	row.UsedPercent = used
	row.RemainingPercent = rem
	row.Exhausted = exhausted
	row.PastReset = pastReset
	row.Stale = stale || pastReset

	switch {
	case stale || pastReset:
		row.Tone = corev1.QuotaTone_QUOTA_TONE_STALE
	case exhausted:
		row.Tone = corev1.QuotaTone_QUOTA_TONE_FULL
	case used >= warningUsedPercent:
		row.Tone = corev1.QuotaTone_QUOTA_TONE_WARN
	default:
		row.Tone = corev1.QuotaTone_QUOTA_TONE_NORMAL
	}

	if pastReset {
		row.RemainingText = text.Msg(text.KeyQuotaAwaitingUpdate)
		row.ResetText = text.Msg(text.KeyQuotaAwaitingUpdate)
		if !stale {
			row.Footnote = text.Msg(
				text.KeyQuotaPreviouslyResetTimePassedButQuotaRecoveryIsUnconfirmed,
				text.ArgText("value", text.Msg(text.KeyQuotaLastReading)),
			)
		}
	} else if exhausted {
		row.RemainingText = text.Msg(text.KeyQuotaExhaustedQuota)
	} else {
		row.RemainingText = text.Msg(text.KeyQuotaLeft, text.Str("value", strconv.FormatInt(int64(rem), 10)))
	}
	if !pastReset && w.ResetsAtMs > nowMs {
		row.ResetText = ResetRelative(w.ResetsAtMs, nowMs)
		row.ResetClockText = ResetClock(w.ResetsAtMs, nowMs)
	}
	if stale {
		valText := row.RemainingText
		if valText != nil {
			row.RemainingText = text.Msg(text.KeyQuotaPreviously, text.ArgText("value", valText))
		}
	}
	return row
}

// OrderedWindows builds and orders rows, omitting missing/nil percentages cleanly.
func OrderedWindows(agent v1.Agent, windows []*v1.QuotaWindow, nowMs int64, stale bool) []*corev1.QuotaRowView {
	if len(windows) == 0 {
		return nil
	}
	byName := make(map[string]*v1.QuotaWindow, len(windows))
	for _, w := range windows {
		if w != nil && w.Name != "" && w.UsedPercent != nil {
			byName[w.Name] = w
		}
	}
	if len(byName) == 0 {
		return nil
	}

	known := KnownWindows(agent)
	seen := make(map[string]bool, len(byName))
	var out []*corev1.QuotaRowView
	for _, name := range known {
		if w, ok := byName[name]; ok {
			seen[name] = true
			if row := BuildWindow(agent, w, nowMs, stale); row != nil {
				out = append(out, row)
			}
		}
	}
	var extras []string
	for name := range byName {
		if !seen[name] {
			extras = append(extras, name)
		}
	}
	sort.Strings(extras)
	for _, name := range extras {
		if row := BuildWindow(agent, byName[name], nowMs, stale); row != nil {
			out = append(out, row)
		}
	}
	return out
}

func groupWindowRows(agent v1.Agent, rows []*corev1.QuotaRowView) []*corev1.QuotaWindowGroupView {
	if len(rows) == 0 {
		return nil
	}
	if agent != v1.Agent_AGENT_ANTIGRAVITY {
		return []*corev1.QuotaWindowGroupView{{Rows: rows}}
	}
	var groups []*corev1.QuotaWindowGroupView
	index := make(map[string]int)
	for _, row := range rows {
		desc := WindowDescriptor(agent, row.WindowName)
		key := ""
		if desc.GroupLabel != nil {
			key = textKeyOrLiteral(desc.GroupLabel)
		}
		if pos, ok := index[key]; ok {
			groups[pos].Rows = append(groups[pos].Rows, row)
			continue
		}
		index[key] = len(groups)
		groups = append(groups, &corev1.QuotaWindowGroupView{
			GroupLabel: desc.GroupLabel,
			Rows:       []*corev1.QuotaRowView{row},
		})
	}
	return groups
}

func textKeyOrLiteral(t *corev1.Text) string {
	if t == nil {
		return ""
	}
	if t.Key != "" {
		return "k:" + t.Key
	}
	if t.Literal != "" {
		return "p:" + t.Literal
	}
	return ""
}

// TightestRow returns the most constrained window across rows (exhausted/highest used first).
func TightestRow(rows []*corev1.QuotaRowView) *corev1.QuotaRowView {
	var best *corev1.QuotaRowView
	for _, r := range rows {
		if r == nil || !r.HasPercent {
			continue
		}
		if best == nil {
			best = r
			continue
		}
		// Prefer active warnings/full over stale; prefer higher used percent.
		rScore := tightestScore(r)
		bScore := tightestScore(best)
		if rScore > bScore {
			best = r
		}
	}
	return best
}

func tightestScore(r *corev1.QuotaRowView) int32 {
	if r == nil || !r.HasPercent {
		return -1000
	}
	score := r.UsedPercent
	if r.Stale {
		// Still break ties if all rows are stale, but active rows outrank stale rows.
		return score - 200
	}
	if r.Exhausted {
		score += 500
	} else if r.UsedPercent >= warningUsedPercent {
		score += 200
	}
	return score
}

func buildAccountGroup(agent v1.Agent, q *v1.AgentQuota, reachable bool, nowMs int64) (*corev1.QuotaAccountGroupView, []*corev1.QuotaRowView) {
	freshness := Freshness(q.ObservedMs, nowMs)
	failed := q.State == v1.QuotaState_QUOTA_STATE_FAILED
	waiting := q.State == v1.QuotaState_QUOTA_STATE_WAITING
	unsupported := q.State == v1.QuotaState_QUOTA_STATE_UNSUPPORTED
	lastRecord := !reachable || failed || freshness != nil

	var sectionState corev1.QuotaSectionState
	switch {
	case unsupported:
		sectionState = corev1.QuotaSectionState_QUOTA_SECTION_STATE_UNSUPPORTED
	case failed:
		sectionState = corev1.QuotaSectionState_QUOTA_SECTION_STATE_FAILED
	case waiting:
		sectionState = corev1.QuotaSectionState_QUOTA_SECTION_STATE_WAITING
	case q.State == v1.QuotaState_QUOTA_STATE_OK:
		sectionState = corev1.QuotaSectionState_QUOTA_SECTION_STATE_OK
	default:
		sectionState = corev1.QuotaSectionState_QUOTA_SECTION_STATE_MISSING
	}

	rows := OrderedWindows(agent, q.Windows, nowMs, lastRecord)
	grp := &corev1.QuotaAccountGroupView{
		Account:             q.Account,
		Plan:                lists.PlanName(agent, q.Plan),
		State:               sectionState,
		LastRecord:          lastRecord,
		FreshnessText:       freshness,
		IsSubscriptionLogin: q.Login == v1.QuotaLogin_QUOTA_LOGIN_SUBSCRIPTION,
		Groups:              groupWindowRows(agent, rows),
	}

	if q.Detail != "" && !waiting {
		grp.ErrorText = text.Plain(q.Detail)
	}
	switch {
	case unsupported:
		grp.StatusText = text.Msg(text.KeyQuotaQuotaLookupIsNotSupportedYet)
		grp.UsageHint = usageHintForAgent(agent)
	case failed:
		if q.ObservedMs > 0 && len(rows) > 0 {
			grp.StatusText = text.Msg(text.KeyQuotaLastReading)
		} else if q.Detail != "" {
			grp.StatusText = text.Msg(text.KeyQuotaFailure, text.Str("name", agentDisplayName(agent)), text.Str("detail", q.Detail))
		} else {
			grp.StatusText = text.Msg(text.KeyQuotaFailureNoReason, text.Str("name", agentDisplayName(agent)))
		}
		grp.UsageHint = usageHintForAgent(agent)
	case waiting:
		if q.Detail != "" {
			grp.StatusText = text.Plain(q.Detail)
		} else if agent == v1.Agent_AGENT_ANTIGRAVITY {
			grp.StatusText = text.Msg(text.KeyQuotaAgyWaiting)
		} else {
			grp.StatusText = text.Msg(text.KeyQuotaWaiting, text.Str("name", agentDisplayName(agent)))
		}
	case !reachable:
		if recv := ReceivedAt(q.ObservedMs, nowMs, false); recv != nil {
			grp.StatusText = recv
		} else {
			grp.StatusText = text.Msg(text.KeyQuotaLastReading)
		}
	case len(rows) == 0:
		grp.StatusText = text.Msg(text.KeyQuotaNoDataReceivedForThisQuota)
		grp.UsageHint = usageHintForAgent(agent)
	default:
		grp.StatusText = ReceivedAt(q.ObservedMs, nowMs, freshness == nil)
	}
	return grp, rows
}

// MachineAgentOrder returns the ordered list of enabled CLIs installed/reported on a machine for quota display.
func MachineAgentOrder(meta *v1.MachineMeta) []v1.Agent {
	var out []v1.Agent
	seen := make(map[v1.Agent]bool)
	for _, info := range lists.MachineVersions(meta) {
		a := info.Agent
		if a == v1.Agent_AGENT_UNSPECIFIED || seen[a] || lists.IsAgentDisabled(meta, a) {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	if meta != nil {
		for _, q := range meta.Quotas {
			if q == nil || q.Agent == v1.Agent_AGENT_UNSPECIFIED || seen[q.Agent] || lists.IsAgentDisabled(meta, q.Agent) {
				continue
			}
			seen[q.Agent] = true
			out = append(out, q.Agent)
		}
	}
	if len(out) == 0 && meta != nil && len(meta.DisabledAgents) > 0 {
		// Safety fallback matching daemon invariant: never leave zero enabled CLIs.
		for _, info := range lists.MachineVersions(meta) {
			if info.Agent != v1.Agent_AGENT_UNSPECIFIED && !seen[info.Agent] {
				seen[info.Agent] = true
				out = append(out, info.Agent)
			}
		}
	}
	return out
}

func agentDisplayName(a v1.Agent) string {
	info := lists.AgentInfo(a)
	if info.Name != "" {
		return info.Name
	}
	return strconv.Itoa(int(a))
}

// BuildCliSections computes quota sections for all relevant CLIs on a machine.
// If filterAgent is specified, only that CLI is returned.
func BuildCliSections(m *lists.Machine, filterAgent v1.Agent, nowMs int64) []*corev1.QuotaCliSection {
	if m == nil || m.Meta == nil {
		return nil
	}
	reachable := lists.MachineReachable(m)
	byAgent := make(map[v1.Agent][]*v1.AgentQuota)
	for _, q := range m.Meta.Quotas {
		if q == nil || q.Agent == v1.Agent_AGENT_UNSPECIFIED {
			continue
		}
		byAgent[q.Agent] = append(byAgent[q.Agent], q)
	}

	var agents []v1.Agent
	if filterAgent != v1.Agent_AGENT_UNSPECIFIED {
		agents = []v1.Agent{filterAgent}
	} else {
		agents = MachineAgentOrder(m.Meta)
	}

	sections := make([]*corev1.QuotaCliSection, 0, len(agents))
	for _, agent := range agents {
		entries := byAgent[agent]
		sec := &corev1.QuotaCliSection{
			Agent:     agent,
			AgentName: agentDisplayName(agent),
		}
		if len(entries) == 0 {
			sec.State = corev1.QuotaSectionState_QUOTA_SECTION_STATE_UNSUPPORTED
			sec.Unsupported = true
			sec.EmptyOrErrorText = text.Msg(text.KeyQuotaQuotaLookupIsNotSupportedYet)
			sec.UsageHint = usageHintForAgent(agent)
			sec.SummaryMeta = text.Msg(text.KeyQuotaQuotaLookupIsNotSupportedYet)
			sec.SummaryTone = corev1.QuotaTone_QUOTA_TONE_STALE
			sec.SummaryPrimaryText = text.Msg(text.KeyQuotaQuotaLookupIsNotSupportedYet)
			sections = append(sections, sec)
			continue
		}

		var allRows []*corev1.QuotaRowView
		for _, q := range entries {
			grp, rows := buildAccountGroup(agent, q, reachable, nowMs)
			sec.Accounts = append(sec.Accounts, grp)
			allRows = append(allRows, rows...)
		}

		first := sec.Accounts[0]
		sec.State = first.State
		sec.Unsupported = first.State == corev1.QuotaSectionState_QUOTA_SECTION_STATE_UNSUPPORTED
		if len(allRows) > 0 {
			tight := TightestRow(allRows)
			sec.SummaryRow = tight
			sec.SummaryTone = tight.Tone
			lineLabel := tight.PoolName
			if lineLabel == nil {
				lineLabel = tight.Label
			}
			val := tight.RemainingText
			if lineLabel != nil && val != nil {
				sec.SummaryLine = text.Join(text.KeyCommonNoteSeparator, lineLabel, val)
			} else if val != nil {
				sec.SummaryLine = val
			}
			sec.SummaryPrimaryText = val
			sec.SummaryPrimaryWarn = tight.Tone == corev1.QuotaTone_QUOTA_TONE_WARN || tight.Tone == corev1.QuotaTone_QUOTA_TONE_FULL
			if tight.Exhausted && !tight.Stale && !tight.PastReset && tight.ResetText != nil {
				sec.SummaryWindowText = tight.ResetText
			} else {
				sec.SummaryWindowText = lineLabel
			}
		} else {
			sec.SummaryTone = corev1.QuotaTone_QUOTA_TONE_STALE
			sec.EmptyOrErrorText = first.StatusText
			sec.SummaryMeta = first.StatusText
			sec.SummaryPrimaryText = first.StatusText
			if first.ErrorText != nil && !text.IsEmpty(first.ErrorText) {
				sec.EmptyOrErrorText = first.ErrorText
			}
			sec.UsageHint = first.UsageHint
		}
		if first.FreshnessText != nil && sec.SummaryMeta == nil {
			sec.SummaryMeta = first.FreshnessText
		}
		sections = append(sections, sec)
	}
	return sections
}

// MachineCardSummaries returns compact quota sections for a machine card (only entries with a percentage).
func MachineCardSummaries(m *lists.Machine, nowMs int64) []*corev1.QuotaCliSection {
	sections := BuildCliSections(m, v1.Agent_AGENT_UNSPECIFIED, nowMs)
	var out []*corev1.QuotaCliSection
	for _, s := range sections {
		if s.SummaryRow != nil && s.SummaryRow.HasPercent {
			out = append(out, s)
		}
	}
	return out
}

// Banner computes the global account quota warning/exhausted banner across machines.
// Returns nil when no reachable non-stale window is >= 80% used.
func Banner(machines []*lists.Machine, nowMs int64) *corev1.QuotaBannerView {
	type candidate struct {
		score  int32
		banner *corev1.QuotaBannerView
	}
	var best *candidate
	for _, m := range machines {
		if !lists.MachineReachable(m) {
			continue
		}
		for _, s := range BuildCliSections(m, v1.Agent_AGENT_UNSPECIFIED, nowMs) {
			for _, acct := range s.Accounts {
				for _, wg := range acct.Groups {
					for _, r := range wg.Rows {
						if !r.HasPercent || r.Stale || r.UsedPercent < warningUsedPercent {
							continue
						}
						sev := corev1.QuotaBannerSeverity_QUOTA_BANNER_SEVERITY_WARN
						key := text.KeyQuotaWarning
						score := r.UsedPercent
						if r.Exhausted {
							sev = corev1.QuotaBannerSeverity_QUOTA_BANNER_SEVERITY_FULL
							key = text.KeyQuotaExhausted
							score += 1000
						}
						winText := r.PoolName
						if winText == nil {
							winText = r.Label
						}
						msg := text.Msg(
							key,
							text.Str("name", s.AgentName),
							text.ArgText("window", winText),
						)
						if best == nil || score > best.score {
							best = &candidate{
								score: score,
								banner: &corev1.QuotaBannerView{
									Severity:   sev,
									Message:    msg,
									MachineId:  m.ID,
									Agent:      s.Agent,
									WindowName: r.WindowName,
								},
							}
						}
					}
				}
			}
		}
	}
	if best == nil {
		return nil
	}
	return best.banner
}

func pad2[T ~int | ~int32 | ~int64](v T) string {
	if v < 10 && v >= 0 {
		return "0" + strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatInt(int64(v), 10)
}
