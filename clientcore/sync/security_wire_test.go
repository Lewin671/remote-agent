package sync

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/lists"
	"github.com/Lewin671/remote-agent/clientcore/domain/outcome"
	"github.com/Lewin671/remote-agent/clientcore/gen/litewire"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/security"
	"github.com/Lewin671/remote-agent/clientcore/text"
	"github.com/Lewin671/remote-agent/clientcore/transport"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"github.com/Lewin671/remote-agent/shared/linkcore"
	"github.com/Lewin671/remote-agent/shared/peerwire"
	stdv1 "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"google.golang.org/protobuf/proto"
)

type syncTestActor struct {
	id   string
	ed   ed25519.PrivateKey
	edP  ed25519.PublicKey
	x    *ecdh.PrivateKey
	xPub [32]byte
}

func newSyncTestActor(t *testing.T, tag byte) syncTestActor {
	t.Helper()
	edPriv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{tag}, ed25519.SeedSize))
	edPub := edPriv.Public().(ed25519.PublicKey)
	xPriv, err := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{tag + 0x50}, 32))
	if err != nil {
		t.Fatal(err)
	}
	var xp [32]byte
	copy(xp[:], xPriv.PublicKey().Bytes())
	return syncTestActor{
		id:   devicesec.ID(edPub),
		ed:   edPriv,
		edP:  edPub,
		x:    xPriv,
		xPub: xp,
	}
}

func (a syncTestActor) member(k devicesec.Kind) devicesec.Member {
	return devicesec.Member{ID: a.id, EdPublic: [32]byte(a.edP), XPublic: a.xPub, Kind: k}
}

type syncTestHPKE struct{}

func (syncTestHPKE) Seal(recipient, info, aad, plaintext []byte) ([]byte, []byte, error) {
	eph, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	rec, err := ecdh.X25519().NewPublicKey(recipient)
	if err != nil {
		return nil, nil, err
	}
	dh, err := eph.ECDH(rec)
	if err != nil {
		return nil, nil, err
	}
	ct, err := devicesec.HPKESealWithDH(recipient, eph.PublicKey().Bytes(), dh, info, aad, plaintext)
	if err != nil {
		return nil, nil, err
	}
	return eph.PublicKey().Bytes(), ct, nil
}

