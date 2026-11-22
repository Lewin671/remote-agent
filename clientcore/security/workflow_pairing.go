package security

import (
	"slices"
	"strings"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

func (e *Engine) scheduleSecurityPollTimer() {
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_ScheduleTimer{
			ScheduleTimer: &corev1.ScheduleTimerEffect{
				Kind:    corev1.TimerKind_TIMER_KIND_SECURITY_POLL,
				Key:     "security-poll",
				DelayMs: pairPollIntervalMs,
			},
		},
	})
}

func (e *Engine) cancelSecurityPollTimer() {
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_CancelTimer{
			CancelTimer: &corev1.CancelTimerEffect{
				Kind: corev1.TimerKind_TIMER_KIND_SECURITY_POLL,
				Key:  "security-poll",
			},
		},
	})
}

func (e *Engine) clearActivePollWithError(err error) {
	wf := e.ensureWF()
	p := wf.activePoll
	wf.activePoll = nil
	e.cancelSecurityPollTimer()
	if p != nil && p.onResult != nil && err != nil {
		p.onResult(nil, err)
	}
}

func (e *Engine) resetWorkflowPairingSessions() {
	wf := e.ensureWF()
	e.clearActivePollWithError(fatalWfError("canceled", text.KeySecurityPairingCanceled, ""))
	for id, s := range wf.initPairings {
		if s != nil && s.ephemSlot != "" {
			e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, s.ephemSlot)
		}
		delete(wf.initPairings, id)
	}
	for id, s := range wf.joinPairings {
		if s != nil {
			if s.ephemSlot != "" {
				e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, s.ephemSlot)
			}
			if !s.reusesDeviceEd && s.edSlot != "" {
				e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, s.edSlot)
			}
			if s.xSlot != "" {
				e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_X, s.xSlot)
			}
		}
		delete(wf.joinPairings, id)
	}
}

func initiatorSessionToProto(s *initiatorPairingSession) *corev1.SecurityPairingInfo {
	if s == nil {
		return nil
	}
	return &corev1.SecurityPairingInfo{
		PairingId:  s.pairingID,
		Code:       s.code,
		Url:        s.url,
		ExpiresMs:  s.expiresMs,
		SasEmoji:   slices.Clone(s.sasEmoji),
		JoinerKind: s.candidateKind,
		Joined:     s.joined,
		Approved:   s.approved,
		Delivered:  s.delivered,
	}
}

func joinerSessionToProto(s *joinerPairingSession) *corev1.SecurityPairingInfo {
	if s == nil {
		return nil
	}
	return &corev1.SecurityPairingInfo{
		PairingId:   s.pairingID,
		Code:        s.code,
		SasEmoji:    slices.Clone(s.sasEmoji),
		JoinerKind:  v1.DeviceKind_DEVICE_KIND_CLIENT,
		Joined:      true,
		Registering: s.registering,
	}
}

func (e *Engine) startPairingOp(fgGen uint64, webOrigin, replacesDeviceID string, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	if err := e.checkFG(fgGen); err != nil {
		done(nil, err)
		return
	}
	if wf.pendingOp != nil {
		done(nil, newWfError("pending", text.KeySecurityFinishTheCurrentSecurityOperationFirst))
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
		if !e.isAuthority() || wf.record == nil {
			done(nil, devicesec.ErrUntrusted)
			return
		}
		server := e.effectiveServer()
		var acctPub []byte
		if len(wf.record.AccountPublicKey) == 32 {
			acctPub = wf.record.AccountPublicKey
		} else if prof := wf.publicProfiles[PublicProfileStorageKey(server, e.pin)]; prof != nil {
			acctPub = prof.AccountPublicKey[:]
		}
		if len(acctPub) != 32 {
			done(nil, newWfError("invalid", text.KeySecurityAccountPublicKeyMissingPairAgainOrUseA))
			return
		}
		trust, err := devicesec.PairTrustDescriptor(e.pin, e.selfDeviceID)
		if err != nil {
			done(nil, err)
			return
		}

		beginCreate := func() {
			ephemSlot := "pair-init-" + e.nextWorkflowToken()
			e.withEntropy(32, func() {
				if err := e.checkFG(fgGen); err != nil {
					done(nil, err)
					return
				}
				e.reqKeyGen(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, ephemSlot, false, func(ephemPub []byte, kErr error) {
					if kErr != nil {
						done(nil, kErr)
						return
					}
					if len(ephemPub) != 32 {
						e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, ephemSlot)
						done(nil, devicesec.ErrInvalid)
						return
					}
					if err := e.checkFG(fgGen); err != nil {
						e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, ephemSlot)
						done(nil, err)
						return
					}
					commit := devicesec.PairDomainCommit(ephemPub, trust)
					reqBytes, err := (&v1.CreatePairingRequest{Commit: commit}).Marshal()
					if err != nil {
						e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, ephemSlot)
						done(nil, err)
						return
					}
					e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_CREATE_PAIRING, server, "", true, fgGen, reqBytes, func(respBytes []byte, rErr *rpcError) {
						if rErr != nil {
							e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, ephemSlot)
							done(nil, rErr)
							return
						}
						var resp v1.CreatePairingResponse
						if err := resp.Unmarshal(respBytes); err != nil || resp.PairingId == "" || resp.Code == "" {
							e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, ephemSlot)
							done(nil, devicesec.ErrInvalid)
							return
						}
						if len(resp.ApproverPublicKey) > 0 && wf.deviceEdPresent && !sameBytes(resp.ApproverPublicKey, wf.deviceEdPub) {
							e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, ephemSlot)
							done(nil, devicesec.ErrUntrusted)
							return
						}
						var commitArr [32]byte
						copy(commitArr[:], commit)
						sess := &initiatorPairingSession{
							pairingID:        resp.PairingId,
							code:             resp.Code,
							url:              FormatPairLink(webOrigin, server, resp.Code, commitArr),
							server:           server,
							expiresMs:        resp.ExpiresMs,
							ephemSlot:        ephemSlot,
							ephemPub:         slices.Clone(ephemPub),
							trust:            trust,
							replacesDeviceID: replacesDeviceID,
						}
						wf.initPairings[resp.PairingId] = sess
						done(&corev1.SecurityWorkflowResultEffect{
							Pairing: initiatorSessionToProto(sess),
						}, nil)
					})
				})
			})
		}

		if replacesDeviceID != "" {
			if _, live := e.head.Core.Member(replacesDeviceID); live || !slices.Contains(e.head.Core.Removed, replacesDeviceID) {
				done(nil, devicesec.ErrUntrusted)
				return
			}
			var wasMachine bool
			for _, s := range e.states {
				if m, ok := s.Core.Member(replacesDeviceID); ok && m.Kind == devicesec.Machine {
					wasMachine = true
					break
				}
			}
			if !wasMachine {
				done(nil, devicesec.ErrUntrusted)
				return
			}
			lmReq, _ := (&v1.ListMachinesRequest{}).Marshal()
			e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LIST_MACHINES, server, "", true, fgGen, lmReq, func(respBytes []byte, lErr *rpcError) {
				if lErr != nil {
					done(nil, lErr)
					return
				}
				var lmr v1.ListMachinesResponse
				if err := lmr.Unmarshal(respBytes); err != nil {
					done(nil, devicesec.ErrInvalid)
					return
				}
				var hasArchive bool
				for _, m := range lmr.Machines {
					if m != nil && m.Id == replacesDeviceID {
						hasArchive = true
						break
					}
				}
				if !hasArchive {
					done(nil, newWfError("not_found", text.KeySecurityTheComputerSArchiveWasDeletedJoinAsA))
					return
				}
				beginCreate()
			})
			return
		}
		beginCreate()
	})
}

