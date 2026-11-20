package sync

import (
	"slices"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/projection"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

type eventFetchPhase uint8

const (
	eventFetchNone eventFetchPhase = iota
	eventFetchNewer
	eventFetchTail
	eventFetchPrefix
)

// EventBuffer holds the contiguous prefix + out-of-order pending events and reducer state for one session.
type EventBuffer struct {
	SessionID        string
	prefix           []*v1.Event
	pending          map[uint64]*v1.Event
	verifiedSeqs     map[uint64]bool
	unavailableSeqs  map[uint64]bool
	approxBytesBySeq map[uint64]int64
	approxBytes      int64
	usedAtMs         int64

	errorText            *corev1.Text
	fetchErrorText       *corev1.Text
	retired              bool
	tailRequested        bool
	awaitingCache        bool
	backfilling          bool
	fetching             bool
	refetch              bool
	loaded               bool
	retryingCorruptCache bool

	activeCacheOpID uint64
	activeFetchOpID uint64
	cacheFirstSeq   uint64
	cacheAfterSeq   uint64
	fetchGen        uint64
	fetchPhase      eventFetchPhase
	fetchCursor     uint64
	endSeq          *uint64

	reducer    *projection.Reducer
	rev        uint64
	cachedSnap *corev1.ProjectionState
}

func newEventBuffer(sid string, agent v1.Agent, monoMs int64) *EventBuffer {
	return &EventBuffer{
		SessionID:        sid,
		pending:          make(map[uint64]*v1.Event),
		verifiedSeqs:     make(map[uint64]bool),
		unavailableSeqs:  make(map[uint64]bool),
		approxBytesBySeq: make(map[uint64]int64),
		usedAtMs:         monoMs,
		reducer:          projection.NewReducer(agent),
	}
}

func (b *EventBuffer) resetContent(agent v1.Agent) {
	b.prefix = nil
	clear(b.pending)
	clear(b.verifiedSeqs)
	clear(b.unavailableSeqs)
	clear(b.approxBytesBySeq)
	b.approxBytes = 0
	b.tailRequested = false
	b.errorText = nil
	b.fetchErrorText = nil
	b.loaded = false
	b.reducer = projection.NewReducer(agent)
	b.cachedSnap = nil
}

func (b *EventBuffer) rebuildReducer(agent v1.Agent) {
	b.reducer = projection.NewReducer(agent)
	if len(b.prefix) > 0 {
		b.reducer.Append(b.prefix)
	}
	b.cachedSnap = nil
}

func (b *EventBuffer) hasSeq(seq uint64) bool {
	if seq == 0 {
		return false
	}
	if seq <= uint64(len(b.prefix)) {
		return true
	}
	_, ok := b.pending[seq]
	return ok
}

func (b *EventBuffer) highestHeldSeq() uint64 {
	best := uint64(len(b.prefix))
	for seq := range b.pending {
		if seq > best {
			best = seq
		}
	}
	return best
}

func (b *EventBuffer) hasServerGap(serverLastSeq uint64) bool {
	if b == nil {
		return false
	}
	if uint64(len(b.prefix)) < serverLastSeq {
		return true
	}
	for seq := range b.pending {
		if seq <= serverLastSeq {
			return true
		}
	}
	return false
}

// PrefixLen returns the contiguous verified/placeholder prefix length for session.
func (b *EventBuffer) PrefixLen() uint64 {
	if b == nil {
		return 0
	}
	return uint64(len(b.prefix))
}

// PendingCount returns the number of buffered out-of-order events beyond the contiguous prefix.
func (b *EventBuffer) PendingCount() int {
	if b == nil {
		return 0
	}
	return len(b.pending)
}

// IsLoaded reports whether the buffer has the full prefix with no gaps or errors.
func (b *EventBuffer) IsLoaded() bool {
	return b != nil && b.loaded
}

// IsRetired reports whether records were retired across recovery.
func (b *EventBuffer) IsRetired() bool {
	return b != nil && b.retired
}

// HasPending reports whether seq exists in either prefix or pending map.
func (b *EventBuffer) HasSeq(seq uint64) bool {
	return b != nil && b.hasSeq(seq)
}

// ShownEvents returns the events currently displayable for this session (the contiguous tail during tail-first fetch, or the contiguous prefix).
func (b *EventBuffer) ShownEvents() []*v1.Event {
	if b == nil {
		return nil
	}
	if !b.tailRequested || len(b.pending) == 0 {
		return b.prefix
	}
	var maxSeq uint64
	for seq := range b.pending {
		if seq > maxSeq {
			maxSeq = seq
		}
	}
	if maxSeq == 0 {
		return b.prefix
	}
	start := maxSeq
	for start > 1 {
		if _, ok := b.pending[start-1]; !ok {
			break
		}
		start--
	}
	out := make([]*v1.Event, 0, int(maxSeq-start+1))
	for seq := start; seq <= maxSeq; seq++ {
		if ev := b.pending[seq]; ev != nil {
			out = append(out, ev)
		}
	}
	return out
}

// Buffer returns the active EventBuffer for sid, if cached in memory.
func (e *Engine) Buffer(sid string) *EventBuffer {
	return e.buffers[sid]
}

func (e *Engine) handleSessionWatch(ctx *StepContext, ev *corev1.SessionWatchEvent) {
	if ev == nil || ev.SessionId == "" {
		return
	}
	sid := ev.SessionId
	if ev.Watched {
		e.watchedSessions[sid] = true
		if b := e.buffers[sid]; b != nil {
			b.usedAtMs = e.monoMs
			if !b.fetching && !b.awaitingCache && e.running && e.conn == lists.ConnConnected {
				e.startFetchEvents(ctx, sid)
			}
		} else if e.sessions[sid] != nil {
			e.startLoadEvents(ctx, sid)
		}
	} else {
		delete(e.watchedSessions, sid)
		if b := e.buffers[sid]; b != nil {
			b.usedAtMs = e.monoMs
		}
		e.trimHistory(ctx)
	}
	e.updateLivePollers(ctx)
	ctx.Dirty.MarkSessionData()
}

func (e *Engine) handleSessionPin(ctx *StepContext, ev *corev1.SessionPinEvent) {
	if ev == nil || ev.SessionId == "" {
		return
	}
	if ev.Pinned {
		e.pinnedSessions[ev.SessionId] = true
		if b := e.buffers[ev.SessionId]; b != nil {
			b.usedAtMs = e.monoMs
		}
	} else {
		delete(e.pinnedSessions, ev.SessionId)
		e.trimHistory(ctx)
	}
}

func (e *Engine) retryEvents(ctx *StepContext, sid string) {
	if sid == "" || e.sessions[sid] == nil {
		return
	}
	b := e.buffers[sid]
	if b == nil {
		e.startLoadEvents(ctx, sid)
		return
	}
	b.fetchErrorText = nil
	if !b.retired {
		b.errorText = nil
	}
	e.startFetchEvents(ctx, sid)
	ctx.Dirty.MarkSessionData()
}

func (e *Engine) bumpRev(b *EventBuffer) {
	e.nextEventRev++
	if b != nil {
		b.rev = e.nextEventRev
	}
}

func (e *Engine) markSessionRetired(sid string) {
	b := e.buffers[sid]
	if b == nil {
		return
	}
	b.retired = true
	b.errorText = text.Msg(text.KeyErrorsHistoryKeysUnavailableRecordsFromBeforeRecoveryCannotBe)
	e.bumpRev(b)
}

func (e *Engine) startLoadEvents(ctx *StepContext, sid string) {
	s := e.sessions[sid]
	if s == nil {
		return
	}
	if b := e.buffers[sid]; b != nil {
		return
	}
	var agent v1.Agent
	if s.Meta != nil {
		agent = s.Meta.Agent
	}
	b := newEventBuffer(sid, agent, e.monoMs)
	e.bumpRev(b)
	if s.RetiredHistory {
		b.retired = true
		b.errorText = text.Msg(text.KeyErrorsHistoryKeysUnavailableRecordsFromBeforeRecoveryCannotBe)
		e.buffers[sid] = b
		ctx.Dirty.MarkSessionData()
		return
	}
	e.buffers[sid] = b
	b.awaitingCache = true
	b.activeCacheOpID = e.allocOpID()
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_CacheRead{
			CacheRead: &corev1.CacheReadEffect{
				Kind: &corev1.CacheReadEffect_Events{
					Events: &corev1.CacheReadEvents{
						OpId:      b.activeCacheOpID,
						SessionId: sid,
						Newest:    true,
						Limit:     CacheEventPageSize,
					},
				},
			},
		},
	})
	e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_HISTORY_SWEEP, "", HistorySweepIntervalMs)
	ctx.Dirty.MarkSessionData()
}

