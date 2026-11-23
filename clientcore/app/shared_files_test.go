package app

import (
	"bytes"
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
)

func sfTestHash(b byte) []byte {
	return bytes.Repeat([]byte{b}, 32)
}

func sfUint64(v uint64) *uint64 { return &v }

type sfHarness struct {
	t          *testing.T
	core       *Core
	nowMs      int64
	monoMs     int64
	sessionID  string
	machineID  string
	chunkBytes int32
}

func newSFHarness(t *testing.T, chunkBytes int32, files ...*v1.SharedFile) *sfHarness {
	t.Helper()
	h := &sfHarness{
		t:          t,
		core:       New(),
		nowMs:      1_791_078_060_000,
		monoMs:     10_000,
		sessionID:  "s-dl",
		machineID:  "m-dl",
		chunkBytes: chunkBytes,
	}
	t.Cleanup(h.core.Close)

	ids := make([]string, 0, 64)
	for i := 1; i <= 64; i++ {
		ids = append(ids, "sf-req-"+string(rune('a'+(i/26)%26))+string(rune('a'+i%26)))
	}
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_EntropyProvided{
			EntropyProvided: &corev1.EntropyProvided{
				RandomIds:  ids,
				UnitFloats: []float64{0.5, 0.5, 0.5, 0.5},
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
			TokenResult: &corev1.TokenResultEvent{OpId: tokOp, Token: "tok-sf", ExpiresMs: h.nowMs + 3_600_000},
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
					TrustedMachineIds: []string{h.machineID},
					TrustedClientIds:  []string{"dev-1"},
				},
				Machines: []*corev1.DemoMachine{
					{
						MachineId: h.machineID,
						Online:    true,
						Meta: &v1.MachineMeta{
							Hostname:        "Studio",
							Home:            "/Users/me",
							ProtocolVersion: 17,
							Capabilities: &v1.MachineCapabilities{
								SharedFiles:          true,
								SharedFileChunkBytes: chunkBytes,
							},
						},
					},
				},
				Sessions: []*corev1.DemoSession{
					{
						SessionId: h.sessionID,
						MachineId: h.machineID,
						LastSeq:   1,
						Meta: &v1.Session{
							Id:     h.sessionID,
							Cwd:    "/Users/me/proj",
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
								Seq: 1,
								Kind: &v1.Event_ToolCall{
									ToolCall: &v1.ToolCall{
										CallId:      "tc-sf",
										Status:      "completed",
										SharedFiles: files,
									},
								},
							},
						},
					},
				},
			},
		},
	})
	return h
}

func (h *sfHarness) advance(deltaMs int64) {
	h.nowMs += deltaMs
	h.monoMs += deltaMs
}

func (h *sfHarness) dispatch(ev *corev1.Event) *corev1.EffectBatch {
	if ev.NowMs == 0 {
		ev.NowMs = h.nowMs
	}
	if ev.MonoMs == 0 {
		ev.MonoMs = h.monoMs
	}
	return h.core.Dispatch(ev)
}

func (h *sfHarness) card(fileID string) *corev1.SessionSharedFileItem {
	h.t.Helper()
	vm, err := h.core.View(&corev1.Screen{
		Screen: &corev1.Screen_Session{
			Session: &corev1.SessionScreenRequest{SessionId: h.sessionID},
		},
	})
	if err != nil {
		h.t.Fatalf("View failed: %v", err)
	}
	for _, it := range vm.GetSession().GetTimeline().GetItems() {
		if sf := it.GetSharedFiles(); sf != nil {
			for _, f := range sf.GetFiles() {
				if f.GetFileId() == fileID {
					return f
				}
			}
		}
	}
	h.t.Fatalf("shared file %q not found in session timeline", fileID)
	return nil
}

func findInspectDisk(b *corev1.EffectBatch) *corev1.SharedFileInspectDiskEffect {
	for _, ef := range b.GetEffects() {
		if x := ef.GetSharedFileInspectDisk(); x != nil {
			return x
		}
	}
	return nil
}

func findDispatchChunk(b *corev1.EffectBatch) (*corev1.DispatchCommandEffect, *v1.GetSharedFileChunkRequest) {
	for _, ef := range b.GetEffects() {
		if dc := ef.GetDispatchCommand(); dc != nil {
			var cmd v1.Command
			if err := cmd.Unmarshal(dc.GetCommandBytes()); err == nil && cmd.GetGetSharedFileChunk() != nil {
				return dc, cmd.GetGetSharedFileChunk()
			}
		}
	}
	return nil, nil
}

func findWriteChunk(b *corev1.EffectBatch) *corev1.SharedFileWriteChunkEffect {
	for _, ef := range b.GetEffects() {
		if x := ef.GetSharedFileWriteChunk(); x != nil {
			return x
		}
	}
	return nil
}

func findVerifyCommit(b *corev1.EffectBatch) *corev1.SharedFileVerifyAndCommitEffect {
	for _, ef := range b.GetEffects() {
		if x := ef.GetSharedFileVerifyAndCommit(); x != nil {
			return x
		}
	}
	return nil
}

func findCompleted(b *corev1.EffectBatch) *corev1.SharedFileCompletedEffect {
	for _, ef := range b.GetEffects() {
		if x := ef.GetSharedFileCompleted(); x != nil {
			return x
		}
	}
	return nil
}

func findDeletePartial(b *corev1.EffectBatch) *corev1.SharedFileDeletePartialEffect {
	for _, ef := range b.GetEffects() {
		if x := ef.GetSharedFileDeletePartial(); x != nil {
			return x
		}
	}
	return nil
}

func findDeleteLocal(b *corev1.EffectBatch) *corev1.SharedFileDeleteLocalEffect {
	for _, ef := range b.GetEffects() {
		if x := ef.GetSharedFileDeleteLocal(); x != nil {
			return x
		}
	}
	return nil
}

func findSFScheduleTimer(b *corev1.EffectBatch, kind corev1.TimerKind) *corev1.ScheduleTimerEffect {
	for _, ef := range b.GetEffects() {
		if x := ef.GetScheduleTimer(); x != nil && x.GetKind() == kind {
			return x
		}
	}
	return nil
}

func findSFCancelTimer(b *corev1.EffectBatch, kind corev1.TimerKind) *corev1.CancelTimerEffect {
	for _, ef := range b.GetEffects() {
		if x := ef.GetCancelTimer(); x != nil && x.GetKind() == kind {
			return x
		}
	}
	return nil
}

func (h *sfHarness) deliverChunkOK(dc *corev1.DispatchCommandEffect, data []byte, totalBytes uint64) *corev1.EffectBatch {
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
					Outcome:         "executed",
					Reason:          "executed",
					ResultAvailable: true,
					Kind: &v1.CommandResult_GetSharedFileChunk{
						GetSharedFileChunk: &v1.GetSharedFileChunkResponse{
							Data:       data,
							TotalBytes: sfUint64(totalBytes),
						},
					},
				},
			},
		},
	})
}

func (h *sfHarness) deliverDaemonError(dc *corev1.DispatchCommandEffect, errCode, errMsg string) *corev1.EffectBatch {
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
					ErrorCode:    errCode,
					ErrorMessage: errMsg,
				},
			},
		},
	})
}

func (h *sfHarness) deliverTransportDrop(dc *corev1.DispatchCommandEffect, code string) *corev1.EffectBatch {
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

func (h *sfHarness) setMachineOnline(online bool) *corev1.EffectBatch {
	return h.dispatch(&corev1.Event{
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_MachineUpdated{
					MachineUpdated: &corev1.PlainMachine{
						MachineId: h.machineID,
						Online:    online,
						MetaRev:   2,
						Meta: &v1.MachineMeta{
							Hostname:        "Studio",
							Home:            "/Users/me",
							ProtocolVersion: 17,
							Capabilities: &v1.MachineCapabilities{
								SharedFiles:          true,
								SharedFileChunkBytes: h.chunkBytes,
							},
						},
					},
				},
			},
		},
	})
}

func (h *sfHarness) setSessionExited() *corev1.EffectBatch {
	return h.dispatch(&corev1.Event{
		Event: &corev1.Event_StreamFrame{
			StreamFrame: &corev1.StreamFrameEvent{
				LegId: 1,
				Frame: &corev1.StreamFrameEvent_SessionUpdated{
					SessionUpdated: &corev1.PlainSession{
						SessionId:     h.sessionID,
						MachineId:     h.machineID,
						LastSeq:       1,
						ListedEndedMs: h.nowMs,
						MetaRev:       2,
						Meta: &v1.Session{
							Id:     h.sessionID,
							Cwd:    "/Users/me/proj",
							Agent:  v1.Agent_AGENT_CODEX,
							Status: v1.SessionStatus_SESSION_STATUS_EXITED,
						},
					},
				},
			},
		},
	})
}

