package security

import (
	"bytes"
	"slices"

	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

func buildRecoveryContextSignBytes(challenge, recoveryEd []byte, recoveryID string) []byte {
	var ed [32]byte
	copy(ed[:], recoveryEd)
	msg := make([]byte, 0, len(recoveryCtxPrefix)+len(challenge)+32+len(recoveryID))
	msg = append(msg, recoveryCtxPrefix...)
	msg = append(msg, challenge...)
	msg = append(msg, ed[:]...)
	msg = append(msg, recoveryID...)
	return msg
}

func (e *Engine) fetchAndAcceptRecoveryContext(
	server string,
	recPub []byte,
	recoveryID string,
	genPrefix [10]byte,
	existingRec *PersistedRecord,
	fgGen uint64,
	signFn func(msg []byte) ([]byte, error),
	cb func(resp *v1.GetRecoveryContextResponse, pin devicesec.Pin, acctPub [32]byte, rawStates, rawProofs [][]byte, err error),
) {
	chalReq, _ := (&v1.GetRecoveryChallengeRequest{}).Marshal()
	e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_RECOVERY_CHALLENGE, server, "", chalReq, func(cBytes []byte, cErr *rpcError) {
		if cErr != nil {
			cb(nil, devicesec.Pin{}, [32]byte{}, nil, nil, cErr)
			return
		}
		if fgGen != 0 {
			if err := e.checkFG(fgGen); err != nil {
				cb(nil, devicesec.Pin{}, [32]byte{}, nil, nil, err)
				return
			}
		}
		var cr v1.GetRecoveryChallengeResponse
		if err := cr.Unmarshal(cBytes); err != nil || len(cr.Challenge) == 0 {
			cb(nil, devicesec.Pin{}, [32]byte{}, nil, nil, devicesec.ErrInvalid)
			return
		}
		signMsg := buildRecoveryContextSignBytes(cr.Challenge, recPub, recoveryID)
		sig, err := signFn(signMsg)
		if err != nil {
			cb(nil, devicesec.Pin{}, [32]byte{}, nil, nil, err)
			return
		}
		ctxReq, _ := (&v1.GetRecoveryContextRequest{
			RecoveryEd: slices.Clone(recPub),
			RecoveryId: recoveryID,
			Challenge:  cr.Challenge,
			Signature:  sig,
		}).Marshal()
		e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_RECOVERY_CONTEXT, server, "", ctxReq, func(respBytes []byte, rErr *rpcError) {
			if rErr != nil {
				cb(nil, devicesec.Pin{}, [32]byte{}, nil, nil, rErr)
				return
			}
			var resp v1.GetRecoveryContextResponse
			if err := resp.Unmarshal(respBytes); err != nil {
				cb(nil, devicesec.Pin{}, [32]byte{}, nil, nil, devicesec.ErrInvalid)
				return
			}
			if len(resp.AccountPublicKey) != 32 || devicesec.ID(resp.AccountPublicKey) != resp.AccountId {
				cb(nil, devicesec.Pin{}, [32]byte{}, nil, nil, devicesec.ErrUntrusted)
				return
			}
			if len(resp.States) == 0 {
				cb(nil, devicesec.Pin{}, [32]byte{}, nil, nil, devicesec.ErrInvalid)
				return
			}
			rawStates := make([][]byte, 0, len(resp.States))
			rawProofs := make([][]byte, 0, len(resp.States))
			for _, ss := range resp.States {
				if ss == nil || len(ss.State) == 0 {
					cb(nil, devicesec.Pin{}, [32]byte{}, nil, nil, devicesec.ErrInvalid)
					return
				}
				rawStates = append(rawStates, ss.State)
				rawProofs = append(rawProofs, ss.Recovery)
			}
			genesis, err := devicesec.ParseState(rawStates[0])
			if err != nil {
				cb(nil, devicesec.Pin{}, [32]byte{}, nil, nil, err)
				return
			}
			genHash, _ := genesis.Hash()
			if genPrefix != ([10]byte{}) && !bytes.Equal(genHash[:10], genPrefix[:]) {
				cb(nil, devicesec.Pin{}, [32]byte{}, nil, nil, devicesec.ErrUntrusted)
				return
			}
			pin, err := genesis.Pin()
			if err != nil || pin.Account != resp.AccountId {
				cb(nil, devicesec.Pin{}, [32]byte{}, nil, nil, devicesec.ErrUntrusted)
				return
			}
			states, _, _, err := parseAndVerifyChain(pin, rawStates, rawProofs)
			if err != nil {
				cb(nil, devicesec.Pin{}, [32]byte{}, nil, nil, err)
				return
			}
			if existingRec != nil {
				if existingRec.Pin != pin || len(rawStates) < len(existingRec.States) {
					cb(nil, devicesec.Pin{}, [32]byte{}, nil, nil, devicesec.ErrUntrusted)
					return
				}
				for i, s := range existingRec.States {
					if !bytes.Equal(rawStates[i], s) {
						cb(nil, devicesec.Pin{}, [32]byte{}, nil, nil, devicesec.ErrUntrusted)
						return
					}
				}
			}
			head := states[len(states)-1]
			if len(recPub) == 32 && resp.RecoveryId == "" && !bytes.Equal(head.Core.RecoveryEd[:], recPub) {
				cb(nil, devicesec.Pin{}, [32]byte{}, nil, nil, devicesec.ErrUntrusted)
				return
			}
			var acctPub32 [32]byte
			copy(acctPub32[:], resp.AccountPublicKey)
			cb(&resp, pin, acctPub32, rawStates, rawProofs, nil)
		})
	})
}

