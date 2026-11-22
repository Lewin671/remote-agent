package schedule

import (
	"slices"

	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
)

// SeenToken is an opaque (task_id, run_id) read marker persisted per machine.
type SeenToken struct {
	TaskID string
	RunID  string
}

// NotificationRef identifies one notified run whose OS/web push notification can be dismissed.
type NotificationRef struct {
	MachineID       string
	TaskID          string
	RunID           string
	SessionID       string
	NotificationSeq uint64
}

type unreadGuard struct {
	machine string
	task    string
	run     string
}

type dismissKey struct {
	machine string
	task    string
	run     string
	session string
	seq     uint64
}

// SeenOutcome captures persistence and notification side effects emitted by a SeenTracker step.
type SeenOutcome struct {
	SaveMachines []string
	Dismiss      []NotificationRef
}

// SeenTracker manages read/unread state, detail-entry dot pinning, and notification dismissal.
type SeenTracker struct {
	seenLoaded          map[string]bool
	seen                map[string]map[SeenToken]bool
	pendingReadInMemory map[string]map[SeenToken]bool
	loaded              map[string][]*Task
	verified            map[string]bool
	manuallyUnread      map[unreadGuard]bool
	unreadReturnSession string
	dismissed           map[dismissKey]bool

	detail         *TaskKey
	visit          *TaskKey
	entryUnread    map[string]bool
	entryCaptured  bool
	visibleSession string
}

// NewSeenTracker creates an empty SeenTracker.
func NewSeenTracker() *SeenTracker {
	return &SeenTracker{
		seenLoaded:          make(map[string]bool),
		seen:                make(map[string]map[SeenToken]bool),
		pendingReadInMemory: make(map[string]map[SeenToken]bool),
		loaded:              make(map[string][]*Task),
		verified:            make(map[string]bool),
		manuallyUnread:      make(map[unreadGuard]bool),
		dismissed:           make(map[dismissKey]bool),
	}
}

func (s *SeenTracker) clearUnreadGuards() {
	clear(s.manuallyUnread)
	s.unreadReturnSession = ""
}

func visibleNotifiedTokens(tasks []*Task) map[SeenToken]bool {
	out := make(map[SeenToken]bool)
	for _, t := range tasks {
		if !Visible(t) {
			continue
		}
		for _, r := range AllRuns(t) {
			if r.Notified && r.ID != "" {
				out[SeenToken{TaskID: t.ID, RunID: r.ID}] = true
			}
		}
	}
	return out
}

type seenTokensSorted []SeenToken

func (s seenTokensSorted) Len() int { return len(s) }
func (s seenTokensSorted) Less(i, j int) bool {
	if s[i].TaskID != s[j].TaskID {
		return s[i].TaskID < s[j].TaskID
	}
	return s[i].RunID < s[j].RunID
}
func (s seenTokensSorted) Swap(i, j int) { s[i], s[j] = s[j], s[i] }

// TokensFor returns sorted seen tokens for machineID.
func (s *SeenTracker) TokensFor(machineID string) []SeenToken {
	m := s.seen[machineID]
	out := make([]SeenToken, 0, len(m))
	for tok := range m {
		out = append(out, tok)
	}
	sortutil.SortInterface(seenTokensSorted(out))
	return out
}

