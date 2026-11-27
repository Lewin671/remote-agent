package app

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/internal/test/testrelay"
	"github.com/Lewin671/remote-agent/clientcore/security"
	"github.com/Lewin671/remote-agent/clientcore/text"
	stdv1 "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"google.golang.org/protobuf/proto"
)

func openDaemonEnvelopeWithSeed(
	t *testing.T,
	rawEnv []byte,
	sec, seed []byte,
	expectedSignerEdPub ed25519.PublicKey,
) (devicesec.Header, []byte) {
	t.Helper()
	var env v1.Envelope
	if err := env.Unmarshal(rawEnv); err != nil {
		t.Fatalf("unmarshal sealed Envelope: %v", err)
	}
	ct, err := security.FromProtoEnvelope(&env)
	if err != nil {
		t.Fatalf("FromProtoEnvelope: %v", err)
	}
	om, err := ct.OriginBytes()
	if err != nil {
		t.Fatalf("OriginBytes: %v", err)
	}
	if !ed25519.Verify(expectedSignerEdPub, om, ct.Origin) {
		t.Fatal("Ed25519 signature verification failed on Core-sealed terminal envelope")
	}
	hdr, key, err := devicesec.DeriveCipherKey(sec, seed, ct)
	if err != nil {
		t.Fatalf("DeriveCipherKey: %v", err)
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		t.Fatalf("cipher.NewGCM: %v", err)
	}
	plain, err := aead.Open(nil, ct.Nonce, ct.Cipher, ct.Header)
	if err != nil {
		t.Fatalf("AES-GCM Open failed: %v", err)
	}
	return hdr, plain
}

func openCoreTerminalForTest(
	t *testing.T,
	c *Core,
	phone, trusted appTestActor,
	dom devicesec.Hash,
	st devicesec.State,
	sec, seed []byte,
	sid, tid string,
	nowMs int64,
) (streamID string, legID []byte) {
	t.Helper()
	// Enable relayPipe mode + provide entropy + start sync engine.
	// Start sync and deliver warm token.
	randBytes := make([]byte, 512)
	_, _ = rand.Read(randBytes)
	c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 1,
		MonoMs: 1201,
		Event: &corev1.Event_EntropyProvided{
			EntropyProvided: &corev1.EntropyProvided{
				RandomIds:   []string{"stream-id-00000001", "req-open-term-0001", "att-open-term-0001", "th-req-00000000001"},
				UnitFloats:  []float64{0.2, 0.4, 0.6, 0.8},
				RandomBytes: randBytes,
			},
		},
	})
	// Start sync engine (producing e.running == true, matching production) and seed a fresh login token.
	startBatch := c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 2,
		MonoMs: 1202,
		Event: &corev1.Event_SyncLifecycle{
			SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START},
		},
	})
	for q := append([]*corev1.Effect(nil), startBatch.Effects...); len(q) > 0; {
		eff := q[0]
		q = q[1:]
		switch k := eff.Effect.(type) {
		case *corev1.Effect_SecurityStorage:
			if k.SecurityStorage.Op == corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP {
				next := c.Dispatch(&corev1.Event{
					NowMs:  nowMs + 3,
					MonoMs: 1203,
					Event: &corev1.Event_SecurityStorageResult{
						SecurityStorageResult: &corev1.SecurityStorageResultEvent{
							OpId:                k.SecurityStorage.OpId,
							Found:               true,
							LoginToken:          "term-test-tok",
							LoginTokenExpiresMs: nowMs + 7*24*3600*1000,
						},
					},
				})
				q = append(q, next.Effects...)
			}
		}
	}
	// Handle HEALTHZ + FORWARD naturally through RelayRpcEffect.
	openBatch := c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 5,
		MonoMs: 1205,
		Event: &corev1.Event_TerminalOpen{
			TerminalOpen: &corev1.TerminalOpenAction{
				OpId:       1,
				SessionId:  sid,
				MachineId:  trusted.id,
				TerminalId: tid,
			},
		},
	})
	var openSock *corev1.TerminalOpenSocketEffect
	for _, eff := range openBatch.Effects {
		if os := eff.GetTerminalOpenSocket(); os != nil {
			openSock = os
		}
	}
	if openSock == nil || !openSock.IsInitial || openSock.StreamId == "" {
		t.Fatalf("expected initial TerminalOpenSocketEffect, got %+v", openBatch.Effects)
	}
	var hello stdv1.TermHello
	if err := proto.Unmarshal(openSock.HelloBytes, &hello); err != nil {
		t.Fatalf("unmarshal TermHello: %v", err)
	}
	if hello.StreamId != openSock.StreamId || hello.MachineId != trusted.id {
		t.Fatalf("unexpected TermHello: %+v", &hello)
	}

	// Simulate WS open -> Core emits KeySign (for OpenTerminal command) -> RelayRpc(FORWARD).
	sockBatch := c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 10,
		MonoMs: 1210,
		Event: &corev1.Event_TerminalSocket{
			TerminalSocket: &corev1.TerminalSocketEvent{
				StreamId: openSock.StreamId,
				LegId:    openSock.LegId,
				Opened:   true,
			},
		},
	})

	var fwdRpc *corev1.RelayRpcEffect
	var stateLive *corev1.TerminalStateChangedEffect
	queue := sockBatch.Effects
	for len(queue) > 0 {
		eff := queue[0]
		queue = queue[1:]
		switch k := eff.Effect.(type) {
		case *corev1.Effect_SecurityStorage:
			next := c.Dispatch(&corev1.Event{
				NowMs:  nowMs + 12,
				MonoMs: 1212,
				Event: &corev1.Event_SecurityStorageResult{
					SecurityStorageResult: &corev1.SecurityStorageResultEvent{OpId: k.SecurityStorage.OpId},
				},
			})
			queue = append(queue, next.Effects...)
		case *corev1.Effect_SecurityKeySign:
			sig := ed25519.Sign(phone.ed, k.SecurityKeySign.Message)
			next := c.Dispatch(&corev1.Event{
				NowMs:  nowMs + 11,
				MonoMs: 1211,
				Event: &corev1.Event_SecurityKeySigned{
					SecurityKeySigned: &corev1.SecurityKeySignedEvent{OpId: k.SecurityKeySign.OpId, Signature: sig},
				},
			})
			queue = append(queue, next.Effects...)
		case *corev1.Effect_SecurityCryptoBatch:
			next := c.Dispatch(&corev1.Event{
				NowMs:  nowMs + 12,
				MonoMs: 1212,
				Event: &corev1.Event_SecurityCryptoBatchDone{
					SecurityCryptoBatchDone: runTestCryptoBatch(t, k.SecurityCryptoBatch),
				},
			})
			queue = append(queue, next.Effects...)
		case *corev1.Effect_RelayRpc:
			if k.RelayRpc.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_CHALLENGE {
				respBytes, _ := proto.Marshal(&stdv1.GetChallengeResponse{Challenge: []byte("chal-term-1")})
				next := c.Dispatch(&corev1.Event{
					NowMs:  nowMs + 12,
					MonoMs: 1212,
					Event:  &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{OpId: k.RelayRpc.OpId, ResponseBytes: respBytes}},
				})
				queue = append(queue, next.Effects...)
				continue
			}
			if k.RelayRpc.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_LOGIN {
				respBytes, _ := proto.Marshal(&stdv1.LoginResponse{Token: "tok-term-1", ExpiresMs: nowMs + 86_400_000})
				next := c.Dispatch(&corev1.Event{
					NowMs:  nowMs + 12,
					MonoMs: 1212,
					Event:  &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{OpId: k.RelayRpc.OpId, ResponseBytes: respBytes}},
				})
				queue = append(queue, next.Effects...)
				continue
			}
			if k.RelayRpc.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_HEALTHZ {
				next := c.Dispatch(&corev1.Event{
					NowMs:  nowMs + 13,
					MonoMs: 1213,
					Event: &corev1.Event_RelayRpcResult{
						RelayRpcResult: &corev1.RelayRpcResultEvent{
							OpId:       k.RelayRpc.OpId,
							RelayEpoch: "epoch-1",
						},
					},
				})
				queue = append(queue, next.Effects...)
				continue
			}
			if k.RelayRpc.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_FORWARD {
				fwdRpc = k.RelayRpc
				var fwdReq stdv1.ForwardRequest
				if err := proto.Unmarshal(fwdRpc.RequestBytes, &fwdReq); err != nil {
					t.Fatalf("unmarshal ForwardRequest: %v", err)
				}
				// Daemon opens command, computes hashes, and seals executed CommandResult!
				cmdEnvBytes, _ := proto.Marshal(fwdReq.Command)
				cmdHdr, cmdPlain := openDaemonEnvelopeWithSeed(t, cmdEnvBytes, sec, nil, phone.edP)
				var cmd stdv1.Command
				if err := proto.Unmarshal(cmdPlain, &cmd); err != nil {
					t.Fatalf("unmarshal Command: %v", err)
				}
				if cmd.GetOpenTerminal().GetStreamId() != openSock.StreamId {
					t.Fatalf("OpenTerminal streamId mismatch: got %q want %q", cmd.GetOpenTerminal().GetStreamId(), openSock.StreamId)
				}
				reqKeyHash := devicesec.RequestIdentity(cmdHdr)
				payloadHash, err := devicesec.AuthorizedPayloadHash(cmdHdr, cmdPlain)
				if err != nil {
					t.Fatalf("AuthorizedPayloadHash: %v", err)
				}
				resPlain, _ := proto.Marshal(&stdv1.CommandResult{
					Outcome:         "executed",
					Reason:          "ok",
					ResultAvailable: true,
					RequestKeyHash:  reqKeyHash[:],
					PayloadHash:     payloadHash[:],
					Kind:            &stdv1.CommandResult_OpenTerminal{OpenTerminal: &stdv1.OpenTerminalResponse{}},
				})
				resEnv := sealDaemonWireWithSeed(t, trusted, dom, st, sec, nil, resPlain, devicesec.Result, trusted.id, sid, cmdHdr.Request, 0)
				rawResEnv, _ := resEnv.Marshal()
				var stdResEnv stdv1.Envelope
				_ = proto.Unmarshal(rawResEnv, &stdResEnv)
				fwdRespBytes, _ := proto.Marshal(&stdv1.ForwardResponse{
					AttemptId: fwdReq.AttemptId,
					Outcome:   "executed",
					Reason:    "ok",
					Result:    &stdResEnv,
				})
				next := c.Dispatch(&corev1.Event{
					NowMs:  nowMs + 15,
					MonoMs: 1215,
					Event: &corev1.Event_RelayRpcResult{
						RelayRpcResult: &corev1.RelayRpcResultEvent{
							OpId:          fwdRpc.OpId,
							ResponseBytes: fwdRespBytes,
						},
					},
				})
				queue = append(queue, next.Effects...)
			}
		case *corev1.Effect_TerminalStateChanged:
			if k.TerminalStateChanged.OpenSettled {
				stateLive = k.TerminalStateChanged
			}
		}
	}
	if fwdRpc == nil {
		t.Fatal("expected Forward RelayRpcEffect for OpenTerminal")
	}
	if stateLive == nil || !stateLive.OpenOk || stateLive.State != corev1.TerminalLifecycleState_TERMINAL_LIFECYCLE_STATE_UNSPECIFIED {
		t.Fatalf("expected OpenSettled ok state, got %+v", stateLive)
	}
	return openSock.StreamId, openSock.LegId
}