func TestSharedFileDownloadMultiChunkProgressAndETA(t *testing.T) {
	const block = int32(1024)
	const size = int64(2500) // 3 blocks: 1024, 1024, 452
	h := newSFHarness(t, block, &v1.SharedFile{
		FileId:    "f-vid",
		Name:      "clip.mp4",
		Path:      "/Users/me/proj/clip.mp4",
		SizeBytes: size,
		Sha256:    sfTestHash(0xaa),
	})

	// Start download -> InspectDisk for full 2500 bytes.
	eff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_SharedFileStartDownload{
			SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{
				SessionId:         h.sessionID,
				FileId:            "f-vid",
				AutoOpenIfStaying: true,
			},
		},
	})
	insp := findInspectDisk(eff)
	if insp == nil || insp.GetRequiredBytes() != size {
		t.Fatalf("expected InspectDisk(2500), got %+v", insp)
	}
	if c := h.card("f-vid"); c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADING {
		t.Fatalf("expected DOWNLOADING card, got %v", c.GetStatus())
	}

	// Disk space OK -> dispatches chunk 0 with BULK route hint.
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_SharedFileDiskInspected{
			SharedFileDiskInspected: &corev1.SharedFileDiskInspectedEvent{
				OpId:           insp.GetOpId(),
				SessionId:      h.sessionID,
				FileId:         "f-vid",
				Known:          true,
				AvailableBytes: 100_000,
			},
		},
	})
	dc0, req0 := findDispatchChunk(eff)
	if dc0 == nil || req0.GetOffset() != 0 || dc0.GetRouteHint() != corev1.CommandRouteHint_COMMAND_ROUTE_HINT_BULK {
		t.Fatalf("expected bulk chunk 0, got dc=%+v req=%+v", dc0, req0)
	}

	// Chunk 0 arrives at +1500ms (< 3s -> no ETA yet).
	h.advance(1500)
	eff = h.deliverChunkOK(dc0, bytes.Repeat([]byte{1}, int(block)), uint64(size))
	wc0 := findWriteChunk(eff)
	if wc0 == nil || wc0.GetOffset() != 0 || len(wc0.GetData()) != 1024 || wc0.GetCommittedOffset() != 1024 || !wc0.GetResetPartial() {
		t.Fatalf("unexpected write chunk 0: %+v", wc0)
	}
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_SharedFileWriteChunkResult{
			SharedFileWriteChunkResult: &corev1.SharedFileWriteChunkResultEvent{
				OpId:      wc0.GetOpId(),
				SessionId: h.sessionID,
				FileId:    "f-vid",
				Ok:        true,
			},
		},
	})
	if c := h.card("f-vid"); c.GetDownloadedBytes() != 1024 || c.GetSubtitle().GetKey() != text.KeySharedFileSubtitleDownloading {
		t.Fatalf("expected no ETA before 3s, got downloaded=%d subtitle=%+v", c.GetDownloadedBytes(), c.GetSubtitle())
	}
	dc1, req1 := findDispatchChunk(eff)
	if dc1 == nil || req1.GetOffset() != 1024 {
		t.Fatalf("expected chunk 1 at offset 1024, got %+v", req1)
	}

	// Chunk 1 arrives at +2500ms (total elapsed 4000ms >= 3s -> ETA shown!).
	// gained=2048 in 4s = 512 B/s, remaining=452 -> 0.88s -> clamped to 1s.
	h.advance(2500)
	eff = h.deliverChunkOK(dc1, bytes.Repeat([]byte{2}, int(block)), uint64(size))
	wc1 := findWriteChunk(eff)
	if wc1 == nil || wc1.GetOffset() != 1024 || len(wc1.GetData()) != 1024 || wc1.GetCommittedOffset() != 2048 || wc1.GetResetPartial() {
		t.Fatalf("unexpected write chunk 1: %+v", wc1)
	}
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_SharedFileWriteChunkResult{
			SharedFileWriteChunkResult: &corev1.SharedFileWriteChunkResultEvent{
				OpId:      wc1.GetOpId(),
				SessionId: h.sessionID,
				FileId:    "f-vid",
				Ok:        true,
			},
		},
	})
	if c := h.card("f-vid"); c.GetDownloadedBytes() != 2048 || c.GetSubtitle().GetKey() != text.KeySharedFileSubtitleDownloadingEta {
		t.Fatalf("expected ETA subtitle at 4s, got downloaded=%d subtitle=%+v", c.GetDownloadedBytes(), c.GetSubtitle())
	}
	dc2, req2 := findDispatchChunk(eff)
	if dc2 == nil || req2.GetOffset() != 2048 {
		t.Fatalf("expected chunk 2 at offset 2048, got %+v", req2)
	}

	// Final chunk 2 arrives padded to 1024 -> Core strips padding to 452 bytes!
	h.advance(500)
	padded := append(bytes.Repeat([]byte{3}, 452), make([]byte, int(block)-452)...)
	eff = h.deliverChunkOK(dc2, padded, uint64(size))
	wc2 := findWriteChunk(eff)
	if wc2 == nil || wc2.GetOffset() != 2048 || len(wc2.GetData()) != 452 || wc2.GetCommittedOffset() != size {
		t.Fatalf("unexpected write chunk 2: %+v", wc2)
	}
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_SharedFileWriteChunkResult{
			SharedFileWriteChunkResult: &corev1.SharedFileWriteChunkResultEvent{
				OpId:      wc2.GetOpId(),
				SessionId: h.sessionID,
				FileId:    "f-vid",
				Ok:        true,
			},
		},
	})
	vc := findVerifyCommit(eff)
	if vc == nil || vc.GetTotalSizeBytes() != size || vc.GetDeliverOnly() || !vc.GetProbeDuration() || !bytes.Equal(vc.GetExpectedSha256(), sfTestHash(0xaa)) {
		t.Fatalf("unexpected VerifyAndCommit: %+v", vc)
	}

	// Verify & commit succeeds with probed duration -> DOWNLOADED + SharedFileCompleted(auto_open=true).
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_SharedFileVerifyAndCommitResult{
			SharedFileVerifyAndCommitResult: &corev1.SharedFileVerifyAndCommitResultEvent{
				OpId:        vc.GetOpId(),
				SessionId:   h.sessionID,
				FileId:      "f-vid",
				Ok:          true,
				HasDuration: true,
				DurationMs:  92_000,
			},
		},
	})
	comp := findCompleted(eff)
	if comp == nil || !comp.GetAutoOpen() || comp.GetDeliverOnly() || comp.GetKind() != corev1.SharedFileKind_SHARED_FILE_KIND_VIDEO {
		t.Fatalf("unexpected CompletedEffect: %+v", comp)
	}
	cFinal := h.card("f-vid")
	if cFinal.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADED ||
		cFinal.GetDurationText() != "1:32" ||
		cFinal.GetSubtitle().GetKey() != text.KeySharedFileSubtitleMediaSize {
		t.Fatalf("unexpected final card: %+v", cFinal)
	}
}

func TestSharedFileDownloadZeroByteAndChunkValidation(t *testing.T) {
	const block = int32(64)
	cases := []struct {
		name           string
		sizeBytes      int64
		chunkData      []byte
		respTotalBytes *uint64
		wantValid      bool
	}{
		{
			name:           "0-byte file with full zero block succeeds",
			sizeBytes:      0,
			chunkData:      make([]byte, block),
			respTotalBytes: sfUint64(0),
			wantValid:      true,
		},
		{
			name:           "0-byte file with empty 0-byte chunk rejected",
			sizeBytes:      0,
			chunkData:      nil,
			respTotalBytes: sfUint64(0),
			wantValid:      false,
		},
		{
			name:           "non-empty file with 0-byte chunk rejected",
			sizeBytes:      10,
			chunkData:      nil,
			respTotalBytes: sfUint64(10),
			wantValid:      false,
		},
		{
			name:           "short chunk rejected",
			sizeBytes:      10,
			chunkData:      make([]byte, 10),
			respTotalBytes: sfUint64(10),
			wantValid:      false,
		},
		{
			name:           "mismatched total_bytes rejected",
			sizeBytes:      10,
			chunkData:      make([]byte, block),
			respTotalBytes: sfUint64(11),
			wantValid:      false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newSFHarness(t, block, &v1.SharedFile{
				FileId:    "f0",
				Name:      "empty.txt",
				SizeBytes: tc.sizeBytes,
				Sha256:    sfTestHash(0x01),
			})
			eff := h.dispatch(&corev1.Event{
				Event: &corev1.Event_SharedFileStartDownload{
					SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{
						SessionId: h.sessionID,
						FileId:    "f0",
					},
				},
			})
			if tc.sizeBytes == 0 {
				if findInspectDisk(eff) != nil {
					t.Fatalf("0-byte file should skip InspectDisk")
				}
			} else {
				insp := findInspectDisk(eff)
				if insp == nil {
					t.Fatalf("expected InspectDisk for non-empty file")
				}
				eff = h.dispatch(&corev1.Event{
					Event: &corev1.Event_SharedFileDiskInspected{
						SharedFileDiskInspected: &corev1.SharedFileDiskInspectedEvent{
							OpId:      insp.GetOpId(),
							SessionId: h.sessionID,
							FileId:    "f0",
							Known:     false,
						},
					},
				})
			}
			dc, _ := findDispatchChunk(eff)
			if dc == nil {
				t.Fatalf("expected DispatchCommand for chunk 0")
			}
			eff = h.dispatch(&corev1.Event{
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
							Outcome:         "executed",
							Reason:          "executed",
							ResultAvailable: true,
							Kind: &v1.CommandResult_GetSharedFileChunk{
								GetSharedFileChunk: &v1.GetSharedFileChunkResponse{
									Data:       tc.chunkData,
									TotalBytes: tc.respTotalBytes,
								},
							},
						},
					},
				},
			})

			if !tc.wantValid {
				if findDeletePartial(eff) == nil {
					t.Fatalf("expected DeletePartial on invalid chunk")
				}
				c := h.card("f0")
				if c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_FAILED ||
					!c.GetSubtitleIsError() {
					t.Fatalf("expected FAILED card on invalid chunk, got %+v", c)
				}
				return
			}

			wc := findWriteChunk(eff)
			if wc == nil || len(wc.GetData()) != 0 {
				t.Fatalf("expected empty write chunk for 0-byte file, got %+v", wc)
			}
			eff = h.dispatch(&corev1.Event{
				Event: &corev1.Event_SharedFileWriteChunkResult{
					SharedFileWriteChunkResult: &corev1.SharedFileWriteChunkResultEvent{
						OpId:      wc.GetOpId(),
						SessionId: h.sessionID,
						FileId:    "f0",
						Ok:        true,
					},
				},
			})
			vc := findVerifyCommit(eff)
			if vc == nil || vc.GetTotalSizeBytes() != 0 {
				t.Fatalf("expected VerifyAndCommit(0), got %+v", vc)
			}
			eff = h.dispatch(&corev1.Event{
				Event: &corev1.Event_SharedFileVerifyAndCommitResult{
					SharedFileVerifyAndCommitResult: &corev1.SharedFileVerifyAndCommitResultEvent{
						OpId:      vc.GetOpId(),
						SessionId: h.sessionID,
						FileId:    "f0",
						Ok:        true,
					},
				},
			})
			if findCompleted(eff) == nil || h.card("f0").GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADED {
				t.Fatalf("expected 0-byte file to finish DOWNLOADED")
			}
		})
	}
}

