package lists

import (
	"cmp"
	"slices"
	"strings"

	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

// RecentOpsMax is the maximum number of recent operation IDs retained in ProjectPrefs.
const RecentOpsMax = 64

// ProjectSummary represents one project row in listings and scope selectors.
type ProjectSummary struct {
	Key             string
	Name            string
	ShortPath       string
	Sessions        []*Session
	UpdatedMs       int64
	HasLive         bool
	LatestCreatedMs int64
	Paths           []string
	MachineIDs      []string
	MachineNames    []string
	Ended           int32
	WaitingCount    int32
	LiveCount       int32
	AllOffline      bool
	Pinned          bool
	Hidden          bool
}

// RowMeta controls which cross-item disambiguators should be shown on rows.
type RowMeta struct {
	ShowMachine bool
	ShowAgent   bool
	ShowProject bool
}

// ScopeRow represents one choice in the home scope switcher.
type ScopeRow struct {
	Scope        Scope
	WaitingCount int32
	LiveCount    int32
	ProjectCount int32
	Current      bool
	Project      *ProjectSummary
}

// ProjectsViewResult holds the derived projects listing and resolved scope.
type ProjectsViewResult struct {
	All           []*ProjectSummary
	Pinned        []*ProjectSummary
	Others        []*ProjectSummary
	Hidden        []*ProjectSummary
	Listed        []*ProjectSummary
	FilteredPin   []*ProjectSummary
	FilteredOther []*ProjectSummary
	DefaultScope  Scope
	ResolvedScope Scope
	ResolvedToken string
	ScopeRows     []*ScopeRow
	WaitingTotal  int32
}

// PrefsSetSettingsOp updates per-project new-session configuration.
type PrefsSetSettingsOp struct {
	Path         string
	MachineID    string
	Moved        bool
	Agent        *v1.Agent
	Sets         []*v1.AgentSettings
	KeepExisting bool
}

// FullPrefsOp represents a typed mutation to ProjectPrefs.
type FullPrefsOp struct {
	Kind        PrefsOpKind
	Path        string
	SetSettings *PrefsSetSettingsOp
}

// ProjectKey returns the session's project identity (its working directory path).
func ProjectKey(s *Session) string {
	if s == nil || s.Info == nil {
		return ""
	}
	return s.Info.Cwd
}

// ProjectName returns the last non-empty path segment of key, or empty string if none exists.
func ProjectName(key string) string {
	parts := strings.Split(key, "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return ""
}

// MachineName returns the display name of m (hostname without ".local", else first 8 chars of ID).
func MachineName(m *Machine, fallbackID string) string {
	if m != nil && m.Meta != nil && m.Meta.Hostname != "" {
		host := strings.TrimSuffix(m.Meta.Hostname, ".local")
		if host != "" {
			return host
		}
	}
	id := fallbackID
	if m != nil && m.ID != "" {
		id = m.ID
	}
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

type projectSums struct {
	endedBy         map[string]int32
	latestCreatedMs int64
	latestEndedMs   int64
	machineIDs      []string
}

type projectAgg struct {
	key             string
	name            string
	sessions        []*Session
	updatedMs       int64
	hasLive         bool
	latestCreatedMs int64
	paths           []string
	machineIDs      []string
	machineNames    []string
	ended           int32
	waitingCount    int32
	liveCount       int32
	allOffline      bool
}

func indexMachines(w *World) map[string]*Machine {
	machines := make(map[string]*Machine)
	if w == nil {
		return machines
	}
	for _, m := range w.Machines {
		if m != nil && m.ID != "" {
			machines[m.ID] = m
		}
	}
	return machines
}

func appendUniqueStr(dst []string, seen map[string]bool, v string) []string {
	if v == "" || seen[v] {
		return dst
	}
	seen[v] = true
	return append(dst, v)
}

func aggregateProjects(w *World) ([]*projectAgg, map[string]*Machine) {
	machinesByID := indexMachines(w)
	if w == nil {
		return nil, machinesByID
	}

	byKey := make(map[string][]*Session)
	var keysOrder []string
	seenKeys := make(map[string]bool)
	addKey := func(k string) {
		if !seenKeys[k] {
			seenKeys[k] = true
			keysOrder = append(keysOrder, k)
		}
	}

	for _, s := range w.Sessions {
		if s == nil {
			continue
		}
		if s.Info != nil && s.Info.TaskOwned && !NeedsAttention(s) {
			continue
		}
		k := ProjectKey(s)
		addKey(k)
		byKey[k] = append(byKey[k], s)
	}

	sortedMachines := make([]*Machine, 0, len(machinesByID))
	for _, m := range machinesByID {
		sortedMachines = append(sortedMachines, m)
	}
	slices.SortFunc(sortedMachines, func(a, b *Machine) int {
		return compareUTF16(a.ID, b.ID)
	})

	type machineSummary struct {
		machineID string
		summary   *v1.ProjectSummary
	}
	var summaries []machineSummary
	for _, m := range sortedMachines {
		if m.Meta == nil {
			continue
		}
		for _, ps := range m.Meta.Projects {
			if ps != nil {
				summaries = append(summaries, machineSummary{machineID: m.ID, summary: ps})
			}
		}
	}
	slices.SortStableFunc(summaries, func(a, b machineSummary) int {
		if c := cmp.Compare(b.summary.LatestCreatedMs, a.summary.LatestCreatedMs); c != 0 {
			return c
		}
		return compareUTF16(a.machineID, b.machineID)
	})

	sums := make(map[string]*projectSums)
	for _, item := range summaries {
		p := item.summary
		sum := sums[p.Path]
		if sum == nil {
			sum = &projectSums{endedBy: make(map[string]int32)}
			sums[p.Path] = sum
			addKey(p.Path)
		}
		sum.endedBy[item.machineID] = int32(p.Ended)
		if p.LatestCreatedMs > sum.latestCreatedMs {
			sum.latestCreatedMs = p.LatestCreatedMs
		}
		if p.LatestEndedMs > sum.latestEndedMs {
			sum.latestEndedMs = p.LatestEndedMs
		}
		if !slices.Contains(sum.machineIDs, item.machineID) {
			sum.machineIDs = append(sum.machineIDs, item.machineID)
		}
	}

	added := make(map[string]*v1.ProjectSettings)
	if w.Prefs != nil {
		for _, st := range w.Prefs.Settings {
			if st == nil || st.Path == "" {
				continue
			}
			if _, ok := byKey[st.Path]; ok {
				continue
			}
			if _, ok := sums[st.Path]; ok {
				continue
			}
			if _, ok := machinesByID[st.MachineId]; !ok {
				continue
			}
			addKey(st.Path)
			added[st.Path] = st
		}
	}

	out := make([]*projectAgg, 0, len(keysOrder))
	seenMachines := make(map[string]bool, 4)
	heldEndedBy := make(map[string]int32, 4)
	for _, key := range keysOrder {
		sess := byKey[key]
		sum := sums[key]
		own := added[key]

		clear(seenMachines)
		clear(heldEndedBy)
		var machineIDs []string
		var paths []string
		var updatedMs int64
		var latestCreatedMs int64
		var hasLive bool
		var waitingCount, liveCount int32

		for _, s := range sess {
			machineIDs = appendUniqueStr(machineIDs, seenMachines, s.MachineID)
			if s.Info != nil && s.Info.Cwd != "" {
				if len(paths) == 0 || !slices.Contains(paths, s.Info.Cwd) {
					paths = append(paths, s.Info.Cwd)
				}
			}
			if s.UpdatedMs > updatedMs {
				updatedMs = s.UpdatedMs
			}
			exited := Exited(s)
			if !exited {
				hasLive = true
			} else {
				heldEndedBy[s.MachineID]++
			}
			if c := CreatedMs(s); c > latestCreatedMs {
				latestCreatedMs = c
			}
			if NeedsAttention(s) {
				waitingCount++
			} else if (s.Info == nil || !s.Info.TaskOwned) && !exited {
				liveCount++
			}
		}
		if len(paths) == 0 && key != "" {
			paths = []string{key}
		}
		if sum != nil {
			for _, id := range sum.machineIDs {
				machineIDs = appendUniqueStr(machineIDs, seenMachines, id)
			}
			if sum.latestCreatedMs > updatedMs {
				updatedMs = sum.latestCreatedMs
			}
			if sum.latestEndedMs > updatedMs {
				updatedMs = sum.latestEndedMs
			}
			if sum.latestCreatedMs > latestCreatedMs {
				latestCreatedMs = sum.latestCreatedMs
			}
		}
		if own != nil {
			machineIDs = appendUniqueStr(machineIDs, seenMachines, own.MachineId)
			if own.UpdatedMs > updatedMs {
				updatedMs = own.UpdatedMs
			}
			if own.UpdatedMs > latestCreatedMs {
				latestCreatedMs = own.UpdatedMs
			}
		}

		var endedCount int32
		for _, mid := range machineIDs {
			var sumEnded int32
			if sum != nil {
				sumEnded = sum.endedBy[mid]
			}
			endedCount += max(sumEnded, heldEndedBy[mid])
		}

		var machineNames []string
		anyReachable := false
		for _, mid := range machineIDs {
			m := machinesByID[mid]
			if MachineReachable(m) {
				anyReachable = true
			}
			name := MachineName(m, mid)
			if name != "" && !slices.Contains(machineNames, name) {
				machineNames = append(machineNames, name)
			}
		}

		out = append(out, &projectAgg{
			key:             key,
			name:            ProjectName(key),
			sessions:        sess,
			updatedMs:       updatedMs,
			hasLive:         hasLive,
			latestCreatedMs: latestCreatedMs,
			paths:           paths,
			machineIDs:      machineIDs,
			machineNames:    machineNames,
			ended:           endedCount,
			waitingCount:    waitingCount,
			liveCount:       liveCount,
			allOffline:      len(machineIDs) > 0 && !anyReachable,
		})
	}

	slices.SortFunc(out, func(a, b *projectAgg) int {
		if a.hasLive != b.hasLive {
			if a.hasLive {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(b.latestCreatedMs, a.latestCreatedMs); c != 0 {
			return c
		}
		return compareUTF16(a.key, b.key)
	})

	return out, machinesByID
}

func toProjectSummary(p *projectAgg, pinned, hidden bool) *ProjectSummary {
	return &ProjectSummary{
		Key:             p.key,
		Name:            p.name,
		ShortPath:       ShortPath(p.key),
		Sessions:        p.sessions,
		UpdatedMs:       p.updatedMs,
		HasLive:         p.hasLive,
		LatestCreatedMs: p.latestCreatedMs,
		Paths:           slices.Clone(p.paths),
		MachineIDs:      slices.Clone(p.machineIDs),
		MachineNames:    slices.Clone(p.machineNames),
		Ended:           p.ended,
		WaitingCount:    p.waitingCount,
		LiveCount:       p.liveCount,
		AllOffline:      p.allOffline,
		Pinned:          pinned,
		Hidden:          hidden,
	}
}

func prefsSets(prefs *v1.ProjectPrefs) ([]string, map[string]bool) {
	var pinned []string
	hidden := make(map[string]bool)
	if prefs == nil {
		return pinned, hidden
	}
	for _, p := range prefs.Hidden {
		if p != nil && p.Path != "" {
			hidden[p.Path] = true
		}
	}
	for _, p := range prefs.Pinned {
		if p != nil && p.Path != "" {
			pinned = append(pinned, p.Path)
		}
	}
	return pinned, hidden
}

// IsHidden reports whether key is marked hidden in prefs.
func IsHidden(prefs *v1.ProjectPrefs, key string) bool {
	if prefs == nil {
		return false
	}
	for _, p := range prefs.Hidden {
		if p != nil && p.Path == key {
			return true
		}
	}
	return false
}

// IsPinned reports whether key is marked pinned in prefs and not also hidden.
func IsPinned(prefs *v1.ProjectPrefs, key string) bool {
	if prefs == nil || IsHidden(prefs, key) {
		return false
	}
	for _, p := range prefs.Pinned {
		if p != nil && p.Path == key {
			return true
		}
	}
	return false
}

// SavedSettings returns the stored ProjectSettings for key, or nil if absent.
func SavedSettings(prefs *v1.ProjectPrefs, key string) *v1.ProjectSettings {
	if prefs == nil {
		return nil
	}
	for _, s := range prefs.Settings {
		if s != nil && s.Path == key {
			return s
		}
	}
	return nil
}

// BuildProjects computes projects, pinned/others/hidden groups, listing, scope resolution, and scope rows.
func BuildProjects(w *World, search string, remembered *Scope, rememberedToken string, hmacKey []byte, currentScope *Scope) *ProjectsViewResult {
	if w == nil {
		return &ProjectsViewResult{
			DefaultScope:  Scope{AllProjects: true},
			ResolvedScope: Scope{AllProjects: true},
		}
	}
	allAgg, _ := aggregateProjects(w)
	return buildProjectsFromAgg(w, allAgg, search, remembered, rememberedToken, hmacKey, currentScope)
}

func buildProjectsFromAgg(w *World, allAgg []*projectAgg, search string, remembered *Scope, rememberedToken string, hmacKey []byte, currentScope *Scope) *ProjectsViewResult {
	res := &ProjectsViewResult{
		DefaultScope:  Scope{AllProjects: true},
		ResolvedScope: Scope{AllProjects: true},
	}
	pinnedOrder, hiddenSet := prefsSets(w.Prefs)

	byKey := make(map[string]*projectAgg, len(allAgg))
	for _, p := range allAgg {
		byKey[p.key] = p
	}

	pinnedSet := make(map[string]bool)
	var pinnedViews []*ProjectSummary
	for _, path := range pinnedOrder {
		if hiddenSet[path] || pinnedSet[path] {
			continue
		}
		if p, ok := byKey[path]; ok {
			pinnedSet[path] = true
			pinnedViews = append(pinnedViews, toProjectSummary(p, true, false))
		}
	}

	var allViews, othersViews, hiddenViews []*ProjectSummary
	for _, p := range allAgg {
		isHid := hiddenSet[p.key]
		isPin := pinnedSet[p.key]
		v := toProjectSummary(p, isPin, isHid)
		allViews = append(allViews, v)
		if isHid {
			hiddenViews = append(hiddenViews, v)
		} else if !isPin {
			othersViews = append(othersViews, v)
		}
	}

	listed := make([]*ProjectSummary, 0, len(pinnedViews)+len(othersViews))
	listed = append(listed, pinnedViews...)
	listed = append(listed, othersViews...)

	defScope := defaultScopeFromListed(listed)
	rem := remembered
	if rem == nil && rememberedToken != "" && len(hmacKey) > 0 {
		rem = DecodeScope(rememberedToken, hmacKey, listed)
	}
	resolved := ResolveScope(rem, listed)

	var resolvedToken string
	if len(hmacKey) > 0 {
		resolvedToken = EncodeScope(resolved, hmacKey)
	}

	trimmedSearch := strings.TrimSpace(search)
	lowerSearch := strings.ToLower(trimmedSearch)
	matchProject := func(p *ProjectSummary) bool {
		if lowerSearch == "" {
			return true
		}
		return strings.Contains(strings.ToLower(p.Name), lowerSearch) ||
			strings.Contains(strings.ToLower(p.Key), lowerSearch)
	}
	var filteredPinned, filteredOthers []*ProjectSummary
	for _, p := range pinnedViews {
		if matchProject(p) {
			filteredPinned = append(filteredPinned, p)
		}
	}
	for _, p := range othersViews {
		if matchProject(p) {
			filteredOthers = append(filteredOthers, p)
		}
	}

	cur := resolved
	if currentScope != nil {
		cur = *currentScope
	}
	var scopeRows []*ScopeRow
	if len(listed) > 1 && trimmedSearch == "" {
		wCount, lCount := ActivityCounts(w.Sessions)
		scopeRows = append(scopeRows, &ScopeRow{
			Scope:        Scope{AllProjects: true},
			WaitingCount: wCount,
			LiveCount:    lCount,
			ProjectCount: int32(len(listed)),
			Current:      cur.AllProjects,
		})
	}
	for _, p := range listed {
		if !matchProject(p) {
			continue
		}
		scopeRows = append(scopeRows, &ScopeRow{
			Scope: Scope{
				Key:       p.Key,
				Name:      p.Name,
				ShortPath: p.ShortPath,
			},
			WaitingCount: p.WaitingCount,
			LiveCount:    p.LiveCount,
			Current:      !cur.AllProjects && cur.Key == p.Key,
			Project:      p,
		})
	}

	res.All = allViews
	res.Pinned = pinnedViews
	res.Others = othersViews
	res.Hidden = hiddenViews
	res.Listed = listed
	res.FilteredPin = filteredPinned
	res.FilteredOther = filteredOthers
	res.DefaultScope = defScope
	res.ResolvedScope = resolved
	res.ResolvedToken = resolvedToken
	res.ScopeRows = scopeRows
	res.WaitingTotal, _ = ActivityCounts(w.Sessions)
	return res
}

// ApplySettings applies a PrefsSetSettingsOp to stored (or nil).
func ApplySettings(stored *v1.ProjectSettings, op *PrefsSetSettingsOp, nowMs int64) *v1.ProjectSettings {
	var b *v1.ProjectSettings
	if stored != nil {
		b = stored.Clone()
	} else {
		b = &v1.ProjectSettings{
			Path:      op.Path,
			MachineId: op.MachineID,
		}
	}
	sets := make(map[v1.Agent]*v1.AgentSettings, len(b.Agents))
	for _, s := range b.Agents {
		if s != nil {
			sets[s.Agent] = s.Clone()
		}
	}
	if op.Moved {
		b.MachineId = op.MachineID
		for _, a := range SettingsAgents {
			sets[a] = &v1.AgentSettings{Agent: a}
		}
	}
	if op.Agent != nil {
		b.Agent = *op.Agent
	}
	for _, s := range op.Sets {
		if s == nil {
			continue
		}
		if !op.KeepExisting || sets[s.Agent] == nil {
			sets[s.Agent] = s.Clone()
		}
	}
	agents := make([]*v1.AgentSettings, 0, len(sets))
	for _, s := range sets {
		agents = append(agents, s)
	}
	slices.SortFunc(agents, func(a, b *v1.AgentSettings) int {
		return cmp.Compare(a.Agent, b.Agent)
	})
	b.Agents = agents
	b.UpdatedMs = nowMs
	return b
}

// ApplyPrefsOp applies op to base and returns the updated ProjectPrefs.
func ApplyPrefsOp(base *v1.ProjectPrefs, op *FullPrefsOp, opID string, nowMs int64) *v1.ProjectPrefs {
	var out *v1.ProjectPrefs
	if base != nil {
		out = base.Clone()
	} else {
		out = &v1.ProjectPrefs{}
	}
	out.Schema = 1
	newPref := func(path string) *v1.ProjectPref {
		return &v1.ProjectPref{Path: path, SinceMs: nowMs}
	}
	removePath := func(list []*v1.ProjectPref, path string) []*v1.ProjectPref {
		var kept []*v1.ProjectPref
		for _, p := range list {
			if p != nil && p.Path != path {
				kept = append(kept, p)
			}
		}
		return kept
	}
	hasPath := func(list []*v1.ProjectPref, path string) bool {
		for _, p := range list {
			if p != nil && p.Path == path {
				return true
			}
		}
		return false
	}

	if op != nil {
		switch op.Kind {
		case PrefsOpPin:
			out.Hidden = removePath(out.Hidden, op.Path)
			if !hasPath(out.Pinned, op.Path) {
				out.Pinned = append(out.Pinned, newPref(op.Path))
			}
		case PrefsOpUnpin:
			out.Pinned = removePath(out.Pinned, op.Path)
		case PrefsOpHide:
			out.Pinned = removePath(out.Pinned, op.Path)
			if !hasPath(out.Hidden, op.Path) {
				out.Hidden = append(out.Hidden, newPref(op.Path))
			}
		case PrefsOpUnhide, PrefsOpAdd:
			out.Hidden = removePath(out.Hidden, op.Path)
		case PrefsOpSetSettings:
			if op.SetSettings != nil {
				var existing *v1.ProjectSettings
				var rest []*v1.ProjectSettings
				for _, cur := range out.Settings {
					if cur != nil && cur.Path == op.SetSettings.Path {
						existing = cur
					} else if cur != nil {
						rest = append(rest, cur)
					}
				}
				out.Settings = append(rest, ApplySettings(existing, op.SetSettings, nowMs))
			}
		}
	}

	if opID != "" {
		ops := append(slices.Clone(out.RecentOps), opID)
		if len(ops) > RecentOpsMax {
			ops = ops[len(ops)-RecentOpsMax:]
		}
		out.RecentOps = ops
	}
	return out
}

// PlanAddProject computes the FullPrefsOp needed to add/re-show path on machineID, or nil if no-op.
func PlanAddProject(w *World, machineID, path string) *FullPrefsOp {
	var prefs *v1.ProjectPrefs
	if w != nil {
		prefs = w.Prefs
	}
	saved := SavedSettings(prefs, path)
	if saved == nil || saved.MachineId != machineID {
		return &FullPrefsOp{
			Kind: PrefsOpSetSettings,
			Path: path,
			SetSettings: &PrefsSetSettingsOp{
				Path:      path,
				MachineID: machineID,
				Moved:     true,
			},
		}
	}
	if IsHidden(prefs, path) {
		return &FullPrefsOp{
			Kind: PrefsOpUnhide,
			Path: path,
		}
	}
	return nil
}