func (e *Engine) handleCacheEventsLoaded(ctx *StepContext, ev *corev1.CacheEventsLoadedEvent) {
	if ev == nil || ev.SessionId == "" {
		return
	}
	b := e.buffers[ev.SessionId]
	if b == nil || ev.OpId != b.activeCacheOpID {
		return
	}
	b.activeCacheOpID = 0
	s := e.sessions[ev.SessionId]
	var agent v1.Agent
	if s != nil && s.Meta != nil {
		agent = s.Meta.Agent
	}

	if ev.Retired {
		b.retired = true
		b.awaitingCache = false
		b.backfilling = false
		b.errorText = text.Msg(text.KeyErrorsHistoryKeysUnavailableRecordsFromBeforeRecoveryCannotBe)
		e.bumpRev(b)
		if s != nil {
			s.RetiredHistory = true
		}
		ctx.Dirty.MarkSessionData()
		return
	}

	if ev.Broken || ev.Error != "" {
		e.dropCorruptCacheAndRefetch(ctx, ev.SessionId, b, agent)
		return
	}

	wasBackfilling := b.backfilling
	ok := e.ingestEvents(ctx, ev.SessionId, b, ev.Events, false, false, true, !wasBackfilling)
	if b.retired {
		b.awaitingCache = false
		b.backfilling = false
		e.bumpRev(b)
		ctx.Dirty.MarkSessionData()
		return
	}
	if !ok {
		e.dropCorruptCacheAndRefetch(ctx, ev.SessionId, b, agent)
		return
	}

	if !wasBackfilling {
		b.awaitingCache = false
		if len(ev.Events) >= int(CacheEventPageSize) && ev.Events[0] != nil && ev.Events[0].Seq > 1 {
			b.tailRequested = true
			b.backfilling = true
			b.cacheFirstSeq = ev.Events[0].Seq
			b.cacheAfterSeq = 0
			e.requestNextCacheBackfill(ctx, ev.SessionId, b)
			if e.running && e.conn == lists.ConnConnected {
				e.startFetchEvents(ctx, ev.SessionId)
			}
			ctx.Dirty.MarkSessionData()
			return
		}
		if e.running && e.conn == lists.ConnConnected {
			e.startFetchEvents(ctx, ev.SessionId)
		}
		e.trimHistory(ctx)
		ctx.Dirty.MarkSessionData()
		return
	}

	var lastSeq uint64
	if len(ev.Events) > 0 && ev.Events[len(ev.Events)-1] != nil {
		lastSeq = ev.Events[len(ev.Events)-1].Seq
	}
	if len(ev.Events) < int(CacheEventPageSize) || lastSeq <= b.cacheAfterSeq || lastSeq+1 >= b.cacheFirstSeq {
		b.backfilling = false
		b.awaitingCache = false
		if e.running && e.conn == lists.ConnConnected {
			e.startFetchEvents(ctx, ev.SessionId)
		}
		e.trimHistory(ctx)
		if s != nil {
			e.refreshSessionPending(s)
		}
		ctx.Dirty.Add(true, true, false)
		ctx.Dirty.MarkSessionData()
		return
	}
	b.cacheAfterSeq = lastSeq
	e.requestNextCacheBackfill(ctx, ev.SessionId, b)
	// Keep intermediate backfill pages silent while tailRequested keeps tail visible.
	if !b.tailRequested {
		if s != nil {
			e.refreshSessionPending(s)
		}
		ctx.Dirty.Add(true, true, false)
		ctx.Dirty.MarkSessionData()
	}
}

