package security

import (
	"crypto/sha256"
	"errors"
	"slices"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

func (e *Engine) renewLeaseOp(fgGen uint64, force bool, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	finish := func(res *corev1.SecurityWorkflowResultEffect, err error) {
		if done != nil {
			done(res, err)
		}
	}
	if err := e.checkFG(fgGen); err != nil {
		finish(nil, err)
		return
	}
	runAfterPre := func() {
		pending := wf.pendingOp
		if pending != nil && pending.Kind != "offer" && pending.Kind != "delete-records" {
			finish(nil, nil)
			return
		}
		e.syncStates(false, "", func(sErr error) {
			if sErr != nil {
				finish(nil, sErr)
				return
			}
			if err := e.checkFG(fgGen); err != nil {
				finish(nil, err)
				return
			}
			if !e.isAuthority() {
				finish(nil, nil)
				return
			}
			var currentLease *devicesec.LeaseCertificate
			if e.hasLease && e.lease.Head == e.headHash {
				l := e.lease
				currentLease = &l
			}
			if !force && (pending == nil || len(pending.Retry) == 0) && !needsLeaseRenewal(currentLease, e.effectiveNow(e.lastNowMs)) {
				finish(nil, nil)
				return
			}
			// If an independent offer/delete-records op is pending, attach retry lease cleanly onto pending.Retry.
			if pending != nil {
				if pending.Server != e.effectiveServer() || pending.MemberID != e.selfDeviceID {
					finish(nil, devicesec.ErrStale)
					return
				}
				var existingCert *devicesec.LeaseCertificate
				if len(pending.Retry) > 0 {
					var retryReq v1.PutSecurityStateRequest
					if retryReq.Unmarshal(pending.Retry) == nil && len(retryReq.Lease) > 0 {
						if lc, err := devicesec.ParseLease(retryReq.Lease); err == nil && lc.Head == e.headHash && lc.Verify(e.head) == nil {
							existingCert = &lc
						}
					}
				}
				effNow := e.effectiveNow(e.lastNowMs)
				needSign := existingCert == nil || effNow >= existingCert.NotAfter || (currentLease != nil && currentLease.IssuedAt > existingCert.IssuedAt)
				uploadPendingLease := func(curPending *PersistedOperation, leaseBytes []byte, reqBytes []byte) {
					e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_SECURITY_STATE, curPending.Server, "", true, fgGen, reqBytes, func(_ []byte, rErr *rpcError) {
						if rErr != nil {
							finish(nil, rErr)
							return
						}
						e.installRecordCAS(nil, nil, nil, leaseBytes, e.lastNowMs, nil, func(_ *PersistedRecord, iErr error) {
							if iErr != nil {
								finish(nil, iErr)
								return
							}
							clearedRetry := *curPending
							clearedRetry.Retry = nil
							e.saveOperationCAS(curPending, &clearedRetry, func(_ *PersistedOperation, scErr error) {
								finish(nil, scErr)
							})
						})
					})
				}
				if needSign {
					e.buildAndSignLease(e.head, corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_ED, "", e.currentSigningTime(e.lastNowMs), func(fresh devicesec.LeaseCertificate, lErr error) {
						if lErr != nil {
							finish(nil, lErr)
							return
						}
						if err := e.checkFG(fgGen); err != nil {
							finish(nil, err)
							return
						}
						lb, _ := fresh.MarshalBinary()
						reqBytes, _ := (&v1.PutSecurityStateRequest{Lease: lb}).Marshal()
						nextP := *pending
						nextP.Retry = reqBytes
						e.saveOperationCAS(pending, &nextP, func(saved *PersistedOperation, sErr error) {
							if sErr != nil {
								finish(nil, sErr)
								return
							}
							uploadPendingLease(saved, lb, reqBytes)
						})
					})
					return
				}
				lb, _ := existingCert.MarshalBinary()
				uploadPendingLease(pending, lb, pending.Retry)
				return
			}

			op := &PersistedOperation{
				ID:       e.nextWorkflowToken(),
				Revision: 1,
				Server:   e.effectiveServer(),
				MemberID: e.selfDeviceID,
				Kind:     "lease",
				Parent:   e.headHash,
				Phase:    "PREPARED",
			}
			hh := e.headHash
			e.claimOperationCAS(op, &hh, e.selfDeviceID, func(saved *PersistedOperation, cErr error) {
				if cErr != nil {
					finish(nil, cErr)
					return
				}
				e.resumeLeaseOp(saved, fgGen, finish)
			})
		})
	}

	if wf.pendingOp != nil && wf.pendingOp.Kind != "delete-records" {
		e.resumePendingOp(fgGen, func(_ *corev1.SecurityWorkflowResultEffect, rErr error) {
			if rErr != nil {
				finish(nil, rErr)
				return
			}
			runAfterPre()
		})
		return
	}
	runAfterPre()
}