func (e *Engine) prepareEmergencyRecoveryOp(fgGen uint64, rawServer, code string, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	if err := e.checkFG(fgGen); err != nil {
		done(nil, err)
		return
	}
	server, err := CheckServerURL(rawServer)
	if err != nil {
		done(nil, err)
		return
	}
	if wf.pendingOp != nil || wf.regIntent != nil || wf.bootstrapInvalid {
		done(nil, newWfError("pending", text.KeySecurityFinishTheCurrentSecurityOperationFirst))
		return
	}
	oldSeed, genPrefix, ok := DecodeRecoveryCode(code)
	if !ok {
		done(nil, newWfError("invalid", errInvalidRecoveryCode.Error()))
		return
	}
	oldRecPub, err := devicesec.Ed25519PublicFromSeed(oldSeed[:])
	if err != nil {
		zeroBytes(oldSeed[:])
		done(nil, err)
		return
	}
	signWithOld := func(msg []byte) ([]byte, error) {
		return devicesec.Ed25519SignSeed(oldSeed[:], msg)
	}
	e.fetchAndAcceptRecoveryContext(server, oldRecPub, "", genPrefix, wf.record, fgGen, signWithOld, func(resp *v1.GetRecoveryContextResponse, pin devicesec.Pin, acctPub [32]byte, rawStates, rawProofs [][]byte, cErr error) {
		zeroBytes(oldSeed[:])
		if cErr != nil {
			done(nil, cErr)
			return
		}
		if err := e.checkFG(fgGen); err != nil {
			done(nil, err)
			return
		}
		head, err := devicesec.ParseState(rawStates[len(rawStates)-1])
		if err != nil {
			done(nil, err)
			return
		}
		headHash, _ := head.Hash()

		var retiring string
		if wf.record != nil {
			if wf.record.Pin != pin || (wf.record.Server != "" && wf.record.Server != server) {
				done(nil, devicesec.ErrUntrusted)
				return
			}
			if _, ok := head.Core.Member(wf.record.MemberID); ok && head.Core.AuthorityID != wf.record.MemberID {
				retiring = wf.record.MemberID
			}
		}

		e.withEntropy(128, func() {
			tok := e.nextWorkflowToken()
			edSlot := "rec-ed-" + tok
			xSlot := "rec-x-" + tok
			if err := e.checkFG(fgGen); err != nil {
				done(nil, err)
				return
			}
			e.reqKeyGen(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, edSlot, true, func(candEdPub []byte, edErr error) {
				if edErr != nil {
					done(nil, edErr)
					return
				}
				e.reqKeyGen(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_X, xSlot, true, func(candXPub []byte, xErr error) {
					if xErr != nil {
						e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, edSlot)
						done(nil, xErr)
						return
					}
					var newRecSeed, newSecret [32]byte
					if !e.readRandom(newRecSeed[:]) || !e.readRandom(newSecret[:]) {
						zeroBytes(newRecSeed[:])
						zeroBytes(newSecret[:])
						e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, edSlot)
						e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_X, xSlot)
						done(nil, ErrNeedEntropy)
						return
					}
					defer func() {
						zeroBytes(newRecSeed[:])
						zeroBytes(newSecret[:])
					}()
					newRecPub, err := devicesec.Ed25519PublicFromSeed(newRecSeed[:])
					if err != nil {
						e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, edSlot)
						e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_X, xSlot)
						done(nil, err)
						return
					}
					newCode := EncodeRecoveryCode(newRecSeed, pin.Genesis)
					candID := devicesec.ID(candEdPub)
					now := max(uint64(max(1, e.lastNowMs)), head.Core.IssuedAt)
					op := &PersistedOperation{
						ID:       e.nextWorkflowToken(),
						Revision: 1,
						Server:   server,
						MemberID: candID,
						Kind:     "recovery",
						Parent:   headHash,
						Phase:    "PREPARED",
						Recovery: &PersistedRecoveryDraft{
							AccountID:        pin.Account,
							AccountPublicKey: slices.Clone(acctPub[:]),
							Domain:           pin.Domain,
							Genesis:          pin.Genesis,
							EdSlot:           edSlot,
							XSlot:            xSlot,
							CandidateEdPub:   slices.Clone(candEdPub),
							CandidateXPub:    slices.Clone(candXPub),
							RecoveryEd:       slices.Clone(newRecPub),
							Secret:           slices.Clone(newSecret[:]),
							NowMs:            int64(now),
							RetiringMemberID: retiring,
							RecoveryID:       resp.RecoveryId,
							ReadyMs:          int64(resp.ReadyMs),
							States:           rawStates,
							RecoveryProofs:   rawProofs,
						},
					}
					e.claimOperationCAS(op, nil, candID, func(_ *PersistedOperation, clErr error) {
						if clErr != nil {
							e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, edSlot)
							e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_X, xSlot)
							done(nil, clErr)
							return
						}
						wf.server = server
						done(&corev1.SecurityWorkflowResultEffect{
							RecoveryCode: newCode,
							Recovery: &corev1.SecurityRecoveryInfo{
								RecoveryId: resp.RecoveryId,
								Phase:      resp.Phase,
								ReadyMs:    int64(resp.ReadyMs),
							},
						}, nil)
					})
				})
			})
		})
	})
}