func (e *Engine) pollPairingInitiatorOp(fgGen uint64, pairingID string, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	if err := e.checkFG(fgGen); err != nil {
		done(nil, err)
		return
	}
	sess := wf.initPairings[pairingID]
	if sess == nil {
		done(nil, newWfError("not_found", text.KeySecurityPairingExpiredOrWasCanceled))
		return
	}
	if !e.isAuthority() {
		done(nil, devicesec.ErrUntrusted)
		return
	}
	gReq, _ := (&v1.GetPairingRequest{PairingId: pairingID}).Marshal()
	e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_PAIRING, sess.server, "", true, fgGen, gReq, func(respBytes []byte, rErr *rpcError) {
		if rErr != nil {
			if rErr.code == connectCodeNotFound {
				if sess.ephemSlot != "" {
					e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, sess.ephemSlot)
					sess.ephemSlot = ""
				}
				delete(wf.initPairings, pairingID)
				done(nil, fatalWfError("canceled", text.KeySecurityPairingExpiredOrWasCanceled, ""))
				return
			}
			done(nil, rErr)
			return
		}
		var st v1.GetPairingResponse
		if err := st.Unmarshal(respBytes); err != nil {
			done(nil, devicesec.ErrInvalid)
			return
		}
		switch st.State {
		case v1.PairingState_PAIRING_STATE_CANCELED:
			if sess.ephemSlot != "" {
				e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, sess.ephemSlot)
				sess.ephemSlot = ""
			}
			delete(wf.initPairings, pairingID)
			done(nil, fatalWfError("canceled", text.KeySecurityPairingExpiredOrWasCanceled, ""))
			return
		case v1.PairingState_PAIRING_STATE_DELIVERED:
			if sess.ephemSlot != "" {
				e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, sess.ephemSlot)
				sess.ephemSlot = ""
			}
			sess.delivered = true
			sess.approved = true
			sess.joined = true
			delete(wf.initPairings, pairingID)
			done(&corev1.SecurityWorkflowResultEffect{
				Pairing: initiatorSessionToProto(sess),
			}, nil)
			return
		case v1.PairingState_PAIRING_STATE_JOINED:
			if sess.joined {
				done(&corev1.SecurityWorkflowResultEffect{
					Pairing: initiatorSessionToProto(sess),
				}, nil)
				return
			}
			if len(st.JoinerPublic) != 32 || len(st.DevicePublicKey) != 32 || len(st.DeviceHpkePublicKey) != 32 ||
				(st.JoinerKind != v1.DeviceKind_DEVICE_KIND_MACHINE && st.JoinerKind != v1.DeviceKind_DEVICE_KIND_CLIENT) ||
				(sess.replacesDeviceID != "" && st.JoinerKind != v1.DeviceKind_DEVICE_KIND_MACHINE) {
				done(nil, devicesec.ErrUntrusted)
				return
			}
			candID := devicesec.ID(st.DevicePublicKey)
			if !devicesec.IsDeviceID(candID) || candID == e.selfDeviceID {
				done(nil, devicesec.ErrUntrusted)
				return
			}
			if slices.Contains(e.head.Core.Removed, candID) {
				done(nil, devicesec.ErrUntrusted)
				return
			}
			if existing, ok := e.head.Core.Member(candID); ok {
				if existing.Kind != devicesec.Kind(st.JoinerKind) || !sameBytes(existing.EdPublic[:], st.DevicePublicKey) || sess.replacesDeviceID != "" {
					done(nil, devicesec.ErrUntrusted)
					return
				}
			}
			var peerX32 [32]byte
			copy(peerX32[:], st.JoinerPublic)
			e.requestKeyAgreeKind(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, sess.ephemSlot, peerX32, false, func(shared []byte, aErr error) {
				if aErr != nil {
					done(nil, aErr)
					return
				}
				if err := e.checkFG(fgGen); err != nil {
					done(nil, err)
					return
				}
				transcript := devicesec.PairDomainTranscript(
					sess.pairingID,
					sess.ephemPub,
					st.JoinerPublic,
					st.DevicePublicKey,
					st.DeviceHpkePublicKey,
					int32(st.JoinerKind),
					sess.trust,
				)
				ps, err := devicesec.DerivePairSecrets(shared, transcript)
				if err != nil {
					done(nil, err)
					return
				}
				revBytes, _ := (&v1.RevealPairingRequest{
					PairingId:       sess.pairingID,
					InitiatorPublic: slices.Clone(sess.ephemPub),
					Trust:           slices.Clone(sess.trust),
				}).Marshal()
				e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_REVEAL_PAIRING, sess.server, "", true, fgGen, revBytes, func(_ []byte, rvErr *rpcError) {
					if rvErr != nil {
						done(nil, rvErr)
						return
					}
					if sess.ephemSlot != "" {
						e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, sess.ephemSlot)
						sess.ephemSlot = ""
					}
					sess.joined = true
					sess.candidateID = candID
					sess.candidateEd = slices.Clone(st.DevicePublicKey)
					sess.candidateX = slices.Clone(st.DeviceHpkePublicKey)
					sess.candidateKind = st.JoinerKind
					sess.pairKey = slices.Clone(ps.Key)
					sess.sasEmoji = ps.Emoji()
					done(&corev1.SecurityWorkflowResultEffect{
						Pairing: initiatorSessionToProto(sess),
					}, nil)
				})
			})
			return
		case v1.PairingState_PAIRING_STATE_APPROVED:
			sess.approved = true
			sess.joined = true
			done(&corev1.SecurityWorkflowResultEffect{
				Pairing: initiatorSessionToProto(sess),
			}, nil)
			return
		default:
			done(&corev1.SecurityWorkflowResultEffect{
				Pairing: initiatorSessionToProto(sess),
			}, nil)
		}
	})
}