// LoadSeen applies persisted seen tokens loaded from shell storage for machineID.
func (s *SeenTracker) LoadSeen(machineID string, tokens []SeenToken, found bool) SeenOutcome {
	s.seenLoaded[machineID] = true
	var out SeenOutcome

	if found {
		m := make(map[SeenToken]bool, len(tokens))
		for _, tok := range tokens {
			if tok.TaskID != "" && tok.RunID != "" {
				m[tok] = true
			}
		}
		for tok := range s.pendingReadInMemory[machineID] {
			m[tok] = true
		}
		delete(s.pendingReadInMemory, machineID)
		s.seen[machineID] = m
		if s.verified[machineID] {
			valid := visibleNotifiedTokens(s.loaded[machineID])
			pruned := false
			for tok := range m {
				if !valid[tok] {
					delete(m, tok)
					pruned = true
				}
			}
			if pruned {
				out.SaveMachines = append(out.SaveMachines, machineID)
			}
		}
	} else if tasks, hasTasks := s.loaded[machineID]; hasTasks {
		baseline := visibleNotifiedTokens(tasks)
		for tok := range s.pendingReadInMemory[machineID] {
			baseline[tok] = true
		}
		delete(s.pendingReadInMemory, machineID)
		s.seen[machineID] = baseline
		out.SaveMachines = append(out.SaveMachines, machineID)
	}

	s.markViewed(machineID, &out)
	return out
}

// Observe ingests a cached (authoritative=false) or live (authoritative=true) task list for machineID.
func (s *SeenTracker) Observe(machineID string, tasks []*Task, authoritative bool) SeenOutcome {
	s.loaded[machineID] = tasks
	s.verified[machineID] = authoritative
	var out SeenOutcome

	valid := visibleNotifiedTokens(tasks)
	if s.seenLoaded[machineID] {
		if s.seen[machineID] == nil {
			s.seen[machineID] = valid
			out.SaveMachines = append(out.SaveMachines, machineID)
		} else if authoritative {
			cur := s.seen[machineID]
			changed := false
			for tok := range cur {
				if !valid[tok] {
					delete(cur, tok)
					changed = true
				}
			}
			if changed {
				out.SaveMachines = append(out.SaveMachines, machineID)
			}
		}
	}
	if authoritative {
		for g := range s.manuallyUnread {
			if g.machine == machineID && !valid[SeenToken{TaskID: g.task, RunID: g.run}] {
				delete(s.manuallyUnread, g)
			}
		}
	}

	s.markViewed(machineID, &out)
	return out
}

// UnreadIDs returns the set of notified run IDs on task not yet marked seen.
func (s *SeenTracker) UnreadIDs(machineID string, task *Task) map[string]bool {
	if task == nil {
		return nil
	}
	m := s.seen[machineID]
	if m == nil {
		// Before baseline is established, do not invent false unread badges.
		return nil
	}
	var out map[string]bool
	for _, r := range AllRuns(task) {
		if r.Notified && r.ID != "" && !m[SeenToken{TaskID: task.ID, RunID: r.ID}] {
			if out == nil {
				out = make(map[string]bool)
			}
			out[r.ID] = true
		}
	}
	return out
}

// HasNewResult reports whether task has any unread notified run.
func (s *SeenTracker) HasNewResult(machineID string, task *Task) bool {
	return len(s.UnreadIDs(machineID, task)) > 0
}

// DetailUnread returns the run IDs captured as unread when entering the active detail visit.
func (s *SeenTracker) DetailUnread(machineID, taskID string) map[string]bool {
	if !s.entryCaptured || s.visit == nil || s.visit.MachineID != machineID || s.visit.TaskID != taskID {
		return nil
	}
	return s.entryUnread
}

// DetailCaptured reports whether the active detail visit has captured its entry snapshot.
func (s *SeenTracker) DetailCaptured(machineID, taskID string) bool {
	return s.entryCaptured && s.visit != nil && s.visit.MachineID == machineID && s.visit.TaskID == taskID
}

// OpenTask records entering or returning to a task's detail screen.
func (s *SeenTracker) OpenTask(machineID, taskID string) SeenOutcome {
	next := TaskKey{MachineID: machineID, TaskID: taskID}
	if s.detail == nil || *s.detail != next {
		s.clearUnreadGuards()
	}
	if s.visit == nil || *s.visit != next {
		s.visit = &next
		s.entryUnread = nil
		s.entryCaptured = false
	} else if s.entryCaptured {
		for _, t := range s.loaded[machineID] {
			if t.ID == taskID {
				for rid := range s.UnreadIDs(machineID, t) {
					if s.entryUnread == nil {
						s.entryUnread = make(map[string]bool)
					}
					s.entryUnread[rid] = true
				}
				break
			}
		}
	}
	s.detail = &next
	var out SeenOutcome
	s.markViewed(machineID, &out)
	return out
}