func (e *Engine) continueRecoverySigningOp(fgGen uint64, oldCode string, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	if err := e.checkFG(fgGen); err != nil {
		done(nil, err)
		return
	}
	op := wf.pendingOp
	if op == nil || op.Kind != "recovery" || op.Recovery == nil {
		done(nil, devicesec.ErrStale)
		return
	}
	draft := op.Recovery

	submitSigned := func(curOp *PersistedOperation) {
		e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_SUBMIT_RECOVERY, curOp.Server, "", curOp.Signed, func(respBytes []byte, rErr *rpcError) {
			if rErr != nil {
				done(nil, rErr)
				return
			}
			var sr v1.SubmitRecoveryResponse
			if err := sr.Unmarshal(respBytes); err != nil || sr.RecoveryId == "" {
				done(nil, devicesec.ErrInvalid)
				return
			}
			nextDraft := *curOp.Recovery
			nextDraft.RecoveryID = sr.RecoveryId
			nextDraft.ReadyMs = int64(sr.ReadyMs)
			nextOp := *curOp
			nextOp.Phase = "PUBLISHED"
			nextOp.Recovery = &nextDraft
			e.saveOperationCAS(curOp, &nextOp, func(_ *PersistedOperation, sErr error) {
				if sErr != nil {
					done(nil, sErr)
					return
				}
				done(&corev1.SecurityWorkflowResultEffect{
					Recovery: &corev1.SecurityRecoveryInfo{
						RecoveryId: sr.RecoveryId,
						Phase:      v1.GetRecoveryContextResponse_PHASE_PENDING,
						ReadyMs:    int64(sr.ReadyMs),
					},
				}, nil)
			})
		})
	}

	if len(op.Signed) > 0 {
		submitSigned(op)
		return
	}

	oldSeed, genPrefix, ok := DecodeRecoveryCode(oldCode)
	if !ok || !bytes.Equal(draft.Genesis[:10], genPrefix[:]) {
		zeroBytes(oldSeed[:])
		done(nil, newWfError("invalid", errInvalidRecoveryCode.Error()))
		return
	}
	oldRecPub, err := devicesec.Ed25519PublicFromSeed(oldSeed[:])
	if err != nil {
		zeroBytes(oldSeed[:])
		done(nil, err)
		return
	}
	if len(draft.States) == 0 || len(draft.RecoveryEd) != 32 || len(draft.Secret) != 32 {
		zeroBytes(oldSeed[:])
		done(nil, devicesec.ErrInvalid)
		return
	}
	pin := devicesec.Pin{
		Account: draft.AccountID,
		Domain:  draft.Domain,
		Genesis: draft.Genesis,
	}
	states, _, _, err := parseAndVerifyChain(pin, draft.States, draft.RecoveryProofs)
	if err != nil || len(states) == 0 {
		zeroBytes(oldSeed[:])
		done(nil, err)
		return
	}
	parent := states[len(states)-1]
	if !bytes.Equal(parent.Core.RecoveryEd[:], oldRecPub) {
		zeroBytes(oldSeed[:])
		done(nil, newWfError("invalid", errInvalidRecoveryCode.Error()))
		return
	}
	newMember, err := devicesec.NewMember(draft.CandidateEdPub, draft.CandidateXPub, devicesec.Client)
	if err != nil {
		zeroBytes(oldSeed[:])
		done(nil, err)
		return
	}
	members := make([]devicesec.Member, 0, len(parent.Core.Members)+1)
	var replaced bool
	for _, m := range parent.Core.Members {
		if m.ID == parent.Core.AuthorityID {
			continue
		}
		if m.ID == newMember.ID {
			members = append(members, newMember)
			replaced = true
		} else {
			members = append(members, m)
		}
	}
	if !replaced {
		members = append(members, newMember)
	}
	var newRecEd [32]byte
	copy(newRecEd[:], draft.RecoveryEd)
	now := max(uint64(max(1, draft.NowMs)), uint64(max(1, e.lastNowMs)))
	if parent.Core.IssuedAt > now {
		now = parent.Core.IssuedAt
	}

	e.withEntropy(64, func() {
		if err := e.checkFG(fgGen); err != nil {
			zeroBytes(oldSeed[:])
			done(nil, err)
			return
		}
		e.buildAndSignStateWithKEM(
			corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED,
			draft.EdSlot,
			true,
			fgGen,
			func(hpke devicesec.HPKESender) (devicesec.State, error) {
				return devicesec.NewRecovery(parent, newMember, members, draft.Secret, newRecEd, now, hpke)
			},
			members,
			func(target devicesec.State, sErr error) {
				defer zeroBytes(oldSeed[:])
				if sErr != nil {
					done(nil, sErr)
					return
				}
				stmt, err := devicesec.NewRecoveryStatement(parent, target, e.entropy)
				if err != nil {
					done(nil, err)
					return
				}
				stmtSB, err := stmt.SigningBytes()
				if err != nil {
					done(nil, err)
					return
				}
				stmtSig, err := devicesec.Ed25519SignSeed(oldSeed[:], stmtSB)
				if err != nil {
					done(nil, err)
					return
				}
				stmt.Signature = stmtSig
				if err := stmt.Verify(parent, target); err != nil {
					done(nil, err)
					return
				}
				targetBytes, err := target.MarshalBinary()
				if err != nil {
					done(nil, err)
					return
				}
				proofBytes, err := stmt.MarshalBinary()
				if err != nil {
					done(nil, err)
					return
				}
				reqBytes, err := (&v1.SubmitRecoveryRequest{
					Statement: proofBytes,
					State:     targetBytes,
				}).Marshal()
				if err != nil {
					done(nil, err)
					return
				}
				nextDraft := *draft
				nextDraft.TargetState = targetBytes
				nextDraft.Proof = proofBytes
				nextOp := *op
				nextOp.Signed = reqBytes
				nextOp.Recovery = &nextDraft
				e.saveOperationCAS(op, &nextOp, func(saved *PersistedOperation, saveErr error) {
					if saveErr != nil {
						done(nil, saveErr)
						return
					}
					submitSigned(saved)
				})
			},
		)
	})
}