func sealWireObj(
	t *testing.T,
	signer syncTestActor,
	dom devicesec.Hash,
	st devicesec.State,
	sec, seed, plain []byte,
	purpose devicesec.Purpose,
	machineID, sessionID, reqID string,
	seq uint64,
) *v1.Envelope {
	t.Helper()
	sh, _ := st.Hash()
	hdr := devicesec.Header{
		Account:      st.Core.Account,
		Domain:       dom,
		Epoch:        st.Core.Epoch,
		State:        sh,
		ContentEpoch: st.Core.ContentEpoch,
		Sender:       signer.id,
		Kind:         purpose.Kind(),
		Purpose:      purpose,
		Machine:      machineID,
		Session:      sessionID,
		Request:      reqID,
		Seq:          seq,
	}
	hb, key, nonce, err := devicesec.PrepareCiphertext(sec, seed, hdr, len(plain), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(blk)
	ct, om, err := devicesec.AssembleCiphertext(hb, nonce, aead.Seal(nil, nonce, plain, hb))
	if err != nil {
		t.Fatal(err)
	}
	ct.Origin = ed25519.Sign(signer.ed, om)
	env, err := security.ToProtoEnvelope(ct)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func applyTestSecurityRecord(e *Engine, nowMs int64, rec security.RecordUpdate) StepResult {
	effs := e.secEngine.ApplyRecord(nowMs, rec)
	res := e.Run(func(ctx *StepContext) {
		e.syncSecurityStateFromEngine(ctx, true)
	})
	return StepResult{Effects: append(effs, res.Effects...)}
}

func bootstrapTestSecurityRecord(t *testing.T, e *Engine, actor syncTestActor, nowMs int64, rec *security.PersistedRecord, token string) {
	t.Helper()
	raw, err := rec.Encode()
	if err != nil {
		t.Fatal(err)
	}
	e.HandleEvent(&corev1.Event{
		NowMs: nowMs,
		Event: &corev1.Event_SecurityConfig{
			SecurityConfig: &corev1.SecurityConfigEvent{
				Account:      rec.Pin.Account,
				DeviceId:     rec.MemberID,
				Server:       rec.Server,
				InlineCrypto: true,
				Foreground:   true,
			},
		},
	})
	_, bootRes := e.HandleEvent(&corev1.Event{
		NowMs: nowMs,
		Event: &corev1.Event_SecurityAction{
			SecurityAction: &corev1.SecurityWorkflowActionEvent{
				ActionId: 1,
				Action:   &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
			},
		},
	})
	var storOp uint64
	for _, eff := range bootRes.Effects {
		if st := eff.GetSecurityStorage(); st != nil {
			storOp = st.OpId
		}
	}
	if storOp == 0 {
		t.Fatalf("expected bootstrap storage effect")
	}
	_, storRes := e.HandleEvent(&corev1.Event{
		NowMs: nowMs,
		Event: &corev1.Event_SecurityStorageResult{
			SecurityStorageResult: &corev1.SecurityStorageResultEvent{
				OpId:        storOp,
				HasDeviceEd: true,
				HasDeviceX:  true,
				Entries: []*corev1.SecurityStorageEntry{{
					Bucket:   corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD,
					Key:      "record",
					Revision: "rev-1",
					Data:     raw,
				}},
			},
		},
	})
	pumpSecurityEffects(t, e, actor, nowMs, storRes)
	if token != "" {
		e.cachedToken = token
		e.secEngine.SetAuthToken(token, nowMs+7*86_400_000)
	}
}

func pumpSecurityEffects(t *testing.T, e *Engine, actor syncTestActor, nowMs int64, initRes StepResult) ([]*corev1.Effect, []SettledCommand) {
	t.Helper()
	var allEffects []*corev1.Effect
	var allSettled []SettledCommand
	collect := func(res StepResult) {
		allEffects = append(allEffects, res.Effects...)
		allSettled = append(allSettled, res.Settled...)
	}
	collect(initRes)
	for i := 0; i < len(allEffects); i++ {
		eff := allEffects[i]
		switch k := eff.Effect.(type) {
		case *corev1.Effect_SecurityKeySign:
			sig := ed25519.Sign(actor.ed, k.SecurityKeySign.Message)
			_, r := e.HandleEvent(&corev1.Event{
				NowMs: nowMs,
				Event: &corev1.Event_SecurityKeySigned{
					SecurityKeySigned: &corev1.SecurityKeySignedEvent{OpId: k.SecurityKeySign.OpId, Signature: sig},
				},
			})
			collect(r)
		case *corev1.Effect_SecurityKeyAgree:
			peer, err := ecdh.X25519().NewPublicKey(k.SecurityKeyAgree.PeerPublic)
			if err != nil {
				t.Fatal(err)
			}
			shared, err := actor.x.ECDH(peer)
			if err != nil {
				t.Fatal(err)
			}
			_, r := e.HandleEvent(&corev1.Event{
				NowMs: nowMs,
				Event: &corev1.Event_SecurityKeyAgreed{
					SecurityKeyAgreed: &corev1.SecurityKeyAgreedEvent{OpId: k.SecurityKeyAgree.OpId, Shared: shared},
				},
			})
			collect(r)
		case *corev1.Effect_SecurityCryptoBatch:
			results := make([]*corev1.SecurityCryptoBatchResult, len(k.SecurityCryptoBatch.Items))
			for j, it := range k.SecurityCryptoBatch.Items {
				switch v := it.Kind.(type) {
				case *corev1.SecurityCryptoBatchItem_Verify:
					ok := len(v.Verify.PublicKey) == ed25519.PublicKeySize && ed25519.Verify(v.Verify.PublicKey, v.Verify.Message, v.Verify.Signature)
					results[j] = &corev1.SecurityCryptoBatchResult{Ok: ok}
				case *corev1.SecurityCryptoBatchItem_AeadOpen:
					blk, _ := aes.NewCipher(v.AeadOpen.Key)
					aead, _ := cipher.NewGCM(blk)
					pt, err := aead.Open(nil, v.AeadOpen.Nonce, v.AeadOpen.Ciphertext, v.AeadOpen.Aad)
					if err != nil {
						results[j] = &corev1.SecurityCryptoBatchResult{Ok: false, Error: err.Error()}
					} else {
						results[j] = &corev1.SecurityCryptoBatchResult{Ok: true, Data: pt}
					}
				case *corev1.SecurityCryptoBatchItem_AeadSeal:
					blk, _ := aes.NewCipher(v.AeadSeal.Key)
					aead, _ := cipher.NewGCM(blk)
					ct := aead.Seal(nil, v.AeadSeal.Nonce, v.AeadSeal.Plaintext, v.AeadSeal.Aad)
					results[j] = &corev1.SecurityCryptoBatchResult{Ok: true, Data: ct}
				}
			}
			_, r := e.HandleEvent(&corev1.Event{
				NowMs: nowMs,
				Event: &corev1.Event_SecurityCryptoBatchDone{
					SecurityCryptoBatchDone: &corev1.SecurityCryptoBatchDoneEvent{OpId: k.SecurityCryptoBatch.OpId, Results: results},
				},
			})
			collect(r)
		}
	}
	return allEffects, allSettled
}

func TestSyncSeedDemoLockOutAndWireEndToEndFlow(t *testing.T) {
	nowMs := int64(1_700_000_000_000)

	// Part 1: SeedDemoWorld works before real security initialization.
	demoEng := NewEngine()
	demoEng.HandleEvent(&corev1.Event{
		NowMs: nowMs,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Machines: []*corev1.DemoMachine{{MachineId: "demo-m", Online: true}},
			},
		},
	})
	if demoEng.MachineEntry("demo-m") == nil {
		t.Fatalf("expected SeedDemoWorld to populate demo machine before security init")
	}

	// Part 2: Real security setup locks out SeedDemoWorld forever and executes wire lists/events/commands/prefs.
	phone := newSyncTestActor(t, 1)
	machine := newSyncTestActor(t, 2)
	rec := newSyncTestActor(t, 3)
	dom := devicesec.Digest("sync-domain", []byte{1})
	sec := bytes.Repeat([]byte{0x7a}, 32)
	u0, err := devicesec.NewGenesis(phone.id, dom, phone.member(devicesec.Client), [32]byte(rec.edP), sec, uint64(nowMs), syncTestHPKE{})
	if err != nil {
		t.Fatal(err)
	}
	sb0, _ := u0.SigningBytes()
	u0.Signature = ed25519.Sign(phone.ed, sb0)
	s0Raw, _ := u0.MarshalBinary()
	s0, _ := devicesec.ParseState(s0Raw)
	gen, _ := s0.Hash()

	u1, err := devicesec.NewMembershipState(s0, []devicesec.Member{phone.member(devicesec.Client), machine.member(devicesec.Machine)}, nil, sec, sec, uint64(nowMs+10), syncTestHPKE{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sb1, _ := u1.SigningBytes()
	u1.Signature = ed25519.Sign(phone.ed, sb1)
	s1Raw, _ := u1.MarshalBinary()
	s1, _ := devicesec.ParseState(s1Raw)
	lease, _ := devicesec.NewLease(s1, uint64(nowMs+10))
	lb, _ := lease.SigningBytes()
	lease.Signature = ed25519.Sign(phone.ed, lb)
	leaseRaw, _ := lease.MarshalBinary()

	e := NewEngine()
	detEntropy := make([]byte, 4096)
	for off := 0; off < len(detEntropy); off += sha256.Size {
		var ctr [8]byte
		binary.LittleEndian.PutUint64(ctr[:], uint64(off/sha256.Size+1))
		sum := sha256.Sum256(ctr[:])
		copy(detEntropy[off:], sum[:])
	}
	e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 50,
		Event: &corev1.Event_EntropyProvided{
			EntropyProvided: &corev1.EntropyProvided{RandomBytes: detEntropy},
		},
	})
	e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 50,
		Event: &corev1.Event_SecurityConfig{
			SecurityConfig: &corev1.SecurityConfigEvent{
				Account:      phone.id,
				DeviceId:     phone.id,
				InlineCrypto: false,
			},
		},
	})
	// Demo seeding MUST be permanently ignored once security is configured.
	e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 50,
		Event: &corev1.Event_SeedDemoWorld{
			SeedDemoWorld: &corev1.SeedDemoWorldEvent{
				Machines: []*corev1.DemoMachine{{MachineId: "blocked-demo-m", Online: true}},
			},
		},
	})
	if e.MachineEntry("blocked-demo-m") != nil {
		t.Fatalf("expected SeedDemoWorld to be ignored after SecurityConfig")
	}

	recRes := applyTestSecurityRecord(e, nowMs+50, security.RecordUpdate{
		NetworkGeneration: 1,
		Account:           phone.id,
		Domain:            dom[:],
		Genesis:           gen[:],
		States:            [][]byte{s0Raw, s1Raw},
		Lease:             leaseRaw,
	})
	pumpSecurityEffects(t, e, phone, nowMs+50, recRes)
	if !e.security.Loaded || !e.security.IsMember || !e.security.TrustedMachines[machine.id] {
		t.Fatalf("expected security state synced, got %+v", e.security)
	}

	// Bring sync online via standard lifecycle/token/stream handshake.
	e.HandleEvent(&corev1.Event{NowMs: nowMs + 60, Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START}}})
	_, tokRes := e.HandleEvent(&corev1.Event{NowMs: nowMs + 60, Event: &corev1.Event_TokenResult{TokenResult: &corev1.TokenResultEvent{OpId: e.activeTokenOpID, Token: "tok"}}})
	var legID uint64
	for _, ef := range tokRes.Effects {
		if os := ef.GetOpenStream(); os != nil {
			legID = os.LegId
		}
	}
	e.HandleEvent(&corev1.Event{NowMs: nowMs + 70, Event: &corev1.Event_StreamReady{StreamReady: &corev1.StreamReadyEvent{LegId: legID}}})

	// Feed Wire ListsFetched with encrypted MachineMeta, SessionKey, SessionMeta (+ Checkpoint), DeviceMeta.
	mmRaw, _ := (&v1.MachineMeta{Hostname: "host-a", ProtocolVersion: PeerVersion}).Marshal()
	sessSeed := bytes.Repeat([]byte{0x5a}, 32)
	cpRaw, _ := (&v1.LiveSnapshot{Session: &v1.Session{Id: "s1", LastSeq: 10}, Running: true}).Marshal()
	smRaw, _ := (&v1.Session{Id: "s1", Title: "wire session", LastSeq: 10, CurrentCheckpoint: cpRaw, Agent: v1.Agent_AGENT_CODEX}).Marshal()
	dmRaw, _ := (&v1.DeviceMeta{Name: "my phone"}).Marshal()

	_, listsRes := e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 80,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId: e.refreshOpID,
				WireMachines: []*v1.Machine{{
					Id:     machine.id,
					Online: true,
					Meta:   sealWireObj(t, machine, dom, s1, sec, nil, mmRaw, devicesec.MachineMeta, machine.id, "", "", 1),
				}},
				WireSessions: []*v1.RemoteSession{{
					MachineId: machine.id,
					SessionId: "s1",
					LastSeq:   10,
					Key:       sealWireObj(t, machine, dom, s1, sec, nil, sessSeed, devicesec.SessionKey, machine.id, "s1", "", 1),
					Meta:      sealWireObj(t, machine, dom, s1, sec, nil, smRaw, devicesec.SessionMeta, machine.id, "s1", "", 1),
				}},
				WireDevices: []*v1.Device{{
					Id:   phone.id,
					Kind: v1.DeviceKind_DEVICE_KIND_CLIENT,
					Meta: sealWireObj(t, phone, dom, s1, sec, nil, dmRaw, devicesec.DeviceMeta, "", "", phone.id, 1),
				}},
			},
		},
	})
	pumpSecurityEffects(t, e, phone, nowMs+80, listsRes)
	if m := e.MachineEntry(machine.id); m == nil || m.Meta.GetHostname() != "host-a" {
		t.Fatalf("expected decrypted machine host-a, got %+v", m)
	}
	if s := e.SessionEntry("s1"); s == nil || s.Info.GetTitle() != "wire session" || s.Checkpoint == nil {
		t.Fatalf("expected decrypted session + checkpoint, got %+v", s)
	}

	// Watch session and feed 9 WireEvents in EventsPageFetched -> must trigger ONE CryptoBatch Verify (>8) and WriteStoredEvents.
	e.HandleEvent(&corev1.Event{NowMs: nowMs + 90, Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: "s1", Watched: true}}})
	b := e.Buffer("s1")
	e.HandleEvent(&corev1.Event{NowMs: nowMs + 90, Event: &corev1.Event_CacheEventsLoaded{CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{OpId: b.activeCacheOpID, SessionId: "s1"}}})
	stored := make([]*v1.StoredEvent, 10)
	for i := range stored {
		seq := uint64(i + 1)
		evRaw, _ := (&v1.Event{Seq: seq, SessionId: "s1", Kind: &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "msg"}}}).Marshal()
		stored[i] = &v1.StoredEvent{
			Seq:      seq,
			Envelope: sealWireObj(t, machine, dom, s1, sec, sessSeed, evRaw, devicesec.Event, machine.id, "s1", "", seq),
		}
	}
	_, pageRes := e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 100,
		Event: &corev1.Event_EventsPageFetched{
			EventsPageFetched: &corev1.EventsPageFetchedEvent{
				OpId:       b.activeFetchOpID,
				SessionId:  "s1",
				WireEvents: stored,
			},
		},
	})
	pageEffects, _ := pumpSecurityEffects(t, e, phone, nowMs+100, pageRes)
	var sawBatchVerify, sawWriteStored bool
	for _, ef := range pageEffects {
		if cb := ef.GetSecurityCryptoBatch(); cb != nil && len(cb.Items) == 10 {
			sawBatchVerify = true
		}
		if cw := ef.GetCacheWrite(); cw != nil && cw.GetWriteStoredEvents() != nil && len(cw.GetWriteStoredEvents().Events) == 10 {
			sawWriteStored = true
		}
	}
	if !sawBatchVerify || !sawWriteStored || len(e.Buffer("s1").ShownEvents()) != 10 {
		t.Fatalf("expected batchVerify=%v writeStored=%v shown=%d", sawBatchVerify, sawWriteStored, len(e.Buffer("s1").ShownEvents()))
	}

	// Outbound command sealing + SealedResult validation.
	var reqID string
	cmdStep := e.Run(func(ctx *StepContext) {
		reqID, _ = e.SubmitCommand(ctx, SubmitCommandParams{
			MachineID: machine.id,
			SessionID: "s1",
			Command:   &v1.Command{Kind: &v1.Command_Interrupt{Interrupt: &v1.InterruptRequest{SessionId: "s1"}}},
		})
	})
	cmdEffs, _ := pumpSecurityEffects(t, e, phone, nowMs+110, cmdStep)
	var dc *corev1.DispatchCommandEffect
	for _, ef := range cmdEffs {
		if got := ef.GetDispatchCommand(); got != nil && got.RequestId == reqID {
			dc = got
		}
	}
	if dc == nil || dc.SealedEnvelope == nil {
		t.Fatalf("expected DispatchCommandEffect with SealedEnvelope for %s (effs=%v)", reqID, cmdEffs)
	}
	prep := e.inFlightCmds[reqID]
	var resRaw []byte
	resRaw = litewire.AppendString(resRaw, 3, "executed")
	resRaw = litewire.AppendString(resRaw, 4, "ok")
	resRaw = litewire.AppendBytes(resRaw, 6, prep.RequestKeyHash)
	resRaw = litewire.AppendBytes(resRaw, 7, prep.PayloadHash)
	resRaw = litewire.AppendBool(resRaw, 8, true)
	sealedRes := sealWireObj(t, machine, dom, s1, sec, nil, resRaw, devicesec.Result, machine.id, "s1", reqID, 0)
	_, outRes := e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 120,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:    reqID,
				AttemptId:    dc.AttemptId,
				Source:       corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				AttemptBound: true,
				OuterOutcome: "executed",
				OuterReason:  "ok",
				SealedResult: sealedRes,
			},
		},
	})
	_, settled := pumpSecurityEffects(t, e, phone, nowMs+120, outRes)
	if len(settled) != 1 || settled[0].Category != outcome.Executed {
		t.Fatalf("expected Executed from SealedResult, got settled=%+v", settled)
	}

	// Tampered PayloadHash in SealedResult must settle as Unknown.
	var badReqID string
	badStep := e.Run(func(ctx *StepContext) {
		badReqID, _ = e.SubmitCommand(ctx, SubmitCommandParams{
			MachineID: machine.id,
			SessionID: "s1",
			Command:   &v1.Command{Kind: &v1.Command_Interrupt{Interrupt: &v1.InterruptRequest{SessionId: "s1"}}},
		})
	})
	badEffs, _ := pumpSecurityEffects(t, e, phone, nowMs+121, badStep)
	var badDC *corev1.DispatchCommandEffect
	for _, ef := range badEffs {
		if got := ef.GetDispatchCommand(); got != nil && got.RequestId == badReqID {
			badDC = got
		}
	}
	if badDC == nil {
		t.Fatalf("expected DispatchCommandEffect for %s", badReqID)
	}
	badPrep := e.inFlightCmds[badReqID]
	var badResRaw []byte
	badResRaw = litewire.AppendString(badResRaw, 3, "executed")
	badResRaw = litewire.AppendString(badResRaw, 4, "ok")
	badResRaw = litewire.AppendBytes(badResRaw, 6, badPrep.RequestKeyHash)
	badResRaw = litewire.AppendBytes(badResRaw, 7, bytes.Repeat([]byte{0xde}, 32))
	badResRaw = litewire.AppendBool(badResRaw, 8, true)
	badSealed := sealWireObj(t, machine, dom, s1, sec, nil, badResRaw, devicesec.Result, machine.id, "s1", badReqID, 0)
	_, badOutRes := e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 125,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:    badReqID,
				AttemptId:    badDC.AttemptId,
				Source:       corev1.OutcomeSource_OUTCOME_SOURCE_DAEMON,
				AttemptBound: true,
				OuterOutcome: "executed",
				OuterReason:  "ok",
				SealedResult: badSealed,
			},
		},
	})
	_, badSettled := pumpSecurityEffects(t, e, phone, nowMs+125, badOutRes)
	if len(badSettled) != 1 || badSettled[0].Category != outcome.Unknown {
		t.Fatalf("expected Unknown on tampered PayloadHash, got settled=%+v", badSettled)
	}

	// Prefs CAS sealing.
	prefsStep := e.Run(func(ctx *StepContext) {
		e.EnqueuePrefsOp(ctx, &lists.FullPrefsOp{Kind: lists.PrefsOpPin, Path: "/proj/a"}, false, "")
	})
	var getPrefsOp uint64
	for _, ef := range prefsStep.Effects {
		if rpc := ef.GetRpc(); rpc != nil && rpc.GetGetAccountPrefs() != nil {
			getPrefsOp = rpc.OpId
		}
	}
	_, casGetRes := e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 130,
		Event: &corev1.Event_AccountPrefsFetched{
			AccountPrefsFetched: &corev1.AccountPrefsFetchedEvent{
				OpId:      getPrefsOp,
				WirePrefs: &v1.AccountPrefsObject{ObjectId: ProjectPrefsObjectID, Revision: 0},
			},
		},
	})
	casEffs, _ := pumpSecurityEffects(t, e, phone, nowMs+130, casGetRes)
	var putRpc *corev1.PutAccountPrefsRpc
	for _, ef := range casEffs {
		if rpc := ef.GetRpc(); rpc != nil && rpc.GetPutAccountPrefs() != nil {
			putRpc = rpc.GetPutAccountPrefs()
		}
	}
	if putRpc == nil || putRpc.SealedEnvelope == nil {
		t.Fatalf("expected PutAccountPrefsRpc with SealedEnvelope")
	}
}

