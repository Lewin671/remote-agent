package security

import (
	"slices"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

// HandleOpenNotify verifies and decrypts a push notification envelope without SecCall.
// Supports locked Android/iOS mode via persisted notify_blob AND Web Service Worker mode via record_blob + KeyAgree.
func (e *Engine) HandleOpenNotify(nowMs int64, ev *corev1.SecurityOpenNotifyEvent) []*corev1.Effect {
	e.touchNow(nowMs)
	if ev == nil {
		return nil
	}
	emitErr := func(err error) {
		msg := "invalid notify"
		if err != nil {
			msg = err.Error()
		}
		e.out = append(e.out, &corev1.Effect{
			Effect: &corev1.Effect_SecurityNotifyOpened{
				SecurityNotifyOpened: &corev1.SecurityNotifyOpenedEffect{
					OpId:  ev.OpId,
					Error: msg,
				},
			},
		})
	}
	if ev.Envelope == nil {
		emitErr(devicesec.ErrInvalid)
		return e.DrainEffects()
	}

	ct, err := FromProtoEnvelope(ev.Envelope)
	if err != nil {
		emitErr(err)
		return e.DrainEffects()
	}
	h, err := devicesec.ParseHeader(ct.Header)
	if err != nil || h.Purpose != devicesec.Notify {
		emitErr(devicesec.ErrInvalid)
		return e.DrainEffects()
	}
	if ev.SessionId != "" && h.Session != ev.SessionId {
		emitErr(devicesec.ErrUntrusted)
		return e.DrainEffects()
	}

	finishWithPlain := func(plain []byte, verifyUnchanged bool) {
		body, _, err := ValidateAndDecodeBody(h, plain)
		if err != nil {
			emitErr(err)
			return
		}
		nc, ok := body.(*v1.NotifyContent)
		if !ok || nc == nil {
			emitErr(devicesec.ErrInvalid)
			return
		}
		if ev.MachineId != "" && nc.MachineId != ev.MachineId {
			emitErr(devicesec.ErrUntrusted)
			return
		}
		e.out = append(e.out, &corev1.Effect{
			Effect: &corev1.Effect_SecurityNotifyOpened{
				SecurityNotifyOpened: &corev1.SecurityNotifyOpenedEffect{
					OpId:                  ev.OpId,
					Content:               nc,
					HeaderBytes:           slices.Clone(ev.Envelope.Header),
					SessionId:             h.Session,
					MachineId:             h.Machine,
					RequestId:             h.Request,
					VerifyRecordUnchanged: verifyUnchanged,
				},
			},
		})
	}

	// Path A: Android / iOS background notification extension using persisted notify_blob (no private key needed).
	if len(ev.NotifyBlob) > 0 {
		ntf, err := DecodePersistedNotifyRecord(ev.NotifyBlob)
		if err != nil {
			emitErr(err)
			return e.DrainEffects()
		}
		head, err := devicesec.ParseState(ntf.HeadState)
		if err != nil {
			emitErr(err)
			return e.DrainEffects()
		}
		if err := ntf.Pin.Verify(head); err != nil {
			emitErr(err)
			return e.DrainEffects()
		}
		headHash, _ := head.Hash()
		if h.State != headHash {
			emitErr(devicesec.ErrStale)
			return e.DrainEffects()
		}
		if _, err := ct.VerifyOrigin(head, head, false, 0); err != nil {
			emitErr(err)
			return e.DrainEffects()
		}
		openedH, plain, err := devicesec.OpenCipherWithPurposeKey(ntf.PurposeKey[:], nil, ct)
		if err != nil {
			emitErr(err)
			return e.DrainEffects()
		}
		h = openedH
		finishWithPlain(plain, false)
		return e.DrainEffects()
	}

	// Path B: Web Service Worker using persisted record_blob + KeyAgree(DEVICE_X) to authenticate grant.
	if len(ev.RecordBlob) > 0 {
		rec, err := DecodePersistedRecord(ev.RecordBlob)
		if err != nil {
			emitErr(err)
			return e.DrainEffects()
		}
		states, _, _, err := parseAndVerifyChain(rec.Pin, rec.States, rec.RecoveryProofs)
		if err != nil {
			emitErr(err)
			return e.DrainEffects()
		}
		head := states[len(states)-1]
		mem, ok := head.Core.Member(rec.MemberID)
		if !rec.Member || !ok || mem.Kind != devicesec.Client {
			emitErr(ErrNotMember)
			return e.DrainEffects()
		}
		if len(rec.Lease) == 0 {
			emitErr(ErrLeaseExpired)
			return e.DrainEffects()
		}
		lease, err := devicesec.ParseLease(rec.Lease)
		if err != nil {
			emitErr(err)
			return e.DrainEffects()
		}
		atNow := ev.NowMs
		if atNow <= 0 {
			atNow = e.lastNowMs
		}
		if rec.FloorMs > atNow {
			atNow = rec.FloorMs
		}
		if atNow < 1 {
			atNow = 1
		}
		if err := lease.Valid(head, uint64(atNow)); err != nil {
			emitErr(err)
			return e.DrainEffects()
		}
		byHash := make(map[devicesec.Hash]devicesec.State, len(states))
		for _, s := range states {
			sh, _ := s.Hash()
			byHash[sh] = s
		}
		srcState, ok := byHash[h.State]
		if !ok {
			emitErr(devicesec.ErrStale)
			return e.DrainEffects()
		}
		if _, err := ct.VerifyOrigin(srcState, head, false, uint64(atNow)); err != nil {
			emitErr(err)
			return e.DrainEffects()
		}
		enc, _, err := rec.Pin.GrantChallenge(head, rec.MemberID)
		if err != nil {
			emitErr(err)
			return e.DrainEffects()
		}
		e.requestKeyAgreeKind(corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_X, "", enc, false, func(shared []byte, agreeErr error) {
			if agreeErr != nil {
				emitErr(agreeErr)
				return
			}
			sec, err := rec.Pin.OpenGrantWithDH(head, rec.MemberID, shared)
			if err != nil || len(sec) != 32 {
				emitErr(devicesec.ErrUntrusted)
				return
			}
			// Authenticate stored head content key if persisted.
			var headStored []byte
			for _, ck := range rec.ContentKeys {
				if ck.Epoch == head.Core.ContentEpoch {
					headStored = ck.Secret[:]
					break
				}
			}
			if len(headStored) == 32 && !sameBytes(sec, headStored) {
				emitErr(devicesec.ErrUntrusted)
				return
			}
			// Derive notify purpose key and open ciphertext.
			contentSecret := sec
			if h.ContentEpoch != head.Core.ContentEpoch {
				var found bool
				for _, ck := range rec.ContentKeys {
					if ck.Epoch == h.ContentEpoch {
						contentSecret = ck.Secret[:]
						found = true
						break
					}
				}
				if !found {
					emitErr(devicesec.ErrStale)
					return
				}
			}
			pk, err := devicesec.DerivePurposeKeyRaw(contentSecret, rec.Pin.Account, rec.Pin.Domain, h.ContentEpoch, devicesec.Notify)
			if err != nil {
				emitErr(err)
				return
			}
			openedH, plain, err := devicesec.OpenCipherWithPurposeKey(pk, nil, ct)
			if err != nil {
				emitErr(err)
				return
			}
			h = openedH
			finishWithPlain(plain, true)
		})
		return e.DrainEffects()
	}

	emitErr(devicesec.ErrInvalid)
	return e.DrainEffects()
}