func (e *Engine) confirmRecoveryCodeSavedOp(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	if err := e.checkFG(fgGen); err != nil {
		done(nil, err)
		return
	}
	op := wf.pendingOp
	if op == nil || op.Kind != "recovery" {
		done(nil, devicesec.ErrStale)
		return
	}
	next := *op
	next.CodeSaved = true
	e.saveOperationCAS(op, &next, func(_ *PersistedOperation, sErr error) {
		if sErr != nil {
			done(nil, sErr)
			return
		}
		done(&corev1.SecurityWorkflowResultEffect{CodeSaved: true}, nil)
	})
}

func (e *Engine) resumeRecoveryOp(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	if err := e.checkFG(fgGen); err != nil {
		done(nil, err)
		return
	}
	op := wf.pendingOp
	if op == nil || op.Kind != "recovery" || op.Recovery == nil {
		done(nil, nil)
		return
	}
	draft := op.Recovery
	if draft.RecoveryID == "" {
		if len(op.Signed) > 0 {
			e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_SUBMIT_RECOVERY, op.Server, "", op.Signed, func(respBytes []byte, rErr *rpcError) {
				if rErr != nil {
					done(nil, rErr)
					return
				}
				var sr v1.SubmitRecoveryResponse
				if err := sr.Unmarshal(respBytes); err != nil || sr.RecoveryId == "" {
					done(nil, devicesec.ErrInvalid)
					return
				}
				nextDraft := *draft
				nextDraft.RecoveryID = sr.RecoveryId
				nextDraft.ReadyMs = int64(sr.ReadyMs)
				nextOp := *op
				nextOp.Phase = "PUBLISHED"
				nextOp.Recovery = &nextDraft
				e.saveOperationCAS(op, &nextOp, func(_ *PersistedOperation, sErr error) {
					if sErr != nil {
						done(nil, sErr)
						return
					}
					e.resumeRecoveryOp(fgGen, done)
				})
			})
			return
		}
		done(&corev1.SecurityWorkflowResultEffect{
			ResumedDraft: true,
			CodeSaved:    op.CodeSaved,
		}, nil)
		return
	}

	signWithCand := func(msg []byte) ([]byte, error) {
		return nil, devicesec.ErrInvalid
	}
	_ = signWithCand
	// Fetch challenge then sign recovery context using CANDIDATE_ED slot asynchronously.
	chalReq, _ := (&v1.GetRecoveryChallengeRequest{}).Marshal()
	e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_RECOVERY_CHALLENGE, op.Server, "", chalReq, func(cBytes []byte, cErr *rpcError) {
		if cErr != nil {
			done(nil, cErr)
			return
		}
		if err := e.checkFG(fgGen); err != nil {
			done(nil, err)
			return
		}
		var cr v1.GetRecoveryChallengeResponse
		if err := cr.Unmarshal(cBytes); err != nil || len(cr.Challenge) == 0 {
			done(nil, devicesec.ErrInvalid)
			return
		}
		msg := buildRecoveryContextSignBytes(cr.Challenge, nil, draft.RecoveryID)
		e.requestKeySignKind(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, draft.EdSlot, msg, func(sig []byte, sErr error) {
			if sErr != nil {
				done(nil, sErr)
				return
			}
			ctxReq, _ := (&v1.GetRecoveryContextRequest{
				RecoveryId: draft.RecoveryID,
				Challenge:  cr.Challenge,
				Signature:  sig,
			}).Marshal()
			e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_RECOVERY_CONTEXT, op.Server, "", ctxReq, func(respBytes []byte, gErr *rpcError) {
				if gErr != nil {
					done(nil, gErr)
					return
				}
				var resp v1.GetRecoveryContextResponse
				if err := resp.Unmarshal(respBytes); err != nil {
					done(nil, devicesec.ErrInvalid)
					return
				}
				if int64(resp.ReadyMs) > 0 && int64(resp.ReadyMs) != draft.ReadyMs {
					nextDraft := *draft
					nextDraft.ReadyMs = int64(resp.ReadyMs)
					nextOp := *op
					nextOp.Recovery = &nextDraft
					e.saveOperationCAS(op, &nextOp, func(saved *PersistedOperation, saveErr error) {
						if saveErr != nil {
							done(nil, saveErr)
							return
						}
						done(&corev1.SecurityWorkflowResultEffect{
							ResumedDraft: true,
							CodeSaved:    saved.CodeSaved,
							Recovery: &corev1.SecurityRecoveryInfo{
								RecoveryId: saved.Recovery.RecoveryID,
								Phase:      resp.Phase,
								ReadyMs:    saved.Recovery.ReadyMs,
							},
						}, nil)
					})
					return
				}
				done(&corev1.SecurityWorkflowResultEffect{
					ResumedDraft: true,
					CodeSaved:    op.CodeSaved,
					Recovery: &corev1.SecurityRecoveryInfo{
						RecoveryId: draft.RecoveryID,
						Phase:      resp.Phase,
						ReadyMs:    draft.ReadyMs,
					},
				}, nil)
			})
		})
	})
}

