package sync

import (
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
)

type prefsPhase uint8

const (
	prefsPhaseIdle prefsPhase = iota
	prefsPhaseGet
	prefsPhasePut
)

type prefsQueueItem struct {
	_           [0]func()
	OpID        string
	Path        string
	ForSettings bool
	DraftKey    string
	Op          *lists.FullPrefsOp
	Attempt     int
	ActiveOpID  uint64
	Phase       prefsPhase
	InitialBase uint64
}

// PrefsResultCallback can be handled via SettledPrefs if needed by UI drafts.
type SettledPrefs struct {
	Path        string
	DraftKey    string
	ForSettings bool
	OK          bool
	ErrorReason world.Reason
}

func (e *Engine) startLoadPrefs(ctx *StepContext) {
	if !e.running {
		return
	}
	if e.demoMode {
		e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_PREFS_RETRY, "")
		e.prefs.LoadFailed = false
		ctx.Dirty.Add(true, true, false)
		return
	}
	e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_PREFS_RETRY, "")
	e.prefsLoadOpID = e.allocOpID()
	e.emitRelayRpc(
		ctx,
		corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_ACCOUNT_PREFS,
		e.cachedToken,
		relayRpcDefaultTimeoutMs,
		"",
		(&v1.GetAccountPrefsRequest{ObjectId: ProjectPrefsObjectID}).AppendMarshal(nil),
		&pendingRelayRpc{kind: relayRpcPrefsGet, opID: e.prefsLoadOpID},
	)
}

// EnqueuePrefsOp enqueues an idempotent ProjectPrefs CAS mutation.
// For settings mutations pass forSettings=true and optional draftKey for draft tracking.
func (e *Engine) EnqueuePrefsOp(ctx *StepContext, op *lists.FullPrefsOp, forSettings bool, draftKey string) bool {
	ok := e.enqueuePrefsOp(ctx, op, forSettings, draftKey)
	e.refreshWorld()
	return ok
}

func (e *Engine) enqueuePrefsOp(ctx *StepContext, op *lists.FullPrefsOp, forSettings bool, draftKey string) bool {
	if op == nil {
		return false
	}
	e.w.PrefsGlobalError = world.Reason{}
	if forSettings {
		delete(e.w.PrefsSettingsError, op.Path)
	} else {
		delete(e.w.PrefsSaveErrorByPath, op.Path)
	}

	if errReason := e.prefsMutationGuardError(); !errReason.IsZero() {
		if forSettings {
			e.w.PrefsSettingsError[op.Path] = errReason
		} else {
			e.w.PrefsSaveErrorByPath[op.Path] = errReason
			e.w.PrefsGlobalError = errReason
		}
		ctx.Dirty.Add(true, true, true)
		return false
	}

	opID := e.nextID(ctx)
	item := prefsQueueItem{
		OpID:        opID,
		Path:        op.Path,
		ForSettings: forSettings,
		DraftKey:    draftKey,
		Op:          op,
	}
	if forSettings {
		e.w.PrefsSavingSettings[op.Path] = true
	} else {
		e.w.PrefsPendingPath[op.Path] = true
	}
	e.prefsQueue = append(e.prefsQueue, item)
	if len(e.prefsQueue) == 1 {
		e.startNextPrefsOp(ctx)
	}
	ctx.Dirty.Add(true, true, true)
	return true
}

func (e *Engine) prefsMutationGuardError() world.Reason {
	if e.accountClockSkewed() {
		return world.Reason{Kind: world.ReasonClockBehind}
	}
	if e.accountControlLost() {
		return world.Reason{Kind: world.ReasonRemoteControlExpired}
	}
	if !e.networkAvailable {
		return world.Reason{Kind: world.ReasonNoNetwork}
	}
	if e.w.World.Connection != lists.ConnConnected {
		return world.Reason{Kind: world.ReasonNotConnected}
	}
	return world.Reason{}
}

func (e *Engine) startNextPrefsOp(ctx *StepContext) {
	if len(e.prefsQueue) == 0 || !e.running {
		return
	}
	head := &e.prefsQueue[0]
	if e.demoMode {
		if e.demoPrefsError != "" {
			e.finishPrefsOp(ctx, world.Reason{Kind: world.ReasonServerReturned, Param: e.demoPrefsError})
			return
		}
		nextPrefs := lists.ApplyPrefsOp(e.prefs.Prefs, head.Op, head.OpID, e.nowMs)
		if nextPrefs == nil {
			nextPrefs = &v1.ProjectPrefs{Schema: 1}
		} else {
			nextPrefs.Schema = 1
		}
		e.applyAccountPrefsDecoded(&decodedPrefs{
			Revision: e.prefs.Revision + 1,
			Prefs:    nextPrefs,
		}, true)
		e.finishPrefsOp(ctx, world.Reason{})
		return
	}
	head.Phase = prefsPhaseGet
	head.ActiveOpID = e.allocOpID()
	if head.Attempt == 0 {
		head.InitialBase = e.prefs.Revision
	}
	e.emitRelayRpc(
		ctx,
		corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_ACCOUNT_PREFS,
		e.cachedToken,
		relayRpcDefaultTimeoutMs,
		"",
		(&v1.GetAccountPrefsRequest{ObjectId: ProjectPrefsObjectID}).AppendMarshal(nil),
		&pendingRelayRpc{kind: relayRpcPrefsGet, opID: head.ActiveOpID},
	)
}