func (e *Engine) prepareCreationOp(fgGen uint64, rawServer string, storagePersisted bool, done func(*corev1.SecurityWorkflowResultEffect, error)) {
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
	if op := wf.pendingOp; op != nil {
		if op.Kind != "create" || op.Server != server {
			done(nil, newWfError("conflict", text.KeySecurityFinishTheOriginalSecurityOperationFirst))
			return
		}
		persisted := storagePersisted
		if op.Create != nil {
			persisted = op.Create.StoragePersisted
		}
		done(&corev1.SecurityWorkflowResultEffect{
			ResumedDraft: true,
			CodeSaved:    op.CodeSaved,
			Flag:         persisted,
		}, nil)
		return
	}
	if wf.record != nil || wf.regIntent != nil || wf.bootstrapInvalid {
		done(nil, devicesec.ErrStale)
		return
	}
	const edSlot = "create-ed"
	const xSlot = "create-x"
	e.withEntropy(128, func() {
		if err := e.checkFG(fgGen); err != nil {
			done(nil, err)
			return
		}
		e.reqKeyGen(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, edSlot, true, func(edPub []byte, edErr error) {
			if edErr != nil {
				done(nil, edErr)
				return
			}
			if err := e.checkFG(fgGen); err != nil {
				done(nil, err)
				return
			}
			e.reqKeyGen(corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_X, xSlot, true, func(xPub []byte, xErr error) {
				if xErr != nil {
					done(nil, xErr)
					return
				}
				if err := e.checkFG(fgGen); err != nil {
					done(nil, err)
					return
				}
				var rootRaw, domain, recoverySeed, secret [32]byte
				if !e.readRandom(rootRaw[:]) || !e.readRandom(domain[:]) ||
					!e.readRandom(recoverySeed[:]) || !e.readRandom(secret[:]) {
					zeroBytes(rootRaw[:])
					zeroBytes(recoverySeed[:])
					done(nil, ErrNeedEntropy)
					return
				}
				defer func() {
					zeroBytes(rootRaw[:])
					zeroBytes(recoverySeed[:])
					zeroBytes(secret[:])
				}()

				acctSeed, err := deriveAccountSignSeed(rootRaw[:])
				if err != nil {
					done(nil, err)
					return
				}
				defer zeroBytes(acctSeed)
				acctPub, err := devicesec.Ed25519PublicFromSeed(acctSeed)
				if err != nil {
					done(nil, err)
					return
				}
				recPubBytes, err := devicesec.Ed25519PublicFromSeed(recoverySeed[:])
				if err != nil {
					done(nil, err)
					return
				}
				var recEd [32]byte
				copy(recEd[:], recPubBytes)
				member, err := devicesec.NewMember(edPub, xPub, devicesec.Client)
				if err != nil {
					done(nil, err)
					return
				}
				now := max(1, uint64(max(1, e.lastNowMs)))
				acctID := devicesec.ID(acctPub)

				// Pre-sign DeviceRegistration before clearing ephemeral accountSeed in defer.
				regBody, err := (&v1.DeviceRegistration{
					AccountPublicKey: slices.Clone(acctPub),
					DevicePublicKey:  slices.Clone(member.EdPublic[:]),
					Kind:             v1.DeviceKind_DEVICE_KIND_CLIENT,
					CreatedMs:        int64(now),
				}).Marshal()
				if err != nil {
					done(nil, err)
					return
				}
				regSignInput := make([]byte, 0, len(registerContext)+len(regBody))
				regSignInput = append(regSignInput, registerContext...)
				regSignInput = append(regSignInput, regBody...)
				regSig, err := devicesec.Ed25519SignSeed(acctSeed, regSignInput)
				if err != nil {
					done(nil, err)
					return
				}
				regReqBytes, err := (&v1.RegisterDeviceRequest{
					Registration:    regBody,
					Signature:       regSig,
					ProtocolVersion: protocolVersion,
				}).Marshal()
				if err != nil {
					done(nil, err)
					return
				}

				secretCopy := slices.Clone(secret[:])
				recSeedCopy := recoverySeed
				e.buildAndSignStateWithKEM(
					corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED,
					edSlot,
					true,
					fgGen,
					func(hpke devicesec.HPKESender) (devicesec.State, error) {
						return devicesec.NewGenesis(acctID, domain, member, recEd, secretCopy, now, hpke)
					},
					[]devicesec.Member{member},
					func(genesis devicesec.State, gErr error) {
						defer zeroBytes(recSeedCopy[:])
						zeroBytes(secretCopy)
						if gErr != nil {
							done(nil, gErr)
							return
						}
						genHash, err := genesis.Hash()
						if err != nil {
							done(nil, err)
							return
						}
						genBytes, err := genesis.MarshalBinary()
						if err != nil {
							done(nil, err)
							return
						}
						code := EncodeRecoveryCode(recSeedCopy, genHash)
						op := &PersistedOperation{
							ID:               e.nextWorkflowToken(),
							Revision:         1,
							Server:           server,
							MemberID:         member.ID,
							Kind:             "create",
							Phase:            "PREPARED",
							Signed:           regReqBytes,
							StoragePersisted: storagePersisted,
							Create: &PersistedCreateDraft{
								AccountPublicKey: acctPub,
								EdSlot:           edSlot,
								XSlot:            xSlot,
								StoragePersisted: storagePersisted,
								States:           [][]byte{genBytes},
							},
						}
						e.claimOperationCAS(op, nil, member.ID, func(_ *PersistedOperation, cErr error) {
							if cErr != nil {
								done(nil, cErr)
								return
							}
							wf.server = server
							done(&corev1.SecurityWorkflowResultEffect{
								RecoveryCode: code,
								Flag:         storagePersisted,
							}, nil)
						})
					},
				)
			})
		})
	})
}

func (e *Engine) confirmCreationCodeSavedOp(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	if err := e.checkFG(fgGen); err != nil {
		done(nil, err)
		return
	}
	op := wf.pendingOp
	if op == nil || op.Kind != "create" {
		done(nil, devicesec.ErrStale)
		return
	}
	next := *op
	next.CodeSaved = true
	e.saveOperationCAS(op, &next, func(saved *PersistedOperation, sErr error) {
		if sErr != nil {
			done(nil, sErr)
			return
		}
		if saved.Phase == "PUBLISHED" {
			e.adoptCreationOp(saved, fgGen, done)
			return
		}
		done(&corev1.SecurityWorkflowResultEffect{CodeSaved: true}, nil)
	})
}

