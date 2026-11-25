package app

import (
	"bytes"
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/attachments"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func attTestHash(b byte) []byte {
	return bytes.Repeat([]byte{b}, 32)
}

type attDispatchedCmd struct {
	dc  *corev1.DispatchCommandEffect
	cmd *v1.Command
}

type attHarness struct {
	t         *testing.T
	core      *Core
	nowMs     int64
	monoMs    int64
	sessionID string
	machineA  string
	machineB  string
	project   string
}

func newAttHarness(t *testing.T, persistDrafts, persistOutbox bool) *attHarness {
	t.Helper()
	h := &attHarness{
		t:         t,
		core:      New(),
		nowMs:     1_791_078_060_000,
		monoMs:    10_000,
		sessionID: "s-att",
		machineA:  "m-a",
		machineB:  "m-b",
		project:   "/Users/me/proj",
	}
	t.Cleanup(h.core.Close)

	ids := make([]string, 0, 128)
	for i := 1; i <= 128; i++ {
		ids = append(ids, "att-id-"+string(rune('a'+(i/26)%26))+string(rune('a'+i%26)))
	}
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_EntropyProvided{
			EntropyProvided: &corev1.EntropyProvided{
				RandomIds:  ids,
				UnitFloats: []float64{0.5, 0.5, 0.5, 0.5, 0.5, 0.5},
			},
		},
	})
	effStart := h.dispatch(&corev1.Event{
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
		},
	})
	var tokOp uint64
	for _, ef := range effStart.GetEffects() {
		if et := ef.GetEnsureToken(); et != nil {
			tokOp = et.GetOpId()
		}
	}
	effTok := h.dispatch(&corev1.Event{
		Event: &corev1.Event_TokenResult{
			TokenResult: &corev1.TokenResultEvent{OpId: tokOp, Token: "tok-att", ExpiresMs: h.nowMs + 3_600_000},
		},
	})
	var legID uint64
	for _, ef := range effTok.GetEffects() {
		if os := ef.GetOpenStream(); os != nil {
			legID = os.GetLegId()
		}
	}
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_StreamReady{
			StreamReady: &corev1.StreamReadyEvent{LegId: legID, ServerTimeMs: h.nowMs},
		},
	})
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Security: &corev1.DemoSecurity{
					IsMember:          true,
					SelfDeviceId:      "dev-1",
					AuthorityDeviceId: "dev-1",
					LeaseEndMs:        h.nowMs + 86_400_000,
					TrustedMachineIds: []string{h.machineA, h.machineB},
					TrustedClientIds:  []string{"dev-1"},
				},
				Machines: []*corev1.DemoMachine{
					{
						MachineId: h.machineA,
						Online:    true,
						Meta: &v1.MachineMeta{
							Hostname:        "Alpha",
							Home:            "/Users/me",
							ProtocolVersion: 17,
							Capabilities: &v1.MachineCapabilities{
								ImageUpload:          true,
								FileUpload:           true,
								SharedFiles:          true,
								SharedFileChunkBytes: 512 * 1024,
							},
						},
					},
					{
						MachineId: h.machineB,
						Online:    true,
						Meta: &v1.MachineMeta{
							Hostname:        "Beta",
							Home:            "/Users/me",
							ProtocolVersion: 17,
							Capabilities: &v1.MachineCapabilities{
								ImageUpload: true,
								FileUpload:  false, // Beta does NOT support files
							},
						},
					},
				},
				Sessions: []*corev1.DemoSession{
					{
						SessionId: h.sessionID,
						MachineId: h.machineA,
						LastSeq:   1,
						Meta: &v1.Session{
							Id:     h.sessionID,
							Cwd:    h.project,
							Agent:  v1.Agent_AGENT_CODEX,
							Status: v1.SessionStatus_SESSION_STATUS_RUNNING,
						},
					},
				},
				SessionEvents: []*corev1.DemoSessionEvents{
					{
						SessionId: h.sessionID,
						Events: []*v1.Event{
							{
								Seq:       1,
								SessionId: h.sessionID,
								Kind:      &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "init"}},
							},
						},
					},
				},
			},
		},
	})
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentConfig{
			AttachmentConfig: &corev1.AttachmentConfigEvent{
				PersistDrafts: persistDrafts,
				PersistOutbox: persistOutbox,
			},
		},
	})
	return h
}

func (h *attHarness) advance(deltaMs int64) {
	h.nowMs += deltaMs
	h.monoMs += deltaMs
}

func (h *attHarness) dispatch(ev *corev1.Event) *corev1.EffectBatch {
	if ev.NowMs == 0 {
		ev.NowMs = h.nowMs
	}
	if ev.MonoMs == 0 {
		ev.MonoMs = h.monoMs
	}
	return h.core.Dispatch(ev)
}

func (h *attHarness) sessionScope() *corev1.DraftScope {
	return &corev1.DraftScope{
		Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION,
		Id:   h.sessionID,
	}
}

func (h *attHarness) newSessionScope() *corev1.DraftScope {
	return &corev1.DraftScope{
		Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_NEW_SESSION,
		Id:   h.project,
	}
}

func (h *attHarness) draftView(scope *corev1.DraftScope) *corev1.AttachmentDraftView {
	h.t.Helper()
	vm, err := h.core.View(&corev1.Screen{
		Screen: &corev1.Screen_AttachmentDraft{
			AttachmentDraft: &corev1.AttachmentDraftScreenRequest{Scope: scope},
		},
	})
	if err != nil {
		h.t.Fatalf("View(AttachmentDraft): %v", err)
	}
	return vm.GetAttachmentDraft().GetDraft()
}

func (h *attHarness) sessionView() *corev1.SessionViewModel {
	h.t.Helper()
	vm, err := h.core.View(&corev1.Screen{
		Screen: &corev1.Screen_Session{
			Session: &corev1.SessionScreenRequest{SessionId: h.sessionID},
		},
	})
	if err != nil {
		h.t.Fatalf("View(Session): %v", err)
	}
	return vm.GetSession()
}

func pickedImage(name string, sizeBytes int64, sourceRef string, hasOrig bool) *corev1.DraftPickedItem {
	return &corev1.DraftPickedItem{
		Candidate: &corev1.AttachmentCandidateInput{
			Name:      name,
			MimeType:  "image/jpeg",
			SizeBytes: sizeBytes,
		},
		SourceRef:         sourceRef,
		HasOriginalSource: hasOrig,
	}
}

func pickedFile(name, mime string, sizeBytes int64, sourceRef string) *corev1.DraftPickedItem {
	return &corev1.DraftPickedItem{
		Candidate: &corev1.AttachmentCandidateInput{
			Name:      name,
			MimeType:  mime,
			SizeBytes: sizeBytes,
			ForceFile: true,
		},
		SourceRef: sourceRef,
	}
}

func findPrepareEffects(b *corev1.EffectBatch) []*corev1.AttachmentPrepareImageEffect {
	var out []*corev1.AttachmentPrepareImageEffect
	for _, ef := range b.GetEffects() {
		if p := ef.GetAttachmentPrepareImage(); p != nil {
			out = append(out, p)
		}
	}
	return out
}

func findHashEffects(b *corev1.EffectBatch) []*corev1.AttachmentHashFileEffect {
	var out []*corev1.AttachmentHashFileEffect
	for _, ef := range b.GetEffects() {
		if p := ef.GetAttachmentHashFile(); p != nil {
			out = append(out, p)
		}
	}
	return out
}

func findReadChunkEffects(b *corev1.EffectBatch) []*corev1.AttachmentReadChunkEffect {
	var out []*corev1.AttachmentReadChunkEffect
	for _, ef := range b.GetEffects() {
		if p := ef.GetAttachmentReadChunk(); p != nil {
			out = append(out, p)
		}
	}
	return out
}

func findDraftReadEffects(b *corev1.EffectBatch) []*corev1.DraftStorageReadEffect {
	var out []*corev1.DraftStorageReadEffect
	for _, ef := range b.GetEffects() {
		if r := ef.GetDraftStorageRead(); r != nil {
			out = append(out, r)
		}
	}
	return out
}

func findDraftWriteEffects(b *corev1.EffectBatch) []*corev1.DraftStorageWriteEffect {
	var out []*corev1.DraftStorageWriteEffect
	for _, ef := range b.GetEffects() {
		if p := ef.GetDraftStorageWrite(); p != nil {
			out = append(out, p)
		}
	}
	return out
}

func findDraftDeleteEffects(b *corev1.EffectBatch) []*corev1.DraftStorageDeleteEffect {
	var out []*corev1.DraftStorageDeleteEffect
	for _, ef := range b.GetEffects() {
		if p := ef.GetDraftStorageDelete(); p != nil {
			out = append(out, p)
		}
	}
	return out
}

func findReleaseLocalEffects(b *corev1.EffectBatch) []*corev1.AttachmentReleaseLocalEffect {
	var out []*corev1.AttachmentReleaseLocalEffect
	for _, ef := range b.GetEffects() {
		if p := ef.GetAttachmentReleaseLocal(); p != nil {
			out = append(out, p)
		}
	}
	return out
}

func findDispatchedCmds(b *corev1.EffectBatch) []attDispatchedCmd {
	var out []attDispatchedCmd
	for _, ef := range b.GetEffects() {
		if dc := ef.GetDispatchCommand(); dc != nil {
			var cmd v1.Command
			if err := cmd.Unmarshal(dc.GetCommandBytes()); err == nil {
				out = append(out, attDispatchedCmd{dc: dc, cmd: &cmd})
			}
		}
	}
	return out
}

func findTimers(b *corev1.EffectBatch, kind corev1.TimerKind) []*corev1.ScheduleTimerEffect {
	var out []*corev1.ScheduleTimerEffect
	for _, ef := range b.GetEffects() {
		if st := ef.GetScheduleTimer(); st != nil && st.GetKind() == kind {
			out = append(out, st)
		}
	}
	return out
}

func (h *attHarness) settleCmdOK(dc *corev1.DispatchCommandEffect, res *v1.CommandResult) *corev1.EffectBatch {
	res.Outcome = "executed"
	res.Reason = "executed"
	res.ResultAvailable = true
	return h.dispatch(&corev1.Event{
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         dc.GetRequestId(),
				AttemptId:         dc.GetAttemptId(),
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				Signed:            true,
				HashesMatch:       true,
				AttemptBound:      true,
				OuterMatchesInner: true,
				OuterOutcome:      "executed",
				OuterReason:       "executed",
				Result:            res,
			},
		},
	})
}

func (h *attHarness) settleCmdDaemonErr(dc *corev1.DispatchCommandEffect, code string) *corev1.EffectBatch {
	return h.dispatch(&corev1.Event{
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:         dc.GetRequestId(),
				AttemptId:         dc.GetAttemptId(),
				Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				Signed:            true,
				HashesMatch:       true,
				AttemptBound:      true,
				OuterMatchesInner: true,
				OuterOutcome:      "executed",
				OuterReason:       "executed",
				Result: &v1.CommandResult{
					Outcome:      "executed",
					Reason:       "executed",
					ErrorCode:    code,
					ErrorMessage: code,
				},
			},
		},
	})
}

func (h *attHarness) settleCmdTransportDrop(dc *corev1.DispatchCommandEffect, code string) *corev1.EffectBatch {
	return h.dispatch(&corev1.Event{
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:     dc.GetRequestId(),
				AttemptId:     dc.GetAttemptId(),
				Source:        corev1.OutcomeSource_OUTCOME_SOURCE_TRANSPORT,
				NotForwarded:  true,
				TransportCode: code,
			},
		},
	})
}

func beginOK(tid string, persisted int64, maxChunk int32, expiresMs int64) *v1.CommandResult {
	return &v1.CommandResult{
		Kind: &v1.CommandResult_BeginImageUpload{
			BeginImageUpload: &v1.BeginImageUploadResponse{
				Status: &v1.ImageUploadStatus{
					TransferId:     tid,
					State:          v1.ImageUploadState_IMAGE_UPLOAD_STATE_RECEIVING,
					PersistedBytes: persisted,
					MaxChunkBytes:  maxChunk,
					ExpiresMs:      expiresMs,
				},
			},
		},
	}
}

func beginCompleteOK(tid, imgID, fileID string, expiresMs int64) *v1.CommandResult {
	return &v1.CommandResult{
		Kind: &v1.CommandResult_BeginImageUpload{
			BeginImageUpload: &v1.BeginImageUploadResponse{
				Status: &v1.ImageUploadStatus{
					TransferId: tid,
					State:      v1.ImageUploadState_IMAGE_UPLOAD_STATE_COMPLETE,
					ImageId:    imgID,
					FileId:     fileID,
					ExpiresMs:  expiresMs,
				},
			},
		},
	}
}

func putOK(tid string, persisted int64) *v1.CommandResult {
	return &v1.CommandResult{
		Kind: &v1.CommandResult_PutImageChunk{
			PutImageChunk: &v1.PutImageChunkResponse{
				Status: &v1.ImageUploadStatus{
					TransferId:     tid,
					State:          v1.ImageUploadState_IMAGE_UPLOAD_STATE_RECEIVING,
					PersistedBytes: persisted,
				},
			},
		},
	}
}

func statusOK(tid string, persisted int64, maxChunk int32) *v1.CommandResult {
	return &v1.CommandResult{
		Kind: &v1.CommandResult_GetImageUploadStatus{
			GetImageUploadStatus: &v1.GetImageUploadStatusResponse{
				Status: &v1.ImageUploadStatus{
					TransferId:     tid,
					State:          v1.ImageUploadState_IMAGE_UPLOAD_STATE_RECEIVING,
					PersistedBytes: persisted,
					MaxChunkBytes:  maxChunk,
				},
			},
		},
	}
}

func completeStatusOK(tid string, state v1.ImageUploadState, imgID, fileID string, persisted, expiresMs int64) *v1.CommandResult {
	return &v1.CommandResult{
		Kind: &v1.CommandResult_CompleteImageUpload{
			CompleteImageUpload: &v1.CompleteImageUploadResponse{
				Status: &v1.ImageUploadStatus{
					TransferId:     tid,
					State:          state,
					PersistedBytes: persisted,
					ImageId:        imgID,
					FileId:         fileID,
					ExpiresMs:      expiresMs,
				},
			},
		},
	}
}

// driveImageUploadReady drives a single image item from prepare through chunk upload to READY.
func (h *attHarness) driveImageUploadReady(
	eff *corev1.EffectBatch,
	prepRef, thumbRef string,
	sizeBytes int64,
	hash []byte,
	remoteID string,
	expiresMs int64,
) {
	h.t.Helper()
	preps := findPrepareEffects(eff)
	if len(preps) != 1 {
		h.t.Fatalf("expected 1 prepare effect, got %d", len(preps))
	}
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentPrepareImageResult{
			AttachmentPrepareImageResult: &corev1.AttachmentPrepareImageResultEvent{
				OpId:        preps[0].OpId,
				Ok:          true,
				PreparedRef: prepRef,
				ThumbRef:    thumbRef,
				Format:      v1.ImageFormat_IMAGE_FORMAT_JPEG,
				SizeBytes:   sizeBytes,
				Width:       800,
				Height:      600,
				Sha256:      hash,
			},
		},
	})
	for _, dw := range findDraftWriteEffects(eff) {
		if dw.Bucket == corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT {
			h.dispatch(&corev1.Event{
				Event: &corev1.Event_DraftStorageWriteResult{
					DraftStorageWriteResult: &corev1.DraftStorageWriteResultEvent{OpId: dw.OpId, Ok: true},
				},
			})
		}
	}
	cmds := findDispatchedCmds(eff)
	if len(cmds) != 1 || cmds[0].cmd.GetBeginImageUpload() == nil {
		h.t.Fatalf("expected BeginImageUpload command, got %d cmds", len(cmds))
	}
	begin := cmds[0]
	tid := begin.cmd.GetBeginImageUpload().TransferId
	eff = h.settleCmdOK(begin.dc, beginOK(tid, 0, 4, expiresMs))
	var offset int64
	for offset < sizeBytes {
		reads := findReadChunkEffects(eff)
		if len(reads) != 1 {
			h.t.Fatalf("offset=%d: expected 1 ReadChunk effect, got %d", offset, len(reads))
		}
		chunkLen := min(int64(reads[0].Length), sizeBytes-offset)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_AttachmentReadChunkResult{
				AttachmentReadChunkResult: &corev1.AttachmentReadChunkResultEvent{
					OpId:   reads[0].OpId,
					Ok:     true,
					Offset: offset,
					Data:   bytes.Repeat([]byte{'x'}, int(chunkLen)),
				},
			},
		})
		putCmds := findDispatchedCmds(eff)
		if len(putCmds) != 1 || putCmds[0].cmd.GetPutImageChunk() == nil {
			h.t.Fatalf("offset=%d: expected PutImageChunk, got %d", offset, len(putCmds))
		}
		offset += chunkLen
		eff = h.settleCmdOK(putCmds[0].dc, putOK(tid, offset))
	}
	compCmds := findDispatchedCmds(eff)
	if len(compCmds) != 1 || compCmds[0].cmd.GetCompleteImageUpload() == nil {
		h.t.Fatalf("expected CompleteImageUpload, got %d", len(compCmds))
	}
	doneEff := h.settleCmdOK(compCmds[0].dc, completeStatusOK(tid, v1.ImageUploadState_IMAGE_UPLOAD_STATE_COMPLETE, remoteID, "", sizeBytes, expiresMs))
	for _, dw := range findDraftWriteEffects(doneEff) {
		if dw.Bucket == corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT {
			h.dispatch(&corev1.Event{
				Event: &corev1.Event_DraftStorageWriteResult{
					DraftStorageWriteResult: &corev1.DraftStorageWriteResultEvent{OpId: dw.OpId, Ok: true},
				},
			})
		}
	}
}

func TestAttachments_ImageUpload_ChunkedAndReceivingPoll(t *testing.T) {
	h := newAttHarness(t, false, false)
	scope := h.sessionScope()

	eff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{
					pickedImage("photo.jpg", 10, "src-img-1", true),
				},
			},
		},
	})

	preps := findPrepareEffects(eff)
	if len(preps) != 1 || preps[0].SourceRef != "src-img-1" {
		t.Fatalf("expected prepare effect for src-img-1, got %+v", preps)
	}
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentPrepareImageResult{
			AttachmentPrepareImageResult: &corev1.AttachmentPrepareImageResultEvent{
				OpId:        preps[0].OpId,
				Ok:          true,
				PreparedRef: "prep-1",
				ThumbRef:    "thumb-1",
				Format:      v1.ImageFormat_IMAGE_FORMAT_JPEG,
				SizeBytes:   6,
				Width:       640,
				Height:      480,
				Sha256:      attTestHash(0x11),
			},
		},
	})
	beginCmd := findDispatchedCmds(eff)[0]
	if beginCmd.cmd.GetBeginImageUpload() == nil {
		t.Fatalf("expected BeginImageUpload")
	}
	tid := beginCmd.cmd.GetBeginImageUpload().TransferId

	eff = h.settleCmdOK(beginCmd.dc, beginOK(tid, 0, 4, h.nowMs+600_000))
	// Chunk 1: 4 bytes.
	read1 := findReadChunkEffects(eff)[0]
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentReadChunkResult{
			AttachmentReadChunkResult: &corev1.AttachmentReadChunkResultEvent{
				OpId: read1.OpId, Ok: true, Offset: 0, Data: []byte("abcd"),
			},
		},
	})
	put1 := findDispatchedCmds(eff)[0]
	eff = h.settleCmdOK(put1.dc, putOK(tid, 4))
	dv := h.draftView(scope)
	if len(dv.Images) != 1 || dv.Images[0].ProgressPercent <= 0 {
		t.Fatalf("expected progress > 0 after chunk 1, got %+v", dv.Images)
	}

	// Chunk 2: remaining 2 bytes.
	read2 := findReadChunkEffects(eff)[0]
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentReadChunkResult{
			AttachmentReadChunkResult: &corev1.AttachmentReadChunkResultEvent{
				OpId: read2.OpId, Ok: true, Offset: 4, Data: []byte("ef"),
			},
		},
	})
	put2 := findDispatchedCmds(eff)[0]
	eff = h.settleCmdOK(put2.dc, putOK(tid, 6))

	// CompleteImageUpload returns RECEIVING -> schedules 250ms poll timer.
	comp1 := findDispatchedCmds(eff)[0]
	eff = h.settleCmdOK(comp1.dc, completeStatusOK(tid, v1.ImageUploadState_IMAGE_UPLOAD_STATE_RECEIVING, "", "", 6, 0))
	polls := findTimers(eff, corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_POLL)
	if len(polls) != 1 || polls[0].DelayMs != 250 {
		t.Fatalf("expected 250ms poll timer, got %+v", polls)
	}

	// Fire poll #1 -> second CompleteImageUpload -> returns RECEIVING again -> 500ms timer.
	h.advance(250)
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_TimerFired{
			TimerFired: &corev1.TimerFiredEvent{
				TimerId: polls[0].TimerId,
				Kind:    corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_POLL,
				Key:     polls[0].Key,
			},
		},
	})
	comp2 := findDispatchedCmds(eff)[0]
	eff = h.settleCmdOK(comp2.dc, completeStatusOK(tid, v1.ImageUploadState_IMAGE_UPLOAD_STATE_RECEIVING, "", "", 6, 0))
	polls2 := findTimers(eff, corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_POLL)
	if len(polls2) != 1 || polls2[0].DelayMs != 500 {
		t.Fatalf("expected 500ms poll timer, got %+v", polls2)
	}

	// Fire poll #2 -> CompleteImageUpload returns COMPLETE!
	h.advance(500)
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_TimerFired{
			TimerFired: &corev1.TimerFiredEvent{
				TimerId: polls2[0].TimerId,
				Kind:    corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_POLL,
				Key:     polls2[0].Key,
			},
		},
	})
	comp3 := findDispatchedCmds(eff)[0]
	eff = h.settleCmdOK(comp3.dc, completeStatusOK(tid, v1.ImageUploadState_IMAGE_UPLOAD_STATE_COMPLETE, "img-remote-1", "", 6, h.nowMs+600_000))
	expTimers := findTimers(eff, corev1.TimerKind_TIMER_KIND_DRAFT_EXPIRY)
	if len(expTimers) != 1 {
		t.Fatalf("expected DRAFT_EXPIRY timer scheduled on READY, got %+v", expTimers)
	}
	dv = h.draftView(scope)
	if !dv.Ready || !dv.CanSend || dv.Images[0].State != corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_READY {
		t.Fatalf("expected ready draft, got ready=%v canSend=%v state=%v", dv.Ready, dv.CanSend, dv.Images[0].State)
	}
}

