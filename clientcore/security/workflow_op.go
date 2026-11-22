package security

import (
	"slices"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

func (e *Engine) claimOperationCAS(op *PersistedOperation, expectedHead *devicesec.Hash, expectedMemberID string, fn func(saved *PersistedOperation, err error)) {
	wf := e.ensureWF()
	if wf.regIntent != nil {
		fn(nil, devicesec.ErrStale)
		return
	}
	e.reqStorage(&corev1.SecurityStorageEffect{
		Op: corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP,
	}, func(rep storageReply, err error) {
		if err != nil {
			fn(nil, err)
			return
		}
		var recRaw []byte
		for _, ent := range rep.entries {
			if ent == nil {
				continue
			}
			if ent.Bucket == corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_OPERATION && len(ent.Data) > 0 {
				fn(nil, newWfError("pending", text.KeySecurityASecurityOperationIsUnfinishedContinueItFirst))
				return
			}
			if ent.Bucket == corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_REGISTRATION_INTENT && len(ent.Data) > 0 {
				fn(nil, devicesec.ErrStale)
				return
			}
			if ent.Bucket == corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD && len(ent.Data) > 0 {
				recRaw = ent.Data
			}
		}
		if expectedHead != nil {
			if len(recRaw) == 0 {
				fn(nil, devicesec.ErrStale)
				return
			}
			rec, err := DecodePersistedRecord(recRaw)
			if err != nil || rec.MemberID != expectedMemberID {
				fn(nil, devicesec.ErrStale)
				return
			}
			states, _, _, err := parseAndVerifyChain(rec.Pin, rec.States, nil)
			if err != nil {
				fn(nil, devicesec.ErrStale)
				return
			}
			hh, _ := states[len(states)-1].Hash()
			if hh != *expectedHead {
				fn(nil, devicesec.ErrStale)
				return
			}
		}
		data, err := op.Encode()
		if err != nil {
			fn(nil, err)
			return
		}
		rev := u64Str(op.Revision)
		e.reqStorage(&corev1.SecurityStorageEffect{
			Op:         corev1.SecurityStorageOp_SECURITY_STORAGE_OP_WRITE_CAS,
			Bucket:     corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_OPERATION,
			Key:        "pending",
			CreateOnly: true,
			NextRevision: rev,
			Data:       data,
		}, func(wrep storageReply, werr error) {
			if wrep.conflict {
				fn(nil, newWfError("pending", text.KeySecurityASecurityOperationIsUnfinishedContinueItFirst))
				return
			}
			if werr != nil {
				fn(nil, werr)
				return
			}
			wf.pendingOp = op
			wf.pendingOpRev = rev
			e.securityLocked = true
			fn(op, nil)
		})
	})
}

func (e *Engine) saveOperationCAS(prev, next *PersistedOperation, fn func(saved *PersistedOperation, err error)) {
	wf := e.ensureWF()
	cp := *next
	cp.Revision = prev.Revision + 1
	if err := prev.ValidateProgress(&cp); err != nil {
		fn(nil, devicesec.ErrStale)
		return
	}
	data, err := cp.Encode()
	if err != nil {
		fn(nil, err)
		return
	}
	expectedRev := u64Str(prev.Revision)
	if wf.pendingOpRev != "" {
		expectedRev = wf.pendingOpRev
	}
	nextRev := u64Str(cp.Revision)
	e.reqStorage(&corev1.SecurityStorageEffect{
		Op:               corev1.SecurityStorageOp_SECURITY_STORAGE_OP_WRITE_CAS,
		Bucket:           corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_OPERATION,
		Key:              "pending",
		ExpectedRevision: expectedRev,
		NextRevision:     nextRev,
		Data:             data,
	}, func(rep storageReply, werr error) {
		if rep.conflict {
			fn(nil, devicesec.ErrStale)
			return
		}
		if werr != nil {
			fn(nil, werr)
			return
		}
		wf.pendingOp = &cp
		wf.pendingOpRev = nextRev
		fn(&cp, nil)
	})
}

func (e *Engine) clearOperationCAS(op *PersistedOperation, fn func(err error)) {
	wf := e.ensureWF()
	expectedRev := u64Str(op.Revision)
	if wf.pendingOpRev != "" {
		expectedRev = wf.pendingOpRev
	}
	e.reqStorage(&corev1.SecurityStorageEffect{
		Op:               corev1.SecurityStorageOp_SECURITY_STORAGE_OP_DELETE_CAS,
		Bucket:           corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_OPERATION,
		Key:              "pending",
		ExpectedRevision: expectedRev,
	}, func(rep storageReply, werr error) {
		if rep.conflict {
			fn(devicesec.ErrStale)
			return
		}
		if werr != nil {
			fn(werr)
			return
		}
		wf.pendingOp = nil
		wf.pendingOpRev = ""
		fn(nil)
	})
}

func (e *Engine) resumePendingOp(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	if err := e.checkFG(fgGen); err != nil {
		done(nil, err)
		return
	}
	op := wf.pendingOp
	if op == nil {
		if wf.regIntent != nil {
			e.resumeRegistration(fgGen, done)
			return
		}
		done(nil, nil)
		return
	}
	if wf.record != nil && op.Kind != "create" && op.Kind != "recovery" {
		if op.Server != e.effectiveServer() || op.MemberID != e.selfDeviceID {
			done(nil, devicesec.ErrStale)
			return
		}
	}
	if op.Kind == "create" || op.Kind == "recovery" {
		done(nil, newWfError("conflict", text.KeySecurityReturnToAccountCreationOrRecoveryToContinueThe))
		return
	}
	switch op.Kind {
	case "offer":
		e.resumeOfferOp(op, fgGen, done)
	case "delete-records":
		e.resumeDeleteRecordsOp(op, fgGen, done)
	case "lease":
		e.syncStates(false, "", func(sErr error) {
			if sErr != nil {
				done(nil, sErr)
				return
			}
			if err := e.checkFG(fgGen); err != nil {
				done(nil, err)
				return
			}
			e.resumeLeaseOp(op, fgGen, done)
		})
	case "state":
		e.resumeStateOp(op, fgGen, done)
	default:
		done(nil, devicesec.ErrInvalid)
	}
}

func (e *Engine) resumeOfferOp(op *PersistedOperation, fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	if !op.CodeSaved {
		done(nil, nil)
		return
	}
	e.syncStates(false, "", func(sErr error) {
		if sErr != nil {
			done(nil, sErr)
			return
		}
		if err := e.checkFG(fgGen); err != nil {
			done(nil, err)
			return
		}
		if e.isAuthority() && len(op.RecoveryEd) == 32 && sameBytes(e.head.Core.RecoveryEd[:], op.RecoveryEd) {
			e.clearOperationCAS(op, func(cErr error) {
				done(nil, cErr)
			})
			return
		}
		if !e.isMember {
			done(nil, devicesec.ErrUntrusted)
			return
		}
		if e.isAuthority() {
			// Wait cleanly until no longer authority, keep draft.
			done(nil, nil)
			return
		}
		e.publishDeviceOfferRPC(op, fgGen, done)
	})
}

func (e *Engine) publishDeviceOfferRPC(op *PersistedOperation, fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	sendReq := func(curOp *PersistedOperation, req *v1.PutDeviceMetaRequest, reqBytes []byte) {
		if req.DeviceId != e.selfDeviceID {
			done(nil, devicesec.ErrUntrusted)
			return
		}
		e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_DEVICE_META, curOp.Server, "", true, fgGen, reqBytes, func(respBytes []byte, rErr *rpcError) {
			if rErr != nil {
				done(nil, rErr)
				return
			}
			var resp v1.PutDeviceMetaResponse
			if err := resp.Unmarshal(respBytes); err != nil {
				done(nil, devicesec.ErrInvalid)
				return
			}
			finishOffer := func(ok bool) {
				if err := e.checkFG(fgGen); err != nil {
					done(nil, err)
					return
				}
				if !ok {
					rejected := *curOp
					rejected.OfferRejected = true
					e.saveOperationCAS(curOp, &rejected, func(_ *PersistedOperation, sErr error) {
						if sErr != nil {
							done(nil, sErr)
							return
						}
						done(nil, newWfError("conflict", text.KeySecurityDeviceInformationChangedTheOriginalTransferRequestWasNot))
					})
					return
				}
				delivered := *curOp
				delivered.Phase = "DELIVERED"
				e.saveOperationCAS(curOp, &delivered, func(saved *PersistedOperation, sErr error) {
					if sErr != nil {
						done(nil, sErr)
						return
					}
					if err := e.checkFG(fgGen); err != nil {
						done(nil, err)
						return
					}
					e.clearOperationCAS(saved, func(cErr error) {
						done(nil, cErr)
					})
				})
			}

			if resp.Applied {
				finishOffer(true)
				return
			}
			// Check whether server already holds this offer key at higher revision.
			listReq, _ := (&v1.ListDevicesRequest{}).Marshal()
			e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LIST_DEVICES, curOp.Server, "", true, fgGen, listReq, func(lRespBytes []byte, lErr *rpcError) {
				if lErr != nil {
					done(nil, lErr)
					return
				}
				var lr v1.ListDevicesResponse
				if err := lr.Unmarshal(lRespBytes); err != nil {
					done(nil, devicesec.ErrInvalid)
					return
				}
				var ownDev *v1.Device
				for _, d := range lr.Devices {
					if d != nil && d.Id == e.selfDeviceID {
						ownDev = d
						break
					}
				}
				if ownDev == nil || ownDev.Meta == nil || ownDev.MetaRevision <= req.ExpectedRevision {
					finishOffer(false)
					return
				}
				e.openBatchInternal(e.lastNowMs, []OpenItem{{
					Env:             ownDev.Meta,
					ExpectedPurpose: devicesec.DeviceMeta,
					RequestID:       e.selfDeviceID,
					History:         true,
				}}, func(res []OpenResult) {
					if res[0].Err != nil {
						finishOffer(false)
						return
					}
					dm, ok := res[0].Body.(*v1.DeviceMeta)
					if !ok || dm == nil || !sameBytes(dm.HandoffRecoveryEd, curOp.RecoveryEd) {
						finishOffer(false)
						return
					}
					finishOffer(true)
				})
			})
		})
	}

	if len(op.Signed) > 0 {
		var req v1.PutDeviceMetaRequest
		if err := req.Unmarshal(op.Signed); err != nil {
			done(nil, devicesec.ErrInvalid)
			return
		}
		sendReq(op, &req, op.Signed)
		return
	}

	// Build PutDeviceMetaRequest against live server DeviceMeta revision.
	listReq, _ := (&v1.ListDevicesRequest{}).Marshal()
	e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LIST_DEVICES, op.Server, "", true, fgGen, listReq, func(lRespBytes []byte, lErr *rpcError) {
		if lErr != nil {
			done(nil, lErr)
			return
		}
		var lr v1.ListDevicesResponse
		if err := lr.Unmarshal(lRespBytes); err != nil {
			done(nil, devicesec.ErrInvalid)
			return
		}
		var ownDev *v1.Device
		for _, d := range lr.Devices {
			if d != nil && d.Id == e.selfDeviceID {
				ownDev = d
				break
			}
		}
		var rev uint64
		buildSeal := func(base *v1.DeviceMeta) {
			if base == nil {
				base = &v1.DeviceMeta{}
			}
			base.HandoffRecoveryEd = slices.Clone(op.RecoveryEd)
			plain, err := base.Marshal()
			if err != nil {
				done(nil, err)
				return
			}
			e.withEntropy(64, func() {
				e.sealEnvelopeInternal(e.lastNowMs, devicesec.DeviceMeta, "", "", e.selfDeviceID, rev+1, 0, plain, false, nil, func(sr SealResult) {
					if sr.Err != nil {
						done(nil, sr.Err)
						return
					}
					if err := e.checkFG(fgGen); err != nil {
						done(nil, err)
						return
					}
					req := &v1.PutDeviceMetaRequest{
						DeviceId:         e.selfDeviceID,
						ExpectedRevision: rev,
						Meta:             sr.Envelope,
					}
					reqBytes, err := req.Marshal()
					if err != nil {
						done(nil, err)
						return
					}
					next := *op
					next.Signed = reqBytes
					e.saveOperationCAS(op, &next, func(saved *PersistedOperation, sErr error) {
						if sErr != nil {
							done(nil, sErr)
							return
						}
						sendReq(saved, req, reqBytes)
					})
				})
			})
		}

		if ownDev != nil && ownDev.Meta != nil && ownDev.MetaRevision > 0 {
			rev = ownDev.MetaRevision
			e.openBatchInternal(e.lastNowMs, []OpenItem{{
				Env:             ownDev.Meta,
				ExpectedPurpose: devicesec.DeviceMeta,
				RequestID:       e.selfDeviceID,
				History:         true,
			}}, func(res []OpenResult) {
				if res[0].Err != nil {
					done(nil, res[0].Err)
					return
				}
				dm, _ := res[0].Body.(*v1.DeviceMeta)
				buildSeal(dm.Clone())
			})
			return
		}
		buildSeal(nil)
	})
}