func TestSharedFileDownloadMissingSha256FailsClosed(t *testing.T) {
	h := newSFHarness(t, 64, &v1.SharedFile{
		FileId:    "f-nohash",
		Name:      "bad.bin",
		SizeBytes: 128,
		Sha256:    []byte{1, 2, 3}, // malformed (< 32 bytes)
	})
	eff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_SharedFileStartDownload{
			SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{
				SessionId: h.sessionID,
				FileId:    "f-nohash",
			},
		},
	})
	if dc, _ := findDispatchChunk(eff); dc != nil || findInspectDisk(eff) != nil {
		t.Fatalf("must not inspect disk or fetch chunks when sha256 is missing/invalid")
	}
	c := h.card("f-nohash")
	if c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_FAILED || !c.GetSubtitleIsError() {
		t.Fatalf("expected FAILED checksum card, got %+v", c)
	}
}

func TestSharedFileDownloadVerifyMismatchRestartsFromZero(t *testing.T) {
	const block = int32(64)
	h := newSFHarness(t, block, &v1.SharedFile{
		FileId:    "f-chk",
		Name:      "archive.zip",
		SizeBytes: 64,
		Sha256:    sfTestHash(0x55),
	})
	eff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_SharedFileStartDownload{
			SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{SessionId: h.sessionID, FileId: "f-chk"},
		},
	})
	insp := findInspectDisk(eff)
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_SharedFileDiskInspected{
			SharedFileDiskInspected: &corev1.SharedFileDiskInspectedEvent{OpId: insp.GetOpId(), SessionId: h.sessionID, FileId: "f-chk", Known: true, AvailableBytes: 1024},
		},
	})
	dc, _ := findDispatchChunk(eff)
	eff = h.deliverChunkOK(dc, bytes.Repeat([]byte{9}, 64), 64)
	wc := findWriteChunk(eff)
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_SharedFileWriteChunkResult{
			SharedFileWriteChunkResult: &corev1.SharedFileWriteChunkResultEvent{OpId: wc.GetOpId(), SessionId: h.sessionID, FileId: "f-chk", Ok: true},
		},
	})
	vc := findVerifyCommit(eff)

	// Verify fails with CHECKSUM_MISMATCH -> deletes partial, resets downloaded_bytes to 0.
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_SharedFileVerifyAndCommitResult{
			SharedFileVerifyAndCommitResult: &corev1.SharedFileVerifyAndCommitResultEvent{
				OpId:      vc.GetOpId(),
				SessionId: h.sessionID,
				FileId:    "f-chk",
				Ok:        false,
				ErrorKind: corev1.SharedFileIOErrorKind_SHARED_FILE_IO_ERROR_KIND_CHECKSUM_MISMATCH,
			},
		},
	})
	if findDeletePartial(eff) == nil {
		t.Fatalf("expected DeletePartial on verify checksum mismatch")
	}
	if c := h.card("f-chk"); c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_FAILED || c.GetDownloadedBytes() != 0 {
		t.Fatalf("expected FAILED with 0 bytes after checksum mismatch, got %+v", c)
	}

	// Retry starts from offset 0 again (InspectDisk requires full 64 bytes).
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_SharedFileStartDownload{
			SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{SessionId: h.sessionID, FileId: "f-chk"},
		},
	})
	if insp2 := findInspectDisk(eff); insp2 == nil || insp2.GetRequiredBytes() != 64 {
		t.Fatalf("expected full 64-byte InspectDisk on retry after checksum failure, got %+v", insp2)
	}
}

func TestSharedFileDownloadPauseResumeAndRestartHydration(t *testing.T) {
	const block = int32(1024)
	hash1 := sfTestHash(0x11)
	hash2 := sfTestHash(0x22)
	hash3 := sfTestHash(0x33)
	h := newSFHarness(t, block,
		&v1.SharedFile{FileId: "f-part", Name: "part.bin", SizeBytes: 3000, Sha256: hash1},
		&v1.SharedFile{FileId: "f-ready", Name: "ready.bin", SizeBytes: 2048, Sha256: hash2},
		&v1.SharedFile{FileId: "f-done", Name: "done.mp3", SizeBytes: 500, Sha256: hash3},
	)

	// Startup scan via SharedFileConfigEvent{scan_local: true}.
	eff := h.dispatch(&corev1.Event{
		Event: &corev1.Event_SharedFileConfig{
			SharedFileConfig: &corev1.SharedFileConfigEvent{ScanLocal: true},
		},
	})
	var scanOp uint64
	for _, ef := range eff.GetEffects() {
		if sc := ef.GetSharedFileScanLocal(); sc != nil {
			scanOp = sc.GetOpId()
		}
	}
	if scanOp == 0 {
		t.Fatalf("expected SharedFileScanLocalEffect")
	}

	// Shell returns:
	// - f-part: valid mid-file partial (1024/3000)
	// - f-ready: valid complete partial awaiting verify (2048/2048)
	// - f-done: already completed file with duration
	// - f-bad: corrupt truncated partial -> Core must emit DeletePartial
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_SharedFileLocalScanned{
			SharedFileLocalScanned: &corev1.SharedFileLocalScannedEvent{
				OpId: scanOp,
				Entries: []*corev1.SharedFileLocalEntry{
					{
						SessionId:         h.sessionID,
						FileId:            "f-part",
						Completed:         false,
						BytesOnDisk:       1024,
						ExpectedSizeBytes: 3000,
						ChunkBytes:        block,
						CommittedOffset:   1024,
						Sha256:            hash1,
					},
					{
						SessionId:         h.sessionID,
						FileId:            "f-ready",
						Completed:         false,
						BytesOnDisk:       2048,
						ExpectedSizeBytes: 2048,
						ChunkBytes:        block,
						CommittedOffset:   2048,
						Sha256:            hash2,
					},
					{
						SessionId:         h.sessionID,
						FileId:            "f-done",
						Completed:         true,
						BytesOnDisk:       500,
						ExpectedSizeBytes: 500,
						HasDuration:       true,
						DurationMs:        12_000,
					},
					{
						SessionId:         h.sessionID,
						FileId:            "f-bad",
						Completed:         false,
						BytesOnDisk:       100, // truncated!
						ExpectedSizeBytes: 3000,
						ChunkBytes:        block,
						CommittedOffset:   1024,
						Sha256:            hash1,
					},
				},
			},
		},
	})
	if dp := findDeletePartial(eff); dp == nil || dp.GetFileId() != "f-bad" {
		t.Fatalf("expected DeletePartial for corrupt f-bad, got %+v", dp)
	}
	if c := h.card("f-part"); c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_PAUSED || c.GetDownloadedBytes() != 1024 {
		t.Fatalf("expected f-part PAUSED at 1024, got %+v", c)
	}
	if c := h.card("f-done"); c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADED || c.GetDurationText() != "0:12" {
		t.Fatalf("expected f-done DOWNLOADED with 0:12, got %+v", c)
	}

	// 1) Resuming f-part asks InspectDisk only for remaining 1976 bytes and starts at offset 1024!
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_SharedFileStartDownload{
			SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{SessionId: h.sessionID, FileId: "f-part"},
		},
	})
	insp := findInspectDisk(eff)
	if insp == nil || insp.GetRequiredBytes() != 3000-1024 {
		t.Fatalf("expected InspectDisk(1976), got %+v", insp)
	}
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_SharedFileDiskInspected{
			SharedFileDiskInspected: &corev1.SharedFileDiskInspectedEvent{OpId: insp.GetOpId(), SessionId: h.sessionID, FileId: "f-part", Known: true, AvailableBytes: 100_000},
		},
	})
	dc1, req1 := findDispatchChunk(eff)
	if dc1 == nil || req1.GetOffset() != 1024 {
		t.Fatalf("expected resume at offset 1024, got %+v", req1)
	}

	// Pause while chunk 1 is being written to disk: write finishes ok -> absorbed into PAUSED progress, no next chunk dispatched!
	eff = h.deliverChunkOK(dc1, bytes.Repeat([]byte{7}, int(block)), 3000)
	wc1 := findWriteChunk(eff)
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_SharedFilePauseDownload{
			SharedFilePauseDownload: &corev1.SharedFilePauseDownloadAction{SessionId: h.sessionID, FileId: "f-part"},
		},
	})
	if c := h.card("f-part"); c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_PAUSED || c.GetDownloadedBytes() != 1024 {
		t.Fatalf("expected PAUSED at 1024 before in-flight write settles, got %+v", c)
	}
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_SharedFileWriteChunkResult{
			SharedFileWriteChunkResult: &corev1.SharedFileWriteChunkResultEvent{OpId: wc1.GetOpId(), SessionId: h.sessionID, FileId: "f-part", Ok: true},
		},
	})
	if dcNext, _ := findDispatchChunk(eff); dcNext != nil {
		t.Fatalf("paused download must not fetch next chunk after absorbed write")
	}
	if c := h.card("f-part"); c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_PAUSED || c.GetDownloadedBytes() != 2048 {
		t.Fatalf("expected PAUSED progress updated to 2048 after absorbed write, got %+v", c)
	}

	// 2) Resuming f-ready (all blocks already on disk before crash) skips InspectDisk & chunk fetch and emits VerifyAndCommit directly!
	eff = h.dispatch(&corev1.Event{
		Event: &corev1.Event_SharedFileStartDownload{
			SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{SessionId: h.sessionID, FileId: "f-ready"},
		},
	})
	if findInspectDisk(eff) != nil {
		t.Fatalf("f-ready should skip InspectDisk")
	}
	if vc := findVerifyCommit(eff); vc == nil || vc.GetTotalSizeBytes() != 2048 {
		t.Fatalf("expected immediate VerifyAndCommit for f-ready, got %+v", vc)
	}

	// 3) OS cache eviction on f-done reverts card from DOWNLOADED back to AVAILABLE.
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_SharedFileLocalMissing{
			SharedFileLocalMissing: &corev1.SharedFileLocalMissingEvent{SessionId: h.sessionID, FileId: "f-done"},
		},
	})
	if c := h.card("f-done"); c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_AVAILABLE {
		t.Fatalf("expected AVAILABLE after LocalMissing, got %v", c.GetStatus())
	}
}