func TestSyncWireDirectEventsSenderVerification(t *testing.T) {
	h := newDirectSyncHarness(t)
	const sid = "s-direct-wire"

	phone := syncTestActor{
		id:   h.client.id,
		ed:   h.client.signPriv,
		edP:  h.client.signPub[:],
		x:    h.client.xPriv,
		xPub: h.client.xPub,
	}
	machine := syncTestActor{
		id:   h.daemon.id,
		ed:   h.daemon.signPriv,
		edP:  h.daemon.signPub[:],
		x:    h.daemon.xPriv,
		xPub: h.daemon.xPub,
	}
	impostor := newSyncTestActor(t, 9)
	rec := newSyncTestActor(t, 7)

	sec := bytes.Repeat([]byte{0x6b}, 32)
	u0, err := devicesec.NewGenesis(h.account, h.domain, phone.member(devicesec.Client), [32]byte(rec.edP), sec, uint64(h.now-1000), syncTestHPKE{})
	if err != nil {
		t.Fatal(err)
	}
	sb0, _ := u0.SigningBytes()
	u0.Signature = ed25519.Sign(phone.ed, sb0)
	s0Raw, _ := u0.MarshalBinary()
	s0, _ := devicesec.ParseState(s0Raw)
	gen, _ := s0.Hash()

	u1, err := devicesec.NewMembershipState(s0, []devicesec.Member{
		phone.member(devicesec.Client),
		machine.member(devicesec.Machine),
	}, nil, sec, sec, uint64(h.now-500), syncTestHPKE{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sb1, _ := u1.SigningBytes()
	u1.Signature = ed25519.Sign(phone.ed, sb1)
	s1Raw, _ := u1.MarshalBinary()
	s1, _ := devicesec.ParseState(s1Raw)

	u2, err := devicesec.NewMembershipState(s1, []devicesec.Member{
		phone.member(devicesec.Client),
		machine.member(devicesec.Machine),
		impostor.member(devicesec.Client),
	}, nil, sec, sec, uint64(h.now-400), syncTestHPKE{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sb2, _ := u2.SigningBytes()
	u2.Signature = ed25519.Sign(phone.ed, sb2)
	s2Raw, _ := u2.MarshalBinary()
	s2, _ := devicesec.ParseState(s2Raw)
	s2Hash, _ := s2.Hash()
	lease, _ := devicesec.NewLease(s2, uint64(h.now-400))
	lb, _ := lease.SigningBytes()
	lease.Signature = ed25519.Sign(phone.ed, lb)
	leaseRaw, _ := lease.MarshalBinary()

	h.headHash = s2Hash
	h.codec, err = peerwire.NewCodec(h.account, h.domain, s2Hash, s2.Core.Epoch, 17)
	if err != nil {
		t.Fatal(err)
	}

	// Provide deterministic non-repeating OS entropy, bring up main leg, configure transport + wire security.
	detEntropy := make([]byte, 4096)
	for off := 0; off < len(detEntropy); off += sha256.Size {
		var ctr [8]byte
		binary.LittleEndian.PutUint64(ctr[:], uint64(off/sha256.Size+100))
		sum := sha256.Sum256(ctr[:])
		copy(detEntropy[off:], sum[:])
	}
	h.step(&corev1.Event{Event: &corev1.Event_EntropyProvided{EntropyProvided: &corev1.EntropyProvided{RandomBytes: detEntropy}}})

	startRes := h.step(&corev1.Event{
		Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START}},
	})
	var tokenOp uint64
	for _, ef := range startRes.Effects {
		if et := ef.GetEnsureToken(); et != nil {
			tokenOp = et.OpId
		}
	}
	tokRes := h.step(&corev1.Event{
		Event: &corev1.Event_TokenResult{TokenResult: &corev1.TokenResultEvent{OpId: tokenOp, Token: "tok-1", ExpiresMs: h.now + 3_600_000}},
	})
	var legID uint64
	for _, ef := range tokRes.Effects {
		if os := ef.GetOpenStream(); os != nil {
			legID = os.LegId
		}
	}
	h.step(&corev1.Event{Event: &corev1.Event_StreamReady{StreamReady: &corev1.StreamReadyEvent{LegId: legID, ServerTimeMs: h.now}}})
	h.step(&corev1.Event{
		Event: &corev1.Event_TransportConfig{
			TransportConfig: &corev1.TransportConfigEvent{
				Supported:       true,
				Account:         h.account,
				ProtocolVersion: PeerVersion,
				ServerHost:      "127.0.0.1",
				PlatformAgent:   "TestBrowser/1.0",
			},
		},
	})
	h.step(&corev1.Event{Event: &corev1.Event_SecurityConfig{
		SecurityConfig: &corev1.SecurityConfigEvent{Account: h.account, DeviceId: phone.id},
	}})

	// Drive unified S5 Security* handshake.
	var signalsToDaemon [][]byte
	var daemonToClient [][]byte
	type pcRec struct {
		sdp       []byte
		remote    []byte
		connected bool
	}
	clientPCs := map[uint64]*pcRec{}
	daemonPCs := map[uint64]*pcRec{}
	daemonInst := make([]byte, 16)
	daemonBoot := make([]byte, 32)
	for i := range daemonInst {
		daemonInst[i] = byte(10 + i)
		daemonBoot[i] = byte(40 + i)
	}
	daemonPub := peerwire.Public{ID: []byte(h.daemon.id), Role: 2, Instance: daemonInst, Boot: daemonBoot, Sign: h.daemon.signPub[:], Box: h.daemon.xPub[:]}
	clientMember := peerwire.Public{ID: []byte(h.client.id), Role: 1, Sign: h.client.signPub[:], Box: h.client.xPub[:]}
	var daemonCapSent bool

	var handleDaemonEffs func([]linkcore.Effect)
	handleDaemonEffs = func(effs []linkcore.Effect) {
		for _, e := range effs {
			switch e := e.(type) {
			case linkcore.SignFrame:
				sig := ed25519.Sign(h.daemon.signPriv, e.Message)
				handleDaemonEffs(h.daemonSec.Handle(linkcore.FrameSigned{Op: e.Op, Signature: sig}, uint64(h.now)))
			case linkcore.TransmitFrame:
				daemonToClient = append(daemonToClient, e.Raw)
			case linkcore.CreatePC:
				daemonPCs[e.ID] = &pcRec{}
				handleDaemonEffs(h.daemonSec.Handle(linkcore.PCCreated{ID: e.ID, OK: true}, uint64(h.now)))
			case linkcore.DescribePC:
				sdp := testDirectSDP(int(e.ID) + 200)
				daemonPCs[e.ID].sdp = sdp
				handleDaemonEffs(h.daemonSec.Handle(linkcore.PCDescribed{ID: e.ID, SDP: sdp, OK: true}, uint64(h.now)))
				handleDaemonEffs(h.daemonSec.Handle(linkcore.PCLocalCandidate{ID: e.ID, Candidate: []byte("candidate:1 1 udp 2130706431 203.0.113.9 50001 typ srflx"), Valid: true}, uint64(h.now)))
				handleDaemonEffs(h.daemonSec.Handle(linkcore.PCLocalCandidate{ID: e.ID, End: true, Valid: true}, uint64(h.now)))
			case linkcore.SetRemotePC:
				daemonPCs[e.ID].remote = e.SDP
				handleDaemonEffs(h.daemonSec.Handle(linkcore.PCRemoteSet{ID: e.ID, OK: true}, uint64(h.now)))
			case linkcore.AddCandidatesPC:
				handleDaemonEffs(h.daemonSec.Handle(linkcore.PCCandidatesAdded{ID: e.ID, OK: true}, uint64(h.now)))
			case linkcore.HandleControl:
				if e.Frame.Type == 15 {
					handleDaemonEffs(h.daemonSec.Handle(linkcore.ControlHandled{Op: e.Op, Type: 4, Body: directAckPayload(e.Frame)}, uint64(h.now)))
				} else {
					handleDaemonEffs(h.daemonSec.Handle(linkcore.ControlHandled{Op: e.Op, Type: 0}, uint64(h.now)))
				}
			case linkcore.ConfirmPC:
				if !daemonCapSent && e.Use == 1 {
					daemonCapSent = true
					mc := peerwire.PB(peerwire.U(2, 1), peerwire.U(5, 65536), peerwire.U(13, 1))
					cap14 := peerwire.PB(peerwire.B(1, make([]byte, 16)), peerwire.B(2, mc), peerwire.U(3, uint64(h.now+60_000)))
					handleDaemonEffs(h.daemonSec.Handle(linkcore.QueueControl{Type: 14, Body: cap14}, uint64(h.now)))
					cap1 := peerwire.PB(peerwire.B(1, make([]byte, 16)), peerwire.U(2, 15), peerwire.U(4, uint64(h.now+60_000)), peerwire.U(5, 1), peerwire.U(6, 1048576))
					handleDaemonEffs(h.daemonSec.Handle(linkcore.QueueControl{Type: 1, Body: cap1}, uint64(h.now)))
				}
			}
		}
	}

	var drainEffects func([]*corev1.Effect)
	drainEffects = func(effs []*corev1.Effect) {
		for _, eff := range effs {
			switch k := eff.Effect.(type) {
			case *corev1.Effect_SecurityKeySign:
				sig := ed25519.Sign(phone.ed, k.SecurityKeySign.Message)
				drainEffects(h.step(&corev1.Event{
					Event: &corev1.Event_SecurityKeySigned{SecurityKeySigned: &corev1.SecurityKeySignedEvent{OpId: k.SecurityKeySign.OpId, Signature: sig}},
				}).Effects)
			case *corev1.Effect_SecurityKeyAgree:
				peer, err := ecdh.X25519().NewPublicKey(k.SecurityKeyAgree.PeerPublic)
				if err != nil {
					t.Fatal(err)
				}
				shared, err := phone.x.ECDH(peer)
				if err != nil {
					t.Fatal(err)
				}
				stepRes := h.step(&corev1.Event{
					Event: &corev1.Event_SecurityKeyAgreed{SecurityKeyAgreed: &corev1.SecurityKeyAgreedEvent{OpId: k.SecurityKeyAgree.OpId, Shared: shared}},
				})
				if stepRes.Dirty.SyncState || stepRes.Dirty.Home {
					t.Fatalf("expected SecurityKeyAgreed not to mark SyncState/Home dirty when membership unchanged, got %+v", stepRes.Dirty)
				}
				drainEffects(stepRes.Effects)
			case *corev1.Effect_DirectSignalSend:
				raw := peerwire.PB(
					peerwire.B(1, []byte(h.client.id)),
					peerwire.B(2, k.DirectSignalSend.ToDevice),
					peerwire.U(3, uint64(k.DirectSignalSend.FrameClass)),
					peerwire.B(4, k.DirectSignalSend.FrameId),
					peerwire.B(5, k.DirectSignalSend.Sealed),
				)
				signalsToDaemon = append(signalsToDaemon, raw)
			case *corev1.Effect_DirectPcCreate:
				c := k.DirectPcCreate
				clientPCs[c.PcId] = &pcRec{}
				if transport.Use(c.Use) == transport.UseRead {
					h.readPCID = c.PcId
				} else if transport.Use(c.Use) == transport.UseBulk {
					h.bulkPCID = c.PcId
				}
				drainEffects(h.step(&corev1.Event{
					Event: &corev1.Event_DirectPcCreated{DirectPcCreated: &corev1.DirectPcCreatedEvent{PcId: c.PcId, Ok: true}},
				}).Effects)
			case *corev1.Effect_DirectPcDescribe:
				d := k.DirectPcDescribe
				sdp := testDirectSDP(int(d.PcId))
				clientPCs[d.PcId].sdp = sdp
				drainEffects(h.step(&corev1.Event{
					Event: &corev1.Event_DirectPcDescription{DirectPcDescription: &corev1.DirectPcDescriptionEvent{PcId: d.PcId, Sdp: sdp, Ok: true}},
				}).Effects)
				drainEffects(h.step(&corev1.Event{
					Event: &corev1.Event_DirectPcLocalCandidate{DirectPcLocalCandidate: &corev1.DirectPcLocalCandidateEvent{PcId: d.PcId, Candidate: "candidate:1 1 udp 2130706431 203.0.113.5 50000 typ srflx"}},
				}).Effects)
				drainEffects(h.step(&corev1.Event{
					Event: &corev1.Event_DirectPcLocalCandidate{DirectPcLocalCandidate: &corev1.DirectPcLocalCandidateEvent{PcId: d.PcId, EndOfCandidates: true}},
				}).Effects)
			case *corev1.Effect_DirectPcSetRemote:
				sr := k.DirectPcSetRemote
				clientPCs[sr.PcId].remote = sr.Sdp
				drainEffects(h.step(&corev1.Event{
					Event: &corev1.Event_DirectPcRemoteSet{DirectPcRemoteSet: &corev1.DirectPcRemoteSetEvent{PcId: sr.PcId, Ok: true}},
				}).Effects)
			case *corev1.Effect_DirectPcAddCandidates:
				drainEffects(h.step(&corev1.Event{
					Event: &corev1.Event_DirectPcCandidatesAdded{DirectPcCandidatesAdded: &corev1.DirectPcCandidatesAddedEvent{PcId: k.DirectPcAddCandidates.PcId, Ok: true}},
				}).Effects)
			case *corev1.Effect_DirectDcReadStats:
				drainEffects(h.step(&corev1.Event{
					Event: &corev1.Event_DirectDcStats{DirectDcStats: &corev1.DirectDcStatsEvent{
						PcId: k.DirectDcReadStats.PcId,
						Report: &corev1.DirectStatsReport{
							DtlsState: "connected",
							DcState:   "open",
							Pairs: []*corev1.DirectStatsPair{
								{LocalType: corev1.DirectCandType_DIRECT_CAND_TYPE_SRFLX, LocalFamily: corev1.DirectCandFamily_DIRECT_CAND_FAMILY_V4, RemoteType: corev1.DirectCandType_DIRECT_CAND_TYPE_SRFLX, RemoteFamily: corev1.DirectCandFamily_DIRECT_CAND_FAMILY_V4, Nominated: true, State: "succeeded", RequestsSent: 3, ResponsesReceived: 3, CurrentRttSeconds: 0.012, HasCurrentRtt: true},
							},
						},
					}},
				}).Effects)
			}
		}
	}

	drainEffects(applyTestSecurityRecord(h.eng, h.now, security.RecordUpdate{
		NetworkGeneration: 1,
		Account:           h.account,
		Domain:            h.domain[:],
		Genesis:           gen[:],
		States:            [][]byte{s0Raw, s1Raw, s2Raw},
		Lease:             leaseRaw,
	}).Effects)
	if !h.eng.security.Loaded || !h.eng.security.IsMember {
		t.Fatalf("security record not active: sec=%+v", h.eng.security)
	}

	for step := 0; step < 40 && !h.eng.ensureTransportCoord().Ready(h.now, machine.id, transport.Hint{Use: transport.UseRead}); step++ {
		for len(signalsToDaemon) > 0 {
			raw := signalsToDaemon[0]
			signalsToDaemon = signalsToDaemon[1:]
			if h.daemonSec == nil {
				opened, err := h.codec.Open(raw, clientMember, daemonPub, h.daemon.agree(h.client.xPub), uint64(h.now))
				if err != nil {
					t.Fatalf("daemon open initial: %v", err)
				}
				sess, initEffs, err := linkcore.NewSession(linkcore.SessionConfig{
					Self: daemonPub,
					Peer: peerwire.Public{
						ID:       []byte(h.client.id),
						Role:     1,
						Instance: opened.Context.SenderInstance,
						Boot:     opened.Context.SenderBoot,
						Sign:     h.client.signPub[:],
						Box:      h.client.xPub[:],
					},
					Shared:     h.daemon.agree(h.client.xPub),
					Codec:      h.codec,
					Connection: opened.Context.Connection,
					Deadline:   opened.Context.Deadline,
					Entropy:    rand.Reader,
					Allow:      func() bool { return true },
				}, false, uint64(h.now))
				if err != nil {
					t.Fatal(err)
				}
				h.daemonSec = sess
				handleDaemonEffs(initEffs)
			}
			handleDaemonEffs(h.daemonSec.Handle(linkcore.FrameReceived{Raw: raw}, uint64(h.now)))
		}
		for len(daemonToClient) > 0 {
			raw := daemonToClient[0]
			daemonToClient = daemonToClient[1:]
			drainEffects(h.step(&corev1.Event{
				Event: &corev1.Event_DirectSignalReceived{DirectSignalReceived: &corev1.DirectSignalReceivedEvent{Raw: raw}},
			}).Effects)
		}
		for pcID, cpc := range clientPCs {
			for dID, dpc := range daemonPCs {
				if !cpc.connected && !dpc.connected && cpc.sdp != nil && string(dpc.remote) == string(cpc.sdp) && dpc.sdp != nil && string(cpc.remote) == string(dpc.sdp) {
					cpc.connected, dpc.connected = true, true
					drainEffects(h.step(&corev1.Event{
						Event: &corev1.Event_DirectPcState{DirectPcState: &corev1.DirectPcStateEvent{PcId: pcID, Kind: corev1.DirectStateKind_DIRECT_STATE_KIND_CONN, Value: "connected"}},
					}).Effects)
					drainEffects(h.step(&corev1.Event{
						Event: &corev1.Event_DirectDcState{DirectDcState: &corev1.DirectDcStateEvent{PcId: pcID, Open: true}},
					}).Effects)
					handleDaemonEffs(h.daemonSec.Handle(linkcore.PCStateChanged{ID: dID, State: linkcore.PCConnected, Valid: true}, uint64(h.now)))
				}
			}
		}
		h.now += 25
		engAt, engOK, sessAt, sessOK, _, _ := h.eng.ensureTransportCoord().NextTimers(h.now)
		if engOK && h.now >= engAt {
			drainEffects(h.step(&corev1.Event{
				Event: &corev1.Event_TimerFired{TimerFired: &corev1.TimerFiredEvent{
					TimerId: h.eng.timers[timerKey{kind: corev1.TimerKind_TIMER_KIND_DIRECT_ENGINE}].id,
					Kind:    corev1.TimerKind_TIMER_KIND_DIRECT_ENGINE,
				}},
			}).Effects)
		}
		if sessOK && h.now >= sessAt {
			drainEffects(h.step(&corev1.Event{
				Event: &corev1.Event_TimerFired{TimerFired: &corev1.TimerFiredEvent{
					TimerId: h.eng.timers[timerKey{kind: corev1.TimerKind_TIMER_KIND_DIRECT_SESSION}].id,
					Kind:    corev1.TimerKind_TIMER_KIND_DIRECT_SESSION,
				}},
			}).Effects)
		}
		if h.daemonSec != nil {
			handleDaemonEffs(h.daemonSec.Handle(linkcore.Tick{}, uint64(h.now)))
		}
	}
	if !h.eng.ensureTransportCoord().Ready(h.now, machine.id, transport.Hint{Use: transport.UseRead}) || h.readPCID == 0 {
		t.Fatalf("expected direct path ready after S5 handshake, pcID=%d status=%+v", h.readPCID, h.eng.ensureTransportCoord().Status(h.now, machine.id))
	}

	// Deliver sealed WireMachines + WireSessions, watch session, then verify sender checks on DirectEventsReceived.
	mmRaw, _ := (&v1.MachineMeta{Hostname: "ws-direct", ProtocolVersion: PeerVersion}).Marshal()
	sessSeed := bytes.Repeat([]byte{0x42}, 32)
	smRaw, _ := (&v1.Session{Id: sid, Cwd: "/tmp/proj", Agent: v1.Agent_AGENT_CODEX, Status: v1.SessionStatus_SESSION_STATUS_RUNNING, LastSeq: 1}).Marshal()
	h.eng.Run(func(ctx *StepContext) {
		h.eng.startRefresh(ctx)
	})
	_, listsRes := h.eng.HandleEvent(&corev1.Event{
		NowMs: h.now,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId: h.eng.refreshOpID,
				WireMachines: []*v1.Machine{{
					Id:     machine.id,
					Online: true,
					Meta:   sealWireObj(t, machine, h.domain, s2, sec, nil, mmRaw, devicesec.MachineMeta, machine.id, "", "", 1),
				}},
				WireSessions: []*v1.RemoteSession{{
					MachineId: machine.id,
					SessionId: sid,
					LastSeq:   1,
					Key:       sealWireObj(t, machine, h.domain, s2, sec, nil, sessSeed, devicesec.SessionKey, machine.id, sid, "", 1),
					Meta:      sealWireObj(t, machine, h.domain, s2, sec, nil, smRaw, devicesec.SessionMeta, machine.id, sid, "", 1),
				}},
			},
		},
	})
	pumpSecurityEffects(t, h.eng, phone, h.now, listsRes)
	h.step(&corev1.Event{Event: &corev1.Event_SessionWatch{SessionWatch: &corev1.SessionWatchEvent{SessionId: sid, Watched: true}}})
	b := h.eng.Buffer(sid)
	h.step(&corev1.Event{Event: &corev1.Event_CacheEventsLoaded{CacheEventsLoaded: &corev1.CacheEventsLoadedEvent{OpId: b.activeCacheOpID, SessionId: sid}}})

	evRaw, _ := (&v1.Event{Seq: 1, SessionId: sid, Kind: &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "direct"}}}).Marshal()
	badEnv := sealWireObj(t, impostor, h.domain, s2, sec, sessSeed, evRaw, devicesec.Event, machine.id, sid, "", 1)
	badRes := h.step(&corev1.Event{
		Event: &corev1.Event_DirectEventsReceived{
			DirectEventsReceived: &corev1.DirectEventsReceivedEvent{
				MachineId:     machine.id,
				SessionId:     sid,
				PcId:          h.readPCID,
				WireEnvelopes: []*v1.Envelope{badEnv},
			},
		},
	})
	pumpSecurityEffects(t, h.eng, phone, h.now, badRes)
	if buf := h.eng.Buffer(sid); buf != nil && buf.PrefixLen() != 0 {
		t.Fatalf("expected wire direct event from non-machine signer to be rejected, got PrefixLen=%d", buf.PrefixLen())
	}

	goodEnv := sealWireObj(t, machine, h.domain, s2, sec, sessSeed, evRaw, devicesec.Event, machine.id, sid, "", 1)
	goodRes := h.step(&corev1.Event{
		Event: &corev1.Event_DirectEventsReceived{
			DirectEventsReceived: &corev1.DirectEventsReceivedEvent{
				MachineId:     machine.id,
				SessionId:     sid,
				PcId:          h.readPCID,
				WireEnvelopes: []*v1.Envelope{goodEnv},
			},
		},
	})
	pumpSecurityEffects(t, h.eng, phone, h.now, goodRes)
	if buf := h.eng.Buffer(sid); buf == nil || buf.PrefixLen() != 1 {
		t.Fatalf("expected wire direct event from trusted machine to ingest seq 1, got %v", buf)
	}

	// When bootstrapped workflow SyncStates fails on CoordAuthorize, activeSecOpID resets and authorizing closes cleanly.
	bootstrapTestSecurityRecord(t, h.eng, phone, h.now, &security.PersistedRecord{
		Pin:      devicesec.Pin{Account: h.account, Domain: h.domain, Genesis: gen},
		MemberID: phone.id,
		Member:   true,
		Server:   "https://relay.example.com",
		States:   [][]byte{s0Raw, s1Raw, s2Raw},
		Lease:    leaseRaw,
		FloorMs:  h.now,
	}, "tok-auth")
	authEffs := h.eng.Run(func(ctx *StepContext) {
		h.eng.emitCoordEffects(ctx, []transport.CoordEffect{transport.CoordAuthorize{}})
	}).Effects
	var rpcOp uint64
	for _, eff := range authEffs {
		if sr := eff.GetSecurityRpc(); sr != nil && sr.Method == corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_SECURITY_STATES {
			rpcOp = sr.OpId
		}
	}
	if rpcOp == 0 || h.eng.activeSecOpID == 0 {
		t.Fatalf("expected CoordAuthorize with bootstrapped secEngine to emit SecurityRpc GET_SECURITY_STATES, got rpcOp=%d activeSecOpID=%d", rpcOp, h.eng.activeSecOpID)
	}
	// Stale SecurityRpcResultEvent must not clear activeSecOpID.
	h.step(&corev1.Event{
		Event: &corev1.Event_SecurityRpcResult{
			SecurityRpcResult: &corev1.SecurityRpcResultEvent{
				OpId:        999999,
				Error:       "stale",
				ConnectCode: 14,
			},
		},
	})
	if h.eng.activeSecOpID == 0 {
		t.Fatalf("expected stale SecurityRpcResultEvent to be ignored cleanly")
	}
	h.step(&corev1.Event{
		Event: &corev1.Event_SecurityRpcResult{
			SecurityRpcResult: &corev1.SecurityRpcResultEvent{
				OpId:        rpcOp,
				Error:       "sync failed",
				ConnectCode: 14,
			},
		},
	})
	if h.eng.activeSecOpID != 0 {
		t.Fatalf("expected failed SecurityRpcResultEvent to clear activeSecOpID, got %d", h.eng.activeSecOpID)
	}
}