func (e *Engine) resumeDeleteRecordsOp(op *PersistedOperation, fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	e.syncStates(false, "", func(sErr error) {
		if sErr != nil {
			done(nil, sErr)
			return
		}
		if err := e.checkFG(fgGen); err != nil {
			done(nil, err)
			return
		}
		if !e.isAuthority() {
			done(nil, devicesec.ErrUntrusted)
			return
		}
		if e.headHash != op.Parent {
			done(nil, devicesec.ErrStale)
			return
		}
		sendReq := func(curOp *PersistedOperation) {
			e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_DELETE_MACHINE_RECORDS, curOp.Server, "", true, fgGen, curOp.Signed, func(_ []byte, rErr *rpcError) {
				if rErr != nil {
					done(nil, rErr)
					return
				}
				if err := e.checkFG(fgGen); err != nil {
					done(nil, err)
					return
				}
				e.clearOperationCAS(curOp, func(cErr error) {
					done(nil, cErr)
				})
			})
		}
		if len(op.Signed) > 0 {
			sendReq(op)
			return
		}
		reqBytes, err := (&v1.DeleteMachineRecordsRequest{MachineId: op.TargetDeviceID}).Marshal()
		if err != nil {
			done(nil, err)
			return
		}
		next := *op
		next.Signed = reqBytes
		e.saveOperationCAS(op, &next, func(saved *PersistedOperation, sErr error) {
			if sErr != nil {
				done(nil, sErr)
				return
			}
			sendReq(saved)
		})
	})
}