func (e *Engine) requestNextCacheBackfill(ctx *StepContext, sid string, b *EventBuffer) {
	b.activeCacheOpID = e.allocOpID()
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_CacheRead{
			CacheRead: &corev1.CacheReadEffect{
				Kind: &corev1.CacheReadEffect_Events{
					Events: &corev1.CacheReadEvents{
						OpId:      b.activeCacheOpID,
						SessionId: sid,
						Newest:    false,
						AfterSeq:  b.cacheAfterSeq,
						Limit:     CacheEventPageSize,
					},
				},
			},
		},
	})
}

func (e *Engine) dropCorruptCacheAndRefetch(ctx *StepContext, sid string, b *EventBuffer, agent v1.Agent) {
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_CacheDelete{
			CacheDelete: &corev1.CacheDeleteEffect{
				Kind: &corev1.CacheDeleteEffect_DropEventsSessionId{DropEventsSessionId: sid},
			},
		},
	})
	b.resetContent(agent)
	e.bumpRev(b)
	b.retryingCorruptCache = true
	b.awaitingCache = false
	b.backfilling = false
	if e.running && e.conn == lists.ConnConnected {
		e.startFetchEvents(ctx, sid)
	}
	ctx.Dirty.MarkSessionData()
}

func (e *Engine) startFetchEvents(ctx *StepContext, sid string) {
	b := e.buffers[sid]
	s := e.sessions[sid]
	if b == nil || s == nil || !e.running || e.conn != lists.ConnConnected {
		return
	}
	if b.retired {
		return
	}
	if b.fetching || b.awaitingCache {
		b.refetch = true
		return
	}
	b.fetching = true
	b.refetch = false
	b.fetchGen = e.lifecycleGen
	b.endSeq = nil
	b.fetchErrorText = nil

	if b.backfilling {
		latest := s.ServerLastSeq
		held := b.highestHeldSeq()
		if latest <= held {
			b.fetching = false
			return
		}
		cursor := held
		if latest > held+uint64(EventsPageLimit) {
			cursor = latest - uint64(EventsPageLimit)
			b.tailRequested = true
		}
		b.fetchPhase = eventFetchNewer
		b.fetchCursor = cursor
		e.emitGetEvents(ctx, sid, b, cursor)
		return
	}

	latest := s.ServerLastSeq
	prefix := uint64(len(b.prefix))
	if latest > prefix+uint64(EventsPageLimit) && b.errorText == nil {
		after := latest - uint64(EventsPageLimit)
		if !allTailHeld(b, after, uint64(EventsPageLimit)) {
			b.tailRequested = true
			b.fetchPhase = eventFetchTail
			b.fetchCursor = after
			e.emitGetEvents(ctx, sid, b, after)
			return
		}
	}

	b.fetchPhase = eventFetchPrefix
	b.fetchCursor = prefix
	e.emitGetEvents(ctx, sid, b, prefix)
}