func TestInitializedSyncFailureClosesAuthorizingGate(t *testing.T) {
	nowMs := int64(1_700_000_000_000)
	phone := newSyncTestActor(t, 1)
	machine := newSyncTestActor(t, 2)
	rec := newSyncTestActor(t, 3)
	dom := devicesec.Digest("sync-auth-domain", []byte{2})
	sec := bytes.Repeat([]byte{0x6c}, 32)

	u0, err := devicesec.NewGenesis(phone.id, dom, phone.member(devicesec.Client), [32]byte(rec.edP), sec, uint64(nowMs), syncTestHPKE{})
	if err != nil {
		t.Fatal(err)
	}
	sb0, _ := u0.SigningBytes()
	u0.Signature = ed25519.Sign(phone.ed, sb0)
	s0Raw, _ := u0.MarshalBinary()
	s0, _ := devicesec.ParseState(s0Raw)
	gen, _ := s0.Hash()

	u1, err := devicesec.NewMembershipState(s0, []devicesec.Member{phone.member(devicesec.Client), machine.member(devicesec.Machine)}, nil, sec, sec, uint64(nowMs+10), syncTestHPKE{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sb1, _ := u1.SigningBytes()
	u1.Signature = ed25519.Sign(phone.ed, sb1)
	s1Raw, _ := u1.MarshalBinary()
	s1, _ := devicesec.ParseState(s1Raw)
	lease, _ := devicesec.NewLease(s1, uint64(nowMs+10))
	lb, _ := lease.SigningBytes()
	lease.Signature = ed25519.Sign(phone.ed, lb)
	leaseRaw, _ := lease.MarshalBinary()

	e := NewEngine()
	detEntropy := make([]byte, 2048)
	for off := range detEntropy {
		detEntropy[off] = byte(off*17 + 3)
	}
	e.HandleEvent(&corev1.Event{NowMs: nowMs, Event: &corev1.Event_EntropyProvided{EntropyProvided: &corev1.EntropyProvided{RandomBytes: detEntropy}}})
	bootstrapTestSecurityRecord(t, e, phone, nowMs, &security.PersistedRecord{
		Pin:      devicesec.Pin{Account: phone.id, Domain: dom, Genesis: gen},
		MemberID: phone.id,
		Member:   true,
		Server:   "https://relay.example.com",
		States:   [][]byte{s0Raw, s1Raw},
		Lease:    leaseRaw,
		FloorMs:  nowMs + 60_000,
	}, "tok-auth-gate")
	if _, ok := e.secEngine.CoordInfo(); !ok {
		t.Fatal("expected initialized CoordInfo() to be true")
	}

	e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 5,
		Event: &corev1.Event_TransportConfig{
			TransportConfig: &corev1.TransportConfigEvent{
				Supported:       true,
				Account:         phone.id,
				ProtocolVersion: PeerVersion,
				ServerHost:      "127.0.0.1",
				PlatformAgent:   "TestBrowser/1.0",
			},
		},
	})
	_, startRes := e.HandleEvent(&corev1.Event{NowMs: nowMs + 10, Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START}}})
	_, fgRes := e.HandleEvent(&corev1.Event{NowMs: nowMs + 10, Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_FOREGROUND}}})
	_, tokRes := e.HandleEvent(&corev1.Event{NowMs: nowMs + 10, Event: &corev1.Event_TokenResult{TokenResult: &corev1.TokenResultEvent{OpId: e.activeTokenOpID, Token: "tok-auth-gate"}}})
	var legID uint64
	var initSyncOp uint64
	for _, effs := range [][]*corev1.Effect{startRes.Effects, fgRes.Effects, tokRes.Effects} {
		for _, ef := range effs {
			if os := ef.GetOpenStream(); os != nil {
				legID = os.LegId
			}
			if sr := ef.GetSecurityRpc(); sr != nil && sr.Method == corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_SECURITY_STATES {
				initSyncOp = sr.OpId
			}
		}
	}
	if initSyncOp != 0 {
		respBytes, _ := proto.Marshal(&stdv1.GetSecurityStatesResponse{HeadEpoch: 2})
		_, initSyncRes := e.HandleEvent(&corev1.Event{
			NowMs:  nowMs + 15,
			MonoMs: 15,
			Event: &corev1.Event_SecurityRpcResult{
				SecurityRpcResult: &corev1.SecurityRpcResultEvent{
					OpId:          initSyncOp,
					ResponseBytes: respBytes,
				},
			},
		})
		pumpSecurityEffects(t, e, phone, nowMs+15, initSyncRes)
	}
	_, streamRes := e.HandleEvent(&corev1.Event{NowMs: nowMs + 20, MonoMs: 20, Event: &corev1.Event_StreamReady{StreamReady: &corev1.StreamReadyEvent{LegId: legID}}})
	pumpSecurityEffects(t, e, phone, nowMs+20, streamRes)

	// Drain until transport proof expires past FreshMs so DIRECT_ENGINE emits CoordAuthorize -> GET_SECURITY_STATES.
	_, tick1 := e.HandleEvent(&corev1.Event{
		NowMs:  nowMs + 25_000,
		MonoMs: 25_000,
		Event: &corev1.Event_TimerFired{
			TimerFired: &corev1.TimerFiredEvent{
				TimerId: e.timers[timerKey{kind: corev1.TimerKind_TIMER_KIND_DIRECT_ENGINE}].id,
				Kind:    corev1.TimerKind_TIMER_KIND_DIRECT_ENGINE,
			},
		},
	})
	var rpcOp1 uint64
	for _, ef := range tick1.Effects {
		if sr := ef.GetSecurityRpc(); sr != nil && sr.Method == corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_SECURITY_STATES {
			rpcOp1 = sr.OpId
		}
	}
	if rpcOp1 == 0 {
		t.Fatal("expected DIRECT_ENGINE tick via CoordAuthorize to emit GET_SECURITY_STATES SecurityRpc")
	}

	// Simulate transient SyncStates failure on already-initialized client.
	_, errRes := e.HandleEvent(&corev1.Event{
		NowMs:  nowMs + 25_100,
		MonoMs: 25_100,
		Event: &corev1.Event_SecurityRpcResult{
			SecurityRpcResult: &corev1.SecurityRpcResultEvent{
				OpId:         rpcOp1,
				ConnectCode:  14,
				Error:        "transient sync failure",
				NetworkError: true,
			},
		},
	})
	pumpSecurityEffects(t, e, phone, nowMs+25_100, errRes)

	// Without Fix 4, OnAuthorizeDone(now, false) was skipped when CoordInfo()==true, locking authorizing=true forever.
	_, tick2 := e.HandleEvent(&corev1.Event{
		NowMs:  nowMs + 40_000,
		MonoMs: 40_000,
		Event: &corev1.Event_TimerFired{
			TimerFired: &corev1.TimerFiredEvent{
				TimerId: e.timers[timerKey{kind: corev1.TimerKind_TIMER_KIND_DIRECT_ENGINE}].id,
				Kind:    corev1.TimerKind_TIMER_KIND_DIRECT_ENGINE,
			},
		},
	})
	var rpcOp2 uint64
	for _, ef := range tick2.Effects {
		if sr := ef.GetSecurityRpc(); sr != nil && sr.Method == corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_GET_SECURITY_STATES {
			rpcOp2 = sr.OpId
		}
	}
	if rpcOp2 == 0 || rpcOp2 == rpcOp1 {
		t.Fatal("expected failed SyncStates on initialized client to release authorizing gate so next tick retries")
	}
}