func TestAttachments_GlobalPrepareQueueAndFileSerialGate(t *testing.T) {
	h := newAttHarness(t, false, false)
	scope := h.sessionScope()

	eff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{
					pickedImage("a.jpg", 4, "src-i1", true),
					pickedImage("b.jpg", 4, "src-i2", true),
					pickedFile("f1.pdf", "application/pdf", 4, "src-f1"),
					pickedFile("f2.pdf", "application/pdf", 4, "src-f2"),
				},
			},
		},
	})

	preps := findPrepareEffects(eff)
	hashes := findHashEffects(eff)
	if len(preps) != 1 || len(hashes) != 0 {
		t.Fatalf("expected strictly 1 prepare effect and 0 hash effects, got %d / %d", len(preps), len(hashes))
	}

	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentPrepareImageResult{
			AttachmentPrepareImageResult: &corev1.AttachmentPrepareImageResultEvent{
				OpId: preps[0].OpId, Ok: true, PreparedRef: "prep-i1", ThumbRef: "thumb-i1",
				Format: v1.ImageFormat_IMAGE_FORMAT_JPEG, SizeBytes: 4, Width: 100, Height: 100, Sha256: attTestHash(1),
			},
		},
	})
	if len(findDispatchedCmds(eff)) != 1 || len(findPrepareEffects(eff)) != 1 {
		t.Fatalf("expected image 1 begin + image 2 prepare, got cmds=%d preps=%d", len(findDispatchedCmds(eff)), len(findPrepareEffects(eff)))
	}
	prep2 := findPrepareEffects(eff)[0]

	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentPrepareImageResult{
			AttachmentPrepareImageResult: &corev1.AttachmentPrepareImageResultEvent{
				OpId: prep2.OpId, Ok: true, PreparedRef: "prep-i2", ThumbRef: "thumb-i2",
				Format: v1.ImageFormat_IMAGE_FORMAT_JPEG, SizeBytes: 4, Width: 100, Height: 100, Sha256: attTestHash(2),
			},
		},
	})
	if len(findDispatchedCmds(eff)) != 1 || len(findHashEffects(eff)) != 1 {
		t.Fatalf("expected image 2 concurrent begin + file 1 hash, got cmds=%d hashes=%d", len(findDispatchedCmds(eff)), len(findHashEffects(eff)))
	}
	hash1 := findHashEffects(eff)[0]

	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentHashFileResult{
			AttachmentHashFileResult: &corev1.AttachmentHashFileResultEvent{
				OpId: hash1.OpId, Ok: true, SizeBytes: 4, Sha256: attTestHash(3),
			},
		},
	})
	cmdsF1 := findDispatchedCmds(eff)
	hashes2 := findHashEffects(eff)
	if len(cmdsF1) != 1 || cmdsF1[0].cmd.GetBeginImageUpload() == nil || len(hashes2) != 1 {
		t.Fatalf("expected file 1 begin + file 2 hash, got cmds=%d hashes=%d", len(cmdsF1), len(hashes2))
	}

	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentHashFileResult{
			AttachmentHashFileResult: &corev1.AttachmentHashFileResultEvent{
				OpId: hashes2[0].OpId, Ok: true, SizeBytes: 4, Sha256: attTestHash(4),
			},
		},
	})
	if len(findDispatchedCmds(eff)) != 0 {
		t.Fatalf("expected file 2 queued behind file 1 serial gate, but got %d commands", len(findDispatchedCmds(eff)))
	}

	// Complete file 1 -> file 2 immediately dequeues and starts BeginImageUpload!
	eff = h.settleCmdOK(cmdsF1[0].dc, beginCompleteOK(cmdsF1[0].cmd.GetBeginImageUpload().TransferId, "", "file-remote-1", h.nowMs+600_000))
	cmdsF2 := findDispatchedCmds(eff)
	if len(cmdsF2) != 1 || cmdsF2[0].cmd.GetBeginImageUpload() == nil {
		t.Fatalf("expected file 2 BeginImageUpload after file 1 completed, got %d cmds", len(cmdsF2))
	}
}

func TestAttachments_MonotonicProgressGuard_RetryReset_AndAutoRestart(t *testing.T) {
	h := newAttHarness(t, false, false)
	scope := h.sessionScope()

	eff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{
					pickedFile("doc.txt", "text/plain", 8, "src-doc"),
				},
			},
		},
	})
	hashOp := findHashEffects(eff)[0]
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentHashFileResult{
			AttachmentHashFileResult: &corev1.AttachmentHashFileResultEvent{
				OpId: hashOp.OpId, Ok: true, SizeBytes: 8, Sha256: attTestHash(0x22),
			},
		},
	})
	begin1 := findDispatchedCmds(eff)[0]
	tid1 := begin1.cmd.GetBeginImageUpload().TransferId
	eff = h.settleCmdOK(begin1.dc, beginOK(tid1, 0, 4, 0))

	// Chunk 1: transient transport error -> status probe -> retries chunk 1 -> succeeds (resets retry counter to 0)!
	read1 := findReadChunkEffects(eff)[0]
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentReadChunkResult{
			AttachmentReadChunkResult: &corev1.AttachmentReadChunkResultEvent{OpId: read1.OpId, Ok: true, Offset: 0, Data: []byte("1234")},
		},
	})
	put1 := findDispatchedCmds(eff)[0]
	eff = h.settleCmdTransportDrop(put1.dc, "timeout")
	probe1 := findDispatchedCmds(eff)[0]
	if probe1.cmd.GetGetImageUploadStatus() == nil || probe1.cmd.GetGetImageUploadStatus().TransferId != tid1 {
		t.Fatalf("expected GetImageUploadStatus probe on transient Put failure, got %+v", probe1.cmd)
	}
	eff = h.settleCmdOK(probe1.dc, statusOK(tid1, 0, 4))
	read1Retry := findReadChunkEffects(eff)[0]
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentReadChunkResult{
			AttachmentReadChunkResult: &corev1.AttachmentReadChunkResultEvent{OpId: read1Retry.OpId, Ok: true, Offset: 0, Data: []byte("1234")},
		},
	})
	put1Retry := findDispatchedCmds(eff)[0]
	eff = h.settleCmdOK(put1Retry.dc, putOK(tid1, 4))

	// Chunk 2: daemon returns "not_found" -> triggers 1x auto-restart with FRESH transfer_id!
	read2 := findReadChunkEffects(eff)[0]
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentReadChunkResult{
			AttachmentReadChunkResult: &corev1.AttachmentReadChunkResultEvent{OpId: read2.OpId, Ok: true, Offset: 4, Data: []byte("5678")},
		},
	})
	put2 := findDispatchedCmds(eff)[0]
	eff = h.settleCmdDaemonErr(put2.dc, "not_found")
	var begin2 *attDispatchedCmd
	for _, sc := range findDispatchedCmds(eff) {
		if sc.cmd.GetBeginImageUpload() != nil {
			cp := sc
			begin2 = &cp
		}
	}
	if begin2 == nil {
		t.Fatalf("expected auto-restart BeginImageUpload on not_found")
	}
	tid2 := begin2.cmd.GetBeginImageUpload().TransferId
	if tid2 == tid1 {
		t.Fatalf("expected fresh transfer_id on auto-restart, got same %s", tid2)
	}

	// Begin2 succeeds, then PutImageChunk returns non-advancing persisted_bytes == 0 -> monotonic guard fails item!
	eff = h.settleCmdOK(begin2.dc, beginOK(tid2, 0, 4, 0))
	readRestart := findReadChunkEffects(eff)[0]
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentReadChunkResult{
			AttachmentReadChunkResult: &corev1.AttachmentReadChunkResultEvent{OpId: readRestart.OpId, Ok: true, Offset: 0, Data: []byte("1234")},
		},
	})
	putRestart := findDispatchedCmds(eff)[0]
	h.settleCmdOK(putRestart.dc, putOK(tid2, 0)) // did NOT advance!
	dv := h.draftView(scope)
	if dv.Files[0].State != corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_FAILED {
		t.Fatalf("expected FAILED on non-advancing persisted_bytes, got %v", dv.Files[0].State)
	}
	if dv.Files[0].FileErrorText.GetKey() != text.KeyErrorsUploadMadeNoProgress {
		t.Fatalf("expected KeyErrorsUploadMadeNoProgress, got %+v", dv.Files[0].FileErrorText)
	}
}

func TestAttachments_PauseDisconnectAnd30sReconnectResume(t *testing.T) {
	h := newAttHarness(t, false, false)
	scope := h.sessionScope()

	eff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{
					pickedFile("big.bin", "application/octet-stream", 12, "src-big"),
				},
			},
		},
	})
	hashOp := findHashEffects(eff)[0]
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentHashFileResult{
			AttachmentHashFileResult: &corev1.AttachmentHashFileResultEvent{
				OpId: hashOp.OpId, Ok: true, SizeBytes: 12, Sha256: attTestHash(0x33),
			},
		},
	})
	beginCmd := findDispatchedCmds(eff)[0]
	origTID := beginCmd.cmd.GetBeginImageUpload().TransferId
	eff = h.settleCmdOK(beginCmd.dc, beginOK(origTID, 0, 4, 0))
	// Upload chunk 1 (4 bytes).
	r1 := findReadChunkEffects(eff)[0]
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentReadChunkResult{
			AttachmentReadChunkResult: &corev1.AttachmentReadChunkResultEvent{OpId: r1.OpId, Ok: true, Offset: 0, Data: []byte("aaaa")},
		},
	})
	p1 := findDispatchedCmds(eff)[0]
	eff = h.settleCmdOK(p1.dc, putOK(origTID, 4))
	// Pause while chunk 2 read is in-flight!
	itemID := h.draftView(scope).Files[0].ItemId
	pauseEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentPauseUpload{
			AttachmentPauseUpload: &corev1.AttachmentPauseUploadAction{Scope: scope, ItemId: itemID},
		},
	})
	for _, sc := range findDispatchedCmds(pauseEff) {
		if sc.cmd.GetAbortImageUpload() != nil {
			t.Fatalf("pause must NOT emit AbortImageUpload")
		}
	}
	dv := h.draftView(scope)
	if dv.Files[0].State != corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PAUSED || !dv.Files[0].CanResume {
		t.Fatalf("expected PAUSED with CanResume=true, got state=%v canResume=%v", dv.Files[0].State, dv.Files[0].CanResume)
	}

	// Resume -> probes GetImageUploadStatus with SAME transfer_id!
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentResumeUpload{
			AttachmentResumeUpload: &corev1.AttachmentResumeUploadAction{Scope: scope, ItemId: itemID},
		},
	})
	statusCmd := findDispatchedCmds(eff)[0]
	if statusCmd.cmd.GetGetImageUploadStatus() == nil || statusCmd.cmd.GetGetImageUploadStatus().TransferId != origTID {
		t.Fatalf("expected GetImageUploadStatus with same transfer_id %s, got %+v", origTID, statusCmd.cmd)
	}
	eff = h.settleCmdOK(statusCmd.dc, statusOK(origTID, 4, 4))
	r2 := findReadChunkEffects(eff)[0]
	if r2.Offset != 4 {
		t.Fatalf("expected resumed ReadChunk at offset 4, got %d", r2.Offset)
	}
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentReadChunkResult{
			AttachmentReadChunkResult: &corev1.AttachmentReadChunkResultEvent{OpId: r2.OpId, Ok: true, Offset: 4, Data: []byte("bbbb")},
		},
	})
	p2 := findDispatchedCmds(eff)[0]

	// Machine goes offline + transient disconnect during chunk 2 -> enters WAITING_RECONNECT with 30s timer!
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_MachineUpdated{
					MachineUpdated: &corev1.PlainMachine{
						MachineId: h.machineA,
						Online:    false,
						MetaRev:   2,
						Meta: &v1.MachineMeta{
							Hostname: "Alpha", Home: "/Users/me", ProtocolVersion: 17,
							Capabilities: &v1.MachineCapabilities{ImageUpload: true, FileUpload: true},
						},
					},
				},
			},
		},
	})
	eff = h.settleCmdTransportDrop(p2.dc, "machine_offline")
	dv = h.draftView(scope)
	if dv.Files[0].State != corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_WAITING_RECONNECT {
		t.Fatalf("expected WAITING_RECONNECT, got state=%v", dv.Files[0].State)
	}
	recTimers := findTimers(eff, corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_RECONNECT)
	if len(recTimers) != 1 || recTimers[0].DelayMs != 30_000 {
		t.Fatalf("expected 30s reconnect timer scheduled, got %+v", recTimers)
	}

	// Machine comes back online -> reconcileAttachments immediately resumes via GetImageUploadStatus on SAME transfer_id!
	h.advance(1000)
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_MachineUpdated{
					MachineUpdated: &corev1.PlainMachine{
						MachineId: h.machineA,
						Online:    true,
						MetaRev:   3,
						Meta: &v1.MachineMeta{
							Hostname: "Alpha", Home: "/Users/me", ProtocolVersion: 17,
							Capabilities: &v1.MachineCapabilities{ImageUpload: true, FileUpload: true},
						},
					},
				},
			},
		},
	})
	probeCmd := findDispatchedCmds(eff)[0]
	if probeCmd.cmd.GetGetImageUploadStatus() == nil || probeCmd.cmd.GetGetImageUploadStatus().TransferId != origTID {
		t.Fatalf("expected reconnect GetImageUploadStatus with same transfer_id %s", origTID)
	}

	// SYNC_LIFECYCLE_KIND_STOP pauses active upload without aborting!
	stopEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_STOP},
		},
	})
	for _, sc := range findDispatchedCmds(stopEff) {
		if sc.cmd.GetAbortImageUpload() != nil {
			t.Fatalf("SYNC_LIFECYCLE_KIND_STOP must NOT abort uploads")
		}
	}
	if h.draftView(scope).Files[0].State != corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_PAUSED {
		t.Fatalf("expected PAUSED after SYNC_LIFECYCLE_KIND_STOP")
	}
}

func TestAttachments_MachineSwitchRapidABA_AndCapabilityRecheck(t *testing.T) {
	h := newAttHarness(t, false, false)
	scope := h.newSessionScope()

	h.dispatch(&corev1.Event{
		Event: &corev1.Event_NewSessionOpen{NewSessionOpen: &corev1.NewSessionOpenAction{ProjectKey: h.project}},
	})
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_NewSessionSelectMachine{NewSessionSelectMachine: &corev1.NewSessionSelectMachineAction{MachineId: h.machineA}},
	})
	eff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{
					pickedImage("pic.jpg", 4, "src-pic", true),
					pickedFile("notes.txt", "text/plain", 4, "src-txt"),
				},
			},
		},
	})
	prep := findPrepareEffects(eff)[0]
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentPrepareImageResult{
			AttachmentPrepareImageResult: &corev1.AttachmentPrepareImageResultEvent{
				OpId: prep.OpId, Ok: true, PreparedRef: "prep-pic", ThumbRef: "thumb-pic",
				Format: v1.ImageFormat_IMAGE_FORMAT_JPEG, SizeBytes: 4, Width: 100, Height: 100, Sha256: attTestHash(0x44),
			},
		},
	})
	beginOnA := findDispatchedCmds(eff)[0]
	tidA := beginOnA.cmd.GetBeginImageUpload().TransferId
	hashOp := findHashEffects(eff)[0]
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentHashFileResult{
			AttachmentHashFileResult: &corev1.AttachmentHashFileResultEvent{
				OpId: hashOp.OpId, Ok: true, SizeBytes: 4, Sha256: attTestHash(0x45),
			},
		},
	})

	// Switch to machineB (no FileUpload capability):
	effB := h.dispatch(&corev1.Event{
		Event: &corev1.Event_NewSessionSelectMachine{NewSessionSelectMachine: &corev1.NewSessionSelectMachineAction{MachineId: h.machineB}},
	})
	dvB := h.draftView(scope)
	if dvB.Files[0].State != corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_FAILED {
		t.Fatalf("expected file failed on machineB without FileUpload, got %v", dvB.Files[0].State)
	}
	if !h.core.m.state.NewSessions[h.project].AttachmentsMoved {
		t.Fatalf("expected NewSession.AttachmentsMoved == true after machine switch")
	}
	var sawBeginB, sawAbortA bool
	for _, sc := range findDispatchedCmds(effB) {
		if sc.cmd.GetBeginImageUpload() != nil && sc.dc.MachineId == h.machineB {
			sawBeginB = true
		}
		if ab := sc.cmd.GetAbortImageUpload(); ab != nil && ab.TransferId == tidA && sc.dc.MachineId == h.machineA {
			sawAbortA = true
		}
	}
	if !sawBeginB || !sawAbortA {
		t.Fatalf("expected BeginImageUpload on machineB and AbortImageUpload for old machineA transfer; sawBeginB=%v sawAbortA=%v", sawBeginB, sawAbortA)
	}

	// Rapidly switch BACK to machineA (A -> B -> A)!
	effA2 := h.dispatch(&corev1.Event{
		Event: &corev1.Event_NewSessionSelectMachine{NewSessionSelectMachine: &corev1.NewSessionSelectMachineAction{MachineId: h.machineA}},
	})
	var sawBeginA2 bool
	for _, sc := range findDispatchedCmds(effA2) {
		if sc.cmd.GetBeginImageUpload() != nil && sc.dc.MachineId == h.machineA {
			sawBeginA2 = true
		}
	}
	if !sawBeginA2 {
		t.Fatalf("expected fresh BeginImageUpload on machineA after rapid A->B->A")
	}

	// Late stale BeginImageUpload outcome from FIRST machineA upload arrives -> MUST be ignored without corrupting A2!
	h.settleCmdOK(beginOnA.dc, beginOK(tidA, 0, 4, 0))
	if dv := h.draftView(scope); len(dv.Images) != 1 || dv.Images[0].State == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_READY {
		t.Fatalf("stale A outcome must not corrupt active A2 upload: %+v", dv.Images)
	}
}