func TestTerminalPipe_CrossVerifyCryptoPurposesSeqHistoryAndZeroRender(t *testing.T) {
	c, phone, trusted, _, _, dom, s1, sec, _, _, nowMs := setupOpaqueCacheTestCore(t, true)
	seed := bytes.Repeat([]byte{0x9E}, 32)
	seedSessionInCore(t, c, dom, s1, sec, trusted, "s-term", seed, nil, nowMs+50)

	tid := "term-ui-1"
	streamID, _ := openCoreTerminalForTest(t, c, phone, trusted, dom, s1, sec, seed, "s-term", tid, nowMs+100)

	// 1. Outbound TerminalInput (Purpose 8), TerminalResize (Purpose 14), TerminalHistory (Purpose 15)
	//    Verify monotonic seq 1, 2, 3 and cross-decrypt with daemon devicesec!
	inputs := []struct {
		name        string
		act         *corev1.TerminalInputAction
		wantPurpose devicesec.Purpose
		wantSeq     uint64
		check       func(t *testing.T, tf *stdv1.TerminalFrame)
	}{
		{
			name:        "input",
			act:         &corev1.TerminalInputAction{StreamId: streamID, Kind: &corev1.TerminalInputAction_Input{Input: []byte("ls -la\r")}},
			wantPurpose: devicesec.TerminalInput,
			wantSeq:     1,
			check: func(t *testing.T, tf *stdv1.TerminalFrame) {
				if !bytes.Equal(tf.GetInput(), []byte("ls -la\r")) {
					t.Fatalf("input mismatch: %q", tf.GetInput())
				}
			},
		},
		{
			name: "resize",
			act: &corev1.TerminalInputAction{
				StreamId: streamID,
				Kind:     &corev1.TerminalInputAction_Resize{Resize: &corev1.TerminalResizeInput{Cols: 132, Rows: 43, Fit: true}},
			},
			wantPurpose: devicesec.TerminalResize,
			wantSeq:     2,
			check: func(t *testing.T, tf *stdv1.TerminalFrame) {
				r := tf.GetResize()
				if r == nil || r.Cols != 132 || r.Rows != 43 || !r.Fit {
					t.Fatalf("resize mismatch: %+v", r)
				}
			},
		},
		{
			name: "history",
			act: &corev1.TerminalInputAction{
				StreamId: streamID,
				Kind:     &corev1.TerminalInputAction_History{History: &corev1.TerminalHistoryInput{RequestId: "hr-1", Cursor: "c-42"}},
			},
			wantPurpose: devicesec.TerminalHistory,
			wantSeq:     3,
			check: func(t *testing.T, tf *stdv1.TerminalFrame) {
				h := tf.GetHistoryRequest()
				if h == nil || h.RequestId != "hr-1" || h.Cursor != "c-42" {
					t.Fatalf("history request mismatch: %+v", h)
				}
			},
		},
	}

	for i, tc := range inputs {
		t.Run("outbound/"+tc.name, func(t *testing.T) {
			b := c.Dispatch(&corev1.Event{
				NowMs:  nowMs + 200 + int64(i)*10,
				MonoMs: 2200 + int64(i)*10,
				Event:  &corev1.Event_TerminalInput{TerminalInput: tc.act},
			})
			var sent *corev1.TerminalDirectSendEffect
			queue := b.Effects
			for len(queue) > 0 {
				eff := queue[0]
				queue = queue[1:]
				if ks := eff.GetSecurityKeySign(); ks != nil {
					sig := ed25519.Sign(phone.ed, ks.Message)
					next := c.Dispatch(&corev1.Event{
						NowMs:  nowMs + 201 + int64(i)*10,
						MonoMs: 2201 + int64(i)*10,
						Event:  &corev1.Event_SecurityKeySigned{SecurityKeySigned: &corev1.SecurityKeySignedEvent{OpId: ks.OpId, Signature: sig}},
					})
					queue = append(queue, next.Effects...)
				} else if ds := eff.GetTerminalDirectSend(); ds != nil {
					sent = ds
				}
			}
			if sent == nil || !sent.SendRelay || len(sent.SealedEnvelope) == 0 {
				t.Fatalf("expected TerminalDirectSendEffect, got %+v", sent)
			}
			hdr, plain := openDaemonEnvelopeWithSeed(t, sent.SealedEnvelope, sec, seed, phone.edP)
			if hdr.Purpose != tc.wantPurpose || hdr.Seq != tc.wantSeq || hdr.Session != "s-term" || hdr.Request != streamID || hdr.Machine != trusted.id {
				t.Fatalf("header mismatch: purpose=%v(want %v) seq=%d(want %d) sid=%q req=%q", hdr.Purpose, tc.wantPurpose, hdr.Seq, tc.wantSeq, hdr.Session, hdr.Request)
			}
			var tf stdv1.TerminalFrame
			if err := proto.Unmarshal(plain, &tf); err != nil {
				t.Fatalf("unmarshal TerminalFrame: %v", err)
			}
			tc.check(t, &tf)
		})
	}

	// 2. Inbound TerminalOutput (Purpose 7): Snapshot + Output -> verify ZERO RenderEffect on hot path!
	snapBytes, _ := proto.Marshal(&stdv1.TerminalFrame{
		Kind: &stdv1.TerminalFrame_Snapshot{Snapshot: &stdv1.TerminalSnapshot{Cols: 120, Rows: 40, Data: []byte("hello ")}},
	})
	snapEnv := sealDaemonWireWithSeed(t, trusted, dom, s1, sec, seed, snapBytes, devicesec.TerminalOutput, trusted.id, "s-term", streamID, 1)
	snapRaw, _ := snapEnv.Marshal()
	bSnap := c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 300,
		MonoMs: 2300,
		Event: &corev1.Event_TerminalInboundBytes{
			TerminalInboundBytes: &corev1.TerminalInboundBytesEvent{StreamId: streamID, RawEnvelope: snapRaw},
		},
	})
	var deliveredSnap *corev1.TerminalDeliverOutputEffect
	for _, eff := range bSnap.Effects {
		if eff.GetRender() != nil {
			t.Fatalf("HOT PATH VIOLATION: TerminalInboundBytes emitted RenderEffect %+v", eff.GetRender())
		}
		if d := eff.GetTerminalDeliverOutput(); d != nil {
			deliveredSnap = d
		}
	}
	if deliveredSnap == nil || len(deliveredSnap.Frames) != 1 || deliveredSnap.Frames[0].GetSnapshot().GetCols() != 120 || !bytes.Equal(deliveredSnap.Frames[0].GetSnapshot().GetData(), []byte("hello ")) {
		t.Fatalf("unexpected snapshot delivery: %+v", deliveredSnap)
	}

	outBytes, _ := proto.Marshal(&stdv1.TerminalFrame{
		Kind: &stdv1.TerminalFrame_Output{Output: []byte("world!")},
	})
	outEnv := sealDaemonWireWithSeed(t, trusted, dom, s1, sec, seed, outBytes, devicesec.TerminalOutput, trusted.id, "s-term", streamID, 2)
	outRaw, _ := outEnv.Marshal()
	bOut := c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 301,
		MonoMs: 2301,
		Event: &corev1.Event_TerminalInboundBytes{
			TerminalInboundBytes: &corev1.TerminalInboundBytesEvent{StreamId: streamID, RawEnvelope: outRaw},
		},
	})
	var deliveredOut *corev1.TerminalDeliverOutputEffect
	for _, eff := range bOut.Effects {
		if eff.GetRender() != nil {
			t.Fatalf("HOT PATH VIOLATION: output frame emitted RenderEffect %+v", eff.GetRender())
		}
		if d := eff.GetTerminalDeliverOutput(); d != nil {
			deliveredOut = d
		}
	}
	if deliveredOut == nil || len(deliveredOut.Frames) != 1 || !bytes.Equal(deliveredOut.Frames[0].GetOutput(), []byte("world!")) {
		t.Fatalf("unexpected output delivery: %+v", deliveredOut)
	}

	// 3. Inbound HistoryPage feeds DIRECTLY into Core's termhistory.Machine -> TerminalHistoryUpdatedEffect!
	openHist := c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 310,
		MonoMs: 2310,
		Event: &corev1.Event_TerminalHistory{
			TerminalHistory: &corev1.TerminalHistoryEvent{
				TerminalId: tid,
				Kind:       corev1.TerminalHistoryEventKind_TERMINAL_HISTORY_EVENT_KIND_OPEN,
			},
		},
	})
	histReq := findTermHistorySendReq(openHist)
	if histReq == nil || histReq.RequestId == "" {
		t.Fatalf("expected TerminalHistorySendRequestEffect")
	}
	hpBytes, _ := proto.Marshal(&stdv1.TerminalFrame{
		Kind: &stdv1.TerminalFrame_HistoryPage{
			HistoryPage: &stdv1.TerminalHistoryPage{
				RequestId:  histReq.RequestId,
				Lines:      []string{"line 1", "line 2"},
				NextCursor: "next-1",
			},
		},
	})
	hpEnv := sealDaemonWireWithSeed(t, trusted, dom, s1, sec, seed, hpBytes, devicesec.TerminalOutput, trusted.id, "s-term", streamID, 3)
	hpRaw, _ := hpEnv.Marshal()
	bHP := c.Dispatch(&corev1.Event{
		NowMs:  nowMs + 315,
		MonoMs: 2315,
		Event: &corev1.Event_TerminalInboundBytes{
			TerminalInboundBytes: &corev1.TerminalInboundBytesEvent{StreamId: streamID, RawEnvelope: hpRaw},
		},
	})
	upd := findTermHistoryUpdated(bHP)
	if upd == nil || upd.Phase != corev1.TerminalHistoryPhase_TERMINAL_HISTORY_PHASE_OPEN || upd.AddedPage == nil || len(upd.AddedPage.Lines) != 2 {
		t.Fatalf("expected HistoryPage to feed directly into Core termhistory.Machine, got %+v", upd)
	}
	for _, eff := range bHP.Effects {
		if d := eff.GetTerminalDeliverOutput(); d != nil && len(d.Frames) > 0 {
			t.Fatal("HistoryPage must not be redundantly emitted as output frames on TerminalDeliverOutputEffect")
		}
	}
}

