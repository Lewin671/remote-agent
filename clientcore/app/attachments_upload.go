package app

import (
	"slices"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/attachments"
	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	"github.com/Lewin671/remote-agent/clientcore/domain/world"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
	"github.com/Lewin671/remote-agent/clientcore/sync"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func uploadCompleteSlotKey(machineID, transferID string) string {
	if machineID == "" || transferID == "" {
		return ""
	}
	return "upload-complete:" + machineID + ":" + transferID
}

func (c *Core) abandonUploadComplete(ctx *sync.StepContext, machineID, transferID string, completing bool) {
	if ctx == nil || machineID == "" || transferID == "" {
		return
	}
	slotKey := uploadCompleteSlotKey(machineID, transferID)
	if completing || c.m.sync.HasFrozenAttempt(slotKey) {
		c.m.sync.AbandonAttempt(ctx, slotKey)
	}
}

func uploadTimerKey(ownerKind uint8, ownerKey, itemID string) string {
	prefix := "d"
	if ownerKind == attOwnerResend {
		prefix = "r"
	}
	return prefix + "\x00" + ownerKey + "\x00" + itemID
}

func parseUploadTimerKey(raw string) (ownerKind uint8, ownerKey, itemID string, ok bool) {
	p, rest, found := strings.Cut(raw, "\x00")
	if !found {
		return 0, "", "", false
	}
	k, id, found2 := strings.Cut(rest, "\x00")
	if !found2 || k == "" || id == "" {
		return 0, "", "", false
	}
	if p == "d" {
		return attOwnerDraft, k, id, true
	}
	if p == "r" {
		return attOwnerResend, k, id, true
	}
	return 0, "", "", false
}

func (c *Core) lookupUploadItem(ownerKind uint8, ownerKey, itemID string) (scope *corev1.DraftScope, machineID string, original bool, item *attItem, outboxReqID string) {
	if c.m.attachments == nil {
		return nil, "", false, nil, ""
	}
	mgr := c.m.attachments
	if ownerKind == attOwnerDraft {
		d := mgr.drafts[ownerKey]
		if d == nil {
			return nil, "", false, nil, ""
		}
		return d.scope, c.resolveDraftMachineID(d), d.original, d.findItem(itemID), ""
	}
	job := mgr.resendJobs[ownerKey]
	if job == nil || job.aborted {
		return nil, "", false, nil, ""
	}
	for _, it := range job.items {
		if it != nil && it.id == itemID {
			return &corev1.DraftScope{
				Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION,
				Id:   job.sessionID,
			}, job.machineID, job.original, it, job.newRequestID
		}
	}
	return nil, "", false, nil, ""
}

func (c *Core) markItemMissingLocal(it *attItem) {
	if it == nil {
		return
	}
	it.state = corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_FAILED
	it.phase = attPhaseIdle
	it.progress = 0
	it.progressPct = 0
	if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE {
		it.problem = attachments.BuildImageProblem(corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_SOURCE_MISSING)
		it.fileError = nil
	} else {
		it.fileError = text.Msg(text.KeyAttachmentFileMissing)
		it.problem = nil
	}
}

func (c *Core) invalidateItem(
	ctx *sync.StepContext,
	ownerKind uint8,
	ownerKey string,
	it *attItem,
	abortRemote bool,
	effects *[]*corev1.Effect,
) {
	if it == nil || c.m.attachments == nil {
		return
	}
	mgr := c.m.attachments
	oldMachine := it.targetMachineID
	if oldMachine == "" {
		oldMachine = it.uploadedMachineID
	}
	oldTransferID := it.transferID
	hadBegin := it.beginDone || it.phase == attPhaseBegin || it.state == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_READY

	// Synchronously bump generation and clear all remote/progress state first.
	it.generation++
	c.abandonUploadComplete(ctx, oldMachine, oldTransferID, it.phase == attPhaseCompleting)
	if it.activeReqID != "" {
		reqID := it.activeReqID
		it.activeReqID = ""
		delete(mgr.reqToUpload, reqID)
		if ctx != nil {
			c.m.sync.CancelCommand(ctx, reqID)
		}
	}
	if it.activeOpID != 0 {
		delete(mgr.readOpToMeta, it.activeOpID)
		it.activeOpID = 0
	}
	if ctx != nil {
		tk := uploadTimerKey(ownerKind, ownerKey, it.id)
		c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_POLL, tk)
		c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_RECONNECT, tk)
	}

	// Release file upload gate if this item currently holds it.
	if mgr.activeFileUpload != nil &&
		mgr.activeFileUpload.ownerKind == ownerKind &&
		mgr.activeFileUpload.ownerKey == ownerKey &&
		mgr.activeFileUpload.itemID == it.id {
		mgr.activeFileUpload = nil
	}

	it.state = corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PREPARING
	it.phase = attPhaseIdle
	it.transferID = ""
	it.uploadedMachineID = ""
	it.remoteID = ""
	it.expiresMs = 0
	it.persistedBytes = 0
	it.progress = 0
	it.progressPct = 0
	it.beginDone = false
	it.stepRetries = 0
	it.completePolls = 0
	it.statusProbeForComplete = false
	it.problem = nil
	it.fileError = nil

	if abortRemote && hadBegin && oldMachine != "" && oldTransferID != "" && !c.isTransferProtected(oldMachine, oldTransferID) {
		c.emitAbortUpload(ctx, oldMachine, oldTransferID)
	}
	_ = effects
}

func (c *Core) emitAbortUpload(ctx *sync.StepContext, machineID, transferID string) {
	if ctx == nil || machineID == "" || transferID == "" {
		return
	}
	// Best-effort fire-and-forget abort (untracked SlotKey=="").
	c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
		MachineID: machineID,
		Command: &v1.Command{
			ProtocolVersion: sync.PeerVersion,
			Kind: &v1.Command_AbortImageUpload{
				AbortImageUpload: &v1.AbortImageUploadRequest{
					TransferId: transferID,
				},
			},
		},
	})
}

func (c *Core) startPendingDraftUploads(ctx *sync.StepContext, d *attDraft, effects *[]*corev1.Effect) {
	if d == nil {
		return
	}
	machineID := c.resolveDraftMachineID(d)
	nowMs := c.m.sync.NowMs()
	for _, it := range d.items {
		if it == nil || it.remoteOnly {
			continue
		}
		if it.isReadyFor(machineID, d.original, nowMs) {
			continue
		}
		if it.state == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PAUSED ||
			it.state == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_FAILED {
			continue
		}
		if it.phase != attPhaseIdle {
			continue
		}
		c.startItemPipeline(ctx, attOwnerDraft, d.key, d.scope, machineID, d.original, it, effects)
	}
}

func (c *Core) startItemPipeline(
	ctx *sync.StepContext,
	ownerKind uint8,
	ownerKey string,
	scope *corev1.DraftScope,
	machineID string,
	original bool,
	it *attItem,
	effects *[]*corev1.Effect,
) {
	if it == nil || c.m.attachments == nil {
		return
	}
	if it.remoteOnly || !it.hasLocalCopy() {
		c.markItemMissingLocal(it)
		if ownerKind == attOwnerResend {
			c.failItem(ctx, ownerKind, ownerKey, it, it.problem, it.fileError, effects)
		}
		return
	}
	it.targetMachineID = machineID
	mgr := c.m.attachments

	// Validate capability for files up front if machine is known.
	if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_FILE && machineID != "" {
		caps, _ := c.resolveAttachmentCaps(machineID, false)
		if caps.CapabilitiesLoaded && !caps.SupportsFiles {
			c.failItem(ctx, ownerKind, ownerKey, it, nil, text.Msg(text.KeyAttachmentUpdateRequired), effects)
			return
		}
		if !c.uploadNetworkReady(machineID) && len(it.sha256) == 32 {
			// Offline before upload started: wait indefinitely until machine comes online (no 30s timeout!).
			c.parkItemWaitingOnline(ctx, ownerKind, ownerKey, machineID, it)
			return
		}
	}

	if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE {
		if !it.preparedValid(original) {
			if (original && !it.hasOriginalSource) || it.sourceRef == "" {
				c.markItemMissingLocal(it)
				if ownerKind == attOwnerResend {
					c.failItem(ctx, ownerKind, ownerKey, it, it.problem, it.fileError, effects)
				}
				return
			}
			it.state = corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PREPARING
			it.phase = attPhaseQueuedPrepare
			mgr.prepareQueue = append(mgr.prepareQueue, attPrepareRef{
				ownerKind:  ownerKind,
				ownerKey:   ownerKey,
				itemID:     it.id,
				generation: it.generation,
			})
			return
		}
		// Already prepared in current mode -> begin upload directly!
		c.enqueueOrStartUpload(ctx, ownerKind, ownerKey, machineID, it, effects)
		return
	}

	// File item: hash first if 32-byte sha256 not yet computed.
	if len(it.sha256) != 32 {
		it.state = corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PREPARING
		it.phase = attPhaseQueuedPrepare
		mgr.prepareQueue = append(mgr.prepareQueue, attPrepareRef{
			ownerKind:  ownerKind,
			ownerKey:   ownerKey,
			itemID:     it.id,
			generation: it.generation,
		})
		return
	}
	c.enqueueOrStartUpload(ctx, ownerKind, ownerKey, machineID, it, effects)
}