func TestAttachments_OriginalToggle_3AGaps_AndDraftImageViewer(t *testing.T) {
	h := newAttHarness(t, false, false)
	scope := h.sessionScope()

	eff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{
					pickedImage("1.jpg", 2*1024*1024, "src-1", true),
					pickedImage("2.jpg", 1*1024*1024, "src-2", false),
				},
			},
		},
	})
	dv := h.draftView(scope)
	if dv.CanToggleOriginal {
		t.Fatalf("expected CanToggleOriginal=false when second image lacks original source")
	}
	if dv.OriginalToggleDisabledReason.GetKey() != text.KeyImageOriginalMissing {
		t.Fatalf("expected image.original_missing reason, got %+v", dv.OriginalToggleDisabledReason)
	}

	// Test DraftImageViewer open, navigate, close + 3A gap #2 position_text ("1 / 2" -> "2 / 2")!
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftImageViewerOpen{
			DraftImageViewerOpen: &corev1.DraftImageViewerOpenAction{Scope: scope, Index: 0},
		},
	})
	dv = h.draftView(scope)
	if !dv.ImageViewer.Open || dv.ImageViewer.PositionText.GetLiteral() != "1 / 2" || !dv.ImageViewer.HasNext || dv.ImageViewer.HasPrev {
		t.Fatalf("unexpected viewer state at index 0: %+v", dv.ImageViewer)
	}
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftImageViewerNavigate{
			DraftImageViewerNavigate: &corev1.DraftImageViewerNavigateAction{Scope: scope, Delta: 1},
		},
	})
	dv = h.draftView(scope)
	if dv.ImageViewer.CurrentIndex != 1 || dv.ImageViewer.PositionText.GetLiteral() != "2 / 2" || dv.ImageViewer.HasNext || !dv.ImageViewer.HasPrev {
		t.Fatalf("unexpected viewer state at index 1: %+v", dv.ImageViewer)
	}
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftImageViewerClose{
			DraftImageViewerClose: &corev1.DraftImageViewerCloseAction{Scope: scope},
		},
	})
	if h.draftView(scope).ImageViewer.Open {
		t.Fatalf("expected viewer closed")
	}

	// Remove image 2 -> now CanToggleOriginal=true! Drive image 1 to ready, then toggle original=true.
	img2ID := dv.Images[1].ItemId
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftRemoveItem{
			DraftRemoveItem: &corev1.DraftRemoveItemAction{Scope: scope, ItemId: img2ID},
		},
	})
	h.driveImageUploadReady(eff, "prep-comp-1", "thumb-1", 4, attTestHash(0x55), "img-comp-1", h.nowMs+600_000)

	dv = h.draftView(scope)
	if !dv.CanToggleOriginal || !dv.Ready {
		t.Fatalf("expected CanToggleOriginal=true and Ready=true")
	}

	// Toggle original=true -> invalidates compressed image and emits prepare with Original=true!
	effOrig := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftSetOriginal{
			DraftSetOriginal: &corev1.DraftSetOriginalAction{Scope: scope, Original: true},
		},
	})
	prepsOrig := findPrepareEffects(effOrig)
	if len(prepsOrig) != 1 || !prepsOrig[0].Original {
		t.Fatalf("expected re-prepare with Original=true, got %+v", prepsOrig)
	}
	// If original prepare fails, old compressed bytes MUST NOT be sent!
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentPrepareImageResult{
			AttachmentPrepareImageResult: &corev1.AttachmentPrepareImageResultEvent{
				OpId:      prepsOrig[0].OpId,
				Ok:        false,
				ErrorKind: corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_ORIGINAL_TOO_LARGE,
			},
		},
	})
	dv = h.draftView(scope)
	if dv.CanSend || dv.Ready || dv.Images[0].State != corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_FAILED || dv.Images[0].CanRetry {
		t.Fatalf("old compressed bytes must not remain sendable after original prepare failed: canSend=%v state=%v canRetry=%v", dv.CanSend, dv.Images[0].State, dv.Images[0].CanRetry)
	}

	// Turn original off (recovery path for ORIGINAL_TOO_LARGE), then back on with 2 MiB size -> check 3A gap #1 original_total_size_text!
	effComp2 := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftSetOriginal{
			DraftSetOriginal: &corev1.DraftSetOriginalAction{Scope: scope, Original: false},
		},
	})
	prepComp2 := findPrepareEffects(effComp2)[0]
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentPrepareImageResult{
			AttachmentPrepareImageResult: &corev1.AttachmentPrepareImageResultEvent{
				OpId: prepComp2.OpId, Ok: true, PreparedRef: "prep-comp-2", ThumbRef: "thumb-1",
				Format: v1.ImageFormat_IMAGE_FORMAT_JPEG, SizeBytes: 4, Width: 100, Height: 100, Sha256: attTestHash(0x57),
			},
		},
	})
	effOrig2 := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftSetOriginal{
			DraftSetOriginal: &corev1.DraftSetOriginalAction{Scope: scope, Original: true},
		},
	})
	prepRetry := findPrepareEffects(effOrig2)[0]
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentPrepareImageResult{
			AttachmentPrepareImageResult: &corev1.AttachmentPrepareImageResultEvent{
				OpId: prepRetry.OpId, Ok: true, PreparedRef: "prep-orig-1", ThumbRef: "thumb-1",
				Format: v1.ImageFormat_IMAGE_FORMAT_JPEG, SizeBytes: 2 * 1024 * 1024, Width: 2000, Height: 1500, Sha256: attTestHash(0x56),
			},
		},
	})
	dv = h.draftView(scope)
	if dv.OriginalTotalSizeText == nil || dv.OriginalTotalSizeText.GetKey() != text.KeyImageTotalSize {
		t.Fatalf("expected original_total_size_text key image.total_size, got %+v", dv.OriginalTotalSizeText)
	}
}

func TestAttachments_ReservedSlots_AndDraftExpiry(t *testing.T) {
	h := newAttHarness(t, false, false)
	scope := h.sessionScope()

	h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftSetText{DraftSetText: &corev1.DraftSetTextAction{Scope: scope, Text: "hello"}},
	})
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftReserveSlots{
			DraftReserveSlots: &corev1.DraftReserveSlotsAction{Scope: scope, ReservationId: "pick-1", ImageSlots: 4, FileSlots: 4},
		},
	})
	dv := h.draftView(scope)
	if !dv.HasPendingPicks || dv.LibraryRoom != 4 || dv.CanSend {
		t.Fatalf("expected HasPendingPicks=true, LibraryRoom=4, CanSend=false; got %v %d %v", dv.HasPendingPicks, dv.LibraryRoom, dv.CanSend)
	}

	eff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope:         scope,
				ReservationId: "pick-1",
				Items: []*corev1.DraftPickedItem{
					pickedImage("p.jpg", 4, "src-exp", true),
				},
			},
		},
	})
	dv = h.draftView(scope)
	if dv.HasPendingPicks || dv.ReservedImages != 0 {
		t.Fatalf("expected reservation released atomically, got pending=%v reserved=%d", dv.HasPendingPicks, dv.ReservedImages)
	}

	expAt := h.nowMs + 5000
	h.driveImageUploadReady(eff, "prep-exp", "thumb-exp", 4, attTestHash(0x66), "img-exp", expAt)
	if !h.draftView(scope).CanSend {
		t.Fatalf("expected CanSend=true before expiry")
	}

	// Advance past expiry and attempt DraftSendSession -> pre-send expiry check blocks send and triggers re-upload!
	h.advance(6000)
	sendEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftSendSession{
			DraftSendSession: &corev1.DraftSendSessionAction{SessionId: h.sessionID, Mode: v1.SendMode_SEND_MODE_STEER},
		},
	})
	var sawSend, sawReBegin bool
	for _, sc := range findDispatchedCmds(sendEff) {
		if sc.cmd.GetSend() != nil {
			sawSend = true
		}
		if sc.cmd.GetBeginImageUpload() != nil {
			sawReBegin = true
		}
	}
	if sawSend || !sawReBegin {
		t.Fatalf("expired attachment must block SendRequest and trigger BeginImageUpload re-upload; sawSend=%v sawReBegin=%v", sawSend, sawReBegin)
	}
}

func TestAttachments_SendPersistOutbox_CancelPutBack_AndWhenKeptBarrier(t *testing.T) {
	h := newAttHarness(t, true, true)
	scope := h.sessionScope()

	// Open draft and complete initial empty read.
	openEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftOpen{DraftOpen: &corev1.DraftOpenAction{Scope: scope}},
	})
	var readOp uint64
	for _, ef := range openEff.GetEffects() {
		if rd := ef.GetDraftStorageRead(); rd != nil {
			readOp = rd.OpId
		}
	}
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftStorageReadResult{
			DraftStorageReadResult: &corev1.DraftStorageReadResultEvent{OpId: readOp, Found: false},
		},
	})

	// Type text + add 1 ready image.
	w1 := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftSetText{DraftSetText: &corev1.DraftSetTextAction{Scope: scope, Text: "first message"}},
	})
	for _, dw := range findDraftWriteEffects(w1) {
		h.dispatch(&corev1.Event{
			Event: &corev1.Event_DraftStorageWriteResult{DraftStorageWriteResult: &corev1.DraftStorageWriteResultEvent{OpId: dw.OpId, Ok: true}},
		})
	}
	addEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{
					pickedImage("pic.jpg", 4, "src-send-1", true),
				},
			},
		},
	})
	for _, dw := range findDraftWriteEffects(addEff) {
		h.dispatch(&corev1.Event{
			Event: &corev1.Event_DraftStorageWriteResult{DraftStorageWriteResult: &corev1.DraftStorageWriteResultEvent{OpId: dw.OpId, Ok: true}},
		})
	}
	h.driveImageUploadReady(addEff, "prep-send-1", "thumb-send-1", 4, attTestHash(0x77), "img-remote-send-1", h.nowMs+600_000)

	// Send session message -> MUST emit DraftStorageWrite(OUTBOX) BEFORE detaching draft or submitting SendRequest!
	sendEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftSendSession{
			DraftSendSession: &corev1.DraftSendSessionAction{SessionId: h.sessionID, Mode: v1.SendMode_SEND_MODE_STEER},
		},
	})
	outWrites := findDraftWriteEffects(sendEff)
	if len(outWrites) != 1 || outWrites[0].Bucket != corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX {
		t.Fatalf("expected 1 OUTBOX DraftStorageWrite before send, got %+v", outWrites)
	}
	if len(findDispatchedCmds(sendEff)) != 0 {
		t.Fatalf("must NOT submit SendRequest before Outbox write succeeds")
	}

	// Acknowledge Outbox write -> detaches draft WITHOUT aborting daemon transfer, and submits SendRequest!
	commitEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftStorageWriteResult{
			DraftStorageWriteResult: &corev1.DraftStorageWriteResultEvent{OpId: outWrites[0].OpId, Ok: true},
		},
	})
	var sendCmd *attDispatchedCmd
	for _, sc := range findDispatchedCmds(commitEff) {
		if sc.cmd.GetAbortImageUpload() != nil {
			t.Fatalf("draft detach on send must NOT abort protected daemon transfer")
		}
		if sc.cmd.GetSend() != nil {
			cp := sc
			sendCmd = &cp
		}
	}
	if sendCmd == nil || len(sendCmd.cmd.GetSend().ImageIds) != 1 || sendCmd.cmd.GetSend().ImageIds[0] != "img-remote-send-1" {
		t.Fatalf("expected SendRequest with img-remote-send-1, got %+v", sendCmd)
	}
	reqID := sendCmd.cmd.GetSend().RequestId

	for _, dw := range findDraftWriteEffects(commitEff) {
		if dw.Bucket == corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT {
			h.dispatch(&corev1.Event{
				Event: &corev1.Event_DraftStorageWriteResult{DraftStorageWriteResult: &corev1.DraftStorageWriteResultEvent{OpId: dw.OpId, Ok: true}},
			})
		}
	}

	// Fail the SendRequest so the tray item becomes Failed (restorable without server cancel).
	h.settleCmdDaemonErr(sendCmd.dc, "failed")
	svm := h.sessionView()
	var trayItem *corev1.SessionTrayItemView
	for _, it := range svm.GetComposer().GetPendingTray().GetOrderedItems() {
		if it.RequestId == reqID {
			trayItem = it
		}
	}
	if trayItem == nil || len(trayItem.LocalAttachments) != 1 || trayItem.LocalAttachments[0].ThumbRef != "thumb-send-1" {
		t.Fatalf("expected unified tray item with LocalAttachments, got %+v", trayItem)
	}

	// User types new text in composer, then puts back the failed message!
	wTextEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftSetText{DraftSetText: &corev1.DraftSetTextAction{Scope: scope, Text: "already typed"}},
	})
	for _, dw := range findDraftWriteEffects(wTextEff) {
		h.dispatch(&corev1.Event{
			Event: &corev1.Event_DraftStorageWriteResult{DraftStorageWriteResult: &corev1.DraftStorageWriteResultEvent{OpId: dw.OpId, Ok: true}},
		})
	}

	restoreEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_SessionRestoreTrayItem{
			SessionRestoreTrayItem: &corev1.SessionTrayItemAction{SessionId: h.sessionID, RequestId: reqID},
		},
	})
	var sawRestoreEff bool
	for _, ef := range restoreEff.GetEffects() {
		if rc := ef.GetSessionRestoreComposer(); rc != nil && rc.SessionId == h.sessionID && rc.RequestId == reqID {
			sawRestoreEff = true
		}
	}
	if !sawRestoreEff {
		t.Fatalf("expected SessionRestoreComposerEffect")
	}
	dv := h.draftView(scope)
	if dv.Text != "already typed\nfirst message" {
		t.Fatalf("unexpected merged put-back text: %q", dv.Text)
	}
	if dv.Ready || dv.Images[0].State == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_READY {
		t.Fatalf("put-back attachment must invalidate old remote_id and re-upload, got state=%v", dv.Images[0].State)
	}
	// whenKept barrier: DraftStorageWrite(DRAFT) emitted, DraftStorageDelete(OUTBOX) NOT emitted yet!
	if len(findDraftDeleteEffects(restoreEff)) != 0 {
		t.Fatalf("whenKept barrier violated: must NOT delete OUTBOX before DRAFT write completes!")
	}
	draftWrites := findDraftWriteEffects(restoreEff)
	if len(draftWrites) != 1 || draftWrites[0].Bucket != corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT {
		t.Fatalf("expected 1 DRAFT write on put-back, got %+v", draftWrites)
	}

	// Acknowledge DRAFT write -> NOW DraftStorageDelete(OUTBOX) is emitted!
	barrierEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftStorageWriteResult{
			DraftStorageWriteResult: &corev1.DraftStorageWriteResultEvent{OpId: draftWrites[0].OpId, Ok: true},
		},
	})
	dels := findDraftDeleteEffects(barrierEff)
	if len(dels) != 1 || dels[0].Bucket != corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX {
		t.Fatalf("expected OUTBOX delete after DRAFT write succeeded, got %+v", dels)
	}
}

func TestAttachments_CrashRecovery_CoreResend_AndAutoPrune(t *testing.T) {
	h := newAttHarness(t, true, true)

	validOutbox := &corev1.PersistedOutboxRecord{
		SchemaVersion: attachments.PersistedSchemaVersion,
		SessionId:     h.sessionID,
		RequestId:     "req-crashed-1",
		MachineId:     h.machineA,
		Text:          "crashed msg",
		Mode:          v1.SendMode_SEND_MODE_STEER,
		ImageIds:      []string{"old-img-id"},
		Items: []*corev1.PersistedAttachmentItem{
			{
				ItemId:            "old-item-1",
				Kind:              corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE,
				FileName:          "shot.jpg",
				MimeType:          "image/jpeg",
				SizeBytes:         4,
				Sha256:            attTestHash(0x88),
				ImageFormat:       v1.ImageFormat_IMAGE_FORMAT_JPEG,
				Width:             320,
				Height:            240,
				HasOriginalSource: true,
				HasPrepared:       true,
				SourceRef:         "src-crash-1",
				PreparedRef:       "prep-crash-1",
				ThumbRef:          "thumb-crash-1",
			},
		},
		CreatedMs: h.nowMs - 5000,
	}
	rawValid, err := attachments.EncodePersistedOutbox(validOutbox)
	if err != nil {
		t.Fatalf("encode outbox: %v", err)
	}

	cfgEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentConfig{
			AttachmentConfig: &corev1.AttachmentConfigEvent{PersistDrafts: true, PersistOutbox: true, ScanStorage: true},
		},
	})
	var listOp uint64
	for _, ef := range cfgEff.GetEffects() {
		if l := ef.GetDraftStorageList(); l != nil {
			listOp = l.OpId
		}
	}
	listEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftStorageListed{
			DraftStorageListed: &corev1.DraftStorageListedEvent{
				OpId: listOp,
				Entries: []*corev1.DraftStorageEntry{
					{
						Bucket: corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX,
						Key:    attachments.OutboxStorageKey(h.sessionID, "req-crashed-1"),
						Data:   rawValid,
					},
					{
						Bucket: corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX,
						Key:    "legacy-json-key",
						Data:   []byte(`{"legacy":true}`),
					},
				},
			},
		},
	})
	dels := findDraftDeleteEffects(listEff)
	if len(dels) != 1 || dels[0].Key != "legacy-json-key" {
		t.Fatalf("expected corrupt/legacy record deleted, got %+v", dels)
	}

	svm := h.sessionView()
	var restoredItem *corev1.SessionTrayItemView
	for _, it := range svm.GetComposer().GetPendingTray().GetOrderedItems() {
		if it.RequestId == "req-crashed-1" {
			restoredItem = it
		}
	}
	if restoredItem == nil || !restoredItem.RestoredFromCrash || len(restoredItem.LocalAttachments) != 1 || !restoredItem.CanRestore || restoredItem.CanRetry {
		t.Fatalf("expected crash-restored unsure tray item (RestoredFromCrash=true, CanRestore=true, CanRetry=false), got %+v", restoredItem)
	}

	// Simulate an InputStatus=FAILED projection for req-crashed-1 (or OutgoingFailed) -> now CanRetry=true -> Core-driven resend!
	h.core.m.sessions.uiBySession[h.sessionID].Outgoing[0].State = 2 // screens.OutgoingFailed
	retryEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_SessionRetryTrayItem{
			SessionRetryTrayItem: &corev1.SessionTrayItemAction{SessionId: h.sessionID, RequestId: "req-crashed-1"},
		},
	})
	beginResend := findDispatchedCmds(retryEff)[0]
	if beginResend.cmd.GetBeginImageUpload() == nil {
		t.Fatalf("expected BeginImageUpload for Core-driven resend")
	}
	eff := h.settleCmdOK(beginResend.dc, beginCompleteOK(beginResend.cmd.GetBeginImageUpload().TransferId, "new-img-remote-2", "", h.nowMs+600_000))
	resendOutWrites := findDraftWriteEffects(eff)
	if len(resendOutWrites) != 1 || resendOutWrites[0].Bucket != corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX {
		t.Fatalf("expected new OUTBOX write before resend SendRequest, got %+v", resendOutWrites)
	}
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftStorageWriteResult{
			DraftStorageWriteResult: &corev1.DraftStorageWriteResultEvent{OpId: resendOutWrites[0].OpId, Ok: true},
		},
	})
	resendSend := findDispatchedCmds(eff)[0]
	if resendSend.cmd.GetSend() == nil || resendSend.cmd.GetSend().ImageIds[0] != "new-img-remote-2" {
		t.Fatalf("expected resent SendRequest with new-img-remote-2, got %+v", resendSend.cmd)
	}
	newReqID := resendSend.cmd.GetSend().RequestId

	// Mark resend Executed + deliver UserMessage event -> auto-prunes outbox & releases unreferenced local refs!
	h.settleCmdOK(resendSend.dc, &v1.CommandResult{})
	evEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_EventsAppended{
					EventsAppended: &corev1.EventsAppendedFrame{
						SessionId: h.sessionID,
						Events: []*corev1.PlainEvent{
							{
								Seq:      2,
								Verified: true,
								Event: &v1.Event{
									Seq:       2,
									SessionId: h.sessionID,
									Kind: &v1.Event_UserMessage{
										UserMessage: &v1.UserMessage{
											RequestId: newReqID,
											Text:      "crashed msg",
										},
									},
								},
							},
						},
					},
				},
			},
		},
	})
	var prunedOutbox, releasedLocal bool
	for _, d := range findDraftDeleteEffects(evEff) {
		if d.Bucket == corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX {
			prunedOutbox = true
		}
	}
	for _, rel := range findReleaseLocalEffects(evEff) {
		for _, r := range rel.Refs {
			if r == "src-crash-1" {
				releasedLocal = true
			}
		}
	}
	if !prunedOutbox || !releasedLocal {
		t.Fatalf("expected settled outbox pruned and unreferenced local refs released; pruned=%v released=%v", prunedOutbox, releasedLocal)
	}
}

func TestAttachments_ComposerTextDraft_EditRace_AndCleanup(t *testing.T) {
	h := newAttHarness(t, true, true)
	scope := h.sessionScope()

	// 1. Open draft -> emits DraftStorageRead.
	openEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftOpen{DraftOpen: &corev1.DraftOpenAction{Scope: scope}},
	})
	var readOp uint64
	for _, ef := range openEff.GetEffects() {
		if rd := ef.GetDraftStorageRead(); rd != nil {
			readOp = rd.OpId
		}
	}
	if readOp == 0 {
		t.Fatalf("expected DraftStorageReadEffect on DraftOpen")
	}

	// 2. BEFORE disk read completes, user types new text (bumping text_revision)!
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftSetText{DraftSetText: &corev1.DraftSetTextAction{Scope: scope, Text: "fresh user typing"}},
	})
	dv := h.draftView(scope)
	if dv.Text != "fresh user typing" || dv.TextRevision == 0 {
		t.Fatalf("expected user typing preserved, got text=%q rev=%d", dv.Text, dv.TextRevision)
	}

	// 3. Disk read completes with older persisted text -> user's typing MUST win!
	diskDraft := &corev1.PersistedDraftRecord{
		SchemaVersion: attachments.PersistedSchemaVersion,
		Scope:         scope,
		Text:          "stale disk text",
		UpdatedMs:     h.nowMs - 10_000,
	}
	rawDisk, _ := attachments.EncodePersistedDraft(diskDraft)
	loadEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftStorageReadResult{
			DraftStorageReadResult: &corev1.DraftStorageReadResultEvent{OpId: readOp, Found: true, Data: rawDisk},
		},
	})
	dv = h.draftView(scope)
	if dv.Text != "fresh user typing" {
		t.Fatalf("edit race violated: stale disk text overwrote user typing! got %q", dv.Text)
	}
	wEffs := findDraftWriteEffects(loadEff)
	if len(wEffs) != 1 || wEffs[0].Bucket != corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT {
		t.Fatalf("expected flushed DRAFT write after edit-race load, got %+v", wEffs)
	}
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftStorageWriteResult{
			DraftStorageWriteResult: &corev1.DraftStorageWriteResultEvent{OpId: wEffs[0].OpId, Ok: true},
		},
	})

	// 4. Clearing text to empty emits DraftStorageDelete(DRAFT).
	clearEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftSetText{DraftSetText: &corev1.DraftSetTextAction{Scope: scope, Text: ""}},
	})
	dels := findDraftDeleteEffects(clearEff)
	if len(dels) != 1 || dels[0].Bucket != corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT {
		t.Fatalf("expected DRAFT delete when empty, got %+v", dels)
	}

	// 5. DraftClearAll emits DraftStorageClearAllEffect.
	allEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftClearAll{DraftClearAll: &corev1.DraftClearAllAction{}},
	})
	var sawClearAll bool
	for _, ef := range allEff.GetEffects() {
		if ef.GetDraftStorageClearAll() != nil {
			sawClearAll = true
		}
	}
	if !sawClearAll {
		t.Fatalf("expected DraftStorageClearAllEffect")
	}
}