func allTailHeld(b *EventBuffer, after, pageSize uint64) bool {
	prefix := uint64(len(b.prefix))
	for n := uint64(1); n <= pageSize; n++ {
		seq := after + n
		if seq <= prefix {
			continue
		}
		if _, ok := b.pending[seq]; !ok {
			return false
		}
	}
	return true
}

func (e *Engine) emitGetEvents(ctx *StepContext, sid string, b *EventBuffer, after uint64) {
	b.activeFetchOpID = e.allocOpID()
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_Rpc{
			Rpc: &corev1.RpcEffect{
				OpId: b.activeFetchOpID,
				Kind: &corev1.RpcEffect_GetEvents{
					GetEvents: &corev1.GetEventsRpc{
						SessionId: sid,
						AfterSeq:  after,
						Limit:     EventsPageLimit,
					},
				},
			},
		},
	})
}

func (e *Engine) handleEventsPageFetched(ctx *StepContext, ev *corev1.EventsPageFetchedEvent) {
	if ev == nil || ev.SessionId == "" {
		return
	}
	b := e.buffers[ev.SessionId]
	s := e.sessions[ev.SessionId]
	if b == nil || !b.fetching || ev.OpId != b.activeFetchOpID {
		return
	}
	b.activeFetchOpID = 0
	if !e.running || b.fetchGen != e.lifecycleGen || s == nil {
		b.fetching = false
		return
	}
	if ev.Error != "" {
		b.fetching = false
		b.retryingCorruptCache = false
		b.fetchErrorText = text.Msg(text.KeyErrorsHistoryCouldNotContinueLoadingRetry)
		if b.refetch {
			e.startFetchEvents(ctx, ev.SessionId)
		}
		ctx.Dirty.MarkSessionData()
		return
	}

	wasTailPrefix := b.fetchPhase == eventFetchPrefix && b.tailRequested
	prevPrefix := len(b.prefix)
	e.ingestEvents(ctx, ev.SessionId, b, ev.Events, false, true, true, !wasTailPrefix)
	if !wasTailPrefix || !b.tailRequested || b.errorText != nil {
		if wasTailPrefix {
			e.refreshSessionPending(s)
			ctx.Dirty.Add(true, true, false)
		}
		ctx.Dirty.MarkSessionData()
	}
	var lastSeq uint64
	if len(ev.Events) > 0 && ev.Events[len(ev.Events)-1] != nil {
		lastSeq = ev.Events[len(ev.Events)-1].Seq
	}
	if !ev.More {
		end := lastSeq
		if end == 0 {
			end = b.fetchCursor
		}
		b.endSeq = &end
	}

	switch b.fetchPhase {
	case eventFetchNewer:
		if ev.More && b.errorText == nil && lastSeq > b.fetchCursor {
			b.fetchCursor = lastSeq
			e.emitGetEvents(ctx, ev.SessionId, b, lastSeq)
			return
		}
		b.fetching = false
		if b.refetch {
			e.startFetchEvents(ctx, ev.SessionId)
		}
		return

	case eventFetchTail:
		if ev.More && b.errorText == nil && lastSeq > b.fetchCursor {
			b.fetchCursor = lastSeq
			e.emitGetEvents(ctx, ev.SessionId, b, lastSeq)
			return
		}
		b.fetchPhase = eventFetchPrefix
		before := uint64(len(b.prefix))
		if b.endSeq != nil && before >= *b.endSeq && len(b.pending) == 0 {
			e.finishFetchCycle(ctx, ev.SessionId, b, s)
			return
		}
		b.fetchCursor = before
		e.emitGetEvents(ctx, ev.SessionId, b, before)
		return

	case eventFetchPrefix:
		before := uint64(len(b.prefix))
		grew := len(b.prefix) > prevPrefix
		doneEnd := b.endSeq != nil && before >= *b.endSeq && len(b.pending) == 0
		if !ev.More || !grew || doneEnd {
			e.finishFetchCycle(ctx, ev.SessionId, b, s)
			return
		}
		b.fetchCursor = before
		e.emitGetEvents(ctx, ev.SessionId, b, before)
	}
}