func TestTerminalPipe_NegativeCryptoReplayAndLifecycleStates(t *testing.T) {
	c, phone, trusted, _, _, dom, s1, sec, _, _, nowMs := setupOpaqueCacheTestCore(t, true)
	seed := bytes.Repeat([]byte{0x77}, 32)
	wrongSeed := bytes.Repeat([]byte{0x88}, 32)
	seedSessionInCore(t, c, dom, s1, sec, trusted, "s-neg-t", seed, nil, nowMs+50)

	// Case 1: Tampered direct frame is silently dropped without closing stream; tampered relay frame closes stream!
	streamID, _ := openCoreTerminalForTest(t, c, phone, trusted, dom, s1, sec, seed, "s-neg-t", "", nowMs+100)
	outBytes, _ := proto.Marshal(&stdv1.TerminalFrame{Kind: &stdv1.TerminalFrame_Output{Output: []byte("ok")}})
	tamperedEnv := sealDaemonWireWithSeed(t, trusted, dom, s1, sec, seed, outBytes, devicesec.TerminalOutput, trusted.id, "s-neg-t", streamID, 1)
	tamperedEnv.Ciphertext[0] ^= 0xff
	tamperedRaw, _ := tamperedEnv.Marshal()

	bDirectBad := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 120, MonoMs: 1300,
		Event: &corev1.Event_TerminalInboundBytes{
			TerminalInboundBytes: &corev1.TerminalInboundBytesEvent{StreamId: streamID, RawEnvelope: tamperedRaw, FromDirect: true, PcId: 1},
		},
	})
	for _, eff := range bDirectBad.Effects {
		if d := eff.GetTerminalDeliverOutput(); d != nil && len(d.Frames) > 0 {
			t.Fatalf("tampered direct frame must not deliver output frames, got %+v", d)
		}
		if eff.GetTerminalStateChanged() != nil {
			t.Fatalf("tampered direct frame must not close stream, got %+v", eff)
		}
	}

	// Wrong Purpose (Event instead of TerminalOutput) on relay -> closes stream!
	wrongPurposeEnv := sealDaemonWireWithSeed(t, trusted, dom, s1, sec, seed, outBytes, devicesec.Event, trusted.id, "s-neg-t", "", 1)
	wrongPurposeRaw, _ := wrongPurposeEnv.Marshal()
	bWrongP := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 125, MonoMs: 1305,
		Event: &corev1.Event_TerminalInboundBytes{
			TerminalInboundBytes: &corev1.TerminalInboundBytesEvent{StreamId: streamID, RawEnvelope: wrongPurposeRaw},
		},
	})
	var closedState *corev1.TerminalStateChangedEffect
	for _, eff := range bWrongP.Effects {
		if st := eff.GetTerminalStateChanged(); st != nil {
			closedState = st
		}
	}
	if closedState == nil || closedState.State != corev1.TerminalLifecycleState_TERMINAL_LIFECYCLE_STATE_CLOSED {
		t.Fatalf("expected CLOSED on wrong Purpose, got %+v", closedState)
	}

	// Case 2: Wrong sessionSeed -> rejected & closed!
	stream2, _ := openCoreTerminalForTest(t, c, phone, trusted, dom, s1, sec, seed, "s-neg-t", "", nowMs+200)
	wrongSeedEnv := sealDaemonWireWithSeed(t, trusted, dom, s1, sec, wrongSeed, outBytes, devicesec.TerminalOutput, trusted.id, "s-neg-t", stream2, 1)
	wrongSeedRaw, _ := wrongSeedEnv.Marshal()
	bWrongSeed := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 220, MonoMs: 1400,
		Event: &corev1.Event_TerminalInboundBytes{
			TerminalInboundBytes: &corev1.TerminalInboundBytesEvent{StreamId: stream2, RawEnvelope: wrongSeedRaw},
		},
	})
	closedState = nil
	for _, eff := range bWrongSeed.Effects {
		if st := eff.GetTerminalStateChanged(); st != nil {
			closedState = st
		}
	}
	if closedState == nil || closedState.State != corev1.TerminalLifecycleState_TERMINAL_LIFECYCLE_STATE_CLOSED {
		t.Fatalf("expected CLOSED on wrong sessionSeed, got %+v", closedState)
	}

	// Case 3: Conflicting payload at replayed seq -> CLOSED with reason "conflict" + localized KeyErrorsTerminalFramesAreOutOfOrder!
	stream3, _ := openCoreTerminalForTest(t, c, phone, trusted, dom, s1, sec, seed, "s-neg-t", "", nowMs+300)
	env1 := sealDaemonWireWithSeed(t, trusted, dom, s1, sec, seed, outBytes, devicesec.TerminalOutput, trusted.id, "s-neg-t", stream3, 1)
	raw1, _ := env1.Marshal()
	c.Dispatch(&corev1.Event{
		NowMs: nowMs + 320, MonoMs: 1500,
		Event: &corev1.Event_TerminalInboundBytes{
			TerminalInboundBytes: &corev1.TerminalInboundBytesEvent{StreamId: stream3, RawEnvelope: raw1},
		},
	})
	conflictBytes, _ := proto.Marshal(&stdv1.TerminalFrame{Kind: &stdv1.TerminalFrame_Output{Output: []byte("DIFFERENT")}})
	envConflict := sealDaemonWireWithSeed(t, trusted, dom, s1, sec, seed, conflictBytes, devicesec.TerminalOutput, trusted.id, "s-neg-t", stream3, 1)
	rawConflict, _ := envConflict.Marshal()
	bConflict := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 325, MonoMs: 1505,
		Event: &corev1.Event_TerminalInboundBytes{
			TerminalInboundBytes: &corev1.TerminalInboundBytesEvent{StreamId: stream3, RawEnvelope: rawConflict},
		},
	})
	closedState = nil
	for _, eff := range bConflict.Effects {
		if st := eff.GetTerminalStateChanged(); st != nil {
			closedState = st
		}
	}
	if closedState == nil || closedState.State != corev1.TerminalLifecycleState_TERMINAL_LIFECYCLE_STATE_CLOSED ||
		closedState.ReasonCode != "conflict" || closedState.GetReasonText().GetKey() != text.KeyErrorsTerminalFramesAreOutOfOrder {
		t.Fatalf("expected conflict CLOSED with KeyErrorsTerminalFramesAreOutOfOrder, got %+v", closedState)
	}
}