func TestAttachments_StaleCallbacksAndNewSessionProtection(t *testing.T) {
	h := newAttHarness(t, false, false)
	scope := h.newSessionScope()

	// 1. Add image on NewSession, remove it BEFORE prepare finishes -> late PrepareImageResult releases orphan refs!
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_NewSessionOpen{NewSessionOpen: &corev1.NewSessionOpenAction{ProjectKey: h.project}},
	})
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_NewSessionSelectMachine{NewSessionSelectMachine: &corev1.NewSessionSelectMachineAction{MachineId: h.machineA}},
	})
	addEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{
					pickedImage("orphan.jpg", 4, "src-orphan", true),
				},
			},
		},
	})
	prepOp := findPrepareEffects(addEff)[0].OpId
	itemID := h.draftView(scope).Images[0].ItemId
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftRemoveItem{
			DraftRemoveItem: &corev1.DraftRemoveItemAction{Scope: scope, ItemId: itemID},
		},
	})
	lateEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentPrepareImageResult{
			AttachmentPrepareImageResult: &corev1.AttachmentPrepareImageResultEvent{
				OpId: prepOp, Ok: true, PreparedRef: "orphan-prep", ThumbRef: "orphan-thumb",
				Format: v1.ImageFormat_IMAGE_FORMAT_JPEG, SizeBytes: 4, Sha256: attTestHash(0x99),
			},
		},
	})
	rels := findReleaseLocalEffects(lateEff)
	if len(rels) != 1 || len(rels[0].Refs) != 2 {
		t.Fatalf("expected orphan prepared_ref + thumb_ref released on stale prepare result, got %+v", rels)
	}

	// 2. Add ready image to NewSession + Submit -> protects transfer, passes ImageIds in CreateSession, detaches on Executed without abort!
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftSetText{DraftSetText: &corev1.DraftSetTextAction{Scope: scope, Text: "build app"}},
	})
	addEff2 := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{
					pickedImage("spec.jpg", 4, "src-ns-1", true),
				},
			},
		},
	})
	h.driveImageUploadReady(addEff2, "prep-ns-1", "thumb-ns-1", 4, attTestHash(0xaa), "img-ns-1", h.nowMs+600_000)

	subEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_NewSessionSubmit{
			NewSessionSubmit: &corev1.NewSessionSubmitAction{ProjectKey: h.project},
		},
	})
	var createCmd *attDispatchedCmd
	for _, sc := range findDispatchedCmds(subEff) {
		if sc.cmd.GetCreateSession() != nil {
			cp := sc
			createCmd = &cp
		}
	}
	if createCmd == nil || len(createCmd.cmd.GetCreateSession().ImageIds) != 1 || createCmd.cmd.GetCreateSession().ImageIds[0] != "img-ns-1" {
		t.Fatalf("expected CreateSession with Core draft image img-ns-1, got %+v", createCmd)
	}

	// Settle CreateSession as Executed -> detaches draft without abort and releases local refs!
	doneEff := h.settleCmdOK(createCmd.dc, &v1.CommandResult{
		Kind: &v1.CommandResult_CreateSession{
			CreateSession: &v1.CreateSessionResponse{
				Session: &v1.Session{Id: "s-new-1", Cwd: h.project, Agent: v1.Agent_AGENT_CODEX, Status: v1.SessionStatus_SESSION_STATUS_RUNNING},
			},
		},
	})
	for _, sc := range findDispatchedCmds(doneEff) {
		if sc.cmd.GetAbortImageUpload() != nil {
			t.Fatalf("CreateSession Executed must NOT abort consumed transfer")
		}
	}
	if len(h.draftView(scope).Items) != 0 || h.draftView(scope).Text != "" {
		t.Fatalf("expected NewSession draft cleared after CreateSession Executed")
	}
}

func TestAttachments_ResendFailure_RetainsOutboxForRetryAndPutBack(t *testing.T) {
	h := newAttHarness(t, true, true)
	scope := h.sessionScope()

	// Send a message with 1 image and fail the SendRequest so we have a failed tray item backed by outbox.
	openEff := h.dispatch(&corev1.Event{Event: &corev1.Event_DraftOpen{DraftOpen: &corev1.DraftOpenAction{Scope: scope}}})
	for _, ef := range openEff.GetEffects() {
		if rd := ef.GetDraftStorageRead(); rd != nil {
			h.dispatch(&corev1.Event{Event: &corev1.Event_DraftStorageReadResult{DraftStorageReadResult: &corev1.DraftStorageReadResultEvent{OpId: rd.OpId, Found: false}}})
		}
	}
	addEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{pickedImage("retry.jpg", 4, "src-resend-fail", true)},
			},
		},
	})
	for _, dw := range findDraftWriteEffects(addEff) {
		h.dispatch(&corev1.Event{Event: &corev1.Event_DraftStorageWriteResult{DraftStorageWriteResult: &corev1.DraftStorageWriteResultEvent{OpId: dw.OpId, Ok: true}}})
	}
	h.driveImageUploadReady(addEff, "prep-resend-fail", "thumb-resend-fail", 4, attTestHash(0xb1), "img-orig-1", h.nowMs+600_000)

	sendEff := h.dispatch(&corev1.Event{Event: &corev1.Event_DraftSendSession{DraftSendSession: &corev1.DraftSendSessionAction{SessionId: h.sessionID, Mode: v1.SendMode_SEND_MODE_STEER}}})
	outWrite := findDraftWriteEffects(sendEff)[0]
	commitEff := h.dispatch(&corev1.Event{Event: &corev1.Event_DraftStorageWriteResult{DraftStorageWriteResult: &corev1.DraftStorageWriteResultEvent{OpId: outWrite.OpId, Ok: true}}})
	var sendCmd attDispatchedCmd
	for _, sc := range findDispatchedCmds(commitEff) {
		if sc.cmd.GetSend() != nil {
			sendCmd = sc
		}
	}
	origReqID := sendCmd.cmd.GetSend().RequestId
	h.settleCmdDaemonErr(sendCmd.dc, "failed")

	// Retry #1 -> BeginImageUpload fails permanently with daemon error -> MUST NOT release local refs and MUST attach outbox to newReqID!
	retry1Eff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_SessionRetryTrayItem{SessionRetryTrayItem: &corev1.SessionTrayItemAction{SessionId: h.sessionID, RequestId: origReqID}},
	})
	begin1 := findDispatchedCmds(retry1Eff)[0]
	fail1Eff := h.settleCmdDaemonErr(begin1.dc, "resource_exhausted")
	for _, rel := range findReleaseLocalEffects(fail1Eff) {
		t.Fatalf("failed resend must NOT release local refs still owned by failed row: %+v", rel.Refs)
	}
	var failedNewReqID string
	for _, it := range h.sessionView().GetComposer().GetPendingTray().GetOrderedItems() {
		if it.RequestId != origReqID {
			failedNewReqID = it.RequestId
		}
	}
	if failedNewReqID == "" {
		t.Fatalf("expected failed resend row in pending tray")
	}

	// Retry #2 on the failed resend row -> MUST start BeginImageUpload directly in Core.
	retry2Eff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_SessionRetryTrayItem{SessionRetryTrayItem: &corev1.SessionTrayItemAction{SessionId: h.sessionID, RequestId: failedNewReqID}},
	})
	var sawBegin2 bool
	for _, sc := range findDispatchedCmds(retry2Eff) {
		if sc.cmd.GetBeginImageUpload() != nil {
			sawBegin2 = true
		}
	}
	if !sawBegin2 {
		t.Fatalf("expected second retry to start BeginImageUpload directly in Core")
	}
}

func TestAttachments_CancelInFlightResend_AbortsUploadAndNeverSends(t *testing.T) {
	h := newAttHarness(t, false, false)
	scope := h.sessionScope()

	// Send + fail to get a failed tray item backed by outbox.
	addEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{pickedImage("cancel-resend.jpg", 4, "src-cr", true)},
			},
		},
	})
	h.driveImageUploadReady(addEff, "prep-cr", "thumb-cr", 4, attTestHash(0xb2), "img-cr-1", h.nowMs+600_000)
	sendEff := h.dispatch(&corev1.Event{Event: &corev1.Event_DraftSendSession{DraftSendSession: &corev1.DraftSendSessionAction{SessionId: h.sessionID, Mode: v1.SendMode_SEND_MODE_STEER}}})
	sendCmd := findDispatchedCmds(sendEff)[0]
	origReqID := sendCmd.cmd.GetSend().RequestId
	h.settleCmdDaemonErr(sendCmd.dc, "failed")

	// Start resend -> BeginImageUpload is in flight on daemon!
	retryEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_SessionRetryTrayItem{SessionRetryTrayItem: &corev1.SessionTrayItemAction{SessionId: h.sessionID, RequestId: origReqID}},
	})
	beginCmd := findDispatchedCmds(retryEff)[0]
	resendTID := beginCmd.cmd.GetBeginImageUpload().TransferId
	var resendReqID string
	for _, it := range h.sessionView().GetComposer().GetPendingTray().GetOrderedItems() {
		if it.RequestId != origReqID {
			resendReqID = it.RequestId
			if !it.CanCancel {
				t.Fatalf("expected CanCancel=true on resending tray row")
			}
		}
	}
	if resendReqID == "" {
		t.Fatalf("expected resending row in tray")
	}

	// Cancel the resending tray row -> MUST abort upload (AbortImageUpload) and drop late Begin outcome without submitting Send!
	cancelEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_SessionCancelTrayItem{SessionCancelTrayItem: &corev1.SessionTrayItemAction{SessionId: h.sessionID, RequestId: resendReqID}},
	})
	var sawAbort bool
	for _, sc := range findDispatchedCmds(cancelEff) {
		if ab := sc.cmd.GetAbortImageUpload(); ab != nil && ab.TransferId == resendTID {
			sawAbort = true
		}
	}
	if !sawAbort {
		t.Fatalf("expected AbortImageUpload for canceled in-flight resend %s", resendTID)
	}

	// Late BeginImageUpload COMPLETE outcome arrives -> MUST NOT submit SendRequest!
	lateEff := h.settleCmdOK(beginCmd.dc, beginCompleteOK(resendTID, "img-cr-late", "", h.nowMs+600_000))
	for _, sc := range findDispatchedCmds(lateEff) {
		if sc.cmd.GetSend() != nil {
			t.Fatalf("canceled resend must NEVER submit SendRequest on late upload completion")
		}
	}
}

func TestAttachments_PutFailsStatusProbeNoAdvance_DoesNotLoopInfinitely(t *testing.T) {
	h := newAttHarness(t, false, false)
	scope := h.sessionScope()

	eff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{pickedFile("loop.bin", "application/octet-stream", 8, "src-loop")},
			},
		},
	})
	hashOp := findHashEffects(eff)[0]
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentHashFileResult{
			AttachmentHashFileResult: &corev1.AttachmentHashFileResultEvent{OpId: hashOp.OpId, Ok: true, SizeBytes: 8, Sha256: attTestHash(0xb3)},
		},
	})
	beginCmd := findDispatchedCmds(eff)[0]
	tid := beginCmd.cmd.GetBeginImageUpload().TransferId
	eff = h.settleCmdOK(beginCmd.dc, beginOK(tid, 0, 4, 0))

	// Repeat: ReadChunk -> PutImageChunk transport drop -> GetImageUploadStatus succeeds with SAME persisted_bytes=0.
	// Must NOT loop forever: after 3 retries -> WAITING_RECONNECT -> after 3 more retries -> FAILED!
	failed := false
	for iter := 0; iter < 15; iter++ {
		dv := h.draftView(scope)
		if dv.Files[0].State == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_FAILED {
			failed = true
			break
		}
		if timers := findTimers(eff, corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_RECONNECT); len(timers) > 0 {
			h.advance(timers[0].DelayMs)
			eff = h.dispatch(&corev1.Event{
				Event: &corev1.Event_TimerFired{
					TimerFired: &corev1.TimerFiredEvent{
						TimerId: timers[0].TimerId,
						Kind:    corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_RECONNECT,
						Key:     timers[0].Key,
					},
				},
			})
			// Reconnect fires GetImageUploadStatus -> reply with same persisted_bytes=0.
			cmds := findDispatchedCmds(eff)
			if len(cmds) == 1 && cmds[0].cmd.GetGetImageUploadStatus() != nil {
				eff = h.settleCmdOK(cmds[0].dc, statusOK(tid, 0, 4))
			}
			continue
		}
		if reads := findReadChunkEffects(eff); len(reads) > 0 {
			eff = h.dispatch(&corev1.Event{
				Event: &corev1.Event_AttachmentReadChunkResult{
					AttachmentReadChunkResult: &corev1.AttachmentReadChunkResultEvent{OpId: reads[0].OpId, Ok: true, Offset: 0, Data: []byte("1234")},
				},
			})
			putCmd := findDispatchedCmds(eff)[0]
			eff = h.settleCmdTransportDrop(putCmd.dc, "timeout")
			if cmds := findDispatchedCmds(eff); len(cmds) == 1 && cmds[0].cmd.GetGetImageUploadStatus() != nil {
				eff = h.settleCmdOK(cmds[0].dc, statusOK(tid, 0, 4))
			}
			continue
		}
		t.Fatalf("iter=%d: unexpected stall in state %v", iter, dv.Files[0].State)
	}
	if !failed {
		t.Fatalf("expected upload to terminate in FAILED instead of looping infinitely")
	}
}

func TestAttachments_ResendWithMissingLocalItem_FailsCleanlyInsteadOfHanging(t *testing.T) {
	h := newAttHarness(t, true, true)

	// Restore an outbox record mixing 1 valid local image + 1 item missing local copy!
	mixedOutbox := &corev1.PersistedOutboxRecord{
		SchemaVersion: attachments.PersistedSchemaVersion,
		SessionId:     h.sessionID,
		RequestId:     "req-mixed-1",
		MachineId:     h.machineA,
		Text:          "mixed msg",
		Mode:          v1.SendMode_SEND_MODE_STEER,
		ImageIds:      []string{"old-img-1", "old-img-2"},
		Items: []*corev1.PersistedAttachmentItem{
			{
				ItemId:            "ok-1",
				Kind:              corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE,
				FileName:          "ok.jpg",
				MimeType:          "image/jpeg",
				SizeBytes:         4,
				Sha256:            attTestHash(0xb4),
				ImageFormat:       v1.ImageFormat_IMAGE_FORMAT_JPEG,
				HasOriginalSource: true,
				HasPrepared:       true,
				SourceRef:         "src-mix-ok",
				PreparedRef:       "prep-mix-ok",
			},
			{
				ItemId:            "missing-2",
				Kind:              corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE,
				FileName:          "gone.jpg",
				MimeType:          "image/jpeg",
				HasOriginalSource: false,
				HasPrepared:       false,
				SourceRef:         "",
				PreparedRef:       "",
			},
		},
		CreatedMs: h.nowMs - 1000,
	}
	raw, _ := attachments.EncodePersistedOutbox(mixedOutbox)
	cfgEff := h.dispatch(&corev1.Event{Event: &corev1.Event_AttachmentConfig{AttachmentConfig: &corev1.AttachmentConfigEvent{PersistDrafts: true, PersistOutbox: true, ScanStorage: true}}})
	var listOp uint64
	for _, ef := range cfgEff.GetEffects() {
		if l := ef.GetDraftStorageList(); l != nil {
			listOp = l.OpId
		}
	}
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftStorageListed{
			DraftStorageListed: &corev1.DraftStorageListedEvent{
				OpId: listOp,
				Entries: []*corev1.DraftStorageEntry{
					{Bucket: corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX, Key: attachments.OutboxStorageKey(h.sessionID, "req-mixed-1"), Data: raw},
				},
			},
		},
	})
	h.core.m.sessions.uiBySession[h.sessionID].Outgoing[0].State = 2 // OutgoingFailed
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_SessionRetryTrayItem{SessionRetryTrayItem: &corev1.SessionTrayItemAction{SessionId: h.sessionID, RequestId: "req-mixed-1"}},
	})

	// Must NOT hang in SENDING! Resend job must be cleaned up and tray row must be FAILED.
	if len(h.core.m.attachments.resendJobs) != 0 {
		t.Fatalf("expected resend job aborted immediately when item lacks local copy, still have %d jobs", len(h.core.m.attachments.resendJobs))
	}
	var sawFailed bool
	for _, it := range h.sessionView().GetComposer().GetPendingTray().GetOrderedItems() {
		if it.Group == corev1.SessionTrayGroupKind_SESSION_TRAY_GROUP_KIND_FAILED {
			sawFailed = true
		}
		if it.Activity == corev1.SessionTrayRowActivity_SESSION_TRAY_ROW_ACTIVITY_SENDING {
			t.Fatalf("resend with missing local item must NOT stay stuck in SENDING: %+v", it)
		}
	}
	if !sawFailed {
		t.Fatalf("expected failed tray item after missing-local resend failure")
	}
}

func TestAttachments_StopStartAndForeground_ResumesResendAndReschedulesExpiry(t *testing.T) {
	h := newAttHarness(t, false, false)
	scope := h.sessionScope()

	// 1. Upload 1 image to READY (so DRAFT_EXPIRY timer is scheduled), send & fail -> start resend (in PollDelay!).
	addEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{pickedImage("stop1.jpg", 4, "src-stop1", true)},
			},
		},
	})
	h.driveImageUploadReady(addEff, "prep-stop1", "thumb-stop1", 4, attTestHash(0xb5), "img-stop1", h.nowMs+600_000)
	sendEff := h.dispatch(&corev1.Event{Event: &corev1.Event_DraftSendSession{DraftSendSession: &corev1.DraftSendSessionAction{SessionId: h.sessionID, Mode: v1.SendMode_SEND_MODE_STEER}}})
	sendCmd := findDispatchedCmds(sendEff)[0]
	origReqID := sendCmd.cmd.GetSend().RequestId
	h.settleCmdDaemonErr(sendCmd.dc, "failed")

	// Add another READY image to draft (so draft has an active DRAFT_EXPIRY timer).
	addEff2 := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{pickedImage("stop2.jpg", 4, "src-stop2", true)},
			},
		},
	})
	h.driveImageUploadReady(addEff2, "prep-stop2", "thumb-stop2", 4, attTestHash(0xb6), "img-stop2", h.nowMs+600_000)

	// Start resend of origReqID and drive it to CompleteImageUpload RECEIVING (PollDelay)!
	retryEff := h.dispatch(&corev1.Event{Event: &corev1.Event_SessionRetryTrayItem{SessionRetryTrayItem: &corev1.SessionTrayItemAction{SessionId: h.sessionID, RequestId: origReqID}}})
	rBegin := findDispatchedCmds(retryEff)[0]
	rTID := rBegin.cmd.GetBeginImageUpload().TransferId
	rEff := h.settleCmdOK(rBegin.dc, beginOK(rTID, 4, 4, 0)) // persisted=4 -> immediately emits CompleteImageUpload!
	rComp := findDispatchedCmds(rEff)[0]
	h.settleCmdOK(rComp.dc, completeStatusOK(rTID, v1.ImageUploadState_IMAGE_UPLOAD_STATE_RECEIVING, "", "", 4, 0))

	// STOP -> must pause resend (without abort) AND clear draft expiryTimerTargetMs!
	stopEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_STOP}},
	})
	for _, sc := range findDispatchedCmds(stopEff) {
		if sc.cmd.GetAbortImageUpload() != nil {
			t.Fatalf("STOP must NOT abort resend transfer")
		}
	}
	if h.core.m.attachments.drafts["session:"+h.sessionID].expiryTimerTargetMs != 0 {
		t.Fatalf("STOP must clear expiryTimerTargetMs")
	}

	// START -> reconnect stream -> Core auto-resumes resend via GetImageUploadStatus(rTID) AND reschedules DRAFT_EXPIRY!
	startEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START}},
	})
	var tokOp uint64
	for _, ef := range startEff.GetEffects() {
		if et := ef.GetEnsureToken(); et != nil {
			tokOp = et.OpId
		}
	}
	var legID uint64
	if tokOp != 0 {
		tEff := h.dispatch(&corev1.Event{Event: &corev1.Event_TokenResult{TokenResult: &corev1.TokenResultEvent{OpId: tokOp, Token: "tok-2", ExpiresMs: h.nowMs + 3_600_000}}})
		for _, ef := range tEff.GetEffects() {
			if os := ef.GetOpenStream(); os != nil {
				legID = os.LegId
			}
		}
	}
	for _, ef := range startEff.GetEffects() {
		if os := ef.GetOpenStream(); os != nil {
			legID = os.LegId
		}
	}
	readyEff := h.dispatch(&corev1.Event{Event: &corev1.Event_StreamReady{StreamReady: &corev1.StreamReadyEvent{LegId: legID, ServerTimeMs: h.nowMs}}})
	var resumedResend bool
	for _, sc := range findDispatchedCmds(readyEff) {
		if st := sc.cmd.GetGetImageUploadStatus(); st != nil && st.TransferId == rTID {
			resumedResend = true
		}
	}
	if !resumedResend {
		t.Fatalf("expected Core to auto-resume STOP-paused resend transfer %s via GetImageUploadStatus after START+StreamReady", rTID)
	}
	if len(findTimers(readyEff, corev1.TimerKind_TIMER_KIND_DRAFT_EXPIRY)) != 1 {
		t.Fatalf("expected DRAFT_EXPIRY timer rescheduled after STOP+START")
	}

	// FOREGROUND -> forces rescheduling of DRAFT_EXPIRY timer!
	fgEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_FOREGROUND}},
	})
	if len(findTimers(fgEff, corev1.TimerKind_TIMER_KIND_DRAFT_EXPIRY)) != 1 {
		t.Fatalf("expected DRAFT_EXPIRY timer rescheduled on FOREGROUND")
	}
}