func (e *Engine) finishFetchCycle(ctx *StepContext, sid string, b *EventBuffer, s *SessionEntry) {
	b.fetching = false
	b.fetchPhase = eventFetchNone
	b.retryingCorruptCache = false
	missing := b.hasServerGap(s.ServerLastSeq)
	b.loaded = !missing && b.errorText == nil
	if missing && b.errorText == nil {
		b.fetchErrorText = text.Msg(text.KeyErrorsHistoryHasAGapRetry)
	} else if !missing {
		b.fetchErrorText = nil
	}
	ctx.Dirty.MarkSessionData()
	if b.refetch && e.running && e.conn == lists.ConnConnected {
		e.startFetchEvents(ctx, sid)
	}
}

func (e *Engine) handleEventsAppended(ctx *StepContext, frame *corev1.EventsAppendedFrame) {
	sid := frame.SessionId
	b := e.buffers[sid]
	if b == nil {
		return
	}
	e.ingestEvents(ctx, sid, b, frame.Events, true, true, true, true)
	ctx.Dirty.MarkSessionData()
}

func (e *Engine) handleDirectEvents(ctx *StepContext, ev *corev1.DirectEventsReceivedEvent) {
	if ev == nil || ev.SessionId == "" || ev.MachineId == "" || !e.running || !e.foreground {
		return
	}
	s := e.sessions[ev.SessionId]
	if s == nil || s.MachineID != ev.MachineId {
		return
	}
	b := e.buffers[ev.SessionId]
	if b == nil || b.tailRequested {
		return
	}
	if e.security.Loaded && !e.security.TrustedMachines[ev.MachineId] {
		return
	}
	expected := uint64(len(b.prefix)) + 1
	var clean []*corev1.PlainEvent
	for _, pe := range ev.Events {
		if pe == nil || pe.Seq == 0 || pe.Broken || pe.Retired || pe.Unavailable || pe.Seq != expected {
			continue
		}
		if pe.CacheRef != "" {
			pe = &corev1.PlainEvent{
				Seq:         pe.Seq,
				Event:       pe.Event,
				Verified:    pe.Verified,
				ApproxBytes: pe.ApproxBytes,
			}
		}
		clean = append(clean, pe)
		expected++
	}
	if len(clean) == 0 {
		return
	}
	e.ingestEvents(ctx, ev.SessionId, b, clean, false, false, false, true)
	ctx.Dirty.MarkSessionData()
}