func (e *Engine) resumeLeaseOp(op *PersistedOperation, fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	if e.headHash != op.Parent {
		if _, ok := e.byHash[op.Parent]; !ok {
			done(nil, devicesec.ErrStale)
			return
		}
		e.clearOperationCAS(op, func(cErr error) {
			done(nil, cErr)
		})
		return
	}
	if !e.isAuthority() {
		done(nil, devicesec.ErrUntrusted)
		return
	}
	publishReq := func(curOp *PersistedOperation, req *v1.PutSecurityStateRequest, reqBytes []byte) {
		cert, err := devicesec.ParseLease(req.Lease)
		if err != nil || cert.Verify(e.head) != nil {
			done(nil, devicesec.ErrInvalid)
			return
		}
		effNow := e.effectiveNow(e.lastNowMs)
		needFresh := effNow >= cert.NotAfter || (e.hasLease && e.lease.Head == e.headHash && e.lease.IssuedAt > cert.IssuedAt)
		upload := func(finalOp *PersistedOperation, finalReqBytes []byte, finalLeaseBytes []byte) {
			e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_SECURITY_STATE, finalOp.Server, "", true, fgGen, finalReqBytes, func(_ []byte, rErr *rpcError) {
				if rErr != nil {
					done(nil, rErr)
					return
				}
				if err := e.checkFG(fgGen); err != nil {
					done(nil, err)
					return
				}
				e.installRecordCAS(nil, nil, nil, finalLeaseBytes, e.lastNowMs, nil, func(_ *PersistedRecord, iErr error) {
					if iErr != nil {
						done(nil, iErr)
						return
					}
					e.clearOperationCAS(finalOp, func(cErr error) {
						done(nil, cErr)
					})
				})
			})
		}
		if needFresh {
			e.buildAndSignLease(e.head, corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_ED, "", e.currentSigningTime(e.lastNowMs), func(fresh devicesec.LeaseCertificate, lErr error) {
				if lErr != nil {
					done(nil, lErr)
					return
				}
				if err := e.checkFG(fgGen); err != nil {
					done(nil, err)
					return
				}
				lb, _ := fresh.MarshalBinary()
				retryBytes, _ := (&v1.PutSecurityStateRequest{Lease: lb}).Marshal()
				next := *curOp
				next.Retry = retryBytes
				e.saveOperationCAS(curOp, &next, func(saved *PersistedOperation, sErr error) {
					if sErr != nil {
						done(nil, sErr)
						return
					}
					upload(saved, retryBytes, lb)
				})
			})
			return
		}
		upload(curOp, reqBytes, req.Lease)
	}

	if len(op.Signed) == 0 {
		e.buildAndSignLease(e.head, corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_ED, "", e.currentSigningTime(e.lastNowMs), func(cert devicesec.LeaseCertificate, lErr error) {
			if lErr != nil {
				done(nil, lErr)
				return
			}
			if err := e.checkFG(fgGen); err != nil {
				done(nil, err)
				return
			}
			lb, _ := cert.MarshalBinary()
			req := &v1.PutSecurityStateRequest{Lease: lb}
			reqBytes, _ := req.Marshal()
			next := *op
			next.Signed = reqBytes
			e.saveOperationCAS(op, &next, func(saved *PersistedOperation, sErr error) {
				if sErr != nil {
					done(nil, sErr)
					return
				}
				publishReq(saved, req, reqBytes)
			})
		})
		return
	}
	raw := op.Retry
	if len(raw) == 0 {
		raw = op.Signed
	}
	var req v1.PutSecurityStateRequest
	if err := req.Unmarshal(raw); err != nil {
		done(nil, devicesec.ErrInvalid)
		return
	}
	publishReq(op, &req, raw)
}