func TestAttachments_ColdStartOffline_LazyUnopened_7DayExpiry_AndOrphanGC(t *testing.T) {
	h := newAttHarness(t, true, true)
	// Take machineA offline before startup scan.
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_MachineUpdated{
					MachineUpdated: &corev1.PlainMachine{
						MachineId: h.machineA, Online: false, MetaRev: 2,
						Meta: &v1.MachineMeta{Hostname: "Alpha", Home: "/Users/me", ProtocolVersion: 17, Capabilities: &v1.MachineCapabilities{ImageUpload: true, FileUpload: true}},
					},
				},
			},
		},
	})

	freshDraft := &corev1.PersistedDraftRecord{
		SchemaVersion: attachments.PersistedSchemaVersion,
		Scope:         h.sessionScope(),
		Text:          "offline draft",
		UpdatedMs:     h.nowMs - 60_000,
		Items: []*corev1.PersistedAttachmentItem{
			{
				ItemId:            "saved-1",
				Kind:              corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE,
				FileName:          "saved.jpg",
				MimeType:          "image/jpeg",
				SizeBytes:         4,
				Sha256:            attTestHash(0xb7),
				ImageFormat:       v1.ImageFormat_IMAGE_FORMAT_JPEG,
				HasOriginalSource: true,
				HasPrepared:       true,
				SourceRef:         "src-saved-1",
				PreparedRef:       "prep-saved-1",
			},
		},
	}
	stale7DayDraft := &corev1.PersistedDraftRecord{
		SchemaVersion: attachments.PersistedSchemaVersion,
		Scope:         &corev1.DraftScope{Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION, Id: "s-stale-7d"},
		Text:          "ancient draft",
		UpdatedMs:     h.nowMs - attachments.DraftIdleExpiryMs - 1000,
	}
	rawFresh, _ := attachments.EncodePersistedDraft(freshDraft)
	rawStale, _ := attachments.EncodePersistedDraft(stale7DayDraft)

	cfgEff := h.dispatch(&corev1.Event{Event: &corev1.Event_AttachmentConfig{AttachmentConfig: &corev1.AttachmentConfigEvent{PersistDrafts: true, PersistOutbox: true, ScanStorage: true}}})
	var listOp uint64
	for _, ef := range cfgEff.GetEffects() {
		if l := ef.GetDraftStorageList(); l != nil {
			listOp = l.OpId
		}
	}
	scanEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftStorageListed{
			DraftStorageListed: &corev1.DraftStorageListedEvent{
				OpId: listOp,
				Entries: []*corev1.DraftStorageEntry{
					{Bucket: corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT, Key: "session:" + h.sessionID, Data: rawFresh},
					{Bucket: corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT, Key: "session:s-stale-7d", Data: rawStale},
				},
				LocalRefs: []string{"src-saved-1", "prep-saved-1", "orphan-disk-ref"},
			},
		},
	})

	// 1. Unopened session MUST NOT emit PrepareImage, BeginImageUpload, or a 30s reconnect timer on scan!
	if len(findPrepareEffects(scanEff)) != 0 || len(findDispatchedCmds(scanEff)) != 0 || len(findTimers(scanEff, corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_RECONNECT)) != 0 {
		t.Fatalf("unopened session must only hydrate on scan without preparing/uploading/timing out")
	}
	// 2. 7-day idle draft deleted on scan!
	var deletedStale bool
	for _, d := range findDraftDeleteEffects(scanEff) {
		if d.Key == "session:s-stale-7d" {
			deletedStale = true
		}
	}
	if !deletedStale {
		t.Fatalf("expected 7-day idle draft deleted on scan")
	}
	// 3. Orphan local ref released via AttachmentReleaseLocalEffect, while referenced refs are kept!
	var releasedOrphan, releasedLive bool
	for _, rel := range findReleaseLocalEffects(scanEff) {
		for _, r := range rel.Refs {
			if r == "orphan-disk-ref" {
				releasedOrphan = true
			}
			if r == "src-saved-1" || r == "prep-saved-1" {
				releasedLive = true
			}
		}
	}
	if !releasedOrphan || releasedLive {
		t.Fatalf("expected only orphan-disk-ref released; releasedOrphan=%v releasedLive=%v", releasedOrphan, releasedLive)
	}

	// 4. Open draft while offline -> item parks waiting online with NO 30s timeout timer; stays non-FAILED even after 60s!
	openEff := h.dispatch(&corev1.Event{Event: &corev1.Event_DraftOpen{DraftOpen: &corev1.DraftOpenAction{Scope: h.sessionScope()}}})
	if len(findTimers(openEff, corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_RECONNECT)) != 0 {
		t.Fatalf("not-yet-started upload while offline must NOT schedule 30s reconnect failure timer")
	}
	h.advance(60_000)
	if st := h.draftView(h.sessionScope()).Images[0].State; st == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_FAILED {
		t.Fatalf("cold-start offline draft item must NOT turn FAILED after 60s")
	}

	// 5. Machine comes online -> item immediately begins uploading!
	onlineEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_MachineUpdated{
					MachineUpdated: &corev1.PlainMachine{
						MachineId: h.machineA, Online: true, MetaRev: 3,
						Meta: &v1.MachineMeta{Hostname: "Alpha", Home: "/Users/me", ProtocolVersion: 17, Capabilities: &v1.MachineCapabilities{ImageUpload: true, FileUpload: true}},
					},
				},
			},
		},
	})
	var sawBegin bool
	for _, sc := range findDispatchedCmds(onlineEff) {
		if sc.cmd.GetBeginImageUpload() != nil {
			sawBegin = true
		}
	}
	if !sawBegin {
		t.Fatalf("expected offline-parked draft item to start BeginImageUpload when machine comes online")
	}
}

func TestAttachments_FileRetryRecomputesHash(t *testing.T) {
	h := newAttHarness(t, false, false)
	scope := h.sessionScope()

	eff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{pickedFile("report.pdf", "application/pdf", 8, "src-pdf")},
			},
		},
	})
	hash1 := findHashEffects(eff)[0]
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentHashFileResult{
			AttachmentHashFileResult: &corev1.AttachmentHashFileResultEvent{OpId: hash1.OpId, Ok: true, SizeBytes: 8, Sha256: attTestHash(0xb8)},
		},
	})
	begin1 := findDispatchedCmds(eff)[0]
	// Fail upload permanently on daemon.
	h.settleCmdDaemonErr(begin1.dc, "resource_exhausted")
	dv := h.draftView(scope)
	if dv.Files[0].State != corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_FAILED {
		t.Fatalf("expected FAILED file, got %v", dv.Files[0].State)
	}

	// Retry file -> MUST emit AttachmentHashFileEffect to recompute sha256 & sizeBytes!
	retryEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftRetryItem{
			DraftRetryItem: &corev1.DraftRetryItemAction{Scope: scope, ItemId: dv.Files[0].ItemId},
		},
	})
	hashes := findHashEffects(retryEff)
	if len(hashes) != 1 || hashes[0].SourceRef != "src-pdf" {
		t.Fatalf("expected file retry to re-run AttachmentHashFileEffect, got %+v", hashes)
	}
	if len(findDispatchedCmds(retryEff)) != 0 {
		t.Fatalf("expected no BeginImageUpload before fresh hash completes")
	}
}

func TestAttachments_ServerCanceledPutBackPreCheckFails_PreservesTextAndOutbox(t *testing.T) {
	h := newAttHarness(t, false, true)
	scope := h.sessionScope()

	// 1. Send a message with text + 1 image -> SendRequest succeeds and enters server PendingInputs.
	h.dispatch(&corev1.Event{Event: &corev1.Event_DraftSetText{DraftSetText: &corev1.DraftSetTextAction{Scope: scope, Text: "important text"}}})
	addEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{pickedImage("keep.jpg", 4, "src-keep-8", true)},
			},
		},
	})
	h.driveImageUploadReady(addEff, "prep-keep-8", "thumb-keep-8", 4, attTestHash(0xb9), "img-keep-8", h.nowMs+600_000)
	sendEff := h.dispatch(&corev1.Event{Event: &corev1.Event_DraftSendSession{DraftSendSession: &corev1.DraftSendSessionAction{SessionId: h.sessionID, Mode: v1.SendMode_SEND_MODE_QUEUE}}})
	outWrite := findDraftWriteEffects(sendEff)[0]
	commitEff := h.dispatch(&corev1.Event{Event: &corev1.Event_DraftStorageWriteResult{DraftStorageWriteResult: &corev1.DraftStorageWriteResultEvent{OpId: outWrite.OpId, Ok: true}}})
	sendCmd := findDispatchedCmds(commitEff)[0]
	reqID := sendCmd.cmd.GetSend().RequestId
	h.settleCmdOK(sendCmd.dc, &v1.CommandResult{})

	// Deliver server-backed PendingInputs projection for reqID.
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_EventsAppended{
					EventsAppended: &corev1.EventsAppendedFrame{
						SessionId: h.sessionID,
						Events: []*corev1.PlainEvent{
							{
								Seq: 2, Verified: true,
								Event: &v1.Event{
									Seq: 2, SessionId: h.sessionID,
									Kind: &v1.Event_InputState{
										InputState: &v1.InputState{
											RequestId: reqID,
											Status:    v1.InputStatus_INPUT_STATUS_QUEUED,
											Mode:      v1.SendMode_SEND_MODE_QUEUE,
											Text:      "important text",
											ImageIds:  []string{"img-keep-8"},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	})

	// 2. Request put-back while draft is empty -> submits CancelInput to daemon!
	restoreEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_SessionRestoreTrayItem{SessionRestoreTrayItem: &corev1.SessionTrayItemAction{SessionId: h.sessionID, RequestId: reqID}},
	})
	var cancelCmd *attDispatchedCmd
	for _, sc := range findDispatchedCmds(restoreEff) {
		if sc.cmd.GetCancelInput() != nil {
			cp := sc
			cancelCmd = &cp
		}
	}
	if cancelCmd == nil {
		t.Fatalf("expected CancelInput submitted for server-backed restore")
	}

	// 3. BEFORE CancelInput settles, fill all 6 image slots in the draft so put-back attachment pre-check WILL FAIL!
	var six []*corev1.DraftPickedItem
	for i := 0; i < 6; i++ {
		six = append(six, pickedImage("fill.jpg", 4, "src-fill-"+string(rune('a'+i)), true))
	}
	h.dispatch(&corev1.Event{Event: &corev1.Event_DraftAddItems{DraftAddItems: &corev1.DraftAddItemsAction{Scope: scope, Items: six}}})

	// 4. CancelInput settles OK -> MUST still merge text into draft, show notice, AND keep outbox & restorable row!
	settleEff := h.settleCmdOK(cancelCmd.dc, &v1.CommandResult{})
	dv := h.draftView(scope)
	if dv.Text != "important text" {
		t.Fatalf("expected text merged even when attachment put-back failed after server cancel, got %q", dv.Text)
	}
	if dv.Notice == nil {
		t.Fatalf("expected notice explaining why attachments could not fit")
	}
	if h.core.m.attachments.outbox[reqID] == nil {
		t.Fatalf("expected outbox record preserved when attachment put-back failed after server cancel")
	}
	for _, d := range findDraftDeleteEffects(settleEff) {
		if d.Bucket == corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX {
			t.Fatalf("must NOT delete persisted outbox when attachment put-back was deferred")
		}
	}
	var keptRow bool
	for _, it := range h.sessionView().GetComposer().GetPendingTray().GetOrderedItems() {
		if it.RequestId == reqID && it.CanRestore {
			keptRow = true
		}
	}
	if !keptRow {
		t.Fatalf("expected restorable tray row preserved for deferred attachment put-back")
	}
}

func TestAttachments_PutBackWhileDraftUnloaded_WaitsForDraftLoadAndWriteBeforeDeletingOutbox(t *testing.T) {
	h := newAttHarness(t, true, true)
	scope := h.sessionScope()

	// Restore a crash outbox for h.sessionID WITHOUT loading/opening the session draft yet!
	outRec := &corev1.PersistedOutboxRecord{
		SchemaVersion: attachments.PersistedSchemaVersion,
		SessionId:     h.sessionID,
		RequestId:     "req-unloaded-pb",
		MachineId:     h.machineA,
		Text:          "put back msg",
		Mode:          v1.SendMode_SEND_MODE_STEER,
		ImageIds:      []string{"old-id"},
		Items: []*corev1.PersistedAttachmentItem{
			{
				ItemId:            "pb-item-1",
				Kind:              corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE,
				FileName:          "pb.jpg",
				MimeType:          "image/jpeg",
				SizeBytes:         4,
				Sha256:            attTestHash(0xba),
				ImageFormat:       v1.ImageFormat_IMAGE_FORMAT_JPEG,
				HasOriginalSource: true,
				HasPrepared:       true,
				SourceRef:         "src-pb-9",
				PreparedRef:       "prep-pb-9",
			},
		},
		CreatedMs: h.nowMs - 5000,
	}
	rawOut, _ := attachments.EncodePersistedOutbox(outRec)
	cfgEff := h.dispatch(&corev1.Event{Event: &corev1.Event_AttachmentConfig{AttachmentConfig: &corev1.AttachmentConfigEvent{PersistDrafts: true, PersistOutbox: true, ScanStorage: true}}})
	var listOp uint64
	for _, ef := range cfgEff.GetEffects() {
		if l := ef.GetDraftStorageList(); l != nil {
			listOp = l.OpId
		}
	}
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftStorageListed{
			DraftStorageListed: &corev1.DraftStorageListedEvent{
				OpId: listOp,
				Entries: []*corev1.DraftStorageEntry{
					{Bucket: corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX, Key: attachments.OutboxStorageKey(h.sessionID, "req-unloaded-pb"), Data: rawOut},
				},
			},
		},
	})

	// Put back while draft is STILL draftLoadUnloaded!
	pbEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_SessionRestoreTrayItem{SessionRestoreTrayItem: &corev1.SessionTrayItemAction{SessionId: h.sessionID, RequestId: "req-unloaded-pb"}},
	})
	// MUST NOT delete OUTBOX yet; MUST emit DraftStorageRead(DRAFT)!
	if len(findDraftDeleteEffects(pbEff)) != 0 {
		t.Fatalf("whenKept barrier violated: deleted OUTBOX while draft was unloaded!")
	}
	var readOp uint64
	for _, ef := range pbEff.GetEffects() {
		if rd := ef.GetDraftStorageRead(); rd != nil && rd.Bucket == corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT {
			readOp = rd.OpId
		}
	}
	if readOp == 0 {
		t.Fatalf("expected DraftStorageRead(DRAFT) triggered by put-back on unloaded draft")
	}

	// Draft read completes with existing disk text -> merges text & emits DraftStorageWrite(DRAFT), STILL no OUTBOX delete!
	diskDraft := &corev1.PersistedDraftRecord{
		SchemaVersion: attachments.PersistedSchemaVersion,
		Scope:         scope,
		Text:          "disk draft text",
		UpdatedMs:     h.nowMs - 1000,
	}
	rawDraft, _ := attachments.EncodePersistedDraft(diskDraft)
	readDoneEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftStorageReadResult{
			DraftStorageReadResult: &corev1.DraftStorageReadResultEvent{OpId: readOp, Found: true, Data: rawDraft},
		},
	})
	if len(findDraftDeleteEffects(readDoneEff)) != 0 {
		t.Fatalf("whenKept barrier violated: deleted OUTBOX before merged DRAFT write succeeded!")
	}
	if got := h.draftView(scope).Text; got != "disk draft text\nput back msg" {
		t.Fatalf("expected disk text merged with put-back text, got %q", got)
	}
	draftWrites := findDraftWriteEffects(readDoneEff)
	if len(draftWrites) != 1 || draftWrites[0].Bucket != corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT {
		t.Fatalf("expected 1 DRAFT write after load, got %+v", draftWrites)
	}

	// Acknowledge DRAFT write -> NOW OUTBOX is deleted!
	writeDoneEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftStorageWriteResult{
			DraftStorageWriteResult: &corev1.DraftStorageWriteResultEvent{OpId: draftWrites[0].OpId, Ok: true},
		},
	})
	dels := findDraftDeleteEffects(writeDoneEff)
	if len(dels) != 1 || dels[0].Bucket != corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX {
		t.Fatalf("expected OUTBOX delete ONLY after merged DRAFT write succeeded, got %+v", dels)
	}
}

func TestAttachments_RemoveDuringInFlightDraftWrite_DefersLocalReleaseBehindCoalescedWrite(t *testing.T) {
	h := newAttHarness(t, true, true)
	scope := h.sessionScope()

	// Open empty draft so loadState == draftLoadLoaded.
	openEff := h.dispatch(&corev1.Event{Event: &corev1.Event_DraftOpen{DraftOpen: &corev1.DraftOpenAction{Scope: scope}}})
	for _, ef := range openEff.GetEffects() {
		if rd := ef.GetDraftStorageRead(); rd != nil {
			h.dispatch(&corev1.Event{Event: &corev1.Event_DraftStorageReadResult{DraftStorageReadResult: &corev1.DraftStorageReadResultEvent{OpId: rd.OpId, Found: false}}})
		}
	}

	// Add 2 images -> emits DraftStorageWrite #1 (keep write #1 IN FLIGHT!).
	addEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{
					pickedImage("a.jpg", 4, "src-def-a", true),
					pickedImage("b.jpg", 4, "src-def-b", true),
				},
			},
		},
	})
	write1 := findDraftWriteEffects(addEff)[0]
	itemAID := h.draftView(scope).Images[0].ItemId

	// WHILE write #1 is STILL in flight, remove item A!
	// Removal coalesces into write #2 -> MUST NOT emit AttachmentReleaseLocal(src-def-a) yet!
	remEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftRemoveItem{DraftRemoveItem: &corev1.DraftRemoveItemAction{Scope: scope, ItemId: itemAID}},
	})
	if len(findReleaseLocalEffects(remEff)) != 0 {
		t.Fatalf("AttachmentReleaseLocal must NOT precede coalesced draft write #2: %+v", findReleaseLocalEffects(remEff))
	}

	// Write #1 finishes -> triggers coalesced write #2 (snapshot without item A). STILL must NOT release src-def-a yet!
	w1Done := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftStorageWriteResult{DraftStorageWriteResult: &corev1.DraftStorageWriteResultEvent{OpId: write1.OpId, Ok: true}},
	})
	if len(findReleaseLocalEffects(w1Done)) != 0 {
		t.Fatalf("AttachmentReleaseLocal must NOT fire before coalesced write #2 completes!")
	}
	write2 := findDraftWriteEffects(w1Done)[0]

	// Write #2 finishes -> NOW AttachmentReleaseLocal(src-def-a) is emitted!
	w2Done := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftStorageWriteResult{DraftStorageWriteResult: &corev1.DraftStorageWriteResultEvent{OpId: write2.OpId, Ok: true}},
	})
	rels := findReleaseLocalEffects(w2Done)
	if len(rels) != 1 || len(rels[0].Refs) != 1 || rels[0].Refs[0] != "src-def-a" {
		t.Fatalf("expected AttachmentReleaseLocal([src-def-a]) right after coalesced write #2 succeeded, got %+v", rels)
	}
}