func (e *Engine) continueCreationOp(fgGen uint64, setupToken string, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	if err := e.checkFG(fgGen); err != nil {
		done(nil, err)
		return
	}
	op := wf.pendingOp
	if op == nil || op.Kind != "create" || op.Create == nil || len(op.Create.States) == 0 || len(op.Signed) == 0 {
		done(nil, devicesec.ErrStale)
		return
	}
	if op.Phase == "PUBLISHED" {
		if op.CodeSaved {
			e.adoptCreationOp(op, fgGen, done)
			return
		}
		done(&corev1.SecurityWorkflowResultEffect{
			CodeSaved:             false,
			NeedsRecoveryRotation: true,
		}, nil)
		return
	}
	draft := op.Create
	edSlot := draft.EdSlot

	finishWithToken := func(tok string) {
		genesisBytes := draft.States[0]
		genesis, err := devicesec.ParseState(genesisBytes)
		if err != nil {
			done(nil, err)
			return
		}
		pinBytes, _ := (&v1.PinSecurityDomainRequest{GenesisState: genesisBytes}).Marshal()
		e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PIN_SECURITY_DOMAIN, op.Server, tok, pinBytes, func(_ []byte, pErr *rpcError) {
			if pErr != nil {
				done(nil, pErr)
				return
			}
			if err := e.checkFG(fgGen); err != nil {
				done(nil, err)
				return
			}
			signAt := max(uint64(max(1, e.lastNowMs)), genesis.Core.IssuedAt)
			e.buildAndSignLease(genesis, corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, edSlot, signAt, func(lease devicesec.LeaseCertificate, lErr error) {
				if lErr != nil {
					done(nil, lErr)
					return
				}
				if err := e.checkFG(fgGen); err != nil {
					done(nil, err)
					return
				}
				lb, _ := lease.MarshalBinary()
				nextDraft := *draft
				nextDraft.Lease = lb
				nextOp := *wf.pendingOp
				nextOp.Create = &nextDraft
				e.saveOperationCAS(wf.pendingOp, &nextOp, func(saved *PersistedOperation, sErr error) {
					if sErr != nil {
						done(nil, sErr)
						return
					}
					putLeaseReq, _ := (&v1.PutSecurityStateRequest{Lease: lb}).Marshal()
					e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_SECURITY_STATE, saved.Server, tok, putLeaseReq, func(_ []byte, uErr *rpcError) {
						if uErr != nil {
							done(nil, uErr)
							return
						}
						if err := e.checkFG(fgGen); err != nil {
							done(nil, err)
							return
						}
						pubOp := *saved
						pubOp.Phase = "PUBLISHED"
						e.saveOperationCAS(saved, &pubOp, func(pubSaved *PersistedOperation, p2Err error) {
							if p2Err != nil {
								done(nil, p2Err)
								return
							}
							wf.cachedToken = tok
							e.out = append(e.out, &corev1.Effect{
								Effect: &corev1.Effect_SecurityTokenUpdated{
									SecurityTokenUpdated: &corev1.SecurityTokenUpdatedEffect{
										Token: tok,
									},
								},
							})
							if pubSaved.CodeSaved {
								e.adoptCreationOp(pubSaved, fgGen, done)
								return
							}
							done(&corev1.SecurityWorkflowResultEffect{
								CodeSaved:             false,
								NeedsRecoveryRotation: true,
							}, nil)
						})
					})
				})
			})
		})
	}

	var baseReq v1.RegisterDeviceRequest
	if err := baseReq.Unmarshal(op.Signed); err != nil {
		done(nil, devicesec.ErrInvalid)
		return
	}
	if setupToken == "" && len(op.Retry) > 0 {
		var prevRetry v1.RegisterDeviceRequest
		if prevRetry.Unmarshal(op.Retry) == nil {
			setupToken = prevRetry.SetupToken
		}
	}

	e.forceLoginToken(op.Server, corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, edSlot, op.MemberID, true, fgGen, false, func(tok string, lErr error) {
		if lErr == nil {
			finishWithToken(tok)
			return
		}
		var re *rpcError
		if !errors.As(lErr, &re) || (re.code != connectCodePermissionDenied && re.code != connectCodeNotFound && re.code != connectCodeUnauthenticated) {
			done(nil, lErr)
			return
		}

		registerAttempt := func(curOp *PersistedOperation, proof *v1.SignupProof, onRegErr func(*rpcError)) {
			req := baseReq
			req.SetupToken = setupToken
			req.SignupProof = proof
			reqBytes, _ := (&req).Marshal()
			next := *curOp
			next.Retry = reqBytes
			e.saveOperationCAS(curOp, &next, func(saved *PersistedOperation, sErr error) {
				if sErr != nil {
					done(nil, sErr)
					return
				}
				e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_REGISTER_DEVICE, saved.Server, "", reqBytes, func(respBytes []byte, rErr *rpcError) {
					if rErr != nil {
						onRegErr(rErr)
						return
					}
					var resp v1.RegisterDeviceResponse
					if err := resp.Unmarshal(respBytes); err != nil || resp.DeviceId != saved.MemberID {
						done(nil, devicesec.ErrUntrusted)
						return
					}
					e.forceLoginToken(saved.Server, corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED, edSlot, saved.MemberID, true, fgGen, false, func(freshTok string, flErr error) {
						if flErr != nil {
							done(nil, flErr)
							return
						}
						finishWithToken(freshTok)
					})
				})
			})
		}

		registerAttempt(op, nil, func(regErr *rpcError) {
			if regErr.code != connectCodeFailedPrecondition {
				done(nil, regErr)
				return
			}
			// Solve PoW signup challenge.
			chalReq, _ := (&v1.GetSignupChallengeRequest{}).Marshal()
			e.reqRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_SIGNUP_CHALLENGE, op.Server, "", chalReq, func(scBytes []byte, scErr *rpcError) {
				if scErr != nil {
					done(nil, scErr)
					return
				}
				var sc v1.GetSignupChallengeResponse
				if err := sc.Unmarshal(scBytes); err != nil || len(sc.Challenge) == 0 || sc.DifficultyBits > 24 ||
					(e.lastNowMs > 0 && sc.ExpiresMs <= e.lastNowMs) {
					done(nil, newWfError("failed_precondition", text.KeySecurityRegistrationProofOfWorkIsInvalidOrExpired))
					return
				}
				if err := e.checkFG(fgGen); err != nil {
					done(nil, err)
					return
				}
				nonce, ok := solveSignupPoW(sc.Challenge, sc.DifficultyBits)
				if !ok {
					done(nil, newWfError("failed_precondition", text.KeySecurityRegistrationProofOfWorkExpiredRetryTheOriginalDraft))
					return
				}
				registerAttempt(wf.pendingOp, &v1.SignupProof{
					Challenge: sc.Challenge,
					Nonce:     nonce,
				}, func(powErr *rpcError) {
					done(nil, powErr)
				})
			})
		})
	})
}

func solveSignupPoW(challenge []byte, difficultyBits uint32) ([]byte, bool) {
	if len(challenge) == 0 || difficultyBits > 24 {
		return nil, false
	}
	var nonce [8]byte
	buf := make([]byte, len(challenge)+8)
	copy(buf, challenge)
	for n := uint64(0); n < 1<<28; n++ {
		nonce[0] = byte(n >> 56)
		nonce[1] = byte(n >> 48)
		nonce[2] = byte(n >> 40)
		nonce[3] = byte(n >> 32)
		nonce[4] = byte(n >> 24)
		nonce[5] = byte(n >> 16)
		nonce[6] = byte(n >> 8)
		nonce[7] = byte(n)
		copy(buf[len(challenge):], nonce[:])
		sum := sha256.Sum256(buf)
		ok := true
		for bit := range difficultyBits {
			if (sum[bit>>3] & (128 >> (bit & 7))) != 0 {
				ok = false
				break
			}
		}
		if ok {
			return nonce[:], true
		}
	}
	return nil, false
}

func (e *Engine) adoptCreationWithoutCodeOp(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	if err := e.checkFG(fgGen); err != nil {
		done(nil, err)
		return
	}
	op := wf.pendingOp
	if op == nil || op.Kind != "create" || op.Phase != "PUBLISHED" {
		done(nil, devicesec.ErrStale)
		return
	}
	e.adoptCreationOp(op, fgGen, done)
}