func (e *Engine) completeRecoveryOp(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	if err := e.checkFG(fgGen); err != nil {
		done(nil, err)
		return
	}
	op := wf.pendingOp
	if op == nil || op.Kind != "recovery" || op.Recovery == nil || op.Recovery.RecoveryID == "" ||
		len(op.Recovery.TargetState) == 0 || len(op.Recovery.Proof) == 0 || len(op.Recovery.Secret) != 32 {
		done(nil, devicesec.ErrStale)
		return
	}
	if !op.CodeSaved {
		done(nil, newWfError("invalid", text.KeySecurityConfirmRecoveryCodeSavedFirst))
		return
	}
	draft := op.Recovery
	targetState, err := devicesec.ParseState(draft.TargetState)
	if err != nil {
		done(nil, err)
		return
	}
	pin := devicesec.Pin{
		Account: draft.AccountID,
		Domain:  draft.Domain,
		Genesis: draft.Genesis,
	}
	if pin.Verify(targetState) != nil {
		done(nil, devicesec.ErrUntrusted)
		return
	}

	commitRecord := func(curOp *PersistedOperation, states [][]byte, proofs [][]byte, head devicesec.State, leaseRaw []byte, contentKeys []ContentKeyEntry) {
		var acctPub32 [32]byte
		copy(acctPub32[:], curOp.Recovery.AccountPublicKey)
		prof := &PersistedPublicProfile{
			Server:           curOp.Server,
			Pin:              pin,
			AccountPublicKey: acctPub32,
		}
		profBytes, err := prof.Encode()
		if err != nil {
			done(nil, err)
			return
		}
		floorMs := max(e.lastNowMs, curOp.Recovery.NowMs)
		if wf.record != nil && wf.record.FloorMs > floorMs {
			floorMs = wf.record.FloorMs
		}
		var priorKeys []ContentKeyEntry
		if wf.record != nil && wf.record.Pin == pin {
			priorKeys = slices.Clone(wf.record.ContentKeys)
		}
		mergedKeys := append(priorKeys, contentKeys...)
		rec := &PersistedRecord{
			Pin:                    pin,
			MemberID:               curOp.MemberID,
			Member:                 true,
			Server:                 curOp.Server,
			AccountPublicKey:       slices.Clone(acctPub32[:]),
			States:                 states,
			RecoveryProofs:         proofs,
			Lease:                  leaseRaw,
			FloorMs:                floorMs,
			ContentKeys:            mergedKeys,
			RecoveryObjectsPending: true,
		}
		recBytes, err := rec.Encode()
		if err != nil {
			done(nil, err)
			return
		}
		rev := e.nextWorkflowToken()
		opRev := u64Str(curOp.Revision)
		if wf.pendingOpRev != "" {
			opRev = wf.pendingOpRev
		}
		recordExpected := ""
		createRecord := true
		if wf.recordRev != "" {
			recordExpected = wf.recordRev
			createRecord = false
		}
		e.reqStorage(&corev1.SecurityStorageEffect{
			Op:              corev1.SecurityStorageOp_SECURITY_STORAGE_OP_BATCH_COMMIT,
			PromoteEdSlot:   curOp.Recovery.EdSlot,
			PromoteXSlot:    curOp.Recovery.XSlot,
			DiscardSlots:    []string{curOp.Recovery.EdSlot, curOp.Recovery.XSlot},
			ClearLoginToken: true,
			Batch: []*corev1.SecurityStorageWrite{
				{
					Bucket:       corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_PUBLIC_PROFILE,
					Key:          PublicProfileStorageKey(curOp.Server, pin),
					NextRevision: rev,
					Data:         profBytes,
				},
				{
					Bucket:           corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD,
					Key:              "record",
					CreateOnly:       createRecord,
					ExpectedRevision: recordExpected,
					NextRevision:     rev,
					Data:             recBytes,
				},
				{
					Bucket:           corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_OPERATION,
					Key:              "pending",
					ExpectedRevision: opRev,
					Remove:           true,
				},
			},
		}, func(rep storageReply, werr error) {
			if rep.conflict {
				done(nil, devicesec.ErrStale)
				return
			}
			if werr != nil {
				done(nil, werr)
				return
			}
			wf.server = curOp.Server
			wf.record = rec
			wf.recordRev = rev
			wf.pendingOp = nil
			wf.pendingOpRev = ""
			wf.cachedToken = ""
			wf.tokenExpiresMs = 0
			wf.publicProfiles[PublicProfileStorageKey(curOp.Server, pin)] = prof
			wf.legacyRepairNotice = false
			wf.deviceEdPresent = true
			wf.deviceXPresent = true
			if len(rep.deviceEdPub) == 32 {
				wf.deviceEdPub = slices.Clone(rep.deviceEdPub)
			} else {
				wf.deviceEdPub = slices.Clone(curOp.Recovery.CandidateEdPub)
			}
			if len(rep.deviceXPub) == 32 {
				wf.deviceXPub = slices.Clone(rep.deviceXPub)
			} else {
				wf.deviceXPub = slices.Clone(curOp.Recovery.CandidateXPub)
			}
			e.applyPersistedRecordToEngine(rec, func(unlockErr error) {
				if unlockErr != nil {
					done(nil, unlockErr)
					return
				}
				if ntf := e.buildNotifyRecordBytes(rec, head); len(ntf) > 0 {
					e.reqStorage(&corev1.SecurityStorageEffect{
						Op:           corev1.SecurityStorageOp_SECURITY_STORAGE_OP_WRITE_CAS,
						Bucket:       corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_NOTIFY_RECORD,
						Key:          "notify",
						NextRevision: rev,
						Data:         ntf,
					}, func(storageReply, error) {})
				}
				e.republishRecoveryObjects(nil)
				done(&corev1.SecurityWorkflowResultEffect{
					AccountAdopted: &corev1.SecurityAccountAdopted{
						Server:                curOp.Server,
						DeviceId:              curOp.MemberID,
						AccountId:             pin.Account,
						NeedsRecoveryRotation: false,
					},
				}, nil)
			})
		})
	}

	afterCompleteRPC := func(curOp *PersistedOperation, tok string) {
		var secret32 [32]byte
		copy(secret32[:], curOp.Recovery.Secret)
		contentKeys := []ContentKeyEntry{{
			Epoch:  targetState.Core.ContentEpoch,
			Secret: secret32,
		}}
		states := make([][]byte, 0, len(curOp.Recovery.States)+2)
		proofs := make([][]byte, 0, len(curOp.Recovery.RecoveryProofs)+2)
		for i, s := range curOp.Recovery.States {
			states = append(states, s)
			var rp []byte
			if i < len(curOp.Recovery.RecoveryProofs) {
				rp = curOp.Recovery.RecoveryProofs[i]
			}
			proofs = append(proofs, rp)
		}
		states = append(states, curOp.Recovery.TargetState)
		proofs = append(proofs, curOp.Recovery.Proof)

		signAt := max(e.currentSigningTime(e.lastNowMs), targetState.Core.IssuedAt)
		e.buildAndSignLease(targetState, corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, curOp.Recovery.EdSlot, signAt, func(lease devicesec.LeaseCertificate, lErr error) {
			if lErr != nil {
				done(nil, lErr)
				return
			}
			leaseRaw, _ := lease.MarshalBinary()
			putLeaseBytes, _ := (&v1.PutSecurityStateRequest{Lease: leaseRaw}).Marshal()
			e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_SECURITY_STATE, curOp.Server, tok, putLeaseBytes, func(_ []byte, pErr *rpcError) {
				if pErr != nil {
					done(nil, pErr)
					return
				}
				retiring := curOp.Recovery.RetiringMemberID
				if retiring == "" {
					commitRecord(curOp, states, proofs, targetState, leaseRaw, contentKeys)
					return
				}
				if _, stillMember := targetState.Core.Member(retiring); !stillMember || retiring == curOp.MemberID {
					commitRecord(curOp, states, proofs, targetState, leaseRaw, contentKeys)
					return
				}
				// Retire previous member identity safely before committing final record.
				publishRetirement := func(retState devicesec.State, retStateRaw, retLeaseRaw, putRetBytes []byte) {
					e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_SECURITY_STATE, curOp.Server, tok, putRetBytes, func(_ []byte, prErr *rpcError) {
						if prErr != nil {
							done(nil, prErr)
							return
						}
						states = append(states, retStateRaw)
						proofs = append(proofs, nil)
						commitRecord(curOp, states, proofs, retState, retLeaseRaw, contentKeys)
					})
				}
				if len(curOp.Recovery.RetirementRequest) > 0 {
					var rr v1.PutSecurityStateRequest
					if rr.Unmarshal(curOp.Recovery.RetirementRequest) == nil && len(rr.States) == 1 {
						if rs, rErr := devicesec.ParseState(rr.States[0].State); rErr == nil {
							publishRetirement(rs, rr.States[0].State, rr.Lease, curOp.Recovery.RetirementRequest)
							return
						}
					}
				}
				e.withEntropy(64, func() {
					var retSecret [32]byte
					if !e.readRandom(retSecret[:]) {
						done(nil, ErrNeedEntropy)
						return
					}
					var members []devicesec.Member
					for _, m := range targetState.Core.Members {
						if m.ID != retiring {
							members = append(members, m)
						}
					}
					removed := append(slices.Clone(targetState.Core.Removed), retiring)
					sortutil.Strings(removed)
					rNow := max(signAt, targetState.Core.IssuedAt)
					e.buildAndSignStateWithKEM(
						corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED,
						curOp.Recovery.EdSlot,
						true,
						fgGen,
						func(hpke devicesec.HPKESender) (devicesec.State, error) {
							return devicesec.NewMembershipState(targetState, members, removed, secret32[:], retSecret[:], rNow, hpke, e.entropy)
						},
						members,
						func(retState devicesec.State, bErr error) {
							if bErr != nil {
								zeroBytes(retSecret[:])
								done(nil, bErr)
								return
							}
							retRaw, _ := retState.MarshalBinary()
							e.buildAndSignLease(retState, corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, curOp.Recovery.EdSlot, rNow, func(retLease devicesec.LeaseCertificate, rlErr error) {
								if rlErr != nil {
									zeroBytes(retSecret[:])
									done(nil, rlErr)
									return
								}
								retLeaseRaw, _ := retLease.MarshalBinary()
								putRetBytes, _ := (&v1.PutSecurityStateRequest{
									States: []*v1.SecurityState{{State: retRaw}},
									Lease:  retLeaseRaw,
								}).Marshal()
								contentKeys = append(contentKeys, ContentKeyEntry{
									Epoch:  retState.Core.ContentEpoch,
									Secret: retSecret,
								})
								zeroBytes(retSecret[:])
								nextDraft := *curOp.Recovery
								nextDraft.RetirementRequest = putRetBytes
								nextOp := *curOp
								nextOp.Recovery = &nextDraft
								e.saveOperationCAS(curOp, &nextOp, func(saved *PersistedOperation, sErr error) {
									if sErr != nil {
										done(nil, sErr)
										return
									}
									publishRetirement(retState, retRaw, retLeaseRaw, putRetBytes)
								})
							})
						},
					)
				})
			})
		})
	}

	callComplete := func(curOp *PersistedOperation) {
		e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_COMPLETE_RECOVERY, curOp.Server, "", curOp.Recovery.CompleteRequest, func(respBytes []byte, rErr *rpcError) {
			if rErr != nil {
				done(nil, rErr)
				return
			}
			var resp v1.CompleteRecoveryResponse
			if err := resp.Unmarshal(respBytes); err != nil || resp.DeviceId != curOp.MemberID {
				done(nil, devicesec.ErrUntrusted)
				return
			}
			e.forceLoginToken(curOp.Server, corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, curOp.Recovery.EdSlot, curOp.MemberID, true, fgGen, false, func(tok string, lErr error) {
				if lErr != nil {
					done(nil, lErr)
					return
				}
				afterCompleteRPC(curOp, tok)
			})
		})
	}

	if len(draft.CompleteRequest) > 0 {
		callComplete(op)
		return
	}
	label := make([]byte, 0, len(recoveryDonePrefix)+len(draft.RecoveryID))
	label = append(label, recoveryDonePrefix...)
	label = append(label, draft.RecoveryID...)
	e.requestKeySignKind(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, draft.EdSlot, label, func(sig []byte, sErr error) {
		if sErr != nil {
			done(nil, sErr)
			return
		}
		if err := e.checkFG(fgGen); err != nil {
			done(nil, err)
			return
		}
		reqBytes, _ := (&v1.CompleteRecoveryRequest{
			RecoveryId: draft.RecoveryID,
			Signature:  sig,
		}).Marshal()
		nextDraft := *draft
		nextDraft.CompleteRequest = reqBytes
		nextOp := *op
		nextOp.Recovery = &nextDraft
		e.saveOperationCAS(op, &nextOp, func(saved *PersistedOperation, saveErr error) {
			if saveErr != nil {
				done(nil, saveErr)
				return
			}
			callComplete(saved)
		})
	})
}