func (e *Engine) resumeStateOp(op *PersistedOperation, fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	if len(op.Signed) == 0 {
		if op.Action == "recovery" && !op.CodeSaved {
			done(nil, nil)
			return
		}
		e.signStateDraft(op, fgGen, func(saved *PersistedOperation, sErr error) {
			if sErr != nil {
				done(nil, sErr)
				return
			}
			e.continueStateOpAfterSign(saved, fgGen, done)
		})
		return
	}
	e.continueStateOpAfterSign(op, fgGen, done)
}

func (e *Engine) signStateDraft(op *PersistedOperation, fgGen uint64, fn func(saved *PersistedOperation, err error)) {
	e.syncStates(false, "", func(sErr error) {
		if sErr != nil {
			fn(nil, sErr)
			return
		}
		if err := e.checkFG(fgGen); err != nil {
			fn(nil, err)
			return
		}
		if !e.isAuthority() || e.headHash != op.Parent {
			fn(nil, devicesec.ErrStale)
			return
		}
		// Pre-validate pairing draft before signing.
		if op.Pair != nil {
			if op.Pair.CandidateID == e.selfDeviceID {
				fn(nil, devicesec.ErrUntrusted)
				return
			}
			if uint64(op.Pair.ExpiresMs) <= e.effectiveNow(e.lastNowMs) {
				fn(nil, newWfError("expired", text.KeySecurityTheUnpublishedPairingDraftExpiredYouCanExplicitlyAbandon))
				return
			}
			gReq, _ := (&v1.GetPairingRequest{PairingId: op.Pair.PairingID}).Marshal()
			e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_PAIRING, op.Server, "", true, fgGen, gReq, func(respBytes []byte, rErr *rpcError) {
				if rErr != nil {
					fn(nil, rErr)
					return
				}
				var st v1.GetPairingResponse
				if err := st.Unmarshal(respBytes); err != nil {
					fn(nil, devicesec.ErrInvalid)
					return
				}
				if st.State != v1.PairingState_PAIRING_STATE_REVEALED ||
					uint32(st.JoinerKind) != op.Pair.CandidateKind ||
					(op.Pair.ReplacesDeviceID != "" && st.JoinerKind != v1.DeviceKind_DEVICE_KIND_MACHINE) ||
					!sameBytes(st.DevicePublicKey, op.Pair.CandidateEd) ||
					!sameBytes(st.DeviceHpkePublicKey, op.Pair.CandidateX) {
					fn(nil, newWfError("canceled", text.KeySecurityTheUnpublishedPairingDraftWasCanceledOrItsCandidate))
					return
				}
				e.buildAndPersistStateDraft(op, fgGen, fn)
			})
			return
		}
		e.buildAndPersistStateDraft(op, fgGen, fn)
	})
}