func (e *Engine) adoptCreationOp(op *PersistedOperation, fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	draft := op.Create
	if draft == nil || len(draft.States) == 0 || len(draft.AccountPublicKey) != 32 {
		done(nil, devicesec.ErrInvalid)
		return
	}
	genesis, err := devicesec.ParseState(draft.States[0])
	if err != nil {
		done(nil, err)
		return
	}
	pin, err := genesis.Pin()
	if err != nil {
		done(nil, err)
		return
	}
	var acctPub [32]byte
	copy(acctPub[:], draft.AccountPublicKey)
	prof := &PersistedPublicProfile{
		Server:           op.Server,
		Pin:              pin,
		AccountPublicKey: acctPub,
	}
	profBytes, err := prof.Encode()
	if err != nil {
		done(nil, err)
		return
	}
	rec := &PersistedRecord{
		Pin:                   pin,
		MemberID:              op.MemberID,
		Member:                true,
		Server:                op.Server,
		AccountPublicKey:      slices.Clone(acctPub[:]),
		States:                draft.States,
		RecoveryProofs:        make([][]byte, len(draft.States)),
		Lease:                 draft.Lease,
		FloorMs:               e.lastNowMs,
		NeedsRecoveryRotation: !op.CodeSaved,
	}
	recBytes, err := rec.Encode()
	if err != nil {
		done(nil, err)
		return
	}
	rev := e.nextWorkflowToken()
	opRev := u64Str(op.Revision)
	if wf.pendingOpRev != "" {
		opRev = wf.pendingOpRev
	}
	e.reqStorage(&corev1.SecurityStorageEffect{
		Op:            corev1.SecurityStorageOp_SECURITY_STORAGE_OP_BATCH_COMMIT,
		PromoteEdSlot: draft.EdSlot,
		PromoteXSlot:  draft.XSlot,
		DiscardSlots:  []string{draft.EdSlot, draft.XSlot},
		Batch: []*corev1.SecurityStorageWrite{
			{
				Bucket:       corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_PUBLIC_PROFILE,
				Key:          PublicProfileStorageKey(op.Server, pin),
				NextRevision: rev,
				Data:         profBytes,
			},
			{
				Bucket:       corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD,
				Key:          "record",
				CreateOnly:   true,
				NextRevision: rev,
				Data:         recBytes,
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
		wf.server = op.Server
		wf.record = rec
		wf.recordRev = rev
		wf.pendingOp = nil
		wf.pendingOpRev = ""
		wf.publicProfiles[PublicProfileStorageKey(op.Server, pin)] = prof
		wf.legacyRepairNotice = false
		wf.deviceEdPresent = true
		wf.deviceXPresent = true
		if len(rep.deviceEdPub) == 32 {
			wf.deviceEdPub = slices.Clone(rep.deviceEdPub)
		} else if m, ok := genesis.Core.Member(op.MemberID); ok {
			wf.deviceEdPub = slices.Clone(m.EdPublic[:])
		}
		if len(rep.deviceXPub) == 32 {
			wf.deviceXPub = slices.Clone(rep.deviceXPub)
		} else if m, ok := genesis.Core.Member(op.MemberID); ok {
			wf.deviceXPub = slices.Clone(m.XPublic[:])
		}
		e.applyPersistedRecordToEngine(rec, func(unlockErr error) {
			if unlockErr != nil {
				done(nil, unlockErr)
				return
			}
			// Now persist notify_blob with newly unlocked head secret.
			if ntf := e.buildNotifyRecordBytes(rec, genesis); len(ntf) > 0 {
				e.reqStorage(&corev1.SecurityStorageEffect{
					Op:           corev1.SecurityStorageOp_SECURITY_STORAGE_OP_WRITE_CAS,
					Bucket:       corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_NOTIFY_RECORD,
					Key:          "notify",
					NextRevision: rev,
					Data:         ntf,
				}, func(storageReply, error) {})
			}
			done(&corev1.SecurityWorkflowResultEffect{
				CodeSaved:             op.CodeSaved,
				NeedsRecoveryRotation: !op.CodeSaved,
				AccountAdopted: &corev1.SecurityAccountAdopted{
					Server:                op.Server,
					DeviceId:              op.MemberID,
					AccountId:             pin.Account,
					NeedsRecoveryRotation: !op.CodeSaved,
				},
			}, nil)
		})
	})
}

func (e *Engine) removeDeviceOp(fgGen uint64, id string, abandonPairDelivery bool, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	if err := e.checkFG(fgGen); err != nil {
		done(nil, err)
		return
	}
	proceedAfterPre := func() {
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
			if id == e.selfDeviceID {
				done(nil, newWfError("invalid", text.KeySecurityTheManagingClientCannotBeRemovedDirectlyTransferManagement))
				return
			}
			if _, ok := e.head.Core.Member(id); !ok {
				done(nil, nil)
				return
			}
			hh := e.headHash
			op := &PersistedOperation{
				ID:             e.nextWorkflowToken(),
				Revision:       1,
				Server:         e.effectiveServer(),
				MemberID:       e.selfDeviceID,
				Kind:           "state",
				Parent:         hh,
				Phase:          "PREPARED",
				Action:         "remove",
				TargetDeviceID: id,
			}
			e.claimOperationCAS(op, &hh, e.selfDeviceID, func(saved *PersistedOperation, cErr error) {
				if cErr != nil {
					done(nil, cErr)
					return
				}
				e.resumePendingOp(fgGen, done)
			})
		})
	}

	if pending := wf.pendingOp; pending != nil && abandonPairDelivery && pending.Pair != nil && pending.Pair.CandidateID == id {
		e.syncStates(false, "", func(sErr error) {
			if sErr != nil {
				done(nil, sErr)
				return
			}
			var orig v1.PutSecurityStateRequest
			if orig.Unmarshal(pending.Signed) != nil || len(orig.States) == 0 {
				done(nil, devicesec.ErrStale)
				return
			}
			ts, err := devicesec.ParseState(orig.States[len(orig.States)-1].State)
			if err != nil {
				done(nil, err)
				return
			}
			th, _ := ts.Hash()
			if _, ok := e.byHash[th]; !ok {
				done(nil, devicesec.ErrStale)
				return
			}
			e.clearOperationCAS(pending, func(cErr error) {
				if cErr != nil {
					done(nil, cErr)
					return
				}
				proceedAfterPre()
			})
		})
		return
	}
	e.resumePendingOp(fgGen, func(_ *corev1.SecurityWorkflowResultEffect, rErr error) {
		if rErr != nil {
			done(nil, rErr)
			return
		}
		proceedAfterPre()
	})
}

