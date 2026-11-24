package app

import (
	"slices"
	"strings"

	"github.com/Lewin671/remote-agent/clientcore/domain/attachments"
	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/screens"
	"github.com/Lewin671/remote-agent/clientcore/sync"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func (c *Core) protectTransfer(machineID, transferID, owner string) {
	if machineID == "" || transferID == "" || owner == "" || c.m.attachments == nil {
		return
	}
	ref := attTransferRef{machineID: machineID, transferID: transferID}
	owners := c.m.attachments.protectedTransfers[ref]
	if owners == nil {
		owners = make(map[string]bool)
		c.m.attachments.protectedTransfers[ref] = owners
	}
	owners[owner] = true
}

func (c *Core) releaseTransferProtection(machineID, transferID, owner string) {
	if machineID == "" || transferID == "" || owner == "" || c.m.attachments == nil {
		return
	}
	ref := attTransferRef{machineID: machineID, transferID: transferID}
	owners := c.m.attachments.protectedTransfers[ref]
	if owners == nil {
		return
	}
	delete(owners, owner)
	if len(owners) == 0 {
		delete(c.m.attachments.protectedTransfers, ref)
	}
}

func (c *Core) isTransferProtected(machineID, transferID string) bool {
	if machineID == "" || transferID == "" || c.m.attachments == nil {
		return false
	}
	return len(c.m.attachments.protectedTransfers[attTransferRef{machineID: machineID, transferID: transferID}]) > 0
}

func (c *Core) handleDraftSendSession(ctx *sync.StepContext, act *corev1.DraftSendSessionAction) []*corev1.Effect {
	if act == nil || act.SessionId == "" {
		return nil
	}
	sid := act.SessionId
	mgr := c.m.ensureAttachments()
	if mgr.forgottenSessions[sid] {
		return nil
	}
	scope := &corev1.DraftScope{
		Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION,
		Id:   sid,
	}
	d := c.ensureDraftState(scope)
	if d.sending || d.loadState == draftLoadLoading {
		return nil
	}
	if rImg, rFile := d.reservedCounts(); rImg > 0 || rFile > 0 {
		return nil
	}

	var effects []*corev1.Effect
	machineID := c.resolveDraftMachineID(d)
	caps, onlineReady := c.resolveAttachmentCaps(machineID, false)
	if !onlineReady || machineID == "" {
		return nil
	}

	// Refresh any expired attachments right before sending (blocks send and re-uploads).
	if c.refreshDraftExpiredItems(ctx, d, &effects) {
		c.pumpAttachmentQueues(ctx, &effects)
		c.markDraftDirty(ctx, d.scope)
		return effects
	}

	trimmed := strings.TrimSpace(d.text)
	if trimmed == "" && len(d.items) == 0 {
		return nil
	}

	nowMs := c.m.sync.NowMs()
	var (
		imageIDs []string
		fileIDs  []string
		itemIDs  []string
		snaps    []*attItem
	)
	for _, it := range d.items {
		if it == nil {
			continue
		}
		if !it.isReadyFor(machineID, d.original, nowMs) {
			// Still preparing/uploading/paused/failed -> refuse send.
			c.markDraftDirty(ctx, d.scope)
			return effects
		}
		itemIDs = append(itemIDs, it.id)
		snaps = append(snaps, it.cloneForSnapshot())
		if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE {
			imageIDs = append(imageIDs, it.remoteID)
		} else {
			fileIDs = append(fileIDs, it.remoteID)
		}
	}

	// Per docs/session-files.md: MUST verify file_upload capability before sending file_ids!
	if len(fileIDs) > 0 && !caps.SupportsFiles {
		d.notice = caps.FileUploadBlockedReason
		if d.notice == nil {
			d.notice = text.Msg(text.KeyAttachmentUpdateRequired)
		}
		c.markDraftDirty(ctx, d.scope)
		return effects
	}
	if len(imageIDs) > 0 && !caps.SupportsImages {
		d.notice = caps.ImageUploadBlockedReason
		c.markDraftDirty(ctx, d.scope)
		return effects
	}

	mode := act.Mode
	if mode == v1.SendMode_SEND_MODE_UNSPECIFIED {
		mode = v1.SendMode_SEND_MODE_STEER
	}
	reqID := c.m.sync.NextID(ctx)
	sentText := d.text

	// Text-only send: submit immediately and clear draft text.
	if len(snaps) == 0 {
		c.submitSessionSendFromDraft(ctx, sid, machineID, reqID, sentText, mode, nil, nil, nil)
		if strings.TrimSpace(d.text) == trimmed {
			d.text = ""
			d.textRev++
		}
		if d.loadState == draftLoadLoaded {
			c.enqueueDraftWrite(d, &effects)
		}
		c.markDraftDirty(ctx, d.scope)
		return effects
	}

	pending := &attPendingSend{
		sessionID: sid,
		machineID: machineID,
		requestID: reqID,
		text:      sentText,
		mode:      mode,
		original:  d.original,
		itemIDs:   itemIDs,
		imageIDs:  imageIDs,
		fileIDs:   fileIDs,
		items:     snaps,
		outboxKey: attachments.OutboxStorageKey(sid, reqID),
	}

	// Persist Outbox before detaching draft and submitting SendRequest when persist_outbox is enabled.
	if mgr.persistOutbox {
		d.sending = true
		d.pendingSend = pending
		c.emitOutboxWrite(pending.sessionID, pending.machineID, pending.requestID, pending.text, pending.mode, pending.original, pending.imageIDs, pending.fileIDs, pending.items, attWriteKindPreSendOutbox, d.key, &pending.writeOpID, &effects)
		c.markDraftDirty(ctx, d.scope)
		return effects
	}

	// In-memory outbox (persist_outbox == false): commit immediately.
	c.commitSessionSend(ctx, d, pending, false, &effects)
	c.markDraftDirty(ctx, d.scope)
	return effects
}

func (c *Core) emitOutboxWrite(
	sid, machineID, reqID, msgText string,
	mode v1.SendMode,
	original bool,
	imageIDs, fileIDs []string,
	items []*attItem,
	kind attWriteOpKind,
	draftKey string,
	opIDOut *uint64,
	effects *[]*corev1.Effect,
) {
	mgr := c.m.ensureAttachments()
	rec := &corev1.PersistedOutboxRecord{
		SchemaVersion: attachments.PersistedSchemaVersion,
		SessionId:     sid,
		RequestId:     reqID,
		MachineId:     machineID,
		Text:          msgText,
		Mode:          mode,
		Original:      original,
		ImageIds:      slices.Clone(imageIDs),
		FileIds:       slices.Clone(fileIDs),
		CreatedMs:     c.m.sync.NowMs(),
	}
	var refs []string
	for _, it := range items {
		if it == nil {
			continue
		}
		rec.Items = append(rec.Items, it.toPersisted())
		refs = append(refs, it.localRefs()...)
	}
	raw, err := attachments.EncodePersistedOutbox(rec)
	if err != nil {
		return
	}
	outboxKey := attachments.OutboxStorageKey(sid, reqID)
	opID := mgr.allocOpID()
	*opIDOut = opID
	mgr.draftWriteOps[opID] = attWriteOpMeta{
		kind:      kind,
		draftKey:  draftKey,
		sessionID: sid,
		requestID: reqID,
		outboxKey: outboxKey,
	}
	*effects = append(*effects, &corev1.Effect{
		Effect: &corev1.Effect_DraftStorageWrite{
			DraftStorageWrite: &corev1.DraftStorageWriteEffect{
				OpId:           opID,
				Bucket:         corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX,
				Key:            outboxKey,
				Data:           raw,
				ReferencedRefs: refs,
			},
		},
	})
}

func (c *Core) onPreSendOutboxWriteResult(
	ctx *sync.StepContext,
	meta attWriteOpMeta,
	ev *corev1.DraftStorageWriteResultEvent,
	effects *[]*corev1.Effect,
) {
	mgr := c.m.attachments
	d := mgr.drafts[meta.draftKey]
	if d == nil || d.pendingSend == nil || d.pendingSend.writeOpID != ev.OpId || mgr.forgottenSessions[meta.sessionID] {
		// Stale outbox write completed after session forgotten / cleared: delete orphan disk record!
		if ev.Ok {
			*effects = append(*effects, makeDraftStorageDeleteEffect(corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX, meta.outboxKey))
		}
		return
	}
	pending := d.pendingSend
	d.sending = false
	d.pendingSend = nil

	if !ev.Ok {
		d.storageError = attachments.ClassifyStorageErrorText(ev.ErrorKind, false)
		c.markDraftDirty(ctx, d.scope)
		return
	}

	// Re-check capability, online state, and item readiness/expiry AFTER async outbox write!
	caps, onlineReady := c.resolveAttachmentCaps(pending.machineID, false)
	nowMs := c.m.sync.NowMs()
	recheckOK := onlineReady &&
		(len(pending.fileIDs) == 0 || caps.SupportsFiles) &&
		(len(pending.imageIDs) == 0 || caps.SupportsImages)
	if recheckOK {
		for _, id := range pending.itemIDs {
			it := d.findItem(id)
			if it == nil || !it.isReadyFor(pending.machineID, pending.original, nowMs) {
				recheckOK = false
				break
			}
		}
	}
	if !recheckOK {
		// Abort send, delete persisted outbox record, refresh expired items if needed.
		*effects = append(*effects, makeDraftStorageDeleteEffect(corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX, pending.outboxKey))
		if len(pending.fileIDs) > 0 && !caps.SupportsFiles {
			d.notice = text.Msg(text.KeyAttachmentUpdateRequired)
		}
		c.refreshDraftExpiredItems(ctx, d, effects)
		c.pumpAttachmentQueues(ctx, effects)
		c.markDraftDirty(ctx, d.scope)
		return
	}

	c.commitSessionSend(ctx, d, pending, true, effects)
	c.markDraftDirty(ctx, d.scope)
}

func (c *Core) commitSessionSend(
	ctx *sync.StepContext,
	d *attDraft,
	pending *attPendingSend,
	persisted bool,
	effects *[]*corev1.Effect,
) {
	mgr := c.m.attachments
	// 1. Register Outbox record & protect daemon transfers so draft detach NEVER aborts them.
	outRec := &attOutboxRecord{
		sessionID:  pending.sessionID,
		requestID:  pending.requestID,
		machineID:  pending.machineID,
		text:       pending.text,
		mode:       pending.mode,
		original:   pending.original,
		imageIDs:   slices.Clone(pending.imageIDs),
		fileIDs:    slices.Clone(pending.fileIDs),
		items:      pending.items,
		createdMs:  c.m.sync.NowMs(),
		storageKey: pending.outboxKey,
		persisted:  persisted,
	}
	mgr.outbox[outRec.requestID] = outRec
	mgr.outboxBySession[outRec.sessionID] = append(mgr.outboxBySession[outRec.sessionID], outRec.requestID)
	ownerTag := "outbox:" + outRec.requestID
	for _, it := range outRec.items {
		if it != nil {
			c.protectTransfer(outRec.machineID, it.transferID, ownerTag)
		}
	}

	// 2. Detach ONLY the snapshotted items from draft (preserving anything added while persisting!),
	// WITHOUT aborting transfers.
	takenSet := make(map[string]bool, len(pending.itemIDs))
	for _, id := range pending.itemIDs {
		takenSet[id] = true
	}
	keptItems := d.items[:0]
	for _, it := range d.items {
		if it == nil {
			continue
		}
		if takenSet[it.id] {
			// Invalidate generation so any stray callbacks drop, without aborting daemon transfer!
			c.invalidateItem(ctx, attOwnerDraft, d.key, it, false, effects)
			continue
		}
		keptItems = append(keptItems, it)
	}
	d.items = keptItems
	if imgs, _ := d.itemCounts(); imgs == 0 {
		d.original = false
		d.viewerOpen = false
		d.viewerIndex = 0
	}
	// Race-safe text clear: only clear if user didn't already type a new message while persisting.
	if strings.TrimSpace(d.text) == strings.TrimSpace(pending.text) {
		d.text = ""
		d.textRev++
	}
	c.scheduleDraftExpiryTimer(ctx, d)
	if d.loadState == draftLoadLoaded {
		c.enqueueDraftWrite(d, effects)
	}

	// 3. Submit SendRequest via unified Session store & Outgoing!
	summaries := makeOutboxSummaries(outRec.items)
	c.submitSessionSendFromDraft(ctx, outRec.sessionID, outRec.machineID, outRec.requestID, outRec.text, outRec.mode, outRec.imageIDs, outRec.fileIDs, summaries)
}

func makeOutboxSummaries(items []*attItem) []*corev1.OutboxAttachmentSummary {
	if len(items) == 0 {
		return nil
	}
	out := make([]*corev1.OutboxAttachmentSummary, 0, len(items))
	for _, it := range items {
		if it != nil {
			out = append(out, it.toOutboxSummary())
		}
	}
	return out
}

func (c *Core) submitSessionSendFromDraft(
	ctx *sync.StepContext,
	sid, machineID, reqID, msgText string,
	mode v1.SendMode,
	imageIDs, fileIDs []string,
	summaries []*corev1.OutboxAttachmentSummary,
) {
	store := c.m.ensureSessionStore()
	ui := store.ensureUI(sid)
	snap := c.m.sync.SessionSnapshot(sid)
	reconcileSessionSnapshot(snap, ui)
	cmd := &v1.Command{
		ProtocolVersion: sync.PeerVersion,
		Kind: &v1.Command_Send{
			Send: &v1.SendRequest{
				SessionId: sid,
				RequestId: reqID,
				Text:      msgText,
				Mode:      mode,
				ImageIds:  slices.Clone(imageIDs),
				FileIds:   slices.Clone(fileIDs),
			},
		},
	}
	fillObservedPreconditions(cmd, snap, ui)
	slotKey := canonicalSlotKey(cmd, sid, 0)
	c.submitSessionCommandDirect(ctx, store, ui, sid, machineID, slotKey, slotKindSend, "", 0, reqID, cmd)
	if len(summaries) > 0 {
		if lc := ui.LocalContents[reqID]; lc != nil {
			lc.LocalAttachments = summaries
		}
		for _, og := range ui.Outgoing {
			if og != nil && og.RequestID == reqID {
				og.LocalAttachments = summaries
				og.Resending = false
				break
			}
		}
	}
}

func (c *Core) restoreOutboxRecordFromPersisted(rec *corev1.PersistedOutboxRecord, storageKey string) {
	mgr := c.m.ensureAttachments()
	if _, exists := mgr.outbox[rec.RequestId]; exists {
		return
	}
	items := make([]*attItem, 0, len(rec.Items))
	for _, pi := range rec.Items {
		if pi != nil {
			items = append(items, attItemFromPersisted(pi))
		}
	}
	outRec := &attOutboxRecord{
		sessionID:         rec.SessionId,
		requestID:         rec.RequestId,
		machineID:         rec.MachineId,
		text:              rec.Text,
		mode:              rec.Mode,
		original:          rec.Original,
		imageIDs:          slices.Clone(rec.ImageIds),
		fileIDs:           slices.Clone(rec.FileIds),
		items:             items,
		createdMs:         rec.CreatedMs,
		restoredFromCrash: true,
		storageKey:        storageKey,
		persisted:         true,
	}
	mgr.outbox[outRec.requestID] = outRec
	mgr.outboxBySession[outRec.sessionID] = append(mgr.outboxBySession[outRec.sessionID], outRec.requestID)
	ownerTag := "outbox:" + outRec.requestID
	for _, it := range outRec.items {
		if it != nil {
			c.protectTransfer(outRec.machineID, it.transferID, ownerTag)
		}
	}

	// Unify directly with SessionUIState.Outgoing & LocalContents!
	store := c.m.ensureSessionStore()
	ui := store.ensureUI(outRec.sessionID)
	summaries := makeOutboxSummaries(outRec.items)
	ui.LocalContents[outRec.requestID] = &screens.LocalSentContent{
		RequestID:        outRec.requestID,
		Text:             outRec.text,
		ImageIDs:         slices.Clone(outRec.imageIDs),
		FileIDs:          slices.Clone(outRec.fileIDs),
		Mode:             outRec.mode,
		LocalAttachments: summaries,
	}
	if !hasOutgoingReq(ui, outRec.requestID) {
		ui.Outgoing = append(ui.Outgoing, &screens.SessionOutgoingRecord{
			RequestID:         outRec.requestID,
			SlotKey:           "send:" + outRec.sessionID + ":" + outRec.requestID,
			Text:              outRec.text,
			ImageIDs:          slices.Clone(outRec.imageIDs),
			FileIDs:           slices.Clone(outRec.fileIDs),
			Mode:              outRec.mode,
			State:             screens.OutgoingUnknown,
			ErrorText:         attachments.OutboxRestartedReason(),
			RestoredFromCrash: true,
			LocalAttachments:  summaries,
			CreatedMs:         outRec.createdMs,
		})
	}
}

// startOutboxResend handles Retry / Promote / Undo of a message with local attachments directly in Core.
// Returns true if Core took ownership of the resend (false ONLY for legacy unmigrated shells that never
// sent AttachmentConfigEvent; configured Core shells never fall back to SessionResendAttachmentsEffect).
func (c *Core) startOutboxResend(
	ctx *sync.StepContext,
	sid, machineID, sourceReqID, newReqID, msgText string,
	mode v1.SendMode,
	effects *[]*corev1.Effect,
) bool {
	if c.m.attachments == nil || !c.m.attachments.configured {
		if c.m.attachments == nil || c.m.attachments.outbox[sourceReqID] == nil {
			return false
		}
	}
	mgr := c.m.attachments
	src := mgr.outbox[sourceReqID]
	store := c.m.ensureSessionStore()
	ui := store.ensureUI(sid)

	if src == nil || len(src.items) == 0 {
		// Core-configured path with no local outbox copy: fail explicitly instead of emitting legacy effect.
		ui.Outgoing = append(ui.Outgoing, &screens.SessionOutgoingRecord{
			RequestID: newReqID,
			SlotKey:   "send:" + sid + ":" + newReqID,
			Text:      msgText,
			Mode:      mode,
			State:     screens.OutgoingFailed,
			ErrorCode: "failed_precondition",
			ErrorText: text.Msg(text.KeyAttachmentFileMissing),
			CreatedMs: c.m.sync.NowMs(),
		})
		ui.LocalContents[newReqID] = &screens.LocalSentContent{
			RequestID: newReqID,
			Text:      msgText,
			Mode:      mode,
		}
		store.pendingDirty = true
		return true
	}
	if machineID == "" {
		machineID = src.machineID
	}

	clones := make([]*attItem, 0, len(src.items))
	hasFiles := false
	hasLocal := false
	for _, it := range src.items {
		if it == nil {
			continue
		}
		if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_FILE {
			hasFiles = true
		}
		if !it.remoteOnly && it.hasLocalCopy() {
			hasLocal = true
		}
		cp := it.cloneForSnapshot()
		cp.id = mgr.allocItemID()
		cp.remoteID = ""
		cp.uploadedMachineID = ""
		cp.transferID = ""
		cp.expiresMs = 0
		cp.beginDone = false
		cp.persistedBytes = 0
		cp.progress = 0
		cp.progressPct = 0
		cp.generation = 1
		cp.state = corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PREPARING
		cp.phase = attPhaseIdle
		clones = append(clones, cp)
	}
	summaries := makeOutboxSummaries(clones)

	recordImmediateFailure := func(errCode string, errText *corev1.Text) bool {
		c.registerFailedOutboxRecord(sid, machineID, newReqID, msgText, mode, src.original, src.imageIDs, src.fileIDs, clones, effects)
		ui.Outgoing = append(ui.Outgoing, &screens.SessionOutgoingRecord{
			RequestID:        newReqID,
			SlotKey:          "send:" + sid + ":" + newReqID,
			Text:             msgText,
			ImageIDs:         slices.Clone(src.imageIDs),
			FileIDs:          slices.Clone(src.fileIDs),
			Mode:             mode,
			State:            screens.OutgoingFailed,
			ErrorCode:        errCode,
			ErrorText:        errText,
			LocalAttachments: summaries,
			CreatedMs:        c.m.sync.NowMs(),
		})
		ui.LocalContents[newReqID] = &screens.LocalSentContent{
			RequestID:        newReqID,
			Text:             msgText,
			ImageIDs:         slices.Clone(src.imageIDs),
			FileIDs:          slices.Clone(src.fileIDs),
			Mode:             mode,
			LocalAttachments: summaries,
		}
		store.pendingDirty = true
		return true
	}

	if !hasLocal {
		return recordImmediateFailure("failed_precondition", text.Msg(text.KeyAttachmentFileMissing))
	}
	caps, onlineReady := c.resolveAttachmentCaps(machineID, false)
	if !onlineReady || machineID == "" {
		return recordImmediateFailure("unavailable", text.Msg(text.KeyAttachmentDisconnected))
	}
	if hasFiles && !caps.SupportsFiles {
		// Per docs/session-files.md: reject whole resend if computer lacks file_upload.
		return recordImmediateFailure("failed_precondition", text.Msg(text.KeyAttachmentUpdateRequired))
	}

	job := &attResendJob{
		sessionID:       sid,
		machineID:       machineID,
		sourceRequestID: sourceReqID,
		newRequestID:    newReqID,
		text:            msgText,
		mode:            mode,
		original:        src.original,
		items:           clones,
		outboxKey:       attachments.OutboxStorageKey(sid, newReqID),
	}
	mgr.resendJobs[newReqID] = job

	// Show in-progress OutgoingSending row in the pending tray while re-uploading!
	ui.Outgoing = append(ui.Outgoing, &screens.SessionOutgoingRecord{
		RequestID:        newReqID,
		SlotKey:          "send:" + sid + ":" + newReqID,
		Text:             msgText,
		ImageIDs:         slices.Clone(src.imageIDs),
		FileIDs:          slices.Clone(src.fileIDs),
		Mode:             mode,
		State:            screens.OutgoingSending,
		LocalAttachments: summaries,
		Resending:        true,
		CreatedMs:        c.m.sync.NowMs(),
	})
	ui.LocalContents[newReqID] = &screens.LocalSentContent{
		RequestID:        newReqID,
		Text:             msgText,
		ImageIDs:         slices.Clone(src.imageIDs),
		FileIDs:          slices.Clone(src.fileIDs),
		Mode:             mode,
		LocalAttachments: summaries,
	}
	store.pendingDirty = true

	scope := &corev1.DraftScope{Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION, Id: sid}
	for _, it := range job.items {
		if job.aborted {
			break
		}
		c.startItemPipeline(ctx, attOwnerResend, job.newRequestID, scope, machineID, job.original, it, effects)
	}
	c.pumpAttachmentQueues(ctx, effects)
	return true
}

func (c *Core) registerFailedOutboxRecord(
	sid, machineID, reqID, msgText string,
	mode v1.SendMode,
	original bool,
	imageIDs, fileIDs []string,
	items []*attItem,
	effects *[]*corev1.Effect,
) {
	mgr := c.m.ensureAttachments()
	if _, exists := mgr.outbox[reqID]; exists {
		return
	}
	snaps := make([]*attItem, 0, len(items))
	for _, it := range items {
		if it != nil {
			cp := it.cloneForSnapshot()
			cp.remoteID = ""
			cp.uploadedMachineID = ""
			cp.transferID = ""
			cp.expiresMs = 0
			snaps = append(snaps, cp)
		}
	}
	outboxKey := attachments.OutboxStorageKey(sid, reqID)
	outRec := &attOutboxRecord{
		sessionID:  sid,
		requestID:  reqID,
		machineID:  machineID,
		text:       msgText,
		mode:       mode,
		original:   original,
		imageIDs:   slices.Clone(imageIDs),
		fileIDs:    slices.Clone(fileIDs),
		items:      snaps,
		createdMs:  c.m.sync.NowMs(),
		storageKey: outboxKey,
		persisted:  false,
	}
	mgr.outbox[reqID] = outRec
	mgr.outboxBySession[sid] = append(mgr.outboxBySession[sid], reqID)
	if mgr.persistOutbox {
		var opID uint64
		c.emitOutboxWrite(sid, machineID, reqID, msgText, mode, original, imageIDs, fileIDs, snaps, attWriteKindFailedResendOutbox, "", &opID, effects)
	}
}

func (c *Core) onFailedResendOutboxWriteResult(meta attWriteOpMeta, ev *corev1.DraftStorageWriteResultEvent, effects *[]*corev1.Effect) {
	mgr := c.m.attachments
	if mgr == nil {
		return
	}
	rec := mgr.outbox[meta.requestID]
	if rec == nil || mgr.forgottenSessions[meta.sessionID] {
		if ev.Ok {
			*effects = append(*effects, makeDraftStorageDeleteEffect(corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX, meta.outboxKey))
		}
		return
	}
	if ev.Ok {
		rec.persisted = true
	}
}

func (c *Core) onResendItemFinished(ctx *sync.StepContext, newReqID string, effects *[]*corev1.Effect) {
	mgr := c.m.attachments
	job := mgr.resendJobs[newReqID]
	if job == nil || job.aborted {
		return
	}
	nowMs := c.m.sync.NowMs()
	var imageIDs, fileIDs []string
	for _, it := range job.items {
		if it == nil || !it.isReadyFor(job.machineID, job.original, nowMs) {
			return
		}
		if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE {
			imageIDs = append(imageIDs, it.remoteID)
		} else {
			fileIDs = append(fileIDs, it.remoteID)
		}
	}
	if mgr.persistOutbox {
		c.emitOutboxWrite(job.sessionID, job.machineID, job.newRequestID, job.text, job.mode, job.original, imageIDs, fileIDs, job.items, attWriteKindResendOutbox, "", &job.writeOpID, effects)
		return
	}
	c.finalizeResendJob(ctx, job, imageIDs, fileIDs, false, effects)
}

func (c *Core) onResendOutboxWriteResult(ctx *sync.StepContext, meta attWriteOpMeta, ev *corev1.DraftStorageWriteResultEvent, effects *[]*corev1.Effect) {
	mgr := c.m.attachments
	job := mgr.resendJobs[meta.requestID]
	if job == nil || job.aborted || job.writeOpID != ev.OpId || mgr.forgottenSessions[meta.sessionID] {
		if ev.Ok {
			*effects = append(*effects, makeDraftStorageDeleteEffect(corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX, meta.outboxKey))
		}
		return
	}
	if !ev.Ok {
		c.onResendItemFailed(ctx, job.newRequestID, nil, attachments.ClassifyStorageErrorText(ev.ErrorKind, false), effects)
		return
	}
	var imageIDs, fileIDs []string
	for _, it := range job.items {
		if it == nil {
			continue
		}
		if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE {
			imageIDs = append(imageIDs, it.remoteID)
		} else {
			fileIDs = append(fileIDs, it.remoteID)
		}
	}
	c.finalizeResendJob(ctx, job, imageIDs, fileIDs, true, effects)
}

func (c *Core) finalizeResendJob(ctx *sync.StepContext, job *attResendJob, imageIDs, fileIDs []string, persisted bool, effects *[]*corev1.Effect) {
	mgr := c.m.attachments
	delete(mgr.resendJobs, job.newRequestID)

	// Re-check file_upload capability right before submitting SendRequest.
	caps, _ := c.resolveAttachmentCaps(job.machineID, false)
	if len(fileIDs) > 0 && !caps.SupportsFiles {
		// Preserve outbox record under job.newRequestID so failed row can still be retried or put back!
		outRec := &attOutboxRecord{
			sessionID:  job.sessionID,
			requestID:  job.newRequestID,
			machineID:  job.machineID,
			text:       job.text,
			mode:       job.mode,
			original:   job.original,
			imageIDs:   slices.Clone(imageIDs),
			fileIDs:    slices.Clone(fileIDs),
			items:      job.items,
			createdMs:  c.m.sync.NowMs(),
			storageKey: job.outboxKey,
			persisted:  persisted,
		}
		mgr.outbox[outRec.requestID] = outRec
		mgr.outboxBySession[outRec.sessionID] = append(mgr.outboxBySession[outRec.sessionID], outRec.requestID)
		c.markOutgoingFailed(job.sessionID, job.newRequestID, "failed_precondition", text.Msg(text.KeyAttachmentUpdateRequired))
		return
	}

	outRec := &attOutboxRecord{
		sessionID:  job.sessionID,
		requestID:  job.newRequestID,
		machineID:  job.machineID,
		text:       job.text,
		mode:       job.mode,
		original:   job.original,
		imageIDs:   slices.Clone(imageIDs),
		fileIDs:    slices.Clone(fileIDs),
		items:      job.items,
		createdMs:  c.m.sync.NowMs(),
		storageKey: job.outboxKey,
		persisted:  persisted,
	}
	mgr.outbox[outRec.requestID] = outRec
	mgr.outboxBySession[outRec.sessionID] = append(mgr.outboxBySession[outRec.sessionID], outRec.requestID)
	ownerTag := "outbox:" + outRec.requestID
	for _, it := range outRec.items {
		if it != nil {
			c.protectTransfer(outRec.machineID, it.transferID, ownerTag)
		}
	}
	c.submitSessionSendFromDraft(ctx, outRec.sessionID, outRec.machineID, outRec.requestID, outRec.text, outRec.mode, outRec.imageIDs, outRec.fileIDs, makeOutboxSummaries(outRec.items))
}

// abortResendJob aborts an in-flight Core resend job (canceling commands/timers, emitting AbortImageUpload,
// and preventing SendRequest submission). When preserveOutbox is true (cancel-with-undo or put-back),
// items are preserved in memory under reqID so undo/put-back can reuse the local copies; once undo expires
// or put-back completes, normal pruning releases unreferenced local refs.
func (c *Core) abortResendJob(ctx *sync.StepContext, reqID string, preserveOutbox bool, effects *[]*corev1.Effect) bool {
	if c.m.attachments == nil || reqID == "" {
		return false
	}
	mgr := c.m.attachments
	job := mgr.resendJobs[reqID]
	if job == nil {
		return false
	}
	job.aborted = true
	delete(mgr.resendJobs, reqID)
	var imgIDs, fileIDs []string
	if src := mgr.outbox[job.sourceRequestID]; src != nil {
		imgIDs = src.imageIDs
		fileIDs = src.fileIDs
	}
	var refs []string
	for _, it := range job.items {
		if it != nil {
			c.invalidateItem(ctx, attOwnerResend, reqID, it, true, effects)
			refs = append(refs, it.localRefs()...)
		}
	}
	if preserveOutbox {
		if _, exists := mgr.outbox[reqID]; !exists {
			snaps := make([]*attItem, 0, len(job.items))
			for _, it := range job.items {
				if it != nil {
					snaps = append(snaps, it.cloneForSnapshot())
				}
			}
			mgr.outbox[reqID] = &attOutboxRecord{
				sessionID:  job.sessionID,
				requestID:  reqID,
				machineID:  job.machineID,
				text:       job.text,
				mode:       job.mode,
				original:   job.original,
				imageIDs:   slices.Clone(imgIDs),
				fileIDs:    slices.Clone(fileIDs),
				items:      snaps,
				createdMs:  c.m.sync.NowMs(),
				storageKey: job.outboxKey,
				persisted:  false,
			}
			mgr.outboxBySession[job.sessionID] = append(mgr.outboxBySession[job.sessionID], reqID)
		}
	} else {
		c.emitUnreferencedLocalReleases(refs, effects)
	}
	c.pumpAttachmentQueues(ctx, effects)
	return true
}

func (c *Core) onResendItemFailed(ctx *sync.StepContext, newReqID string, problem *corev1.ImageProblemView, fileErr *corev1.Text, effects *[]*corev1.Effect) {
	mgr := c.m.attachments
	job := mgr.resendJobs[newReqID]
	if job == nil || job.aborted {
		return
	}
	job.aborted = true
	delete(mgr.resendJobs, newReqID)
	var imgIDs, fileIDs []string
	if src := mgr.outbox[job.sourceRequestID]; src != nil {
		imgIDs = src.imageIDs
		fileIDs = src.fileIDs
	}
	for _, it := range job.items {
		if it != nil {
			c.invalidateItem(ctx, attOwnerResend, newReqID, it, true, effects)
		}
	}
	// Preserve outbox record under newReqID so the failed row retains local refs and can be retried or put back!
	c.registerFailedOutboxRecord(job.sessionID, job.machineID, job.newRequestID, job.text, job.mode, job.original, imgIDs, fileIDs, job.items, effects)
	errText := fileErr
	if errText == nil && problem != nil {
		errText = problem.Message
	}
	if errText == nil {
		errText = text.Msg(text.KeyAttachmentUploadFailed)
	}
	c.markOutgoingFailed(job.sessionID, job.newRequestID, "upload_failed", errText)
}

func (c *Core) markOutgoingFailed(sid, reqID, errCode string, errText *corev1.Text) {
	store := c.m.ensureSessionStore()
	ui := store.ensureUI(sid)
	for _, og := range ui.Outgoing {
		if og != nil && og.RequestID == reqID {
			og.State = screens.OutgoingFailed
			og.ErrorCode = errCode
			og.ErrorText = errText
			store.pendingDirty = true
			return
		}
	}
}

// canPutBackRequest evaluates put-back preconditions (not restoring, capabilities with put_back=true, per-message limits)
// BEFORE modifying the draft or canceling a server-backed message (docs/session-files.md:9).
func (c *Core) canPutBackRequest(sid, reqID string, imgIDs, fileIDs []string) (*corev1.Text, bool) {
	scope := &corev1.DraftScope{Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION, Id: sid}
	d := c.ensureDraftState(scope)
	if d.loadState == draftLoadLoading {
		return attachments.RestoreWaitReason(), false
	}

	var outRec *attOutboxRecord
	if c.m.attachments != nil {
		outRec = c.m.attachments.outbox[reqID]
	}

	var cands []*corev1.AttachmentCandidateInput
	if outRec != nil && len(outRec.items) > 0 {
		for _, it := range outRec.items {
			if it == nil {
				continue
			}
			if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE {
				cands = append(cands, &corev1.AttachmentCandidateInput{
					Name:      "image.jpg",
					MimeType:  "image/jpeg",
					SizeBytes: it.sizeBytes,
				})
			} else {
				name := it.fileName
				if name == "" {
					name = "file"
				}
				cands = append(cands, &corev1.AttachmentCandidateInput{
					Name:      name,
					MimeType:  it.mimeType,
					SizeBytes: it.sizeBytes,
					ForceFile: true,
				})
			}
		}
	} else {
		for range imgIDs {
			cands = append(cands, &corev1.AttachmentCandidateInput{
				Name:      "image.jpg",
				MimeType:  "image/jpeg",
				SizeBytes: 1,
			})
		}
		for range fileIDs {
			cands = append(cands, &corev1.AttachmentCandidateInput{
				Name:      "file",
				SizeBytes: 0,
				ForceFile: true,
			})
		}
	}
	if len(cands) == 0 {
		return nil, true
	}

	machineID := c.resolveDraftMachineID(d)
	caps, _ := c.resolveAttachmentCaps(machineID, true)
	curImgs, curFiles := d.itemCounts()
	resImgs, resFiles := d.reservedCounts()
	decisions, notice := attachments.EvaluateCandidates(caps, curImgs+resImgs, curFiles+resFiles, cands)
	for _, dec := range decisions {
		if dec != nil && !dec.Accepted {
			reason := notice
			if reason == nil {
				reason = dec.RejectReason
			}
			return reason, false
		}
	}
	return nil, true
}

// applyPutBackToDraft puts a message's text and attachments back into the session draft,
// invalidating all historical remote IDs, starting re-uploads for local copies, marking
// remote-only items failed, and applying the persist-draft-before-delete-outbox (whenKept) barrier.
func (c *Core) applyPutBackToDraft(
	ctx *sync.StepContext,
	sid, reqID, restoredText string,
	imgIDs, fileIDs []string,
	effects *[]*corev1.Effect,
) bool {
	if c.m.attachments == nil || !c.m.attachments.configured {
		// Only apply in-core when attachment manager is configured/active, OR when Core owns the outbox/draft.
		if c.m.attachments == nil || (c.m.attachments.outbox[reqID] == nil && c.m.attachments.drafts["session:"+sid] == nil) {
			return false
		}
	}
	scope := &corev1.DraftScope{Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION, Id: sid}
	d := c.ensureDraftState(scope)
	mgr := c.m.ensureAttachments()

	if reason, ok := c.canPutBackRequest(sid, reqID, imgIDs, fileIDs); !ok {
		d.notice = reason
		c.markDraftDirty(ctx, d.scope)
		return true
	}

	// 1. Merge text preserving what user already typed.
	if restoredText != "" {
		d.text = attachments.MergePutBackText(d.text, restoredText)
		d.textRev++
	}

	// 2. Attach items (invalidating all historical remote IDs!).
	outRec := mgr.outbox[reqID]
	machineID := c.resolveDraftMachineID(d)
	existingImgs, _ := d.itemCounts()
	if outRec != nil && len(outRec.items) > 0 {
		if existingImgs == 0 {
			d.original = outRec.original
		}
		for _, srcIt := range outRec.items {
			if srcIt == nil {
				continue
			}
			cp := srcIt.cloneForSnapshot()
			cp.id = mgr.allocItemID()
			// Synchronously invalidate old remote IDs & expiry: MUST re-upload before sending!
			cp.remoteID = ""
			cp.uploadedMachineID = ""
			cp.transferID = ""
			cp.expiresMs = 0
			cp.beginDone = false
			cp.persistedBytes = 0
			cp.progress = 0
			cp.progressPct = 0
			cp.generation = 1
			cp.targetMachineID = machineID
			cp.state = corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PREPARING
			cp.phase = attPhaseIdle
			if !cp.hasLocalCopy() {
				cp.remoteOnly = true
				c.markItemMissingLocal(cp)
			} else if cp.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE && cp.preparedOriginal != d.original && !cp.hasOriginalSource {
				// Cannot re-prepare into different mode without original source.
				cp.remoteOnly = true
				c.markItemMissingLocal(cp)
			}
			d.items = append(d.items, cp)
		}
	} else {
		// No local outbox copy (e.g., page refresh without persisted outbox or foreign message):
		// add remote-only failed items blocking send until removed/re-selected (docs/session-files.md:66).
		for range imgIDs {
			it := &attItem{
				id:         mgr.allocItemID(),
				kind:       corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE,
				remoteOnly: true,
				generation: 1,
			}
			c.markItemMissingLocal(it)
			d.items = append(d.items, it)
		}
		for range fileIDs {
			it := &attItem{
				id:          mgr.allocItemID(),
				kind:        corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_FILE,
				displayName: "file",
				fileName:    "file",
				remoteOnly:  true,
				generation:  1,
			}
			c.markItemMissingLocal(it)
			d.items = append(d.items, it)
		}
	}

	// 3. Persist updated draft FIRST; only after draft write succeeds do we delete the old Outbox record (whenKept barrier)!
	var targetSeq uint64
	if mgr.persistDrafts && d.loadState == draftLoadLoaded {
		targetSeq = c.enqueueDraftWrite(d, effects)
	}
	if outRec != nil {
		// Drop from in-memory outbox & release transfer protection; defer disk deletion until draft write lands!
		c.removeOutboxFromMemory(outRec)
		if outRec.persisted {
			if targetSeq > 0 && d.activeWriteOpID != 0 {
				d.pendingOutboxDeletes = append(d.pendingOutboxDeletes, pendingDraftWriteBarrier{
					writeSeq:  targetSeq,
					outboxKey: outRec.storageKey,
					sessionID: outRec.sessionID,
					requestID: outRec.requestID,
				})
			} else {
				*effects = append(*effects, makeDraftStorageDeleteEffect(corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX, outRec.storageKey))
			}
		}
	}

	// 4. Kick off re-upload for items that have local copies.
	c.startPendingDraftUploads(ctx, d, effects)
	c.pumpAttachmentQueues(ctx, effects)
	c.markDraftDirty(ctx, d.scope)
	return true
}

func (c *Core) handleDraftPutBackInput(ctx *sync.StepContext, act *corev1.DraftPutBackInputAction) []*corev1.Effect {
	if act == nil || act.SessionId == "" || act.RequestId == "" {
		return nil
	}
	sid := act.SessionId
	reqID := act.RequestId
	store := c.m.ensureSessionStore()
	ui := store.ensureUI(sid)
	snap := c.m.sync.SessionSnapshot(sid)
	reconcileSessionSnapshot(snap, ui)

	var textStr string
	var imgIDs, fileIDs []string
	if c.m.attachments != nil {
		if outRec := c.m.attachments.outbox[reqID]; outRec != nil {
			textStr = outRec.text
			imgIDs = outRec.imageIDs
			fileIDs = outRec.fileIDs
		}
	}
	if textStr == "" && len(imgIDs) == 0 && len(fileIDs) == 0 {
		if lc := ui.LocalContents[reqID]; lc != nil {
			textStr = lc.Text
			imgIDs = lc.ImageIDs
			fileIDs = lc.FileIDs
		}
	}
	if snap.Projection != nil {
		for _, ip := range snap.Projection.Inputs {
			if ip == nil || ip.RequestId != reqID {
				continue
			}
			if textStr == "" {
				if ip.Accepted != nil && ip.Accepted.Text != "" {
					textStr = ip.Accepted.Text
				} else if ip.Detail != nil {
					textStr = ip.Detail.Text
				}
			}
			if len(imgIDs) == 0 && ip.Accepted != nil {
				imgIDs = ip.Accepted.ImageIds
			}
			if len(fileIDs) == 0 && ip.Accepted != nil {
				for _, f := range ip.Accepted.Files {
					if f != nil && f.FileId != "" {
						fileIDs = append(fileIDs, f.FileId)
					}
				}
			}
		}
	}

	// Pre-check BEFORE removing/dismissing the failed record!
	if reason, ok := c.canPutBackRequest(sid, reqID, imgIDs, fileIDs); !ok {
		scope := &corev1.DraftScope{Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION, Id: sid}
		d := c.ensureDraftState(scope)
		d.notice = reason
		c.markDraftDirty(ctx, d.scope)
		return nil
	}

	var effects []*corev1.Effect
	c.clearOutgoingAndSlot(ctx, store, ui, reqID, "", true)
	ui.DismissedInputIDs[reqID] = true
	c.applyPutBackToDraft(ctx, sid, reqID, textStr, imgIDs, fileIDs, &effects)
	pruneSessionLocalContents(ui, map[string]bool{}, activeInputSetFromSnap(snap, ui))
	store.pendingDirty = true
	effects = append(effects, &corev1.Effect{
		Effect: &corev1.Effect_SessionRestoreComposer{
			SessionRestoreComposer: &corev1.SessionRestoreComposerEffect{
				SessionId:     sid,
				RequestId:     reqID,
				Text:          textStr,
				ImageIds:      slices.Clone(imgIDs),
				FileIds:       slices.Clone(fileIDs),
				HandledByCore: true,
			},
		},
	})
	return effects
}

func (c *Core) removeOutboxFromMemory(rec *attOutboxRecord) {
	if rec == nil || c.m.attachments == nil {
		return
	}
	mgr := c.m.attachments
	delete(mgr.outbox, rec.requestID)
	list := mgr.outboxBySession[rec.sessionID]
	for i, id := range list {
		if id == rec.requestID {
			mgr.outboxBySession[rec.sessionID] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(mgr.outboxBySession[rec.sessionID]) == 0 {
		delete(mgr.outboxBySession, rec.sessionID)
	}
	ownerTag := "outbox:" + rec.requestID
	for _, it := range rec.items {
		if it != nil {
			c.releaseTransferProtection(rec.machineID, it.transferID, ownerTag)
		}
	}
}

func (c *Core) deleteOutboxRecord(rec *attOutboxRecord, effects *[]*corev1.Effect) {
	if rec == nil {
		return
	}
	var refs []string
	for _, it := range rec.items {
		refs = append(refs, it.localRefs()...)
	}
	c.removeOutboxFromMemory(rec)
	if rec.persisted && rec.storageKey != "" {
		*effects = append(*effects, makeDraftStorageDeleteEffect(corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX, rec.storageKey))
	}
	c.emitUnreferencedLocalReleases(refs, effects)
}

func (c *Core) pruneSettledOutboxRecords(ctx *sync.StepContext, effects *[]*corev1.Effect) {
	if c.m.attachments == nil || len(c.m.attachments.outbox) == 0 {
		return
	}
	mgr := c.m.attachments
	store := c.m.ensureSessionStore()

	sids := make([]string, 0, len(mgr.outboxBySession))
	for sid := range mgr.outboxBySession {
		sids = append(sids, sid)
	}
	sortutil.Strings(sids)

	for _, sid := range sids {
		snap := c.m.sync.SessionSnapshot(sid)
		ui := store.ensureUI(sid)
		reconcileSessionSnapshot(snap, ui)

		// Build set of request IDs that are still active/retriable/put-back-able.
		keep := make(map[string]bool)
		for _, og := range ui.Outgoing {
			if og != nil && !og.Restored && og.RequestID != "" {
				keep[og.RequestID] = true
			}
		}
		for reqID := range ui.LocalContents {
			keep[reqID] = true
		}
		if snap.Projection != nil {
			for _, pi := range snap.Projection.PendingInputs {
				if pi != nil && pi.RequestId != "" && !ui.DismissedInputIDs[pi.RequestId] {
					keep[pi.RequestId] = true
				}
			}
			for _, ip := range snap.Projection.Inputs {
				if ip == nil || ip.RequestId == "" || ui.DismissedInputIDs[ip.RequestId] {
					continue
				}
				switch ip.Status {
				case v1.InputStatus_INPUT_STATUS_FAILED,
					v1.InputStatus_INPUT_STATUS_NEEDS_TERMINAL,
					v1.InputStatus_INPUT_STATUS_UNCONFIRMED:
					keep[ip.RequestId] = true
				case v1.InputStatus_INPUT_STATUS_CANCELED:
					// Unsent-on-exit inputs stay put-back-able!
					if ip.Detail != nil && ip.Detail.Reason == "exited" {
						keep[ip.RequestId] = true
					}
				}
			}
		}
		if ui.Undo != nil {
			for _, it := range ui.Undo.Items {
				if it != nil && it.SourceReqID != "" {
					keep[it.SourceReqID] = true
				}
			}
		}
		for _, intent := range ui.CancelIntents {
			if intent != nil && intent.SourceReqID != "" {
				keep[intent.SourceReqID] = true
			}
		}
		for _, job := range mgr.resendJobs {
			if job != nil && !job.aborted && job.sourceRequestID != "" {
				keep[job.sourceRequestID] = true
			}
		}

		reqIDs := slices.Clone(mgr.outboxBySession[sid])
		for _, reqID := range reqIDs {
			rec := mgr.outbox[reqID]
			if rec == nil {
				continue
			}
			if !keep[reqID] {
				c.deleteOutboxRecord(rec, effects)
				store.pendingDirty = true
			}
		}
	}
	_ = ctx
}

// prepareNewSessionAttachments checks and captures Core-managed attachments for NewSessionSubmit.
// Returns (imageIDs, fileIDs, blocked). When Core's new-session draft has no items, returns (nil, nil, false)
// so legacy NewSessionSubmitAction.ImageIds/FileIds continue working seamlessly.
func (c *Core) prepareNewSessionAttachments(ctx *sync.StepContext, projectKey, machineID, reqID string, effects *[]*corev1.Effect) ([]string, []string, bool) {
	if c.m.attachments == nil || projectKey == "" {
		return nil, nil, false
	}
	d := c.m.attachments.drafts["new:"+projectKey]
	if d == nil || len(d.items) == 0 {
		return nil, nil, false
	}
	if d.loadState == draftLoadLoading {
		return nil, nil, true
	}
	if rImg, rFile := d.reservedCounts(); rImg > 0 || rFile > 0 {
		return nil, nil, true
	}
	if c.refreshDraftExpiredItems(ctx, d, effects) {
		c.pumpAttachmentQueues(ctx, effects)
		c.markDraftDirty(ctx, d.scope)
		return nil, nil, true
	}
	caps, onlineReady := c.resolveAttachmentCaps(machineID, false)
	if !onlineReady {
		return nil, nil, true
	}
	nowMs := c.m.sync.NowMs()
	var imageIDs, fileIDs, itemIDs []string
	var transfers []attTransferRef
	for _, it := range d.items {
		if it == nil {
			continue
		}
		if !it.isReadyFor(machineID, d.original, nowMs) {
			return nil, nil, true
		}
		itemIDs = append(itemIDs, it.id)
		if it.transferID != "" {
			transfers = append(transfers, attTransferRef{machineID: machineID, transferID: it.transferID})
		}
		if it.kind == corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE {
			imageIDs = append(imageIDs, it.remoteID)
		} else {
			fileIDs = append(fileIDs, it.remoteID)
		}
	}
	if len(fileIDs) > 0 && !caps.SupportsFiles {
		d.notice = text.Msg(text.KeyAttachmentUpdateRequired)
		c.markDraftDirty(ctx, d.scope)
		return nil, nil, true
	}
	if len(imageIDs) > 0 && !caps.SupportsImages {
		d.notice = caps.ImageUploadBlockedReason
		c.markDraftDirty(ctx, d.scope)
		return nil, nil, true
	}
	ownerTag := "start:" + projectKey
	for _, tr := range transfers {
		c.protectTransfer(tr.machineID, tr.transferID, ownerTag)
	}
	c.m.attachments.newSessionStarts[projectKey] = &attNewSessionStart{
		projectKey: projectKey,
		requestID:  reqID,
		machineID:  machineID,
		prompt:     d.text,
		itemIDs:    itemIDs,
		transfers:  transfers,
	}
	return imageIDs, fileIDs, false
}

func (c *Core) onNewSessionCreateSettled(projectKey string, sc sync.SettledCommand, effects *[]*corev1.Effect) {
	if c.m.attachments == nil || projectKey == "" {
		return
	}
	mgr := c.m.attachments
	st := mgr.newSessionStarts[projectKey]
	if st == nil {
		return
	}
	if sc.Category == outcome.Unknown && !sc.Expired {
		// Keep transfers protected while frozen unknown start may be retried!
		return
	}
	delete(mgr.newSessionStarts, projectKey)
	ownerTag := "start:" + projectKey
	for _, tr := range st.transfers {
		c.releaseTransferProtection(tr.machineID, tr.transferID, ownerTag)
	}
	consumed := sc.Category == outcome.Executed && sc.ErrorCode == "" && sc.Result != nil && sc.Result.GetCreateSession() != nil
	if !consumed {
		return
	}

	d := mgr.drafts["new:"+projectKey]
	if d == nil {
		return
	}
	stepPrev, ctx := c.m.sync.BeginStep()
	var localEffects []*corev1.Effect
	takenSet := make(map[string]bool, len(st.itemIDs))
	for _, id := range st.itemIDs {
		takenSet[id] = true
	}
	var releaseRefs []string
	kept := d.items[:0]
	for _, it := range d.items {
		if it == nil {
			continue
		}
		if takenSet[it.id] {
			c.invalidateItem(ctx, attOwnerDraft, d.key, it, false, &localEffects)
			releaseRefs = append(releaseRefs, it.localRefs()...)
			continue
		}
		kept = append(kept, it)
	}
	d.items = kept
	if imgs, _ := d.itemCounts(); imgs == 0 {
		d.original = false
		d.viewerOpen = false
		d.viewerIndex = 0
	}
	if strings.TrimSpace(d.text) == strings.TrimSpace(st.prompt) {
		d.text = ""
		d.textRev++
	}
	c.syncNewSessionPromptFromDraft(d)
	c.syncNewSessionAttachmentCount(d)
	c.scheduleDraftExpiryTimer(ctx, d)
	if d.loadState == draftLoadLoaded {
		c.enqueueDraftWrite(d, &localEffects)
	}
	c.emitUnreferencedLocalReleases(releaseRefs, &localEffects)
	c.markDraftDirty(ctx, d.scope)
	stepRes := c.m.sync.EndStep(stepPrev, ctx)
	*effects = append(*effects, stepRes.Effects...)
	*effects = append(*effects, localEffects...)
}

func (c *Core) releaseNewSessionStartProtection(projectKey string) {
	if c.m.attachments == nil || projectKey == "" {
		return
	}
	st := c.m.attachments.newSessionStarts[projectKey]
	if st == nil {
		return
	}
	delete(c.m.attachments.newSessionStarts, projectKey)
	ownerTag := "start:" + projectKey
	for _, tr := range st.transfers {
		c.releaseTransferProtection(tr.machineID, tr.transferID, ownerTag)
	}
}

func (c *Core) handleDraftForgetSession(ctx *sync.StepContext, act *corev1.DraftForgetSessionAction) []*corev1.Effect {
	if act == nil || act.SessionId == "" {
		return nil
	}
	sid := act.SessionId
	mgr := c.m.ensureAttachments()
	mgr.forgottenSessions[sid] = true

	var effects []*corev1.Effect
	var refs []string

	// 1. Drop session draft.
	draftKey := "session:" + sid
	if d := mgr.drafts[draftKey]; d != nil {
		for _, it := range d.items {
			if it != nil {
				c.invalidateItem(ctx, attOwnerDraft, d.key, it, true, &effects)
				refs = append(refs, it.localRefs()...)
			}
		}
		for _, b := range d.pendingOutboxDeletes {
			effects = append(effects, makeDraftStorageDeleteEffect(corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX, b.outboxKey))
		}
		c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_DRAFT_EXPIRY, d.key)
		delete(mgr.drafts, draftKey)
	}
	if mgr.persistDrafts {
		effects = append(effects, makeDraftStorageDeleteEffect(corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT, draftKey))
	}

	// 2. Abort any active resend jobs for this session.
	for rk, job := range mgr.resendJobs {
		if job != nil && job.sessionID == sid {
			job.aborted = true
			for _, it := range job.items {
				if it != nil {
					c.invalidateItem(ctx, attOwnerResend, rk, it, true, &effects)
					refs = append(refs, it.localRefs()...)
				}
			}
			delete(mgr.resendJobs, rk)
		}
	}

	// 3. Delete all outbox records for this session.
	for _, reqID := range slices.Clone(mgr.outboxBySession[sid]) {
		if rec := mgr.outbox[reqID]; rec != nil {
			for _, it := range rec.items {
				refs = append(refs, it.localRefs()...)
			}
			c.removeOutboxFromMemory(rec)
			if rec.persisted && rec.storageKey != "" {
				effects = append(effects, makeDraftStorageDeleteEffect(corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX, rec.storageKey))
			}
		}
	}

	// 4. Clean SessionUIState.Outgoing / LocalContents for this session.
	if c.m.sessions != nil {
		if ui := c.m.sessions.uiBySession[sid]; ui != nil {
			ui.Outgoing = nil
			clear(ui.LocalContents)
			c.m.sessions.pendingDirty = true
		}
	}

	c.emitUnreferencedLocalReleases(refs, &effects)
	c.pumpAttachmentQueues(ctx, &effects)
	mgr.dirtyDraftScreen = true
	return effects
}

func (c *Core) handleDraftClearAll(ctx *sync.StepContext) []*corev1.Effect {
	mgr := c.m.ensureAttachments()
	var effects []*corev1.Effect

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
		for _, it := range d.items {
			if it != nil {
				c.invalidateItem(ctx, attOwnerDraft, d.key, it, false, &effects)
			}
		}
		c.m.sync.CancelAppTimer(ctx, corev1.TimerKind_TIMER_KIND_DRAFT_EXPIRY, d.key)
	}

	for rk, job := range mgr.resendJobs {
		if job != nil {
			job.aborted = true
			for _, it := range job.items {
				if it != nil {
					c.invalidateItem(ctx, attOwnerResend, rk, it, false, &effects)
				}
			}
		}
	}

	mgr.activeListOpID = 0
	mgr.activePrepareOpID = 0
	mgr.prepareQueue = nil
	mgr.activeFileUpload = nil
	mgr.fileUploadQueue = nil
	clear(mgr.drafts)
	clear(mgr.forgottenSessions)
	clear(mgr.outbox)
	clear(mgr.outboxBySession)
	clear(mgr.resendJobs)
	clear(mgr.newSessionStarts)
	clear(mgr.reqToUpload)
	clear(mgr.readOpToMeta)
	clear(mgr.prepareOpToMeta)
	clear(mgr.draftWriteOps)
	clear(mgr.protectedTransfers)

	if c.m.sessions != nil {
		for _, ui := range c.m.sessions.uiBySession {
			if ui != nil {
				ui.Outgoing = nil
				clear(ui.LocalContents)
			}
		}
		c.m.sessions.pendingDirty = true
	}
	mgr.dirtyDraftScreen = true
	ctx.Dirty.Add(false, false, true)
	effects = append(effects, &corev1.Effect{
		Effect: &corev1.Effect_DraftStorageClearAll{
			DraftStorageClearAll: &corev1.DraftStorageClearAllEffect{},
		},
	})
	return effects
}