func (e *Engine) buildAndPersistStateDraft(op *PersistedOperation, fgGen uint64, fn func(saved *PersistedOperation, err error)) {
	oldSecret, ok := e.epochSecrets[e.head.Core.ContentEpoch]
	if !ok {
		fn(nil, ErrNotInitialized)
		return
	}
	e.withEntropy(128, func() {
		if err := e.checkFG(fgGen); err != nil {
			fn(nil, err)
			return
		}
		var newSecret [32]byte
		if !e.readRandom(newSecret[:]) {
			fn(nil, ErrNeedEntropy)
			return
		}
		now := e.currentSigningTime(e.lastNowMs)
		parent := e.head

		var kemMembers []devicesec.Member
		var buildFn func(devicesec.HPKESender) (devicesec.State, error)

		switch op.Action {
		case "remove":
			var members []devicesec.Member
			for _, m := range parent.Core.Members {
				if m.ID != op.TargetDeviceID {
					members = append(members, m)
				}
			}
			removed := append(slices.Clone(parent.Core.Removed), op.TargetDeviceID)
			slices.Sort(removed)
			kemMembers = members
			buildFn = func(hpke devicesec.HPKESender) (devicesec.State, error) {
				return devicesec.NewMembershipState(parent, members, removed, oldSecret[:], newSecret[:], now, hpke, e.entropy)
			}
		case "recovery":
			if len(op.RecoveryEd) != 32 {
				fn(nil, devicesec.ErrInvalid)
				return
			}
			var recEd [32]byte
			copy(recEd[:], op.RecoveryEd)
			kemMembers = parent.Core.Members
			buildFn = func(hpke devicesec.HPKESender) (devicesec.State, error) {
				return devicesec.NewRecoveryKeyRotation(parent, oldSecret[:], recEd, now, hpke)
			}
		case "handoff":
			if len(op.RecoveryEd) != 32 {
				fn(nil, devicesec.ErrInvalid)
				return
			}
			var recEd [32]byte
			copy(recEd[:], op.RecoveryEd)
			for _, m := range parent.Core.Members {
				if m.ID != parent.Core.AuthorityID {
					kemMembers = append(kemMembers, m)
				}
			}
			buildFn = func(hpke devicesec.HPKESender) (devicesec.State, error) {
				return devicesec.NewHandoff(parent, op.TargetDeviceID, recEd, oldSecret[:], newSecret[:], now, hpke, e.entropy)
			}
		case "pair":
			p := op.Pair
			if p == nil {
				fn(nil, devicesec.ErrInvalid)
				return
			}
			cand, err := devicesec.NewMember(p.CandidateEd, p.CandidateX, devicesec.Kind(p.CandidateKind))
			if err != nil {
				fn(nil, err)
				return
			}
			var existing *devicesec.Member
			if m, found := parent.Core.Member(cand.ID); found {
				existing = &m
			}
			if existing != nil {
				if existing.Kind != cand.Kind || existing.EdPublic != cand.EdPublic {
					fn(nil, devicesec.ErrStale)
					return
				}
				members := make([]devicesec.Member, len(parent.Core.Members))
				for i, m := range parent.Core.Members {
					if m.ID == cand.ID {
						members[i] = cand
					} else {
						members[i] = m
					}
				}
				kemMembers = members
				buildFn = func(hpke devicesec.HPKESender) (devicesec.State, error) {
					return devicesec.NewMembershipState(parent, members, parent.Core.Removed, oldSecret[:], newSecret[:], now, hpke, e.entropy)
				}
			} else {
				kemMembers = append(slices.Clone(parent.Core.Members), cand)
				buildFn = func(hpke devicesec.HPKESender) (devicesec.State, error) {
					return devicesec.NewPairState(parent, cand, p.CandidateX, oldSecret[:], newSecret[:], now, hpke, e.entropy)
				}
			}
		default:
			fn(nil, devicesec.ErrInvalid)
			return
		}

		e.buildAndSignStateWithKEM(corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_ED, "", true, fgGen, buildFn, kemMembers, func(nextState devicesec.State, bErr error) {
			if bErr != nil {
				fn(nil, bErr)
				return
			}
			finishWithLease := func(leaseCert *devicesec.LeaseCertificate) {
				nextStateBytes, err := nextState.MarshalBinary()
				if err != nil {
					fn(nil, err)
					return
				}
				var leaseRaw []byte
				if leaseCert != nil {
					leaseRaw, _ = leaseCert.MarshalBinary()
				}
				putReq := &v1.PutSecurityStateRequest{
					States: []*v1.SecurityState{{State: nextStateBytes}},
					Lease:  leaseRaw,
				}
				signedBytes, err := putReq.Marshal()
				if err != nil {
					fn(nil, err)
					return
				}
				saveWithDelivery := func(deliveryBytes []byte) {
					nextOp := *op
					nextOp.Signed = signedBytes
					nextOp.Delivery = deliveryBytes
					e.saveOperationCAS(op, &nextOp, func(saved *PersistedOperation, sErr error) {
						fn(saved, sErr)
					})
				}
				if op.Pair == nil {
					saveWithDelivery(nil)
					return
				}
				e.buildPairDelivery(op, nextStateBytes, leaseRaw, fgGen, func(dBytes []byte, dErr error) {
					if dErr != nil {
						fn(nil, dErr)
						return
					}
					saveWithDelivery(dBytes)
				})
			}

			if nextState.Core.AuthorityID == e.selfDeviceID {
				e.buildAndSignLease(nextState, corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_ED, "", now, func(l devicesec.LeaseCertificate, lErr error) {
					if lErr != nil {
						fn(nil, lErr)
						return
					}
					if err := e.checkFG(fgGen); err != nil {
						fn(nil, err)
						return
					}
					finishWithLease(&l)
				})
				return
			}
			finishWithLease(nil)
		})
	})
}

