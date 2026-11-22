package security

import (
	"bytes"
	"errors"
	"slices"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

const maxSyncPages = 512

func (e *Engine) installRecordCAS(
	states []devicesec.State,
	rawStates [][]byte,
	rawProofs [][]byte,
	leaseBytes []byte,
	touchNowMs int64,
	extraFlags *struct {
		needsRotation          *bool
		recoveryObjectsPending *bool
	},
	onDone func(updated *PersistedRecord, err error),
) {
	e.installRecordCASAttempt(0, states, rawStates, rawProofs, leaseBytes, touchNowMs, extraFlags, onDone)
}

func (e *Engine) installRecordCASAttempt(
	attempt int,
	states []devicesec.State,
	rawStates [][]byte,
	rawProofs [][]byte,
	leaseBytes []byte,
	touchNowMs int64,
	extraFlags *struct {
		needsRotation          *bool
		recoveryObjectsPending *bool
	},
	onDone func(updated *PersistedRecord, err error),
) {
	wf := e.ensureWF()
	if attempt >= 4 {
		onDone(nil, devicesec.ErrStale)
		return
	}
	e.reqStorage(&corev1.SecurityStorageEffect{
		Op:     corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ,
		Bucket: corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD,
		Key:    "record",
	}, func(rep storageReply, err error) {
		if err != nil {
			onDone(nil, err)
			return
		}
		if !rep.found || len(rep.data) == 0 {
			onDone(nil, ErrNotInitialized)
			return
		}
		cur, err := DecodePersistedRecord(rep.data)
		if err != nil {
			onDone(nil, err)
			return
		}
		if cur.Pin != e.pin || cur.MemberID != e.selfDeviceID {
			onDone(nil, devicesec.ErrStale)
			return
		}
		curStates, _, _, err := parseAndVerifyChain(cur.Pin, cur.States, cur.RecoveryProofs)
		if err != nil {
			onDone(nil, err)
			return
		}
		curHead := curStates[len(curStates)-1]

		next := cur.Clone()
		e.syncUnlockedSecretsToRecord(next)
		if touchNowMs > next.FloorMs {
			next.FloorMs = touchNowMs
		}

		mergedStates := curStates
		if len(states) > 0 {
			var newRaw [][]byte
			var newProofs [][]byte
			for i, s := range states {
				var rp []byte
				if i < len(rawProofs) {
					rp = rawProofs[i]
				}
				if s.Core.Epoch <= curHead.Core.Epoch {
					idx := int(s.Core.Epoch - 1)
					if idx < 0 || idx >= len(curStates) || !sameBytes(curStates[idx].Signature, s.Signature) {
						onDone(nil, devicesec.ErrInvalid)
						return
					}
					continue
				}
				parent := mergedStates[len(mergedStates)-1]
				var rs *devicesec.RecoveryStatement
				if len(rp) > 0 {
					parsed, err := devicesec.ParseRecoveryStatement(rp)
					if err != nil {
						onDone(nil, err)
						return
					}
					rs = &parsed
					next.RecoveryObjectsPending = true
				}
				if err := devicesec.VerifyTransition(parent, s, rs); err != nil {
					onDone(nil, err)
					return
				}
				mergedStates = append(mergedStates, s)
				newRaw = append(newRaw, slices.Clone(rawStates[i]))
				newProofs = append(newProofs, slices.Clone(rp))
			}
			for _, raw := range newRaw {
				next.States = append(next.States, raw)
			}
			for _, rp := range newProofs {
				next.RecoveryProofs = append(next.RecoveryProofs, rp)
			}
		}

		newHead := mergedStates[len(mergedStates)-1]
		newHeadHash, _ := newHead.Hash()
		_, next.Member = newHead.Core.Member(next.MemberID)

		if len(leaseBytes) > 0 {
			l, err := devicesec.ParseLease(leaseBytes)
			if err != nil || l.Verify(newHead) != nil {
				onDone(nil, devicesec.ErrInvalid)
				return
			}
			effFloor := uint64(max(1, next.FloorMs))
			if l.IssuedAt > effFloor+devicesec.MaxClockSkew {
				onDone(nil, devicesec.ErrClock)
				return
			}
			if len(cur.Lease) > 0 {
				if oldL, err := devicesec.ParseLease(cur.Lease); err == nil && oldL.Head == newHeadHash {
					if oldL.IssuedAt > l.IssuedAt || oldL.NotAfter > l.NotAfter ||
						(oldL.IssuedAt == l.IssuedAt && oldL.NotAfter != l.NotAfter) {
						onDone(nil, devicesec.ErrStale)
						return
					}
				}
			}
			next.Lease = slices.Clone(leaseBytes)
		} else if len(next.Lease) > 0 {
			if oldL, err := devicesec.ParseLease(next.Lease); err != nil || oldL.Head != newHeadHash {
				next.Lease = nil
			}
		}

		if extraFlags != nil {
			if extraFlags.needsRotation != nil {
				next.NeedsRecoveryRotation = *extraFlags.needsRotation
			}
			if extraFlags.recoveryObjectsPending != nil {
				next.RecoveryObjectsPending = *extraFlags.recoveryObjectsPending
			}
		}

		encoded, err := next.Encode()
		if err != nil {
			onDone(nil, err)
			return
		}
		nextRev := e.nextWorkflowToken()
		writes := []*corev1.SecurityStorageWrite{
			{
				Bucket:           corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD,
				Key:              "record",
				ExpectedRevision: rep.revision,
				NextRevision:     nextRev,
				Data:             encoded,
			},
		}
		var wroteNotify bool
		if next.Member {
			if ntf := e.buildNotifyRecordBytes(next, newHead); len(ntf) > 0 {
				wroteNotify = true
				writes = append(writes, &corev1.SecurityStorageWrite{
					Bucket:       corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_NOTIFY_RECORD,
					Key:          "notify",
					NextRevision: nextRev,
					Data:         ntf,
				})
			}
		} else {
			writes = append(writes, &corev1.SecurityStorageWrite{
				Bucket: corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_NOTIFY_RECORD,
				Key:    "notify",
				Remove: true,
			})
		}

		e.reqStorage(&corev1.SecurityStorageEffect{
			Op:    corev1.SecurityStorageOp_SECURITY_STORAGE_OP_BATCH_COMMIT,
			Batch: writes,
		}, func(wrep storageReply, werr error) {
			if wrep.conflict {
				e.installRecordCASAttempt(attempt+1, states, rawStates, rawProofs, leaseBytes, touchNowMs, extraFlags, onDone)
				return
			}
			if werr != nil {
				onDone(nil, werr)
				return
			}
			wf.record = next
			wf.recordRev = nextRev
			e.applyPersistedRecordToEngine(next, func(unlockErr error) {
				if next.Member && !wroteNotify {
					// Newly unlocked head secret can now persist notify_blob cleanly.
					if ntf := e.buildNotifyRecordBytes(next, newHead); len(ntf) > 0 {
						e.reqStorage(&corev1.SecurityStorageEffect{
							Op:     corev1.SecurityStorageOp_SECURITY_STORAGE_OP_WRITE_CAS,
							Bucket: corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_NOTIFY_RECORD,
							Key:    "notify",
							Data:   ntf,
						}, func(storageReply, error) {})
					}
				}
				onDone(next, unlockErr)
			})
		})
	})
}

// SyncStates runs Core-driven state synchronization and drains emitted effects.
func (e *Engine) SyncStates(nowMs int64, touch bool, authToken string, onDone func(err error)) []*corev1.Effect {
	e.touchNow(nowMs)
	e.syncStates(touch, authToken, onDone)
	return e.DrainEffects()
}

func (e *Engine) syncStates(touch bool, authToken string, onDone func(err error)) {
	wf := e.ensureWF()
	if onDone != nil {
		wf.syncWaiters = append(wf.syncWaiters, onDone)
	}
	if wf.syncInFlight {
		return
	}
	wf.syncInFlight = true
	finishAll := func(err error) {
		wf.syncInFlight = false
		waiters := wf.syncWaiters
		wf.syncWaiters = nil
		for _, fn := range waiters {
			fn(err)
		}
	}

	server := e.effectiveServer()
	if server == "" || wf.record == nil || !e.hasHead {
		if !wf.bootstrapped {
			e.bootstrapFromStorage(0)
		}
		finishAll(ErrNotInitialized)
		return
	}
	if authToken != "" {
		wf.cachedToken = authToken
	}

	var pageStep func(pages int, touchPending bool)
	pageStep = func(pages int, touchPending bool) {
		if pages >= maxSyncPages {
			wf.recoveryStatusStale = true
			finishAll(devicesec.ErrCapacity)
			return
		}
		cur := wf.record
		if cur == nil {
			finishAll(ErrNotInitialized)
			return
		}
		var touchVal int64
		if touchPending && e.lastNowMs > cur.FloorMs {
			touchVal = e.lastNowMs
		}
		runAfterTouch := func(rec *PersistedRecord) {
			head, err := devicesec.ParseState(rec.States[len(rec.States)-1])
			if err != nil {
				finishAll(err)
				return
			}
			reqBytes, err := (&v1.GetSecurityStatesRequest{
				AfterEpoch: head.Core.Epoch,
				Limit:      8,
			}).Marshal()
			if err != nil {
				finishAll(err)
				return
			}
			e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_SECURITY_STATES, server, "", false, 0, reqBytes, func(respBytes []byte, rErr *rpcError) {
				if rErr != nil {
					wf.recoveryStatusStale = true
					finishAll(rErr)
					return
				}
				var resp v1.GetSecurityStatesResponse
				if err := resp.Unmarshal(respBytes); err != nil {
					wf.recoveryStatusStale = true
					finishAll(devicesec.ErrInvalid)
					return
				}
				e.recoveryPending = resp.RecoveryPending
				e.recoveryReadyMs = int64(resp.RecoveryReadyMs)
				wf.recoveryStatusStale = false

				if resp.HeadEpoch < head.Core.Epoch {
					finishAll(devicesec.ErrStale)
					return
				}
				if len(resp.States) == 0 {
					if resp.HeadEpoch > head.Core.Epoch {
						finishAll(devicesec.ErrStale)
						return
					}
					if len(resp.Lease) > 0 && !sameLeaseBytes(resp.Lease, rec.Lease) {
						e.installRecordCAS(nil, nil, nil, resp.Lease, e.lastNowMs, nil, func(updated *PersistedRecord, iErr error) {
							if iErr != nil {
								finishAll(iErr)
								return
							}
							e.afterSyncSuccess(updated, finishAll)
						})
						return
					}
					e.afterSyncSuccess(rec, finishAll)
					return
				}

				parsed := make([]devicesec.State, 0, len(resp.States))
				rawStates := make([][]byte, 0, len(resp.States))
				rawProofs := make([][]byte, 0, len(resp.States))
				for _, ss := range resp.States {
					if ss == nil || len(ss.State) == 0 {
						finishAll(devicesec.ErrInvalid)
						return
					}
					st, err := devicesec.ParseState(ss.State)
					if err != nil {
						finishAll(err)
						return
					}
					parsed = append(parsed, st)
					rawStates = append(rawStates, ss.State)
					rawProofs = append(rawProofs, ss.Recovery)
				}
				var pageLease []byte
				if len(resp.Lease) > 0 && len(parsed) > 0 {
					if l, lErr := devicesec.ParseLease(resp.Lease); lErr == nil && l.Verify(parsed[len(parsed)-1]) == nil {
						pageLease = resp.Lease
					}
				}
				e.installRecordCAS(parsed, rawStates, rawProofs, pageLease, e.lastNowMs, nil, func(updated *PersistedRecord, iErr error) {
					if iErr != nil {
						finishAll(iErr)
						return
					}
					if updated != nil && len(updated.States) <= len(rec.States) {
						finishAll(devicesec.ErrStale)
						return
					}
					pageStep(pages+1, touchPending)
				})
			})
		}

		if touchVal > cur.FloorMs {
			e.installRecordCAS(nil, nil, nil, nil, touchVal, nil, func(updated *PersistedRecord, tErr error) {
				if tErr != nil {
					finishAll(tErr)
					return
				}
				runAfterTouch(updated)
			})
			return
		}
		runAfterTouch(cur)
	}

	pageStep(0, touch)
}

