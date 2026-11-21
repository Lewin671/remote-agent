package sync

import (
	"slices"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

type prefsPhase uint8

const (
	prefsPhaseIdle prefsPhase = iota
	prefsPhaseGet
	prefsPhasePut
)

type prefsQueueItem struct {
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
	ErrorText   *corev1.Text
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
	e.w.PrefsGlobalErrorText = nil
	if forSettings {
		delete(e.w.PrefsSettingsErrorText, op.Path)
	} else {
		delete(e.w.PrefsSaveErrorTextByPath, op.Path)
	}

	if errText := e.prefsMutationGuardError(); errText != nil {
		if forSettings {
			e.w.PrefsSettingsErrorText[op.Path] = errText
		} else {
			e.w.PrefsSaveErrorTextByPath[op.Path] = errText
			e.w.PrefsGlobalErrorText = errText
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

func (e *Engine) prefsMutationGuardError() *corev1.Text {
	if e.accountClockSkewed() {
		return text.Msg(text.KeyErrorsThisDeviceSClockIsBehindTheAccountStateRemote)
	}
	if e.accountControlLost() {
		return text.Msg(text.KeyErrorsRemoteControlExpiredSavingChangesSessionControlAndNew)
	}
	if !e.networkAvailable {
		return text.Msg(text.KeyConnectionNoNetwork)
	}
	if e.w.World.Connection != lists.ConnConnected {
		return text.Msg(text.KeyErrorsNotConnected)
	}
	return nil
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
		e.openWireDirectoryBatch(ctx, nil, nil, nil, slice, func(_ []decodedMachine, _ []decodedSession, _ []decodedDevice, outP []*decodedPrefs) {
			stepCtx := e.activeStepCtx
			var dp *decodedPrefs
			if len(outP) > 0 {
				dp = outP[0]
			}
			e.applyAccountPrefsDecoded(dp, true)
			if dp != nil && dp.Wire != nil && dp.Wire.Revision > 0 {
				stepCtx.emit(&corev1.Effect{
					Effect: &corev1.Effect_CacheWrite{
						CacheWrite: &corev1.CacheWriteEffect{
							Kind: &corev1.CacheWriteEffect_WritePrefs{WritePrefs: dp.Wire},
						},
					},
				})
			}
			stepCtx.Dirty.MarkAll()
		})
		return
	}
	e.applyAccountPrefs(ev.Prefs, true)
	if ev.Prefs != nil && ev.Prefs.CacheRef != "" {
		ctx.emit(&corev1.Effect{
			Effect: &corev1.Effect_CacheWrite{
				CacheWrite: &corev1.CacheWriteEffect{
					Kind: &corev1.CacheWriteEffect_WritePrefsRef{WritePrefsRef: ev.Prefs.CacheRef},
				},
			},
		})
	}
	ctx.Dirty.MarkAll()
}

func (e *Engine) handlePrefsCASGetResult(ctx *StepContext, ev *corev1.AccountPrefsFetchedEvent) {
	head := &e.prefsQueue[0]
	head.ActiveOpID = 0
	if ev.Error != "" {
		e.finishPrefsOp(ctx, text.Msg(text.KeyErrorsServerReturned, text.Str("value", ev.Error)))
		return
	}
	if ev.WirePrefs != nil || (e.secEngine != nil && e.secEngine.Locked() && ev.Prefs == nil) {
		headOpID := head.OpID
		var slice []*v1.AccountPrefsObject
		if ev.WirePrefs != nil {
			slice = []*v1.AccountPrefsObject{ev.WirePrefs}
		}
		e.openWireDirectoryBatch(ctx, nil, nil, nil, slice, func(_ []decodedMachine, _ []decodedSession, _ []decodedDevice, outP []*decodedPrefs) {
			stepCtx := e.activeStepCtx
			if len(e.prefsQueue) == 0 || e.prefsQueue[0].OpID != headOpID {
				return
			}
			curHead := &e.prefsQueue[0]
			var dp *decodedPrefs
			if len(outP) > 0 {
				dp = outP[0]
			}
			if dp != nil && dp.Broken {
				e.applyAccountPrefsDecoded(dp, true)
				e.finishPrefsOp(stepCtx, text.Msg(text.KeyErrorsInvalidCiphertextSignatureKeyOrContentVerificationFailed))
				return
			}
			var baseRev uint64
			var basePrefs *v1.ProjectPrefs
			if dp != nil {
				baseRev = dp.Revision
				basePrefs = dp.Prefs
			}
			if curHead.Attempt == 0 {
				curHead.InitialBase = baseRev
			} else if baseRev > curHead.InitialBase {
				e.w.ProjectChangedElsewhere = true
			}
			if basePrefs != nil && slices.Contains(basePrefs.RecentOps, curHead.OpID) {
				e.applyAccountPrefsDecoded(dp, true)
				if dp != nil && dp.Wire != nil && dp.Wire.Revision > 0 {
					stepCtx.emit(&corev1.Effect{
						Effect: &corev1.Effect_CacheWrite{
							CacheWrite: &corev1.CacheWriteEffect{
								Kind: &corev1.CacheWriteEffect_WritePrefs{WritePrefs: dp.Wire},
							},
						},
					})
				}
				e.finishPrefsOp(stepCtx, nil)
				return
			}
			nextPrefs := lists.ApplyPrefsOp(basePrefs, curHead.Op, curHead.OpID, e.nowMs)
			e.sealAndEmitPrefsPut(stepCtx, curHead, baseRev, nextPrefs)
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
		if ev.Prefs != nil && ev.Prefs.CacheRef != "" {
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_CacheWrite{
					CacheWrite: &corev1.CacheWriteEffect{
						Kind: &corev1.CacheWriteEffect_WritePrefsRef{WritePrefsRef: ev.Prefs.CacheRef},
					},
				},
			})
		}
		e.finishPrefsOp(ctx, nil)
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
		e.finishPrefsOp(ctx, text.Msg(text.KeyErrorsServerReturned, text.Str("value", ev.Error)))
		return
	}
	if !ev.Applied || ev.SealConflict {
		head.Attempt++
		e.w.ProjectChangedElsewhere = true
		if head.Attempt >= PrefsCASMaxAttempts {
			e.finishPrefsOp(ctx, text.Msg(text.KeyErrorsOtherClientsKeptChangingTheProjectListChangesCould))
			return
		}
		e.startNextPrefsOp(ctx)
		return
	}
	if ev.WireCurrent != nil {
		e.openWireDirectoryBatch(ctx, nil, nil, nil, []*v1.AccountPrefsObject{ev.WireCurrent}, func(_ []decodedMachine, _ []decodedSession, _ []decodedDevice, outP []*decodedPrefs) {
			stepCtx := e.activeStepCtx
			if len(outP) > 0 && outP[0] != nil {
				e.applyAccountPrefsDecoded(outP[0], true)
				if outP[0].Wire != nil && outP[0].Wire.Revision > 0 {
					stepCtx.emit(&corev1.Effect{
						Effect: &corev1.Effect_CacheWrite{
							CacheWrite: &corev1.CacheWriteEffect{
								Kind: &corev1.CacheWriteEffect_WritePrefs{WritePrefs: outP[0].Wire},
							},
						},
					})
				}
			}
			e.finishPrefsOp(stepCtx, nil)
		})
		return
	}
	if ev.Current != nil {
		e.applyAccountPrefs(ev.Current, true)
		if ev.Current.CacheRef != "" {
			ctx.emit(&corev1.Effect{
				Effect: &corev1.Effect_CacheWrite{
					CacheWrite: &corev1.CacheWriteEffect{
						Kind: &corev1.CacheWriteEffect_WritePrefsRef{WritePrefsRef: ev.Current.CacheRef},
					},
				},
			})
		}
	}
	e.finishPrefsOp(ctx, nil)
}

func (e *Engine) finishPrefsOp(ctx *StepContext, errText *corev1.Text) {
	if len(e.prefsQueue) == 0 {
		return
	}
	done := e.prefsQueue[0]
	e.prefsQueue = e.prefsQueue[1:]

	if done.ForSettings {
		delete(e.w.PrefsSavingSettings, done.Path)
		if errText != nil {
			e.w.PrefsSettingsErrorText[done.Path] = errText
		} else {
			delete(e.w.PrefsSettingsErrorText, done.Path)
		}
	} else {
		delete(e.w.PrefsPendingPath, done.Path)
		if errText != nil {
			e.w.PrefsSaveErrorTextByPath[done.Path] = errText
			e.w.PrefsGlobalErrorText = errText
		} else {
			delete(e.w.PrefsSaveErrorTextByPath, done.Path)
			e.w.PrefsGlobalErrorText = nil
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
		CacheRef: in.CacheRef,
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
			CacheRef: in.CacheRef,
		}
	}
}
