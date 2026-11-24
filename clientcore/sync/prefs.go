package sync

import (
	"slices"

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
	e.cancelTimer(ctx, corev1.TimerKind_TIMER_KIND_PREFS_RETRY, "")
	e.prefsLoadOpID = e.allocOpID()
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_Rpc{
			Rpc: &corev1.RpcEffect{
				OpId: e.prefsLoadOpID,
				Kind: &corev1.RpcEffect_GetAccountPrefs{
					GetAccountPrefs: &corev1.GetAccountPrefsRpc{ObjectId: ProjectPrefsObjectID},
				},
			},
		},
	})
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
	head.Phase = prefsPhaseGet
	head.ActiveOpID = e.allocOpID()
	if head.Attempt == 0 {
		head.InitialBase = e.prefs.Revision
	}
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_Rpc{
			Rpc: &corev1.RpcEffect{
				OpId: head.ActiveOpID,
				Kind: &corev1.RpcEffect_GetAccountPrefs{
					GetAccountPrefs: &corev1.GetAccountPrefsRpc{ObjectId: ProjectPrefsObjectID},
				},
			},
		},
	})
}

func (e *Engine) handleAccountPrefsFetched(ctx *StepContext, ev *corev1.AccountPrefsFetchedEvent) {
	if ev == nil {
		return
	}
	if len(e.prefsQueue) > 0 && e.prefsQueue[0].Phase == prefsPhaseGet && ev.OpId == e.prefsQueue[0].ActiveOpID {
		e.handlePrefsCASGetResult(ctx, ev)
		return
	}
	if ev.OpId != e.prefsLoadOpID && ev.OpId != 0 {
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
	if ev.WirePrefs != nil || (e.secEngine != nil && e.secEngine.Locked() && ev.Prefs == nil) {
		var slice []*v1.AccountPrefsObject
		if ev.WirePrefs != nil {
			slice = []*v1.AccountPrefsObject{ev.WirePrefs}
		}
		e.openWireDirectoryBatchTag(ctx, nil, nil, nil, slice, dirBatchMeta{kind: dirKindPrefsLoad})
		return
	}
	e.applyAccountPrefs(ev.Prefs, true)
	ctx.Dirty.MarkAll()
}

func (e *Engine) handlePrefsCASGetResult(ctx *StepContext, ev *corev1.AccountPrefsFetchedEvent) {
	head := &e.prefsQueue[0]
	head.ActiveOpID = 0
	if ev.Error != "" {
		e.finishPrefsOp(ctx, world.Reason{Kind: world.ReasonServerReturned, Param: ev.Error})
		return
	}
	if ev.WirePrefs != nil || (e.secEngine != nil && e.secEngine.Locked() && ev.Prefs == nil) {
		var slice []*v1.AccountPrefsObject
		if ev.WirePrefs != nil {
			slice = []*v1.AccountPrefsObject{ev.WirePrefs}
		}
		e.openWireDirectoryBatchTag(ctx, nil, nil, nil, slice, dirBatchMeta{
			kind: dirKindPrefsCASGet, headOpID: head.OpID,
		})
		return
	}
	var baseRev uint64
	var basePrefs *v1.ProjectPrefs
	if ev.Prefs != nil {
		baseRev = ev.Prefs.Revision
		basePrefs = ev.Prefs.Prefs
	}
	if head.Attempt == 0 {
		head.InitialBase = baseRev
	} else if baseRev > head.InitialBase {
		e.w.ProjectChangedElsewhere = true
	}

	if basePrefs != nil && slices.Contains(basePrefs.RecentOps, head.OpID) {
		e.applyAccountPrefs(ev.Prefs, true)
		e.finishPrefsOp(ctx, world.Reason{})
		return
	}

	nextPrefs := lists.ApplyPrefsOp(basePrefs, head.Op, head.OpID, e.nowMs)
	if e.secEngine != nil && e.secEngine.Locked() {
		e.sealAndEmitPrefsPut(ctx, head, baseRev, nextPrefs)
		return
	}
	head.Phase = prefsPhasePut
	head.ActiveOpID = e.allocOpID()
	ctx.emit(&corev1.Effect{
		Effect: &corev1.Effect_Rpc{
			Rpc: &corev1.RpcEffect{
				OpId: head.ActiveOpID,
				Kind: &corev1.RpcEffect_PutAccountPrefs{
					PutAccountPrefs: &corev1.PutAccountPrefsRpc{
						ObjectId:         ProjectPrefsObjectID,
						ExpectedRevision: baseRev,
						NextPrefs:        nextPrefs,
					},
				},
			},
		},
	})
}

func (e *Engine) handleAccountPrefsPutResult(ctx *StepContext, ev *corev1.AccountPrefsPutResultEvent) {
	if ev == nil || len(e.prefsQueue) == 0 {
		return
	}
	head := &e.prefsQueue[0]
	if head.Phase != prefsPhasePut || ev.OpId != head.ActiveOpID {
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
	if ev.Current != nil {
		e.applyAccountPrefs(ev.Current, true)
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

func (e *Engine) applyAccountPrefs(in *corev1.PlainAccountPrefs, forceAccept bool) {
	if in == nil {
		e.applyAccountPrefsDecoded(nil, forceAccept)
		return
	}
	e.applyAccountPrefsDecoded(&decodedPrefs{
		Revision: in.Revision,
		Prefs:    in.Prefs,
		Broken:   in.Broken,
	}, forceAccept)
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
