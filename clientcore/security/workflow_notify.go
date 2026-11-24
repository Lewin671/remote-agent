package security

import (
	"strconv"
	"strings"
	"unicode/utf8"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

const (
	notifyDiskWarningBytes  uint64 = 10_000_000_000
	notifyDiskCriticalBytes uint64 = 2_000_000_000
	notifyMaxScheduledTitle        = 60
	notifyMaxScheduledText         = 200
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
	outcome := ValidateNotifyOutcome(ev, h, nc)
	outcome.VerifyRecordUnchanged = verifyUnchanged
	e.recordNotifyOutcome(ev.OpId, outcome)
	eff := &corev1.SecurityNotifyOpenedEffect{
		OpId:                  ev.OpId,
		VerifyRecordUnchanged: verifyUnchanged,
	}
	e.out = append(e.out, &corev1.Effect{
		Effect: &corev1.Effect_SecurityNotifyOpened{
			SecurityNotifyOpened: eff,
		},
	})
}

func invalidNotifyOutcome(kind v1.NotifyKind) NotifyOutcome {
	return NotifyOutcome{
		Valid: false,
		Kind:  kind,
	}
}

// ValidateNotifyOutcome validates decrypted push notification content against envelope
// headers and client expectations, returning a non-localized NotifyOutcome for screens
// to render into SecurityNotifyDisplay.
func ValidateNotifyOutcome(ev *corev1.SecurityOpenNotifyEvent, h devicesec.Header, nc *v1.NotifyContent) NotifyOutcome {
	if ev == nil || nc == nil {
		kind := v1.NotifyKind_NOTIFY_KIND_UNSPECIFIED
		if ev != nil && ev.ExpectedKind != v1.NotifyKind_NOTIFY_KIND_UNSPECIFIED {
			kind = ev.ExpectedKind
		} else if nc != nil {
			kind = nc.Kind
		}
		return invalidNotifyOutcome(kind)
	}
	expectedKind := ev.ExpectedKind
	if expectedKind == v1.NotifyKind_NOTIFY_KIND_UNSPECIFIED {
		return invalidNotifyOutcome(nc.Kind)
	}
	if nc.Kind != expectedKind {
		return invalidNotifyOutcome(expectedKind)
	}
	if nc.MachineId == "" || h.Machine == "" || nc.MachineId != h.Machine {
		return invalidNotifyOutcome(expectedKind)
	}
	if ev.MachineId != "" && ev.MachineId != nc.MachineId {
		return invalidNotifyOutcome(expectedKind)
	}

	switch expectedKind {
	case v1.NotifyKind_NOTIFY_KIND_MACHINE_DISK:
		disk := nc.DiskStatus
		if ev.MachineId == "" || ev.MachineId != nc.MachineId ||
			nc.SessionId != "" || h.Session != "" || ev.SessionId != "" ||
			strings.TrimSpace(nc.MachineName) == "" ||
			nc.Title != "" || nc.Text != "" || nc.Approval != nil || disk == nil {
			return invalidNotifyOutcome(expectedKind)
		}
		critical := disk.Pressure == v1.ResourcePressure_RESOURCE_PRESSURE_CRITICAL
		if !critical && disk.Pressure != v1.ResourcePressure_RESOURCE_PRESSURE_WARNING {
			return invalidNotifyOutcome(expectedKind)
		}
		limit := notifyDiskWarningBytes
		if critical {
			limit = notifyDiskCriticalBytes
		}
		if disk.AvailableBytes >= limit {
			return invalidNotifyOutcome(expectedKind)
		}
		expectedTag := nc.MachineId + "/NOTIFY_KIND_MACHINE_DISK"
		if ev.Tag == "" || ev.Tag != expectedTag {
			return invalidNotifyOutcome(expectedKind)
		}
		expectedReq := "disk:" + strconv.FormatInt(disk.ObservedMs, 10) + ":" + strconv.FormatInt(int64(disk.Pressure), 10)
		if h.Request == "" || h.Request != expectedReq {
			return invalidNotifyOutcome(expectedKind)
		}
		return NotifyOutcome{
			Valid:              true,
			Kind:               expectedKind,
			Tag:                expectedTag,
			MachineID:          nc.MachineId,
			MachineName:        nc.MachineName,
			DiskAvailableBytes: disk.AvailableBytes,
			DiskCritical:       critical,
		}

	case v1.NotifyKind_NOTIFY_KIND_SCHEDULED:
		if nc.Approval != nil || nc.SourceEventSeq == 0 ||
			nc.SessionId == "" || h.Session != nc.SessionId || ev.SessionId != nc.SessionId {
			return invalidNotifyOutcome(expectedKind)
		}
		expectedReq := "scheduled:" + strconv.FormatUint(nc.SourceEventSeq, 10)
		if h.Request == "" || h.Request != expectedReq {
			return invalidNotifyOutcome(expectedKind)
		}
		if ev.EventKey == "" || ev.EventKey != expectedReq {
			return invalidNotifyOutcome(expectedKind)
		}
		expectedTag := nc.SessionId + "/" + expectedReq + "/NOTIFY_KIND_SCHEDULED"
		if ev.Tag == "" || ev.Tag != expectedTag {
			return invalidNotifyOutcome(expectedKind)
		}
		if strings.TrimSpace(nc.Title) == "" || utf8.RuneCountInString(nc.Title) > notifyMaxScheduledTitle ||
			strings.TrimSpace(nc.Text) == "" || utf8.RuneCountInString(nc.Text) > notifyMaxScheduledText {
			return invalidNotifyOutcome(expectedKind)
		}
		return NotifyOutcome{
			Valid:          true,
			Kind:           expectedKind,
			Tag:            expectedTag,
			MachineID:      nc.MachineId,
			SessionID:      nc.SessionId,
			SourceEventSeq: nc.SourceEventSeq,
			Title:          nc.Title,
			Text:           nc.Text,
		}

	case v1.NotifyKind_NOTIFY_KIND_TURN_ENDED:
		if nc.Approval != nil || nc.SourceEventSeq == 0 ||
			nc.SessionId == "" || h.Session != nc.SessionId || ev.SessionId != nc.SessionId {
			return invalidNotifyOutcome(expectedKind)
		}
		expectedReq := "notify:" + strconv.FormatInt(int64(v1.NotifyKind_NOTIFY_KIND_TURN_ENDED), 10) + ":" + strconv.FormatUint(nc.SourceEventSeq, 10)
		if h.Request == "" || h.Request != expectedReq {
			return invalidNotifyOutcome(expectedKind)
		}
		expectedTag := nc.SessionId + "/NOTIFY_KIND_TURN_ENDED"
		if ev.Tag == "" || ev.Tag != expectedTag {
			return invalidNotifyOutcome(expectedKind)
		}
		out := NotifyOutcome{
			Valid:           true,
			Kind:            expectedKind,
			Tag:             expectedTag,
			MachineID:       nc.MachineId,
			SessionID:       nc.SessionId,
			SourceEventSeq:  nc.SourceEventSeq,
			Title:           nc.Title,
			Text:            nc.Text,
			SharedFileCount: int32(nc.SharedFileCount),
		}
		if ev.FallbackBody != nil {
			out.HasFallbackBody = true
			out.FallbackBody = *ev.FallbackBody
		}
		return out

	case v1.NotifyKind_NOTIFY_KIND_ATTENTION:
		if nc.SessionId == "" || h.Session != nc.SessionId || ev.SessionId != nc.SessionId {
			return invalidNotifyOutcome(expectedKind)
		}
		if h.Request == "" {
			return invalidNotifyOutcome(expectedKind)
		}
		if nc.Approval != nil && (nc.Approval.ApprovalId == "" || h.Request != nc.Approval.ApprovalId) {
			return invalidNotifyOutcome(expectedKind)
		}
		expectedTag := nc.SessionId + "/NOTIFY_KIND_ATTENTION"
		if ev.Tag == "" || ev.Tag != expectedTag {
			return invalidNotifyOutcome(expectedKind)
		}
		out := NotifyOutcome{
			Valid:          true,
			Kind:           expectedKind,
			Tag:            expectedTag,
			MachineID:      nc.MachineId,
			SessionID:      nc.SessionId,
			SourceEventSeq: nc.SourceEventSeq,
			Title:          nc.Title,
			Text:           nc.Text,
		}
		if a := nc.Approval; a != nil {
			out.HasApproval = true
			out.ApprovalID = a.ApprovalId
			out.ApprovalCommand = a.Command
			out.ApprovalToolName = a.ToolName
			out.ApprovalDescription = a.Description
			out.ApprovalToolInput = a.ToolInput
			out.ApprovalDeadlineMs = a.DeadlineMs
			out.ApprovalTruncated = a.Truncated
		}
		return out

	default:
		return invalidNotifyOutcome(expectedKind)
	}
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