func TestAttachments_PreSendMachineSwitchABA_RejectsStaleAbortedRemoteID(t *testing.T) {
	h := newAttHarness(t, false, true)
	scope := h.sessionScope()

	// 1. Upload 1 image to READY on machineA (remoteID = "img-a-old").
	addEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{pickedImage("aba.jpg", 4, "src-aba", true)},
			},
		},
	})
	h.driveImageUploadReady(addEff, "prep-aba", "thumb-aba", 4, attTestHash(0xbc), "img-a-old", h.nowMs+600_000)

	// 2. Start DraftSendSession -> emits async DraftStorageWrite(OUTBOX) capturing "img-a-old".
	sendEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftSendSession{DraftSendSession: &corev1.DraftSendSessionAction{SessionId: h.sessionID, Mode: v1.SendMode_SEND_MODE_STEER}},
	})
	outWrite := findDraftWriteEffects(sendEff)[0]

	// 3. WHILE Outbox write is in flight, retarget A -> B -> A (aborting "img-a-old" and re-uploading "img-a-new" on machineA with gen=2)!
	toB := h.dispatch(&corev1.Event{Event: &corev1.Event_DraftSetTargetMachine{DraftSetTargetMachine: &corev1.DraftSetTargetMachineAction{Scope: scope, MachineId: h.machineB}}})
	backToA := h.dispatch(&corev1.Event{Event: &corev1.Event_DraftSetTargetMachine{DraftSetTargetMachine: &corev1.DraftSetTargetMachineAction{Scope: scope, MachineId: h.machineA}}})
	var beginA2 *attDispatchedCmd
	for _, sc := range append(findDispatchedCmds(toB), findDispatchedCmds(backToA)...) {
		if sc.cmd.GetBeginImageUpload() != nil && sc.dc.MachineId == h.machineA {
			cp := sc
			beginA2 = &cp
		}
	}
	if beginA2 == nil {
		t.Fatalf("expected fresh BeginImageUpload on machineA after A->B->A")
	}
	h.settleCmdOK(beginA2.dc, beginCompleteOK(beginA2.cmd.GetBeginImageUpload().TransferId, "img-a-new", "", h.nowMs+600_000))

	// 4. Now the earlier Outbox write completes OK -> MUST NOT send stale "img-a-old", MUST delete orphan outbox!
	postWrite := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftStorageWriteResult{
			DraftStorageWriteResult: &corev1.DraftStorageWriteResultEvent{OpId: outWrite.OpId, Ok: true},
		},
	})
	for _, sc := range findDispatchedCmds(postWrite) {
		if sc.cmd.GetSend() != nil {
			t.Fatalf("pre-send recheck MUST reject stale/aborted remoteID after A->B->A, but submitted SendRequest: %+v", sc.cmd.GetSend())
		}
	}
	dels := findDraftDeleteEffects(postWrite)
	if len(dels) != 1 || dels[0].Bucket != corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX {
		t.Fatalf("expected orphan pre-send OUTBOX deleted on generation/remoteID mismatch, got %+v", dels)
	}
}

func TestAttachments_AddItemsWhileDraftLoading_ReleasesPickedHandlesAndReservation(t *testing.T) {
	h := newAttHarness(t, true, true)
	scope := h.sessionScope()

	// Reserve slots before open, then start DraftOpen (loadState == draftLoadLoading).
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftReserveSlots{
			DraftReserveSlots: &corev1.DraftReserveSlotsAction{Scope: scope, ReservationId: "res-load", ImageSlots: 2},
		},
	})
	h.dispatch(&corev1.Event{Event: &corev1.Event_DraftOpen{DraftOpen: &corev1.DraftOpenAction{Scope: scope}}})

	// Pick items arrive while draft is STILL loading -> MUST release reservation, show restore_wait notice, AND emit AttachmentReleaseLocal!
	addEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope:         scope,
				ReservationId: "res-load",
				Items: []*corev1.DraftPickedItem{
					{
						Candidate:         &corev1.AttachmentCandidateInput{Name: "early.jpg", MimeType: "image/jpeg", SizeBytes: 4},
						SourceRef:         "src-early-load",
						ThumbRef:          "thumb-early-load",
						HasOriginalSource: true,
					},
				},
			},
		},
	})
	dv := h.draftView(scope)
	if dv.ReservedImages != 0 || dv.HasPendingPicks {
		t.Fatalf("expected reservation released even when add rejected during load, got reserved=%d pending=%v", dv.ReservedImages, dv.HasPendingPicks)
	}
	if dv.Notice == nil || dv.Notice.GetKey() != text.KeyAttachmentRestoreWait {
		t.Fatalf("expected attachment.restore_wait notice, got %+v", dv.Notice)
	}
	rels := findReleaseLocalEffects(addEff)
	if len(rels) != 1 || len(rels[0].Refs) != 2 {
		t.Fatalf("expected AttachmentReleaseLocal for both rejected refs, got %+v", rels)
	}
}

func TestAttachments_DraftEventBeforeConfig_HoldsEditUntilConfigAndNeverOverwritesDisk(t *testing.T) {
	// Fresh Core WITHOUT AttachmentConfigEvent yet!
	c := New()
	defer c.Close()
	scope := &corev1.DraftScope{Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION, Id: "s-precfg"}

	// 1. User types text BEFORE AttachmentConfigEvent arrives -> must NOT emit DraftStorageWrite or mark loaded!
	earlyEff := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_000, MonoMs: 10_000,
		Event: &corev1.Event_DraftSetText{DraftSetText: &corev1.DraftSetTextAction{Scope: scope, Text: "typed before config"}},
	})
	if len(findDraftWriteEffects(earlyEff)) != 0 {
		t.Fatalf("must NOT write draft before AttachmentConfigEvent arrives")
	}
	if c.m.attachments.drafts["session:s-precfg"].loadState != draftLoadUnloaded {
		t.Fatalf("draft touched before config must stay unloaded until config arrives")
	}

	// 2. AttachmentConfigEvent arrives with PersistDrafts=true -> emits DraftStorageRead(DRAFT) for the activated draft!
	cfgEff := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_000, MonoMs: 10_000,
		Event: &corev1.Event_AttachmentConfig{AttachmentConfig: &corev1.AttachmentConfigEvent{PersistDrafts: true, PersistOutbox: true}},
	})
	var readOp uint64
	for _, ef := range cfgEff.GetEffects() {
		if rd := ef.GetDraftStorageRead(); rd != nil {
			readOp = rd.OpId
		}
	}
	if readOp == 0 {
		t.Fatalf("expected DraftStorageRead after AttachmentConfigEvent arrives for pre-activated draft")
	}

	// 3. Disk read returns older text -> pre-config typing MUST win and flush back to disk!
	rawDisk, _ := attachments.EncodePersistedDraft(&corev1.PersistedDraftRecord{
		SchemaVersion: attachments.PersistedSchemaVersion,
		Scope:         scope,
		Text:          "older disk text",
		UpdatedMs:     1_791_078_000_000,
	})
	readEff := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_000, MonoMs: 10_000,
		Event: &corev1.Event_DraftStorageReadResult{DraftStorageReadResult: &corev1.DraftStorageReadResultEvent{OpId: readOp, Found: true, Data: rawDisk}},
	})
	vm, _ := c.View(&corev1.Screen{Screen: &corev1.Screen_AttachmentDraft{AttachmentDraft: &corev1.AttachmentDraftScreenRequest{Scope: scope}}})
	if got := vm.GetAttachmentDraft().GetDraft().GetText(); got != "typed before config" {
		t.Fatalf("pre-config user typing must win over stale disk draft, got %q", got)
	}
	if len(findDraftWriteEffects(readEff)) != 1 {
		t.Fatalf("expected merged draft flushed to disk after load, got %+v", findDraftWriteEffects(readEff))
	}
}

func TestAttachments_StaleReadChunkPutAndHashAfterPauseCancelRestart(t *testing.T) {
	h := newAttHarness(t, false, false)
	scope := h.sessionScope()

	// 1. Late HashFileResult after item removed -> ignored without starting upload.
	eff1 := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{pickedFile("stale-hash.bin", "application/octet-stream", 8, "src-sh")},
			},
		},
	})
	hashOp := findHashEffects(eff1)[0].OpId
	item1ID := h.draftView(scope).Files[0].ItemId
	h.dispatch(&corev1.Event{Event: &corev1.Event_DraftRemoveItem{DraftRemoveItem: &corev1.DraftRemoveItemAction{Scope: scope, ItemId: item1ID}}})
	lateHash := h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentHashFileResult{
			AttachmentHashFileResult: &corev1.AttachmentHashFileResultEvent{OpId: hashOp, Ok: true, SizeBytes: 8, Sha256: attTestHash(0xc1)},
		},
	})
	if len(findDispatchedCmds(lateHash)) != 0 {
		t.Fatalf("stale HashFileResult after cancel/remove must NOT start upload")
	}

	// 2. Late ReadChunkResult after Pause -> ignored without emitting PutImageChunk!
	eff2 := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{pickedFile("stale-chunk.bin", "application/octet-stream", 8, "src-sc")},
			},
		},
	})
	hOp2 := findHashEffects(eff2)[0].OpId
	eff2 = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentHashFileResult{
			AttachmentHashFileResult: &corev1.AttachmentHashFileResultEvent{OpId: hOp2, Ok: true, SizeBytes: 8, Sha256: attTestHash(0xc2)},
		},
	})
	bCmd := findDispatchedCmds(eff2)[0]
	tid := bCmd.cmd.GetBeginImageUpload().TransferId
	eff2 = h.settleCmdOK(bCmd.dc, beginOK(tid, 0, 4, 0))
	staleReadOp := findReadChunkEffects(eff2)[0].OpId
	item2ID := h.draftView(scope).Files[0].ItemId

	// Pause BEFORE ReadChunkResult arrives.
	h.dispatch(&corev1.Event{Event: &corev1.Event_AttachmentPauseUpload{AttachmentPauseUpload: &corev1.AttachmentPauseUploadAction{Scope: scope, ItemId: item2ID}}})
	lateRead := h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentReadChunkResult{
			AttachmentReadChunkResult: &corev1.AttachmentReadChunkResultEvent{OpId: staleReadOp, Ok: true, Offset: 0, Data: []byte("1234")},
		},
	})
	if len(findDispatchedCmds(lateRead)) != 0 {
		t.Fatalf("stale ReadChunkResult after pause must NOT emit PutImageChunk")
	}

	// 3. Resume -> ReadChunk -> PutImageChunk in flight -> manual Retry (restarts with fresh transfer_id) ->
	// late PutImageChunk outcome from OLD transfer_id arrives -> MUST be ignored without corrupting new upload!
	resEff := h.dispatch(&corev1.Event{Event: &corev1.Event_AttachmentResumeUpload{AttachmentResumeUpload: &corev1.AttachmentResumeUploadAction{Scope: scope, ItemId: item2ID}}})
	stCmd := findDispatchedCmds(resEff)[0]
	resEff = h.settleCmdOK(stCmd.dc, statusOK(tid, 0, 4))
	rOp := findReadChunkEffects(resEff)[0].OpId
	putEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentReadChunkResult{
			AttachmentReadChunkResult: &corev1.AttachmentReadChunkResultEvent{OpId: rOp, Ok: true, Offset: 0, Data: []byte("1234")},
		},
	})
	oldPutCmd := findDispatchedCmds(putEff)[0]

	// Force restart via DraftRetryItem (recomputes hash, bumps generation).
	h.dispatch(&corev1.Event{Event: &corev1.Event_DraftRetryItem{DraftRetryItem: &corev1.DraftRetryItemAction{Scope: scope, ItemId: item2ID}}})
	// Late PutImageChunk from old generation arrives -> must NOT advance progress or read next chunk!
	latePut := h.settleCmdOK(oldPutCmd.dc, putOK(tid, 4))
	if len(findReadChunkEffects(latePut)) != 0 || len(findDispatchedCmds(latePut)) != 0 {
		t.Fatalf("stale PutImageChunk outcome after restart must be ignored, got reads=%d cmds=%d", len(findReadChunkEffects(latePut)), len(findDispatchedCmds(latePut)))
	}
	if h.draftView(scope).Files[0].ProgressPercent != 0 {
		t.Fatalf("stale PutImageChunk must NOT pollute restarted upload progress, got %d%%", h.draftView(scope).Files[0].ProgressPercent)
	}
}

func TestAttachments_StartupScanHydratesLoadingDraft_IgnoresLateReadResult(t *testing.T) {
	c := New()
	defer c.Close()
	scope := &corev1.DraftScope{Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION, Id: "s-race"}
	nowMs := int64(1_791_078_060_000)

	// 1. Configure with ScanStorage=true -> emits DraftStorageListEffect.
	cfgEff := c.Dispatch(&corev1.Event{
		NowMs: nowMs, MonoMs: 10_000,
		Event: &corev1.Event_AttachmentConfig{
			AttachmentConfig: &corev1.AttachmentConfigEvent{
				PersistDrafts: true,
				PersistOutbox: true,
				ScanStorage:   true,
			},
		},
	})
	var listOp uint64
	for _, ef := range cfgEff.GetEffects() {
		if ls := ef.GetDraftStorageList(); ls != nil {
			listOp = ls.OpId
		}
	}
	if listOp == 0 {
		t.Fatalf("expected DraftStorageListEffect on AttachmentConfig(ScanStorage=true)")
	}

	// 2. DraftOpen arrives BEFORE DraftStorageListed -> transitions draft to draftLoadLoading and emits DraftStorageReadEffect.
	openEff := c.Dispatch(&corev1.Event{
		NowMs: nowMs, MonoMs: 10_010,
		Event: &corev1.Event_DraftOpen{
			DraftOpen: &corev1.DraftOpenAction{Scope: scope},
		},
	})
	var readOp uint64
	for _, ef := range openEff.GetEffects() {
		if rd := ef.GetDraftStorageRead(); rd != nil {
			readOp = rd.OpId
		}
	}
	if readOp == 0 {
		t.Fatalf("expected DraftStorageReadEffect on DraftOpen while scan is in flight")
	}

	rawDisk, err := attachments.EncodePersistedDraft(&corev1.PersistedDraftRecord{
		SchemaVersion: attachments.PersistedSchemaVersion,
		Scope:         scope,
		Text:          "hello",
		UpdatedMs:     nowMs - 1_000,
	})
	if err != nil {
		t.Fatal(err)
	}

	// 3. Startup scan completes first -> hydrates draft, clears activeLoadOpID, and marks draft dirty.
	listedEff := c.Dispatch(&corev1.Event{
		NowMs: nowMs, MonoMs: 10_020,
		Event: &corev1.Event_DraftStorageListed{
			DraftStorageListed: &corev1.DraftStorageListedEvent{
				OpId: listOp,
				Entries: []*corev1.DraftStorageEntry{
					{
						Bucket: corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT,
						Key:    attachments.ScopeStorageKey(scope),
						Data:   rawDisk,
					},
				},
			},
		},
	})
	var sawDraftRender bool
	for _, ef := range listedEff.GetEffects() {
		if r := ef.GetRender(); r != nil {
			for _, s := range r.Screens {
				if s == corev1.ScreenTarget_SCREEN_TARGET_ATTACHMENT_DRAFT {
					sawDraftRender = true
				}
			}
		}
	}
	if !sawDraftRender {
		t.Fatalf("expected DraftStorageListed hydrating an open draft to mark ATTACHMENT_DRAFT dirty")
	}

	viewDraft := func() *corev1.AttachmentDraftView {
		vm, err := c.View(&corev1.Screen{
			Screen: &corev1.Screen_AttachmentDraft{
				AttachmentDraft: &corev1.AttachmentDraftScreenRequest{Scope: scope},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		return vm.GetAttachmentDraft().GetDraft()
	}
	if dv := viewDraft(); dv.GetText() != "hello" || dv.GetRestoring() {
		t.Fatalf("expected hydrated text \"hello\" and restoring=false, got text=%q restoring=%v", dv.GetText(), dv.GetRestoring())
	}

	// 4. Late DraftStorageReadResult arrives -> MUST be ignored without duplicating text ("hello\nhello")!
	c.Dispatch(&corev1.Event{
		NowMs: nowMs, MonoMs: 10_030,
		Event: &corev1.Event_DraftStorageReadResult{
			DraftStorageReadResult: &corev1.DraftStorageReadResultEvent{
				OpId:   readOp,
				Bucket: corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT,
				Key:    attachments.ScopeStorageKey(scope),
				Found:  true,
				Data:   rawDisk,
			},
		},
	})
	if got := viewDraft().GetText(); got != "hello" {
		t.Fatalf("late DraftStorageReadResult must not merge text twice, got %q", got)
	}
}

func TestAttachments_NewSessionTextOnlySubmit_ClearsSubmittedRevisionAndPreservesNewEdits(t *testing.T) {
	h := newAttHarness(t, true, true)
	scope := h.newSessionScope()

	h.dispatch(&corev1.Event{
		Event: &corev1.Event_NewSessionOpen{NewSessionOpen: &corev1.NewSessionOpenAction{ProjectKey: h.project}},
	})
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_NewSessionSelectMachine{NewSessionSelectMachine: &corev1.NewSessionSelectMachineAction{MachineId: h.machineA}},
	})
	openEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftOpen{DraftOpen: &corev1.DraftOpenAction{Scope: scope, MachineId: h.machineA}},
	})
	for _, ef := range openEff.GetEffects() {
		if rd := ef.GetDraftStorageRead(); rd != nil {
			h.dispatch(&corev1.Event{
				Event: &corev1.Event_DraftStorageReadResult{
					DraftStorageReadResult: &corev1.DraftStorageReadResultEvent{OpId: rd.OpId, Found: false},
				},
			})
		}
	}

	viewNS := func() *corev1.NewSessionViewModel {
		vm, err := h.core.View(&corev1.Screen{
			Screen: &corev1.Screen_NewSession{NewSession: &corev1.NewSessionScreenRequest{ProjectKey: h.project}},
		})
		if err != nil {
			t.Fatal(err)
		}
		return vm.GetNewSession()
	}

	submitAndFindCreate := func() attDispatchedCmd {
		subEff := h.dispatch(&corev1.Event{
			Event: &corev1.Event_NewSessionSubmit{
				NewSessionSubmit: &corev1.NewSessionSubmitAction{ProjectKey: h.project},
			},
		})
		for _, sc := range findDispatchedCmds(subEff) {
			if sc.cmd.GetCreateSession() != nil {
				return sc
			}
		}
		t.Fatalf("expected CreateSession dispatched")
		return attDispatchedCmd{}
	}

	// Case 1: Text-only submit without subsequent edits -> settles OK -> clears draft text, prompt, and deletes disk draft.
	setEff1 := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftSetText{DraftSetText: &corev1.DraftSetTextAction{Scope: scope, Text: "first task"}},
	})
	for _, dw := range findDraftWriteEffects(setEff1) {
		h.dispatch(&corev1.Event{
			Event: &corev1.Event_DraftStorageWriteResult{
				DraftStorageWriteResult: &corev1.DraftStorageWriteResultEvent{OpId: dw.OpId, Ok: true},
			},
		})
	}
	create1 := submitAndFindCreate()
	if create1.cmd.GetCreateSession().GetPrompt() != "first task" {
		t.Fatalf("expected prompt \"first task\", got %q", create1.cmd.GetCreateSession().GetPrompt())
	}
	settle1 := h.settleCmdOK(create1.dc, &v1.CommandResult{
		Kind: &v1.CommandResult_CreateSession{
			CreateSession: &v1.CreateSessionResponse{
				Session: &v1.Session{Id: "s-text-1", Cwd: h.project, Agent: v1.Agent_AGENT_CODEX, Status: v1.SessionStatus_SESSION_STATUS_RUNNING},
			},
		},
	})
	if got := h.draftView(scope).GetText(); got != "" {
		t.Fatalf("expected text-only draft cleared after CreateSession Executed, got %q", got)
	}
	if got := viewNS().GetPrompt(); got != "" {
		t.Fatalf("expected NewSessionViewModel.Prompt cleared after CreateSession Executed, got %q", got)
	}
	if dels := findDraftDeleteEffects(settle1); len(dels) != 1 || dels[0].Bucket != corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT {
		t.Fatalf("expected empty draft deleted from storage on settlement, got %+v", dels)
	}

	// Case 2: Text-only submit where user types new text BEFORE CreateSession settles -> preserves new text!
	setEff2 := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftSetText{DraftSetText: &corev1.DraftSetTextAction{Scope: scope, Text: "second task"}},
	})
	for _, dw := range findDraftWriteEffects(setEff2) {
		h.dispatch(&corev1.Event{
			Event: &corev1.Event_DraftStorageWriteResult{
				DraftStorageWriteResult: &corev1.DraftStorageWriteResultEvent{OpId: dw.OpId, Ok: true},
			},
		})
	}
	create2 := submitAndFindCreate()
	setEff2New := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftSetText{DraftSetText: &corev1.DraftSetTextAction{Scope: scope, Text: "typed while starting"}},
	})
	for _, dw := range findDraftWriteEffects(setEff2New) {
		h.dispatch(&corev1.Event{
			Event: &corev1.Event_DraftStorageWriteResult{
				DraftStorageWriteResult: &corev1.DraftStorageWriteResultEvent{OpId: dw.OpId, Ok: true},
			},
		})
	}
	settle2 := h.settleCmdOK(create2.dc, &v1.CommandResult{
		Kind: &v1.CommandResult_CreateSession{
			CreateSession: &v1.CreateSessionResponse{
				Session: &v1.Session{Id: "s-text-2", Cwd: h.project, Agent: v1.Agent_AGENT_CODEX, Status: v1.SessionStatus_SESSION_STATUS_RUNNING},
			},
		},
	})
	if got := h.draftView(scope).GetText(); got != "typed while starting" {
		t.Fatalf("expected newly typed text preserved in draft, got %q", got)
	}
	if got := viewNS().GetPrompt(); got != "typed while starting" {
		t.Fatalf("expected newly typed text preserved in NewSessionViewModel.Prompt, got %q", got)
	}
	if dels := findDraftDeleteEffects(settle2); len(dels) != 0 {
		t.Fatalf("must not delete draft storage when new text was typed during submit, got %+v", dels)
	}

	// Case 3: Re-typing the same text (bumping textRev) during submit also preserves it!
	create3 := submitAndFindCreate()
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftSetText{DraftSetText: &corev1.DraftSetTextAction{Scope: scope, Text: ""}},
	})
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftSetText{DraftSetText: &corev1.DraftSetTextAction{Scope: scope, Text: "typed while starting"}},
	})
	h.settleCmdOK(create3.dc, &v1.CommandResult{
		Kind: &v1.CommandResult_CreateSession{
			CreateSession: &v1.CreateSessionResponse{
				Session: &v1.Session{Id: "s-text-3", Cwd: h.project, Agent: v1.Agent_AGENT_CODEX, Status: v1.SessionStatus_SESSION_STATUS_RUNNING},
			},
		},
	})
	if got := h.draftView(scope).GetText(); got != "typed while starting" {
		t.Fatalf("expected newer revision of same text preserved after settle, got %q", got)
	}
	if got := viewNS().GetPrompt(); got != "typed while starting" {
		t.Fatalf("expected NewSessionViewModel.Prompt preserved for newer revision, got %q", got)
	}
}