func (c *Core) enqueueOrStartUpload(
	ctx *sync.StepContext,
	ownerKind uint8,
	ownerKey string,
	machineID string,
	it *attItem,
	effects *[]*corev1.Effect,
) {
	if it == nil || c.m.attachments == nil {
		return
	}
	if machineID == "" {
		return
	}
	caps, _ := c.resolveAttachmentCaps(machineID, false)
	if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_FILE && caps.CapabilitiesLoaded && !caps.SupportsFiles {
		c.failItem(ctx, ownerKind, ownerKey, it, nil, text.Msg(text.KeyAttachmentUpdateRequired), effects)
		return
	}
	if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE && caps.CapabilitiesLoaded && !caps.SupportsImages {
		c.failItem(ctx, ownerKind, ownerKey, it, attachments.BuildImageProblem(corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_PROCESSING_FAILED), nil, effects)
		return
	}
	if !c.uploadNetworkReady(machineID) {
		c.parkItemWaitingOnline(ctx, ownerKind, ownerKey, machineID, it)
		return
	}

	it.targetMachineID = machineID
	if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_FILE {
		// Global serial upload gate for files (at most 1 file uploading across all drafts).
		mgr := c.m.attachments
		it.state = corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PREPARING
		it.phase = attPhaseQueuedUpload
		mgr.fileUploadQueue = append(mgr.fileUploadQueue, attPrepareRef{
			ownerKind:  ownerKind,
			ownerKey:   ownerKey,
			itemID:     it.id,
			generation: it.generation,
		})
		return
	}
	// Images upload concurrently.
	c.beginOrResumeItemUpload(ctx, ownerKind, ownerKey, it, effects)
}

func (c *Core) parkItemWaitingOnline(ctx *sync.StepContext, ownerKind uint8, ownerKey, machineID string, it *attItem) {
	if it == nil {
		return
	}
	it.targetMachineID = machineID
	it.state = corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_WAITING_RECONNECT
	// Only an already-started upload uses the 30s reconnect deadline; an upload that has not
	// started yet waits passively until the computer comes online with no failure timeout.
	if it.beginDone {
		it.phase = attPhaseWaitingReconnect
		nowMono := c.m.sync.MonoMs()
		it.reconnectDeadlineMonoMs = nowMono + attachments.UploadReconnectWindowMs
		it.reconnectNotBeforeMs = nowMono
		if ctx != nil {
			c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_RECONNECT, uploadTimerKey(ownerKind, ownerKey, it.id), attachments.UploadReconnectWindowMs)
		}
		return
	}
	it.phase = attPhaseWaitingOnline
	it.reconnectDeadlineMonoMs = 0
	it.reconnectNotBeforeMs = 0
}

func (c *Core) pumpAttachmentQueues(ctx *sync.StepContext, effects *[]*corev1.Effect) {
	if c.m.attachments == nil {
		return
	}
	mgr := c.m.attachments

	// 1. Pump global prepare/hash queue (concurrency = 1).
	for mgr.activePrepareOpID == 0 && len(mgr.prepareQueue) > 0 {
		ref := mgr.prepareQueue[0]
		mgr.prepareQueue = mgr.prepareQueue[1:]
		scope, machineID, original, it, outboxReqID := c.lookupUploadItem(ref.ownerKind, ref.ownerKey, ref.itemID)
		if it == nil || it.generation != ref.generation || it.phase != attPhaseQueuedPrepare {
			// Skip superseded item before invoking platform decoder/hasher!
			continue
		}
		opID := mgr.allocOpID()
		mgr.activePrepareOpID = opID
		mgr.activePrepareRef = ref
		mgr.prepareOpToMeta[opID] = ref
		it.activeOpID = opID
		it.phase = attPhasePreparing
		if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE {
			*effects = append(*effects, &corev1.Effect{
				Effect: &corev1.Effect_AttachmentPrepareImage{
					AttachmentPrepareImage: &corev1.AttachmentPrepareImageEffect{
						OpId:               opID,
						Scope:              cloneDraftScope(scope),
						OutboxRequestId:    outboxReqID,
						ItemId:             it.id,
						SourceRef:          it.sourceRef,
						Original:           original,
						MaxOriginalBytes:   attachments.OriginalImageMaxBytes,
						MaxDimensionPx:     attachments.CompressedImageMaxDimensionPx,
						MaxCompressedBytes: attachments.CompressedImageMaxBytes,
					},
				},
			})
		} else {
			caps, _ := c.resolveAttachmentCaps(machineID, false)
			*effects = append(*effects, &corev1.Effect{
				Effect: &corev1.Effect_AttachmentHashFile{
					AttachmentHashFile: &corev1.AttachmentHashFileEffect{
						OpId:            opID,
						Scope:           cloneDraftScope(scope),
						OutboxRequestId: outboxReqID,
						ItemId:          it.id,
						SourceRef:       it.sourceRef,
						MaxBytes:        caps.MaxFileBytes,
						IsVideo:         it.isVideo,
					},
				},
			})
		}
	}

	// 2. Pump global file upload serial gate (concurrency = 1).
	for mgr.activeFileUpload == nil && len(mgr.fileUploadQueue) > 0 {
		ref := mgr.fileUploadQueue[0]
		mgr.fileUploadQueue = mgr.fileUploadQueue[1:]
		_, machineID, _, it, _ := c.lookupUploadItem(ref.ownerKind, ref.ownerKey, ref.itemID)
		if it == nil || it.generation != ref.generation || it.phase != attPhaseQueuedUpload {
			continue
		}
		if machineID == "" {
			it.phase = attPhaseIdle
			continue
		}
		refCopy := ref
		mgr.activeFileUpload = &refCopy
		it.targetMachineID = machineID
		c.beginOrResumeItemUpload(ctx, ref.ownerKind, ref.ownerKey, it, effects)
	}
}

func (c *Core) releaseFileUploadGate(ownerKind uint8, ownerKey, itemID string) {
	if c.m.attachments == nil || c.m.attachments.activeFileUpload == nil {
		return
	}
	a := c.m.attachments.activeFileUpload
	if a.ownerKind == ownerKind && a.ownerKey == ownerKey && a.itemID == itemID {
		c.m.attachments.activeFileUpload = nil
	}
}

