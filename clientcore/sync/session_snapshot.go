package sync

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/projection"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

// SessionSnapshot gathers the current sync state and zero-copy shown events for sessionID.
func (e *Engine) SessionSnapshot(sessionID string) world.SessionSnapshot {
	out := world.SessionSnapshot{
		SessionsListed:      e.w.World.SessionsListed,
		LiveLoaded:          e.w.World.LiveLoaded,
		Removed:             e.removed,
		AccountControlLost:  e.accountControlLost(),
		AccountClockSkewed:  e.accountClockSkewed(),
		Conn:                e.w.World.Connection,
		Unlinked:            e.worldUnlinked(),
		ReachabilityUnknown: e.coldStartGraceActive(),
		ProtocolVersion:     PeerVersion,
		DeviceNames:         makeDeviceNameMap(e.devices),
		ThisDeviceID:        e.security.SelfDeviceID,
	}
	for _, id := range e.sessionByIDOps {
		if id == sessionID {
			out.LookingUp = true
			break
		}
	}
	if !out.LookingUp && e.sessionByIDFailed[sessionID] {
		out.LookupFailed = true
		if errReason := e.sessionByIDErrors[sessionID]; !errReason.IsZero() {
			out.LookupError = errReason
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
		out.Session = &s.Session
		if s.MachineID != "" && e.directReady[s.MachineID] {
			out.Unlinked = false
			out.ReachabilityUnknown = false
		}
		if s.Info != nil {
			agent = s.Info.Agent
		}
		if m := e.machines[s.MachineID]; m != nil {
			out.Machine = &m.Machine
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
		out.Error = buf.errorReason
		out.FetchError = buf.fetchErrorReason
		out.PrefixLen = uint64(len(buf.prefix))
		out.PendingCount = int32(len(buf.pending))
		out.UnavailableCount = uint32(len(buf.unavailableSeqs))
		out.EventRev = buf.rev

		if len(buf.prefix) > 0 {
			if buf.reducer == nil {
				buf.rebuildReducer(agent)
			}
			if buf.cachedSnap == nil {
				buf.cachedSnap = buf.reducer.Snapshot()
			}
			if liveSnap != nil {
				out.Projection = projection.CurrentActions(buf.cachedSnap, liveSnap, buf.prefix)
			} else {
				out.Projection = buf.cachedSnap
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