func (e *Engine) deleteMachineRecordsOp(fgGen uint64, machineID string, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	e.resumePendingOp(fgGen, func(_ *corev1.SecurityWorkflowResultEffect, rErr error) {
		if rErr != nil {
			done(nil, rErr)
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
			if _, live := e.head.Core.Member(machineID); live {
				done(nil, devicesec.ErrUntrusted)
				return
			}
			var wasMachine bool
			for _, s := range e.states {
				if m, ok := s.Core.Member(machineID); ok && m.Kind == devicesec.Machine {
					wasMachine = true
					break
				}
			}
			if !wasMachine {
				done(nil, devicesec.ErrUntrusted)
				return
			}
			if wf.pendingOp != nil {
				done(nil, newWfError("pending", text.KeySecurityFinishTheCurrentSecurityOperationFirst))
				return
			}
			reqBytes, _ := (&v1.DeleteMachineRecordsRequest{MachineId: machineID}).Marshal()
			hh := e.headHash
			op := &PersistedOperation{
				ID:             e.nextWorkflowToken(),
				Revision:       1,
				Server:         e.effectiveServer(),
				MemberID:       e.selfDeviceID,
				Kind:           "delete-records",
				Parent:         hh,
				Phase:          "PREPARED",
				TargetDeviceID: machineID,
				Signed:         reqBytes,
			}
			e.claimOperationCAS(op, &hh, e.selfDeviceID, func(_ *PersistedOperation, cErr error) {
				if cErr != nil {
					done(nil, cErr)
					return
				}
				e.resumePendingOp(fgGen, done)
			})
		})
	})
}

func (e *Engine) deleteAccountOp(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
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
		if !e.isAuthority() {
			done(nil, devicesec.ErrUntrusted)
			return
		}
		head := e.headHash
		chalReq, _ := (&v1.GetChallengeRequest{DeviceId: e.selfDeviceID}).Marshal()
		server := e.effectiveServer()
		e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_CHALLENGE, server, "", true, fgGen, chalReq, func(respBytes []byte, rErr *rpcError) {
			if rErr != nil {
				done(nil, rErr)
				return
			}
			var chal v1.GetChallengeResponse
			if err := chal.Unmarshal(respBytes); err != nil || len(chal.Challenge) != 32 {
				done(nil, devicesec.ErrInvalid)
				return
			}
			if err := e.checkFG(fgGen); err != nil {
				done(nil, err)
				return
			}
			if !e.isAuthority() || e.headHash != head || wf.pendingOp != nil {
				done(nil, devicesec.ErrStale)
				return
			}
			msg := make([]byte, 0, len(deleteAccountPrefix)+len(e.pin.Account)+1+32+len(chal.Challenge))
			msg = append(msg, deleteAccountPrefix...)
			msg = append(msg, e.pin.Account...)
			msg = append(msg, 0)
			msg = append(msg, head[:]...)
			msg = append(msg, chal.Challenge...)
			e.requestKeySignKind(corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_ED, "", msg, func(sig []byte, signErr error) {
				if signErr != nil {
					done(nil, signErr)
					return
				}
				if err := e.checkFG(fgGen); err != nil {
					done(nil, err)
					return
				}
				delReq, _ := (&v1.DeleteAccountRequest{
					StateHash: head[:],
					Challenge: chal.Challenge,
					Signature: sig,
				}).Marshal()
				// Explicit foreground-only RPC with zero automatic retry.
				e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_DELETE_ACCOUNT, server, "", true, fgGen, delReq, func(_ []byte, dErr *rpcError) {
					if dErr != nil {
						done(nil, dErr)
						return
					}
					e.clearLocalSecurityState(func(cErr error) {
						done(nil, cErr)
					})
				})
			})
		})
	})
}

func (e *Engine) prepareRecoveryRotationOp(fgGen uint64, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	e.resumePendingOp(fgGen, func(_ *corev1.SecurityWorkflowResultEffect, rErr error) {
		if rErr != nil {
			done(nil, rErr)
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
			if wf.pendingOp != nil {
				done(nil, newWfError("pending", text.KeySecurityFinishTheCurrentSecurityOperationFirst))
				return
			}
			e.withEntropy(64, func() {
				if err := e.checkFG(fgGen); err != nil {
					done(nil, err)
					return
				}
				var seed [32]byte
				if !e.readRandom(seed[:]) {
					done(nil, ErrNeedEntropy)
					return
				}
				defer zeroBytes(seed[:])
				pub, err := devicesec.Ed25519PublicFromSeed(seed[:])
				if err != nil {
					done(nil, err)
					return
				}
				wf.codeDraft = &codeDraft{
					kind:       "state",
					parent:     e.headHash,
					recoveryEd: pub,
				}
				done(&corev1.SecurityWorkflowResultEffect{
					RecoveryCode: EncodeRecoveryCode(seed, e.pin.Genesis),
				}, nil)
			})
		})
	})
}

func (e *Engine) prepareHandoffCodeOp(fgGen uint64, storagePersisted bool, done func(*corev1.SecurityWorkflowResultEffect, error)) {
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
		if !e.isMember || e.isAuthority() {
			done(nil, devicesec.ErrUntrusted)
			return
		}
		e.withEntropy(64, func() {
			if err := e.checkFG(fgGen); err != nil {
				done(nil, err)
				return
			}
			var seed [32]byte
			if !e.readRandom(seed[:]) {
				done(nil, ErrNeedEntropy)
				return
			}
			defer zeroBytes(seed[:])
			pub, err := devicesec.Ed25519PublicFromSeed(seed[:])
			if err != nil {
				done(nil, err)
				return
			}
			wf.codeDraft = &codeDraft{
				kind:             "offer",
				parent:           e.headHash,
				recoveryEd:       pub,
				storagePersisted: storagePersisted,
			}
			done(&corev1.SecurityWorkflowResultEffect{
				RecoveryCode: EncodeRecoveryCode(seed, e.pin.Genesis),
				Flag:         storagePersisted,
			}, nil)
		})
	})
}