func (e *Engine) discardUnsignedRecoveryOp(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	if err := e.checkFG(fgGen); err != nil {
		done(nil, err)
		return
	}
	op := wf.pendingOp
	if op == nil || op.Kind != "recovery" || op.Phase != "PREPARED" || len(op.Signed) > 0 || op.Recovery == nil {
		done(nil, devicesec.ErrStale)
		return
	}
	edSlot, xSlot := op.Recovery.EdSlot, op.Recovery.XSlot
	e.clearOperationCAS(op, func(cErr error) {
		if cErr != nil {
			done(nil, cErr)
			return
		}
		if edSlot != "" {
			e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, edSlot)
		}
		if xSlot != "" {
			e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_X, xSlot)
		}
		done(nil, nil)
	})
}

func (e *Engine) discardRejectedRecoveryOp(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	if err := e.checkFG(fgGen); err != nil {
		done(nil, err)
		return
	}
	op := wf.pendingOp
	if op == nil || op.Kind != "recovery" || op.Recovery == nil || op.Recovery.RecoveryID == "" {
		done(nil, devicesec.ErrStale)
		return
	}
	draft := op.Recovery
	chalReq, _ := (&v1.GetRecoveryChallengeRequest{}).Marshal()
	e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_RECOVERY_CHALLENGE, op.Server, "", chalReq, func(cBytes []byte, cErr *rpcError) {
		if cErr != nil {
			done(nil, cErr)
			return
		}
		var cr v1.GetRecoveryChallengeResponse
		if err := cr.Unmarshal(cBytes); err != nil || len(cr.Challenge) == 0 {
			done(nil, devicesec.ErrInvalid)
			return
		}
		msg := buildRecoveryContextSignBytes(cr.Challenge, nil, draft.RecoveryID)
		e.requestKeySignKind(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, draft.EdSlot, msg, func(sig []byte, sErr error) {
			if sErr != nil {
				done(nil, sErr)
				return
			}
			ctxReq, _ := (&v1.GetRecoveryContextRequest{
				RecoveryId: draft.RecoveryID,
				Challenge:  cr.Challenge,
				Signature:  sig,
			}).Marshal()
			e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_RECOVERY_CONTEXT, op.Server, "", ctxReq, func(respBytes []byte, gErr *rpcError) {
				if gErr != nil {
					done(nil, gErr)
					return
				}
				var resp v1.GetRecoveryContextResponse
				if err := resp.Unmarshal(respBytes); err != nil {
					done(nil, devicesec.ErrInvalid)
					return
				}
				if resp.Phase != v1.GetRecoveryContextResponse_PHASE_REJECTED {
					done(nil, devicesec.ErrStale)
					return
				}
				edSlot, xSlot := draft.EdSlot, draft.XSlot
				e.clearOperationCAS(op, func(clErr error) {
					if clErr != nil {
						done(nil, clErr)
						return
					}
					if edSlot != "" {
						e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, edSlot)
					}
					if xSlot != "" {
						e.reqKeyDiscard(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_X, xSlot)
					}
					done(nil, nil)
				})
			})
		})
	})
}