func (e *Engine) approvePairingOp(fgGen uint64, pairingID string, sasConfirmed bool, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	if err := e.checkFG(fgGen); err != nil {
		done(nil, err)
		return
	}
	if !sasConfirmed {
		done(nil, newWfError("invalid", text.KeySecurityConfirmThePatternsOnTheNewDeviceBeforeApproving))
		return
	}
	sess := wf.initPairings[pairingID]
	if sess == nil || !sess.joined || len(sess.pairKey) != 32 || len(sess.candidateEd) != 32 || len(sess.candidateX) != 32 {
		done(nil, devicesec.ErrStale)
		return
	}
	if wf.pendingOp != nil {
		done(nil, newWfError("pending", text.KeySecurityFinishTheCurrentSecurityOperationFirst))
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
		if !e.isAuthority() {
			done(nil, devicesec.ErrUntrusted)
			return
		}
		exp := sess.expiresMs
		if e.lastNowMs > 0 && (exp == 0 || exp > e.lastNowMs+maxPairWindowMs) {
			exp = e.lastNowMs + maxPairWindowMs
		}
		hh := e.headHash
		op := &PersistedOperation{
			ID:             e.nextWorkflowToken(),
			Revision:       1,
			Server:         sess.server,
			MemberID:       e.selfDeviceID,
			Kind:           "state",
			Parent:         hh,
			Phase:          "PREPARED",
			Action:         "pair",
			TargetDeviceID: sess.candidateID,
			Pair: &PersistedOpPair{
				PairingID:        sess.pairingID,
				CandidateID:      sess.candidateID,
				CandidateEd:      slices.Clone(sess.candidateEd),
				CandidateX:       slices.Clone(sess.candidateX),
				CandidateKind:    uint32(sess.candidateKind),
				PairKey:          slices.Clone(sess.pairKey),
				ExpiresMs:        exp,
				ReplacesDeviceID: sess.replacesDeviceID,
			},
		}
		e.claimOperationCAS(op, &hh, e.selfDeviceID, func(_ *PersistedOperation, cErr error) {
			if cErr != nil {
				done(nil, cErr)
				return
			}
			e.resumePendingOp(fgGen, func(res *corev1.SecurityWorkflowResultEffect, rErr error) {
				if rErr != nil {
					done(res, rErr)
					return
				}
				sess.approved = true
				if wf.pendingOp == nil {
					sess.delivered = true
					delete(wf.initPairings, pairingID)
				}
				done(&corev1.SecurityWorkflowResultEffect{
					Pairing: initiatorSessionToProto(sess),
				}, nil)
			})
		})
	})
}

func (e *Engine) finishPairingDeliveryOp(fgGen uint64, pairingID string, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	if err := e.checkFG(fgGen); err != nil {
		done(nil, err)
		return
	}
	if op := wf.pendingOp; op != nil && op.Kind == "state" && op.Pair != nil && (pairingID == "" || op.Pair.PairingID == pairingID) {
		pid := op.Pair.PairingID
		e.resumePendingOp(fgGen, func(res *corev1.SecurityWorkflowResultEffect, err error) {
			if err != nil {
				done(res, err)
				return
			}
			sess := wf.initPairings[pid]
			if wf.pendingOp == nil {
				if sess != nil {
					sess.delivered = true
					sess.approved = true
					delete(wf.initPairings, pid)
				} else {
					sess = &initiatorPairingSession{pairingID: pid, approved: true, delivered: true}
				}
			}
			done(&corev1.SecurityWorkflowResultEffect{
				Pairing: initiatorSessionToProto(sess),
			}, nil)
		})
		return
	}
	e.pollPairingInitiatorOp(fgGen, pairingID, done)
}

func (e *Engine) cancelPairingInitiatorOp(pairingID string, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	sess := wf.initPairings[pairingID]
	if sess != nil {
		if sess.ephemSlot != "" {
			e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, sess.ephemSlot)
			sess.ephemSlot = ""
		}
		delete(wf.initPairings, pairingID)
	}
	reqBytes, _ := (&v1.CancelPairingRequest{PairingId: pairingID}).Marshal()
	srv := e.effectiveServer()
	if sess != nil && sess.server != "" {
		srv = sess.server
	}
	cleanupDraft := func() {
		if op := wf.pendingOp; op != nil && op.Pair != nil && op.Pair.PairingID == pairingID && op.Phase == "PREPARED" && len(op.Signed) == 0 {
			e.clearOperationCAS(op, func(error) {
				done(nil, nil)
			})
			return
		}
		done(nil, nil)
	}
	if srv == "" {
		cleanupDraft()
		return
	}
	e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_CANCEL_PAIRING, srv, "", false, 0, reqBytes, func(_ []byte, _ *rpcError) {
		cleanupDraft()
	})
}