func (e *Engine) confirmCodeOp(fgGen uint64, kind string, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	if err := e.checkFG(fgGen); err != nil {
		done(nil, err)
		return
	}
	if op := wf.pendingOp; op != nil {
		if op.Kind != kind || (kind == "state" && op.Action != "recovery") {
			done(nil, devicesec.ErrStale)
			return
		}
		if op.Server != e.effectiveServer() || op.MemberID != e.selfDeviceID {
			done(nil, devicesec.ErrStale)
			return
		}
		if !op.CodeSaved {
			next := *op
			next.CodeSaved = true
			e.saveOperationCAS(op, &next, func(_ *PersistedOperation, sErr error) {
				if sErr != nil {
					done(nil, sErr)
					return
				}
				e.resumePendingOp(fgGen, done)
			})
			return
		}
		e.resumePendingOp(fgGen, done)
		return
	}

	draft := wf.codeDraft
	if draft == nil || draft.kind != kind {
		done(nil, devicesec.ErrStale)
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
		if kind == "state" {
			if !e.isAuthority() || e.headHash != draft.parent {
				done(nil, devicesec.ErrStale)
				return
			}
		} else if !e.isMember || e.isAuthority() {
			done(nil, devicesec.ErrUntrusted)
			return
		}
		hh := e.headHash
		op := &PersistedOperation{
			ID:               e.nextWorkflowToken(),
			Revision:         1,
			Server:           e.effectiveServer(),
			MemberID:         e.selfDeviceID,
			Kind:             kind,
			Parent:           hh,
			Phase:            "PREPARED",
			CodeSaved:        true,
			RecoveryEd:       slices.Clone(draft.recoveryEd),
			StoragePersisted: draft.storagePersisted,
		}
		if kind == "state" {
			op.Action = "recovery"
		}
		e.claimOperationCAS(op, &hh, e.selfDeviceID, func(_ *PersistedOperation, cErr error) {
			if cErr != nil {
				done(nil, cErr)
				return
			}
			wf.codeDraft = nil
			e.resumePendingOp(fgGen, done)
		})
	})
}

func (e *Engine) verifiedHandoffCandidates(devices []DeviceOfferInfo) []*corev1.SecurityHandoffCandidate {
	if !e.hasHead {
		return nil
	}
	var out []*corev1.SecurityHandoffCandidate
	for _, d := range devices {
		ed := d.RecoveryEd
		if len(ed) != 32 && d.Meta != nil && len(d.Meta.HandoffRecoveryEd) == 32 {
			ed = d.Meta.HandoffRecoveryEd
		}
		if len(ed) != 32 || d.ID == e.selfDeviceID {
			continue
		}
		if m, ok := e.head.Core.Member(d.ID); ok && m.Kind == devicesec.Client {
			out = append(out, &corev1.SecurityHandoffCandidate{
				DeviceId:   d.ID,
				RecoveryEd: slices.Clone(ed),
			})
		}
	}
	return out
}

func (e *Engine) listHandoffCandidatesOp(actionID uint64) {
	wf := e.ensureWF()
	if !e.isAuthority() {
		e.emitActionResult(actionID, nil, devicesec.ErrUntrusted)
		return
	}
	// Try live directory refresh when devices are needed so successor offers are up to date.
	reqBytes, _ := (&v1.ListDevicesRequest{}).Marshal()
	e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LIST_DEVICES, e.effectiveServer(), "", false, 0, reqBytes, func(respBytes []byte, rErr *rpcError) {
		if rErr != nil {
			// Fallback to local verified device snapshot if available.
			cands := e.verifiedHandoffCandidates(wf.devicesSnap)
			e.emitActionResult(actionID, &corev1.SecurityWorkflowResultEffect{
				HandoffCandidates: cands,
			}, nil)
			return
		}
		var lr v1.ListDevicesResponse
		if err := lr.Unmarshal(respBytes); err != nil {
			e.emitActionResult(actionID, nil, devicesec.ErrInvalid)
			return
		}
		items := make([]OpenItem, 0, len(lr.Devices))
		devIDs := make([]string, 0, len(lr.Devices))
		for _, d := range lr.Devices {
			if d != nil && d.Id != "" && d.Meta != nil && len(d.Meta.SecurityHeader) > 0 {
				items = append(items, OpenItem{
					Env:             d.Meta,
					ExpectedPurpose: devicesec.DeviceMeta,
					RequestID:       d.Id,
					History:         true,
				})
				devIDs = append(devIDs, d.Id)
			}
		}
		if len(items) == 0 {
			e.emitActionResult(actionID, &corev1.SecurityWorkflowResultEffect{
				HandoffCandidates: nil,
			}, nil)
			return
		}
		e.openBatchInternal(e.lastNowMs, items, func(res []OpenResult) {
			var infos []DeviceOfferInfo
			for i, r := range res {
				if r.Err != nil {
					continue
				}
				if dm, ok := r.Body.(*v1.DeviceMeta); ok && dm != nil {
					infos = append(infos, DeviceOfferInfo{
						ID:         devIDs[i],
						RecoveryEd: slices.Clone(dm.HandoffRecoveryEd),
						Meta:       dm,
					})
				}
			}
			wf.devicesSnap = infos
			cands := e.verifiedHandoffCandidates(infos)
			e.emitActionResult(actionID, &corev1.SecurityWorkflowResultEffect{
				HandoffCandidates: cands,
			}, nil)
		})
	})
}