func (c *Core) handleAttachmentPrepareImageResult(ctx *sync.StepContext, ev *corev1.AttachmentPrepareImageResultEvent) []*corev1.Effect {
	if ev == nil || c.m.attachments == nil || ev.OpId == 0 {
		return nil
	}
	mgr := c.m.attachments
	meta, known := mgr.prepareOpToMeta[ev.OpId]
	delete(mgr.prepareOpToMeta, ev.OpId)
	if mgr.activePrepareOpID == ev.OpId {
		mgr.activePrepareOpID = 0
	}

	var effects []*corev1.Effect
	if !known {
		if ev.Ok {
			c.emitUnreferencedLocalReleases([]string{ev.PreparedRef, ev.ThumbRef}, &effects)
		}
		c.pumpAttachmentQueues(ctx, &effects)
		return effects
	}

	scope, machineID, original, it, _ := c.lookupUploadItem(meta.ownerKind, meta.ownerKey, meta.itemID)
	if it == nil || it.generation != meta.generation || it.activeOpID != ev.OpId {
		// Stale prepare result: release any orphan prepared/thumb blobs shell just wrote!
		if ev.Ok {
			c.emitUnreferencedLocalReleases([]string{ev.PreparedRef, ev.ThumbRef}, &effects)
		}
		c.pumpAttachmentQueues(ctx, &effects)
		return effects
	}
	it.activeOpID = 0
	it.phase = attPhaseIdle

	if !ev.Ok || len(ev.Sha256) != 32 || ev.PreparedRef == "" || ev.SizeBytes <= 0 {
		errKind := ev.ErrorKind
		if ev.Ok && errKind == corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_UNSPECIFIED {
			errKind = corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_DECODE
		}
		prob := attachments.ClassifyIOErrorToImageProblem(errKind, ev.ErrorCode, ev.ErrorDetail, ev.IsReadingSource)
		c.failItem(ctx, meta.ownerKind, meta.ownerKey, it, prob, nil, &effects)
		c.pumpAttachmentQueues(ctx, &effects)
		c.markDraftDirty(ctx, scope)
		return effects
	}

	it.preparedRef = ev.PreparedRef
	if ev.ThumbRef != "" {
		it.thumbRef = ev.ThumbRef
	}
	it.imageFormat = ev.Format
	it.width = ev.Width
	it.height = ev.Height
	it.sizeBytes = ev.SizeBytes
	it.sha256 = slices.Clone(ev.Sha256)
	it.preparedOriginal = original
	it.hasPrepared = true

	if meta.ownerKind == attOwnerDraft {
		if d := mgr.drafts[meta.ownerKey]; d != nil && d.loadState == draftLoadLoaded {
			c.enqueueDraftWrite(d, &effects)
		}
	}
	c.enqueueOrStartUpload(ctx, meta.ownerKind, meta.ownerKey, machineID, it, &effects)
	c.pumpAttachmentQueues(ctx, &effects)
	c.markDraftDirty(ctx, scope)
	return effects
}

func (c *Core) handleAttachmentHashFileResult(ctx *sync.StepContext, ev *corev1.AttachmentHashFileResultEvent) []*corev1.Effect {
	if ev == nil || c.m.attachments == nil || ev.OpId == 0 {
		return nil
	}
	mgr := c.m.attachments
	meta, known := mgr.prepareOpToMeta[ev.OpId]
	delete(mgr.prepareOpToMeta, ev.OpId)
	if mgr.activePrepareOpID == ev.OpId {
		mgr.activePrepareOpID = 0
	}

	var effects []*corev1.Effect
	if !known {
		c.pumpAttachmentQueues(ctx, &effects)
		return effects
	}

	scope, machineID, _, it, _ := c.lookupUploadItem(meta.ownerKind, meta.ownerKey, meta.itemID)
	if it == nil || it.generation != meta.generation || it.activeOpID != ev.OpId {
		c.pumpAttachmentQueues(ctx, &effects)
		return effects
	}
	it.activeOpID = 0
	it.phase = attPhaseIdle

	caps, _ := c.resolveAttachmentCaps(machineID, false)
	if !ev.Ok || len(ev.Sha256) != 32 || ev.SizeBytes < 0 {
		errKind := ev.ErrorKind
		if ev.Ok && errKind == corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_UNSPECIFIED {
			errKind = corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_OTHER
		}
		errText := attachments.ClassifyIOErrorToFileText(errKind, caps.MaxFileBytes, it.displayName)
		c.failItem(ctx, meta.ownerKind, meta.ownerKey, it, nil, errText, &effects)
		c.pumpAttachmentQueues(ctx, &effects)
		c.markDraftDirty(ctx, scope)
		return effects
	}
	if ev.SizeBytes > caps.MaxFileBytes {
		errText := attachments.ClassifyIOErrorToFileText(corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_FILE_TOO_LARGE, caps.MaxFileBytes, it.displayName)
		c.failItem(ctx, meta.ownerKind, meta.ownerKey, it, nil, errText, &effects)
		c.pumpAttachmentQueues(ctx, &effects)
		c.markDraftDirty(ctx, scope)
		return effects
	}

	it.sizeBytes = ev.SizeBytes
	it.sha256 = slices.Clone(ev.Sha256)
	if ev.UpdatedSourceRef != "" {
		it.sourceRef = ev.UpdatedSourceRef
	}
	if ev.HasDuration && ev.DurationMs > 0 {
		it.hasDuration = true
		it.durationMs = ev.DurationMs
	}
	if meta.ownerKind == attOwnerDraft {
		if d := mgr.drafts[meta.ownerKey]; d != nil && d.loadState == draftLoadLoaded {
			c.enqueueDraftWrite(d, &effects)
		}
	}
	c.enqueueOrStartUpload(ctx, meta.ownerKind, meta.ownerKey, machineID, it, &effects)
	c.pumpAttachmentQueues(ctx, &effects)
	c.markDraftDirty(ctx, scope)
	return effects
}

func (c *Core) beginOrResumeItemUpload(
	ctx *sync.StepContext,
	ownerKind uint8,
	ownerKey string,
	it *attItem,
	effects *[]*corev1.Effect,
) {
	if it == nil || ctx == nil || it.targetMachineID == "" {
		return
	}
	it.state = corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_UPLOADING
	if it.transferID != "" && it.beginDone {
		slotKey := uploadCompleteSlotKey(it.targetMachineID, it.transferID)
		frozen, expired, _ := c.m.sync.AttemptState(slotKey)
		if expired {
			c.m.sync.AbandonAttempt(ctx, slotKey)
		} else if frozen {
			// Replay the unknown CompleteImageUpload with its original RequestID.
			c.submitUploadComplete(ctx, ownerKind, ownerKey, it, effects)
			return
		}
		// Resume existing transfer via read-only GetImageUploadStatus!
		it.statusProbeForComplete = false
		c.submitUploadStatusProbe(ctx, ownerKind, ownerKey, it, effects)
		return
	}
	if it.transferID == "" {
		it.transferID = c.m.sync.NextID(ctx)
	}
	c.submitUploadBegin(ctx, ownerKind, ownerKey, it, effects)
}

func (c *Core) submitUploadBegin(
	ctx *sync.StepContext,
	ownerKind uint8,
	ownerKey string,
	it *attItem,
	effects *[]*corev1.Effect,
) {
	mgr := c.m.attachments
	var fileName string
	var imgFmt v1.ImageFormat
	if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_FILE {
		fileName = it.fileName
		if fileName == "" {
			fileName = it.displayName
		}
		imgFmt = v1.ImageFormat_IMAGE_FORMAT_UNSPECIFIED
	} else {
		imgFmt = it.imageFormat
	}
	cmd := &v1.Command{
		ProtocolVersion: sync.PeerVersion,
		Kind: &v1.Command_BeginImageUpload{
			BeginImageUpload: &v1.BeginImageUploadRequest{
				TransferId: it.transferID,
				Format:     imgFmt,
				TotalBytes: it.sizeBytes,
				Sha256:     slices.Clone(it.sha256),
				FileName:   fileName,
			},
		},
	}
	it.phase = attPhaseBegin
	reqID, dispatched := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
		MachineID: it.targetMachineID,
		Command:   cmd,
	})
	if !dispatched || reqID == "" {
		// Sync settled synchronously in ctx.Settled (or machine unavailable); record meta if reqID non-empty.
		if reqID != "" {
			it.activeReqID = reqID
			mgr.reqToUpload[reqID] = attActiveUploadMeta{
				ownerKind:  ownerKind,
				ownerKey:   ownerKey,
				itemID:     it.id,
				generation: it.generation,
				step:       attPhaseBegin,
				machineID:  it.targetMachineID,
				transferID: it.transferID,
			}
		}
		return
	}
	it.activeReqID = reqID
	mgr.reqToUpload[reqID] = attActiveUploadMeta{
		ownerKind:  ownerKind,
		ownerKey:   ownerKey,
		itemID:     it.id,
		generation: it.generation,
		step:       attPhaseBegin,
		machineID:  it.targetMachineID,
		transferID: it.transferID,
	}
	_ = effects
}