func TestSharedFileDownloadDisconnectReconnectAndTimeout(t *testing.T) {
	const block = int32(1024)
	startAndGetChunk0 := func(t *testing.T) (*sfHarness, *corev1.DispatchCommandEffect) {
		h := newSFHarness(t, block, &v1.SharedFile{
			FileId:    "f-net",
			Name:      "video.mp4",
			SizeBytes: 2048,
			Sha256:    sfTestHash(0x44),
		})
		eff := h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileStartDownload{
				SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{SessionId: h.sessionID, FileId: "f-net"},
			},
		})
		insp := findInspectDisk(eff)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileDiskInspected{
				SharedFileDiskInspected: &corev1.SharedFileDiskInspectedEvent{OpId: insp.GetOpId(), SessionId: h.sessionID, FileId: "f-net", Known: false},
			},
		})
		dc0, _ := findDispatchChunk(eff)
		return h, dc0
	}

	t.Run("offline drop wakes on reconnect before 30s and succeeds", func(t *testing.T) {
		h, dc0 := startAndGetChunk0(t)
		// Complete chunk 0 so partial has 1024 bytes.
		eff := h.deliverChunkOK(dc0, bytes.Repeat([]byte{1}, int(block)), 2048)
		wc0 := findWriteChunk(eff)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileWriteChunkResult{
				SharedFileWriteChunkResult: &corev1.SharedFileWriteChunkResultEvent{OpId: wc0.GetOpId(), SessionId: h.sessionID, FileId: "f-net", Ok: true},
			},
		})
		dc1, _ := findDispatchChunk(eff)

		// Machine goes offline and chunk 1 fails with network drop.
		h.setMachineOnline(false)
		eff = h.deliverTransportDrop(dc1, "network")
		tm := findSFScheduleTimer(eff, corev1.TimerKind_TIMER_KIND_SHARED_FILE_RECONNECT)
		if tm == nil || tm.GetDelayMs() != 30_000 {
			t.Fatalf("expected 30s reconnect timer, got %+v", tm)
		}
		if c := h.card("f-net"); c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADING || !c.GetWaitingReconnect() {
			t.Fatalf("expected DOWNLOADING + waiting_reconnect, got %+v", c)
		}

		// Machine reconnects at +10s -> cancels 30s timer and immediately retries chunk 1!
		h.advance(10_000)
		eff = h.setMachineOnline(true)
		if findSFCancelTimer(eff, corev1.TimerKind_TIMER_KIND_SHARED_FILE_RECONNECT) == nil {
			t.Fatalf("expected reconnect timer to be canceled when machine returned online")
		}
		dc1Retry, req1Retry := findDispatchChunk(eff)
		if dc1Retry == nil || req1Retry.GetOffset() != 1024 {
			t.Fatalf("expected immediate retry at offset 1024, got %+v", req1Retry)
		}
		if c := h.card("f-net"); c.GetWaitingReconnect() {
			t.Fatalf("expected waiting_reconnect cleared on retry")
		}
	})

	t.Run("offline drop times out at 30s and preserves partial", func(t *testing.T) {
		h, dc0 := startAndGetChunk0(t)
		eff := h.deliverChunkOK(dc0, bytes.Repeat([]byte{1}, int(block)), 2048)
		wc0 := findWriteChunk(eff)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileWriteChunkResult{
				SharedFileWriteChunkResult: &corev1.SharedFileWriteChunkResultEvent{OpId: wc0.GetOpId(), SessionId: h.sessionID, FileId: "f-net", Ok: true},
			},
		})
		dc1, _ := findDispatchChunk(eff)
		h.setMachineOnline(false)
		eff = h.deliverTransportDrop(dc1, "network")
		tm := findSFScheduleTimer(eff, corev1.TimerKind_TIMER_KIND_SHARED_FILE_RECONNECT)

		// Advance 30s and fire timer while still offline -> FAILED(DISCONNECTED), partial 1024 kept!
		h.advance(30_000)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_TimerFired{
				TimerFired: &corev1.TimerFiredEvent{
					TimerId: tm.GetTimerId(),
					Kind:    tm.GetKind(),
					Key:     tm.GetKey(),
				},
			},
		})
		if findDeletePartial(eff) != nil {
			t.Fatalf("disconnect timeout must preserve partial blocks")
		}
		// While offline, card shows OFFLINE (preserving 1024 progress); once back online, shows FAILED(DISCONNECTED).
		h.setMachineOnline(true)
		c := h.card("f-net")
		if c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_FAILED || c.GetDownloadedBytes() != 1024 {
			t.Fatalf("expected FAILED preserving 1024 bytes, got %+v", c)
		}
	})

	t.Run("online transient drop waits 1s then auto-resumes once; second drop fails immediately", func(t *testing.T) {
		h, dc0 := startAndGetChunk0(t)
		eff := h.deliverTransportDrop(dc0, "network")
		tm := findSFScheduleTimer(eff, corev1.TimerKind_TIMER_KIND_SHARED_FILE_RECONNECT)
		if tm == nil || tm.GetDelayMs() != 1_000 {
			t.Fatalf("expected 1s brief pause timer while online, got %+v", tm)
		}
		h.advance(1_000)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_TimerFired{
				TimerFired: &corev1.TimerFiredEvent{TimerId: tm.GetTimerId(), Kind: tm.GetKind(), Key: tm.GetKey()},
			},
		})
		dc0Retry, _ := findDispatchChunk(eff)
		if dc0Retry == nil {
			t.Fatalf("expected retry chunk 0 after 1s timer")
		}
		// Second drop on same attempt -> fails immediately without scheduling another timer!
		eff = h.deliverTransportDrop(dc0Retry, "network")
		if findSFScheduleTimer(eff, corev1.TimerKind_TIMER_KIND_SHARED_FILE_RECONNECT) != nil {
			t.Fatalf("must only auto-resume once per attempt")
		}
		if c := h.card("f-net"); c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_FAILED {
			t.Fatalf("expected FAILED after second disconnect, got %+v", c)
		}
	})

	t.Run("executed outcome missing chunk payload fails as protocol error without reconnect wait", func(t *testing.T) {
		h, dc0 := startAndGetChunk0(t)
		eff := h.dispatch(&corev1.Event{
			Event: &corev1.Event_CommandOutcome{
				CommandOutcome: &corev1.CommandOutcomeEvent{
					RequestId:         dc0.GetRequestId(),
					AttemptId:         dc0.GetAttemptId(),
					Source:            corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
					Signed:            true,
					HashesMatch:       true,
					AttemptBound:      true,
					OuterMatchesInner: true,
					OuterOutcome:      "executed",
					OuterReason:       "executed",
					Result: &v1.CommandResult{
						Outcome:         "executed",
						Reason:          "executed",
						ResultAvailable: true,
						// Kind deliberately nil (missing GetSharedFileChunk payload)
					},
				},
			},
		})
		if findSFScheduleTimer(eff, corev1.TimerKind_TIMER_KIND_SHARED_FILE_RECONNECT) != nil {
			t.Fatalf("missing payload must not schedule reconnect timer")
		}
		if c := h.card("f-net"); c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_FAILED || c.GetWaitingReconnect() {
			t.Fatalf("expected FAILED (not waiting_reconnect) on missing payload, got %+v", c)
		}
	})
}