func (e *Engine) handoffAuthorityOp(fgGen uint64, incomingID string, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	e.resumePendingOp(fgGen, func(_ *corev1.SecurityWorkflowResultEffect, rErr error) {
		if rErr != nil {
			done(nil, rErr)
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
			// Always verify successor offer against live server devices before signing handoff.
			reqBytes, _ := (&v1.ListDevicesRequest{}).Marshal()
			e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LIST_DEVICES, e.effectiveServer(), "", true, fgGen, reqBytes, func(respBytes []byte, lErr *rpcError) {
				var cands []*corev1.SecurityHandoffCandidate
				findOffer := func(list []*corev1.SecurityHandoffCandidate) *corev1.SecurityHandoffCandidate {
					for _, c := range list {
						if c.DeviceId == incomingID {
							return c
						}
					}
					return nil
				}
				proceed := func(list []*corev1.SecurityHandoffCandidate) {
					offer := findOffer(list)
					if offer == nil || len(offer.RecoveryEd) != 32 {
						done(nil, newWfError("invalid", text.KeySecurityGenerateAndSaveARecoveryCodeOnTheSuccessor))
						return
					}
					if wf.pendingOp != nil {
						done(nil, newWfError("pending", text.KeySecurityFinishTheCurrentSecurityOperationFirst))
						return
					}
					hh := e.headHash
					op := &PersistedOperation{
						ID:             e.nextWorkflowToken(),
						Revision:       1,
						Server:         e.effectiveServer(),
						MemberID:       e.selfDeviceID,
						Kind:           "state",
						Parent:         hh,
						Phase:          "PREPARED",
						Action:         "handoff",
						TargetDeviceID: incomingID,
						RecoveryEd:     slices.Clone(offer.RecoveryEd),
					}
					e.claimOperationCAS(op, &hh, e.selfDeviceID, func(_ *PersistedOperation, cErr error) {
						if cErr != nil {
							done(nil, cErr)
							return
						}
						e.resumePendingOp(fgGen, done)
					})
				}

				if lErr != nil {
					proceed(e.verifiedHandoffCandidates(wf.devicesSnap))
					return
				}
				var lr v1.ListDevicesResponse
				if err := lr.Unmarshal(respBytes); err != nil {
					done(nil, devicesec.ErrInvalid)
					return
				}
				var items []OpenItem
				var ids []string
				for _, d := range lr.Devices {
					if d != nil && d.Id == incomingID && d.Meta != nil && len(d.Meta.SecurityHeader) > 0 {
						items = append(items, OpenItem{
							Env:             d.Meta,
							ExpectedPurpose: devicesec.DeviceMeta,
							RequestID:       d.Id,
							History:         true,
						})
						ids = append(ids, d.Id)
					}
				}
				if len(items) == 0 {
					proceed(e.verifiedHandoffCandidates(wf.devicesSnap))
					return
				}
				e.openBatchInternal(e.lastNowMs, items, func(res []OpenResult) {
					if len(res) > 0 && res[0].Err == nil {
						if dm, ok := res[0].Body.(*v1.DeviceMeta); ok && dm != nil {
							cands = e.verifiedHandoffCandidates([]DeviceOfferInfo{{
								ID:         ids[0],
								RecoveryEd: dm.HandoffRecoveryEd,
								Meta:       dm,
							}})
							proceed(cands)
							return
						}
					}
					proceed(nil)
				})
			})
		})
	})
}

func (e *Engine) updateDeviceMetaOp(actionID uint64, act *corev1.SecurityUpdateDeviceMetaAction) {
	if act == nil {
		e.emitActionResult(actionID, nil, devicesec.ErrInvalid)
		return
	}
	targetID := act.DeviceId
	if targetID == "" {
		targetID = e.selfDeviceID
	}
	if targetID == "" || targetID != e.selfDeviceID {
		e.emitActionResult(actionID, nil, newWfError("invalid", text.KeyErrorsNamesCanOnlyBeChangedOnThatDevice))
		return
	}
	server := e.effectiveServer()
	if server == "" || !e.loaded || !e.isMember {
		e.emitActionResult(actionID, nil, ErrNotMember)
		return
	}
	var tryCAS func(n int)
	tryCAS = func(n int) {
		if n >= 5 {
			e.emitActionResult(actionID, nil, devicesec.ErrStale)
			return
		}
		listReq, _ := (&v1.ListDevicesRequest{}).Marshal()
		e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_LIST_DEVICES, server, "", false, 0, listReq, func(respBytes []byte, lErr *rpcError) {
			if lErr != nil {
				e.emitActionResult(actionID, nil, lErr)
				return
			}
			var lr v1.ListDevicesResponse
			if err := lr.Unmarshal(respBytes); err != nil {
				e.emitActionResult(actionID, nil, devicesec.ErrInvalid)
				return
			}
			var own *v1.Device
			for _, d := range lr.Devices {
				if d != nil && d.Id == e.selfDeviceID {
					own = d
					break
				}
			}
			var rev uint64
			applyUpdate := func(cur *v1.DeviceMeta) {
				nextMeta := &v1.DeviceMeta{}
				if cur != nil {
					nextMeta = cur.Clone()
				}
				if act.DescribeSelfOnly {
					if act.Platform != "" {
						nextMeta.Platform = act.Platform
					}
					if nextMeta.Description == "" && act.Description != "" {
						nextMeta.Description = act.Description
					}
				} else {
					nextMeta.Name = act.Name
					if nextMeta.Platform == "" && act.Platform != "" {
						nextMeta.Platform = act.Platform
					}
					if nextMeta.Description == "" && act.Description != "" {
						nextMeta.Description = act.Description
					}
				}
				plain, err := nextMeta.Marshal()
				if err != nil {
					e.emitActionResult(actionID, nil, err)
					return
				}
				e.withEntropy(64, func() {
					e.sealEnvelopeInternal(e.lastNowMs, devicesec.DeviceMeta, "", "", e.selfDeviceID, rev+1, 0, plain, false, nil, func(sr SealResult) {
						if sr.Err != nil {
							e.emitActionResult(actionID, nil, sr.Err)
							return
						}
						putBytes, _ := (&v1.PutDeviceMetaRequest{
							DeviceId:         e.selfDeviceID,
							ExpectedRevision: rev,
							Meta:             sr.Envelope,
						}).Marshal()
						e.callAuthedRpc(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_DEVICE_META, server, "", false, 0, putBytes, func(pBytes []byte, pErr *rpcError) {
							if pErr != nil {
								e.emitActionResult(actionID, nil, pErr)
								return
							}
							var pr v1.PutDeviceMetaResponse
							if err := pr.Unmarshal(pBytes); err != nil {
								e.emitActionResult(actionID, nil, devicesec.ErrInvalid)
								return
							}
							if pr.Applied {
								e.emitActionResult(actionID, nil, nil)
								return
							}
							tryCAS(n + 1)
						})
					})
				})
			}

			if own != nil {
				rev = own.MetaRevision
				if own.Meta != nil && rev > 0 {
					e.openBatchInternal(e.lastNowMs, []OpenItem{{
						Env:             own.Meta,
						ExpectedPurpose: devicesec.DeviceMeta,
						RequestID:       e.selfDeviceID,
						History:         true,
					}}, func(res []OpenResult) {
						if res[0].Err != nil {
							e.emitActionResult(actionID, nil, res[0].Err)
							return
						}
						dm, _ := res[0].Body.(*v1.DeviceMeta)
						applyUpdate(dm)
					})
					return
				}
			}
			applyUpdate(nil)
		})
	}
	tryCAS(0)
}

