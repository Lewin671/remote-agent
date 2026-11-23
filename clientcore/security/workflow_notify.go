package security

import (
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
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
	display := buildNotifyDisplay(ev, h, nc)
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
				Display:               display,
			},
		},
	})
}

func buildNotifyDisplay(ev *corev1.SecurityOpenNotifyEvent, h devicesec.Header, nc *v1.NotifyContent) *corev1.SecurityNotifyDisplay {
	if nc == nil {
		return &corev1.SecurityNotifyDisplay{Valid: false}
	}
	kind := nc.Kind
	out := &corev1.SecurityNotifyDisplay{
		Kind:           kind,
		MachineId:      nc.MachineId,
		SessionId:      nc.SessionId,
		SourceEventSeq: nc.SourceEventSeq,
	}
	if ev != nil && ev.ExpectedKind != v1.NotifyKind_NOTIFY_KIND_UNSPECIFIED && kind != ev.ExpectedKind {
		return out
	}

	switch kind {
	case v1.NotifyKind_NOTIFY_KIND_MACHINE_DISK:
		out.GenericBody = text.Msg(text.KeyMachineStatusHomeWarning)
		disk := nc.DiskStatus
		if nc.SessionId != "" || h.Session != "" || nc.MachineId == "" ||
			strings.TrimSpace(nc.MachineName) == "" ||
			nc.Title != "" || nc.Text != "" || nc.Approval != nil || disk == nil {
			return out
		}
		if ev != nil && ev.MachineId != "" && nc.MachineId != ev.MachineId {
			return out
		}
		critical := disk.Pressure == v1.ResourcePressure_RESOURCE_PRESSURE_CRITICAL
		if !critical && disk.Pressure != v1.ResourcePressure_RESOURCE_PRESSURE_WARNING {
			return out
		}
		limit := notifyDiskWarningBytes
		if critical {
			limit = notifyDiskCriticalBytes
		}
		if disk.AvailableBytes >= limit {
			return out
		}
		expectedTag := nc.MachineId + "/NOTIFY_KIND_MACHINE_DISK"
		if ev != nil && ev.Tag != "" && ev.Tag != expectedTag {
			return out
		}
		expectedReq := "disk:" + strconv.FormatInt(disk.ObservedMs, 10) + ":" + strconv.FormatInt(int64(disk.Pressure), 10)
		if h.Request != "" && h.Request != expectedReq {
			return out
		}
		gbStr := strconv.FormatFloat(float64(disk.AvailableBytes)/1e9, 'f', 1, 64)
		sizeTxt := text.Msg(text.KeyMachineStatusSizeGb, text.Str("size", gbStr))
		out.Valid = true
		out.Tag = expectedTag
		out.Critical = critical
		out.ShowDetailedByDefault = true
		if critical {
			out.Title = text.Msg(text.KeyMachineStatusNotifyCritical, text.Str("computer", nc.MachineName), text.ArgText("size", sizeTxt))
			out.Body = text.Msg(text.KeyMachineStatusNotifyCriticalBody)
		} else {
			out.Title = text.Msg(text.KeyMachineStatusNotifyWarning, text.Str("computer", nc.MachineName), text.ArgText("size", sizeTxt))
			out.Body = text.Msg(text.KeyMachineStatusNotifyWarningBody)
		}
		return out

	case v1.NotifyKind_NOTIFY_KIND_SCHEDULED:
		out.GenericBody = text.Msg(text.KeyNotificationsScheduledTasksHaveNewResults)
		if nc.Approval != nil || nc.SourceEventSeq == 0 || nc.SessionId == "" || (h.Session != "" && h.Session != nc.SessionId) {
			return out
		}
		expectedReq := "scheduled:" + strconv.FormatUint(nc.SourceEventSeq, 10)
		if h.Request != "" && h.Request != expectedReq {
			return out
		}
		if ev != nil && ev.EventKey != "" && ev.EventKey != expectedReq {
			return out
		}
		expectedTag := nc.SessionId + "/" + expectedReq + "/NOTIFY_KIND_SCHEDULED"
		if ev != nil && ev.Tag != "" && ev.Tag != expectedTag {
			return out
		}
		if strings.TrimSpace(nc.Title) == "" || utf8.RuneCountInString(nc.Title) > notifyMaxScheduledTitle ||
			strings.TrimSpace(nc.Text) == "" || utf8.RuneCountInString(nc.Text) > notifyMaxScheduledText {
			return out
		}
		out.Valid = true
		out.Tag = expectedTag
		out.ShowDetailedByDefault = true
		out.Title = text.Plain(nc.Title)
		out.Body = text.Plain(nc.Text)
		return out

	case v1.NotifyKind_NOTIFY_KIND_TURN_ENDED:
		out.GenericBody = text.Msg(text.KeyNotificationsAndroidAConversationTurnFinished)
		if nc.Approval != nil || nc.SourceEventSeq == 0 || nc.SessionId == "" || (h.Session != "" && h.Session != nc.SessionId) {
			return out
		}
		expectedReq := "notify:" + strconv.FormatInt(int64(v1.NotifyKind_NOTIFY_KIND_TURN_ENDED), 10) + ":" + strconv.FormatUint(nc.SourceEventSeq, 10)
		if h.Request != "" && h.Request != expectedReq {
			return out
		}
		expectedTag := nc.SessionId + "/NOTIFY_KIND_TURN_ENDED"
		if ev != nil && ev.Tag != "" && ev.Tag != expectedTag {
			return out
		}
		out.Valid = true
		out.Tag = expectedTag
		if nc.Title != "" {
			out.Title = text.Plain(nc.Title)
		} else {
			out.Title = text.Plain("remote-agent")
		}
		out.SharedFileCount = int32(nc.SharedFileCount)
		if nc.SharedFileCount > 0 {
			suffix := text.Msg(text.KeySharedFileNotificationFiles, text.Int("count", int64(nc.SharedFileCount)))
			out.SharedFilesSuffix = suffix
			if nc.Text != "" {
				out.Body = text.JoinLiteral(" ", text.Plain(nc.Text), suffix)
			} else {
				out.Body = suffix
			}
		} else if nc.Text != "" {
			out.Body = text.Plain(nc.Text)
		} else {
			out.Body = out.GenericBody
		}
		return out

	default:
		out.GenericBody = text.Msg(text.KeyNotificationsAndroidASessionNeedsYourAttention)
		if nc.SessionId != "" && h.Session != "" && h.Session != nc.SessionId {
			return out
		}
		out.Valid = true
		if nc.SessionId != "" {
			out.Tag = nc.SessionId + "/NOTIFY_KIND_ATTENTION"
		}
		if a := nc.Approval; a != nil {
			out.ApprovalId = a.ApprovalId
			out.DeadlineMs = a.DeadlineMs
			out.Truncated = a.Truncated
			var bodyStr string
			if a.Command != "" {
				out.Title = text.Msg(text.KeyListStateAndroidRequestCommand)
				bodyStr = strings.TrimSpace(a.Command)
			} else {
				tool := strings.TrimSpace(a.ToolName)
				if tool != "" {
					out.Title = text.JoinLiteral(" · ", text.Msg(text.KeyListStateAndroidRequestPermission), text.Plain(tool))
				} else {
					out.Title = text.Msg(text.KeyListStateAndroidRequestPermission)
				}
				switch {
				case a.Description != "":
					bodyStr = strings.TrimSpace(a.Description)
				case a.ToolInput != "":
					bodyStr = strings.TrimSpace(a.ToolInput)
				default:
					bodyStr = tool
				}
			}
			out.Body = text.Plain(bodyStr)
			if nc.Title != "" {
				out.SubText = text.Plain(nc.Title)
			}
			descTrim := strings.TrimSpace(a.Description)
			if descTrim != "" && descTrim != bodyStr && bodyStr != "" {
				out.BigText = text.Plain(descTrim + "\n" + bodyStr)
			} else if descTrim != "" {
				out.BigText = text.Plain(descTrim)
			} else if bodyStr != "" {
				out.BigText = text.Plain(bodyStr)
			}
		} else {
			if nc.Title != "" {
				out.Title = text.Plain(nc.Title)
			} else {
				out.Title = text.Plain("remote-agent")
			}
			if nc.Text != "" {
				out.Body = text.Plain(nc.Text)
			} else {
				out.Body = out.GenericBody
			}
		}
		return out
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