func TestTerminalPipe_WebCryptoBatchOver16KiB(t *testing.T) {
	c, phone, trusted, _, _, dom, s1, sec, _, _, nowMs := setupOpaqueCacheTestCore(t, false)
	seed := bytes.Repeat([]byte{0x4F}, 32)
	seedSessionInCore(t, c, dom, s1, sec, trusted, "s-big-t", seed, nil, nowMs+50)
	streamID, _ := openCoreTerminalForTest(t, c, phone, trusted, dom, s1, sec, seed, "s-big-t", "", nowMs+100)

	// Outbound >16 KiB input offloads AES-GCM seal via SecurityCryptoBatchEffect!
	bigIn := bytes.Repeat([]byte("A"), 20*1024)
	bIn := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 150, MonoMs: 1500,
		Event: &corev1.Event_TerminalInput{
			TerminalInput: &corev1.TerminalInputAction{
				StreamId: streamID,
				Kind:     &corev1.TerminalInputAction_Input{Input: bigIn},
			},
		},
	})
	batches := 0
	var sent *corev1.TerminalDirectSendEffect
	queue := bIn.Effects
	for len(queue) > 0 {
		eff := queue[0]
		queue = queue[1:]
		switch k := eff.Effect.(type) {
		case *corev1.Effect_SecurityCryptoBatch:
			batches++
			next := c.Dispatch(&corev1.Event{
				NowMs: nowMs + 151, MonoMs: 1501,
				Event: &corev1.Event_SecurityCryptoBatchDone{SecurityCryptoBatchDone: runTestCryptoBatch(t, k.SecurityCryptoBatch)},
			})
			queue = append(queue, next.Effects...)
		case *corev1.Effect_SecurityStorage:
			next := c.Dispatch(&corev1.Event{
				NowMs:  nowMs + 12,
				MonoMs: 1212,
				Event: &corev1.Event_SecurityStorageResult{
					SecurityStorageResult: &corev1.SecurityStorageResultEvent{OpId: k.SecurityStorage.OpId},
				},
			})
			queue = append(queue, next.Effects...)
		case *corev1.Effect_SecurityKeySign:
			sig := ed25519.Sign(phone.ed, k.SecurityKeySign.Message)
			next := c.Dispatch(&corev1.Event{
				NowMs: nowMs + 152, MonoMs: 1502,
				Event: &corev1.Event_SecurityKeySigned{SecurityKeySigned: &corev1.SecurityKeySignedEvent{OpId: k.SecurityKeySign.OpId, Signature: sig}},
			})
			queue = append(queue, next.Effects...)
		case *corev1.Effect_TerminalDirectSend:
			sent = k.TerminalDirectSend
		}
	}
	if batches != 1 || sent == nil {
		t.Fatalf("expected 1 CryptoBatch + TerminalDirectSendEffect for >16 KiB input, got batches=%d sent=%v", batches, sent != nil)
	}
	hdr, plain := openDaemonEnvelopeWithSeed(t, sent.SealedEnvelope, sec, seed, phone.edP)
	var tf stdv1.TerminalFrame
	_ = proto.Unmarshal(plain, &tf)
	if hdr.Purpose != devicesec.TerminalInput || !bytes.Equal(tf.GetInput(), bigIn) {
		t.Fatalf("decrypted >16 KiB input mismatch")
	}

	// Inbound >16 KiB output offloads AES-GCM open via SecurityCryptoBatchEffect!
	bigOut := bytes.Repeat([]byte("B"), 24*1024)
	outBytes, _ := proto.Marshal(&stdv1.TerminalFrame{Kind: &stdv1.TerminalFrame_Output{Output: bigOut}})
	outEnv := sealDaemonWireWithSeed(t, trusted, dom, s1, sec, seed, outBytes, devicesec.TerminalOutput, trusted.id, "s-big-t", streamID, 1)
	outRaw, _ := outEnv.Marshal()
	bOut := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 200, MonoMs: 1600,
		Event: &corev1.Event_TerminalInboundBytes{
			TerminalInboundBytes: &corev1.TerminalInboundBytesEvent{StreamId: streamID, RawEnvelope: outRaw},
		},
	})
	inBatches := 0
	var delivered *corev1.TerminalDeliverOutputEffect
	queue = bOut.Effects
	for len(queue) > 0 {
		eff := queue[0]
		queue = queue[1:]
		if eff.GetRender() != nil {
			t.Fatal("HOT PATH VIOLATION: >16 KiB terminal output emitted RenderEffect")
		}
		if cb := eff.GetSecurityCryptoBatch(); cb != nil {
			inBatches++
			next := c.Dispatch(&corev1.Event{
				NowMs: nowMs + 201, MonoMs: 1601,
				Event: &corev1.Event_SecurityCryptoBatchDone{SecurityCryptoBatchDone: runTestCryptoBatch(t, cb)},
			})
			queue = append(queue, next.Effects...)
		} else if d := eff.GetTerminalDeliverOutput(); d != nil {
			delivered = d
		}
	}
	if inBatches != 1 || delivered == nil || len(delivered.Frames) != 1 || !bytes.Equal(delivered.Frames[0].GetOutput(), bigOut) {
		t.Fatalf("expected 1 CryptoBatch + exact 24 KiB output, got batches=%d delivered=%v", inBatches, delivered != nil)
	}
}

func BenchmarkTerminalHotPath5MB(b *testing.B) {
	t := &testing.T{}
	c, phone, trusted, _, _, dom, s1, sec, _, _, nowMs := setupOpaqueCacheTestCore(t, true)
	seed := bytes.Repeat([]byte{0x5A}, 32)
	seedSessionInCore(t, c, dom, s1, sec, trusted, "s-bench", seed, nil, nowMs+50)
	chunk := bytes.Repeat([]byte("X"), 4096)      // 4 KiB per frame
	const framesPer5MB = (5 * 1024 * 1024) / 4096 // 1280 frames = 5 MiB

	streamID, _ := openCoreTerminalForTest(t, c, phone, trusted, dom, s1, sec, seed, "s-bench", "", nowMs+100)
	frameBytes, _ := proto.Marshal(&stdv1.TerminalFrame{Kind: &stdv1.TerminalFrame_Output{Output: chunk}})
	rawFrames := make([][]byte, framesPer5MB)
	for i := 0; i < framesPer5MB; i++ {
		env := sealDaemonWireWithSeed(t, trusted, dom, s1, sec, seed, frameBytes, devicesec.TerminalOutput, trusted.id, "s-bench", streamID, uint64(i+1))
		rawFrames[i], _ = env.Marshal()
	}
	b.SetBytes(5 * 1024 * 1024)
	b.ResetTimer()
	for iter := 0; iter < b.N; iter++ {
		if iter > 0 {
			// Reset stream for next 5 MB pass cleanly without counting setup time.
			b.StopTimer()
			c.Dispatch(&corev1.Event{
				NowMs: nowMs + 500, MonoMs: 2500,
				Event: &corev1.Event_TerminalClose{TerminalClose: &corev1.TerminalCloseAction{StreamId: streamID}},
			})
			streamID, _ = openCoreTerminalForTest(t, c, phone, trusted, dom, s1, sec, seed, "s-bench", "", nowMs+600)
			for i := 0; i < framesPer5MB; i++ {
				env := sealDaemonWireWithSeed(t, trusted, dom, s1, sec, seed, frameBytes, devicesec.TerminalOutput, trusted.id, "s-bench", streamID, uint64(i+1))
				rawFrames[i], _ = env.Marshal()
			}
			b.StartTimer()
		}
		for i := 0; i < framesPer5MB; i++ {
			res := c.Dispatch(&corev1.Event{
				NowMs:  nowMs + 700,
				MonoMs: 2700,
				Event: &corev1.Event_TerminalInboundBytes{
					TerminalInboundBytes: &corev1.TerminalInboundBytesEvent{
						StreamId:    streamID,
						RawEnvelope: rawFrames[i],
					},
				},
			})
			gotDeliver := false
			for _, eff := range res.Effects {
				if eff.GetRender() != nil {
					b.Fatalf("frame %d emitted unexpected RenderEffect", i)
				}
				if eff.GetTerminalDeliverOutput() != nil {
					gotDeliver = true
				}
			}
			if !gotDeliver {
				b.Fatalf("frame %d missing TerminalDeliverOutputEffect", i)
			}
		}
	}
}