func TestSharedFileDownloadNotFoundAndSessionExit(t *testing.T) {
	const block = int32(64)

	t.Run("daemon not_found marks CLEANED and deletes partial", func(t *testing.T) {
		h := newSFHarness(t, block, &v1.SharedFile{
			FileId:    "f-nf",
			Name:      "gone.pdf",
			SizeBytes: 128,
			Sha256:    sfTestHash(0x77),
		})
		eff := h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileStartDownload{
				SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{SessionId: h.sessionID, FileId: "f-nf"},
			},
		})
		insp := findInspectDisk(eff)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileDiskInspected{
				SharedFileDiskInspected: &corev1.SharedFileDiskInspectedEvent{OpId: insp.GetOpId(), SessionId: h.sessionID, FileId: "f-nf", Known: false},
			},
		})
		dc0, _ := findDispatchChunk(eff)
		eff = h.deliverDaemonError(dc0, "not_found", "file removed")
		if findDeletePartial(eff) == nil {
			t.Fatalf("expected DeletePartial on daemon not_found")
		}
		if c := h.card("f-nf"); c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_CLEANED {
			t.Fatalf("expected CLEANED on daemon not_found, got %+v", c)
		}
	})

	t.Run("session exit cleans active fetch but preserves verifying and downloaded", func(t *testing.T) {
		h := newSFHarness(t, block,
			&v1.SharedFile{FileId: "f-fetch", Name: "a.bin", SizeBytes: 128, Sha256: sfTestHash(0x81)},
			&v1.SharedFile{FileId: "f-ver", Name: "b.bin", SizeBytes: 64, Sha256: sfTestHash(0x82)},
		)
		// Start f-fetch -> chunk 0 written -> chunk 1 in flight.
		eff := h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileStartDownload{
				SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{SessionId: h.sessionID, FileId: "f-fetch"},
			},
		})
		insp := findInspectDisk(eff)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileDiskInspected{
				SharedFileDiskInspected: &corev1.SharedFileDiskInspectedEvent{OpId: insp.GetOpId(), SessionId: h.sessionID, FileId: "f-fetch", Known: false},
			},
		})
		dc0, _ := findDispatchChunk(eff)
		eff = h.deliverChunkOK(dc0, bytes.Repeat([]byte{1}, 64), 128)
		wc0 := findWriteChunk(eff)
		h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileWriteChunkResult{
				SharedFileWriteChunkResult: &corev1.SharedFileWriteChunkResultEvent{OpId: wc0.GetOpId(), SessionId: h.sessionID, FileId: "f-fetch", Ok: true},
			},
		})

		// Start f-ver -> reaches VERIFYING phase.
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileStartDownload{
				SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{SessionId: h.sessionID, FileId: "f-ver"},
			},
		})
		inspV := findInspectDisk(eff)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileDiskInspected{
				SharedFileDiskInspected: &corev1.SharedFileDiskInspectedEvent{OpId: inspV.GetOpId(), SessionId: h.sessionID, FileId: "f-ver", Known: false},
			},
		})
		dcV, _ := findDispatchChunk(eff)
		eff = h.deliverChunkOK(dcV, bytes.Repeat([]byte{2}, 64), 64)
		wcV := findWriteChunk(eff)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileWriteChunkResult{
				SharedFileWriteChunkResult: &corev1.SharedFileWriteChunkResultEvent{OpId: wcV.GetOpId(), SessionId: h.sessionID, FileId: "f-ver", Ok: true},
			},
		})
		vc := findVerifyCommit(eff)

		// Session exits now!
		eff = h.setSessionExited()
		if dp := findDeletePartial(eff); dp == nil || dp.GetFileId() != "f-fetch" {
			t.Fatalf("expected DeletePartial for f-fetch on session exit, got %+v", dp)
		}
		if c := h.card("f-fetch"); c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_CLEANED {
			t.Fatalf("expected f-fetch CLEANED on session exit, got %+v", c)
		}
		// f-ver finishes local commit -> becomes DOWNLOADED even though session exited!
		h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileVerifyAndCommitResult{
				SharedFileVerifyAndCommitResult: &corev1.SharedFileVerifyAndCommitResultEvent{OpId: vc.GetOpId(), SessionId: h.sessionID, FileId: "f-ver", Ok: true},
			},
		})
		if c := h.card("f-ver"); c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADED {
			t.Fatalf("expected f-ver DOWNLOADED after local verify despite session exit, got %+v", c)
		}
	})
}

func TestSharedFileDownloadDiskFullNormalization(t *testing.T) {
	cases := []struct {
		name        string
		cfgFullKind corev1.SharedFileFailureKind
		wantKey     string
	}{
		{
			name:        "mobile config normalizes to phone storage full",
			cfgFullKind: corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_PHONE_STORAGE_FULL,
			wantKey:     text.KeySharedFileReasonDiskFull,
		},
		{
			name:        "web config normalizes to browser storage full",
			cfgFullKind: corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_BROWSER_STORAGE_FULL,
			wantKey:     text.KeySharedFileReasonBrowserStorageFull,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newSFHarness(t, 64, &v1.SharedFile{
				FileId:    "f-disk",
				Name:      "big.bin",
				SizeBytes: 128,
				Sha256:    sfTestHash(0x99),
			})
			h.dispatch(&corev1.Event{
				Event: &corev1.Event_SharedFileConfig{
					SharedFileConfig: &corev1.SharedFileConfigEvent{StorageFullKind: tc.cfgFullKind},
				},
			})
			eff := h.dispatch(&corev1.Event{
				Event: &corev1.Event_SharedFileStartDownload{
					SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{SessionId: h.sessionID, FileId: "f-disk"},
				},
			})
			insp := findInspectDisk(eff)
			// Preflight reports only 64 bytes free for 128-byte file -> fails immediately without fetching chunks!
			eff = h.dispatch(&corev1.Event{
				Event: &corev1.Event_SharedFileDiskInspected{
					SharedFileDiskInspected: &corev1.SharedFileDiskInspectedEvent{
						OpId:           insp.GetOpId(),
						SessionId:      h.sessionID,
						FileId:         "f-disk",
						Known:          true,
						AvailableBytes: 64,
					},
				},
			})
			if dc, _ := findDispatchChunk(eff); dc != nil {
				t.Fatalf("must not fetch chunks when InspectDisk shows insufficient space")
			}
			c := h.card("f-disk")
			if c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_FAILED ||
				c.GetSubtitle().GetArgs()[0].GetTextValue().GetKey() != tc.wantKey {
				t.Fatalf("expected reason key %q, got card=%+v", tc.wantKey, c)
			}
		})
	}

	t.Run("write and commit storage_full preserve partial and retry skips re-fetch", func(t *testing.T) {
		h := newSFHarness(t, 64, &v1.SharedFile{
			FileId:    "f-wf",
			Name:      "data.bin",
			SizeBytes: 128,
			Sha256:    sfTestHash(0x9a),
		})
		eff := h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileStartDownload{
				SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{SessionId: h.sessionID, FileId: "f-wf"},
			},
		})
		insp := findInspectDisk(eff)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileDiskInspected{
				SharedFileDiskInspected: &corev1.SharedFileDiskInspectedEvent{OpId: insp.GetOpId(), SessionId: h.sessionID, FileId: "f-wf", Known: false},
			},
		})
		// Write chunk 0 ok, chunk 1 fails with raw ENOSPC -> normalized to PHONE_STORAGE_FULL, partial 64 kept.
		dc0, _ := findDispatchChunk(eff)
		eff = h.deliverChunkOK(dc0, bytes.Repeat([]byte{1}, 64), 128)
		wc0 := findWriteChunk(eff)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileWriteChunkResult{
				SharedFileWriteChunkResult: &corev1.SharedFileWriteChunkResultEvent{OpId: wc0.GetOpId(), SessionId: h.sessionID, FileId: "f-wf", Ok: true},
			},
		})
		dc1, _ := findDispatchChunk(eff)
		eff = h.deliverChunkOK(dc1, bytes.Repeat([]byte{2}, 64), 128)
		wc1 := findWriteChunk(eff)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileWriteChunkResult{
				SharedFileWriteChunkResult: &corev1.SharedFileWriteChunkResultEvent{
					OpId:        wc1.GetOpId(),
					SessionId:   h.sessionID,
					FileId:      "f-wf",
					Ok:          false,
					ErrorKind:   corev1.SharedFileIOErrorKind_SHARED_FILE_IO_ERROR_KIND_OTHER,
					ErrorDetail: "write failed: ENOSPC",
				},
			},
		})
		if findDeletePartial(eff) != nil {
			t.Fatalf("storage full must not delete partial blocks")
		}
		c := h.card("f-wf")
		if c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_FAILED ||
			c.GetDownloadedBytes() != 64 ||
			c.GetSubtitle().GetArgs()[0].GetTextValue().GetKey() != text.KeySharedFileReasonDiskFull {
			t.Fatalf("expected PHONE_STORAGE_FULL preserving 64 bytes, got %+v", c)
		}

		// Retry resumes from offset 64, finishes chunk 1, then fails commit with STORAGE_FULL.
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileStartDownload{
				SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{SessionId: h.sessionID, FileId: "f-wf"},
			},
		})
		insp2 := findInspectDisk(eff)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileDiskInspected{
				SharedFileDiskInspected: &corev1.SharedFileDiskInspectedEvent{OpId: insp2.GetOpId(), SessionId: h.sessionID, FileId: "f-wf", Known: false},
			},
		})
		dc1b, req1b := findDispatchChunk(eff)
		if dc1b == nil || req1b.GetOffset() != 64 {
			t.Fatalf("expected resume at 64, got %+v", req1b)
		}
		eff = h.deliverChunkOK(dc1b, bytes.Repeat([]byte{2}, 64), 128)
		wc1b := findWriteChunk(eff)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileWriteChunkResult{
				SharedFileWriteChunkResult: &corev1.SharedFileWriteChunkResultEvent{OpId: wc1b.GetOpId(), SessionId: h.sessionID, FileId: "f-wf", Ok: true},
			},
		})
		vc := findVerifyCommit(eff)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileVerifyAndCommitResult{
				SharedFileVerifyAndCommitResult: &corev1.SharedFileVerifyAndCommitResultEvent{
					OpId:      vc.GetOpId(),
					SessionId: h.sessionID,
					FileId:    "f-wf",
					Ok:        false,
					ErrorKind: corev1.SharedFileIOErrorKind_SHARED_FILE_IO_ERROR_KIND_STORAGE_FULL,
				},
			},
		})
		if findDeletePartial(eff) != nil || h.card("f-wf").GetDownloadedBytes() != 128 {
			t.Fatalf("commit STORAGE_FULL must preserve full 128-byte partial")
		}
		// Retry after commit STORAGE_FULL jumps directly to VerifyAndCommit without re-fetching!
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileStartDownload{
				SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{SessionId: h.sessionID, FileId: "f-wf"},
			},
		})
		if findInspectDisk(eff) != nil || findVerifyCommit(eff) == nil {
			t.Fatalf("retry after commit STORAGE_FULL should emit VerifyAndCommit directly")
		}
	})
}