func TestAttachments_NonRetryablePrepareFailure_DisablesCanRetryAndIgnoresRetryAction(t *testing.T) {
	cases := []struct {
		name      string
		errKind   corev1.AttachmentIOErrorKind
		wantKind  corev1.ImageProblemKind
		wantRetry bool
	}{
		{
			name:      "decode",
			errKind:   corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_DECODE,
			wantKind:  corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_DECODE,
			wantRetry: false,
		},
		{
			name:      "dimensions",
			errKind:   corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_DIMENSIONS,
			wantKind:  corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_DIMENSIONS,
			wantRetry: false,
		},
		{
			name:      "original_too_large",
			errKind:   corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_ORIGINAL_TOO_LARGE,
			wantKind:  corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_ORIGINAL_TOO_LARGE,
			wantRetry: false,
		},
		{
			name:      "storage_full",
			errKind:   corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_STORAGE_FULL,
			wantKind:  corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_STORAGE_FULL,
			wantRetry: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newAttHarness(t, false, false)
			scope := h.sessionScope()

			addEff := h.dispatch(&corev1.Event{
				Event: &corev1.Event_DraftAddItems{
					DraftAddItems: &corev1.DraftAddItemsAction{
						Scope: scope,
						Items: []*corev1.DraftPickedItem{pickedImage(tc.name+".jpg", 1024, "src-"+tc.name, true)},
					},
				},
			})
			prepOp := findPrepareEffects(addEff)[0].OpId
			h.dispatch(&corev1.Event{
				Event: &corev1.Event_AttachmentPrepareImageResult{
					AttachmentPrepareImageResult: &corev1.AttachmentPrepareImageResultEvent{
						OpId:      prepOp,
						Ok:        false,
						ErrorKind: tc.errKind,
					},
				},
			})

			dv := h.draftView(scope)
			if len(dv.Images) != 1 {
				t.Fatalf("expected 1 image, got %d", len(dv.Images))
			}
			img := dv.Images[0]
			if img.State != corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_FAILED {
				t.Fatalf("expected FAILED state, got %v", img.State)
			}
			if img.Problem == nil || img.Problem.Kind != tc.wantKind {
				t.Fatalf("expected problem kind %v, got %+v", tc.wantKind, img.Problem)
			}
			if img.CanRetry != tc.wantRetry {
				t.Fatalf("expected CanRetry=%v for %s, got %v", tc.wantRetry, tc.name, img.CanRetry)
			}

			retryEff := h.dispatch(&corev1.Event{
				Event: &corev1.Event_DraftRetryItem{
					DraftRetryItem: &corev1.DraftRetryItemAction{Scope: scope, ItemId: img.ItemId},
				},
			})
			prepAfter := findPrepareEffects(retryEff)
			if tc.wantRetry && len(prepAfter) != 1 {
				t.Fatalf("expected retry to re-prepare for retryable error %s, got %d effects", tc.name, len(prepAfter))
			}
			if !tc.wantRetry && len(prepAfter) != 0 {
				t.Fatalf("expected retry ignored for non-retryable error %s, got %+v", tc.name, prepAfter)
			}
		})
	}
}

func TestAttachments_LoweredAttachmentLimits_BlockCanSendAndSendActions(t *testing.T) {
	h := newAttHarness(t, false, true)
	scope := h.sessionScope()

	setLimits := func(maxImgs, maxFiles int32) {
		lm := h.core.m.findMachine(h.machineA)
		if lm == nil || lm.Meta == nil || lm.Meta.Capabilities == nil {
			t.Fatalf("missing machineA capabilities")
		}
		lm.Meta.Capabilities.MaxImagesPerMessage = maxImgs
		lm.Meta.Capabilities.MaxFilesPerMessage = maxFiles
	}

	// 1. Add and upload 2 ready images while limit is default (6).
	addImg1 := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{pickedImage("img1.jpg", 4, "src-lim-1", true)},
			},
		},
	})
	h.driveImageUploadReady(addImg1, "prep-lim-1", "thumb-lim-1", 4, attTestHash(0xd1), "img-lim-1", h.nowMs+600_000)

	addImg2 := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{pickedImage("img2.jpg", 4, "src-lim-2", true)},
			},
		},
	})
	h.driveImageUploadReady(addImg2, "prep-lim-2", "thumb-lim-2", 4, attTestHash(0xd2), "img-lim-2", h.nowMs+600_000)

	if dv := h.draftView(scope); !dv.CanSend || dv.SendBlockedReason != nil {
		t.Fatalf("expected CanSend=true before limit lowered, got canSend=%v reason=%+v", dv.CanSend, dv.SendBlockedReason)
	}

	// 2. Lower max_images_per_message to 1 -> CanSend becomes false with attachment.max_images reason, and DraftSendSession is blocked.
	setLimits(1, 6)
	dv := h.draftView(scope)
	if dv.CanSend || dv.Ready {
		t.Fatalf("expected CanSend=false and Ready=false when images exceed lowered limit, got canSend=%v ready=%v", dv.CanSend, dv.Ready)
	}
	if dv.SendBlockedReason == nil || dv.SendBlockedReason.GetKey() != text.KeyAttachmentMaxImages {
		t.Fatalf("expected SendBlockedReason attachment.max_images, got %+v", dv.SendBlockedReason)
	}
	blockedSend := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftSendSession{
			DraftSendSession: &corev1.DraftSendSessionAction{SessionId: h.sessionID, Mode: v1.SendMode_SEND_MODE_STEER},
		},
	})
	if len(findDispatchedCmds(blockedSend)) != 0 || len(findDraftWriteEffects(blockedSend)) != 0 {
		t.Fatalf("DraftSendSession must be blocked when images exceed limit, got cmds=%d writes=%d", len(findDispatchedCmds(blockedSend)), len(findDraftWriteEffects(blockedSend)))
	}
	if dv = h.draftView(scope); dv.Notice == nil || dv.Notice.GetKey() != text.KeyAttachmentMaxImages {
		t.Fatalf("expected draft notice attachment.max_images on blocked send, got %+v", dv.Notice)
	}

	// 3. Raise limit back to 6, start pre-send outbox write, then lower max_images_per_message to 1 BEFORE write completes -> recheck must abort & delete orphan outbox!
	setLimits(6, 6)
	sendStart := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftSendSession{
			DraftSendSession: &corev1.DraftSendSessionAction{SessionId: h.sessionID, Mode: v1.SendMode_SEND_MODE_STEER},
		},
	})
	outWrite := findDraftWriteEffects(sendStart)[0]
	setLimits(1, 6)
	postWrite := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftStorageWriteResult{
			DraftStorageWriteResult: &corev1.DraftStorageWriteResultEvent{OpId: outWrite.OpId, Ok: true},
		},
	})
	for _, sc := range findDispatchedCmds(postWrite) {
		if sc.cmd.GetSend() != nil {
			t.Fatalf("pre-send outbox recheck must block send when limit lowered during write")
		}
	}
	if dels := findDraftDeleteEffects(postWrite); len(dels) != 1 || dels[0].Bucket != corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX {
		t.Fatalf("expected orphan outbox deleted after limit recheck failure, got %+v", dels)
	}
	if dv = h.draftView(scope); dv.Notice == nil || dv.Notice.GetKey() != text.KeyAttachmentMaxImages {
		t.Fatalf("expected notice attachment.max_images after outbox recheck failure, got %+v", dv.Notice)
	}

	// 4. Clear images, add 2 ready files, lower max_files_per_message to 1 -> CanSend becomes false and DraftSendSession is blocked with attachment.max_files.
	h.dispatch(&corev1.Event{Event: &corev1.Event_DraftClear{DraftClear: &corev1.DraftClearAction{Scope: scope}}})
	setLimits(6, 6)
	for i, name := range []string{"f1.txt", "f2.txt"} {
		addF := h.dispatch(&corev1.Event{
			Event: &corev1.Event_DraftAddItems{
				DraftAddItems: &corev1.DraftAddItemsAction{
					Scope: scope,
					Items: []*corev1.DraftPickedItem{pickedFile(name, "text/plain", 4, "src-f-"+name)},
				},
			},
		})
		hashOp := findHashEffects(addF)[0].OpId
		hEff := h.dispatch(&corev1.Event{
			Event: &corev1.Event_AttachmentHashFileResult{
				AttachmentHashFileResult: &corev1.AttachmentHashFileResultEvent{
					OpId: hashOp, Ok: true, SizeBytes: 4, Sha256: attTestHash(byte(0xe1 + i)),
				},
			},
		})
		bCmd := findDispatchedCmds(hEff)[0]
		h.settleCmdOK(bCmd.dc, beginCompleteOK(bCmd.cmd.GetBeginImageUpload().TransferId, "", "file-lim-"+name, h.nowMs+600_000))
	}
	setLimits(6, 1)
	dv = h.draftView(scope)
	if dv.CanSend || dv.Ready {
		t.Fatalf("expected CanSend=false and Ready=false when files exceed lowered limit")
	}
	if dv.SendBlockedReason == nil || dv.SendBlockedReason.GetKey() != text.KeyAttachmentMaxFiles {
		t.Fatalf("expected SendBlockedReason attachment.max_files, got %+v", dv.SendBlockedReason)
	}
	blockedFileSend := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftSendSession{
			DraftSendSession: &corev1.DraftSendSessionAction{SessionId: h.sessionID, Mode: v1.SendMode_SEND_MODE_STEER},
		},
	})
	if len(findDispatchedCmds(blockedFileSend)) != 0 || len(findDraftWriteEffects(blockedFileSend)) != 0 {
		t.Fatalf("DraftSendSession must be blocked when files exceed limit")
	}
	if dv = h.draftView(scope); dv.Notice == nil || dv.Notice.GetKey() != text.KeyAttachmentMaxFiles {
		t.Fatalf("expected notice attachment.max_files on blocked file send, got %+v", dv.Notice)
	}

	// 5. NewSessionSubmit also respects lowered max_images_per_message.
	setLimits(6, 6)
	nsScope := h.newSessionScope()
	h.dispatch(&corev1.Event{Event: &corev1.Event_NewSessionOpen{NewSessionOpen: &corev1.NewSessionOpenAction{ProjectKey: h.project}}})
	h.dispatch(&corev1.Event{Event: &corev1.Event_NewSessionSelectMachine{NewSessionSelectMachine: &corev1.NewSessionSelectMachineAction{MachineId: h.machineA}}})
	for i, name := range []string{"ns1.jpg", "ns2.jpg"} {
		addNS := h.dispatch(&corev1.Event{
			Event: &corev1.Event_DraftAddItems{
				DraftAddItems: &corev1.DraftAddItemsAction{
					Scope: nsScope,
					Items: []*corev1.DraftPickedItem{pickedImage(name, 4, "src-"+name, true)},
				},
			},
		})
		h.driveImageUploadReady(addNS, "prep-"+name, "thumb-"+name, 4, attTestHash(byte(0xf1+i)), "img-"+name, h.nowMs+600_000)
	}
	setLimits(1, 6)
	nsSub := h.dispatch(&corev1.Event{
		Event: &corev1.Event_NewSessionSubmit{NewSessionSubmit: &corev1.NewSessionSubmitAction{ProjectKey: h.project}},
	})
	for _, sc := range findDispatchedCmds(nsSub) {
		if sc.cmd.GetCreateSession() != nil {
			t.Fatalf("NewSessionSubmit must be blocked when images exceed lowered limit")
		}
	}
	if dv = h.draftView(nsScope); dv.CanSend || dv.SendBlockedReason == nil || dv.SendBlockedReason.GetKey() != text.KeyAttachmentMaxImages {
		t.Fatalf("expected NewSession draft blocked with attachment.max_images, got canSend=%v reason=%+v", dv.CanSend, dv.SendBlockedReason)
	}
}

func TestAttachments_StorageLockedByOther_AndConfigUpdate_AndBadgeText(t *testing.T) {
	// 1. StorageLockedByOther disables persistence & scan and exposes localized storage_error notice.
	c := New()
	defer c.Close()
	scope := &corev1.DraftScope{Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION, Id: "s-locked"}
	cfgEff := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_000, MonoMs: 10_000,
		Event: &corev1.Event_AttachmentConfig{
			AttachmentConfig: &corev1.AttachmentConfigEvent{
				PersistDrafts:             true,
				PersistOutbox:             true,
				ScanStorage:               true,
				HasPersistedPreparedImages: true,
				StorageLockedByOther:      true,
			},
		},
	})
	for _, ef := range cfgEff.GetEffects() {
		if ef.GetDraftStorageList() != nil {
			t.Fatalf("must NOT emit DraftStorageListEffect when StorageLockedByOther=true")
		}
	}
	setEff := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_000, MonoMs: 10_010,
		Event: &corev1.Event_DraftSetText{DraftSetText: &corev1.DraftSetTextAction{Scope: scope, Text: "in-memory only"}},
	})
	if len(findDraftWriteEffects(setEff)) != 0 {
		t.Fatalf("must NOT persist draft when StorageLockedByOther=true")
	}
	vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_AttachmentDraft{AttachmentDraft: &corev1.AttachmentDraftScreenRequest{Scope: scope}}})
	if err != nil {
		t.Fatal(err)
	}
	se := vm.GetAttachmentDraft().GetDraft().GetStorageError()
	if se == nil || se.GetKey() != text.KeyAttachmentOtherTabOwnsStorage {
		t.Fatalf("expected StorageError attachment.other_tab_owns_storage, got %+v", se)
	}

	// 2. Runtime AttachmentConfig update (e.g. OPFS degradation) updates HasPersistedPreparedImages without rescanning.
	h := newAttHarness(t, true, true)
	reCfg := h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentConfig{
			AttachmentConfig: &corev1.AttachmentConfigEvent{
				PersistDrafts:             true,
				PersistOutbox:             true,
				ScanStorage:               false,
				HasPersistedPreparedImages: false,
			},
		},
	})
	for _, ef := range reCfg.GetEffects() {
		if ef.GetDraftStorageList() != nil {
			t.Fatalf("config update with ScanStorage=false must not rescan")
		}
	}
	if h.core.m.attachments.hasPersistedPreparedImgs {
		t.Fatalf("expected hasPersistedPreparedImgs updated to false")
	}

	// 3. BadgeText covers PAUSED (resumable vs offline) and FAILED (image vs retryable file).
	sessScope := h.sessionScope()
	openEff := h.dispatch(&corev1.Event{Event: &corev1.Event_DraftOpen{DraftOpen: &corev1.DraftOpenAction{Scope: sessScope}}})
	for _, ef := range openEff.GetEffects() {
		if rd := ef.GetDraftStorageRead(); rd != nil {
			h.dispatch(&corev1.Event{Event: &corev1.Event_DraftStorageReadResult{DraftStorageReadResult: &corev1.DraftStorageReadResultEvent{OpId: rd.OpId, Found: false}}})
		}
	}
	addImg := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: sessScope,
				Items: []*corev1.DraftPickedItem{pickedImage("bad.jpg", 4, "src-bad", true)},
			},
		},
	})
	prepOp := findPrepareEffects(addImg)[0].OpId
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentPrepareImageResult{
			AttachmentPrepareImageResult: &corev1.AttachmentPrepareImageResultEvent{
				OpId: prepOp, Ok: false, ErrorKind: corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_DECODE,
			},
		},
	})
	dv := h.draftView(sessScope)
	if dv.Images[0].BadgeText == nil || dv.Images[0].BadgeText.GetKey() != text.KeyAttachmentFailed {
		t.Fatalf("expected failed image BadgeText attachment.failed, got %+v", dv.Images[0].BadgeText)
	}

	addFile := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: sessScope,
				Items: []*corev1.DraftPickedItem{pickedFile("doc.txt", "text/plain", 8, "src-doc")},
			},
		},
	})
	hashOp := findHashEffects(addFile)[0].OpId
	hEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentHashFileResult{
			AttachmentHashFileResult: &corev1.AttachmentHashFileResultEvent{
				OpId: hashOp, Ok: true, SizeBytes: 8, Sha256: attTestHash(0xab),
			},
		},
	})
	fileID := h.draftView(sessScope).Files[0].ItemId
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentPauseUpload{
			AttachmentPauseUpload: &corev1.AttachmentPauseUploadAction{Scope: sessScope, ItemId: fileID},
		},
	})
	dv = h.draftView(sessScope)
	if dv.Files[0].BadgeText == nil || dv.Files[0].BadgeText.GetKey() != text.KeyScheduleResume {
		t.Fatalf("expected resumable paused file BadgeText schedule.resume, got %+v", dv.Files[0].BadgeText)
	}
	bCmd := findDispatchedCmds(hEff)[0]
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentResumeUpload{
			AttachmentResumeUpload: &corev1.AttachmentResumeUploadAction{Scope: sessScope, ItemId: fileID},
		},
	})
	_ = bCmd
	// Fail file upload via hash failure -> retryable file BadgeText is common.retry.
	addFile2 := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: sessScope,
				Items: []*corev1.DraftPickedItem{pickedFile("doc2.txt", "text/plain", 8, "src-doc2")},
			},
		},
	})
	hashOp2 := findHashEffects(addFile2)[0].OpId
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentHashFileResult{
			AttachmentHashFileResult: &corev1.AttachmentHashFileResultEvent{
				OpId: hashOp2, Ok: false, ErrorKind: corev1.AttachmentIOErrorKind_ATTACHMENT_IO_ERROR_KIND_OTHER,
			},
		},
	})
	dv = h.draftView(sessScope)
	if dv.Files[1].BadgeText == nil || dv.Files[1].BadgeText.GetKey() != text.KeyCommonRetry {
		t.Fatalf("expected retryable failed file BadgeText common.retry, got %+v", dv.Files[1].BadgeText)
	}
}