func (e *Engine) buildPairDelivery(op *PersistedOperation, nextStateBytes, leaseRaw []byte, fgGen uint64, fn func([]byte, error)) {
	wf := e.ensureWF()
	p := op.Pair
	rec := wf.record
	if rec == nil || len(rec.States) == 0 {
		fn(nil, devicesec.ErrInvalid)
		return
	}
	var acctPub []byte
	if len(rec.AccountPublicKey) == 32 {
		acctPub = rec.AccountPublicKey
	} else if prof := wf.publicProfiles[PublicProfileStorageKey(op.Server, e.pin)]; prof != nil {
		acctPub = prof.AccountPublicKey[:]
	}
	if len(acctPub) != 32 {
		fn(nil, newWfError("invalid", text.KeySecurityAccountPublicKeyMissingPairAgainOrUseA))
		return
	}
	authMem, ok := e.head.Core.Member(e.selfDeviceID)
	if !ok {
		fn(nil, devicesec.ErrUntrusted)
		return
	}
	var totalBytes int
	chainStates := make([]*v1.SecurityState, 0, len(rec.States)+1)
	for i, s := range rec.States {
		var rp []byte
		if i < len(rec.RecoveryProofs) {
			rp = rec.RecoveryProofs[i]
		}
		totalBytes += len(s) + len(rp)
		chainStates = append(chainStates, &v1.SecurityState{State: s, Recovery: rp})
	}
	totalBytes += len(nextStateBytes)
	chainStates = append(chainStates, &v1.SecurityState{State: nextStateBytes})
	if len(chainStates) > devicesec.MaxPairStates || totalBytes > devicesec.MaxPairChainBytes {
		fn(nil, devicesec.ErrChainTooLarge)
		return
	}

	e.withEntropy(64, func() {
		if err := e.checkFG(fgGen); err != nil {
			fn(nil, err)
			return
		}
		var nonce [16]byte
		var envNonce [12]byte
		if !e.readRandom(nonce[:]) || !e.readRandom(envNonce[:]) {
			fn(nil, ErrNeedEntropy)
			return
		}
		ap := &v1.PairApproval{
			AccountId:                rec.Pin.Account,
			PairingId:                p.PairingID,
			ApproverDeviceId:         e.selfDeviceID,
			ApproverPublicKey:        slices.Clone(authMem.EdPublic[:]),
			CandidateDevicePublicKey: slices.Clone(p.CandidateEd),
			CandidateKind:            v1.DeviceKind(p.CandidateKind),
			ExpiresMs:                p.ExpiresMs,
			Nonce:                    nonce[:],
			ReplacesDeviceId:         p.ReplacesDeviceID,
		}
		apBytes, err := ap.Marshal()
		if err != nil {
			fn(nil, err)
			return
		}
		signInput := make([]byte, 0, len(PairApprovalContext)+len(apBytes))
		signInput = append(signInput, PairApprovalContext...)
		signInput = append(signInput, apBytes...)
		e.requestKeySignKind(corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_ED, "", signInput, func(sig []byte, sErr error) {
			if sErr != nil {
				fn(nil, sErr)
				return
			}
			if err := e.checkFG(fgGen); err != nil {
				fn(nil, err)
				return
			}
			payloadMsg := &v1.PairingPayload{
				Server:            op.Server,
				Approval:          apBytes,
				ApprovalSignature: sig,
				SecurityStates:    chainStates,
				AccountPublicKey:  slices.Clone(acctPub),
				Lease:             slices.Clone(leaseRaw),
			}
			plain, err := payloadMsg.Marshal()
			if err != nil {
				fn(nil, err)
				return
			}
			hdrBytes, err := (&v1.Header{
				Version:     1,
				Kind:        v1.PayloadKind_PAYLOAD_KIND_PAIRING,
				RequestId:   p.PairingID,
				Sender:      e.selfDeviceID,
				Compression: v1.Compression_COMPRESSION_NONE,
			}).Marshal()
			if err != nil {
				fn(nil, err)
				return
			}
			ct, err := sealPairingEnvelopeAESGCM(p.PairKey, envNonce[:], hdrBytes, plain)
			if err != nil {
				fn(nil, err)
				return
			}
			env := &v1.Envelope{
				Header:     hdrBytes,
				Nonce:      envNonce[:],
				Ciphertext: ct,
			}
			reqBytes, err := (&v1.ApprovePairingRequest{
				PairingId:         p.PairingID,
				Payload:           env,
				Approval:          apBytes,
				ApprovalSignature: sig,
			}).Marshal()
			if err != nil {
				fn(nil, err)
				return
			}
			fn(reqBytes, nil)
		})
	})
}