func (e *Engine) startJoinPairingAction(actionID uint64, act *corev1.SecurityStartJoinPairingAction) {
	wf := e.ensureWF()
	if act == nil {
		e.emitActionResult(actionID, nil, devicesec.ErrInvalid)
		return
	}
	rawServer := act.Server
	code := strings.ReplaceAll(strings.TrimSpace(act.Code), " ", "")
	commitHint := slices.Clone(act.CommitHint)
	if strings.TrimSpace(act.RawLink) != "" {
		lc, ls, lh, ok := ParsePairLink(act.RawLink)
		if !ok {
			e.emitActionResult(actionID, nil, newWfError("invalid", text.KeySecurityInvalidPairingKey))
			return
		}
		code = lc
		rawServer = ls
		if len(lh) == 32 {
			commitHint = lh
		}
	}
	server, err := CheckServerURL(rawServer)
	if err != nil {
		e.emitActionResult(actionID, nil, err)
		return
	}
	if code == "" {
		e.emitActionResult(actionID, nil, devicesec.ErrInvalid)
		return
	}
	if wf.bootstrapInvalid || wf.pendingOp != nil || wf.regIntent != nil {
		e.emitActionResult(actionID, nil, newWfError("pending", text.KeySecurityFinishTheCurrentSecurityOperationFirst))
		return
	}
	if len(commitHint) != 0 && len(commitHint) != 32 {
		e.emitActionResult(actionID, nil, devicesec.ErrUntrusted)
		return
	}
	if act.ExistingRepair {
		if act.ExistingServer != "" && act.ExistingServer != server {
			e.emitActionResult(actionID, nil, devicesec.ErrUntrusted)
			return
		}
		if wf.record == nil || wf.record.Server != server || !wf.deviceEdPresent || len(wf.deviceEdPub) != 32 {
			e.emitActionResult(actionID, nil, devicesec.ErrUntrusted)
			return
		}
	} else if wf.record != nil && wf.record.Member {
		e.emitActionResult(actionID, nil, devicesec.ErrStale)
		return
	}

	// Cancel any superseded join attempt.
	e.clearActivePollWithError(fatalWfError("canceled", text.KeySecurityPairingCanceled, ""))
	for id, old := range wf.joinPairings {
		if old != nil {
			if old.ephemSlot != "" {
				e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, old.ephemSlot)
			}
			if !old.reusesDeviceEd && old.edSlot != "" {
				e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, old.edSlot)
			}
			if old.xSlot != "" {
				e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_X, old.xSlot)
			}
		}
		delete(wf.joinPairings, id)
	}

	tok := e.nextWorkflowToken()
	reusesEd := act.ExistingRepair
	edSlot := ""
	if !reusesEd {
		edSlot = "join-ed-" + tok
	}
	xSlot := "join-x-" + tok
	ephemSlot := "join-ephem-" + tok

	discardSlots := func() {
		e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, ephemSlot)
		if !reusesEd && edSlot != "" {
			e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, edSlot)
		}
		e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_X, xSlot)
	}

	withCandidateKeys := func(edPub, xPub, ephemPub []byte) {
		peekReq, _ := (&v1.PeekPairingRequest{
			Code:            code,
			ProtocolVersion: protocolVersion,
		}).Marshal()
		e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PEEK_PAIRING, server, "", peekReq, func(pBytes []byte, pErr *rpcError) {
			if pErr != nil {
				discardSlots()
				e.emitActionResult(actionID, nil, pErr)
				return
			}
			var pr v1.PeekPairingResponse
			if err := pr.Unmarshal(pBytes); err != nil || pr.PairingId == "" || len(pr.Commit) != 32 {
				discardSlots()
				e.emitActionResult(actionID, nil, devicesec.ErrInvalid)
				return
			}
			if len(commitHint) == 32 && !sameBytes(pr.Commit, commitHint) {
				discardSlots()
				e.emitActionResult(actionID, nil, devicesec.ErrUntrusted)
				return
			}
			cand := &PersistedPairingCandidate{
				Server:         server,
				PairingID:      pr.PairingId,
				EdSlot:         edSlot,
				XSlot:          xSlot,
				EdPublic:       slices.Clone(edPub),
				XPublic:        slices.Clone(xPub),
				ReusesDeviceEd: reusesEd,
			}
			candBytes, err := cand.Encode()
			if err != nil {
				discardSlots()
				e.emitActionResult(actionID, nil, err)
				return
			}
			candRev := e.nextWorkflowToken()
			e.reqStorage(&corev1.SecurityStorageEffect{
				Op:           corev1.SecurityStorageOp_SECURITY_STORAGE_OP_WRITE_CAS,
				Bucket:       corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_PAIRING_CANDIDATE,
				Key:          PairingCandidateStorageKey(server, pr.PairingId),
				NextRevision: candRev,
				Data:         candBytes,
			}, func(_ storageReply, werr error) {
				if werr != nil {
					discardSlots()
					e.emitActionResult(actionID, nil, werr)
					return
				}
				jReq, _ := (&v1.JoinPairingRequest{
					Code:                code,
					JoinerPublic:        slices.Clone(ephemPub),
					JoinerKind:          v1.DeviceKind_DEVICE_KIND_CLIENT,
					DevicePublicKey:     slices.Clone(edPub),
					DeviceHpkePublicKey: slices.Clone(xPub),
					ProtocolVersion:     protocolVersion,
				}).Marshal()
				e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_JOIN_PAIRING, server, "", jReq, func(jBytes []byte, jErr *rpcError) {
					if jErr != nil {
						e.reqStorage(&corev1.SecurityStorageEffect{
							Op:     corev1.SecurityStorageOp_SECURITY_STORAGE_OP_DELETE_CAS,
							Bucket: corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_PAIRING_CANDIDATE,
							Key:    PairingCandidateStorageKey(server, pr.PairingId),
						}, func(storageReply, error) {})
						discardSlots()
						e.emitActionResult(actionID, nil, jErr)
						return
					}
					var jr v1.JoinPairingResponse
					if err := jr.Unmarshal(jBytes); err != nil || jr.PairingId == "" || jr.CandidateToken == "" || len(jr.Commit) != 32 {
						discardSlots()
						e.emitActionResult(actionID, nil, devicesec.ErrInvalid)
						return
					}
					if jr.PairingId != pr.PairingId || !sameBytes(jr.Commit, pr.Commit) ||
						(len(commitHint) == 32 && !sameBytes(jr.Commit, commitHint)) {
						discardSlots()
						e.emitActionResult(actionID, nil, devicesec.ErrUntrusted)
						return
					}
					sess := &joinerPairingSession{
						pairingID:      jr.PairingId,
						server:         server,
						code:           code,
						commitHint:     commitHint,
						commit:         slices.Clone(jr.Commit),
						existingRepair: act.ExistingRepair,
						replace:        act.ExistingRepair,
						ephemSlot:      ephemSlot,
						ephemPub:       slices.Clone(ephemPub),
						edSlot:         edSlot,
						xSlot:          xSlot,
						edPublic:       slices.Clone(edPub),
						xPublic:        slices.Clone(xPub),
						reusesDeviceEd: reusesEd,
						candidateToken: jr.CandidateToken,
					}
					wf.joinPairings[jr.PairingId] = sess
					wf.activePoll = &activePoll{
						kind:      pollJoinRevealed,
						pairingID: jr.PairingId,
						onResult: func(res *corev1.SecurityWorkflowResultEffect, err error) {
							e.emitActionResult(actionID, res, err)
							e.emitStatusChanged()
						},
					}
					e.pollJoinState(jr.PairingId, v1.PairingState_PAIRING_STATE_REVEALED)
				})
			})
		})
	}

	e.withEntropy(64, func() {
		generateRest := func(edPub []byte) {
			e.reqKeyGen(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_X, xSlot, true, func(xPub []byte, xErr error) {
				if xErr != nil {
					if !reusesEd && edSlot != "" {
						e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, edSlot)
					}
					e.emitActionResult(actionID, nil, xErr)
					return
				}
				e.reqKeyGen(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, ephemSlot, false, func(ephemPub []byte, epErr error) {
					if epErr != nil {
						if !reusesEd && edSlot != "" {
							e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, edSlot)
						}
						e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_X, xSlot)
						e.emitActionResult(actionID, nil, epErr)
						return
					}
					withCandidateKeys(edPub, xPub, ephemPub)
				})
			})
		}

		if reusesEd {
			generateRest(slices.Clone(wf.deviceEdPub))
			return
		}
		e.reqKeyGen(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, edSlot, true, func(edPub []byte, edErr error) {
			if edErr != nil {
				e.emitActionResult(actionID, nil, edErr)
				return
			}
			generateRest(edPub)
		})
	})
}