func TestSharedFileDownloadLocalCacheMaxBytesCap(t *testing.T) {
	const block = int32(64)
	h := newSFHarness(t, block,
		&v1.SharedFile{FileId: "f-over", Name: "over.zip", SizeBytes: 101, Sha256: sfTestHash(0xc1)},
		&v1.SharedFile{FileId: "f-at", Name: "at.zip", SizeBytes: 100, Sha256: sfTestHash(0xc2)},
	)
	h.dispatch(&corev1.Event{
		Event: &corev1.Event_SharedFileConfig{
			SharedFileConfig: &corev1.SharedFileConfigEvent{
				LocalCacheMaxBytes: 100,
				StorageFullKind:    corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_BROWSER_STORAGE_FULL,
			},
		},
	})

	runFile := func(fid string, size int64) (*corev1.SharedFileVerifyAndCommitEffect, *corev1.SharedFileCompletedEffect) {
		eff := h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileStartDownload{
				SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{SessionId: h.sessionID, FileId: fid, AutoOpenIfStaying: true},
			},
		})
		insp := findInspectDisk(eff)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileDiskInspected{
				SharedFileDiskInspected: &corev1.SharedFileDiskInspectedEvent{OpId: insp.GetOpId(), SessionId: h.sessionID, FileId: fid, Known: false},
			},
		})
		for off := int64(0); off < size; off += int64(block) {
			dc, _ := findDispatchChunk(eff)
			eff = h.deliverChunkOK(dc, bytes.Repeat([]byte{4}, int(block)), uint64(size))
			wc := findWriteChunk(eff)
			eff = h.dispatch(&corev1.Event{
				Event: &corev1.Event_SharedFileWriteChunkResult{
					SharedFileWriteChunkResult: &corev1.SharedFileWriteChunkResultEvent{OpId: wc.GetOpId(), SessionId: h.sessionID, FileId: fid, Ok: true},
				},
			})
		}
		vc := findVerifyCommit(eff)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileVerifyAndCommitResult{
				SharedFileVerifyAndCommitResult: &corev1.SharedFileVerifyAndCommitResultEvent{OpId: vc.GetOpId(), SessionId: h.sessionID, FileId: fid, Ok: true},
			},
		})
		return vc, findCompleted(eff)
	}

	// Strictly over cap (101 > 100): deliver_only=true, card returns to AVAILABLE, auto_open=false.
	vcOver, compOver := runFile("f-over", 101)
	if !vcOver.GetDeliverOnly() || compOver == nil || !compOver.GetDeliverOnly() || compOver.GetAutoOpen() {
		t.Fatalf("expected deliver_only=true & auto_open=false for over-cap file, vc=%+v comp=%+v", vcOver, compOver)
	}
	if c := h.card("f-over"); c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_AVAILABLE || c.GetDownloadedBytes() != 0 {
		t.Fatalf("expected over-cap file to return to AVAILABLE, got %+v", c)
	}

	// Exactly at cap (100 <= 100): cached locally, DOWNLOADED, auto_open=true.
	vcAt, compAt := runFile("f-at", 100)
	if vcAt.GetDeliverOnly() || compAt == nil || compAt.GetDeliverOnly() || !compAt.GetAutoOpen() {
		t.Fatalf("expected cached commit & auto_open=true for at-cap file, vc=%+v comp=%+v", vcAt, compAt)
	}
	if c := h.card("f-at"); c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADED {
		t.Fatalf("expected at-cap file DOWNLOADED, got %+v", c)
	}
}