// HideTask pauses reading when the detail screen moves to the background while preserving the visit's dots.
func (s *SeenTracker) HideTask(machineID, taskID string) {
	key := TaskKey{MachineID: machineID, TaskID: taskID}
	if s.detail != nil && *s.detail == key {
		s.detail = nil
		if s.visibleSession == "" && s.unreadReturnSession == "" {
			s.clearUnreadGuards()
		}
	}
}

// CloseTask clears detail visit state when leaving the detail screen.
func (s *SeenTracker) CloseTask(machineID, taskID string) {
	key := TaskKey{MachineID: machineID, TaskID: taskID}
	if s.detail != nil && *s.detail == key {
		s.detail = nil
	}
	if s.visit != nil && *s.visit == key {
		s.visit = nil
		s.entryUnread = nil
		s.entryCaptured = false
		if s.visibleSession == "" && s.unreadReturnSession == "" {
			s.clearUnreadGuards()
		}
	}
}

// SetVisibleSession updates the currently viewed conversation ID so matching notified runs are marked read.
func (s *SeenTracker) SetVisibleSession(sessionID string) SeenOutcome {
	if sessionID != "" && sessionID == s.unreadReturnSession {
		s.unreadReturnSession = ""
	} else if s.visibleSession != sessionID {
		s.clearUnreadGuards()
	}
	s.visibleSession = sessionID
	var out SeenOutcome
	for machineID := range s.loaded {
		s.markViewed(machineID, &out)
	}
	return out
}

// ShowList clears active detail/session viewing guards when returning to the Schedules list.
func (s *SeenTracker) ShowList() {
	s.detail = nil
	s.visibleSession = ""
	s.clearUnreadGuards()
}

// MarkRead explicitly marks all notified runs on task as read.
func (s *SeenTracker) MarkRead(machineID string, task *Task) SeenOutcome {
	var out SeenOutcome
	if task == nil {
		return out
	}
	for g := range s.manuallyUnread {
		if g.machine == machineID && g.task == task.ID {
			delete(s.manuallyUnread, g)
		}
	}
	if s.seen[machineID] == nil {
		if !s.seenLoaded[machineID] {
			if s.pendingReadInMemory[machineID] == nil {
				s.pendingReadInMemory[machineID] = make(map[SeenToken]bool)
			}
			for _, r := range AllRuns(task) {
				if r.Notified && r.ID != "" {
					s.pendingReadInMemory[machineID][SeenToken{TaskID: task.ID, RunID: r.ID}] = true
					s.maybeDismiss(machineID, task.ID, r, &out)
				}
			}
			return out
		}
		s.seen[machineID] = make(map[SeenToken]bool)
	}
	changed := false
	for _, r := range AllRuns(task) {
		if r.Notified && r.ID != "" {
			tok := SeenToken{TaskID: task.ID, RunID: r.ID}
			if !s.seen[machineID][tok] {
				s.seen[machineID][tok] = true
				changed = true
			}
			s.maybeDismiss(machineID, task.ID, r, &out)
		}
	}
	if changed {
		out.SaveMachines = append(out.SaveMachines, machineID)
	}
	return out
}