func TestAttachments_DraftAppendText_QueuesDuringLoadAndPreservesPersistedText(t *testing.T) {
	// 1. Append while draft is loading preserves disk draft text and merges cleanup prompt after read completes.
	h := newAttHarness(t, true, true)
	nsScope := &corev1.DraftScope{Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_NEW_SESSION, Id: "/Users/me/proj"}
	diskBytes, err := attachments.EncodePersistedDraft(&corev1.PersistedDraftRecord{
		Scope:     nsScope,
		Text:      "existing saved draft",
		UpdatedMs: h.nowMs,
	})
	if err != nil {
		t.Fatal(err)
	}

	openEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftOpen{DraftOpen: &corev1.DraftOpenAction{Scope: nsScope, MachineId: "m1"}},
	})
	readOps := findDraftReadEffects(openEff)
	if len(readOps) != 1 {
		t.Fatalf("expected 1 DraftStorageReadEffect, got %d", len(readOps))
	}

	// Append cleanup prompt BEFORE disk read returns (simulating NewTaskScreen opening a project with saved draft).
	appendEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAppendText{
			DraftAppendText: &corev1.DraftAppendTextAction{
				Scope:     nsScope,
				Text:      "Clean up temporary files.",
				MachineId: "m1",
			},
		},
	})
	if len(findDraftWriteEffects(appendEff)) != 0 {
		t.Fatalf("must NOT write draft before disk read completes")
	}

	// Disk read returns existing saved draft -> Core hydrates disk text THEN merges queued append and persists.
	readResEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftStorageReadResult{
			DraftStorageReadResult: &corev1.DraftStorageReadResultEvent{
				OpId:   readOps[0].OpId,
				Bucket: corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT,
				Key:    readOps[0].Key,
				Found:  true,
				Data:   diskBytes,
			},
		},
	})
	want := "existing saved draft\n\nClean up temporary files."
	if got := h.draftView(nsScope).Text; got != want {
		t.Fatalf("merged draft text = %q, want %q", got, want)
	}
	writes := findDraftWriteEffects(readResEff)
	if len(writes) != 1 {
		t.Fatalf("expected 1 DraftStorageWriteEffect after flushing queued append, got %d", len(writes))
	}
	dec, ok := attachments.DecodePersistedDraft(writes[0].Data)
	if !ok || dec.Text != want {
		t.Fatalf("persisted draft text = %q (ok=%v), want %q", dec.GetText(), ok, want)
	}

	// Duplicate append on loaded draft is a no-op (no extra write).
	dupEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAppendText{
			DraftAppendText: &corev1.DraftAppendTextAction{
				Scope: nsScope,
				Text:  "Clean up temporary files.",
			},
		},
	})
	if len(findDraftWriteEffects(dupEff)) != 0 {
		t.Fatalf("duplicate DraftAppendText must not emit write")
	}
	if got := h.draftView(nsScope).Text; got != want {
		t.Fatalf("draft text changed on duplicate append: %q", got)
	}

	// 2. Append arriving before AttachmentConfigEvent is queued and merged once startup scan hydrates disk record.
	c := New()
	defer c.Close()
	c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_000, MonoMs: 10_000,
		Event: &corev1.Event_DraftAppendText{
			DraftAppendText: &corev1.DraftAppendTextAction{Scope: nsScope, Text: "Pre-config prompt"},
		},
	})
	cfgEff := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_000, MonoMs: 10_010,
		Event: &corev1.Event_AttachmentConfig{
			AttachmentConfig: &corev1.AttachmentConfigEvent{
				PersistDrafts: true, PersistOutbox: true, ScanStorage: true, HasPersistedPreparedImages: true,
			},
		},
	})
	var listOp uint64
	for _, ef := range cfgEff.GetEffects() {
		if l := ef.GetDraftStorageList(); l != nil {
			listOp = l.OpId
		}
	}
	if listOp == 0 {
		t.Fatalf("expected DraftStorageListEffect")
	}
	listEff := c.Dispatch(&corev1.Event{
		NowMs: 1_791_078_060_000, MonoMs: 10_020,
		Event: &corev1.Event_DraftStorageListed{
			DraftStorageListed: &corev1.DraftStorageListedEvent{
				OpId: listOp,
				Entries: []*corev1.DraftStorageEntry{{
					Bucket: corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT,
					Key:    attachments.ScopeStorageKey(nsScope),
					Data:   diskBytes,
				}},
			},
		},
	})
	if len(findDraftWriteEffects(listEff)) != 1 {
		t.Fatalf("expected persisted write after startup scan flushed pre-config append")
	}
	vm, err := c.View(&corev1.Screen{Screen: &corev1.Screen_AttachmentDraft{AttachmentDraft: &corev1.AttachmentDraftScreenRequest{Scope: nsScope}}})
	if err != nil {
		t.Fatal(err)
	}
	wantPre := "existing saved draft\n\nPre-config prompt"
	if got := vm.GetAttachmentDraft().GetDraft().GetText(); got != wantPre {
		t.Fatalf("pre-config merged text = %q, want %q", got, wantPre)
	}

	// 3. Append on an absent disk draft (found=false) sets text to the addition and persists it.
	emptyScope := &corev1.DraftScope{Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_NEW_SESSION, Id: "/Users/me/empty"}
	appendEmpty := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAppendText{
			DraftAppendText: &corev1.DraftAppendTextAction{Scope: emptyScope, Text: "Only cleanup", MachineId: "m1"},
		},
	})
	rdEmpty := findDraftReadEffects(appendEmpty)
	if len(rdEmpty) != 1 {
		t.Fatalf("expected DraftStorageReadEffect for unloaded append, got %d", len(rdEmpty))
	}
	resEmpty := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftStorageReadResult{
			DraftStorageReadResult: &corev1.DraftStorageReadResultEvent{
				OpId: rdEmpty[0].OpId, Bucket: corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT, Key: rdEmpty[0].Key, Found: false,
			},
		},
	})
	if got := h.draftView(emptyScope).Text; got != "Only cleanup" {
		t.Fatalf("empty disk draft append = %q, want %q", got, "Only cleanup")
	}
	if len(findDraftWriteEffects(resEmpty)) != 1 {
		t.Fatalf("expected DraftStorageWriteEffect for empty disk draft append")
	}
}

func TestDraftRestoreAdvancesItemIDSequence(t *testing.T) {
	h := newAttHarness(t, true, true)
	scope := &corev1.DraftScope{Kind: corev1.DraftScopeKind_DRAFT_SCOPE_KIND_SESSION, Id: h.sessionID}
	persistedDraft, err := attachments.EncodePersistedDraft(&corev1.PersistedDraftRecord{
		SchemaVersion: attachments.PersistedSchemaVersion,
		Scope:         scope,
		Text:          "restored",
		UpdatedMs:     h.nowMs,
		Items: []*corev1.PersistedAttachmentItem{{
			ItemId:    "att-1",
			Kind:      corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE,
			MimeType:  "image/jpeg",
			SizeBytes: 1024,
			SourceRef: "blob:src-restored",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	persistedOutbox, err := attachments.EncodePersistedOutbox(&corev1.PersistedOutboxRecord{
		SchemaVersion: attachments.PersistedSchemaVersion,
		SessionId:     h.sessionID,
		RequestId:     "req-old",
		MachineId:     h.machineA,
		CreatedMs:     h.nowMs,
		Items: []*corev1.PersistedAttachmentItem{{
			ItemId:    "att-5",
			Kind:      corev1.AttachmentItemKind_ATTACHMENT_ITEM_KIND_IMAGE,
			MimeType:  "image/png",
			SizeBytes: 512,
			SourceRef: "blob:src-outbox",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate startup storage scan hydrating persisted draft (att-1) + outbox (att-5).
	cfgEff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentConfig{
			AttachmentConfig: &corev1.AttachmentConfigEvent{
				PersistDrafts: true, PersistOutbox: true, ScanStorage: true, HasPersistedPreparedImages: true,
			},
		},
	})
	var listOp uint64
	for _, ef := range cfgEff.GetEffects() {
		if l := ef.GetDraftStorageList(); l != nil {
			listOp = l.OpId
		}
	}
	if listOp == 0 {
		t.Fatalf("expected DraftStorageListEffect")
	}
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftStorageListed{
			DraftStorageListed: &corev1.DraftStorageListedEvent{
				OpId: listOp,
				Entries: []*corev1.DraftStorageEntry{
					{
						Bucket: corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_OUTBOX,
						Key:    "req-old",
						Data:   persistedOutbox,
					},
					{
						Bucket: corev1.DraftStorageBucket_DRAFT_STORAGE_BUCKET_DRAFT,
						Key:    attachments.ScopeStorageKey(scope),
						Data:   persistedDraft,
					},
				},
			},
		},
	})

	// Open draft and add a new image: must allocate att-6 (not att-1!) so items never collide.
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftOpen{
			DraftOpen: &corev1.DraftOpenAction{Scope: scope, MachineId: h.machineA},
		},
	})
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{{
					Candidate: &corev1.AttachmentCandidateInput{
						MimeType: "image/png", SizeBytes: 2048,
					},
					SourceRef: "blob:src-new",
				}},
			},
		},
	})

	vm := h.draftView(scope)
	if len(vm.Images) != 2 {
		t.Fatalf("images count = %d, want 2", len(vm.Images))
	}
	if vm.Images[0].ItemId != "att-1" || vm.Images[1].ItemId != "att-6" {
		t.Fatalf("item IDs = [%q, %q], want [\"att-1\", \"att-6\"]", vm.Images[0].ItemId, vm.Images[1].ItemId)
	}
}

func TestAttachments_CompleteUnknownOnDisconnect_RetriesSameRequestIDAfterReconnect(t *testing.T) {
	h := newAttHarness(t, false, false)
	scope := h.sessionScope()

	eff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{pickedImage("pic.jpg", 4, "src-pic", true)},
			},
		},
	})
	prep := findPrepareEffects(eff)[0]
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentPrepareImageResult{
			AttachmentPrepareImageResult: &corev1.AttachmentPrepareImageResultEvent{
				OpId:        prep.OpId,
				Ok:          true,
				PreparedRef: "prep-pic",
				ThumbRef:    "thumb-pic",
				Format:      v1.ImageFormat_IMAGE_FORMAT_JPEG,
				SizeBytes:   4,
				Width:       100,
				Height:      100,
				Sha256:      attTestHash(0x91),
			},
		},
	})
	beginCmd := findDispatchedCmds(eff)[0]
	tid := beginCmd.cmd.GetBeginImageUpload().TransferId
	eff = h.settleCmdOK(beginCmd.dc, beginOK(tid, 0, 4, h.nowMs+600_000))
	readEff := findReadChunkEffects(eff)[0]
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentReadChunkResult{
			AttachmentReadChunkResult: &corev1.AttachmentReadChunkResultEvent{
				OpId: readEff.OpId, Ok: true, Offset: 0, Data: []byte("abcd"),
			},
		},
	})
	putCmd := findDispatchedCmds(eff)[0]
	eff = h.settleCmdOK(putCmd.dc, putOK(tid, 4))

	comp1 := findDispatchedCmds(eff)[0]
	if comp1.cmd.GetCompleteImageUpload() == nil {
		t.Fatalf("expected CompleteImageUpload, got %+v", comp1.cmd)
	}
	origReqID := comp1.dc.GetRequestId()

	// Machine disconnects mid-CompleteImageUpload (server broadcasts offline + returns errUnknown deadline_exceeded).
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_MachineUpdated{
					MachineUpdated: &corev1.PlainMachine{
						MachineId: h.machineA,
						Online:    false,
						MetaRev:   2,
						Meta: &v1.MachineMeta{
							Hostname: "Alpha", Home: "/Users/me", ProtocolVersion: 17,
							Capabilities: &v1.MachineCapabilities{ImageUpload: true, FileUpload: true},
						},
					},
				},
			},
		},
	})
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:     origReqID,
				AttemptId:     comp1.dc.GetAttemptId(),
				Source:        corev1.OutcomeSource_OUTCOME_SOURCE_SERVER,
				NotForwarded:  false,
				AttemptBound:  false,
				TransportCode: "deadline_exceeded",
			},
		},
	})

	// Drive sync's internal command retries while the machine stays offline (all hitting machine_offline).
	for iter := 0; iter < 30; iter++ {
		if h.draftView(scope).Images[0].State == corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_WAITING_RECONNECT {
			break
		}
		retryTimers := findTimers(eff, corev1.TimerKind_TIMER_KIND_COMMAND_RETRY)
		if len(retryTimers) == 0 {
			t.Fatalf("iter=%d: expected TIMER_KIND_COMMAND_RETRY or WAITING_RECONNECT, got state=%v", iter, h.draftView(scope).Images[0].State)
		}
		h.advance(retryTimers[0].DelayMs)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_TimerFired{
				TimerFired: &corev1.TimerFiredEvent{
					TimerId: retryTimers[0].TimerId,
					Kind:    retryTimers[0].Kind,
					Key:     retryTimers[0].Key,
				},
			},
		})
		cmds := findDispatchedCmds(eff)
		if len(cmds) != 1 || cmds[0].dc.GetRequestId() != origReqID {
			t.Fatalf("iter=%d: expected retry with same RequestID %q, got %+v", iter, origReqID, cmds)
		}
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_CommandOutcome{
				CommandOutcome: &corev1.CommandOutcomeEvent{
					RequestId:     origReqID,
					AttemptId:     cmds[0].dc.GetAttemptId(),
					Source:        corev1.OutcomeSource_OUTCOME_SOURCE_SERVER,
					NotForwarded:  true,
					AttemptBound:  true,
					OuterReason:   "machine_offline",
					TransportCode: "unavailable",
				},
			},
		})
	}

	dv := h.draftView(scope)
	if dv.Images[0].State != corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_WAITING_RECONNECT {
		t.Fatalf("expected WAITING_RECONNECT instead of UPLOAD_FAILED, got state=%v problem=%+v", dv.Images[0].State, dv.Images[0].Problem)
	}

	// 5 seconds later the daemon reconnects -> Core must automatically retry CompleteImageUpload with the EXACT SAME RequestID!
	h.advance(5_000)
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_MachineUpdated{
					MachineUpdated: &corev1.PlainMachine{
						MachineId: h.machineA,
						Online:    true,
						MetaRev:   3,
						Meta: &v1.MachineMeta{
							Hostname: "Alpha", Home: "/Users/me", ProtocolVersion: 17,
							Capabilities: &v1.MachineCapabilities{ImageUpload: true, FileUpload: true},
						},
					},
				},
			},
		},
	})
	resumed := findDispatchedCmds(eff)
	if len(resumed) != 1 || resumed[0].cmd.GetCompleteImageUpload() == nil {
		t.Fatalf("expected automatic CompleteImageUpload replay after reconnect, got %+v", resumed)
	}
	if resumed[0].dc.GetRequestId() != origReqID {
		t.Fatalf("expected replay with SAME RequestID %q, got %q", origReqID, resumed[0].dc.GetRequestId())
	}
	if !resumed[0].dc.GetRetryOfUnknown() {
		t.Fatalf("expected RetryOfUnknown=true on replayed CompleteImageUpload")
	}

	// Daemon replies with the completed upload -> item becomes READY cleanly without ever showing UPLOAD_FAILED!
	h.settleCmdOK(resumed[0].dc, completeStatusOK(tid, v1.ImageUploadState_IMAGE_UPLOAD_STATE_COMPLETE, "img-recovered-1", "", 4, h.nowMs+600_000))
	dv = h.draftView(scope)
	if dv.Images[0].State != corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_READY || !dv.CanSend {
		t.Fatalf("expected READY draft after replay, got state=%v canSend=%v problem=%+v", dv.Images[0].State, dv.CanSend, dv.Images[0].Problem)
	}
}

func TestAttachments_CompleteUnknownBoundedFailure_ShowsUploadFailed(t *testing.T) {
	h := newAttHarness(t, false, false)
	scope := h.sessionScope()

	eff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{pickedImage("fail.jpg", 4, "src-fail", true)},
			},
		},
	})
	prep := findPrepareEffects(eff)[0]
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentPrepareImageResult{
			AttachmentPrepareImageResult: &corev1.AttachmentPrepareImageResultEvent{
				OpId: prep.OpId, Ok: true, PreparedRef: "prep-fail", ThumbRef: "thumb-fail",
				Format: v1.ImageFormat_IMAGE_FORMAT_JPEG, SizeBytes: 4, Width: 100, Height: 100, Sha256: attTestHash(0x92),
			},
		},
	})
	beginCmd := findDispatchedCmds(eff)[0]
	tid := beginCmd.cmd.GetBeginImageUpload().TransferId
	eff = h.settleCmdOK(beginCmd.dc, beginOK(tid, 0, 4, h.nowMs+600_000))
	readEff := findReadChunkEffects(eff)[0]
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentReadChunkResult{
			AttachmentReadChunkResult: &corev1.AttachmentReadChunkResultEvent{
				OpId: readEff.OpId, Ok: true, Offset: 0, Data: []byte("abcd"),
			},
		},
	})
	putCmd := findDispatchedCmds(eff)[0]
	eff = h.settleCmdOK(putCmd.dc, putOK(tid, 4))
	comp1 := findDispatchedCmds(eff)[0]

	// Machine goes offline and never comes back within the 30s reconnect window.
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_MachineUpdated{
					MachineUpdated: &corev1.PlainMachine{
						MachineId: h.machineA, Online: false, MetaRev: 2,
						Meta: &v1.MachineMeta{
							Hostname: "Alpha", Home: "/Users/me", ProtocolVersion: 17,
							Capabilities: &v1.MachineCapabilities{ImageUpload: true, FileUpload: true},
						},
					},
				},
			},
		},
	})
	eff = h.settleCmdTransportDrop(comp1.dc, "timeout")
	recTimers := findTimers(eff, corev1.TimerKind_TIMER_KIND_ATTACHMENT_UPLOAD_RECONNECT)
	if len(recTimers) != 1 || recTimers[0].DelayMs != attachments.UploadReconnectWindowMs {
		t.Fatalf("expected 30s reconnect timer, got %+v", recTimers)
	}
	h.advance(attachments.UploadReconnectWindowMs)
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_TimerFired{
			TimerFired: &corev1.TimerFiredEvent{
				TimerId: recTimers[0].TimerId, Kind: recTimers[0].Kind, Key: recTimers[0].Key,
			},
		},
	})
	dv := h.draftView(scope)
	if dv.Images[0].State != corev1.AttachmentItemState_ATTACHMENT_ITEM_STATE_FAILED ||
		dv.Images[0].Problem.GetKind() != corev1.ImageProblemKind_IMAGE_PROBLEM_KIND_UPLOAD_FAILED {
		t.Fatalf("expected FAILED/UPLOAD_FAILED after reconnect window expiry, got state=%v problem=%+v", dv.Images[0].State, dv.Images[0].Problem)
	}
}

func TestAttachments_CompleteItemUploadClearsFrozenCompleteTracker(t *testing.T) {
	h := newAttHarness(t, false, false)
	scope := h.sessionScope()

	eff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_DraftAddItems{
			DraftAddItems: &corev1.DraftAddItemsAction{
				Scope: scope,
				Items: []*corev1.DraftPickedItem{pickedImage("probe.jpg", 4, "src-probe", true)},
			},
		},
	})
	prep := findPrepareEffects(eff)[0]
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentPrepareImageResult{
			AttachmentPrepareImageResult: &corev1.AttachmentPrepareImageResultEvent{
				OpId: prep.OpId, Ok: true, PreparedRef: "prep-probe", ThumbRef: "thumb-probe",
				Format: v1.ImageFormat_IMAGE_FORMAT_JPEG, SizeBytes: 4, Width: 100, Height: 100, Sha256: attTestHash(0x93),
			},
		},
	})
	beginCmd := findDispatchedCmds(eff)[0]
	tid := beginCmd.cmd.GetBeginImageUpload().TransferId
	eff = h.settleCmdOK(beginCmd.dc, beginOK(tid, 0, 4, h.nowMs+600_000))
	readEff := findReadChunkEffects(eff)[0]
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_AttachmentReadChunkResult{
			AttachmentReadChunkResult: &corev1.AttachmentReadChunkResultEvent{
				OpId: readEff.OpId, Ok: true, Offset: 0, Data: []byte("abcd"),
			},
		},
	})
	putCmd := findDispatchedCmds(eff)[0]
	eff = h.settleCmdOK(putCmd.dc, putOK(tid, 4))
	comp1 := findDispatchedCmds(eff)[0]

	// Freeze the CompleteImageUpload slot via an unknown transport drop while machine goes offline.
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_MachineUpdated{
					MachineUpdated: &corev1.PlainMachine{
						MachineId: h.machineA, Online: false, MetaRev: 2,
						Meta: &v1.MachineMeta{
							Hostname: "Alpha", Home: "/Users/me", ProtocolVersion: 17,
							Capabilities: &v1.MachineCapabilities{ImageUpload: true, FileUpload: true},
						},
					},
				},
			},
		},
	})
	h.settleCmdTransportDrop(comp1.dc, "timeout")
	slotKey := uploadCompleteSlotKey(h.machineA, tid)
	if !h.core.m.sync.HasFrozenAttempt(slotKey) {
		t.Fatalf("expected frozen CompleteImageUpload tracker for %s", slotKey)
	}

	// If upload subsequently completes via a status probe path, completeItemUpload must clear the frozen tracker.
	draftKey := attachments.ScopeStorageKey(scope)
	it := h.core.m.attachments.drafts[draftKey].items[0]
	prev, ctx := h.core.m.sync.BeginStep()
	var effects []*corev1.Effect
	h.core.completeItemUpload(ctx, attOwnerDraft, draftKey, scope, it, "img-from-probe", h.nowMs+600_000, &effects)
	h.core.m.sync.EndStep(prev, ctx)

	if h.core.m.sync.HasFrozenAttempt(slotKey) {
		t.Fatalf("completeItemUpload leaked frozen tracker for %s", slotKey)
	}
}