func (e *Engine) continueStateOpAfterSign(op *PersistedOperation, fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	var orig v1.PutSecurityStateRequest
	if err := orig.Unmarshal(op.Signed); err != nil || len(orig.States) == 0 {
		done(nil, devicesec.ErrInvalid)
		return
	}
	targetBytes := orig.States[len(orig.States)-1].State
	targetState, err := devicesec.ParseState(targetBytes)
	if err != nil {
		done(nil, err)
		return
	}
	targetHash, _ := targetState.Hash()

	// Handoff self-revocation reconciliation: target already verified in local chain & moved authority.
	if !e.isMember && e.byHash[targetHash].Core.Epoch == targetState.Core.Epoch && targetState.Core.AuthorityID != e.selfDeviceID {
		delivered := *op
		delivered.Phase = "DELIVERED"
		e.saveOperationCAS(op, &delivered, func(saved *PersistedOperation, sErr error) {
			if sErr != nil {
				done(nil, sErr)
				return
			}
			if err := e.checkFG(fgGen); err != nil {
				done(nil, err)
				return
			}
			e.clearOperationCAS(saved, func(cErr error) {
				done(nil, cErr)
			})
		})
		return
	}

	e.syncStates(false, "", func(sErr error) {
		if sErr != nil {
			done(nil, sErr)
			return
		}
		if err := e.checkFG(fgGen); err != nil {
			done(nil, err)
			return
		}
		var installed bool
		if int(targetState.Core.Epoch) <= len(e.states) && targetState.Core.Epoch >= 1 {
			cand := e.states[targetState.Core.Epoch-1]
			ch, _ := cand.Hash()
			if ch != targetHash {
				done(nil, devicesec.ErrStale)
				return
			}
			installed = true
		} else if e.headHash != op.Parent {
			done(nil, devicesec.ErrStale)
			return
		}

		finishDeliveryAndClear := func(curOp *PersistedOperation) {
			delivered := *curOp
			delivered.Phase = "DELIVERED"
			e.saveOperationCAS(curOp, &delivered, func(saved *PersistedOperation, sErr error) {
				if sErr != nil {
					done(nil, sErr)
					return
				}
				if err := e.checkFG(fgGen); err != nil {
					done(nil, err)
					return
				}
				if saved.Action == "recovery" && saved.CodeSaved {
					clearFlag := false
					e.installRecordCAS(nil, nil, nil, nil, 0, &struct {
						needsRotation          *bool
						recoveryObjectsPending *bool
					}{needsRotation: &clearFlag}, func(_ *PersistedRecord, iErr error) {
						if iErr != nil {
							done(nil, iErr)
							return
						}
						e.clearOperationCAS(saved, func(cErr error) {
							done(nil, cErr)
						})
					})
					return
				}
				e.clearOperationCAS(saved, func(cErr error) {
					done(nil, cErr)
				})
			})
		}

		if op.Phase == "DELIVERED" {
			if !installed {
				done(nil, devicesec.ErrStale)
				return
			}
			if op.Action == "recovery" && op.CodeSaved {
				clearFlag := false
				e.installRecordCAS(nil, nil, nil, nil, 0, &struct {
					needsRotation          *bool
					recoveryObjectsPending *bool
				}{needsRotation: &clearFlag}, func(_ *PersistedRecord, iErr error) {
					if iErr != nil {
						done(nil, iErr)
						return
					}
					e.clearOperationCAS(op, func(cErr error) {
						done(nil, cErr)
					})
				})
				return
			}
			e.clearOperationCAS(op, func(cErr error) {
				done(nil, cErr)
			})
			return
		}

		deliverIfNeeded := func(curOp *PersistedOperation) {
			pubOp := *curOp
			pubOp.Phase = "PUBLISHED"
			e.saveOperationCAS(curOp, &pubOp, func(saved *PersistedOperation, sErr error) {
				if sErr != nil {
					done(nil, sErr)
					return
				}
				if len(saved.Delivery) == 0 {
					finishDeliveryAndClear(saved)
					return
				}
				var delivery v1.ApprovePairingRequest
				if err := delivery.Unmarshal(saved.Delivery); err != nil {
					done(nil, devicesec.ErrInvalid)
					return
				}
				e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_APPROVE_PAIRING, saved.Server, "", true, fgGen, saved.Delivery, func(_ []byte, aErr *rpcError) {
					checkFallback := func(origErr *rpcError) {
						if origErr.code != connectCodeNotFound && origErr.code != connectCodeFailedPrecondition && origErr.code != connectCodeCanceled {
							done(nil, origErr)
							return
						}
						listReq, _ := (&v1.ListDevicesRequest{}).Marshal()
						e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LIST_DEVICES, saved.Server, "", true, fgGen, listReq, func(lRespBytes []byte, lErr *rpcError) {
							if lErr != nil {
								done(nil, lErr)
								return
							}
							var lr v1.ListDevicesResponse
							if err := lr.Unmarshal(lRespBytes); err != nil {
								done(nil, devicesec.ErrInvalid)
								return
							}
							ap, err := VerifyPairApproval(delivery.Approval, delivery.ApprovalSignature)
							if err != nil {
								done(nil, err)
								return
							}
							candID := devicesec.ID(ap.CandidateDevicePublicKey)
							for _, d := range lr.Devices {
								if d != nil && d.Id == candID {
									finishDeliveryAndClear(saved)
									return
								}
							}
							done(nil, fatalWfError("pairing_delivery", text.KeySecurityPairingDeliveryExpiredExplicitlyRemoveTheUnfinishedCandidateBefore, "pairing_delivery"))
						})
					}
					if aErr != nil {
						checkFallback(aErr)
						return
					}
					getReq, _ := (&v1.GetPairingRequest{PairingId: delivery.PairingId}).Marshal()
					e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_PAIRING, saved.Server, "", true, fgGen, getReq, func(gRespBytes []byte, gErr *rpcError) {
						if gErr != nil {
							checkFallback(gErr)
							return
						}
						var gr v1.GetPairingResponse
						if err := gr.Unmarshal(gRespBytes); err != nil {
							done(nil, devicesec.ErrInvalid)
							return
						}
						switch gr.State {
						case v1.PairingState_PAIRING_STATE_DELIVERED:
							finishDeliveryAndClear(saved)
						case v1.PairingState_PAIRING_STATE_CANCELED:
							done(nil, fatalWfError("pairing_delivery", text.KeySecurityPairingWasCanceledExplicitlyRemoveTheUnfinishedCandidateBefore, "pairing_delivery"))
						default:
							// Waiting for joiner delivery confirmation.
							done(nil, nil)
						}
					})
				})
			})
		}

		// Determine current request (orig or retry) & check independent lease freshness.
		reqRaw := op.Retry
		if len(reqRaw) == 0 {
			reqRaw = op.Signed
		}
		var req v1.PutSecurityStateRequest
		if err := req.Unmarshal(reqRaw); err != nil {
			done(nil, devicesec.ErrInvalid)
			return
		}
		var leaseCert *devicesec.LeaseCertificate
		if len(req.Lease) > 0 {
			lc, err := devicesec.ParseLease(req.Lease)
			if err != nil || lc.Verify(targetState) != nil {
				done(nil, devicesec.ErrInvalid)
				return
			}
			leaseCert = &lc
		}
		effNow := e.effectiveNow(e.lastNowMs)
		needLeaseRefresh := (!installed || e.headHash == targetHash) &&
			targetState.Core.AuthorityID == e.selfDeviceID &&
			(leaseCert == nil || effNow >= leaseCert.NotAfter ||
				(e.hasLease && e.lease.Head == targetHash && e.lease.IssuedAt > leaseCert.IssuedAt))

		proceedPublish := func(curOp *PersistedOperation, curReq *v1.PutSecurityStateRequest, curReqBytes []byte) {
			if !installed {
				e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_SECURITY_STATE, curOp.Server, "", true, fgGen, curReqBytes, func(respBytes []byte, rErr *rpcError) {
					if rErr != nil {
						done(nil, rErr)
						return
					}
					var resp v1.PutSecurityStateResponse
					if err := resp.Unmarshal(respBytes); err != nil || resp.HeadEpoch != targetState.Core.Epoch {
						done(nil, devicesec.ErrStale)
						return
					}
					respHead, err := devicesec.ParseState(resp.Head)
					if err != nil {
						done(nil, devicesec.ErrStale)
						return
					}
					rhh, _ := respHead.Hash()
					if rhh != targetHash {
						done(nil, devicesec.ErrStale)
						return
					}
					e.installRecordCAS([]devicesec.State{targetState}, [][]byte{targetBytes}, [][]byte{nil}, curReq.Lease, e.lastNowMs, nil, func(_ *PersistedRecord, iErr error) {
						if iErr != nil {
							done(nil, iErr)
							return
						}
						deliverIfNeeded(curOp)
					})
				})
				return
			}
			if e.headHash == targetHash && len(curReq.Lease) > 0 {
				reqLease, err := devicesec.ParseLease(curReq.Lease)
				if err == nil && (!e.hasLease || e.lease.IssuedAt < reqLease.IssuedAt) {
					e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_SECURITY_STATE, curOp.Server, "", true, fgGen, curReqBytes, func(_ []byte, rErr *rpcError) {
						if rErr != nil {
							done(nil, rErr)
							return
						}
						e.installRecordCAS(nil, nil, nil, curReq.Lease, e.lastNowMs, nil, func(_ *PersistedRecord, iErr error) {
							if iErr != nil {
								done(nil, iErr)
								return
							}
							deliverIfNeeded(curOp)
						})
					})
					return
				}
			}
			deliverIfNeeded(curOp)
		}

		if needLeaseRefresh {
			signAt := e.currentSigningTime(e.lastNowMs)
			if targetState.Core.IssuedAt > signAt {
				signAt = targetState.Core.IssuedAt
			}
			e.buildAndSignLease(targetState, corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_ED, "", signAt, func(fresh devicesec.LeaseCertificate, lErr error) {
				if lErr != nil {
					done(nil, lErr)
					return
				}
				if err := e.checkFG(fgGen); err != nil {
					done(nil, err)
					return
				}
				lb, _ := fresh.MarshalBinary()
				newReq := &v1.PutSecurityStateRequest{
					States: orig.States,
					Lease:  lb,
				}
				newReqBytes, _ := newReq.Marshal()
				nextOp := *op
				nextOp.Retry = newReqBytes
				e.saveOperationCAS(op, &nextOp, func(saved *PersistedOperation, sErr error) {
					if sErr != nil {
						done(nil, sErr)
						return
					}
					proceedPublish(saved, newReq, newReqBytes)
				})
			})
			return
		}
		proceedPublish(op, &req, reqRaw)
	})
}