// MarkUnread explicitly marks task's latest notification unread with navigation protection.
func (s *SeenTracker) MarkUnread(machineID string, task *Task) SeenOutcome {
	var out SeenOutcome
	if task == nil {
		return out
	}
	run := LatestNotification(task)
	if run == nil {
		return out
	}
	s.manuallyUnread[unreadGuard{machine: machineID, task: task.ID, run: run.ID}] = true
	if s.detail != nil && s.detail.MachineID == machineID && s.detail.TaskID == task.ID && run.SessionID != "" {
		s.unreadReturnSession = run.SessionID
	}
	if m := s.seen[machineID]; m != nil {
		tok := SeenToken{TaskID: task.ID, RunID: run.ID}
		if m[tok] {
			delete(m, tok)
			out.SaveMachines = append(out.SaveMachines, machineID)
		}
	}
	if pm := s.pendingReadInMemory[machineID]; pm != nil {
		delete(pm, SeenToken{TaskID: task.ID, RunID: run.ID})
	}
	return out
}

// Forget drops in-memory state for a removed or unsupported machine.
func (s *SeenTracker) Forget(machineID string) {
	delete(s.loaded, machineID)
	delete(s.verified, machineID)
	delete(s.seenLoaded, machineID)
	delete(s.seen, machineID)
	delete(s.pendingReadInMemory, machineID)
	for g := range s.manuallyUnread {
		if g.machine == machineID {
			delete(s.manuallyUnread, g)
		}
	}
	for k := range s.dismissed {
		if k.machine == machineID {
			delete(s.dismissed, k)
		}
	}
}

func (s *SeenTracker) maybeDismiss(machineID, taskID string, r *Run, out *SeenOutcome) {
	if r == nil || r.SessionID == "" || r.NotificationSeq == 0 {
		return
	}
	dk := dismissKey{
		machine: machineID,
		task:    taskID,
		run:     r.ID,
		session: r.SessionID,
		seq:     r.NotificationSeq,
	}
	if s.dismissed[dk] {
		return
	}
	s.dismissed[dk] = true
	out.Dismiss = append(out.Dismiss, NotificationRef{
		MachineID:       machineID,
		TaskID:          taskID,
		RunID:           r.ID,
		SessionID:       r.SessionID,
		NotificationSeq: r.NotificationSeq,
	})
}

func (s *SeenTracker) markViewed(machineID string, out *SeenOutcome) {
	detailVerified := s.verified[machineID]
	var visibleTasks []*Task
	for _, t := range s.loaded[machineID] {
		if Visible(t) {
			visibleTasks = append(visibleTasks, t)
		}
	}
	if detailVerified && !s.entryCaptured && s.detail != nil && s.detail.MachineID == machineID {
		for _, t := range visibleTasks {
			if t.ID == s.detail.TaskID {
				s.entryUnread = s.UnreadIDs(machineID, t)
				s.entryCaptured = true
				break
			}
		}
	}

	var toRead []struct {
		taskID string
		run    *Run
	}
	for _, t := range visibleTasks {
		onDetail := detailVerified && s.detail != nil && s.detail.MachineID == machineID && s.detail.TaskID == t.ID
		for _, r := range AllRuns(t) {
			if !r.Notified || r.ID == "" {
				continue
			}
			if s.manuallyUnread[unreadGuard{machine: machineID, task: t.ID, run: r.ID}] {
				continue
			}
			onSession := s.visibleSession != "" && r.SessionID == s.visibleSession
			if onDetail || onSession {
				toRead = append(toRead, struct {
					taskID string
					run    *Run
				}{taskID: t.ID, run: r})
			}
		}
	}
	if len(toRead) == 0 {
		return
	}

	changed := false
	for _, item := range toRead {
		tok := SeenToken{TaskID: item.taskID, RunID: item.run.ID}
		if s.seen[machineID] == nil {
			if s.pendingReadInMemory[machineID] == nil {
				s.pendingReadInMemory[machineID] = make(map[SeenToken]bool)
			}
			s.pendingReadInMemory[machineID][tok] = true
		} else if !s.seen[machineID][tok] {
			s.seen[machineID][tok] = true
			changed = true
		}
		s.maybeDismiss(machineID, item.taskID, item.run, out)
	}
	if changed && !slices.Contains(out.SaveMachines, machineID) {
		out.SaveMachines = append(out.SaveMachines, machineID)
	}
}
