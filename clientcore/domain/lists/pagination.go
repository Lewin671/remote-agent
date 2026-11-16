package lists

// AfterPlace reports whether an ended session with (endedMs, sessionID) comes after cursor place in server listing order.
// A session with endedMs == 0 (not ended) is never past the cursor.
func AfterPlace(endedMs int64, sessionID string, place *SessionPlace) bool {
	if place == nil || endedMs == 0 {
		return false
	}
	return endedMs < place.EndedMs || (endedMs == place.EndedMs && compareUTF16(sessionID, place.SessionID) > 0)
}

// PastFetched reports whether session s lies past the pages fetched so far.
func PastFetched(w *World, s *Session) bool {
	if w == nil || s == nil || w.SessionsNext == nil {
		return false
	}
	ms := s.ListedEndedMs
	if ms == 0 && Exited(s) {
		ms = EndedMs(s)
	}
	return AfterPlace(ms, s.ID, w.SessionsNext)
}

// NeedsEndedPage reports whether displaying want ended sessions (with total known) requires fetching another page.
func NeedsEndedPage(w *World, held []*Session, want, total int) bool {
	if !MoreSessions(w) {
		return false
	}
	if len(held) < min(want, total) {
		return true
	}
	limit := min(max(0, want), len(held))
	for i := 0; i < limit; i++ {
		if PastFetched(w, held[i]) {
			return true
		}
	}
	return false
}

// ReconcileSessionEntry describes a locally held session when reconciling against a fresh first page.
type ReconcileSessionEntry struct {
	ID            string
	MachineID     string
	ListedEndedMs int64
}

// ReconcileEndedSessions determines which stale held sessions should be removed vs unlisted.
func ReconcileEndedSessions(
	w *World,
	reconcile []ReconcileSessionEntry,
	pageIDs, pushedIDs, knownMachineIDs []string,
	pageNext *SessionPlace,
) (removeIDs, unlistIDs []string) {
	pageSet := make(map[string]bool, len(pageIDs))
	for _, id := range pageIDs {
		pageSet[id] = true
	}
	pushedSet := make(map[string]bool, len(pushedIDs))
	for _, id := range pushedIDs {
		pushedSet[id] = true
	}
	machineSet := make(map[string]bool, len(knownMachineIDs))
	for _, id := range knownMachineIDs {
		machineSet[id] = true
	}
	if w != nil {
		for _, m := range w.Machines {
			if m != nil && m.ID != "" {
				machineSet[m.ID] = true
			}
		}
	}
	for _, s := range reconcile {
		if pageSet[s.ID] || pushedSet[s.ID] {
			continue
		}
		hasMachine := machineSet[s.MachineID]
		if pageNext != nil && hasMachine && AfterPlace(s.ListedEndedMs, s.ID, pageNext) {
			continue
		}
		if pageNext == nil || !hasMachine {
			removeIDs = append(removeIDs, s.ID)
		} else {
			unlistIDs = append(unlistIDs, s.ID)
		}
	}
	return removeIDs, unlistIDs
}