func (c *Core) submitUploadStatusProbe(
	ctx *sync.StepContext,
	ownerKind uint8,
	ownerKey string,
	it *attItem,
	effects *[]*corev1.Effect,
) {
	mgr := c.m.attachments
	cmd := &v1.Command{
		ProtocolVersion: sync.PeerVersion,
		Kind: &v1.Command_GetImageUploadStatus{
			GetImageUploadStatus: &v1.GetImageUploadStatusRequest{
				TransferId: it.transferID,
			},
		},
	}
	it.phase = attPhaseStatusProbe
	reqID, _ := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
		MachineID: it.targetMachineID,
		Command:   cmd,
	})
	if reqID != "" {
		it.activeReqID = reqID
		mgr.reqToUpload[reqID] = attActiveUploadMeta{
			ownerKind:  ownerKind,
			ownerKey:   ownerKey,
			itemID:     it.id,
			generation: it.generation,
			step:       attPhaseStatusProbe,
			machineID:  it.targetMachineID,
			transferID: it.transferID,
		}
	}
	_ = effects
}

func (c *Core) requestNextUploadChunk(
	ctx *sync.StepContext,
	ownerKind uint8,
	ownerKey string,
	it *attItem,
	effects *[]*corev1.Effect,
) {
	mgr := c.m.attachments
	if it.persistedBytes >= it.sizeBytes {
		c.submitUploadComplete(ctx, ownerKind, ownerKey, it, effects)
		return
	}
	chunkSize := it.chunkBytes
	if chunkSize <= 0 {
		chunkSize = attachments.ResolveUploadChunkBytes(mgr.maxChunkBytes, 0)
		it.chunkBytes = chunkSize
	}
	remaining := it.sizeBytes - it.persistedBytes
	length := int64(chunkSize)
	if remaining < length {
		length = remaining
	}
	scope, _, _, _, outboxReqID := c.lookupUploadItem(ownerKind, ownerKey, it.id)
	opID := mgr.allocOpID()
	ref := attPrepareRef{
		ownerKind:  ownerKind,
		ownerKey:   ownerKey,
		itemID:     it.id,
		generation: it.generation,
	}
	mgr.readOpToMeta[opID] = ref
	it.activeOpID = opID
	it.pendingChunkOff = it.persistedBytes
	it.pendingChunkLen = length
	it.phase = attPhaseReadingChunk
	*effects = append(*effects, &corev1.Effect{
		Effect: &corev1.Effect_AttachmentReadChunk{
			AttachmentReadChunk: &corev1.AttachmentReadChunkEffect{
				OpId:              opID,
				Scope:             cloneDraftScope(scope),
				OutboxRequestId:   outboxReqID,
				ItemId:            it.id,
				Kind:              it.kind,
				SourceRef:         it.sourceRef,
				PreparedRef:       it.preparedRef,
				Offset:            it.persistedBytes,
				Length:            int32(length),
				ExpectedSizeBytes: it.sizeBytes,
				ExpectedSha256:    slices.Clone(it.sha256),
			},
		},
	})
}

func (c *Core) handleAttachmentReadChunkResult(ctx *sync.StepContext, ev *corev1.AttachmentReadChunkResultEvent) []*corev1.Effect {
	if ev == nil || c.m.attachments == nil || ev.OpId == 0 {
		return nil
	}
	mgr := c.m.attachments
	meta, known := mgr.readOpToMeta[ev.OpId]
	delete(mgr.readOpToMeta, ev.OpId)
	if !known {
		return nil
	}
	var effects []*corev1.Effect
	scope, machineID, _, it, _ := c.lookupUploadItem(meta.ownerKind, meta.ownerKey, meta.itemID)
	if it == nil || it.generation != meta.generation || it.activeOpID != ev.OpId || it.phase != attPhaseReadingChunk {
		// Stale read result — drop silently and pump file queue in case gate freed.
		c.pumpAttachmentQueues(ctx, &effects)
		return effects
	}
	it.activeOpID = 0

	if !ev.Ok || ev.Offset != it.pendingChunkOff || int64(len(ev.Data)) != it.pendingChunkLen {
		errKind := ev.ErrorKind
		if ev.Ok && errKind == corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_UNSPECIFIED {
			errKind = corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_SOURCE_CHANGED
		}
		if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE {
			prob := attachments.ClassifyIOErrorToImageProblem(errKind, "", ev.ErrorDetail, true)
			c.failItem(ctx, meta.ownerKind, meta.ownerKey, it, prob, nil, &effects)
		} else {
			caps, _ := c.resolveAttachmentCaps(machineID, false)
			errText := attachments.ClassifyIOErrorToFileText(errKind, caps.MaxFileBytes, it.displayName)
			c.failItem(ctx, meta.ownerKind, meta.ownerKey, it, nil, errText, &effects)
		}
		c.pumpAttachmentQueues(ctx, &effects)
		c.markDraftDirty(ctx, scope)
		return effects
	}

	// Dispatch PutImageChunk over BULK route.
	cmd := &v1.Command{
		ProtocolVersion: sync.PeerVersion,
		Kind: &v1.Command_PutImageChunk{
			PutImageChunk: &v1.PutImageChunkRequest{
				TransferId: it.transferID,
				Offset:     ev.Offset,
				Data:       ev.Data,
			},
		},
	}
	it.phase = attPhasePuttingChunk
	reqID, _ := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
		MachineID:   it.targetMachineID,
		Command:     cmd,
		BulkFileKey: it.transferID,
		BulkBytes:   int64(len(ev.Data)),
		BulkUpload:  true,
	})
	if reqID != "" {
		it.activeReqID = reqID
		mgr.reqToUpload[reqID] = attActiveUploadMeta{
			ownerKind:  meta.ownerKind,
			ownerKey:   meta.ownerKey,
			itemID:     it.id,
			generation: it.generation,
			step:       attPhasePuttingChunk,
			machineID:  it.targetMachineID,
			transferID: it.transferID,
		}
	}
	return effects
}

func (c *Core) submitUploadComplete(
	ctx *sync.StepContext,
	ownerKind uint8,
	ownerKey string,
	it *attItem,
	effects *[]*corev1.Effect,
) {
	mgr := c.m.attachments
	cmd := &v1.Command{
		ProtocolVersion: sync.PeerVersion,
		Kind: &v1.Command_CompleteImageUpload{
			CompleteImageUpload: &v1.CompleteImageUploadRequest{
				TransferId: it.transferID,
			},
		},
	}
	it.phase = attPhaseCompleting
	reqID, _ := c.m.sync.SubmitCommand(ctx, sync.SubmitCommandParams{
		SlotKey:   uploadCompleteSlotKey(it.targetMachineID, it.transferID),
		MachineID: it.targetMachineID,
		Command:   cmd,
	})
	if reqID != "" {
		it.activeReqID = reqID
		mgr.reqToUpload[reqID] = attActiveUploadMeta{
			ownerKind:  ownerKind,
			ownerKey:   ownerKey,
			itemID:     it.id,
			generation: it.generation,
			step:       attPhaseCompleting,
			machineID:  it.targetMachineID,
			transferID: it.transferID,
		}
	}
	_ = effects
}