func TestTerminalPipe_BackpressureCapsInboundAndOutbound(t *testing.T) {
	// 1. Inbound 512-frame cap closes with "backlog" + localized KeyErrorsTerminalOutputBacklogReconnectForTheLatestDisplay.
	t.Run("inbound_512_frames", func(t *testing.T) {
		c, phone, trusted, _, _, dom, s1, sec, _, _, nowMs := setupOpaqueCacheTestCore(t, false)
		seed := bytes.Repeat([]byte{0x61}, 32)
		seedSessionInCore(t, c, dom, s1, sec, trusted, "s-bp1", seed, nil, nowMs+50)
		streamID, _ := openCoreTerminalForTest(t, c, phone, trusted, dom, s1, sec, seed, "s-bp1", "", nowMs+100)

		// Frame 1: incompressible >16 KiB payload holds openInFlight in async SecurityCryptoBatchEffect.
		big := make([]byte, 20*1024)
		_, _ = rand.Read(big)
		bigBytes, _ := proto.Marshal(&stdv1.TerminalFrame{Kind: &stdv1.TerminalFrame_Output{Output: big}})
		bigEnv := sealDaemonWireWithSeed(t, trusted, dom, s1, sec, seed, bigBytes, devicesec.TerminalOutput, trusted.id, "s-bp1", streamID, 1)
		bigRaw, _ := bigEnv.Marshal()
		b1 := c.Dispatch(&corev1.Event{
			NowMs: nowMs + 200, MonoMs: 2000,
			Event: &corev1.Event_TerminalInboundBytes{TerminalInboundBytes: &corev1.TerminalInboundBytesEvent{StreamId: streamID, RawEnvelope: bigRaw}},
		})
		var sawBatch bool
		for _, eff := range b1.Effects {
			if eff.GetSecurityCryptoBatch() != nil {
				sawBatch = true
			}
		}
		if !sawBatch {
			t.Fatal("expected async SecurityCryptoBatchEffect for >16 KiB frame")
		}

		// Frames 2..512 fill the 512-frame budget without closing.
		smallBytes, _ := proto.Marshal(&stdv1.TerminalFrame{Kind: &stdv1.TerminalFrame_Output{Output: []byte("x")}})
		smallEnv := sealDaemonWireWithSeed(t, trusted, dom, s1, sec, seed, smallBytes, devicesec.TerminalOutput, trusted.id, "s-bp1", streamID, 2)
		smallRaw, _ := smallEnv.Marshal()
		for i := 0; i < 511; i++ {
			b := c.Dispatch(&corev1.Event{
				NowMs: nowMs + 201, MonoMs: 2001,
				Event: &corev1.Event_TerminalInboundBytes{TerminalInboundBytes: &corev1.TerminalInboundBytesEvent{StreamId: streamID, RawEnvelope: smallRaw}},
			})
			for _, eff := range b.Effects {
				if eff.GetTerminalStateChanged() != nil {
					t.Fatalf("unexpected close at frame %d: %+v", i+2, eff)
				}
			}
		}

		// Frame 513 exceeds 512-frame cap -> closes with "backlog".
		bOver := c.Dispatch(&corev1.Event{
			NowMs: nowMs + 202, MonoMs: 2002,
			Event: &corev1.Event_TerminalInboundBytes{TerminalInboundBytes: &corev1.TerminalInboundBytesEvent{StreamId: streamID, RawEnvelope: smallRaw}},
		})
		var st *corev1.TerminalStateChangedEffect
		for _, eff := range bOver.Effects {
			if s := eff.GetTerminalStateChanged(); s != nil {
				st = s
			}
		}
		if st == nil || st.State != corev1.TerminalLifecycleState_TERMINAL_LIFECYCLE_STATE_CLOSED ||
			st.ReasonCode != "backlog" || st.GetReasonText().GetKey() != text.KeyErrorsTerminalOutputBacklogReconnectForTheLatestDisplay {
			t.Fatalf("expected backlog close on 513th frame, got %+v", st)
		}
	})

	// 2. Inbound 2 MiB byte cap (<512 frames) closes with "backlog".
	t.Run("inbound_2MiB_bytes", func(t *testing.T) {
		c, phone, trusted, _, _, dom, s1, sec, _, _, nowMs := setupOpaqueCacheTestCore(t, false)
		seed := bytes.Repeat([]byte{0x62}, 32)
		seedSessionInCore(t, c, dom, s1, sec, trusted, "s-bp2", seed, nil, nowMs+50)
		streamID, _ := openCoreTerminalForTest(t, c, phone, trusted, dom, s1, sec, seed, "s-bp2", "", nowMs+100)

		chunk := make([]byte, 32*1024)
		_, _ = rand.Read(chunk)
		chunkBytes, _ := proto.Marshal(&stdv1.TerminalFrame{Kind: &stdv1.TerminalFrame_Output{Output: chunk}})
		chunkEnv := sealDaemonWireWithSeed(t, trusted, dom, s1, sec, seed, chunkBytes, devicesec.TerminalOutput, trusted.id, "s-bp2", streamID, 1)
		chunkRaw, _ := chunkEnv.Marshal()
		var closed *corev1.TerminalStateChangedEffect
		for i := 0; i < 80 && closed == nil; i++ {
			b := c.Dispatch(&corev1.Event{
				NowMs: nowMs + 200, MonoMs: 2000,
				Event: &corev1.Event_TerminalInboundBytes{TerminalInboundBytes: &corev1.TerminalInboundBytesEvent{StreamId: streamID, RawEnvelope: chunkRaw}},
			})
			for _, eff := range b.Effects {
				if s := eff.GetTerminalStateChanged(); s != nil {
					closed = s
				}
			}
		}
		if closed == nil || closed.ReasonCode != "backlog" || closed.GetReasonText().GetKey() != text.KeyErrorsTerminalOutputBacklogReconnectForTheLatestDisplay {
			t.Fatalf("expected 2 MiB byte cap backlog close, got %+v", closed)
		}
	})

	// 3. Outbound 64-frame cap closes with "input_backlog" + KeyErrorsTerminalInputBacklogReconnect.
	t.Run("outbound_64_frames", func(t *testing.T) {
		c, phone, trusted, _, _, dom, s1, sec, _, _, nowMs := setupOpaqueCacheTestCore(t, false)
		seed := bytes.Repeat([]byte{0x63}, 32)
		seedSessionInCore(t, c, dom, s1, sec, trusted, "s-bp3", seed, nil, nowMs+50)
		streamID, _ := openCoreTerminalForTest(t, c, phone, trusted, dom, s1, sec, seed, "s-bp3", "", nowMs+100)

		// 1st input >16 KiB stalls sendInFlight in SecurityCryptoBatchEffect.
		bigIn := make([]byte, 20*1024)
		_, _ = rand.Read(bigIn)
		c.Dispatch(&corev1.Event{
			NowMs: nowMs + 200, MonoMs: 2000,
			Event: &corev1.Event_TerminalInput{TerminalInput: &corev1.TerminalInputAction{
				StreamId: streamID, Kind: &corev1.TerminalInputAction_Input{Input: bigIn},
			}},
		})
		// Next 63 inputs fill the 64-frame cap without closing.
		for i := 0; i < 63; i++ {
			b := c.Dispatch(&corev1.Event{
				NowMs: nowMs + 201, MonoMs: 2001,
				Event: &corev1.Event_TerminalInput{TerminalInput: &corev1.TerminalInputAction{
					StreamId: streamID, Kind: &corev1.TerminalInputAction_Input{Input: []byte("a")},
				}},
			})
			for _, eff := range b.Effects {
				if eff.GetTerminalStateChanged() != nil {
					t.Fatalf("unexpected close at outbound input %d: %+v", i+2, eff)
				}
			}
		}
		// 65th input trips the 64-frame cap -> "input_backlog".
		bOver := c.Dispatch(&corev1.Event{
			NowMs: nowMs + 202, MonoMs: 2002,
			Event: &corev1.Event_TerminalInput{TerminalInput: &corev1.TerminalInputAction{
				StreamId: streamID, Kind: &corev1.TerminalInputAction_Input{Input: []byte("a")},
			}},
		})
		var st *corev1.TerminalStateChangedEffect
		for _, eff := range bOver.Effects {
			if s := eff.GetTerminalStateChanged(); s != nil {
				st = s
			}
		}
		if st == nil || st.State != corev1.TerminalLifecycleState_TERMINAL_LIFECYCLE_STATE_CLOSED ||
			st.ReasonCode != "input_backlog" || st.GetReasonText().GetKey() != text.KeyErrorsTerminalInputBacklogReconnect {
			t.Fatalf("expected input_backlog close on 65th outbound frame, got %+v", st)
		}
	})
}