func (e *Engine) handleAccountPrefsFetched(ctx *StepContext, ev accountPrefsResult) {
	if len(e.prefsQueue) > 0 && e.prefsQueue[0].Phase == prefsPhaseGet && ev.OpID == e.prefsQueue[0].ActiveOpID {
		e.handlePrefsCASGetResult(ctx, ev)
		return
	}
	if ev.OpID != e.prefsLoadOpID && ev.OpID != 0 {
		return
	}
	e.prefsLoadOpID = 0
	if ev.Error != "" {
		e.prefs.LoadFailed = !e.prefs.Loaded
		if e.running && e.w.LivePushConnected && !e.prefs.Loaded {
			wait := e.prefsBackoffMs
			if wait <= 0 {
				wait = PrefsRetryInitialMs
			}
			e.prefsBackoffMs = min(wait*2, PrefsRetryMaxMs)
			e.scheduleTimer(ctx, corev1.TimerKind_TIMER_KIND_PREFS_RETRY, "", wait)
		}
		ctx.Dirty.MarkAll()
		return
	}
	e.prefsBackoffMs = PrefsRetryInitialMs
	e.prefs.LoadFailed = false
	var slice []*v1.AccountPrefsObject
	if ev.WirePrefs != nil {
		slice = []*v1.AccountPrefsObject{ev.WirePrefs}
	}
	e.openWireDirectoryBatchTag(ctx, nil, nil, nil, slice, dirBatchMeta{kind: dirKindPrefsLoad})
}

func (e *Engine) handlePrefsCASGetResult(ctx *StepContext, ev accountPrefsResult) {
	head := &e.prefsQueue[0]
	head.ActiveOpID = 0
	if ev.Error != "" {
		e.finishPrefsOp(ctx, world.Reason{Kind: world.ReasonServerReturned, Param: ev.Error})
		return
	}
	var slice []*v1.AccountPrefsObject
	if ev.WirePrefs != nil {
		slice = []*v1.AccountPrefsObject{ev.WirePrefs}
	}
	e.openWireDirectoryBatchTag(ctx, nil, nil, nil, slice, dirBatchMeta{
		kind: dirKindPrefsCASGet, headOpID: head.OpID,
	})
}

func (e *Engine) handleAccountPrefsPutResult(ctx *StepContext, ev accountPrefsPutResult) {
	if len(e.prefsQueue) == 0 {
		return
	}
	head := &e.prefsQueue[0]
	if head.Phase != prefsPhasePut || ev.OpID != head.ActiveOpID {
		return
	}
	head.ActiveOpID = 0
	if ev.Error != "" {
		e.finishPrefsOp(ctx, world.Reason{Kind: world.ReasonServerReturned, Param: ev.Error})
		return
	}
	if !ev.Applied || ev.SealConflict {
		head.Attempt++
		e.w.ProjectChangedElsewhere = true
		if head.Attempt >= PrefsCASMaxAttempts {
			e.finishPrefsOp(ctx, world.Reason{Kind: world.ReasonPrefsConflict})
			return
		}
		e.startNextPrefsOp(ctx)
		return
	}
	if ev.WireCurrent != nil {
		e.openWireDirectoryBatchTag(ctx, nil, nil, nil, []*v1.AccountPrefsObject{ev.WireCurrent}, dirBatchMeta{
			kind: dirKindPrefsCASPut,
		})
		return
	}
	e.finishPrefsOp(ctx, world.Reason{})
}

func (e *Engine) finishPrefsOp(ctx *StepContext, errReason world.Reason) {
	if len(e.prefsQueue) == 0 {
		return
	}
	done := e.prefsQueue[0]
	e.prefsQueue = e.prefsQueue[1:]

	if done.ForSettings {
		delete(e.w.PrefsSavingSettings, done.Path)
		if !errReason.IsZero() {
			e.w.PrefsSettingsError[done.Path] = errReason
		} else {
			delete(e.w.PrefsSettingsError, done.Path)
		}
	} else {
		delete(e.w.PrefsPendingPath, done.Path)
		if !errReason.IsZero() {
			e.w.PrefsSaveErrorByPath[done.Path] = errReason
			e.w.PrefsGlobalError = errReason
		} else {
			delete(e.w.PrefsSaveErrorByPath, done.Path)
			e.w.PrefsGlobalError = world.Reason{}
		}
	}

	if len(e.prefsQueue) > 0 && e.running {
		e.startNextPrefsOp(ctx)
	}
	ctx.Dirty.MarkAll()
}

func (e *Engine) applyAccountPrefsDecoded(in *decodedPrefs, forceAccept bool) {
	if in == nil {
		if !e.prefs.Loaded {
			e.prefs = PrefsState{Loaded: true}
		}
		return
	}
	if in.Broken {
		if !e.prefs.Loaded {
			e.prefs.Broken = true
		}
		return
	}
	if !e.prefs.Loaded || in.Revision > e.prefs.Revision || (forceAccept && in.Revision == e.prefs.Revision) {
		var clone *v1.ProjectPrefs
		if in.Prefs != nil {
			clone = in.Prefs.Clone()
		}
		e.prefs = PrefsState{
			Loaded:   true,
			Revision: in.Revision,
			Prefs:    clone,
		}
	}
}