func TestCommandRetryResealsEnvelopeAfterContentEpochRotation(t *testing.T) {
	nowMs := int64(1_700_000_000_000)
	phone := newSyncTestActor(t, 1)
	machine := newSyncTestActor(t, 2)
	rec := newSyncTestActor(t, 3)
	dom := devicesec.Digest("cmd-reseal-domain", []byte{3})
	sec1 := bytes.Repeat([]byte{0x41}, 32)
	sec2 := bytes.Repeat([]byte{0x42}, 32)

	u0, err := devicesec.NewGenesis(phone.id, dom, phone.member(devicesec.Client), [32]byte(rec.edP), sec1, uint64(nowMs), syncTestHPKE{})
	if err != nil {
		t.Fatal(err)
	}
	sb0, _ := u0.SigningBytes()
	u0.Signature = ed25519.Sign(phone.ed, sb0)
	s0Raw, _ := u0.MarshalBinary()
	s0, _ := devicesec.ParseState(s0Raw)
	gen, _ := s0.Hash()

	u1, err := devicesec.NewMembershipState(s0, []devicesec.Member{phone.member(devicesec.Client), machine.member(devicesec.Machine)}, nil, sec1, sec1, uint64(nowMs+10), syncTestHPKE{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sb1, _ := u1.SigningBytes()
	u1.Signature = ed25519.Sign(phone.ed, sb1)
	s1Raw, _ := u1.MarshalBinary()
	s1, _ := devicesec.ParseState(s1Raw)
	s1Hash, _ := s1.Hash()
	lease1, _ := devicesec.NewLease(s1, uint64(nowMs+10))
	lb1, _ := lease1.SigningBytes()
	lease1.Signature = ed25519.Sign(phone.ed, lb1)
	lease1Raw, _ := lease1.MarshalBinary()

	e := NewEngine()
	detEntropy := make([]byte, 4096)
	for off := range detEntropy {
		detEntropy[off] = byte(off*29 + 7)
	}
	e.HandleEvent(&corev1.Event{NowMs: nowMs, Event: &corev1.Event_EntropyProvided{EntropyProvided: &corev1.EntropyProvided{RandomBytes: detEntropy}}})
	bootstrapTestSecurityRecord(t, e, phone, nowMs, &security.PersistedRecord{
		Pin:      devicesec.Pin{Account: phone.id, Domain: dom, Genesis: gen},
		MemberID: phone.id,
		Member:   true,
		Server:   "https://relay.example.com",
		States:   [][]byte{s0Raw, s1Raw},
		Lease:    lease1Raw,
		FloorMs:  nowMs,
	}, "tok-cmd-reseal")

	e.HandleEvent(&corev1.Event{NowMs: nowMs + 10, Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START}}})
	e.HandleEvent(&corev1.Event{NowMs: nowMs + 10, Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_FOREGROUND}}})
	_, tokRes := e.HandleEvent(&corev1.Event{NowMs: nowMs + 10, Event: &corev1.Event_TokenResult{TokenResult: &corev1.TokenResultEvent{OpId: e.activeTokenOpID, Token: "tok-cmd-reseal"}}})
	var legID uint64
	for _, ef := range tokRes.Effects {
		if os := ef.GetOpenStream(); os != nil {
			legID = os.LegId
		}
	}
	e.HandleEvent(&corev1.Event{NowMs: nowMs + 20, Event: &corev1.Event_StreamReady{StreamReady: &corev1.StreamReadyEvent{LegId: legID}}})
	mmRaw, _ := (&v1.MachineMeta{Hostname: "host-reseal", ProtocolVersion: PeerVersion}).Marshal()
	_, listsRes := e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 25,
		Event: &corev1.Event_ListsFetched{
			ListsFetched: &corev1.ListsFetchedEvent{
				OpId: e.refreshOpID,
				WireMachines: []*v1.Machine{{
					Id:     machine.id,
					Online: true,
					Meta:   sealWireObj(t, machine, dom, s1, sec1, nil, mmRaw, devicesec.MachineMeta, machine.id, "", "", 1),
				}},
			},
		},
	})
	pumpSecurityEffects(t, e, phone, nowMs+25, listsRes)

	// Initial command is sealed under content epoch s1.Core.ContentEpoch.
	var reqID string
	subRes := e.Run(func(ctx *StepContext) {
		reqID, _ = e.SubmitCommand(ctx, SubmitCommandParams{
			MachineID: machine.id,
			SessionID: "s1",
			Command:   &v1.Command{Kind: &v1.Command_Interrupt{Interrupt: &v1.InterruptRequest{SessionId: "s1"}}},
		})
	})
	eff1, _ := pumpSecurityEffects(t, e, phone, nowMs+30, subRes)
	var dc1 *corev1.DispatchCommandEffect
	for _, ef := range eff1 {
		if got := ef.GetDispatchCommand(); got != nil && got.RequestId == reqID {
			dc1 = got
		}
	}
	if dc1 == nil || dc1.SealedEnvelope == nil {
		t.Fatalf("expected initial DispatchCommandEffect with SealedEnvelope (effs=%v)", eff1)
	}
	ct1, err := security.FromProtoEnvelope(dc1.SealedEnvelope)
	if err != nil {
		t.Fatalf("parse initial envelope: %v", err)
	}
	hdr1, err := devicesec.ParseHeader(ct1.Header)
	if err != nil {
		t.Fatalf("parse initial header: %v", err)
	}
	if hdr1.ContentEpoch != s1.Core.ContentEpoch || hdr1.State != s1Hash {
		t.Fatalf("expected initial seal at epoch=%d head=%x, got epoch=%d head=%x", s1.Core.ContentEpoch, s1Hash, hdr1.ContentEpoch, hdr1.State)
	}

	// Transient transport failure schedules TIMER_KIND_COMMAND_RETRY.
	_, failRes := e.HandleEvent(&corev1.Event{
		NowMs:  nowMs + 40,
		MonoMs: 40,
		Event: &corev1.Event_CommandOutcome{
			CommandOutcome: &corev1.CommandOutcomeEvent{
				RequestId:     reqID,
				AttemptId:     dc1.AttemptId,
				Source:        corev1.OutcomeSource_OUTCOME_SOURCE_TRANSPORT,
				TransportCode: errCodeUnavailable,
				NotForwarded:  true,
			},
		},
	})
	var retryDelay int64
	var retryTimerID uint64
	for _, ef := range failRes.Effects {
		if st := ef.GetScheduleTimer(); st != nil && st.Kind == corev1.TimerKind_TIMER_KIND_COMMAND_RETRY {
			retryDelay = st.DelayMs
			retryTimerID = st.TimerId
		}
	}
	if retryDelay <= 0 || retryTimerID == 0 {
		t.Fatalf("expected TIMER_KIND_COMMAND_RETRY after unavailable transport outcome, got %+v", failRes.Effects)
	}

	// Before retry fires, rotate content epoch via a new membership state s2 + sec2.
	extra := newSyncTestActor(t, 4)
	u2, err := devicesec.NewMembershipState(s1, []devicesec.Member{phone.member(devicesec.Client), machine.member(devicesec.Machine), extra.member(devicesec.Client)}, nil, sec1, sec2, uint64(nowMs+50), syncTestHPKE{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sb2, _ := u2.SigningBytes()
	u2.Signature = ed25519.Sign(phone.ed, sb2)
	s2Raw, _ := u2.MarshalBinary()
	s2, _ := devicesec.ParseState(s2Raw)
	s2Hash, _ := s2.Hash()
	if s2.Core.ContentEpoch == s1.Core.ContentEpoch {
		t.Fatalf("expected rotated ContentEpoch > %d", s1.Core.ContentEpoch)
	}
	lease2, _ := devicesec.NewLease(s2, uint64(nowMs+50))
	lb2, _ := lease2.SigningBytes()
	lease2.Signature = ed25519.Sign(phone.ed, lb2)
	lease2Raw, _ := lease2.MarshalBinary()
	recRes := applyTestSecurityRecord(e, nowMs+50, security.RecordUpdate{
		NetworkGeneration: 1,
		Account:           phone.id,
		Domain:            dom[:],
		Genesis:           gen[:],
		States:            [][]byte{s0Raw, s1Raw, s2Raw},
		Lease:             lease2Raw,
	})
	pumpSecurityEffects(t, e, phone, nowMs+50, recRes)

	// Fire retry timer -> must preserve RequestId/DeadlineMs/CommandBytes while resealing for s2.ContentEpoch.
	_, retryStep := e.HandleEvent(&corev1.Event{
		NowMs:  nowMs + 40 + retryDelay,
		MonoMs: 40 + retryDelay,
		Event:  &corev1.Event_TimerFired{TimerFired: &corev1.TimerFiredEvent{TimerId: retryTimerID, Kind: corev1.TimerKind_TIMER_KIND_COMMAND_RETRY, Key: reqID}},
	})
	eff2, _ := pumpSecurityEffects(t, e, phone, nowMs+40+retryDelay, retryStep)
	var dc2 *corev1.DispatchCommandEffect
	for _, ef := range eff2 {
		if got := ef.GetDispatchCommand(); got != nil && got.RequestId == reqID {
			dc2 = got
		}
	}
	if dc2 == nil || dc2.SealedEnvelope == nil {
		t.Fatalf("expected retried DispatchCommandEffect with SealedEnvelope")
	}
	if dc2.RequestId != dc1.RequestId || dc2.DeadlineMs != dc1.DeadlineMs || !bytes.Equal(dc2.CommandBytes, dc1.CommandBytes) {
		t.Fatalf("retry must preserve RequestId/DeadlineMs/CommandBytes, got dc2=%+v vs dc1=%+v", dc2, dc1)
	}
	ct2, err := security.FromProtoEnvelope(dc2.SealedEnvelope)
	if err != nil {
		t.Fatalf("parse retried envelope: %v", err)
	}
	hdr2, err := devicesec.ParseHeader(ct2.Header)
	if err != nil {
		t.Fatalf("parse retried header: %v", err)
	}
	if hdr2.ContentEpoch != s2.Core.ContentEpoch || hdr2.State != s2Hash {
		t.Fatalf("expected retried envelope resealed at epoch=%d head=%x, got epoch=%d head=%x", s2.Core.ContentEpoch, s2Hash, hdr2.ContentEpoch, hdr2.State)
	}
}

func TestSessionByIDDecryptFailureIsRetryableNotMissing(t *testing.T) {
	nowMs := int64(1_700_000_000_000)
	phone := newSyncTestActor(t, 11)
	machine := newSyncTestActor(t, 12)
	rec := newSyncTestActor(t, 13)
	dom := devicesec.Digest("sync-session-by-id", []byte{2})
	sec := bytes.Repeat([]byte{0x55}, 32)
	u0, err := devicesec.NewGenesis(phone.id, dom, phone.member(devicesec.Client), [32]byte(rec.edP), sec, uint64(nowMs), syncTestHPKE{})
	if err != nil {
		t.Fatal(err)
	}
	sb0, _ := u0.SigningBytes()
	u0.Signature = ed25519.Sign(phone.ed, sb0)
	s0Raw, _ := u0.MarshalBinary()
	s0, _ := devicesec.ParseState(s0Raw)
	gen, _ := s0.Hash()

	u1, err := devicesec.NewMembershipState(s0, []devicesec.Member{phone.member(devicesec.Client), machine.member(devicesec.Machine)}, nil, sec, sec, uint64(nowMs+10), syncTestHPKE{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sb1, _ := u1.SigningBytes()
	u1.Signature = ed25519.Sign(phone.ed, sb1)
	s1Raw, _ := u1.MarshalBinary()
	s1, _ := devicesec.ParseState(s1Raw)
	lease, _ := devicesec.NewLease(s1, uint64(nowMs+10))
	lb, _ := lease.SigningBytes()
	lease.Signature = ed25519.Sign(phone.ed, lb)
	leaseRaw, _ := lease.MarshalBinary()

	e := NewEngine()
	e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 20,
		Event: &corev1.Event_SecurityConfig{
			SecurityConfig: &corev1.SecurityConfigEvent{Account: phone.id, DeviceId: phone.id, InlineCrypto: true},
		},
	})
	recRes := applyTestSecurityRecord(e, nowMs+20, security.RecordUpdate{
		NetworkGeneration: 1,
		Account:           phone.id,
		Domain:            dom[:],
		Genesis:           gen[:],
		States:            [][]byte{s0Raw, s1Raw},
		Lease:             leaseRaw,
	})
	pumpSecurityEffects(t, e, phone, nowMs+20, recRes)

	// Start sync so e.running == true.
	e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 30,
		Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START}},
	})

	fetchOp := func(sid string) uint64 {
		_, res := e.HandleEvent(&corev1.Event{
			NowMs: nowMs + 40,
			Event: &corev1.Event_FetchSession{FetchSession: &corev1.FetchSessionAction{SessionId: sid}},
		})
		for _, ef := range res.Effects {
			if rpc := ef.GetRpc(); rpc != nil && rpc.GetFetchSessionById() != nil && rpc.GetFetchSessionById().SessionId == sid {
				return rpc.OpId
			}
		}
		t.Fatalf("expected FetchSessionById RPC for %s", sid)
		return 0
	}

	// 1. Server returns empty WireSession with no error -> missing (LookupFailed=true, LookupErrorText=nil).
	opEmpty := fetchOp("s-missing")
	e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 50,
		Event: &corev1.Event_SessionByIdFetched{SessionByIdFetched: &corev1.SessionByIdFetchedEvent{OpId: opEmpty}},
	})
	snapMissing := e.SessionSnapshot("s-missing")
	if !snapMissing.LookupFailed || snapMissing.LookupErrorText != nil || snapMissing.Found {
		t.Fatalf("expected missing snapshot (LookupFailed=true, LookupErrorText=nil), got %+v", snapMissing)
	}

	// 2. Server returns a WireSession whose encrypted metadata is tampered -> read failure with error reason, NOT missing.
	metaBytes, _ := (&v1.Session{Id: "s-bad", Cwd: "/repo"}).Marshal()
	badEnv := sealWireObj(t, machine, dom, s1, sec, nil, metaBytes, devicesec.SessionMeta, machine.id, "s-bad", "", 1)
	badEnv.Ciphertext[0] ^= 0xff // tamper ciphertext so opening fails

	opBad := fetchOp("s-bad")
	_, stepBad := e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 60,
		Event: &corev1.Event_SessionByIdFetched{
			SessionByIdFetched: &corev1.SessionByIdFetchedEvent{
				OpId: opBad,
				WireSession: &v1.RemoteSession{
					SessionId: "s-bad",
					MachineId: machine.id,
					Meta:      badEnv,
				},
			},
		},
	})
	pumpSecurityEffects(t, e, phone, nowMs+60, stepBad)

	snapBad := e.SessionSnapshot("s-bad")
	if !snapBad.LookupFailed || snapBad.Found || snapBad.LookupErrorText == nil || snapBad.LookupErrorText.GetKey() != text.KeyErrorsInvalidCiphertextSignatureKeyOrContentVerificationFailed {
		t.Fatalf("expected decrypt failure to report LookupErrorText reason (retryable, not missing), got found=%v failed=%v err=%+v",
			snapBad.Found, snapBad.LookupFailed, snapBad.LookupErrorText)
	}

	// 3. Retrying FetchSessionAction clears the prior error and issues a fresh FetchSessionById RPC.
	opRetry := fetchOp("s-bad")
	if opRetry == 0 || opRetry == opBad {
		t.Fatalf("expected new retry FetchSessionById opID, got %d", opRetry)
	}
	snapRetrying := e.SessionSnapshot("s-bad")
	if !snapRetrying.LookingUp || snapRetrying.LookupFailed || snapRetrying.LookupErrorText != nil {
		t.Fatalf("expected retrying snapshot to clear previous failure, got %+v", snapRetrying)
	}
}