func TestTerminalPipe_PolicyViolation1008ChecksPerLegToken(t *testing.T) {
	c, phone, trusted, _, _, dom, s1, sec, _, _, nowMs := setupOpaqueCacheTestCore(t, true)
	seed := bytes.Repeat([]byte{0x64}, 32)
	seedSessionInCore(t, c, dom, s1, sec, trusted, "s-1008", seed, nil, nowMs+50)
	streamID, leg1 := openCoreTerminalForTest(t, c, phone, trusted, dom, s1, sec, seed, "s-1008", "", nowMs+100)

	// Rotate cached token via real 401 refresh flow on RefreshDevices -> "term-test-tok-newer".
	bRef := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 150, MonoMs: 1500,
		Event: &corev1.Event_RefreshDevices{RefreshDevices: &corev1.RefreshDevicesAction{}},
	})
	var listOp uint64
	for _, eff := range bRef.Effects {
		if r := eff.GetRelayRpc(); r != nil && r.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_DEVICES {
			listOp = r.OpId
		}
	}
	if listOp == 0 {
		t.Fatal("expected LIST_DEVICES RelayRpc")
	}
	b401 := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 160, MonoMs: 1510,
		Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{
			OpId: listOp, ConnectCode: security.ConnectCodeUnauthenticated, DiagnosticMessage: "expired",
		}},
	})
	for q := append([]*corev1.Effect(nil), b401.Effects...); len(q) > 0; {
		eff := q[0]
		q = q[1:]
		if r := eff.GetRelayRpc(); r != nil {
			switch r.Method {
			case corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_CHALLENGE:
				rb, _ := proto.Marshal(&stdv1.GetChallengeResponse{Challenge: bytes.Repeat([]byte{5}, 32)})
				next := c.Dispatch(&corev1.Event{
					NowMs: nowMs + 161, MonoMs: 1511,
					Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{OpId: r.OpId, ResponseBytes: rb}},
				})
				q = append(q, next.Effects...)
			case corev1.RelayRpcMethod_RELAY_RPC_METHOD_LOGIN:
				rb, _ := proto.Marshal(&stdv1.LoginResponse{Token: "term-test-tok-newer", ExpiresMs: nowMs + 7*24*3600*1000})
				next := c.Dispatch(&corev1.Event{
					NowMs: nowMs + 163, MonoMs: 1513,
					Event: &corev1.Event_RelayRpcResult{RelayRpcResult: &corev1.RelayRpcResultEvent{OpId: r.OpId, ResponseBytes: rb}},
				})
				q = append(q, next.Effects...)
			}
		} else if ks := eff.GetSecurityKeySign(); ks != nil {
			next := c.Dispatch(&corev1.Event{
				NowMs: nowMs + 162, MonoMs: 1512,
				Event: &corev1.Event_SecurityKeySigned{SecurityKeySigned: &corev1.SecurityKeySignedEvent{
					OpId: ks.OpId, Signature: ed25519.Sign(phone.ed, ks.Message),
				}},
			})
			q = append(q, next.Effects...)
		}
	}

	// Old leg1 (opened with "term-test-tok") reports 1008: MUST NOT clear "term-test-tok-newer"!
	bOld := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 200, MonoMs: 2000,
		Event: &corev1.Event_TerminalSocket{TerminalSocket: &corev1.TerminalSocketEvent{
			StreamId: streamID, LegId: leg1, Closed: true, PolicyViolation: true,
		}},
	})
	for _, eff := range bOld.Effects {
		if tu := eff.GetSecurityTokenUpdated(); tu != nil && tu.Cleared {
			t.Fatal("1008 on stale-token leg must NOT clear newer cached token")
		}
	}

	// Now open a fresh stream (which uses current "term-test-tok-newer") and deliver 1008 -> MUST clear!
	stream2, leg2 := openCoreTerminalForTest(t, c, phone, trusted, dom, s1, sec, seed, "s-1008", "", nowMs+300)
	bCur := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 350, MonoMs: 2350,
		Event: &corev1.Event_TerminalSocket{TerminalSocket: &corev1.TerminalSocketEvent{
			StreamId: stream2, LegId: leg2, Closed: true, PolicyViolation: true,
		}},
	})
	var cleared bool
	for _, eff := range bCur.Effects {
		if tu := eff.GetSecurityTokenUpdated(); tu != nil && tu.Cleared {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("1008 on current-token leg MUST clear cached token")
	}
}

func TestTerminalPipe_InputSealErrNeedEntropyRetriesAfterEntropyProvided(t *testing.T) {
	c, phone, trusted, _, _, dom, s1, sec, _, _, nowMs := setupOpaqueCacheTestCore(t, true)
	seed := bytes.Repeat([]byte{0x65}, 32)
	seedSessionInCore(t, c, dom, s1, sec, trusted, "s-ent", seed, nil, nowMs+50)
	streamID, _ := openCoreTerminalForTest(t, c, phone, trusted, dom, s1, sec, seed, "s-ent", "", nowMs+100)

	// Send inputs without refilling RandomBytes until Core's entropy pool drains and hits ErrNeedEntropy.
	var stalledSeq uint64
	var askedEntropy bool
	for seq := uint64(1); seq <= 64; seq++ {
		b := c.Dispatch(&corev1.Event{
			NowMs: nowMs + 200 + int64(seq), MonoMs: 2000 + int64(seq),
			Event: &corev1.Event_TerminalInput{TerminalInput: &corev1.TerminalInputAction{
				StreamId: streamID, Kind: &corev1.TerminalInputAction_Input{Input: []byte("entropy-step")},
			}},
		})
		var sent bool
		for q := append([]*corev1.Effect(nil), b.Effects...); len(q) > 0; {
			eff := q[0]
			q = q[1:]
			if eff.GetTerminalStateChanged() != nil {
				t.Fatalf("ErrNeedEntropy must NOT close terminal at seq %d, got %+v", seq, eff)
			}
			if eff.GetRequestEntropy() != nil {
				askedEntropy = true
			}
			if ks := eff.GetSecurityKeySign(); ks != nil {
				next := c.Dispatch(&corev1.Event{
					NowMs: nowMs + 200 + int64(seq), MonoMs: 2000 + int64(seq),
					Event: &corev1.Event_SecurityKeySigned{SecurityKeySigned: &corev1.SecurityKeySignedEvent{
						OpId: ks.OpId, Signature: ed25519.Sign(phone.ed, ks.Message),
					}},
				})
				q = append(q, next.Effects...)
			} else if eff.GetTerminalDirectSend() != nil {
				sent = true
			}
		}
		if !sent {
			stalledSeq = seq
			break
		}
	}
	if stalledSeq == 0 || !askedEntropy {
		t.Fatalf("expected input seal to stall on ErrNeedEntropy with RequestEntropyEffect, stalledSeq=%d asked=%v", stalledSeq, askedEntropy)
	}

	// Refill OS entropy -> Core retries the stalled input with its exact unwound seq (NOT stalledSeq+1)!
	rb := make([]byte, 256)
	_, _ = rand.Read(rb)
	b2 := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 300, MonoMs: 2300,
		Event: &corev1.Event_EntropyProvided{EntropyProvided: &corev1.EntropyProvided{RandomBytes: rb}},
	})
	var sent *corev1.TerminalDirectSendEffect
	for q := append([]*corev1.Effect(nil), b2.Effects...); len(q) > 0; {
		eff := q[0]
		q = q[1:]
		if ks := eff.GetSecurityKeySign(); ks != nil {
			next := c.Dispatch(&corev1.Event{
				NowMs: nowMs + 301, MonoMs: 2301,
				Event: &corev1.Event_SecurityKeySigned{SecurityKeySigned: &corev1.SecurityKeySignedEvent{
					OpId: ks.OpId, Signature: ed25519.Sign(phone.ed, ks.Message),
				}},
			})
			q = append(q, next.Effects...)
		} else if s := eff.GetTerminalDirectSend(); s != nil {
			sent = s
		}
	}
	if sent == nil {
		t.Fatal("expected TerminalDirectSendEffect after EntropyProvided")
	}
	hdr, plain := openDaemonEnvelopeWithSeed(t, sent.SealedEnvelope, sec, seed, phone.edP)
	if hdr.Seq != stalledSeq {
		t.Fatalf("expected unwound seq=%d after ErrNeedEntropy retry, got %d", stalledSeq, hdr.Seq)
	}
	var tf stdv1.TerminalFrame
	_ = proto.Unmarshal(plain, &tf)
	if string(tf.GetInput()) != "entropy-step" {
		t.Fatalf("retried plaintext = %q, want entropy-step", string(tf.GetInput()))
	}
}