func (c *Core) settleAttachmentUpload(sc sync.SettledCommand, effects *[]*corev1.Effect, dirty *sync.DirtyFlags) bool {
	if c.m.attachments == nil || sc.RequestID == "" {
		return false
	}
	mgr := c.m.attachments
	meta, ok := mgr.reqToUpload[sc.RequestID]
	if !ok {
		return false
	}
	delete(mgr.reqToUpload, sc.RequestID)

	stepPrev, ctx := c.m.sync.BeginStep()
	var localEffects []*corev1.Effect

	scope, _, _, it, _ := c.lookupUploadItem(meta.ownerKind, meta.ownerKey, meta.itemID)
	if it == nil || it.generation != meta.generation || it.activeReqID != sc.RequestID {
		// Stale RPC outcome: if Begin or Complete succeeded on daemon for an unprotected superseded transfer, abort it!
		if sc.Category == outcome.Executed && sc.ErrorCode == "" &&
			(meta.step == attPhaseBegin || meta.step == attPhaseCompleting) &&
			meta.machineID != "" && meta.transferID != "" && !c.isTransferProtected(meta.machineID, meta.transferID) {
			c.emitAbortUpload(ctx, meta.machineID, meta.transferID)
		}
		c.pumpAttachmentQueues(ctx, &localEffects)
		stepRes := c.m.sync.EndStep(stepPrev, ctx)
		*effects = append(*effects, stepRes.Effects...)
		*effects = append(*effects, localEffects...)
		return true
	}
	it.activeReqID = ""

	if sc.Category == outcome.Executed && sc.ErrorCode == "" {
		c.handleUploadStepSuccess(ctx, meta, scope, it, sc.Result, &localEffects)
	} else {
		c.handleUploadStepFailure(ctx, meta, scope, it, sc, &localEffects)
	}

	c.pumpAttachmentQueues(ctx, &localEffects)
	stepRes := c.m.sync.EndStep(stepPrev, ctx)
	*effects = append(*effects, stepRes.Effects...)
	*effects = append(*effects, localEffects...)
	dirty.Add(stepRes.Dirty.Home, stepRes.Dirty.Project, stepRes.Dirty.NewSession)
	if stepRes.Dirty.SessionData {
		dirty.MarkSessionData()
	}
	for _, nested := range stepRes.Settled {
		if !c.settleAttachmentUpload(nested, effects, dirty) {
			c.processSessionSettled(nested, effects, dirty)
		}
	}
	return true
}

func (c *Core) handleUploadStepSuccess(
	ctx *sync.StepContext,
	meta attActiveUploadMeta,
	scope *corev1.DraftScope,
	it *attItem,
	res *v1.CommandResult,
	effects *[]*corev1.Effect,
) {
	mgr := c.m.attachments
	var st *v1.ImageUploadStatus
	switch meta.step {
	case attPhaseBegin:
		if res != nil && res.GetBeginImageUpload() != nil {
			st = res.GetBeginImageUpload().GetStatus()
		}
	case attPhaseStatusProbe:
		if res != nil && res.GetGetImageUploadStatus() != nil {
			st = res.GetGetImageUploadStatus().GetStatus()
		}
	case attPhasePuttingChunk:
		if res != nil && res.GetPutImageChunk() != nil {
			st = res.GetPutImageChunk().GetStatus()
		}
	case attPhaseCompleting:
		if res != nil && res.GetCompleteImageUpload() != nil {
			st = res.GetCompleteImageUpload().GetStatus()
		}
	}
	if st == nil {
		c.failItemWithText(ctx, meta.ownerKind, meta.ownerKey, it, attachments.UploadIncompleteText(), corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UPLOAD_FAILED, effects)
		c.markDraftDirty(ctx, scope)
		return
	}

	if st.MaxChunkBytes > 0 {
		it.chunkBytes = attachments.ResolveUploadChunkBytes(mgr.maxChunkBytes, st.MaxChunkBytes)
	} else if it.chunkBytes <= 0 {
		it.chunkBytes = attachments.ResolveUploadChunkBytes(mgr.maxChunkBytes, 0)
	}

	// Already COMPLETE or USED?
	if st.State == v1.ImageUploadState_IMAGE_UPLOAD_STATE_COMPLETE || st.State == v1.ImageUploadState_IMAGE_UPLOAD_STATE_USED {
		remoteID := st.ImageId
		if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_FILE {
			remoteID = st.FileId
		}
		if remoteID == "" {
			c.failItemWithText(ctx, meta.ownerKind, meta.ownerKey, it, attachments.UploadMissingIDText(), corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UPLOAD_FAILED, effects)
			c.markDraftDirty(ctx, scope)
			return
		}
		c.completeItemUpload(ctx, meta.ownerKind, meta.ownerKey, scope, it, remoteID, st.ExpiresMs, effects)
		return
	}

	// Validate persisted_bytes range & monotonic advance on Put.
	if meta.step == attPhasePuttingChunk {
		if !attachments.ValidatePutAdvance(it.pendingChunkOff, it.pendingChunkLen, st.PersistedBytes, it.sizeBytes) {
			errText := attachments.UploadMadeNoProgressText()
			if !attachments.ValidateUploadProgress(st.PersistedBytes, it.sizeBytes) {
				errText = attachments.UploadProgressInvalidText()
			}
			c.failItemWithText(ctx, meta.ownerKind, meta.ownerKey, it, errText, corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UPLOAD_FAILED, effects)
			c.markDraftDirty(ctx, scope)
			return
		}
	} else {
		if !attachments.ValidateUploadProgress(st.PersistedBytes, it.sizeBytes) {
			c.failItemWithText(ctx, meta.ownerKind, meta.ownerKey, it, attachments.UploadProgressInvalidText(), corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UPLOAD_FAILED, effects)
			c.markDraftDirty(ctx, scope)
			return
		}
	}

	wasBeginDone := it.beginDone
	if meta.step == attPhaseBegin {
		it.beginDone = true
	}

	// Advance persistedBytes & monotonic displayed progress; ONLY reset per-step retry budget
	// when persisted_bytes genuinely advances (or Begin transitions into the chunk/complete phase).
	prevPersisted := it.persistedBytes
	advanced := st.PersistedBytes > prevPersisted
	it.persistedBytes = st.PersistedBytes
	ratio, pct := attachments.UploadProgressRatio(it.persistedBytes, it.sizeBytes, false)
	if ratio > it.progress {
		it.progress = ratio
		it.progressPct = pct
	}
	if advanced || (meta.step == attPhaseBegin && !wasBeginDone) {
		it.stepRetries = 0
		it.retriedReconnect = false
	}

	// Handle RECEIVING after Complete (or StatusProbe following an uncertain Complete): poll with backoff!
	if (meta.step == attPhaseCompleting || (meta.step == attPhaseStatusProbe && it.statusProbeForComplete)) &&
		st.State == v1.ImageUploadState_IMAGE_UPLOAD_STATE_RECEIVING &&
		st.PersistedBytes == it.sizeBytes {
		if it.completePolls >= attachments.UploadCompleteMaxPolls {
			c.failItemWithText(ctx, meta.ownerKind, meta.ownerKey, it, attachments.UploadStillVerifyingText(), corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UPLOAD_FAILED, effects)
			c.markDraftDirty(ctx, scope)
			return
		}
		delay := attachments.UploadPollBackoffMs(it.completePolls)
		it.completePolls++
		it.phase = attPhasePollDelay
		c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_POLL, uploadTimerKey(meta.ownerKind, meta.ownerKey, it.id), delay)
		c.markDraftDirty(ctx, scope)
		return
	}

	if meta.step == attPhaseCompleting {
		// Non-COMPLETE, non-RECEIVING-full state after Complete.
		c.failItemWithText(ctx, meta.ownerKind, meta.ownerKey, it, attachments.UploadIncompleteText(), corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UPLOAD_FAILED, effects)
		c.markDraftDirty(ctx, scope)
		return
	}

	it.statusProbeForComplete = false
	if it.persistedBytes < it.sizeBytes {
		c.requestNextUploadChunk(ctx, meta.ownerKind, meta.ownerKey, it, effects)
	} else {
		c.submitUploadComplete(ctx, meta.ownerKind, meta.ownerKey, it, effects)
	}
	c.markDraftDirty(ctx, scope)
}