func (e *Engine) confirmJoinSasAction(actionID uint64, pairingID string) {
	wf := e.ensureWF()
	sess := wf.joinPairings[pairingID]
	if sess == nil || len(sess.pairKey) != 32 || sess.registering {
		e.emitActionResult(actionID, nil, devicesec.ErrStale)
		return
	}
	sess.sasConfirmed = true
	e.clearActivePollWithError(fatalWfError("canceled", text.KeySecurityPairingCanceled, ""))
	wf.activePoll = &activePoll{
		kind:      pollJoinApproved,
		pairingID: pairingID,
		onResult: func(res *corev1.SecurityWorkflowResultEffect, err error) {
			e.emitActionResult(actionID, res, err)
			e.emitStatusChanged()
		},
	}
	e.pollJoinState(pairingID, v1.PairingState_PAIRING_STATE_APPROVED)
}

func (e *Engine) cancelJoinPairingAction(actionID uint64, pairingID string) {
	wf := e.ensureWF()
	if pairingID == "" && wf.activePoll != nil {
		pairingID = wf.activePoll.pairingID
	}
	if wf.activePoll != nil && (pairingID == "" || wf.activePoll.pairingID == pairingID) {
		e.clearActivePollWithError(fatalWfError("canceled", text.KeySecurityPairingCanceled, ""))
	}
	sess := wf.joinPairings[pairingID]
	delete(wf.joinPairings, pairingID)
	if sess == nil {
		e.emitActionResult(actionID, nil, nil)
		return
	}
	if sess.ephemSlot != "" {
		e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, sess.ephemSlot)
	}
	if !sess.reusesDeviceEd && sess.edSlot != "" {
		e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, sess.edSlot)
	}
	if sess.xSlot != "" {
		e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_X, sess.xSlot)
	}
	e.reqStorage(&corev1.SecurityStorageEffect{
		Op:     corev1.SecurityStorageOp_SECURITY_STORAGE_OP_DELETE_CAS,
		Bucket: corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_PAIRING_CANDIDATE,
		Key:    PairingCandidateStorageKey(sess.server, sess.pairingID),
	}, func(storageReply, error) {})
	reqBytes, _ := (&v1.CancelPairingRequest{
		PairingId:      sess.pairingID,
		CandidateToken: sess.candidateToken,
	}).Marshal()
	e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_CANCEL_PAIRING, sess.server, "", reqBytes, func(_ []byte, _ *rpcError) {
		e.emitActionResult(actionID, nil, nil)
	})
}

func (e *Engine) cleanupJoinerFailure(sess *joinerPairingSession) {
	wf := e.ensureWF()
	if sess == nil {
		return
	}
	delete(wf.joinPairings, sess.pairingID)
	if sess.ephemSlot != "" {
		e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, sess.ephemSlot)
	}
	if !sess.reusesDeviceEd && sess.edSlot != "" {
		e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, sess.edSlot)
	}
	if sess.xSlot != "" {
		e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_X, sess.xSlot)
	}
	e.reqStorage(&corev1.SecurityStorageEffect{
		Op:     corev1.SecurityStorageOp_SECURITY_STORAGE_OP_DELETE_CAS,
		Bucket: corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_PAIRING_CANDIDATE,
		Key:    PairingCandidateStorageKey(sess.server, sess.pairingID),
	}, func(storageReply, error) {})
}