func TestTerminalPipe_NegativeRelayTamperReplaySeqJumpMismatchAndMissingSeed(t *testing.T) {
	c, phone, trusted, other, _, dom, s1, sec, _, _, nowMs := setupOpaqueCacheTestCore(t, true)
	seed := bytes.Repeat([]byte{0x66}, 32)
	seedSessionInCore(t, c, dom, s1, sec, trusted, "s-neg2", seed, nil, nowMs+50)
	outBytes, _ := proto.Marshal(&stdv1.TerminalFrame{Kind: &stdv1.TerminalFrame_Output{Output: []byte("ok")}})

	assertRelayCloses := func(t *testing.T, label string, makeRaw func(t *testing.T, streamID string) []byte) {
		t.Helper()
		t.Run(label, func(t *testing.T) {
			streamID, _ := openCoreTerminalForTest(t, c, phone, trusted, dom, s1, sec, seed, "s-neg2", "", nowMs+100)
			b := c.Dispatch(&corev1.Event{
				NowMs: nowMs + 150, MonoMs: 1500,
				Event: &corev1.Event_TerminalInboundBytes{TerminalInboundBytes: &corev1.TerminalInboundBytesEvent{
					StreamId: streamID, RawEnvelope: makeRaw(t, streamID),
				}},
			})
			var closed bool
			for _, eff := range b.Effects {
				if st := eff.GetTerminalStateChanged(); st != nil && st.State == corev1.TerminalLifecycleState_TERMINAL_LIFECYCLE_STATE_CLOSED {
					closed = true
				}
			}
			if !closed {
				t.Fatalf("%s: expected relay frame to close terminal", label)
			}
		})
	}

	assertRelayCloses(t, "tampered_relay_frame", func(t *testing.T, sid string) []byte {
		env := sealDaemonWireWithSeed(t, trusted, dom, s1, sec, seed, outBytes, devicesec.TerminalOutput, trusted.id, "s-neg2", sid, 1)
		env.Ciphertext[0] ^= 0xaa
		raw, _ := env.Marshal()
		return raw
	})
	assertRelayCloses(t, "mismatched_request_id", func(t *testing.T, _ string) []byte {
		env := sealDaemonWireWithSeed(t, trusted, dom, s1, sec, seed, outBytes, devicesec.TerminalOutput, trusted.id, "s-neg2", "wrong-stream-id-0001", 1)
		raw, _ := env.Marshal()
		return raw
	})
	assertRelayCloses(t, "mismatched_session_id", func(t *testing.T, sid string) []byte {
		env := sealDaemonWireWithSeed(t, trusted, dom, s1, sec, seed, outBytes, devicesec.TerminalOutput, trusted.id, "other-session", sid, 1)
		raw, _ := env.Marshal()
		return raw
	})
	assertRelayCloses(t, "mismatched_machine_id", func(t *testing.T, sid string) []byte {
		env := sealDaemonWireWithSeed(t, trusted, dom, s1, sec, seed, outBytes, devicesec.TerminalOutput, other.id, "s-neg2", sid, 1)
		raw, _ := env.Marshal()
		return raw
	})
	assertRelayCloses(t, "non_member_signer", func(t *testing.T, sid string) []byte {
		env := sealDaemonWireWithSeed(t, other, dom, s1, sec, seed, outBytes, devicesec.TerminalOutput, trusted.id, "s-neg2", sid, 1)
		raw, _ := env.Marshal()
		return raw
	})

	// Exact identical replay (same seq + same plaintext) is dropped cleanly without closing or duplicate delivery;
	// subsequent seq jump (seq=1 -> seq=3) closes with conflict.
	t.Run("identical_replay_and_seq_jump", func(t *testing.T) {
		streamID, _ := openCoreTerminalForTest(t, c, phone, trusted, dom, s1, sec, seed, "s-neg2", "", nowMs+200)
		env1 := sealDaemonWireWithSeed(t, trusted, dom, s1, sec, seed, outBytes, devicesec.TerminalOutput, trusted.id, "s-neg2", streamID, 1)
		raw1, _ := env1.Marshal()
		bFirst := c.Dispatch(&corev1.Event{
			NowMs: nowMs + 210, MonoMs: 2100,
			Event: &corev1.Event_TerminalInboundBytes{TerminalInboundBytes: &corev1.TerminalInboundBytesEvent{StreamId: streamID, RawEnvelope: raw1}},
		})
		var firstFrames int
		for _, eff := range bFirst.Effects {
			if d := eff.GetTerminalDeliverOutput(); d != nil {
				firstFrames += len(d.Frames)
			}
		}
		if firstFrames != 1 {
			t.Fatalf("expected 1 delivered frame on seq=1, got %d", firstFrames)
		}

		bReplay := c.Dispatch(&corev1.Event{
			NowMs: nowMs + 211, MonoMs: 2101,
			Event: &corev1.Event_TerminalInboundBytes{TerminalInboundBytes: &corev1.TerminalInboundBytesEvent{StreamId: streamID, RawEnvelope: raw1}},
		})
		var acked uint32
		for _, eff := range bReplay.Effects {
			if eff.GetTerminalStateChanged() != nil {
				t.Fatalf("identical replay must NOT close terminal, got %+v", eff)
			}
			if d := eff.GetTerminalDeliverOutput(); d != nil {
				if len(d.Frames) != 0 {
					t.Fatalf("identical replay must NOT re-deliver frames, got %d", len(d.Frames))
				}
				acked += d.ConsumedFrames
			}
		}
		if acked != 1 {
			t.Fatalf("identical replay must ack ConsumedFrames=1 for backpressure release, got %d", acked)
		}

		// Seq jump (seq=3 skipping seq=2) must NOT deliver out-of-order and triggers replay/gap handling or closes on overflow.
		envJump := sealDaemonWireWithSeed(t, trusted, dom, s1, sec, seed, outBytes, devicesec.TerminalOutput, trusted.id, "s-neg2", streamID, 3)
		rawJump, _ := envJump.Marshal()
		bJump := c.Dispatch(&corev1.Event{
			NowMs: nowMs + 212, MonoMs: 2102,
			Event: &corev1.Event_TerminalInboundBytes{TerminalInboundBytes: &corev1.TerminalInboundBytesEvent{StreamId: streamID, RawEnvelope: rawJump}},
		})
		for _, eff := range bJump.Effects {
			if d := eff.GetTerminalDeliverOutput(); d != nil && len(d.Frames) > 0 {
				t.Fatal("seq jump (seq=3 before seq=2) must NOT deliver frame 3 out of order")
			}
		}
	})

	// Missing sessionSeed: TerminalOpen refuses to open (not_found), and if seeds are cleared mid-stream,
	// outbound TerminalInput fails and closes the terminal without emitting TerminalDirectSendEffect.
	t.Run("outbound_missing_seed", func(t *testing.T) {
		c2, phone2, trusted2, _, _, dom2, s12, sec2, _, _, now2 := setupOpaqueCacheTestCore(t, true)
		// 1. Session with valid Meta but NO SessionKey cannot open a terminal.
		metaBytes, _ := proto.Marshal(&stdv1.Session{Id: "s-noseed"})
		metaEnv := sealDaemonWireWithSeed(t, trusted2, dom2, s12, sec2, nil, metaBytes, devicesec.SessionMeta, trusted2.id, "s-noseed", "", 1)
		c2.Dispatch(&corev1.Event{
			NowMs: now2 + 40, MonoMs: 1040,
			Event: &corev1.Event_CacheBootstrapLoaded{CacheBootstrapLoaded: &corev1.CacheBootstrapLoadedEvent{
				OpId: 1,
				RawSessions: testrelay.MustRawSlice([]*v1.RemoteSession{{
					SessionId: "s-noseed", MachineId: trusted2.id, Meta: metaEnv,
				}}),
			}},
		})
		bOpen := c2.Dispatch(&corev1.Event{
			NowMs: now2 + 50, MonoMs: 1050,
			Event: &corev1.Event_TerminalOpen{TerminalOpen: &corev1.TerminalOpenAction{
				OpId: 505, SessionId: "s-noseed", MachineId: trusted2.id,
			}},
		})
		var openFailed bool
		for _, eff := range bOpen.Effects {
			if eff.GetTerminalOpenSocket() != nil {
				t.Fatal("TerminalOpen must NOT open socket when sessionSeed is missing")
			}
			if st := eff.GetTerminalStateChanged(); st != nil && st.OpenSettled && !st.OpenOk && st.ReasonCode == "not_found" {
				openFailed = true
			}
		}
		if !openFailed {
			t.Fatalf("expected TerminalOpen not_found failure when sessionSeed missing, got %+v", bOpen.Effects)
		}

		// 2. If security state clears mid-stream (dropping sessionSeeds), outbound TerminalInput closes cleanly without sending.
		seed2 := bytes.Repeat([]byte{0x67}, 32)
		seedSessionInCore(t, c2, dom2, s12, sec2, trusted2, "s-dropseed", seed2, nil, now2+60)
		streamID, _ := openCoreTerminalForTest(t, c2, phone2, trusted2, dom2, s12, sec2, seed2, "s-dropseed", "", now2+100)
		bClr := c2.Dispatch(&corev1.Event{
			NowMs: now2 + 120, MonoMs: 1220,
			Event: &corev1.Event_SecurityAction{SecurityAction: &corev1.SecurityWorkflowActionEvent{
				ActionId: 99,
				Action:   &corev1.SecurityWorkflowActionEvent_ClearSecurity{ClearSecurity: &corev1.SecurityActionEmpty{}},
			}},
		})
		for _, eff := range bClr.Effects {
			if st := eff.GetSecurityStorage(); st != nil {
				c2.Dispatch(&corev1.Event{
					NowMs: now2 + 121, MonoMs: 1221,
					Event: &corev1.Event_SecurityStorageResult{SecurityStorageResult: &corev1.SecurityStorageResultEvent{OpId: st.OpId}},
				})
			}
		}
		b := c2.Dispatch(&corev1.Event{
			NowMs: now2 + 150, MonoMs: 1500,
			Event: &corev1.Event_TerminalInput{TerminalInput: &corev1.TerminalInputAction{
				StreamId: streamID, Kind: &corev1.TerminalInputAction_Input{Input: []byte("ls")},
			}},
		})
		var closed bool
		for _, eff := range b.Effects {
			if eff.GetTerminalDirectSend() != nil {
				t.Fatal("outbound input with missing sessionSeed must NOT emit TerminalDirectSendEffect")
			}
			if st := eff.GetTerminalStateChanged(); st != nil && st.State == corev1.TerminalLifecycleState_TERMINAL_LIFECYCLE_STATE_CLOSED {
				closed = true
			}
		}
		if !closed {
			t.Fatal("outbound input with missing sessionSeed must fail and close terminal")
		}
	})
}