func (c *Core) handleUploadStepFailure(
	ctx *sync.StepContext,
	meta attActiveUploadMeta,
	scope *corev1.DraftScope,
	it *attItem,
	sc sync.SettledCommand,
	effects *[]*corev1.Effect,
) {
	// 1. Daemon deterministic conflict/expiry (not_found / already_exists / generation_conflict): 1x auto-restart with fresh transfer_id.
	if attachments.ShouldRestartUpload(sc.FromDaemon, sc.ErrorCode) && !it.restartedOnce {
		it.restartedOnce = true
		oldTransfer := it.transferID
		if oldTransfer != "" {
			c.abandonUploadComplete(ctx, it.targetMachineID, oldTransfer, meta.step == attPhaseCompleting)
			if !c.isTransferProtected(it.targetMachineID, oldTransfer) {
				c.emitAbortUpload(ctx, it.targetMachineID, oldTransfer)
			}
		}
		it.transferID = c.m.sync.NextID(ctx)
		it.beginDone = false
		it.persistedBytes = 0
		it.stepRetries = 0
		it.completePolls = 0
		it.statusProbeForComplete = false
		c.submitUploadBegin(ctx, meta.ownerKind, meta.ownerKey, it, effects)
		c.markDraftDirty(ctx, scope)
		return
	}

	// 2. Retryable transient/unknown error (never retry daemon business errors or conflicts).
	isRetryable := !sc.FromDaemon &&
		!sc.Expired &&
		sc.Category != outcome.Conflict &&
		(sc.Category == outcome.Unknown || attachments.IsUploadTransientCode(sc.ErrorCode))

	if isRetryable {
		// Use uploadNetworkReady (not resolveAttachmentCaps, which stays true during DisplayGrace
		// after a stream drop) so retries wait for real connectivity instead of burning synchronously.
		netReady := c.uploadNetworkReady(it.targetMachineID)
		if netReady && it.stepRetries < attachments.UploadStepMaxRetries {
			it.stepRetries++
			switch meta.step {
			case attPhaseBegin:
				c.submitUploadBegin(ctx, meta.ownerKind, meta.ownerKey, it, effects)
			case attPhasePuttingChunk:
				it.statusProbeForComplete = false
				c.submitUploadStatusProbe(ctx, meta.ownerKind, meta.ownerKey, it, effects)
			case attPhaseCompleting:
				slotKey := uploadCompleteSlotKey(it.targetMachineID, it.transferID)
				frozen, expired, _ := c.m.sync.AttemptState(slotKey)
				if frozen && !expired {
					c.submitUploadComplete(ctx, meta.ownerKind, meta.ownerKey, it, effects)
				} else {
					if expired {
						c.m.sync.AbandonAttempt(ctx, slotKey)
					}
					it.statusProbeForComplete = true
					c.submitUploadStatusProbe(ctx, meta.ownerKind, meta.ownerKey, it, effects)
				}
			case attPhaseStatusProbe:
				c.submitUploadStatusProbe(ctx, meta.ownerKind, meta.ownerKey, it, effects)
			}
			c.markDraftDirty(ctx, scope)
			return
		}
		// Enter 30s WAITING_RECONNECT once per step/disconnect (release file gate while waiting so other drafts aren't blocked).
		if !it.retriedReconnect {
			it.retriedReconnect = true
			it.stepRetries = 0
			c.releaseFileUploadGate(meta.ownerKind, meta.ownerKey, it.id)
			it.state = corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_WAITING_RECONNECT
			it.phase = attPhaseWaitingReconnect
			nowMono := c.m.sync.MonoMs()
			it.reconnectDeadlineMonoMs = nowMono + attachments.UploadReconnectWindowMs
			delay := attachments.UploadReconnectWindowMs
			if netReady {
				it.reconnectNotBeforeMs = nowMono + attachments.UploadOnlineJitterMs
				delay = attachments.UploadOnlineJitterMs
			} else {
				it.reconnectNotBeforeMs = nowMono
			}
			c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_RECONNECT, uploadTimerKey(meta.ownerKind, meta.ownerKey, it.id), delay)
			c.markDraftDirty(ctx, scope)
			return
		}
	}

	// 3. Permanent failure.
	if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE {
		rawDiag := ""
		if sc.ErrorReason.Kind == world.ReasonPlain {
			rawDiag = sc.ErrorReason.Param
		}
		prob := attachments.ClassifyImageProblem(&corev1.ImageProblemInput{
			ErrorCode:     sc.ErrorCode,
			RawDiagnostic: rawDiag,
		})
		c.failItem(ctx, meta.ownerKind, meta.ownerKey, it, prob, nil, effects)
	} else {
		errText := attachments.ClassifyDaemonFileUploadError(sc.FromDaemon, sc.ErrorCode, screens.ReasonText(sc.ErrorReason))
		c.failItem(ctx, meta.ownerKind, meta.ownerKey, it, nil, errText, effects)
	}
	c.markDraftDirty(ctx, scope)
}

func (c *Core) completeItemUpload(
	ctx *sync.StepContext,
	ownerKind uint8,
	ownerKey string,
	scope *corev1.DraftScope,
	it *attItem,
	remoteID string,
	expiresMs int64,
	effects *[]*corev1.Effect,
) {
	c.releaseFileUploadGate(ownerKind, ownerKey, it.id)
	it.state = corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_READY
	it.phase = attPhaseIdle
	it.remoteID = remoteID
	it.uploadedMachineID = it.targetMachineID
	it.expiresMs = expiresMs
	it.persistedBytes = it.sizeBytes
	it.progress = 1.0
	it.progressPct = 100
	it.stepRetries = 0
	it.completePolls = 0
	it.restartedOnce = false
	it.retriedReconnect = false
	it.problem = nil
	it.fileError = nil

	if ownerKind == attOwnerDraft {
		if d := c.m.attachments.drafts[ownerKey]; d != nil {
			c.scheduleDraftExpiryTimer(ctx, d)
			if d.loadState == draftLoadLoaded {
				c.enqueueDraftWrite(d, effects)
			}
		}
		c.markDraftDirty(ctx, scope)
		return
	}
	// Outbox resend job item completed!
	c.onResendItemFinished(ctx, ownerKey, effects)
}

func (c *Core) failItem(
	ctx *sync.StepContext,
	ownerKind uint8,
	ownerKey string,
	it *attItem,
	problem *corev1.ImageProblemView,
	fileErr *corev1.Text,
	effects *[]*corev1.Effect,
) {
	c.releaseFileUploadGate(ownerKind, ownerKey, it.id)
	c.abandonUploadComplete(ctx, it.targetMachineID, it.transferID, it.phase == attPhaseCompleting)
	if it.activeReqID != "" {
		delete(c.m.attachments.reqToUpload, it.activeReqID)
		if ctx != nil {
			c.m.sync.CancelCommand(ctx, it.activeReqID)
		}
		it.activeReqID = ""
	}
	if it.activeOpID != 0 {
		delete(c.m.attachments.readOpToMeta, it.activeOpID)
		it.activeOpID = 0
	}
	if ctx != nil {
		tk := uploadTimerKey(ownerKind, ownerKey, it.id)
		c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_POLL, tk)
		c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_RECONNECT, tk)
	}
	it.state = corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_FAILED
	it.phase = attPhaseIdle
	it.problem = problem
	it.fileError = fileErr
	if ownerKind == attOwnerResend {
		c.onResendItemFailed(ctx, ownerKey, problem, fileErr, effects)
	}
}

func (c *Core) failItemWithText(
	ctx *sync.StepContext,
	ownerKind uint8,
	ownerKey string,
	it *attItem,
	fileErr *corev1.Text,
	imgKind corev1.ImageProblemKind,
	effects *[]*corev1.Effect,
) {
	if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE {
		c.failItem(ctx, ownerKind, ownerKey, it, attachments.BuildImageProblem(imgKind), nil, effects)
	} else {
		c.failItem(ctx, ownerKind, ownerKey, it, nil, fileErr, effects)
	}
}