func TestProjectPrefsRecoveryHistoryUnavailableAllowsRebuild(t *testing.T) {
	const nowMs int64 = 1_700_000_000_000
	dom := devicesec.Digest("sync-recovery-prefs", []byte{1})
	oldPhone := newSyncTestActor(t, 31)
	newPhone := newSyncTestActor(t, 32)
	recKey := newSyncTestActor(t, 33)
	newRecKey := newSyncTestActor(t, 34)
	account := oldPhone.id

	sec0 := bytes.Repeat([]byte{0x11}, 32)
	u0, err := devicesec.NewGenesis(account, dom, oldPhone.member(devicesec.Client), [32]byte(recKey.edP), sec0, uint64(nowMs-2000), syncTestHPKE{})
	if err != nil {
		t.Fatal(err)
	}
	sb0, _ := u0.SigningBytes()
	u0.Signature = ed25519.Sign(oldPhone.ed, sb0)
	s0Raw, _ := u0.MarshalBinary()
	s0, _ := devicesec.ParseState(s0Raw)
	genHash, _ := s0.Hash()

	sec1 := bytes.Repeat([]byte{0x22}, 32)
	u1, err := devicesec.NewRecovery(s0, newPhone.member(devicesec.Client), []devicesec.Member{newPhone.member(devicesec.Client)}, sec1, [32]byte(newRecKey.edP), uint64(nowMs-1000), syncTestHPKE{})
	if err != nil {
		t.Fatal(err)
	}
	sb1, _ := u1.SigningBytes()
	u1.Signature = ed25519.Sign(newPhone.ed, sb1)
	s1Raw, _ := u1.MarshalBinary()
	s1, _ := devicesec.ParseState(s1Raw)
	stmt, err := devicesec.NewRecoveryStatement(s0, s1, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	stmtBytes, _ := stmt.SigningBytes()
	stmt.Signature = ed25519.Sign(recKey.ed, stmtBytes)
	stmtRaw, _ := stmt.MarshalBinary()

	leaseU, _ := devicesec.NewLease(s1, uint64(nowMs-500))
	leaseSB, _ := leaseU.SigningBytes()
	leaseU.Signature = ed25519.Sign(newPhone.ed, leaseSB)
	leaseRaw, _ := leaseU.MarshalBinary()

	e := NewEngine()
	e.HandleEvent(&corev1.Event{
		NowMs: nowMs,
		Event: &corev1.Event_EntropyProvided{EntropyProvided: &corev1.EntropyProvided{
			RandomBytes: bytes.Repeat([]byte{0x9c}, 256),
		}},
	})
	e.HandleEvent(&corev1.Event{
		NowMs: nowMs,
		Event: &corev1.Event_SecurityConfig{
			SecurityConfig: &corev1.SecurityConfigEvent{
				Account:      account,
				DeviceId:     newPhone.id,
				InlineCrypto: false,
			},
		},
	})
	initRes := applyTestSecurityRecord(e, nowMs, security.RecordUpdate{
		NetworkGeneration: 1,
		Account:           account,
		Domain:            dom[:],
		Genesis:           genHash[:],
		States:            [][]byte{s0Raw, s1Raw},
		RecoveryProofs:    [][]byte{nil, stmtRaw},
		Lease:             leaseRaw,
	})
	pumpSecurityEffects(t, e, newPhone, nowMs, initRes)
	e.HandleEvent(&corev1.Event{NowMs: nowMs, Event: &corev1.Event_SyncLifecycle{SyncLifecycle: &corev1.SyncLifecycleAction{Kind: corev1.SyncLifecycleKind_SYNC_LIFECYCLE_KIND_START}}})
	_, tokRes := e.HandleEvent(&corev1.Event{NowMs: nowMs, Event: &corev1.Event_TokenResult{TokenResult: &corev1.TokenResultEvent{OpId: e.activeTokenOpID, Token: "tok"}}})
	var legID uint64
	for _, ef := range tokRes.Effects {
		if os := ef.GetOpenStream(); os != nil {
			legID = os.LegId
		}
	}
	e.HandleEvent(&corev1.Event{NowMs: nowMs, Event: &corev1.Event_StreamReady{StreamReady: &corev1.StreamReadyEvent{LegId: legID}}})

	// Pre-recovery ProjectPrefs sealed under epoch 1 (oldPhone's secret).
	oldPrefsRaw, err := (&v1.ProjectPrefs{Schema: 1, Pinned: []*v1.ProjectPref{{Path: "/old"}}}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	preRecEnv := sealWireObj(t, oldPhone, dom, s0, sec0, nil, oldPrefsRaw, devicesec.Prefs, "", "", ProjectPrefsObjectID, 3)

	// 1. Loading pre-recovery ProjectPrefs must NOT mark prefs Broken.
	loadStep := e.Run(func(ctx *StepContext) {
		e.openWireDirectoryBatchTag(ctx, nil, nil, nil, []*v1.AccountPrefsObject{
			{ObjectId: ProjectPrefsObjectID, Revision: 3, Envelope: preRecEnv},
		}, dirBatchMeta{kind: dirKindPrefsLoad})
	})
	pumpSecurityEffects(t, e, newPhone, nowMs+10, loadStep)
	if !e.prefs.Loaded || e.prefs.Broken || e.prefs.Revision != 3 || e.prefs.Prefs == nil {
		t.Fatalf("expected recovery-unavailable prefs to load cleanly as rebuildable empty prefs, got %+v", e.prefs)
	}

	// 2. Saving a new pref via CAS must succeed by rebuilding from empty at ExpectedRevision=3.
	casStep := e.Run(func(ctx *StepContext) {
		e.EnqueuePrefsOp(ctx, &lists.FullPrefsOp{Kind: lists.PrefsOpPin, Path: "/proj/rebuilt"}, false, "/proj/rebuilt")
	})
	var getOp uint64
	for _, ef := range casStep.Effects {
		if rpc := ef.GetRpc(); rpc != nil && rpc.GetGetAccountPrefs() != nil {
			getOp = rpc.OpId
		}
	}
	if getOp == 0 {
		t.Fatal("expected GetAccountPrefs RPC for prefs CAS")
	}
	_, getRes := e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 20,
		Event: &corev1.Event_AccountPrefsFetched{
			AccountPrefsFetched: &corev1.AccountPrefsFetchedEvent{
				OpId:      getOp,
				WirePrefs: &v1.AccountPrefsObject{ObjectId: ProjectPrefsObjectID, Revision: 3, Envelope: preRecEnv},
			},
		},
	})
	casEffs, _ := pumpSecurityEffects(t, e, newPhone, nowMs+20, getRes)
	var putRpc *corev1.PutAccountPrefsRpc
	for _, ef := range casEffs {
		if rpc := ef.GetRpc(); rpc != nil && rpc.GetPutAccountPrefs() != nil {
			putRpc = rpc.GetPutAccountPrefs()
		}
	}
	if putRpc == nil || putRpc.ExpectedRevision != 3 || putRpc.SealedEnvelope == nil {
		t.Fatalf("expected rebuilt PutAccountPrefsRpc at ExpectedRevision=3, got %+v", putRpc)
	}

	// 3. A genuinely corrupted ciphertext in the current epoch must still be rejected as Broken.
	badEnv := sealWireObj(t, newPhone, dom, s1, sec1, nil, oldPrefsRaw, devicesec.Prefs, "", "", ProjectPrefsObjectID, 4)
	badEnv.Ciphertext[0] ^= 0xff
	e.prefsQueue = nil
	badStep := e.Run(func(ctx *StepContext) {
		e.EnqueuePrefsOp(ctx, &lists.FullPrefsOp{Kind: lists.PrefsOpPin, Path: "/proj/bad"}, false, "/proj/bad")
	})
	var badGetOp uint64
	for _, ef := range badStep.Effects {
		if rpc := ef.GetRpc(); rpc != nil && rpc.GetGetAccountPrefs() != nil {
			badGetOp = rpc.OpId
		}
	}
	_, badGetRes := e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 30,
		Event: &corev1.Event_AccountPrefsFetched{
			AccountPrefsFetched: &corev1.AccountPrefsFetchedEvent{
				OpId:      badGetOp,
				WirePrefs: &v1.AccountPrefsObject{ObjectId: ProjectPrefsObjectID, Revision: 4, Envelope: badEnv},
			},
		},
	})
	badEffs, _ := pumpSecurityEffects(t, e, newPhone, nowMs+30, badGetRes)
	for _, ef := range badEffs {
		if rpc := ef.GetRpc(); rpc != nil && rpc.GetPutAccountPrefs() != nil {
			t.Fatal("tampered ProjectPrefs must not be overwritten by CAS")
		}
	}
	if e.w.PrefsGlobalErrorText == nil || e.w.PrefsGlobalErrorText.GetKey() != text.KeyErrorsInvalidCiphertextSignatureKeyOrContentVerificationFailed {
		t.Fatalf("expected verification error on tampered prefs, got %+v", e.w.PrefsGlobalErrorText)
	}
}

