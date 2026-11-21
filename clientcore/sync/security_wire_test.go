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
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/gen/litewire"
	"github.com/Lewin671/remote-agent/clientcore/security"
	"github.com/Lewin671/remote-agent/clientcore/transport"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"github.com/Lewin671/remote-agent/shared/linkcore"
	"github.com/Lewin671/remote-agent/shared/peerwire"
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

func pumpSyncSecurityEffects(t *testing.T, e *Engine, actor syncTestActor, nowMs int64, initRes StepResult) ([]*corev1.Effect, []SettledCommand) {
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

	_, recRes := e.HandleEvent(&corev1.Event{
		NowMs: nowMs + 50,
		Event: &corev1.Event_SecurityRecord{
			SecurityRecord: &corev1.SecurityRecordEvent{
				NetworkGeneration: 1,
				Account:           phone.id,
				Domain:            dom[:],
				Genesis:           gen[:],
				States:            [][]byte{s0Raw, s1Raw},
				Lease:             leaseRaw,
			},
		},
	})
	pumpSyncSecurityEffects(t, e, phone, nowMs+50, recRes)
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
					Id:   machine.id,
					Online: true,
					Meta: sealWireObj(t, machine, dom, s1, sec, nil, mmRaw, devicesec.MachineMeta, machine.id, "", "", 1),
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
	pumpSyncSecurityEffects(t, e, phone, nowMs+80, listsRes)
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
	pageEffects, _ := pumpSyncSecurityEffects(t, e, phone, nowMs+100, pageRes)
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
	cmdEffs, _ := pumpSyncSecurityEffects(t, e, phone, nowMs+110, cmdStep)
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
	_, settled := pumpSyncSecurityEffects(t, e, phone, nowMs+120, outRes)
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
	badEffs, _ := pumpSyncSecurityEffects(t, e, phone, nowMs+121, badStep)
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
	_, badSettled := pumpSyncSecurityEffects(t, e, phone, nowMs+125, badOutRes)
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
	casEffs, _ := pumpSyncSecurityEffects(t, e, phone, nowMs+130, casGetRes)
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

	drainEffects(h.step(&corev1.Event{Event: &corev1.Event_SecurityRecord{
		SecurityRecord: &corev1.SecurityRecordEvent{
			NetworkGeneration: 1,
			Account:           h.account,
			Domain:            h.domain[:],
			Genesis:           gen[:],
			States:            [][]byte{s0Raw, s1Raw, s2Raw},
			Lease:             leaseRaw,
		},
	}}).Effects)
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
	pumpSyncSecurityEffects(t, h.eng, phone, h.now, listsRes)
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
	pumpSyncSecurityEffects(t, h.eng, phone, h.now, badRes)
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
	pumpSyncSecurityEffects(t, h.eng, phone, h.now, goodRes)
	if buf := h.eng.Buffer(sid); buf == nil || buf.PrefixLen() != 1 {
		t.Fatalf("expected wire direct event from trusted machine to ingest seq 1, got %v", buf)
	}

	// When secEngine is locked, CoordAuthorize triggers SyncSecurityEffect, and a failed SecurityRecordEvent closes the authorizing gate.
	authEffs := h.eng.Run(func(ctx *StepContext) {
		h.eng.emitCoordEffects(ctx, []transport.CoordEffect{transport.CoordAuthorize{}})
	}).Effects
	var authSyncOp uint64
	for _, eff := range authEffs {
		if ss := eff.GetSyncSecurity(); ss != nil {
			authSyncOp = ss.OpId
		}
	}
	if authSyncOp == 0 {
		t.Fatalf("expected CoordAuthorize with locked secEngine to emit SyncSecurityEffect")
	}
	h.step(&corev1.Event{
		Event: &corev1.Event_SecurityRecord{
			SecurityRecord: &corev1.SecurityRecordEvent{
				OpId:              authSyncOp,
				NetworkGeneration: 1,
				Error:             "sync failed",
			},
		},
	})
	if h.eng.activeSecOpID != 0 {
		t.Fatalf("expected failed SecurityRecordEvent to clear activeSecOpID, got %d", h.eng.activeSecOpID)
	}

	// Stale non-zero OpId must be ignored even when activeSecOpID == 0.
	staleRes := h.step(&corev1.Event{
		Event: &corev1.Event_SecurityRecord{
			SecurityRecord: &corev1.SecurityRecordEvent{
				OpId:    999999,
				Cleared: true,
			},
		},
	})
	if !h.eng.security.Loaded || !h.eng.security.IsMember || h.eng.secEngine.Snapshot().Cleared || len(staleRes.Effects) != 0 {
		t.Fatalf("expected stale SecurityRecordEvent with unexpected OpId to be ignored cleanly")
	}
}
