package sync

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/projection"
	"github.com/Lewin671/remote-agent/clientcore/screens"
)

// SessionSnapshot gathers the current sync state and zero-copy shown events for sessionID.
func (e *Engine) SessionSnapshot(sessionID string) screens.SessionViewInput {
	out := screens.SessionViewInput{
		SessionsListed:     e.sessionsListed,
		LiveLoaded:         e.liveLoaded,
		Removed:            e.removed,
		AccountControlLost: e.accountControlLost(),
		AccountClockSkewed: e.accountClockSkewed(),
		Conn:               e.conn,
		Unlinked:           e.worldUnlinked(),
		ProtocolVersion:    ProtocolVersion,
		DeviceNames:        makeDeviceNameMap(e.devices),
		ThisDeviceID:       e.security.SelfDeviceID,
	}
	for _, id := range e.sessionByIDOps {
		if id == sessionID {
			out.LookingUp = true
			break
		}
	}

	s := e.sessions[sessionID]
	buf := e.buffers[sessionID]
	if s == nil && buf == nil {
		return out
	}
	out.Found = true

	var liveSnap *v1.LiveSnapshot
	var agent v1.Agent
	if s != nil {
		out.LastSeq = s.LastSeq
		out.Retired = s.RetiredHistory
		out.Session = &lists.Session{
			ID:             s.ID,
			MachineID:      s.MachineID,
			Info:           s.Meta,
			UpdatedMs:      s.UpdatedMs,
			ListedEndedMs:  s.ListedEndedMs,
			Pending:        s.LivePending,
			ScheduleName:   s.ScheduleName,
			ScheduleUnread: s.ScheduleUnread,
		}
		if s.MachineID != "" && e.directReady[s.MachineID] {
			out.Unlinked = false
		}
		if s.Meta != nil {
			agent = s.Meta.Agent
		}
		if m := e.machines[s.MachineID]; m != nil {
			reachable := e.machineReachable(m)
			out.Machine = &lists.Machine{
				ID:         m.ID,
				Online:     m.Online,
				Reachable:  &reachable,
				LastSeenMs: m.LastSeenMs,
				Meta:       m.Meta,
			}
		}
		if s.Checkpoint != nil {
			liveSnap = s.Checkpoint
			out.Checkpoint = s.Checkpoint
		}
	}

	if buf != nil {
		out.Loading = buf.awaitingCache || buf.fetching
		out.Loaded = buf.loaded
		out.AwaitingCache = buf.awaitingCache
		out.Backfilling = buf.backfilling
		out.Fetching = buf.fetching
		out.HasGap = (s != nil && buf.hasServerGap(s.ServerLastSeq)) || (s == nil && len(buf.pending) > 0)
		out.TailRequested = buf.tailRequested
		out.Retired = out.Retired || buf.retired
		out.RetryingCorruptCache = buf.retryingCorruptCache
		out.Error = buf.errorText
		out.FetchError = buf.fetchErrorText
		out.PrefixLen = uint64(len(buf.prefix))
		out.PendingCount = int32(len(buf.pending))
		out.UnavailableCount = uint32(len(buf.unavailableSeqs))

		if len(buf.prefix) > 0 {
			if buf.reducer == nil {
				buf.rebuildReducer(agent)
			}
			if liveSnap != nil {
				out.Projection = buf.reducer.CurrentActions(liveSnap)
			} else {
				out.Projection = buf.reducer.Snapshot()
			}
		} else if liveSnap != nil {
			out.Projection = projection.CurrentActions(nil, liveSnap, nil)
		}

		out.Events = buf.ShownEvents()
	} else if liveSnap != nil {
		out.Projection = projection.CurrentActions(nil, liveSnap, nil)
	}

	return out
}

// LookupSessionIfMissing triggers a one-shot FetchSessionById RPC when sessionID is not loaded yet.
func (e *Engine) LookupSessionIfMissing(ctx *StepContext, sessionID string) {
	if sessionID == "" || e.sessions[sessionID] != nil || !e.running {
		return
	}
	for _, id := range e.sessionByIDOps {
		if id == sessionID {
			return
		}
	}
	e.fetchSessionByID(ctx, sessionID)
}

// RetrySessionHistory retries loading/catching up session events after an error or gap.
func (e *Engine) RetrySessionHistory(ctx *StepContext, sessionID string) {
	e.retryEvents(ctx, sessionID)
}

func makeDeviceNameMap(devices map[string]*DeviceEntry) map[string]string {
	if len(devices) == 0 {
		return nil
	}
	out := make(map[string]string, len(devices))
	for id, d := range devices {
		if d != nil && d.Meta != nil && d.Meta.Name != "" {
			out[id] = d.Meta.Name
		}
	}
	return out
}