func (e *Engine) ingestEvents(
	ctx *StepContext,
	sid string,
	b *EventBuffer,
	items []*corev1.PlainEvent,
	fillGaps bool,
	save bool,
	fromServer bool,
	notifyDirty bool,
) bool {
	s := e.sessions[sid]
	if s == nil {
		return true
	}
	b.usedAtMs = e.monoMs
	var cacheRefs []string
	var maxSeq uint64
	advancedPrefix := false
	changedEvents := false
	ok := true

	for _, pe := range items {
		if pe == nil || pe.Seq == 0 {
			continue
		}
		if pe.Retired {
			if pe.Seq > maxSeq {
				maxSeq = pe.Seq
			}
			b.retired = true
			s.RetiredHistory = true
			b.errorText = text.Msg(text.KeyErrorsHistoryKeysUnavailableRecordsFromBeforeRecoveryCannotBe)
			changedEvents = true
			ok = false
			break
		}
		if pe.Broken {
			if pe.Seq > maxSeq {
				maxSeq = pe.Seq
			}
			b.errorText = text.Msg(text.KeyErrorsInvalidCiphertextSignatureKeyOrContentVerificationFailed)
			changedEvents = true
			ok = false
			break
		}

		expected := uint64(len(b.prefix)) + 1
		if pe.Seq > maxSeq {
			maxSeq = pe.Seq
		}

		ev := pe.Event
		if ev == nil {
			ev = &v1.Event{Seq: pe.Seq}
		} else if ev.Seq == 0 {
			ev = ev.Clone()
			ev.Seq = pe.Seq
		}

		if pe.Verified {
			b.verifiedSeqs[pe.Seq] = true
			if b.unavailableSeqs[pe.Seq] {
				delete(b.unavailableSeqs, pe.Seq)
				changedEvents = true
			}
		} else if pe.Unavailable {
			if !b.unavailableSeqs[pe.Seq] {
				b.unavailableSeqs[pe.Seq] = true
				changedEvents = true
			}
		}

		b.addApproxBytes(pe.Seq, pe.ApproxBytes)

		switch {
		case pe.Seq == expected:
			b.prefix = append(b.prefix, ev)
			if b.reducer != nil {
				b.reducer.Append([]*v1.Event{ev})
				b.cachedSnap = nil
			}
			advancedPrefix = true
			changedEvents = true
			for {
				next := uint64(len(b.prefix)) + 1
				nxtEv, found := b.pending[next]
				if !found {
					break
				}
				delete(b.pending, next)
				b.prefix = append(b.prefix, nxtEv)
				if b.reducer != nil {
					b.reducer.Append([]*v1.Event{nxtEv})
					b.cachedSnap = nil
				}
			}
			if len(b.pending) == 0 {
				// The prefix has caught up with the tail it was showing; a later
				// out-of-order live event must not switch the display back to it.
				b.tailRequested = false
			}
		case pe.Seq > expected:
			if _, exists := b.pending[pe.Seq]; !exists {
				b.pending[pe.Seq] = ev
				changedEvents = true
			}
		default:
			// seq <= len(b.prefix): keep existing verified event; replace skeleton/unavailable placeholder if incoming verified.
			idx := int(pe.Seq - 1)
			if pe.Verified && idx < len(b.prefix) && projection.IsSkeleton(b.prefix[idx]) && !projection.IsSkeleton(ev) {
				b.prefix[idx] = ev
				var agent v1.Agent
				if s.Meta != nil {
					agent = s.Meta.Agent
				}
				b.rebuildReducer(agent)
				advancedPrefix = true
				changedEvents = true
			}
		}

		if save && pe.CacheRef != "" && (pe.Verified || pe.Unavailable) {
			cacheRefs = append(cacheRefs, pe.CacheRef)
		}
	}

	if changedEvents {
		e.bumpRev(b)
	}
	if fromServer && maxSeq > s.ServerLastSeq {
		s.ServerLastSeq = maxSeq
	}
	if maxSeq > s.LastSeq {
		s.LastSeq = maxSeq
	}
	if b.fetchErrorText != nil && !b.fetching && !b.awaitingCache && !b.backfilling && !b.hasServerGap(s.ServerLastSeq) {
		b.fetchErrorText = nil
		if b.errorText == nil {
			b.loaded = true
		}
	}
	if len(cacheRefs) > 0 {
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_CacheWrite{
				CacheWrite: &corev1.CacheWriteEffect{
					Kind: &corev1.CacheWriteEffect_WriteEvents{
						WriteEvents: &corev1.CacheWriteEvents{
							SessionId: sid,
							CacheRefs: cacheRefs,
						},
					},
				},
			},
		})
	}
	if advancedPrefix && notifyDirty {
		e.refreshSessionPending(s)
		ctx.Dirty.Add(true, true, false)
	}
	if fillGaps && b.hasServerGap(s.ServerLastSeq) {
		e.startFetchEvents(ctx, sid)
	}
	return ok
}