func (c *Core) handleAttachmentPauseUpload(ctx *sync.StepContext, act *corev1.AttachmentPauseUploadAction) []*corev1.Effect {
	if act == nil || act.ItemId == "" || c.m.attachments == nil {
		return nil
	}
	d := c.m.attachments.drafts[attachments.ScopeStorageKey(act.Scope)]
	if d == nil {
		return nil
	}
	it := d.findItem(act.ItemId)
	if it == nil {
		return nil
	}
	if it.state != corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_UPLOADING &&
		it.state != corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_WAITING_RECONNECT {
		return nil
	}
	var effects []*corev1.Effect
	it.pausedByStop = false
	c.pauseUploadItem(ctx, attOwnerDraft, d.key, it)
	c.pumpAttachmentQueues(ctx, &effects)
	c.markDraftDirty(ctx, d.scope)
	return effects
}

func (c *Core) pauseUploadItem(ctx *sync.StepContext, ownerKind uint8, ownerKey string, it *attItem) {
	if it == nil || c.m.attachments == nil {
		return
	}
	mgr := c.m.attachments
	// Bump generation so late RPCs/chunk reads are dropped, while preserving transferID & persistedBytes!
	it.generation++
	if it.activeReqID != "" {
		reqID := it.activeReqID
		it.activeReqID = ""
		delete(mgr.reqToUpload, reqID)
		if ctx != nil {
			if it.phase == attPhaseCompleting {
				c.abandonUploadComplete(ctx, it.targetMachineID, it.transferID, true)
			}
			c.m.sync.CancelCommand(ctx, reqID)
		}
	}
	if it.activeOpID != 0 {
		delete(mgr.readOpToMeta, it.activeOpID)
		if mgr.activePrepareOpID == it.activeOpID {
			mgr.activePrepareOpID = 0
		}
		it.activeOpID = 0
	}
	if ctx != nil {
		tk := uploadTimerKey(ownerKind, ownerKey, it.id)
		c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_POLL, tk)
		c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_RECONNECT, tk)
	}
	c.releaseFileUploadGate(ownerKind, ownerKey, it.id)
	it.state = corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PAUSED
	it.phase = attPhaseIdle
	it.stepRetries = 0
	it.completePolls = 0
}

func (c *Core) handleAttachmentResumeUpload(ctx *sync.StepContext, act *corev1.AttachmentResumeUploadAction) []*corev1.Effect {
	if act == nil || act.ItemId == "" || c.m.attachments == nil {
		return nil
	}
	d := c.m.attachments.drafts[attachments.ScopeStorageKey(act.Scope)]
	if d == nil {
		return nil
	}
	it := d.findItem(act.ItemId)
	if it == nil {
		return nil
	}
	if it.state != corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PAUSED &&
		it.state != corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_WAITING_RECONNECT {
		return nil
	}
	var effects []*corev1.Effect
	it.pausedByStop = false
	machineID := c.resolveDraftMachineID(d)
	if it.targetMachineID != "" && machineID != "" && it.targetMachineID != machineID {
		// Target machine changed while paused -> invalidate and start fresh on new machine.
		c.invalidateItem(ctx, attOwnerDraft, d.key, it, true, &effects)
	} else {
		tk := uploadTimerKey(attOwnerDraft, d.key, it.id)
		c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_RECONNECT, tk)
		it.retriedReconnect = false
		it.phase = attPhaseIdle
	}
	c.startItemPipeline(ctx, attOwnerDraft, d.key, d.scope, machineID, d.original, it, &effects)
	c.pumpAttachmentQueues(ctx, &effects)
	c.markDraftDirty(ctx, d.scope)
	return effects
}

func (c *Core) handleUploadPollTimer(ctx *sync.StepContext, timerKey string) []*corev1.Effect {
	ownerKind, ownerKey, itemID, ok := parseUploadTimerKey(timerKey)
	if !ok {
		return nil
	}
	scope, _, _, it, _ := c.lookupUploadItem(ownerKind, ownerKey, itemID)
	if it == nil || it.phase != attPhasePollDelay {
		return nil
	}
	var effects []*corev1.Effect
	c.submitUploadComplete(ctx, ownerKind, ownerKey, it, &effects)
	c.markDraftDirty(ctx, scope)
	return effects
}

func (c *Core) handleUploadReconnectTimer(ctx *sync.StepContext, timerKey string) []*corev1.Effect {
	ownerKind, ownerKey, itemID, ok := parseUploadTimerKey(timerKey)
	if !ok {
		return nil
	}
	scope, machineID, original, it, _ := c.lookupUploadItem(ownerKind, ownerKey, itemID)
	if it == nil || it.phase != attPhaseWaitingReconnect {
		return nil
	}
	var effects []*corev1.Effect
	nowMono := c.m.sync.MonoMs()
	if c.uploadNetworkReady(machineID) {
		it.phase = attPhaseIdle
		c.startItemPipeline(ctx, ownerKind, ownerKey, scope, machineID, original, it, &effects)
		c.pumpAttachmentQueues(ctx, &effects)
		c.markDraftDirty(ctx, scope)
		return effects
	}
	if nowMono < it.reconnectDeadlineMonoMs {
		c.m.sync.ScheduleAppTimer(ctx, corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_RECONNECT, timerKey, it.reconnectDeadlineMonoMs-nowMono)
		return nil
	}
	// 30s reconnect window expired -> fail item (keeping transferID/persistedBytes until user retries).
	c.failItemWithText(ctx, ownerKind, ownerKey, it, text.Msg(text.KeyAttachmentUploadFailed), corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UPLOAD_FAILED, &effects)
	c.pumpAttachmentQueues(ctx, &effects)
	c.markDraftDirty(ctx, scope)
	return effects
}

func isItemActiveForStop(it *attItem) bool {
	if it == nil {
		return false
	}
	if it.state == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_READY ||
		it.state == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_FAILED ||
		it.state == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PAUSED {
		return false
	}
	return it.phase != attPhaseIdle ||
		it.state == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_UPLOADING ||
		it.state == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_WAITING_RECONNECT ||
		it.state == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PREPARING
}

func (c *Core) pauseAllActiveUploadsOnStop(effects *[]*corev1.Effect) {
	if c.m.attachments == nil {
		return
	}
	mgr := c.m.attachments
	mgr.syncStopped = true
	stepPrev, ctx := c.m.sync.BeginStep()

	// Clear queued prepare/file slots; pausedByStop items will re-enqueue cleanly on START.
	mgr.activePrepareOpID = 0
	mgr.prepareQueue = nil
	mgr.activeFileUpload = nil
	mgr.fileUploadQueue = nil

	keys := make([]string, 0, len(mgr.drafts))
	for k := range mgr.drafts {
		keys = append(keys, k)
	}
	sortutil.Strings(keys)
	for _, k := range keys {
		d := mgr.drafts[k]
		if d == nil {
			continue
		}
		if d.expiryTimerTargetMs != 0 {
			d.expiryTimerTargetMs = 0
			c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_DRAFT_EXPIRY, d.key)
		}
		for _, it := range d.items {
			if isItemActiveForStop(it) {
				it.pausedByStop = true
				c.pauseUploadItem(ctx, attOwnerDraft, d.key, it)
				c.markDraftDirty(ctx, d.scope)
			}
		}
	}

	// Also pause all in-flight outbox resend jobs without aborting daemon transfers.
	resendKeys := make([]string, 0, len(mgr.resendJobs))
	for k := range mgr.resendJobs {
		resendKeys = append(resendKeys, k)
	}
	sortutil.Strings(resendKeys)
	for _, rk := range resendKeys {
		job := mgr.resendJobs[rk]
		if job == nil || job.aborted {
			continue
		}
		for _, it := range job.items {
			if isItemActiveForStop(it) {
				it.pausedByStop = true
				c.pauseUploadItem(ctx, attOwnerResend, rk, it)
			}
		}
	}

	stepRes := c.m.sync.EndStep(stepPrev, ctx)
	*effects = append(*effects, stepRes.Effects...)
}