func (e *Engine) discardPendingOp(fgGen uint64, acknowledgePublished bool, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	if err := e.checkFG(fgGen); err != nil {
		done(nil, err)
		return
	}
	op := wf.pendingOp
	if op == nil {
		wf.codeDraft = nil
		done(nil, nil)
		return
	}
	switch {
	case op.Kind == "offer" && op.Phase == "PREPARED":
		if !acknowledgePublished && op.CodeSaved && len(op.Signed) > 0 {
			done(nil, devicesec.ErrStale)
			return
		}
	case op.Kind == "create" && op.Phase == "PREPARED" && !op.CodeSaved:
		if op.Create != nil {
			if op.Create.EdSlot != "" {
				e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, op.Create.EdSlot)
			}
			if op.Create.XSlot != "" {
				e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_X, op.Create.XSlot)
			}
		}
	case op.Kind == "state" && op.Phase == "PREPARED" && len(op.Signed) == 0 && !(op.Action == "recovery" && op.CodeSaved):
		// Safe to discard unsigned state draft.
	case op.Kind == "recovery" && op.Phase == "PREPARED" && len(op.Signed) == 0:
		if op.Recovery != nil {
			if op.Recovery.EdSlot != "" {
				e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, op.Recovery.EdSlot)
			}
			if op.Recovery.XSlot != "" {
				e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_X, op.Recovery.XSlot)
			}
		}
	default:
		done(nil, devicesec.ErrStale)
		return
	}
	e.clearOperationCAS(op, func(err error) {
		done(nil, err)
	})
}