func (e *Engine) afterSyncSuccess(rec *PersistedRecord, finishAll func(error)) {
	wf := e.ensureWF()
	e.verifiedNetGen = e.netGen
	if rec != nil && !rec.Member {
		wf.cachedToken = ""
		wf.tokenExpiresMs = 0
		e.out = append(e.out, &corev1.Effect{
			Effect: &corev1.Effect_SecurityTokenUpdated{
				SecurityTokenUpdated: &corev1.SecurityTokenUpdatedEffect{Cleared: true},
			},
		})
	}
	if rec != nil && rec.Member && rec.RecoveryObjectsPending {
		e.republishRecoveryObjects(nil)
	}
	// Check foreground auto lease renewal policy (<24h left and >=12h since issued).
	if rec != nil && e.isAuthority() && wf.foreground && !wf.opBusy && !e.isClockSkewed(e.lastNowMs) {
		if wf.pendingOp == nil || wf.pendingOp.Kind == "offer" || wf.pendingOp.Kind == "delete-records" {
			var curLease *devicesec.LeaseCertificate
			if e.hasLease && e.lease.Head == e.headHash {
				l := e.lease
				curLease = &l
			}
			effNow := e.effectiveNow(e.lastNowMs)
			if needsLeaseRenewal(curLease, effNow) && (wf.lastLeaseRenewAttemptMs == 0 || e.lastNowMs-wf.lastLeaseRenewAttemptMs >= 60_000) {
				wf.lastLeaseRenewAttemptMs = e.lastNowMs
				e.runOp(0, true, func(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
					e.renewLeaseOp(fgGen, false, func(res *corev1.SecurityWorkflowResultEffect, err error) {
						if done != nil {
							done(res, err)
						}
					})
				})
			}
		}
	}
	finishAll(nil)
}