func (c *Core) uploadNetworkReady(machineID string) bool {
	if c.m.state.World.Connection != lists.ConnConnected {
		return false
	}
	_, onlineReady := c.resolveAttachmentCaps(machineID, false)
	return onlineReady
}

func (c *Core) resumeStoppedUploadsOnStart(effects *[]*corev1.Effect) {
	if c.m.attachments == nil {
		return
	}
	mgr := c.m.attachments
	mgr.syncStopped = false
	stepPrev, ctx := c.m.sync.BeginStep()
	var localEffects []*corev1.Effect

	keys := make([]string, 0, len(mgr.drafts))
	for k := range mgr.drafts {
		keys = append(keys, k)
	}
	sortutil.Strings(keys)
	for _, k := range keys {
		d := mgr.drafts[k]
		if d == nil {
			continue
		}
		machineID := c.resolveDraftMachineID(d)
		netReady := c.uploadNetworkReady(machineID)
		resumed := false
		for _, it := range d.items {
			if it != nil && it.pausedByStop && netReady {
				it.pausedByStop = false
				it.retriedReconnect = false
				c.startItemPipeline(ctx, attOwnerDraft, d.key, d.scope, machineID, d.original, it, &localEffects)
				resumed = true
			}
		}
		if resumed {
			c.markDraftDirty(ctx, d.scope)
		}
		c.scheduleDraftExpiryTimer(ctx, d)
	}

	resendKeys := make([]string, 0, len(mgr.resendJobs))
	for k := range mgr.resendJobs {
		resendKeys = append(resendKeys, k)
	}
	sortutil.Strings(resendKeys)
	for _, rk := range resendKeys {
		job := mgr.resendJobs[rk]
		if job == nil || job.aborted {
			continue
		}
		if !c.uploadNetworkReady(job.machineID) {
			continue
		}
		scope := &corev1.DraftScope{Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION, Id: job.sessionID}
		for _, it := range job.items {
			if it != nil && it.pausedByStop {
				it.pausedByStop = false
				it.retriedReconnect = false
				c.startItemPipeline(ctx, attOwnerResend, rk, scope, job.machineID, job.original, it, &localEffects)
			}
		}
	}

	c.pumpAttachmentQueues(ctx, &localEffects)
	stepRes := c.m.sync.EndStep(stepPrev, ctx)
	*effects = append(*effects, stepRes.Effects...)
	*effects = append(*effects, localEffects...)
}

func (c *Core) refreshAllExpiredDraftsOnForeground(effects *[]*corev1.Effect) {
	if c.m.attachments == nil {
		return
	}
	mgr := c.m.attachments
	stepPrev, ctx := c.m.sync.BeginStep()
	var localEffects []*corev1.Effect
	keys := make([]string, 0, len(mgr.drafts))
	for k := range mgr.drafts {
		keys = append(keys, k)
	}
	sortutil.Strings(keys)
	for _, k := range keys {
		d := mgr.drafts[k]
		if d == nil {
			continue
		}
		// Force timer rescheduling on foreground in case the OS cleared timers while backgrounded.
		d.expiryTimerTargetMs = 0
		if c.refreshDraftExpiredItems(ctx, d, &localEffects) {
			c.markDraftDirty(ctx, d.scope)
		}
		c.scheduleDraftExpiryTimer(ctx, d)
	}
	c.pumpAttachmentQueues(ctx, &localEffects)
	stepRes := c.m.sync.EndStep(stepPrev, ctx)
	*effects = append(*effects, stepRes.Effects...)
	*effects = append(*effects, localEffects...)
}

func (c *Core) reconcileAttachments(effects *[]*corev1.Effect, dirty *sync.DirtyFlags) {
	if c.m.attachments == nil {
		return
	}
	mgr := c.m.attachments
	stepPrev, ctx := c.m.sync.BeginStep()
	var localEffects []*corev1.Effect
	nowMono := c.m.sync.MonoMs()

	if !mgr.syncStopped {
		// 1. Wake WAITING_RECONNECT and STOP-paused draft uploads & refresh expiry timers when machines come online.
		draftKeys := make([]string, 0, len(mgr.drafts))
		for k := range mgr.drafts {
			draftKeys = append(draftKeys, k)
		}
		sortutil.Strings(draftKeys)
		for _, k := range draftKeys {
			d := mgr.drafts[k]
			if d == nil {
				continue
			}
			machineID := c.resolveDraftMachineID(d)
			netReady := c.uploadNetworkReady(machineID)
			woke := false
			for _, it := range d.items {
				if it == nil {
					continue
				}
				if it.pausedByStop && netReady && d.activated {
					it.pausedByStop = false
					it.retriedReconnect = false
					c.startItemPipeline(ctx, attOwnerDraft, d.key, d.scope, machineID, d.original, it, &localEffects)
					woke = true
					continue
				}
				if it.phase == attPhaseWaitingOnline && netReady && d.activated {
					it.phase = attPhaseIdle
					c.startItemPipeline(ctx, attOwnerDraft, d.key, d.scope, machineID, d.original, it, &localEffects)
					woke = true
					continue
				}
				if it.phase == attPhaseWaitingReconnect && netReady && nowMono >= it.reconnectNotBeforeMs && nowMono <= it.reconnectDeadlineMonoMs {
					c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_RECONNECT, uploadTimerKey(attOwnerDraft, d.key, it.id))
					it.phase = attPhaseIdle
					c.startItemPipeline(ctx, attOwnerDraft, d.key, d.scope, machineID, d.original, it, &localEffects)
					woke = true
				}
			}
			if woke {
				c.markDraftDirty(ctx, d.scope)
			}
			c.scheduleDraftExpiryTimer(ctx, d)
		}

		// 2. Wake WAITING_RECONNECT and STOP-paused outbox resend uploads.
		resendKeys := make([]string, 0, len(mgr.resendJobs))
		for k := range mgr.resendJobs {
			resendKeys = append(resendKeys, k)
		}
		sortutil.Strings(resendKeys)
		for _, rk := range resendKeys {
			job := mgr.resendJobs[rk]
			if job == nil || job.aborted {
				continue
			}
			if !c.uploadNetworkReady(job.machineID) {
				continue
			}
			scope := &corev1.DraftScope{Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION, Id: job.sessionID}
			for _, it := range job.items {
				if it == nil {
					continue
				}
				if it.pausedByStop {
					it.pausedByStop = false
					it.retriedReconnect = false
					c.startItemPipeline(ctx, attOwnerResend, job.newRequestID, scope, job.machineID, job.original, it, &localEffects)
					continue
				}
				if it.phase == attPhaseWaitingOnline {
					it.phase = attPhaseIdle
					c.startItemPipeline(ctx, attOwnerResend, job.newRequestID, scope, job.machineID, job.original, it, &localEffects)
					continue
				}
				if it.phase == attPhaseWaitingReconnect && nowMono >= it.reconnectNotBeforeMs && nowMono <= it.reconnectDeadlineMonoMs {
					c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_RECONNECT, uploadTimerKey(attOwnerResend, job.newRequestID, it.id))
					it.phase = attPhaseIdle
					c.startItemPipeline(ctx, attOwnerResend, job.newRequestID, scope, job.machineID, job.original, it, &localEffects)
				}
			}
		}
	}

	// 3. Reconcile & prune settled outbox records against session events/projection.
	c.pruneSettledOutboxRecords(ctx, &localEffects)

	c.pumpAttachmentQueues(ctx, &localEffects)
	stepRes := c.m.sync.EndStep(stepPrev, ctx)
	*effects = append(*effects, stepRes.Effects...)
	*effects = append(*effects, localEffects...)
	dirty.Add(stepRes.Dirty.Home, stepRes.Dirty.Project, stepRes.Dirty.NewSession)
	if stepRes.Dirty.SessionData {
		dirty.MarkSessionData()
	}
	for _, nested := range stepRes.Settled {
		if !c.settleAttachmentUpload(nested, effects, dirty) {
			c.processSessionSettled(nested, effects, dirty)
		}
	}
}