func (e *Engine) completeAccountPublicProfileOp(fgGen uint64, rawServer, code string, done func(*corev1.SecurityWorkflowResultEffect, error)) {
	wf := e.ensureWF()
	if err := e.checkFG(fgGen); err != nil {
		done(nil, err)
		return
	}
	server := e.effectiveServer()
	if rawServer != "" {
		srv, err := CheckServerURL(rawServer)
		if err != nil {
			done(nil, err)
			return
		}
		server = srv
	}
	if !e.loaded || !e.hasHead || wf.record == nil {
		done(nil, ErrNotInitialized)
		return
	}
	seed, genPrefix, ok := DecodeRecoveryCode(code)
	if !ok {
		done(nil, newWfError("invalid", errInvalidRecoveryCode.Error()))
		return
	}
	recPub, err := devicesec.Ed25519PublicFromSeed(seed[:])
	if err != nil {
		zeroBytes(seed[:])
		done(nil, err)
		return
	}
	signWithSeed := func(msg []byte) ([]byte, error) {
		return devicesec.Ed25519SignSeed(seed[:], msg)
	}
	e.fetchAndAcceptRecoveryContext(server, recPub, "", genPrefix, wf.record, fgGen, signWithSeed, func(_ *v1.GetRecoveryContextResponse, pin devicesec.Pin, acctPub [32]byte, states [][]byte, proofs [][]byte, cErr error) {
		zeroBytes(seed[:])
		if cErr != nil {
			done(nil, cErr)
			return
		}
		if err := e.checkFG(fgGen); err != nil {
			done(nil, err)
			return
		}
		cur := wf.record
		if cur == nil || cur.Pin != pin {
			done(nil, devicesec.ErrStale)
			return
		}
		prof := &PersistedPublicProfile{
			Server:           server,
			Pin:              pin,
			AccountPublicKey: acctPub,
		}
		profBytes, err := prof.Encode()
		if err != nil {
			done(nil, err)
			return
		}
		nextRec := cur.Clone()
		nextRec.AccountPublicKey = slices.Clone(acctPub[:])
		recBytes, err := nextRec.Encode()
		if err != nil {
			done(nil, err)
			return
		}
		nextRev := e.nextWorkflowToken()
		_, _, _ = states, proofs, acctPub
		e.reqStorage(&corev1.SecurityStorageEffect{
			Op: corev1.SecurityStorageOp_SECURITY_STORAGE_OP_BATCH_COMMIT,
			Batch: []*corev1.SecurityStorageWrite{
				{
					Bucket:       corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_PUBLIC_PROFILE,
					Key:          PublicProfileStorageKey(server, pin),
					NextRevision: nextRev,
					Data:         profBytes,
				},
				{
					Bucket:           corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD,
					Key:              "record",
					ExpectedRevision: wf.recordRev,
					NextRevision:     nextRev,
					Data:             recBytes,
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
			wf.record = nextRec
			wf.recordRev = nextRev
			wf.publicProfiles[PublicProfileStorageKey(server, pin)] = prof
			done(nil, nil)
		})
	})
}

func (e *Engine) checkRetainedDeniedOp(actionID uint64) {
	wf := e.ensureWF()
	server := e.effectiveServer()
	if server == "" || e.selfDeviceID == "" {
		e.emitActionResult(actionID, &corev1.SecurityWorkflowResultEffect{Flag: false}, nil)
		return
	}
	var attemptLogin func(retries int)
	attemptLogin = func(retries int) {
		e.forceLoginToken(server, corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_ED, "", e.selfDeviceID, false, 0, false, func(_ string, err error) {
			if err == nil {
				e.emitActionResult(actionID, &corev1.SecurityWorkflowResultEffect{Flag: false}, nil)
				return
			}
			var re *rpcError
			if errors.As(err, &re) && re.code == connectCodePermissionDenied {
				if retries == 0 {
					attemptLogin(1)
					return
				}
				wf.legacyRepairNotice = wf.legacyRepairNotice
				e.emitActionResult(actionID, &corev1.SecurityWorkflowResultEffect{Flag: true}, nil)
				return
			}
			e.emitActionResult(actionID, &corev1.SecurityWorkflowResultEffect{Flag: false}, nil)
		})
	}
	attemptLogin(0)
}

func (e *Engine) checkExitAdviceOp(actionID uint64) {
	wf := e.ensureWF()
	unresolved := wf.pendingOp != nil || wf.regIntent != nil
	auth := e.isAuthority()
	msg := text.KeySecurityExitingDeletesLocalKeysYouWillNeedToPair
	if unresolved {
		msg = text.KeySecurityTheSecurityOperationSOutcomeIsUnverifiedContinueThe
	} else if auth {
		msg = text.KeySecurityThisBrowserIsTheOnlyManagingClientTransferManagement
	}
	e.emitActionResult(actionID, &corev1.SecurityWorkflowResultEffect{
		ExitAdvice: &corev1.SecurityExitAdvice{
			Authority:  auth,
			Unresolved: unresolved,
			Message:    msg,
		},
	}, nil)
}

func (e *Engine) clearLocalSecurityState(onDone func(error)) {
	wf := e.ensureWF()
	e.resetWorkflowPairingSessions()
	e.reqStorage(&corev1.SecurityStorageEffect{
		Op:              corev1.SecurityStorageOp_SECURITY_STORAGE_OP_CLEAR_ALL,
		ClearLoginToken: true,
	}, func(_ storageReply, err error) {
		if err != nil {
			onDone(err)
			return
		}
		e.resetChainState()
		e.loaded = false
		e.isMember = false
		e.removed = false
		e.cleared = true
		e.securityLocked = false
		wf.record = nil
		wf.recordRev = ""
		wf.pendingOp = nil
		wf.pendingOpRev = ""
		wf.regIntent = nil
		wf.regIntentRev = ""
		wf.codeDraft = nil
		wf.cachedToken = ""
		wf.tokenExpiresMs = 0
		wf.deviceEdPresent = false
		wf.deviceEdPub = nil
		wf.deviceXPresent = false
		wf.deviceXPub = nil
		clear(wf.publicProfiles)
		e.out = append(e.out, &corev1.Effect{
			Effect: &corev1.Effect_SecurityTokenUpdated{
				SecurityTokenUpdated: &corev1.SecurityTokenUpdatedEffect{Cleared: true},
			},
		})
		if wf.onStateChanged != nil {
			wf.onStateChanged()
		}
		onDone(nil)
	})
}

func (e *Engine) clearSecurityOp(actionID uint64) {
	e.clearLocalSecurityState(func(err error) {
		if err != nil {
			e.emitActionResult(actionID, nil, err)
			return
		}
		e.emitActionResult(actionID, nil, nil)
		e.emitStatusChanged()
	})
}