func (e *Engine) republishRecoveryObjects(onDone func(error)) {
	wf := e.ensureWF()
	finish := func(err error) {
		if err == nil {
			e.emitStatusChanged()
		}
		if onDone != nil {
			onDone(err)
		}
	}
	rec := wf.record
	if rec == nil || !rec.Member || !e.hasHead {
		finish(nil)
		return
	}
	var boundary uint64
	for i := len(e.states) - 1; i >= 0; i-- {
		if i < len(e.recoveryAt) && e.recoveryAt[i] {
			boundary = e.states[i].Core.ContentEpoch
			break
		}
	}
	if boundary == 0 {
		doneFlag := false
		e.installRecordCAS(nil, nil, nil, nil, 0, &struct {
			needsRotation          *bool
			recoveryObjectsPending *bool
		}{recoveryObjectsPending: &doneFlag}, func(_ *PersistedRecord, err error) {
			finish(err)
		})
		return
	}

	server := e.effectiveServer()
	const prefsObj = "project-prefs"
	var tryAttempt func(n int)
	tryAttempt = func(n int) {
		if n >= 5 {
			finish(devicesec.ErrStale)
			return
		}
		getBytes, err := (&v1.GetAccountPrefsRequest{ObjectId: prefsObj}).Marshal()
		if err != nil {
			finish(err)
			return
		}
		e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_ACCOUNT_PREFS, server, "", false, 0, getBytes, func(respBytes []byte, rErr *rpcError) {
			if rErr != nil {
				finish(rErr)
				return
			}
			var resp v1.GetAccountPrefsResponse
			if err := resp.Unmarshal(respBytes); err != nil {
				finish(devicesec.ErrInvalid)
				return
			}
			obj := resp.Object
			if obj == nil || obj.Envelope == nil || obj.Revision == 0 {
				doneFlag := false
				e.installRecordCAS(nil, nil, nil, nil, 0, &struct {
					needsRotation          *bool
					recoveryObjectsPending *bool
				}{recoveryObjectsPending: &doneFlag}, func(_ *PersistedRecord, iErr error) {
					finish(iErr)
				})
				return
			}
			e.openBatchInternal(e.lastNowMs, []OpenItem{{
				Env:             obj.Envelope,
				ExpectedPurpose: devicesec.Prefs,
				RequestID:       prefsObj,
				Seq:             obj.Revision,
				History:         true,
			}}, func(results []OpenResult) {
				r := results[0]
				if r.Err != nil {
					if errors.Is(r.Err, ErrRecoveryHistoryUnavailable) {
						doneFlag := false
						e.installRecordCAS(nil, nil, nil, nil, 0, &struct {
							needsRotation          *bool
							recoveryObjectsPending *bool
						}{recoveryObjectsPending: &doneFlag}, func(_ *PersistedRecord, iErr error) {
							finish(iErr)
						})
						return
					}
					finish(r.Err)
					return
				}
				if r.Header.ContentEpoch >= boundary {
					doneFlag := false
					e.installRecordCAS(nil, nil, nil, nil, 0, &struct {
						needsRotation          *bool
						recoveryObjectsPending *bool
					}{recoveryObjectsPending: &doneFlag}, func(_ *PersistedRecord, iErr error) {
						finish(iErr)
					})
					return
				}
				pp, ok := r.Body.(*v1.ProjectPrefs)
				if !ok || pp == nil {
					finish(devicesec.ErrInvalid)
					return
				}
				pp.Schema = 1
				raw, err := pp.Marshal()
				if err != nil {
					finish(err)
					return
				}
				e.withEntropy(64, func() {
					e.sealEnvelopeInternal(e.lastNowMs, devicesec.Prefs, "", "", prefsObj, obj.Revision+1, 0, raw, false, nil, func(sr SealResult) {
						if sr.Err != nil {
							finish(sr.Err)
							return
						}
						putBytes, err := (&v1.PutAccountPrefsRequest{
							ObjectId:         prefsObj,
							ExpectedRevision: obj.Revision,
							Envelope:         sr.Envelope,
						}).Marshal()
						if err != nil {
							finish(err)
							return
						}
						e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_ACCOUNT_PREFS, server, "", false, 0, putBytes, func(putRespBytes []byte, pErr *rpcError) {
							if pErr != nil {
								finish(pErr)
								return
							}
							var pr v1.PutAccountPrefsResponse
							if err := pr.Unmarshal(putRespBytes); err != nil {
								finish(devicesec.ErrInvalid)
								return
							}
							if pr.Applied {
								doneFlag := false
								e.installRecordCAS(nil, nil, nil, nil, 0, &struct {
									needsRotation          *bool
									recoveryObjectsPending *bool
								}{recoveryObjectsPending: &doneFlag}, func(_ *PersistedRecord, iErr error) {
									finish(iErr)
								})
								return
							}
							tryAttempt(n + 1)
						})
					})
				})
			})
		})
	}
	tryAttempt(0)
}

func sameLeaseBytes(a, b []byte) bool {
	return bytes.Equal(a, b)
}