func TestTerminalPipe_CrossCompatWithLegacyHandleSecurityOp(t *testing.T) {
	c, phone, trusted, _, _, dom, s1, sec, _, _, nowMs := setupOpaqueCacheTestCore(t, true)
	seed := bytes.Repeat([]byte{0x68}, 32)
	seedSessionInCore(t, c, dom, s1, sec, trusted, "s-compat", seed, nil, nowMs+50)
	streamID, _ := openCoreTerminalForTest(t, c, phone, trusted, dom, s1, sec, seed, "s-compat", "", nowMs+100)

	// Direction 1: New pipe TerminalInputAction -> sealed envelope -> opened via legacy SecurityOpRequest(OpenEnvelope)!
	bIn := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 200, MonoMs: 2000,
		Event: &corev1.Event_TerminalInput{TerminalInput: &corev1.TerminalInputAction{
			StreamId: streamID, Kind: &corev1.TerminalInputAction_Input{Input: []byte("compat-in")},
		}},
	})
	var sealedIn []byte
	for q := append([]*corev1.Effect(nil), bIn.Effects...); len(q) > 0; {
		eff := q[0]
		q = q[1:]
		if ks := eff.GetSecurityKeySign(); ks != nil {
			next := c.Dispatch(&corev1.Event{
				NowMs: nowMs + 201, MonoMs: 2001,
				Event: &corev1.Event_SecurityKeySigned{SecurityKeySigned: &corev1.SecurityKeySignedEvent{
					OpId: ks.OpId, Signature: ed25519.Sign(phone.ed, ks.Message),
				}},
			})
			q = append(q, next.Effects...)
		} else if s := eff.GetTerminalDirectSend(); s != nil {
			sealedIn = s.SealedEnvelope
		}
	}
	if len(sealedIn) == 0 {
		t.Fatal("expected sealed envelope from new TerminalInputAction")
	}
	var envIn v1.Envelope
	if err := envIn.Unmarshal(sealedIn); err != nil {
		t.Fatalf("unmarshal sealedIn: %v", err)
	}
	bOpen := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 205, MonoMs: 2005,
		Event: &corev1.Event_SecurityOpRequest{SecurityOpRequest: &corev1.SecurityOpRequestEvent{
			OpId: 9901,
			Op: &corev1.SecurityOpRequestEvent_OpenEnvelope{OpenEnvelope: &corev1.SecurityOpenEnvelopeOp{
				RawEnvelope:         envIn.AppendMarshal(nil),
				ExpectedPurpose:     uint32(devicesec.TerminalInput),
				MachineId:           trusted.id,
				SessionId:           "s-compat",
				RequestId:           streamID,
				Seq:                 1,
				OverrideSessionSeed: seed,
			}},
		}},
	})
	var opRes *corev1.SecurityOpResultEffect
	for _, eff := range bOpen.Effects {
		if r := eff.GetSecurityOpResult(); r != nil && r.OpId == 9901 {
			opRes = r
		}
	}
	if opRes == nil || opRes.Error != "" || opRes.GetOpened() == nil {
		t.Fatalf("legacy OpenEnvelope failed on new pipe's sealed input: %+v", opRes)
	}
	var tfIn stdv1.TerminalFrame
	_ = proto.Unmarshal(opRes.GetOpened().GetPlaintext(), &tfIn)
	if string(tfIn.GetInput()) != "compat-in" {
		t.Fatalf("legacy OpenEnvelope plaintext = %q, want compat-in", string(tfIn.GetInput()))
	}

	// Direction 2: Legacy SecurityOpRequest(SealEnvelope) for client-origin TerminalInput verifies identically via daemon-side open,
	// and a machine-origin TerminalOutput envelope opens identically through BOTH legacy HandleSecurityOp(OpenEnvelope) and new TerminalInboundBytesEvent!
	legacyInPlain, _ := proto.Marshal(&stdv1.TerminalFrame{Kind: &stdv1.TerminalFrame_Input{Input: []byte("compat-legacy-in")}})
	bSeal := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 210, MonoMs: 2010,
		Event: &corev1.Event_SecurityOpRequest{SecurityOpRequest: &corev1.SecurityOpRequestEvent{
			OpId: 9902,
			Op: &corev1.SecurityOpRequestEvent_SealEnvelope{SealEnvelope: &corev1.SecuritySealEnvelopeOp{
				Purpose:             uint32(devicesec.TerminalInput),
				MachineId:           trusted.id,
				SessionId:           "s-compat",
				RequestId:           streamID,
				Seq:                 2,
				DeadlineMs:          nowMs + 60_000,
				Plaintext:           legacyInPlain,
				UseSessionSeed:      true,
				OverrideSessionSeed: seed,
			}},
		}},
	})
	var legacyEnvBytes []byte
	for q := append([]*corev1.Effect(nil), bSeal.Effects...); len(q) > 0; {
		eff := q[0]
		q = q[1:]
		if ks := eff.GetSecurityKeySign(); ks != nil {
			next := c.Dispatch(&corev1.Event{
				NowMs: nowMs + 211, MonoMs: 2011,
				Event: &corev1.Event_SecurityKeySigned{SecurityKeySigned: &corev1.SecurityKeySignedEvent{
					OpId: ks.OpId, Signature: ed25519.Sign(phone.ed, ks.Message),
				}},
			})
			q = append(q, next.Effects...)
		} else if r := eff.GetSecurityOpResult(); r != nil && r.OpId == 9902 {
			if r.Error != "" {
				t.Fatalf("legacy SealEnvelope failed: %s", r.Error)
			}
			legacyEnvBytes = r.GetSealed().GetRawEnvelope()
		}
	}
	if len(legacyEnvBytes) == 0 {
		t.Fatal("missing legacy SealEnvelope result")
	}
	hdrLeg, plainLeg := openDaemonEnvelopeWithSeed(t, legacyEnvBytes, sec, seed, phone.edP)
	var tfLeg stdv1.TerminalFrame
	_ = proto.Unmarshal(plainLeg, &tfLeg)
	if hdrLeg.Purpose != devicesec.TerminalInput || hdrLeg.Seq != 2 || string(tfLeg.GetInput()) != "compat-legacy-in" {
		t.Fatalf("legacy-sealed input mismatch: hdr=%+v input=%q", hdrLeg, string(tfLeg.GetInput()))
	}

	outPlain, _ := proto.Marshal(&stdv1.TerminalFrame{Kind: &stdv1.TerminalFrame_Output{Output: []byte("compat-out")}})
	outEnv := sealDaemonWireWithSeed(t, trusted, dom, s1, sec, seed, outPlain, devicesec.TerminalOutput, trusted.id, "s-compat", streamID, 1)
	rawOut, _ := outEnv.Marshal()
	bLegacyOpenOut := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 215, MonoMs: 2015,
		Event: &corev1.Event_SecurityOpRequest{SecurityOpRequest: &corev1.SecurityOpRequestEvent{
			OpId: 9903,
			Op: &corev1.SecurityOpRequestEvent_OpenEnvelope{OpenEnvelope: &corev1.SecurityOpenEnvelopeOp{
				RawEnvelope:         rawOut,
				ExpectedPurpose:     uint32(devicesec.TerminalOutput),
				MachineId:           trusted.id,
				SessionId:           "s-compat",
				RequestId:           streamID,
				Seq:                 1,
				OverrideSessionSeed: seed,
			}},
		}},
	})
	var legOutPlain []byte
	for _, eff := range bLegacyOpenOut.Effects {
		if r := eff.GetSecurityOpResult(); r != nil && r.OpId == 9903 {
			if r.Error != "" {
				t.Fatalf("legacy OpenEnvelope(TerminalOutput) failed: %s", r.Error)
			}
			legOutPlain = r.GetOpened().GetPlaintext()
		}
	}
	bDel := c.Dispatch(&corev1.Event{
		NowMs: nowMs + 220, MonoMs: 2020,
		Event: &corev1.Event_TerminalInboundBytes{TerminalInboundBytes: &corev1.TerminalInboundBytesEvent{
			StreamId: streamID, RawEnvelope: rawOut,
		}},
	})
	var delivered *corev1.TerminalDeliverOutputEffect
	for _, eff := range bDel.Effects {
		if d := eff.GetTerminalDeliverOutput(); d != nil && len(d.Frames) > 0 {
			delivered = d
		}
	}
	var tfLegOut stdv1.TerminalFrame
	_ = proto.Unmarshal(legOutPlain, &tfLegOut)
	if delivered == nil || !bytes.Equal(delivered.Frames[0].GetOutput(), tfLegOut.GetOutput()) || string(delivered.Frames[0].GetOutput()) != "compat-out" {
		t.Fatalf("cross-compat output mismatch: pipe=%+v legacy=%q", delivered, string(tfLegOut.GetOutput()))
	}
}

func TestDecorateTerminalStateEffectsReasonTexts(t *testing.T) {
	cases := []struct {
		name       string
		reasonCode string
		literal    string
		wantKey    string
		wantArg    string
	}{
		{name: "conflict", reasonCode: "conflict", wantKey: text.KeyErrorsTerminalFramesAreOutOfOrder},
		{name: "replay_overflow", reasonCode: "replay_overflow", wantKey: text.KeyTerminalReplayOverflow},
		{name: "input_backlog", reasonCode: "input_backlog", wantKey: text.KeyErrorsTerminalInputBacklogReconnect},
		{name: "backlog", reasonCode: "backlog", wantKey: text.KeyErrorsTerminalOutputBacklogReconnectForTheLatestDisplay},
		{name: "socket_error", reasonCode: "socket_error", literal: "eof", wantKey: text.KeyErrorsTerminalConnectionDroppedReason, wantArg: "eof"},
		{name: "unknown_default", reasonCode: "mystery", wantKey: text.KeyErrorsTerminalConnectionDropped},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := &corev1.TerminalStateChangedEffect{
				State:      corev1.TerminalLifecycleState_TERMINAL_LIFECYCLE_STATE_CLOSED,
				ReasonCode: tc.reasonCode,
			}
			if tc.literal != "" {
				st.ReasonText = &corev1.Text{Literal: tc.literal}
			}
			effs := []*corev1.Effect{{Effect: &corev1.Effect_TerminalStateChanged{TerminalStateChanged: st}}}
			decorateTerminalStateEffects(effs)
			if st.ReasonText == nil || st.ReasonText.Key != tc.wantKey {
				t.Fatalf("ReasonCode=%q -> ReasonText=%+v, want key %q", tc.reasonCode, st.ReasonText, tc.wantKey)
			}
			if tc.wantArg != "" {
				if len(st.ReasonText.Args) != 1 || st.ReasonText.Args[0].GetStrValue() != tc.wantArg {
					t.Fatalf("ReasonCode=%q -> args=%+v, want %q", tc.reasonCode, st.ReasonText.Args, tc.wantArg)
				}
			}
		})
	}
}