func TestSharedFileDownloadCancelDeleteAndAutoOpenTickets(t *testing.T) {
	const block = int32(64)

	t.Run("cancel during verifying cleans up late committed file", func(t *testing.T) {
		h := newSFHarness(t, block, &v1.SharedFile{FileId: "f-c", Name: "doc.pdf", SizeBytes: 64, Sha256: sfTestHash(0xd1)})
		eff := h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileStartDownload{
				SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{SessionId: h.sessionID, FileId: "f-c"},
			},
		})
		insp := findInspectDisk(eff)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileDiskInspected{
				SharedFileDiskInspected: &corev1.SharedFileDiskInspectedEvent{OpId: insp.GetOpId(), SessionId: h.sessionID, FileId: "f-c", Known: false},
			},
		})
		dc, _ := findDispatchChunk(eff)
		eff = h.deliverChunkOK(dc, bytes.Repeat([]byte{1}, 64), 64)
		wc := findWriteChunk(eff)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileWriteChunkResult{
				SharedFileWriteChunkResult: &corev1.SharedFileWriteChunkResultEvent{OpId: wc.GetOpId(), SessionId: h.sessionID, FileId: "f-c", Ok: true},
			},
		})
		vc := findVerifyCommit(eff)

		// User cancels while VerifyAndCommit is in flight.
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileCancelDownload{
				SharedFileCancelDownload: &corev1.SharedFileCancelDownloadAction{SessionId: h.sessionID, FileId: "f-c"},
			},
		})
		if findDeletePartial(eff) == nil || h.card("f-c").GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_AVAILABLE {
			t.Fatalf("expected DeletePartial and AVAILABLE after cancel")
		}
		// Late VerifyAndCommitResult{ok:true} triggers DeleteLocal to avoid orphan file on disk!
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileVerifyAndCommitResult{
				SharedFileVerifyAndCommitResult: &corev1.SharedFileVerifyAndCommitResultEvent{OpId: vc.GetOpId(), SessionId: h.sessionID, FileId: "f-c", Ok: true},
			},
		})
		if findDeleteLocal(eff) == nil || h.card("f-c").GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_AVAILABLE {
			t.Fatalf("expected DeleteLocal for late commit after cancel")
		}
	})

	t.Run("auto-open ticket revoked on leaving session or backgrounding", func(t *testing.T) {
		revokeCases := []struct {
			name   string
			revoke func(h *sfHarness)
		}{
			{
				name: "SetVisibleSession to other session",
				revoke: func(h *sfHarness) {
					h.dispatch(&corev1.Event{
						Event: &corev1.Event_SharedFileSetVisibleSession{
							SharedFileSetVisibleSession: &corev1.SharedFileSetVisibleSessionAction{SessionId: "s-other"},
						},
					})
				},
			},
			{
				name: "SessionWatch false",
				revoke: func(h *sfHarness) {
					h.dispatch(&corev1.Event{
						Event: &corev1.Event_SessionWatch{
							SessionWatch: &corev1.SessionWatchEvent{SessionId: h.sessionID, Watched: false},
						},
					})
				},
			},
			{
				name: "SyncLifecycle BACKGROUND",
				revoke: func(h *sfHarness) {
					h.dispatch(&corev1.Event{
						Event: &corev1.Event_SyncLifecycle{
							SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_BACKGROUND},
						},
					})
				},
			},
		}
		for _, rc := range revokeCases {
			t.Run(rc.name, func(t *testing.T) {
				h := newSFHarness(t, block, &v1.SharedFile{FileId: "f-ao", Name: "pic.png", SizeBytes: 64, Sha256: sfTestHash(0xe1)})
				h.dispatch(&corev1.Event{
					Event: &corev1.Event_SharedFileSetVisibleSession{
						SharedFileSetVisibleSession: &corev1.SharedFileSetVisibleSessionAction{SessionId: h.sessionID},
					},
				})
				eff := h.dispatch(&corev1.Event{
					Event: &corev1.Event_SharedFileStartDownload{
						SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{
							SessionId:         h.sessionID,
							FileId:            "f-ao",
							AutoOpenIfStaying: true,
						},
					},
				})
				insp := findInspectDisk(eff)
				eff = h.dispatch(&corev1.Event{
					Event: &corev1.Event_SharedFileDiskInspected{
						SharedFileDiskInspected: &corev1.SharedFileDiskInspectedEvent{OpId: insp.GetOpId(), SessionId: h.sessionID, FileId: "f-ao", Known: false},
					},
				})
				dc, _ := findDispatchChunk(eff)
				rc.revoke(h)
				eff = h.deliverChunkOK(dc, bytes.Repeat([]byte{1}, 64), 64)
				wc := findWriteChunk(eff)
				eff = h.dispatch(&corev1.Event{
					Event: &corev1.Event_SharedFileWriteChunkResult{
						SharedFileWriteChunkResult: &corev1.SharedFileWriteChunkResultEvent{OpId: wc.GetOpId(), SessionId: h.sessionID, FileId: "f-ao", Ok: true},
					},
				})
				vc := findVerifyCommit(eff)
				eff = h.dispatch(&corev1.Event{
					Event: &corev1.Event_SharedFileVerifyAndCommitResult{
						SharedFileVerifyAndCommitResult: &corev1.SharedFileVerifyAndCommitResultEvent{OpId: vc.GetOpId(), SessionId: h.sessionID, FileId: "f-ao", Ok: true},
					},
				})
				comp := findCompleted(eff)
				if comp == nil || comp.GetAutoOpen() {
					t.Fatalf("expected auto_open=false after %s, got %+v", rc.name, comp)
				}
			})
		}
	})

	t.Run("delete local, forget session, and clear all emit storage cleanup effects", func(t *testing.T) {
		h := newSFHarness(t, block, &v1.SharedFile{FileId: "f-del", Name: "pic.png", SizeBytes: 64, Sha256: sfTestHash(0xf1)})
		h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileLocalScanned{
				SharedFileLocalScanned: &corev1.SharedFileLocalScannedEvent{
					Entries: []*corev1.SharedFileLocalEntry{
						{SessionId: h.sessionID, FileId: "f-del", Completed: true, BytesOnDisk: 64, ExpectedSizeBytes: 64},
					},
				},
			},
		})
		if h.card("f-del").GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADED {
			t.Fatalf("expected DOWNLOADED before delete")
		}
		eff := h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileDeleteLocal{
				SharedFileDeleteLocal: &corev1.SharedFileDeleteLocalAction{SessionId: h.sessionID, FileId: "f-del"},
			},
		})
		if findDeleteLocal(eff) == nil || h.card("f-del").GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_AVAILABLE {
			t.Fatalf("expected DeleteLocal and AVAILABLE")
		}

		effForget := h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileForgetSession{
				SharedFileForgetSession: &corev1.SharedFileForgetSessionAction{SessionId: h.sessionID},
			},
		})
		var gotDelSess bool
		for _, ef := range effForget.GetEffects() {
			if ef.GetSharedFileDeleteSession().GetSessionId() == h.sessionID {
				gotDelSess = true
			}
		}
		if !gotDelSess {
			t.Fatalf("expected SharedFileDeleteSessionEffect")
		}

		effClear := h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileClearAll{SharedFileClearAll: &corev1.SharedFileClearAllAction{}},
		})
		var gotClear bool
		for _, ef := range effClear.GetEffects() {
			if ef.GetSharedFileClearAll() != nil {
				gotClear = true
			}
		}
		if !gotClear {
			t.Fatalf("expected SharedFileClearAllEffect")
		}
	})

	t.Run("lifecycle STOP pauses active download preserving partial and revoking auto-open", func(t *testing.T) {
		h := newSFHarness(t, block, &v1.SharedFile{FileId: "f-stop", Name: "clip.mp4", SizeBytes: 128, Sha256: sfTestHash(0xf2)})
		eff := h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileStartDownload{
				SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{
					SessionId:         h.sessionID,
					FileId:            "f-stop",
					AutoOpenIfStaying: true,
				},
			},
		})
		insp := findInspectDisk(eff)
		eff = h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileDiskInspected{
				SharedFileDiskInspected: &corev1.SharedFileDiskInspectedEvent{OpId: insp.GetOpId(), SessionId: h.sessionID, FileId: "f-stop", Known: false},
			},
		})
		dc0, _ := findDispatchChunk(eff)
		eff = h.deliverChunkOK(dc0, bytes.Repeat([]byte{1}, 64), 128)
		wc0 := findWriteChunk(eff)
		h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileWriteChunkResult{
				SharedFileWriteChunkResult: &corev1.SharedFileWriteChunkResultEvent{OpId: wc0.GetOpId(), SessionId: h.sessionID, FileId: "f-stop", Ok: true},
			},
		})
		effStop := h.dispatch(&corev1.Event{
			Event: &corev1.Event_SyncLifecycle{
				SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_STOP},
			},
		})
		if findDeletePartial(effStop) != nil {
			t.Fatalf("STOP must preserve partial blocks on disk")
		}
		if c := h.card("f-stop"); c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_PAUSED || c.GetDownloadedBytes() != 64 {
			t.Fatalf("expected PAUSED at 64 bytes after STOP, got %+v", c)
		}
	})

	t.Run("duration probed out-of-order merges onto downloaded file", func(t *testing.T) {
		h := newSFHarness(t, block, &v1.SharedFile{FileId: "f-dur", Name: "voice.m4a", SizeBytes: 64, Sha256: sfTestHash(0xf3)})
		h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileDurationProbed{
				SharedFileDurationProbed: &corev1.SharedFileDurationProbedEvent{SessionId: h.sessionID, FileId: "f-dur", DurationMs: 65_000},
			},
		})
		h.dispatch(&corev1.Event{
			Event: &corev1.Event_SharedFileLocalScanned{
				SharedFileLocalScanned: &corev1.SharedFileLocalScannedEvent{
					Entries: []*corev1.SharedFileLocalEntry{
						{SessionId: h.sessionID, FileId: "f-dur", Completed: true, BytesOnDisk: 64, ExpectedSizeBytes: 64},
					},
				},
			},
		})
		if c := h.card("f-dur"); c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADED || c.GetDurationText() != "1:05" {
			t.Fatalf("expected DOWNLOADED with merged 1:05 duration, got %+v", c)
		}
	})

	t.Run("stale verify guards new download and serializes per-key verify", func(t *testing.T) {
		interruptCases := []struct {
			name            string
			interrupt       func(h *sfHarness)
			restartDownload bool
			lateAtWrite     bool
		}{
			{
				name: "cancel during verify then restart waits for prior verify without DeleteLocal",
				interrupt: func(h *sfHarness) {
					h.dispatch(&corev1.Event{
						Event: &corev1.Event_SharedFileCancelDownload{
							SharedFileCancelDownload: &corev1.SharedFileCancelDownloadAction{SessionId: h.sessionID, FileId: "f-sv"},
						},
					})
				},
				restartDownload: true,
			},
			{
				name: "delete during verify then restart ignores late verify during write without DeleteLocal",
				interrupt: func(h *sfHarness) {
					h.dispatch(&corev1.Event{
						Event: &corev1.Event_SharedFileDeleteLocal{
							SharedFileDeleteLocal: &corev1.SharedFileDeleteLocalAction{SessionId: h.sessionID, FileId: "f-sv"},
						},
					})
				},
				restartDownload: true,
				lateAtWrite:     true,
			},
			{
				name: "ForgetSession during verify without restart cleans late commit",
				interrupt: func(h *sfHarness) {
					h.dispatch(&corev1.Event{
						Event: &corev1.Event_SharedFileForgetSession{
							SharedFileForgetSession: &corev1.SharedFileForgetSessionAction{SessionId: h.sessionID},
						},
					})
				},
				restartDownload: false,
			},
			{
				name: "ClearAll during verify without restart cleans late commit",
				interrupt: func(h *sfHarness) {
					h.dispatch(&corev1.Event{
						Event: &corev1.Event_SharedFileClearAll{SharedFileClearAll: &corev1.SharedFileClearAllAction{}},
					})
				},
				restartDownload: false,
			},
			{
				name: "ClearAll during verify then restart serializes verify without DeleteLocal",
				interrupt: func(h *sfHarness) {
					h.dispatch(&corev1.Event{
						Event: &corev1.Event_SharedFileClearAll{SharedFileClearAll: &corev1.SharedFileClearAllAction{}},
					})
				},
				restartDownload: true,
			},
		}

		for _, tc := range interruptCases {
			t.Run(tc.name, func(t *testing.T) {
				h := newSFHarness(t, block, &v1.SharedFile{FileId: "f-sv", Name: "doc.pdf", SizeBytes: 64, Sha256: sfTestHash(0xd9)})
				startToVerify := func() (*corev1.SharedFileWriteChunkEffect, *corev1.SharedFileVerifyAndCommitEffect) {
					eff := h.dispatch(&corev1.Event{
						Event: &corev1.Event_SharedFileStartDownload{
							SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{SessionId: h.sessionID, FileId: "f-sv"},
						},
					})
					insp := findInspectDisk(eff)
					eff = h.dispatch(&corev1.Event{
						Event: &corev1.Event_SharedFileDiskInspected{
							SharedFileDiskInspected: &corev1.SharedFileDiskInspectedEvent{OpId: insp.GetOpId(), SessionId: h.sessionID, FileId: "f-sv", Known: false},
						},
					})
					dc, _ := findDispatchChunk(eff)
					eff = h.deliverChunkOK(dc, bytes.Repeat([]byte{1}, 64), 64)
					wc := findWriteChunk(eff)
					if tc.lateAtWrite {
						return wc, nil
					}
					eff = h.dispatch(&corev1.Event{
						Event: &corev1.Event_SharedFileWriteChunkResult{
							SharedFileWriteChunkResult: &corev1.SharedFileWriteChunkResultEvent{OpId: wc.GetOpId(), SessionId: h.sessionID, FileId: "f-sv", Ok: true},
						},
					})
					return wc, findVerifyCommit(eff)
				}

				// Round 1 reaches VERIFYING.
				savedLate := tc.lateAtWrite
				tc.lateAtWrite = false
				_, vc1 := startToVerify()
				tc.lateAtWrite = savedLate
				if vc1 == nil {
					t.Fatalf("expected round-1 VerifyAndCommit")
				}

				tc.interrupt(h)

				if !tc.restartDownload {
					effLate := h.dispatch(&corev1.Event{
						Event: &corev1.Event_SharedFileVerifyAndCommitResult{
							SharedFileVerifyAndCommitResult: &corev1.SharedFileVerifyAndCommitResultEvent{
								OpId:      vc1.GetOpId(),
								SessionId: h.sessionID,
								FileId:    "f-sv",
								Ok:        true,
							},
						},
					})
					if findDeleteLocal(effLate) == nil {
						t.Fatalf("expected DeleteLocal for orphaned late verify commit")
					}
					if c := h.card("f-sv"); c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_AVAILABLE {
						t.Fatalf("expected AVAILABLE, got %+v", c)
					}
					return
				}

				// Round 2 starts immediately while vc1 is still in flight.
				wc2, vc2Early := startToVerify()
				if tc.lateAtWrite {
					// Late vc1 arrives while round 2 is writing its chunk: must NOT emit DeleteLocal!
					effLate := h.dispatch(&corev1.Event{
						Event: &corev1.Event_SharedFileVerifyAndCommitResult{
							SharedFileVerifyAndCommitResult: &corev1.SharedFileVerifyAndCommitResultEvent{
								OpId:      vc1.GetOpId(),
								SessionId: h.sessionID,
								FileId:    "f-sv",
								Ok:        true,
							},
						},
					})
					if findDeleteLocal(effLate) != nil {
						t.Fatalf("stale verify must not emit DeleteLocal while round 2 is active")
					}
					eff := h.dispatch(&corev1.Event{
						Event: &corev1.Event_SharedFileWriteChunkResult{
							SharedFileWriteChunkResult: &corev1.SharedFileWriteChunkResultEvent{OpId: wc2.GetOpId(), SessionId: h.sessionID, FileId: "f-sv", Ok: true},
						},
					})
					vc2 := findVerifyCommit(eff)
					if vc2 == nil {
						t.Fatalf("expected round-2 VerifyAndCommit after vc1 settled")
					}
					h.dispatch(&corev1.Event{
						Event: &corev1.Event_SharedFileVerifyAndCommitResult{
							SharedFileVerifyAndCommitResult: &corev1.SharedFileVerifyAndCommitResultEvent{OpId: vc2.GetOpId(), SessionId: h.sessionID, FileId: "f-sv", Ok: true},
						},
					})
					if c := h.card("f-sv"); c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADED {
						t.Fatalf("expected DOWNLOADED after round 2, got %+v", c)
					}
					return
				}

				// Round 2 reached verify stage while vc1 is still in flight: MUST NOT emit a concurrent VerifyAndCommit!
				if vc2Early != nil {
					t.Fatalf("must not emit a second concurrent VerifyAndCommit while vc1 is in flight")
				}
				// Now vc1 settles ok -> must NOT emit DeleteLocal, and must emit round-2 VerifyAndCommit!
				effLate := h.dispatch(&corev1.Event{
					Event: &corev1.Event_SharedFileVerifyAndCommitResult{
						SharedFileVerifyAndCommitResult: &corev1.SharedFileVerifyAndCommitResultEvent{
							OpId:      vc1.GetOpId(),
							SessionId: h.sessionID,
							FileId:    "f-sv",
							Ok:        true,
						},
					},
				})
				if findDeleteLocal(effLate) != nil {
					t.Fatalf("stale verify must not emit DeleteLocal when round 2 is waiting to verify")
				}
				vc2 := findVerifyCommit(effLate)
				if vc2 == nil || vc2.GetOpId() == vc1.GetOpId() {
					t.Fatalf("expected serialized round-2 VerifyAndCommit after vc1 finished, got %+v", vc2)
				}
				h.dispatch(&corev1.Event{
					Event: &corev1.Event_SharedFileVerifyAndCommitResult{
						SharedFileVerifyAndCommitResult: &corev1.SharedFileVerifyAndCommitResultEvent{
							OpId:      vc2.GetOpId(),
							SessionId: h.sessionID,
							FileId:    "f-sv",
							Ok:        true,
						},
					},
				})
				if c := h.card("f-sv"); c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADED {
					t.Fatalf("expected DOWNLOADED after serialized round-2 verify, got %+v", c)
				}
			})
		}
	})

	t.Run("pause and STOP during VERIFYING do not discard verified download", func(t *testing.T) {
		cases := []struct {
			name        string
			deliverOnly bool
			isStop      bool
			wantStatus  corev1.SharedFileCardStatus
		}{
			{
				name:        "pause during cached VERIFYING is ignored and settles DOWNLOADED",
				deliverOnly: false,
				isStop:      false,
				wantStatus:  corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADED,
			},
			{
				name:        "pause during deliver_only VERIFYING is ignored and settles AVAILABLE",
				deliverOnly: true,
				isStop:      false,
				wantStatus:  corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_AVAILABLE,
			},
			{
				name:        "STOP during cached VERIFYING revokes auto_open and settles DOWNLOADED",
				deliverOnly: false,
				isStop:      true,
				wantStatus:  corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADED,
			},
			{
				name:        "STOP during deliver_only VERIFYING settles AVAILABLE without DeleteLocal",
				deliverOnly: true,
				isStop:      true,
				wantStatus:  corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_AVAILABLE,
			},
		}

		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				h := newSFHarness(t, block, &v1.SharedFile{FileId: "f-vp", Name: "pack.zip", SizeBytes: 64, Sha256: sfTestHash(0xda)})
				if tc.deliverOnly {
					h.dispatch(&corev1.Event{
						Event: &corev1.Event_SharedFileConfig{
							SharedFileConfig: &corev1.SharedFileConfigEvent{
								LocalCacheMaxBytes: 32,
								StorageFullKind:    corev1.SharedFileFailureKind_SHARED_FILE_FAILURE_KIND_BROWSER_STORAGE_FULL,
							},
						},
					})
				}
				h.dispatch(&corev1.Event{
					Event: &corev1.Event_SharedFileSetVisibleSession{
						SharedFileSetVisibleSession: &corev1.SharedFileSetVisibleSessionAction{SessionId: h.sessionID},
					},
				})
				eff := h.dispatch(&corev1.Event{
					Event: &corev1.Event_SharedFileStartDownload{
						SharedFileStartDownload: &corev1.SharedFileStartDownloadAction{
							SessionId:         h.sessionID,
							FileId:            "f-vp",
							AutoOpenIfStaying: true,
						},
					},
				})
				insp := findInspectDisk(eff)
				eff = h.dispatch(&corev1.Event{
					Event: &corev1.Event_SharedFileDiskInspected{
						SharedFileDiskInspected: &corev1.SharedFileDiskInspectedEvent{OpId: insp.GetOpId(), SessionId: h.sessionID, FileId: "f-vp", Known: false},
					},
				})
				dc, _ := findDispatchChunk(eff)
				eff = h.deliverChunkOK(dc, bytes.Repeat([]byte{2}, 64), 64)
				wc := findWriteChunk(eff)
				eff = h.dispatch(&corev1.Event{
					Event: &corev1.Event_SharedFileWriteChunkResult{
						SharedFileWriteChunkResult: &corev1.SharedFileWriteChunkResultEvent{OpId: wc.GetOpId(), SessionId: h.sessionID, FileId: "f-vp", Ok: true},
					},
				})
				vc := findVerifyCommit(eff)
				if vc == nil || vc.GetDeliverOnly() != tc.deliverOnly {
					t.Fatalf("expected VerifyAndCommit(deliverOnly=%v), got %+v", tc.deliverOnly, vc)
				}

				if tc.isStop {
					h.dispatch(&corev1.Event{
						Event: &corev1.Event_SyncLifecycle{
							SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_STOP},
						},
					})
				} else {
					h.dispatch(&corev1.Event{
						Event: &corev1.Event_SharedFilePauseDownload{
							SharedFilePauseDownload: &corev1.SharedFilePauseDownloadAction{SessionId: h.sessionID, FileId: "f-vp"},
						},
					})
				}
				if c := h.card("f-vp"); c.GetStatus() != corev1.SharedFileCardStatus_SHARED_FILE_CARD_STATUS_DOWNLOADING {
					t.Fatalf("expected card to remain DOWNLOADING during VERIFYING, got %+v", c)
				}

				effDone := h.dispatch(&corev1.Event{
					Event: &corev1.Event_SharedFileVerifyAndCommitResult{
						SharedFileVerifyAndCommitResult: &corev1.SharedFileVerifyAndCommitResultEvent{
							OpId:      vc.GetOpId(),
							SessionId: h.sessionID,
							FileId:    "f-vp",
							Ok:        true,
						},
					},
				})
				if findDeleteLocal(effDone) != nil {
					t.Fatalf("must not emit DeleteLocal after pause/STOP during VERIFYING")
				}
				comp := findCompleted(effDone)
				if comp == nil || comp.GetDeliverOnly() != tc.deliverOnly {
					t.Fatalf("expected SharedFileCompletedEffect(deliverOnly=%v), got %+v", tc.deliverOnly, comp)
				}
				wantAutoOpen := !tc.isStop && !tc.deliverOnly
				if comp.GetAutoOpen() != wantAutoOpen {
					t.Fatalf("auto_open = %v, want %v", comp.GetAutoOpen(), wantAutoOpen)
				}
				if c := h.card("f-vp"); c.GetStatus() != tc.wantStatus {
					t.Fatalf("final status = %v, want %v", c.GetStatus(), tc.wantStatus)
				}
			})
		}
	})
}