func (e *Engine) pollJoinState(pairingID string, want v1.PairingState) {
	wf := e.ensureWF()
	sess := wf.joinPairings[pairingID]
	poll := wf.activePoll
	if sess == nil || poll == nil || poll.pairingID != pairingID {
		e.cancelSecurityPollTimer()
		return
	}
	resolvePoll := func(res *corev1.SecurityWorkflowResultEffect, err error) {
		cur := wf.activePoll
		if cur == nil || cur.pairingID != pairingID {
			return
		}
		wf.activePoll = nil
		e.cancelSecurityPollTimer()
		if cur.onResult != nil {
			cur.onResult(res, err)
		}
	}

	gReq, _ := (&v1.GetPairingRequest{
		PairingId:      sess.pairingID,
		CandidateToken: sess.candidateToken,
	}).Marshal()
	e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_PAIRING, sess.server, "", gReq, func(respBytes []byte, rErr *rpcError) {
		if wf.activePoll == nil || wf.activePoll.pairingID != pairingID {
			return
		}
		if rErr != nil {
			if rErr.code == connectCodeNotFound || rErr.code == connectCodePermissionDenied {
				e.cleanupJoinerFailure(sess)
				resolvePoll(nil, fatalWfError("canceled", text.KeySecurityPairingExpiredOrWasCanceled, ""))
				return
			}
			// Transient network error: keep polling until canceled or resolved.
			e.scheduleSecurityPollTimer()
			return
		}
		var st v1.GetPairingResponse
		if err := st.Unmarshal(respBytes); err != nil {
			e.cleanupJoinerFailure(sess)
			resolvePoll(nil, devicesec.ErrInvalid)
			return
		}
		if st.State == v1.PairingState_PAIRING_STATE_CANCELED {
			e.cleanupJoinerFailure(sess)
			resolvePoll(nil, fatalWfError("canceled", text.KeySecurityPairingExpiredOrWasCanceled, ""))
			return
		}
		if st.State < want {
			e.scheduleSecurityPollTimer()
			return
		}

		if want == v1.PairingState_PAIRING_STATE_REVEALED {
			if len(st.InitiatorPublic) != 32 || len(st.Trust) == 0 {
				e.cleanupJoinerFailure(sess)
				resolvePoll(nil, devicesec.ErrUntrusted)
				return
			}
			if !sameBytes(devicesec.PairDomainCommit(st.InitiatorPublic, st.Trust), sess.commit) ||
				(len(sess.commitHint) == 32 && !sameBytes(sess.commit, sess.commitHint)) {
				e.cleanupJoinerFailure(sess)
				resolvePoll(nil, devicesec.ErrUntrusted)
				return
			}
			pin, approver, err := devicesec.ParsePairTrustDescriptor(st.Trust)
			if err != nil || !devicesec.IsDeviceID(approver) {
				e.cleanupJoinerFailure(sess)
				resolvePoll(nil, devicesec.ErrUntrusted)
				return
			}
			if sess.existingRepair && wf.record != nil && pin != wf.record.Pin {
				e.cleanupJoinerFailure(sess)
				resolvePoll(nil, devicesec.ErrUntrusted)
				return
			}
			var initX32 [32]byte
			copy(initX32[:], st.InitiatorPublic)
			e.requestKeyAgreeKind(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, sess.ephemSlot, initX32, false, func(shared []byte, aErr error) {
				if aErr != nil {
					e.cleanupJoinerFailure(sess)
					resolvePoll(nil, aErr)
					return
				}
				transcript := devicesec.PairDomainTranscript(
					sess.pairingID,
					st.InitiatorPublic,
					sess.ephemPub,
					sess.edPublic,
					sess.xPublic,
					int32(v1.DeviceKind_DEVICE_KIND_CLIENT),
					st.Trust,
				)
				ps, err := devicesec.DerivePairSecrets(shared, transcript)
				if err != nil {
					e.cleanupJoinerFailure(sess)
					resolvePoll(nil, err)
					return
				}
				if sess.ephemSlot != "" {
					e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X, sess.ephemSlot)
					sess.ephemSlot = ""
				}
				sess.trust = pin
				sess.approver = approver
				sess.pairKey = slices.Clone(ps.Key)
				sess.sasEmoji = ps.Emoji()
				resolvePoll(&corev1.SecurityWorkflowResultEffect{
					Pairing: joinerSessionToProto(sess),
				}, nil)
			})
			return
		}

		// want == PAIRING_STATE_APPROVED
		if st.Payload == nil {
			e.scheduleSecurityPollTimer()
			return
		}
		var hdr v1.Header
		if err := hdr.Unmarshal(st.Payload.Header); err != nil ||
			hdr.Kind != v1.PayloadKind_PAYLOAD_KIND_PAIRING ||
			hdr.RequestId != sess.pairingID {
			e.cleanupJoinerFailure(sess)
			resolvePoll(nil, devicesec.ErrUntrusted)
			return
		}
		plain, err := openPairingEnvelopeAESGCM(sess.pairKey, st.Payload.Nonce, st.Payload.Header, st.Payload.Ciphertext)
		if err != nil {
			e.cleanupJoinerFailure(sess)
			resolvePoll(nil, devicesec.ErrUntrusted)
			return
		}
		if hdr.Compression == v1.Compression_COMPRESSION_DEFLATE {
			plain, err = devicesec.InflateBounded(plain, maxStoreBlobBytes)
			if err != nil {
				e.cleanupJoinerFailure(sess)
				resolvePoll(nil, err)
				return
			}
		} else if hdr.Compression != v1.Compression_COMPRESSION_NONE {
			e.cleanupJoinerFailure(sess)
			resolvePoll(nil, devicesec.ErrInvalid)
			return
		}
		var payload v1.PairingPayload
		if err := payload.Unmarshal(plain); err != nil {
			e.cleanupJoinerFailure(sess)
			resolvePoll(nil, devicesec.ErrInvalid)
			return
		}
		pSrv, err := CheckServerURL(payload.Server)
		if err != nil || pSrv != sess.server || len(payload.AccountPublicKey) != 32 ||
			devicesec.ID(payload.AccountPublicKey) != sess.trust.Account {
			e.cleanupJoinerFailure(sess)
			resolvePoll(nil, devicesec.ErrUntrusted)
			return
		}
		rawStates := make([][]byte, 0, len(payload.SecurityStates))
		rawProofs := make([][]byte, 0, len(payload.SecurityStates))
		for _, ss := range payload.SecurityStates {
			if ss == nil || len(ss.State) == 0 {
				e.cleanupJoinerFailure(sess)
				resolvePoll(nil, devicesec.ErrInvalid)
				return
			}
			rawStates = append(rawStates, ss.State)
			rawProofs = append(rawProofs, ss.Recovery)
		}
		states, _, _, err := parseAndVerifyChain(sess.trust, rawStates, rawProofs)
		if err != nil || len(states) == 0 {
			e.cleanupJoinerFailure(sess)
			resolvePoll(nil, devicesec.ErrUntrusted)
			return
		}
		head := states[len(states)-1]
		selfID := devicesec.ID(sess.edPublic)
		selfMem, ok := head.Core.Member(selfID)
		if !ok || selfMem.Kind != devicesec.Client || !sameBytes(selfMem.XPublic[:], sess.xPublic) {
			e.cleanupJoinerFailure(sess)
			resolvePoll(nil, devicesec.ErrUntrusted)
			return
		}
		ap, err := VerifyPairApproval(payload.Approval, payload.ApprovalSignature)
		if err != nil ||
			ap.AccountId != sess.trust.Account ||
			ap.PairingId != sess.pairingID ||
			ap.ApproverDeviceId != sess.approver ||
			head.Core.AuthorityID != ap.ApproverDeviceId ||
			ap.CandidateKind != v1.DeviceKind_DEVICE_KIND_CLIENT ||
			!sameBytes(ap.CandidateDevicePublicKey, sess.edPublic) ||
			ap.ReplacesDeviceId != "" {
			e.cleanupJoinerFailure(sess)
			resolvePoll(nil, devicesec.ErrUntrusted)
			return
		}
		if len(payload.Lease) > 0 {
			lease, err := devicesec.ParseLease(payload.Lease)
			if err != nil || lease.Verify(head) != nil {
				e.cleanupJoinerFailure(sess)
				resolvePoll(nil, devicesec.ErrUntrusted)
				return
			}
		}

		var floorMs int64
		var priorKeys []ContentKeyEntry
		if sess.existingRepair {
			oldRec := wf.record
			if oldRec == nil || oldRec.Pin != sess.trust || len(rawStates) < len(oldRec.States) {
				e.cleanupJoinerFailure(sess)
				resolvePoll(nil, devicesec.ErrStale)
				return
			}
			for i, prevRaw := range oldRec.States {
				if !sameBytes(rawStates[i], prevRaw) {
					e.cleanupJoinerFailure(sess)
					resolvePoll(nil, devicesec.ErrStale)
					return
				}
			}
			floorMs = oldRec.FloorMs
			priorKeys = slices.Clone(oldRec.ContentKeys)
		}
		if e.lastNowMs > floorMs {
			floorMs = e.lastNowMs
		}
		var acctPub32 [32]byte
		copy(acctPub32[:], payload.AccountPublicKey)
		newRec := &PersistedRecord{
			Pin:              sess.trust,
			MemberID:         selfID,
			Member:           true,
			Server:           sess.server,
			AccountPublicKey: slices.Clone(acctPub32[:]),
			States:           rawStates,
			RecoveryProofs:   rawProofs,
			Lease:            slices.Clone(payload.Lease),
			FloorMs:          floorMs,
			ContentKeys:      priorKeys,
		}

		now := max(1, e.lastNowMs)
		regBody, err := (&v1.DeviceRegistration{
			AccountPublicKey: slices.Clone(payload.AccountPublicKey),
			DevicePublicKey:  slices.Clone(sess.edPublic),
			Kind:             v1.DeviceKind_DEVICE_KIND_CLIENT,
			CreatedMs:        now,
		}).Marshal()
		if err != nil {
			e.cleanupJoinerFailure(sess)
			resolvePoll(nil, err)
			return
		}
		signKind := corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED
		signSlot := sess.edSlot
		if sess.reusesDeviceEd {
			signKind = corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_ED
			signSlot = ""
		}
		signMsg := make([]byte, 0, len(registerContext)+len(regBody))
		signMsg = append(signMsg, registerContext...)
		signMsg = append(signMsg, regBody...)

		sess.registering = true
		e.requestKeySignKind(signKind, signSlot, signMsg, func(regSig []byte, sErr error) {
			if sErr != nil {
				sess.registering = false
				resolvePoll(nil, sErr)
				return
			}
			regReqBytes, err := (&v1.RegisterDeviceRequest{
				Registration:      regBody,
				Signature:         regSig,
				Approval:          slices.Clone(payload.Approval),
				ApprovalSignature: slices.Clone(payload.ApprovalSignature),
				ProtocolVersion:   protocolVersion,
			}).Marshal()
			if err != nil {
				sess.registering = false
				resolvePoll(nil, err)
				return
			}
			intent := &PersistedRegistrationIntent{
				Server:           sess.server,
				DeviceID:         selfID,
				RegisterRequest:  regReqBytes,
				Replace:          sess.existingRepair,
				AccountPublicKey: slices.Clone(acctPub32[:]),
				PairingID:        sess.pairingID,
				CandidateToken:   sess.candidateToken,
				EdSlot:           sess.edSlot,
				XSlot:            sess.xSlot,
				ReusesDeviceEd:   sess.reusesDeviceEd,
				Record:           newRec,
			}
			intentBytes, err := intent.Encode()
			if err != nil {
				sess.registering = false
				resolvePoll(nil, err)
				return
			}
			intentRev := e.nextWorkflowToken()
			e.reqStorage(&corev1.SecurityStorageEffect{
				Op:           corev1.SecurityStorageOp_SECURITY_STORAGE_OP_WRITE_CAS,
				Bucket:       corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_REGISTRATION_INTENT,
				Key:          "intent",
				CreateOnly:   true,
				NextRevision: intentRev,
				Data:         intentBytes,
			}, func(wrep storageReply, werr error) {
				if wrep.conflict {
					sess.registering = false
					resolvePoll(nil, devicesec.ErrStale)
					return
				}
				if werr != nil {
					sess.registering = false
					resolvePoll(nil, werr)
					return
				}
				wf.regIntent = intent
				wf.regIntentRev = intentRev
				e.securityLocked = true
				e.resumeRegistration(0, func(res *corev1.SecurityWorkflowResultEffect, regErr error) {
					sess.registering = false
					resolvePoll(res, regErr)
				})
			})
		})
	})
}