func (b *EventBuffer) addApproxBytes(seq uint64, bytes int64) {
	if bytes <= 0 {
		return
	}
	if prev, ok := b.approxBytesBySeq[seq]; ok {
		b.approxBytes -= prev
	}
	b.approxBytesBySeq[seq] = bytes
	b.approxBytes += bytes
}

func (e *Engine) trimHistory(ctx *StepContext) {
	type idleEntry struct {
		sid string
		buf *EventBuffer
	}
	var idle []idleEntry
	var totalBytes int64
	for sid, b := range e.buffers {
		if e.watchedSessions[sid] || e.pinnedSessions[sid] {
			continue
		}
		idle = append(idle, idleEntry{sid: sid, buf: b})
		totalBytes += b.approxBytes
	}
	slices.SortFunc(idle, func(a, b idleEntry) int {
		if a.buf.usedAtMs < b.buf.usedAtMs {
			return -1
		}
		if a.buf.usedAtMs > b.buf.usedAtMs {
			return 1
		}
		return 0
	})
	count := len(idle)
	for _, it := range idle {
		missingSession := e.sessions[it.sid] == nil
		expired := e.monoMs-it.buf.usedAtMs >= HistoryIdleMs
		if missingSession || count > HistoryIdleCount || totalBytes > HistoryIdleBytes || expired {
			delete(e.buffers, it.sid)
			count--
			totalBytes -= it.buf.approxBytes
		}
	}
	if count > 0 && e.running && !e.isTimerActive(corev1.TimerKind_TIMER_KIND_HISTORY_SWEEP, "") {
		e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_HISTORY_SWEEP, "", HistorySweepIntervalMs)
	}
}
