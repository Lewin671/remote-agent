package security

import (
	"slices"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

func (e *Engine) emitNotifyError(opID uint64, err error) {
	msg := "invalid notify"
	if err != nil {
		msg = err.Error()
	}
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_SecurityNotifyOpened{
			SecurityNotifyOpened: &corev1.SecurityNotifyOpenedEffect{
				OpId:  opID,
				Error: msg,
			},
		},
	})
}

func (e *Engine) finishNotifyPlain(ev *corev1.SecurityOpenNotifyEvent, h devicesec.Header, plain []byte, verifyUnchanged bool) {
	body, _, err := ValidateAndDecodeBody(h, plain)
	if err != nil {
		e.emitNotifyError(ev.OpId, err)
		return
	}
	nc, ok := body.(*v1.NotifyContent)
	if !ok || nc == nil {
		e.emitNotifyError(ev.OpId, devicesec.ErrInvalid)
		return
	}
	if ev.MachineId != "" && nc.MachineId != ev.MachineId {
		e.emitNotifyError(ev.OpId, devicesec.ErrUntrusted)
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

type openNotifyTask struct {
	ev   *corev1.SecurityOpenNotifyEvent
	h    devicesec.Header
	ct   devicesec.Ciphertext
	rec  *PersistedRecord
	head devicesec.State
}

func (t *openNotifyTask) onError(e *Engine, err error) {
	e.emitNotifyError(t.ev.OpId, err)
}

func (t *openNotifyTask) step(e *Engine, ev *secEvent) error {
	if ev.err != nil {
		return ev.err
	}
	sec, err := t.rec.Pin.OpenGrantWithDH(t.head, t.rec.MemberID, ev.bytes)
	if err != nil || len(sec) != 32 {
		return devicesec.ErrUntrusted
	}
	// Authenticate stored head content key if persisted.
	for _, ck := range t.rec.ContentKeys {
		if ck.Epoch == t.head.Core.ContentEpoch && !sameBytes(sec, ck.Secret[:]) {
			return devicesec.ErrUntrusted
		}
	}
	contentSecret := sec
	if t.h.ContentEpoch != t.head.Core.ContentEpoch {
		var found bool
		for _, ck := range t.rec.ContentKeys {
			if ck.Epoch == t.h.ContentEpoch {
				c := ck.Secret
				contentSecret = c[:]
				found = true
				break
			}
		}
		if !found {
			return devicesec.ErrStale
		}
	}
	pk, err := devicesec.DerivePurposeKeyRaw(contentSecret, t.rec.Pin.Account, t.rec.Pin.Domain, t.h.ContentEpoch, devicesec.Notify)
	if err != nil {
		return err
	}
	openedH, plain, err := devicesec.OpenCipherWithPurposeKey(pk, nil, t.ct)
	if err != nil {
		return err
	}
	e.finishNotifyPlain(t.ev, openedH, plain, true)
	return nil
}

func (e *Engine) handleOpenNotifyErr(ev *corev1.SecurityOpenNotifyEvent) error {
	if ev.Envelope == nil {
		return devicesec.ErrInvalid
	}
	ct, err := FromProtoEnvelope(ev.Envelope)
	if err != nil {
		return err
	}
	h, err := devicesec.ParseHeader(ct.Header)
	if err != nil || h.Purpose != devicesec.Notify {
		return devicesec.ErrInvalid
	}
	if ev.SessionId != "" && h.Session != ev.SessionId {
		return devicesec.ErrUntrusted
	}
	if len(ev.NotifyBlob) > 0 {
		ntf, err := DecodePersistedNotifyRecord(ev.NotifyBlob)
		if err != nil {
			return err
		}
		head, err := devicesec.ParseState(ntf.HeadState)
		if err != nil {
			return err
		}
		if err := ntf.Pin.Verify(head); err != nil {
			return err
		}
		if headHash, _ := head.Hash(); h.State != headHash {
			return devicesec.ErrStale
		}
		if _, err := ct.VerifyOrigin(head, head, false, 0); err != nil {
			return err
		}
		openedH, plain, err := devicesec.OpenCipherWithPurposeKey(ntf.PurposeKey[:], nil, ct)
		if err != nil {
			return err
		}
		e.finishNotifyPlain(ev, openedH, plain, false)
		return nil
	}
	if len(ev.RecordBlob) > 0 {
		rec, err := DecodePersistedRecord(ev.RecordBlob)
		if err != nil {
			return err
		}
		states, _, _, err := parseAndVerifyChain(rec.Pin, rec.States, rec.RecoveryProofs)
		if err != nil {
			return err
		}
		head := states[len(states)-1]
		if mem, ok := head.Core.Member(rec.MemberID); !rec.Member || !ok || mem.Kind != devicesec.Client {
			return ErrNotMember
		}
		if len(rec.Lease) == 0 {
			return ErrLeaseExpired
		}
		lease, err := devicesec.ParseLease(rec.Lease)
		if err != nil {
			return err
		}
		atNow := ev.NowMs
		if atNow <= 0 {
			atNow = e.lastNowMs
		}
		atNow = max(max(atNow, rec.FloorMs), 1)
		if err := lease.Valid(head, uint64(atNow)); err != nil {
			return err
		}
		byHash := make(map[devicesec.Hash]devicesec.State, len(states))
		for _, s := range states {
			sh, _ := s.Hash()
			byHash[sh] = s
		}
		srcState, ok := byHash[h.State]
		if !ok {
			return devicesec.ErrStale
		}
		if _, err := ct.VerifyOrigin(srcState, head, false, uint64(atNow)); err != nil {
			return err
		}
		enc, _, err := rec.Pin.GrantChallenge(head, rec.MemberID)
		if err != nil {
			return err
		}
		e.waitAgree(corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_X, "", enc, false, &openNotifyTask{ev: ev, h: h, ct: ct, rec: rec, head: head}, 0)
		return nil
	}
	return devicesec.ErrInvalid
}

// HandleOpenNotify verifies and decrypts a push notification envelope inside Go clientcore.
// Supports locked Android/iOS mode via persisted notify_blob AND Web Service Worker mode via record_blob + KeyAgree.
func (e *Engine) HandleOpenNotify(nowMs int64, ev *corev1.SecurityOpenNotifyEvent) []*corev1.Effect {
	e.touchNow(nowMs)
	if ev == nil {
		return nil
	}
	if err := e.handleOpenNotifyErr(ev); err != nil {
		e.emitNotifyError(ev.OpId, err)
	}
	return e.DrainEffects()
}