func (e *Engine) resumeRegistration(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	if fgGen != 0 {
		if err := e.checkFG(fgGen); err != nil {
			done(nil, err)
			return
		}
	}
	intent := wf.regIntent
	if intent == nil || intent.Record == nil {
		done(nil, nil)
		return
	}

	commitIntent := func() {
		if fgGen != 0 {
			if err := e.checkFG(fgGen); err != nil {
				done(nil, err)
				return
			}
		}
		if intent.PairingID != "" && intent.CandidateToken != "" {
			ackReq, _ := (&v1.GetPairingRequest{
				PairingId:      intent.PairingID,
				CandidateToken: intent.CandidateToken,
				Received:       true,
			}).Marshal()
			// Best-effort ack so initiator knows the payload was delivered.
			e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_PAIRING, intent.Server, "", ackReq, func([]byte, *rpcError) {})
		}
		var acctPub32 [32]byte
		copy(acctPub32[:], intent.AccountPublicKey)
		prof := &PersistedPublicProfile{
			Server:           intent.Server,
			Pin:              intent.Record.Pin,
			AccountPublicKey: acctPub32,
		}
		profBytes, err := prof.Encode()
		if err != nil {
			done(nil, err)
			return
		}
		recBytes, err := intent.Record.Encode()
		if err != nil {
			done(nil, err)
			return
		}
		rev := e.nextWorkflowToken()
		batch := []*corev1.SecurityStorageWrite{
			{
				Bucket:       corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_PUBLIC_PROFILE,
				Key:          PublicProfileStorageKey(intent.Server, intent.Record.Pin),
				NextRevision: rev,
				Data:         profBytes,
			},
			{
				Bucket:           corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD,
				Key:              "record",
				CreateOnly:       !intent.Replace || wf.recordRev == "",
				ExpectedRevision: func() string { if intent.Replace { return wf.recordRev }; return "" }(),
				NextRevision:     rev,
				Data:             recBytes,
			},
			{
				Bucket:           corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_REGISTRATION_INTENT,
				Key:              "intent",
				ExpectedRevision: wf.regIntentRev,
				Remove:           true,
			},
		}
		if intent.PairingID != "" {
			batch = append(batch, &corev1.SecurityStorageWrite{
				Bucket: corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_PAIRING_CANDIDATE,
				Key:    PairingCandidateStorageKey(intent.Server, intent.PairingID),
				Remove: true,
			})
		}
		var discard []string
		if !intent.ReusesDeviceEd && intent.EdSlot != "" {
			discard = append(discard, intent.EdSlot)
		}
		if intent.XSlot != "" {
			discard = append(discard, intent.XSlot)
		}
		eff := &corev1.SecurityStorageEffect{
			Op:              corev1.SecurityStorageOp_SECURITY_STORAGE_OP_BATCH_COMMIT,
			PromoteXSlot:    intent.XSlot,
			DiscardSlots:    discard,
			ClearLoginToken: true,
			Batch:           batch,
		}
		if !intent.ReusesDeviceEd {
			eff.PromoteEdSlot = intent.EdSlot
		}
		e.reqStorage(eff, func(rep storageReply, werr error) {
			if rep.conflict {
				done(nil, devicesec.ErrStale)
				return
			}
			if werr != nil {
				done(nil, werr)
				return
			}
			rec := intent.Record.Clone()
			wf.regIntent = nil
			wf.regIntentRev = ""
			if intent.PairingID != "" {
				delete(wf.joinPairings, intent.PairingID)
			}
			wf.server = intent.Server
			wf.record = rec
			wf.recordRev = rev
			wf.cachedToken = ""
			wf.tokenExpiresMs = 0
			wf.publicProfiles[PublicProfileStorageKey(intent.Server, rec.Pin)] = prof
			wf.legacyRepairNotice = false
			wf.deviceEdPresent = true
			wf.deviceXPresent = true
			if len(rep.deviceEdPub) == 32 {
				wf.deviceEdPub = slices.Clone(rep.deviceEdPub)
			}
			if len(rep.deviceXPub) == 32 {
				wf.deviceXPub = slices.Clone(rep.deviceXPub)
			} else if len(rec.States) > 0 {
				if hd, err := devicesec.ParseState(rec.States[len(rec.States)-1]); err == nil {
					if m, ok := hd.Core.Member(rec.MemberID); ok {
						if len(wf.deviceEdPub) != 32 {
							wf.deviceEdPub = slices.Clone(m.EdPublic[:])
						}
						wf.deviceXPub = slices.Clone(m.XPublic[:])
					}
				}
			}
			e.applyPersistedRecordToEngine(rec, 0, func(unlockErr error) {
				if unlockErr != nil {
					done(nil, unlockErr)
					return
				}
				if len(rec.States) > 0 {
					if head, pErr := devicesec.ParseState(rec.States[len(rec.States)-1]); pErr == nil {
						if ntf := e.buildNotifyRecordBytes(rec, head); len(ntf) > 0 {
							e.reqStorage(&corev1.SecurityStorageEffect{
								Op:           corev1.SecurityStorageOp_SECURITY_STORAGE_OP_WRITE_CAS,
								Bucket:       corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_NOTIFY_RECORD,
								Key:          "notify",
								NextRevision: rev,
								Data:         ntf,
							}, func(storageReply, error) {})
						}
					}
				}
				done(&corev1.SecurityWorkflowResultEffect{
					AccountAdopted: &corev1.SecurityAccountAdopted{
						Server:                intent.Server,
						DeviceId:              rec.MemberID,
						AccountId:             rec.Pin.Account,
						NeedsRecoveryRotation: rec.NeedsRecoveryRotation,
					},
				}, nil)
			})
		})
	}

	e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_REGISTER_DEVICE, intent.Server, "", intent.RegisterRequest, func(respBytes []byte, rErr *rpcError) {
		if rErr == nil {
			var resp v1.RegisterDeviceResponse
			if err := resp.Unmarshal(respBytes); err != nil || resp.DeviceId != intent.DeviceID {
				done(nil, devicesec.ErrUntrusted)
				return
			}
			commitIntent()
			return
		}
		if rErr.code == connectCodeAlreadyExists || rErr.code == connectCodePermissionDenied {
			kind := corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED
			slot := intent.EdSlot
			if intent.ReusesDeviceEd {
				kind = corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_ED
				slot = ""
			}
			e.forceLoginToken(intent.Server, kind, slot, intent.DeviceID, false, 0, false, func(_ string, lErr error) {
				if lErr == nil {
					commitIntent()
					return
				}
				done(nil, rErr)
			})
			return
		}
		done(nil, rErr)
	})
}