func TestBuildAuthoritativeDevicesSeparatesTrustedAndRegistered(t *testing.T) {
	e := NewEngine()
	e.security.Loaded = true
	e.security.SelfDeviceID = "c-self"
	e.security.AuthorityDeviceID = "c-self"
	e.security.TrustedClients = map[string]bool{
		"c-self":        true,
		"c-unreg-phone": true,
	}
	e.security.TrustedMachines = map[string]bool{
		"m-reg":   true,
		"m-unreg": true,
	}

	opened := []decodedDevice{
		{DeviceID: "c-self", Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, CreatedMs: 100, LastSeenMs: 200, Registered: true},
		{DeviceID: "m-reg", Kind: v1.DeviceKind_DEVICE_KIND_MACHINE, CreatedMs: 100, LastSeenMs: 200, Registered: true},
	}
	auth := e.buildAuthoritativeDevices(opened)
	e.reconcileDecodedDevices(auth, nil, nil, true)

	sv := e.BuildSyncStateView()
	byID := make(map[string]*corev1.SyncDeviceView, len(sv.Devices))
	for _, d := range sv.Devices {
		byID[d.DeviceId] = d
	}
	for _, id := range []string{"c-self", "m-reg"} {
		d := byID[id]
		if d == nil || !d.Trusted || !d.Registered {
			t.Fatalf("expected registered member %s to have Trusted=true, Registered=true, got %+v", id, d)
		}
	}
	for _, id := range []string{"c-unreg-phone", "m-unreg"} {
		d := byID[id]
		if d == nil || !d.Trusted || d.Registered {
			t.Fatalf("expected unregistered candidate %s to have Trusted=true, Registered=false, got %+v", id, d)
		}
	}
}
