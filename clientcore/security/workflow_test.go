package security

import (
	"bytes"
	"compress/flate"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"slices"
	"strings"
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/entropy"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/text"
	stdv1 "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"google.golang.org/protobuf/proto"
)

type recordedSignCall struct {
	Kind corev1.SecurityKeyKind
	Slot string
	Msg  []byte
	Sig  []byte
}

type mockStoreEntry struct {
	rev  string
	data []byte
}

type mockStoreKey struct {
	bucket corev1.SecurityStoreBucket
	key    string
}

type fakeClientHarness struct {
	t     *testing.T
	relay *fakeRelay
	pool  *entropy.Pool
	eng   *Engine

	nowMs   int64
	entByte byte

	store         map[mockStoreKey]mockStoreEntry
	legacyCleared bool
	conflictOnce  bool
	storageReadErr string
	keyAgreeErr    string

	deviceEd ed25519.PrivateKey
	deviceX  *ecdh.PrivateKey
	candEd   map[string]ed25519.PrivateKey
	candX    map[string]*ecdh.PrivateKey
	ephemX   map[string]*ecdh.PrivateKey

	signCalls       []recordedSignCall
	actionResults   map[uint64]*corev1.SecurityWorkflowResultEffect
	statusUpdates   []*corev1.SecurityWorkflowStatus
	notifyOpened    map[uint64]*corev1.SecurityNotifyOpenedEffect
	tokenEvents     []*corev1.SecurityTokenUpdatedEffect
	pollTimerActive bool
	nextActionID    uint64
}

// testRelayVersion deliberately differs from sync.RelayVersion: the fake relay
// rejects entry requests that do not carry it, so hardcoded versions fail.
const testRelayVersion uint32 = 9017

func newFakeClientHarness(t *testing.T, relay *fakeRelay, tag byte) *fakeClientHarness {
	t.Helper()
	pool := &entropy.Pool{}
	eng := NewEngine(pool, testRelayVersion)
	h := &fakeClientHarness{
		t:             t,
		relay:         relay,
		pool:          pool,
		eng:           eng,
		nowMs:         relay.nowMs,
		entByte:       tag,
		store:         make(map[mockStoreKey]mockStoreEntry),
		candEd:        make(map[string]ed25519.PrivateKey),
		candX:         make(map[string]*ecdh.PrivateKey),
		ephemX:        make(map[string]*ecdh.PrivateKey),
		actionResults: make(map[uint64]*corev1.SecurityWorkflowResultEffect),
		notifyOpened:  make(map[uint64]*corev1.SecurityNotifyOpenedEffect),
	}
	eng.SetForeground(true)
	return h
}

func (h *fakeClientHarness) restart(server string, legacyCleared bool) {
	h.t.Helper()
	pool := &entropy.Pool{}
	eng := NewEngine(pool, testRelayVersion)
	h.pool = pool
	h.eng = eng
	h.legacyCleared = legacyCleared
	h.actionResults = make(map[uint64]*corev1.SecurityWorkflowResultEffect)
	h.statusUpdates = nil
	h.notifyOpened = make(map[uint64]*corev1.SecurityNotifyOpenedEffect)
	h.tokenEvents = nil
	h.pollTimerActive = false
	h.ephemX = make(map[string]*ecdh.PrivateKey)
	eng.Configure(&corev1.SecurityConfigEvent{
		InlineCrypto:  true,
		Server:        server,
		LegacyCleared: legacyCleared,
		Foreground:    true,
	})
}

func (h *fakeClientHarness) seedEntropy(n int) {
	buf := make([]byte, n)
	for i := range buf {
		h.entByte++
		buf[i] = h.entByte ^ byte(i*37+13)
	}
	h.pool.Append(buf)
}

func (h *fakeClientHarness) genRandomKey(seedByteLen int) []byte {
	h.entByte++
	buf := make([]byte, seedByteLen)
	_, _ = rand.Read(buf)
	return buf
}

func (h *fakeClientHarness) runAction(act *corev1.SecurityWorkflowActionEvent, devices ...DeviceOfferInfo) *corev1.SecurityWorkflowResultEffect {
	h.t.Helper()
	h.nextActionID++
	aid := h.nextActionID
	act.ActionId = aid
	h.seedEntropy(256)
	effs := h.eng.HandleWorkflowAction(h.nowMs, act, devices, nil)
	h.pump(effs)
	res := h.actionResults[aid]
	if res == nil {
		h.t.Fatalf("missing workflow result for action %d (%T)", aid, act.Action)
	}
	return res
}

func (h *fakeClientHarness) startAction(act *corev1.SecurityWorkflowActionEvent, devices ...DeviceOfferInfo) uint64 {
	h.t.Helper()
	h.nextActionID++
	aid := h.nextActionID
	act.ActionId = aid
	h.seedEntropy(256)
	effs := h.eng.HandleWorkflowAction(h.nowMs, act, devices, nil)
	h.pump(effs)
	return aid
}

func (h *fakeClientHarness) pumpPollTicks(maxTicks int, stopWhen func() bool) {
	h.t.Helper()
	for range maxTicks {
		if stopWhen() {
			return
		}
		if !h.pollTimerActive {
			return
		}
		h.pollTimerActive = false
		h.nowMs += pairPollIntervalMs
		h.relay.nowMs = h.nowMs
		h.pump(h.eng.HandleSecurityPollTick(h.nowMs))
	}
}

func (h *fakeClientHarness) pump(effs []*corev1.Effect) {
	queue := slices.Clone(effs)
	for len(queue) > 0 {
		eff := queue[0]
		queue = queue[1:]
		if eff == nil {
			continue
		}
		switch k := eff.Effect.(type) {
		case *corev1.Effect_RequestEntropy:
			need := max(int(k.RequestEntropy.MinRandomBytes), 128)
			h.seedEntropy(need)
			queue = append(queue, h.eng.FlushEntropy()...)

		case *corev1.Effect_SecurityKeyGenerate:
			kg := k.SecurityKeyGenerate
			var pub []byte
			switch kg.KeyKind {
			case corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_ED, corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED:
				priv := ed25519.NewKeyFromSeed(h.genRandomKey(32))
				pub = slices.Clone(priv.Public().(ed25519.PublicKey))
				if kg.KeyKind == corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_ED && kg.Slot == "" {
					h.deviceEd = priv
				} else {
					h.candEd[kg.Slot] = priv
				}
			case corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_X, corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_X, corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X:
				priv, err := ecdh.X25519().NewPrivateKey(h.genRandomKey(32))
				if err != nil {
					h.t.Fatal(err)
				}
				pub = slices.Clone(priv.PublicKey().Bytes())
				switch kg.KeyKind {
				case corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_X:
					if kg.Slot == "" {
						h.deviceX = priv
					} else {
						h.candX[kg.Slot] = priv
					}
				case corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_X:
					h.candX[kg.Slot] = priv
				case corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X:
					h.ephemX[kg.Slot] = priv
				}
			}
			queue = append(queue, h.eng.HandleKeyGenerated(h.nowMs, &corev1.SecurityKeyGeneratedEvent{
				OpId:      kg.OpId,
				PublicKey: pub,
			})...)

		case *corev1.Effect_SecurityKeySign:
			ks := k.SecurityKeySign
			var priv ed25519.PrivateKey
			if ks.KeyKind == corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_ED && ks.Slot == "" {
				priv = h.deviceEd
			} else {
				priv = h.candEd[ks.Slot]
			}
			if priv == nil {
				queue = append(queue, h.eng.HandleKeySigned(h.nowMs, &corev1.SecurityKeySignedEvent{
					OpId:  ks.OpId,
					Error: "missing signing key",
				})...)
				continue
			}
			sig := ed25519.Sign(priv, ks.Message)
			h.signCalls = append(h.signCalls, recordedSignCall{
				Kind: ks.KeyKind,
				Slot: ks.Slot,
				Msg:  slices.Clone(ks.Message),
				Sig:  slices.Clone(sig),
			})
			queue = append(queue, h.eng.HandleKeySigned(h.nowMs, &corev1.SecurityKeySignedEvent{
				OpId:      ks.OpId,
				Signature: sig,
			})...)

		case *corev1.Effect_SecurityKeyAgree:
			ka := k.SecurityKeyAgree
			if h.keyAgreeErr != "" {
				queue = append(queue, h.eng.HandleKeyAgreed(h.nowMs, &corev1.SecurityKeyAgreedEvent{
					OpId:  ka.OpId,
					Error: h.keyAgreeErr,
				})...)
				continue
			}
			var priv *ecdh.PrivateKey
			switch ka.KeyKind {
			case corev1.SecurityKeyKind_SECURITY_KEY_KIND_DEVICE_X:
				priv = h.deviceX
			case corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_X:
				priv = h.candX[ka.Slot]
			case corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X:
				priv = h.ephemX[ka.Slot]
			}
			if priv == nil {
				queue = append(queue, h.eng.HandleKeyAgreed(h.nowMs, &corev1.SecurityKeyAgreedEvent{
					OpId:  ka.OpId,
					Error: "missing agree key",
				})...)
				continue
			}
			peer, err := ecdh.X25519().NewPublicKey(ka.PeerPublic)
			if err != nil {
				queue = append(queue, h.eng.HandleKeyAgreed(h.nowMs, &corev1.SecurityKeyAgreedEvent{
					OpId:  ka.OpId,
					Error: err.Error(),
				})...)
				continue
			}
			shared, err := priv.ECDH(peer)
			if err != nil {
				queue = append(queue, h.eng.HandleKeyAgreed(h.nowMs, &corev1.SecurityKeyAgreedEvent{
					OpId:  ka.OpId,
					Error: err.Error(),
				})...)
				continue
			}
			queue = append(queue, h.eng.HandleKeyAgreed(h.nowMs, &corev1.SecurityKeyAgreedEvent{
				OpId:   ka.OpId,
				Shared: shared,
			})...)

		case *corev1.Effect_SecurityKeyEncapsulate:
			ke := k.SecurityKeyEncapsulate
			var pairs []*corev1.SecurityEncapsulatedPair
			for _, rec := range ke.Recipients {
				eph, err := ecdh.X25519().GenerateKey(rand.Reader)
				if err != nil {
					h.t.Fatal(err)
				}
				peer, err := ecdh.X25519().NewPublicKey(rec.XPublic)
				if err != nil {
					h.t.Fatal(err)
				}
				dh, err := eph.ECDH(peer)
				if err != nil {
					h.t.Fatal(err)
				}
				pairs = append(pairs, &corev1.SecurityEncapsulatedPair{
					MemberId: rec.MemberId,
					Enc:      slices.Clone(eph.PublicKey().Bytes()),
					Dh:       dh,
				})
			}
			queue = append(queue, h.eng.HandleKeyEncapsulated(h.nowMs, &corev1.SecurityKeyEncapsulatedEvent{
				OpId:  ke.OpId,
				Pairs: pairs,
			})...)

		case *corev1.Effect_SecurityKeyDiscard:
			kd := k.SecurityKeyDiscard
			switch kd.KeyKind {
			case corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_ED:
				delete(h.candEd, kd.Slot)
			case corev1.SecurityKeyKind_SECURITY_KEY_KIND_CANDIDATE_X:
				delete(h.candX, kd.Slot)
			case corev1.SecurityKeyKind_SECURITY_KEY_KIND_EPHEMERAL_X:
				delete(h.ephemX, kd.Slot)
			}

		case *corev1.Effect_SecurityStorage:
			queue = append(queue, h.handleStorageEffect(k.SecurityStorage)...)

		case *corev1.Effect_SecurityRpc:
			sr := k.SecurityRpc
			respBytes, rErr := h.relay.call(sr.Method, sr.AuthToken, sr.RequestBytes)
			ev := &corev1.SecurityRpcResultEvent{
				OpId:          sr.OpId,
				ResponseBytes: respBytes,
			}
			if rErr != nil {
				ev.ConnectCode = rErr.code
				ev.Error = rErr.msg
				ev.NetworkError = rErr.network
			}
			queue = append(queue, h.eng.HandleRpcResult(h.nowMs, ev)...)

		case *corev1.Effect_ScheduleTimer:
			if k.ScheduleTimer.Kind == corev1.TimerKind_TIMER_KIND_SECURITY_POLL {
				h.pollTimerActive = true
			}

		case *corev1.Effect_CancelTimer:
			if k.CancelTimer.Kind == corev1.TimerKind_TIMER_KIND_SECURITY_POLL {
				h.pollTimerActive = false
			}

		case *corev1.Effect_SecurityTokenUpdated:
			h.tokenEvents = append(h.tokenEvents, k.SecurityTokenUpdated)

		case *corev1.Effect_SecurityWorkflowResult:
			r := k.SecurityWorkflowResult
			h.actionResults[r.ActionId] = r

		case *corev1.Effect_SecurityStatusChanged:
			h.statusUpdates = append(h.statusUpdates, k.SecurityStatusChanged.Status)

		case *corev1.Effect_SecurityNotifyOpened:
			no := k.SecurityNotifyOpened
			h.notifyOpened[no.OpId] = no
		}
	}
}

func (h *fakeClientHarness) handleStorageEffect(st *corev1.SecurityStorageEffect) []*corev1.Effect {
	ev := &corev1.SecurityStorageResultEvent{
		OpId:          st.OpId,
		LegacyCleared: h.legacyCleared,
	}
	if h.deviceEd != nil {
		ev.HasDeviceEd = true
		ev.DeviceEdPub = slices.Clone(h.deviceEd.Public().(ed25519.PublicKey))
	}
	if h.deviceX != nil {
		ev.HasDeviceX = true
		ev.DeviceXPub = slices.Clone(h.deviceX.PublicKey().Bytes())
	}

	switch st.Op {
	case corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ:
		if ent, ok := h.store[mockStoreKey{st.Bucket, st.Key}]; ok {
			ev.Found = true
			ev.Revision = ent.rev
			ev.Data = slices.Clone(ent.data)
		}
	case corev1.SecurityStorageOp_SECURITY_STORAGE_OP_READ_BOOTSTRAP:
		if h.storageReadErr != "" {
			ev.Error = h.storageReadErr
			break
		}
		for sk, ent := range h.store {
			if st.Bucket != corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_UNSPECIFIED && sk.bucket != st.Bucket {
				continue
			}
			ev.Entries = append(ev.Entries, &corev1.SecurityStorageEntry{
				Bucket:   sk.bucket,
				Key:      sk.key,
				Revision: ent.rev,
				Data:     slices.Clone(ent.data),
			})
		}
	case corev1.SecurityStorageOp_SECURITY_STORAGE_OP_WRITE_CAS:
		if h.conflictOnce {
			h.conflictOnce = false
			ev.Conflict = true
			break
		}
		sk := mockStoreKey{st.Bucket, st.Key}
		cur, exists := h.store[sk]
		if st.CreateOnly && exists {
			ev.Conflict = true
			break
		}
		if st.ExpectedRevision != "" && (!exists || cur.rev != st.ExpectedRevision) {
			ev.Conflict = true
			break
		}
		h.store[sk] = mockStoreEntry{rev: st.NextRevision, data: slices.Clone(st.Data)}
		ev.Revision = st.NextRevision
	case corev1.SecurityStorageOp_SECURITY_STORAGE_OP_DELETE_CAS:
		sk := mockStoreKey{st.Bucket, st.Key}
		cur, exists := h.store[sk]
		if st.ExpectedRevision != "" && exists && cur.rev != st.ExpectedRevision {
			ev.Conflict = true
			break
		}
		delete(h.store, sk)
	case corev1.SecurityStorageOp_SECURITY_STORAGE_OP_BATCH_COMMIT:
		if h.conflictOnce {
			h.conflictOnce = false
			ev.Conflict = true
			break
		}
		for _, w := range st.Batch {
			sk := mockStoreKey{w.Bucket, w.Key}
			cur, exists := h.store[sk]
			if w.CreateOnly && exists {
				ev.Conflict = true
				break
			}
			if w.ExpectedRevision != "" && (!exists || cur.rev != w.ExpectedRevision) {
				ev.Conflict = true
				break
			}
		}
		if ev.Conflict {
			break
		}
		if st.PromoteEdSlot != "" {
			priv, ok := h.candEd[st.PromoteEdSlot]
			if !ok {
				ev.Error = "missing promote ed slot"
				break
			}
			h.deviceEd = priv
			delete(h.candEd, st.PromoteEdSlot)
		}
		if st.PromoteXSlot != "" {
			priv, ok := h.candX[st.PromoteXSlot]
			if !ok {
				ev.Error = "missing promote x slot"
				break
			}
			h.deviceX = priv
			delete(h.candX, st.PromoteXSlot)
		}
		for _, w := range st.Batch {
			sk := mockStoreKey{w.Bucket, w.Key}
			if w.Remove {
				delete(h.store, sk)
			} else {
				h.store[sk] = mockStoreEntry{rev: w.NextRevision, data: slices.Clone(w.Data)}
			}
		}
		for _, slot := range st.DiscardSlots {
			delete(h.candEd, slot)
			delete(h.candX, slot)
			delete(h.ephemX, slot)
		}
		if h.deviceEd != nil {
			ev.HasDeviceEd = true
			ev.DeviceEdPub = slices.Clone(h.deviceEd.Public().(ed25519.PublicKey))
		}
		if h.deviceX != nil {
			ev.HasDeviceX = true
			ev.DeviceXPub = slices.Clone(h.deviceX.PublicKey().Bytes())
		}
	case corev1.SecurityStorageOp_SECURITY_STORAGE_OP_CLEAR_ALL:
		clear(h.store)
		h.deviceEd = nil
		h.deviceX = nil
		clear(h.candEd)
		clear(h.candX)
		clear(h.ephemX)
		ev.HasDeviceEd = false
		ev.HasDeviceX = false
		ev.DeviceEdPub = nil
		ev.DeviceXPub = nil
	}
	return h.eng.HandleStorageResult(h.nowMs, ev)
}

func TestWorkflowCreationPoWCrashAndRotation(t *testing.T) {
	relay := newFakeRelay(1_700_000_000_000)
	relay.powBits = 4

	h := newFakeClientHarness(t, relay, 0x10)
	boot := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	if !boot.Ok || boot.Status.Bootstrap != corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_EMPTY {
		t.Fatalf("expected EMPTY bootstrap, got %+v", boot)
	}
	if h.eng.Locked() {
		t.Fatal("Demo Mode invariant violated: EMPTY bootstrap must NOT lock securityLocked")
	}

	prep := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareCreation{
			PrepareCreation: &corev1.SecurityPrepareCreationAction{
				Server:           "https://relay.example.com",
				StoragePersisted: true,
			},
		},
	})
	if !prep.Ok || prep.RecoveryCode == "" {
		t.Fatalf("prepareCreation failed: %+v", prep)
	}

	// Verify discard unsigned creation draft before continuing
	h2 := newFakeClientHarness(t, relay, 0x11)
	_ = h2.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	prep2 := h2.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareCreation{
			PrepareCreation: &corev1.SecurityPrepareCreationAction{Server: "https://relay.example.com"},
		},
	})
	if !prep2.Ok || len(h2.candEd) != 1 || len(h2.candX) != 1 {
		t.Fatalf("expected candidate slots generated, got ed=%d x=%d", len(h2.candEd), len(h2.candX))
	}
	disc := h2.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_DiscardPendingOp{DiscardPendingOp: &corev1.SecurityBoolAction{}},
	})
	if !disc.Ok || len(h2.candEd) != 0 || len(h2.candX) != 0 || disc.Status.Bootstrap != corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_EMPTY {
		t.Fatalf("expected discard to clean draft and slots, got %+v", disc)
	}

	// Crash before code saved -> restart -> ContinueCreation -> Adopt without code
	h.restart("https://relay.example.com", false)
	boot = h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	if boot.Status.Bootstrap != corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_PENDING_CREATE {
		t.Fatalf("expected PENDING_CREATE on restart, got %v", boot.Status.Bootstrap)
	}

	cont := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ContinueCreation{
			ContinueCreation: &corev1.SecurityStringAction{Value: ""},
		},
	})
	if !cont.Ok || !cont.NeedsRecoveryRotation || cont.CodeSaved {
		t.Fatalf("expected NeedsRecoveryRotation when adopting without saved code, got %+v", cont)
	}

	// Crash right after PUBLISHED, before adopt -> restart -> AdoptCreationWithoutCode
	h.restart("https://relay.example.com", false)
	_ = h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	adopt := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_AdoptCreationWithoutCode{AdoptCreationWithoutCode: &corev1.SecurityActionEmpty{}},
	})
	if !adopt.Ok || adopt.AccountAdopted == nil || !adopt.AccountAdopted.NeedsRecoveryRotation {
		t.Fatalf("expected AccountAdopted with NeedsRecoveryRotation, got %+v", adopt)
	}
	if adopt.Status.Bootstrap != corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_READY || !adopt.Status.IsAuthority || !adopt.Status.NeedsRecoveryCodeRotation {
		t.Fatalf("unexpected post-adopt status: %+v", adopt.Status)
	}
	if len(h.candEd) != 0 || len(h.candX) != 0 || h.deviceEd == nil || h.deviceX == nil {
		t.Fatal("expected candidate slots promoted and cleared")
	}

	var sawLoginSign, sawGenesisSign, sawLeaseSign bool
	for _, sc := range h.signCalls {
		if bytes.HasPrefix(sc.Msg, []byte(loginContext)) {
			sawLoginSign = true
		}
		if sb, err := h.eng.head.SigningBytes(); err == nil && bytes.Equal(sc.Msg, sb) {
			sawGenesisSign = true
			if !devicesec.VerifyEd25519(h.deviceEd.Public().(ed25519.PublicKey), sb, h.eng.head.Signature) {
				t.Fatal("golden genesis signature failed devicesec.VerifyEd25519")
			}
		}
		if lb, err := h.eng.lease.SigningBytes(); err == nil && bytes.Equal(sc.Msg, lb) {
			sawLeaseSign = true
			if !devicesec.VerifyEd25519(h.deviceEd.Public().(ed25519.PublicKey), lb, h.eng.lease.Signature) {
				t.Fatal("golden lease signature failed devicesec.VerifyEd25519")
			}
		}
	}
	if !sawLoginSign || !sawGenesisSign || !sawLeaseSign {
		t.Fatalf("missing expected KeySign golden byte observations: login=%v genesis=%v lease=%v", sawLoginSign, sawGenesisSign, sawLeaseSign)
	}

	// Rotate recovery code -> clears NeedsRecoveryCodeRotation
	rotPrep := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareRecoveryRotation{PrepareRecoveryRotation: &corev1.SecurityActionEmpty{}},
	})
	if !rotPrep.Ok || rotPrep.RecoveryCode == "" {
		t.Fatalf("prepareRecoveryRotation failed: %+v", rotPrep)
	}
	rotDone := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ConfirmRecoveryRotation{ConfirmRecoveryRotation: &corev1.SecurityActionEmpty{}},
	})
	if !rotDone.Ok || rotDone.Status.NeedsRecoveryCodeRotation {
		t.Fatalf("expected NeedsRecoveryCodeRotation cleared after ConfirmRecoveryRotation, got %+v", rotDone)
	}
}

func TestWorkflowPairingCrashRepairAndFallback(t *testing.T) {
	relay := newFakeRelay(1_700_000_000_000)

	a := newFakeClientHarness(t, relay, 0x20)
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareCreation{
			PrepareCreation: &corev1.SecurityPrepareCreationAction{Server: "https://relay.example.com"},
		},
	})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ConfirmCreationCodeSaved{ConfirmCreationCodeSaved: &corev1.SecurityActionEmpty{}},
	})
	cont := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ContinueCreation{ContinueCreation: &corev1.SecurityStringAction{}},
	})
	if !cont.Ok || cont.Status.NeedsRecoveryCodeRotation || !cont.Status.HasAccountPublicProfile {
		t.Fatalf("expected clean account A, got %+v", cont)
	}

	startP := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartPairing{
			StartPairing: &corev1.SecurityStartPairingAction{WebOrigin: "https://web.example.com"},
		},
	})
	if !startP.Ok || startP.Pairing == nil || startP.Pairing.Url == "" {
		t.Fatalf("StartPairing failed: %+v", startP)
	}
	pid := startP.Pairing.PairingId

	// Test commitHint mismatch rejection
	badJoiner := newFakeClientHarness(t, relay, 0x30)
	_ = badJoiner.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	badRes := badJoiner.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartJoinPairing{
			StartJoinPairing: &corev1.SecurityStartJoinPairingAction{
				Server:     "https://relay.example.com",
				Code:       startP.Pairing.Code,
				CommitHint: bytes.Repeat([]byte{0x99}, 32),
			},
		},
	})
	if badRes.Ok {
		t.Fatal("expected commitHint mismatch to fail")
	}

	// Real Joiner B via pair URL link
	b := newFakeClientHarness(t, relay, 0x40)
	_ = b.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	joinAID := b.startAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartJoinPairing{
			StartJoinPairing: &corev1.SecurityStartJoinPairingAction{RawLink: startP.Pairing.Url},
		},
	})

	pollA := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PollPairing{
			PollPairing: &corev1.SecurityStringAction{Value: pid},
		},
	})
	if !pollA.Ok || !pollA.Pairing.Joined || len(pollA.Pairing.SasEmoji) != 4 {
		t.Fatalf("expected PollPairing joined with 4 SAS emoji, got %+v", pollA)
	}

	b.pumpPollTicks(5, func() bool { return b.actionResults[joinAID] != nil })
	joinRes := b.actionResults[joinAID]
	if joinRes == nil || !joinRes.Ok || joinRes.Pairing == nil || !slices.Equal(joinRes.Pairing.SasEmoji, pollA.Pairing.SasEmoji) {
		t.Fatalf("joiner SAS mismatch or failure: joinRes=%+v initSAS=%v", joinRes, pollA.Pairing.SasEmoji)
	}

	confirmAID := b.startAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ConfirmJoinSas{
			ConfirmJoinSas: &corev1.SecurityStringAction{Value: pid},
		},
	})
	appRes := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ApprovePairing{
			ApprovePairing: &corev1.SecurityApprovePairingAction{PairingId: pid, SasConfirmed: true},
		},
	})
	if !appRes.Ok || appRes.Pairing == nil || !appRes.Pairing.Approved {
		t.Fatalf("ApprovePairing failed: %+v", appRes)
	}

	var sawApprovalSign bool
	for _, sc := range a.signCalls {
		if bytes.HasPrefix(sc.Msg, []byte(PairApprovalContext)) {
			sawApprovalSign = true
			ap, err := VerifyPairApproval(sc.Msg[len(PairApprovalContext):], sc.Sig)
			if err != nil || ap.PairingId != pid {
				t.Fatalf("invalid PairApproval golden bytes: %v", err)
			}
		}
	}
	if !sawApprovalSign {
		t.Fatal("expected PairApprovalContext KeySign call on initiator")
	}

	// Inject transient failure on REGISTER_DEVICE -> persist REGISTRATION_INTENT -> crash & resume!
	relay.failNextMethod = corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_REGISTER_DEVICE
	relay.failNextCode = connectCodeUnavailable
	b.pumpPollTicks(5, func() bool { return b.actionResults[confirmAID] != nil })
	if b.actionResults[confirmAID] == nil || b.actionResults[confirmAID].Ok {
		t.Fatalf("expected transient REGISTER_DEVICE failure before resume, got %+v", b.actionResults[confirmAID])
	}
	b.restart("https://relay.example.com", false)
	bootB := b.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	if bootB.Status.Bootstrap != corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_PENDING_REGISTRATION {
		t.Fatalf("expected PENDING_REGISTRATION after crash before RegisterDevice, got %v", bootB.Status.Bootstrap)
	}
	resB := b.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ResumePendingOp{ResumePendingOp: &corev1.SecurityActionEmpty{}},
	})
	if !resB.Ok || resB.AccountAdopted == nil || resB.Status.Bootstrap != corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_READY || resB.Status.IsAuthority {
		t.Fatalf("expected B adopted as non-authority member, got %+v", resB)
	}
	bDeviceID := b.eng.selfDeviceID

	finA := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_FinishPairingDelivery{
			FinishPairingDelivery: &corev1.SecurityStringAction{Value: pid},
		},
	})
	if !finA.Ok || !finA.Pairing.Delivered {
		t.Fatalf("expected initiator delivery confirmed, got %+v", finA)
	}

	// Remove B and check B becomes REMOVED on sync
	remB := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_RemoveDevice{
			RemoveDevice: &corev1.SecurityRemoveDeviceAction{DeviceId: bDeviceID},
		},
	})
	if !remB.Ok {
		t.Fatalf("RemoveDevice failed: %+v", remB)
	}
	syncB := b.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_SyncNow{SyncNow: &corev1.SecuritySyncNowAction{Touch: true}},
	})
	if syncB.Ok || syncB.StructuredErrorCode != corev1.SecurityErrorCode_SECURITY_ERROR_CODE_PERMISSION_DENIED {
		t.Fatalf("expected B sync permission_denied after removal, got %+v", syncB)
	}
	if b.eng.BuildWorkflowStatus().Bootstrap != corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_READY {
		t.Fatalf("expected B to keep local pinned record on server denial alone, got %+v", b.eng.BuildWorkflowStatus())
	}
	retB := b.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_CheckRetainedDenied{CheckRetainedDenied: &corev1.SecurityActionEmpty{}},
	})
	if !retB.Ok || !retB.Flag {
		t.Fatalf("expected B CheckRetainedDenied flag=true after two PermissionDenied logins, got %+v", retB)
	}

	// Pair C and verify ExistingRepair=true reuses DEVICE_ED while rotating DEVICE_X
	p2 := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartPairing{StartPairing: &corev1.SecurityStartPairingAction{}},
	})
	c := newFakeClientHarness(t, relay, 0x50)
	_ = c.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	cJoin := c.startAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartJoinPairing{
			StartJoinPairing: &corev1.SecurityStartJoinPairingAction{Server: "https://relay.example.com", Code: p2.Pairing.Code},
		},
	})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PollPairing{PollPairing: &corev1.SecurityStringAction{Value: p2.Pairing.PairingId}},
	})
	c.pumpPollTicks(5, func() bool { return c.actionResults[cJoin] != nil })
	cConf := c.startAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ConfirmJoinSas{ConfirmJoinSas: &corev1.SecurityStringAction{Value: p2.Pairing.PairingId}},
	})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ApprovePairing{
			ApprovePairing: &corev1.SecurityApprovePairingAction{PairingId: p2.Pairing.PairingId, SasConfirmed: true},
		},
	})
	c.pumpPollTicks(5, func() bool { return c.actionResults[cConf] != nil })
	if c.actionResults[cConf] == nil || !c.actionResults[cConf].Ok {
		t.Fatalf("expected C adopted, got %+v", c.actionResults[cConf])
	}
	if finP2 := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_FinishPairingDelivery{FinishPairingDelivery: &corev1.SecurityStringAction{Value: p2.Pairing.PairingId}},
	}); !finP2.Ok {
		t.Fatalf("FinishPairingDelivery p2 failed: %+v", finP2)
	}

	cOldEd := slices.Clone(c.deviceEd.Public().(ed25519.PublicKey))
	cOldX := slices.Clone(c.deviceX.PublicKey().Bytes())

	p3 := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartPairing{StartPairing: &corev1.SecurityStartPairingAction{}},
	})
	if !p3.Ok || p3.Pairing == nil {
		t.Fatalf("StartPairing p3 failed: %+v", p3)
	}
	c.eng.wf.record.Member = false
	cRepJoin := c.startAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartJoinPairing{
			StartJoinPairing: &corev1.SecurityStartJoinPairingAction{
				Server:         "https://relay.example.com",
				Code:           p3.Pairing.Code,
				ExistingRepair: true,
				ExistingServer: "https://relay.example.com",
			},
		},
	})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PollPairing{PollPairing: &corev1.SecurityStringAction{Value: p3.Pairing.PairingId}},
	})
	c.pumpPollTicks(5, func() bool { return c.actionResults[cRepJoin] != nil })
	cRepConf := c.startAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ConfirmJoinSas{ConfirmJoinSas: &corev1.SecurityStringAction{Value: p3.Pairing.PairingId}},
	})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ApprovePairing{
			ApprovePairing: &corev1.SecurityApprovePairingAction{PairingId: p3.Pairing.PairingId, SasConfirmed: true},
		},
	})
	c.pumpPollTicks(5, func() bool { return c.actionResults[cRepConf] != nil })
	if c.actionResults[cRepConf] == nil || !c.actionResults[cRepConf].Ok {
		t.Fatalf("ExistingRepair failed: %+v", c.actionResults[cRepConf])
	}
	if finP3 := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_FinishPairingDelivery{FinishPairingDelivery: &corev1.SecurityStringAction{Value: p3.Pairing.PairingId}},
	}); !finP3.Ok {
		t.Fatalf("FinishPairingDelivery p3 failed: %+v", finP3)
	}
	if !bytes.Equal(c.deviceEd.Public().(ed25519.PublicKey), cOldEd) {
		t.Fatal("ExistingRepair must reuse DEVICE_ED")
	}
	if bytes.Equal(c.deviceX.PublicKey().Bytes(), cOldX) {
		t.Fatal("ExistingRepair must rotate DEVICE_X")
	}

	// Test APPROVE_PAIRING NotFound fallback -> ListDevices confirms candidate already registered -> succeeds!
	p4 := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartPairing{StartPairing: &corev1.SecurityStartPairingAction{}},
	})
	d := newFakeClientHarness(t, relay, 0x60)
	_ = d.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	dJoin := d.startAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartJoinPairing{
			StartJoinPairing: &corev1.SecurityStartJoinPairingAction{Server: "https://relay.example.com", Code: p4.Pairing.Code},
		},
	})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PollPairing{PollPairing: &corev1.SecurityStringAction{Value: p4.Pairing.PairingId}},
	})
	d.pumpPollTicks(5, func() bool { return d.actionResults[dJoin] != nil })
	candEdPub := d.eng.wf.joinPairings[p4.Pairing.PairingId].edPublic
	candID := devicesec.ID(candEdPub)
	relay.mu.Lock()
	relay.devices[candID] = &v1.Device{Id: candID, Kind: v1.DeviceKind_DEVICE_KIND_CLIENT}
	relay.devicePubs[candID] = slices.Clone(candEdPub)
	relay.deviceAcct[candID] = a.eng.pin.Account
	relay.forceApproveNotFound = true
	relay.mu.Unlock()
	appFallback := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ApprovePairing{
			ApprovePairing: &corev1.SecurityApprovePairingAction{PairingId: p4.Pairing.PairingId, SasConfirmed: true},
		},
	})
	if !appFallback.Ok || a.eng.wf.pendingOp != nil {
		t.Fatalf("expected ApprovePairing NotFound + ListDevices fallback to succeed, got %+v", appFallback)
	}

	// Verify bounded deflate helper used for COMPRESSION_DEFLATE PairingPayload envelopes
	var deflateBuf bytes.Buffer
	zw, _ := flate.NewWriter(&deflateBuf, flate.DefaultCompression)
	_, _ = zw.Write([]byte("hello-pairing"))
	_ = zw.Close()
	inf, err := devicesec.InflateBounded(deflateBuf.Bytes(), maxStoreBlobBytes)
	if err != nil || string(inf) != "hello-pairing" {
		t.Fatalf("expected InflateBounded to inflate deflate payload, err=%v got=%q", err, inf)
	}
}

func TestWorkflowLeaseRenewalClockSkewLegacyAndNotify(t *testing.T) {
	relay := newFakeRelay(1_700_000_000_000)
	h := newFakeClientHarness(t, relay, 0x70)

	// Legacy cleared notice surfaced on bootstrap & cleared on DismissUpgradeNotice
	h.legacyCleared = true
	boot := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	if !boot.Ok || !boot.Status.LegacyRepairNotice {
		t.Fatalf("expected LegacyRepairNotice on bootstrap, got %+v", boot.Status)
	}
	dis := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_DismissUpgradeNotice{DismissUpgradeNotice: &corev1.SecurityActionEmpty{}},
	})
	if !dis.Ok || dis.Status.LegacyRepairNotice {
		t.Fatalf("expected LegacyRepairNotice cleared, got %+v", dis.Status)
	}

	_ = h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareCreation{
			PrepareCreation: &corev1.SecurityPrepareCreationAction{Server: "https://relay.example.com"},
		},
	})
	_ = h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ConfirmCreationCodeSaved{ConfirmCreationCodeSaved: &corev1.SecurityActionEmpty{}},
	})
	_ = h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ContinueCreation{ContinueCreation: &corev1.SecurityStringAction{}},
	})

	origIssued := h.eng.lease.IssuedAt

	// +13h: remaining 59h > 24h -> NO auto-renewal
	h.nowMs += 13 * 60 * 60 * 1000
	relay.nowMs = h.nowMs
	_ = h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_SyncNow{SyncNow: &corev1.SecuritySyncNowAction{Touch: true}},
	})
	if h.eng.lease.IssuedAt != origIssued {
		t.Fatal("should NOT renew lease when >24h remaining")
	}

	// +50h total: remaining 22h < 24h AND elapsed 50h >= 12h -> AUTO-RENEW!
	h.nowMs += 37 * 60 * 60 * 1000
	relay.nowMs = h.nowMs
	_ = h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_SyncNow{SyncNow: &corev1.SecuritySyncNowAction{Touch: true}},
	})
	if h.eng.lease.IssuedAt <= origIssued {
		t.Fatalf("expected auto lease renewal when <24h remaining & >=12h elapsed: orig=%d cur=%d", origIssued, h.eng.lease.IssuedAt)
	}

	// Floor rollback protection: wall clock jumping backward never regresses effectiveNow below FloorMs!
	savedNow := h.nowMs
	h.nowMs = int64(origIssued) - 1_000_000_000
	if eff := h.eng.effectiveNow(h.nowMs); eff < uint64(h.eng.wf.record.FloorMs) {
		t.Fatalf("floor rollback violated: eff=%d floor=%d", eff, h.eng.wf.record.FloorMs)
	}
	// Clock skew detection: when local floor+wall clock lags head/lease IssuedAt by > MaxClockSkew
	h.eng.floorMs = int64(origIssued) - 1_000_000_000
	h.eng.lastNowMs = int64(origIssued) - 1_000_000_000
	st := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_GetStatus{GetStatus: &corev1.SecurityActionEmpty{}},
	})
	if !st.Status.ClockSkewed || st.Status.Bootstrap != corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_CLOCK_SKEWED {
		t.Fatalf("expected CLOCK_SKEWED when wall clock jumps behind floor/head, got %+v", st.Status)
	}
	h.nowMs = savedNow
	h.eng.touchNow(savedNow)
	if h.eng.floorMs < savedNow {
		h.eng.floorMs = savedNow
	}

	// CompleteAccountPublicProfile persists across restart
	h.eng.wf.record.AccountPublicKey = nil
	clear(h.eng.wf.publicProfiles)
	rot := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareRecoveryRotation{PrepareRecoveryRotation: &corev1.SecurityActionEmpty{}},
	})
	_ = h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ConfirmRecoveryRotation{ConfirmRecoveryRotation: &corev1.SecurityActionEmpty{}},
	})
	cap := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_CompleteAccountPublicProfile{
			CompleteAccountPublicProfile: &corev1.SecurityServerCodeAction{Server: "https://relay.example.com", Code: rot.RecoveryCode},
		},
	})
	if !cap.Ok || !cap.Status.HasAccountPublicProfile {
		t.Fatalf("CompleteAccountPublicProfile failed: %+v", cap)
	}
	h.restart("https://relay.example.com", false)
	reBoot := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	if !reBoot.Ok || !reBoot.Status.HasAccountPublicProfile {
		t.Fatalf("expected AccountPublicProfile persisted across restart, got %+v", reBoot.Status)
	}

	// UpdateDeviceMeta CAS retry
	relay.bumpMetaOnPutOnce = true
	metaRes := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_UpdateDeviceMeta{
			UpdateDeviceMeta: &corev1.SecurityUpdateDeviceMetaAction{Name: "Phone A", Platform: "ios"},
		},
	})
	if !metaRes.Ok {
		t.Fatalf("UpdateDeviceMeta CAS retry failed: %+v", metaRes)
	}

	// CAS conflict halts without clearing Pin
	_ = h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareRecoveryRotation{PrepareRecoveryRotation: &corev1.SecurityActionEmpty{}},
	})
	h.conflictOnce = true
	confRes := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ConfirmRecoveryRotation{ConfirmRecoveryRotation: &corev1.SecurityActionEmpty{}},
	})
	if confRes.Ok || h.eng.pin.Account == "" {
		t.Fatalf("expected CAS conflict error without clearing pin, got ok=%v account=%q", confRes.Ok, h.eng.pin.Account)
	}
	_ = h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_DiscardPendingOp{DiscardPendingOp: &corev1.SecurityBoolAction{Flag: true}},
	})

	// Notify Open tests: Path A (NotifyBlob) and Path B (RecordBlob + KeyAgree)
	machSeed := bytes.Repeat([]byte{0x91}, 32)
	machEd := ed25519.NewKeyFromSeed(machSeed)
	machX, _ := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{0x92}, 32))
	var machXPub [32]byte
	copy(machXPub[:], machX.PublicKey().Bytes())
	machMem, _ := devicesec.NewMember(machEd.Public().(ed25519.PublicKey), machXPub[:], devicesec.Machine)

	pMach := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartPairing{StartPairing: &corev1.SecurityStartPairingAction{}},
	})
	machEphem, _ := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{0x93}, 32))
	joinBytes, _ := (&v1.JoinPairingRequest{
		Code:                pMach.Pairing.Code,
		JoinerPublic:        machEphem.PublicKey().Bytes(),
		JoinerKind:          v1.DeviceKind_DEVICE_KIND_MACHINE,
		DevicePublicKey:     machMem.EdPublic[:],
		DeviceHpkePublicKey: machMem.XPublic[:],
		ProtocolVersion:     testRelayVersion,
	}).Marshal()
	_, _ = relay.call(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_JOIN_PAIRING, "", joinBytes)
	_ = h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PollPairing{PollPairing: &corev1.SecurityStringAction{Value: pMach.Pairing.PairingId}},
	})
	_ = h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ApprovePairing{
			ApprovePairing: &corev1.SecurityApprovePairingAction{PairingId: pMach.Pairing.PairingId, SasConfirmed: true},
		},
	})
	ntfEntry := h.store[mockStoreKey{corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_NOTIFY_RECORD, "notify"}]
	recEntry := h.store[mockStoreKey{corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD, "record"}]
	if len(ntfEntry.data) == 0 || len(recEntry.data) == 0 {
		t.Fatal("expected persisted notify_blob and record_blob")
	}

	headBytes := h.eng.wf.record.States[len(h.eng.wf.record.States)-1]
	headState, _ := devicesec.ParseState(headBytes)
	headHash, _ := headState.Hash()
	sec := h.eng.epochSecrets[headState.Core.ContentEpoch]
	ncBytes, _ := proto.Marshal(&stdv1.NotifyContent{
		MachineId:      machMem.ID,
		SessionId:      "sess-1",
		Kind:           stdv1.NotifyKind_NOTIFY_KIND_TURN_ENDED,
		SourceEventSeq: 1,
		Title:          "Done",
		Text:           "Finished turn",
	})
	hdr := devicesec.Header{
		Account:      h.eng.pin.Account,
		Domain:       h.eng.pin.Domain,
		State:        headHash,
		Epoch:        headState.Core.Epoch,
		ContentEpoch: headState.Core.ContentEpoch,
		Kind:         devicesec.Notify.Kind(),
		Purpose:      devicesec.Notify,
		Sender:       machMem.ID,
		Session:      "sess-1",
		Machine:      machMem.ID,
		Request:      "notify:2:1",
	}
	ct, err := devicesec.NewCiphertext(sec[:], nil, hdr, ncBytes, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ob, _ := ct.OriginBytes()
	ct.Origin = ed25519.Sign(machEd, ob)
	pbEnv, err := ToProtoEnvelope(ct)
	if err != nil {
		t.Fatal(err)
	}

	// Path A succeeds with validated display
	h.pump(h.eng.HandleOpenNotify(h.nowMs, &corev1.SecurityOpenNotifyEvent{
		OpId:         9001,
		NotifyBlob:   ntfEntry.data,
		Envelope:     pbEnv,
		SessionId:    "sess-1",
		Tag:          "sess-1/NOTIFY_KIND_TURN_ENDED",
		ExpectedKind: v1.NotifyKind_NOTIFY_KIND_TURN_ENDED,
	}))
	if res := h.notifyOpened[9001]; res == nil || res.Error != "" || !res.GetDisplay().GetValid() || res.GetDisplay().GetTitle().GetLiteral() != "Done" || res.VerifyRecordUnchanged {
		t.Fatalf("Path A open notify failed: %+v", res)
	}

	// Path B succeeds with VerifyRecordUnchanged: true
	h.pump(h.eng.HandleOpenNotify(h.nowMs, &corev1.SecurityOpenNotifyEvent{
		OpId:         9002,
		RecordBlob:   recEntry.data,
		Envelope:     pbEnv,
		NowMs:        h.nowMs,
		SessionId:    "sess-1",
		Tag:          "sess-1/NOTIFY_KIND_TURN_ENDED",
		ExpectedKind: v1.NotifyKind_NOTIFY_KIND_TURN_ENDED,
	}))
	if res := h.notifyOpened[9002]; res == nil || res.Error != "" || !res.VerifyRecordUnchanged || !res.GetDisplay().GetValid() || res.GetDisplay().GetBody().GetLiteral() != "Finished turn" {
		t.Fatalf("Path B open notify failed: %+v", res)
	}

	// Omitting ExpectedKind fails closed without leaking plaintext title/body.
	h.pump(h.eng.HandleOpenNotify(h.nowMs, &corev1.SecurityOpenNotifyEvent{
		OpId:       9006,
		NotifyBlob: ntfEntry.data,
		Envelope:   pbEnv,
	}))
	if res := h.notifyOpened[9006]; res == nil || res.Error != "" || res.GetDisplay().GetValid() || res.GetDisplay().GetTitle() != nil || res.GetDisplay().GetBody() != nil {
		t.Fatalf("expected UNSPECIFIED ExpectedKind to fail closed without title/body, got %+v", res)
	}

	// Remove machine (abandoning pending pair delivery automatically) & verify stale Path A and removed-sender Path B reject!
	remMach := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_RemoveDevice{
			RemoveDevice: &corev1.SecurityRemoveDeviceAction{DeviceId: machMem.ID},
		},
	})
	if !remMach.Ok {
		t.Fatalf("expected removing unfinished pairing candidate to abandon its pending delivery op, got %+v", remMach)
	}
	h.pump(h.eng.HandleOpenNotify(h.nowMs, &corev1.SecurityOpenNotifyEvent{
		OpId:       9003,
		NotifyBlob: ntfEntry.data, // bound to older head before RemoveDevice
		Envelope:   pbEnv,
	}))
	// ntfEntry matches pbEnv's h.State (older head), whereas current NotifyBlob is updated to new head:
	curNtf := h.store[mockStoreKey{corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_NOTIFY_RECORD, "notify"}]
	h.pump(h.eng.HandleOpenNotify(h.nowMs, &corev1.SecurityOpenNotifyEvent{
		OpId:       9005,
		NotifyBlob: curNtf.data,
		Envelope:   pbEnv,
	}))
	if res := h.notifyOpened[9005]; res == nil || res.Error == "" {
		t.Fatalf("expected stale h.State != headHash to reject Path A, got %+v", res)
	}
	recAfterRem := h.store[mockStoreKey{corev1.SecurityStoreBucket_SECURITY_STORE_BUCKET_RECORD, "record"}]
	// Historical pre-removal notification bound to prior state stays openable under live=false (matching seccall.go),
	// whereas a notification from the removed machine targeting the post-removal head is rejected.
	newHeadBytes := h.eng.wf.record.States[len(h.eng.wf.record.States)-1]
	newHeadState, _ := devicesec.ParseState(newHeadBytes)
	newHeadHash, _ := newHeadState.Hash()
	newSec := h.eng.epochSecrets[newHeadState.Core.ContentEpoch]
	hdrAfter := hdr
	hdrAfter.State = newHeadHash
	hdrAfter.Epoch = newHeadState.Core.Epoch
	hdrAfter.ContentEpoch = newHeadState.Core.ContentEpoch
	ctAfter, err := devicesec.NewCiphertext(newSec[:], nil, hdrAfter, ncBytes, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	obAfter, _ := ctAfter.OriginBytes()
	ctAfter.Origin = ed25519.Sign(machEd, obAfter)
	pbEnvAfter, err := ToProtoEnvelope(ctAfter)
	if err != nil {
		t.Fatal(err)
	}
	h.pump(h.eng.HandleOpenNotify(h.nowMs, &corev1.SecurityOpenNotifyEvent{
		OpId:       9004,
		RecordBlob: recAfterRem.data,
		Envelope:   pbEnvAfter,
		NowMs:      h.nowMs,
	}))
	if res := h.notifyOpened[9004]; res == nil || res.Error == "" {
		t.Fatalf("expected post-removal machine notification to be rejected by Path B, got %+v", res)
	}

	// DeleteMachineRecords + DeleteAccount
	relay.addMachine(h.eng.pin.Account, machMem)
	delM := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_DeleteMachineRecords{DeleteMachineRecords: &corev1.SecurityStringAction{Value: machMem.ID}},
	})
	if !delM.Ok {
		t.Fatalf("DeleteMachineRecords failed: %+v", delM)
	}
	delA := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_DeleteAccount{DeleteAccount: &corev1.SecurityActionEmpty{}},
	})
	if !delA.Ok || h.eng.Locked() || delA.Status.Bootstrap != corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_EMPTY {
		t.Fatalf("DeleteAccount failed: %+v", delA)
	}
}

func TestWorkflowHandoffAndEmergencyRecovery(t *testing.T) {
	relay := newFakeRelay(1_700_000_000_000)

	a := newFakeClientHarness(t, relay, 0x80)
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}}})
	prepA := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareCreation{
			PrepareCreation: &corev1.SecurityPrepareCreationAction{Server: "https://relay.example.com"},
		},
	})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ConfirmCreationCodeSaved{ConfirmCreationCodeSaved: &corev1.SecurityActionEmpty{}}})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ContinueCreation{ContinueCreation: &corev1.SecurityStringAction{}}})
	_ = prepA.RecoveryCode

	pp := &v1.ProjectPrefs{Schema: 1, RecentOps: []string{"/repo/app"}}
	ppBytes, _ := pp.Marshal()
	hHash := a.eng.headHash
	sec0 := a.eng.epochSecrets[a.eng.head.Core.ContentEpoch]
	pHdr := devicesec.Header{
		Account:      a.eng.pin.Account,
		Domain:       a.eng.pin.Domain,
		State:        hHash,
		Epoch:        a.eng.head.Core.Epoch,
		ContentEpoch: a.eng.head.Core.ContentEpoch,
		Kind:         devicesec.Prefs.Kind(),
		Purpose:      devicesec.Prefs,
		Sender:       a.eng.selfDeviceID,
		Request:      "project-prefs",
		Seq:          1,
	}
	pCt, err := devicesec.NewCiphertext(sec0[:], nil, pHdr, ppBytes, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pOb, _ := pCt.OriginBytes()
	pCt.Origin = ed25519.Sign(a.deviceEd, pOb)
	pEnv, err := ToProtoEnvelope(pCt)
	if err != nil {
		t.Fatal(err)
	}
	relay.prefs[a.eng.pin.Account+":project-prefs"] = &v1.AccountPrefsObject{
		ObjectId: "project-prefs",
		Revision: 1,
		Envelope: pEnv,
	}

	b := newFakeClientHarness(t, relay, 0x85)
	_ = b.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}}})
	stP := a.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_StartPairing{StartPairing: &corev1.SecurityStartPairingAction{}}})
	jB := b.startAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartJoinPairing{
			StartJoinPairing: &corev1.SecurityStartJoinPairingAction{Server: "https://relay.example.com", Code: stP.Pairing.Code},
		},
	})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_PollPairing{PollPairing: &corev1.SecurityStringAction{Value: stP.Pairing.PairingId}}})
	b.pumpPollTicks(5, func() bool { return b.actionResults[jB] != nil })
	cB := b.startAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ConfirmJoinSas{ConfirmJoinSas: &corev1.SecurityStringAction{Value: stP.Pairing.PairingId}}})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ApprovePairing{ApprovePairing: &corev1.SecurityApprovePairingAction{PairingId: stP.Pairing.PairingId, SasConfirmed: true}}})
	b.pumpPollTicks(5, func() bool { return b.actionResults[cB] != nil })

	// Directed handoff A -> B
	offPrep := b.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareHandoffCode{PrepareHandoffCode: &corev1.SecurityBoolAction{Flag: true}},
	})
	if !offPrep.Ok || offPrep.RecoveryCode == "" {
		t.Fatalf("PrepareHandoffCode failed: %+v", offPrep)
	}
	pubOff := b.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PublishHandoffOffer{PublishHandoffOffer: &corev1.SecurityActionEmpty{}},
	})
	if !pubOff.Ok {
		t.Fatalf("PublishHandoffOffer failed: %+v", pubOff)
	}
	candsRes := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ListHandoffCandidates{ListHandoffCandidates: &corev1.SecurityActionEmpty{}},
	})
	if !candsRes.Ok || len(candsRes.HandoffCandidates) != 1 || candsRes.HandoffCandidates[0].DeviceId != b.eng.selfDeviceID {
		t.Fatalf("expected B in HandoffCandidates, got %+v", candsRes.HandoffCandidates)
	}
	hoRes := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_HandoffAuthority{HandoffAuthority: &corev1.SecurityStringAction{Value: b.eng.selfDeviceID}},
	})
	if !hoRes.Ok || a.eng.isMember || hoRes.Status.Bootstrap != corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_REMOVED {
		t.Fatalf("expected A self-revoked after HandoffAuthority, got %+v", hoRes)
	}
	bSync := b.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_SyncNow{SyncNow: &corev1.SecuritySyncNowAction{Touch: true}},
	})
	if !bSync.Ok || !bSync.Status.IsAuthority {
		t.Fatalf("expected B to become Authority on SyncNow, got %+v", bSync.Status)
	}
	if st := b.eng.BuildWorkflowStatus(); !st.IsAuthority || !st.LeaseValid || st.PendingOp != nil {
		t.Fatalf("expected B background auto-lease-renewal to complete after SyncNow, got %+v", st)
	}
	currentCode := offPrep.RecoveryCode

	// Pair C onto account (non-authority member)
	stP2 := b.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_StartPairing{StartPairing: &corev1.SecurityStartPairingAction{}}})
	c := newFakeClientHarness(t, relay, 0x90)
	_ = c.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}}})
	jC := c.startAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartJoinPairing{
			StartJoinPairing: &corev1.SecurityStartJoinPairingAction{Server: "https://relay.example.com", Code: stP2.Pairing.Code},
		},
	})
	_ = b.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_PollPairing{PollPairing: &corev1.SecurityStringAction{Value: stP2.Pairing.PairingId}}})
	c.pumpPollTicks(5, func() bool { return c.actionResults[jC] != nil })
	confC := c.startAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ConfirmJoinSas{ConfirmJoinSas: &corev1.SecurityStringAction{Value: stP2.Pairing.PairingId}}})
	_ = b.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ApprovePairing{ApprovePairing: &corev1.SecurityApprovePairingAction{PairingId: stP2.Pairing.PairingId, SasConfirmed: true}}})
	c.pumpPollTicks(5, func() bool { return c.actionResults[confC] != nil })

	_ = c.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_SyncNow{SyncNow: &corev1.SecuritySyncNowAction{Touch: true}}})
	c.nowMs = b.nowMs
	relay.nowMs = b.nowMs

	// Test VETO flow: D submits recovery -> B vetoes -> D DiscardRejectedRecovery!
	d := newFakeClientHarness(t, relay, 0x95)
	_ = d.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}}})
	dPrep := d.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareEmergencyRecovery{
			PrepareEmergencyRecovery: &corev1.SecurityServerCodeAction{Server: "https://relay.example.com", Code: currentCode},
		},
	})
	if !dPrep.Ok || dPrep.RecoveryCode == "" || dPrep.Recovery != nil || len(d.eng.wf.pendingOp.Signed) == 0 {
		t.Fatalf("expected signed emergency recovery draft without premature RecoveryInfo on D, got %+v", dPrep)
	}
	_ = d.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ConfirmRecoveryCodeSaved{ConfirmRecoveryCodeSaved: &corev1.SecurityActionEmpty{}}})
	dSub := d.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ResumeRecovery{ResumeRecovery: &corev1.SecurityActionEmpty{}}})
	if !dSub.Ok || dSub.Recovery == nil || dSub.Recovery.Phase != v1.GetRecoveryContextResponse_PHASE_PENDING {
		t.Fatalf("expected PHASE_PENDING recovery on D via ResumeRecovery, got %+v", dSub)
	}
	bVetoRot := b.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_PrepareRecoveryRotation{PrepareRecoveryRotation: &corev1.SecurityActionEmpty{}}})
	_ = b.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ConfirmRecoveryRotation{ConfirmRecoveryRotation: &corev1.SecurityActionEmpty{}}})
	currentCode = bVetoRot.RecoveryCode
	dRes := d.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ResumeRecovery{ResumeRecovery: &corev1.SecurityActionEmpty{}}})
	if !dRes.Ok || dRes.Recovery.Phase != v1.GetRecoveryContextResponse_PHASE_REJECTED {
		t.Fatalf("expected PHASE_REJECTED after veto, got %+v", dRes)
	}
	dDisc := d.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_DiscardRejectedRecovery{DiscardRejectedRecovery: &corev1.SecurityActionEmpty{}}})
	if !dDisc.Ok || len(d.candEd) != 0 || dDisc.Status.Bootstrap != corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_EMPTY {
		t.Fatalf("DiscardRejectedRecovery failed: %+v", dDisc)
	}

	// Real Emergency Recovery onto C (RetiringMemberID = old C ID)
	_ = c.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_SyncNow{SyncNow: &corev1.SecuritySyncNowAction{}}})
	cOldMemberID := c.eng.selfDeviceID
	cPrep := c.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareEmergencyRecovery{
			PrepareEmergencyRecovery: &corev1.SecurityServerCodeAction{Server: "https://relay.example.com", Code: currentCode},
		},
	})
	if !cPrep.Ok || cPrep.RecoveryCode == "" || cPrep.Recovery != nil || len(c.eng.wf.pendingOp.Signed) == 0 || c.eng.wf.pendingOp.Recovery.RetiringMemberID != cOldMemberID {
		t.Fatalf("expected C signed emergency recovery draft with RetiringMemberID=%s, got %+v", cOldMemberID, cPrep)
	}
	_ = c.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ConfirmRecoveryCodeSaved{ConfirmRecoveryCodeSaved: &corev1.SecurityActionEmpty{}}})
	cSub := c.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ResumeRecovery{ResumeRecovery: &corev1.SecurityActionEmpty{}}})
	if !cSub.Ok || cSub.Recovery == nil || cSub.Recovery.Phase != v1.GetRecoveryContextResponse_PHASE_PENDING || cSub.Recovery.ReadyMs <= c.nowMs {
		t.Fatalf("expected PHASE_PENDING with future ReadyMs on C, got %+v", cSub)
	}

	c.nowMs += 49 * 60 * 60 * 1000
	relay.nowMs = c.nowMs
	cReady := c.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ResumeRecovery{ResumeRecovery: &corev1.SecurityActionEmpty{}}})
	if !cReady.Ok || cReady.Recovery.Phase != v1.GetRecoveryContextResponse_PHASE_READY {
		t.Fatalf("expected PHASE_READY after 48h, got %+v", cReady)
	}
	cComp := c.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_CompleteRecovery{CompleteRecovery: &corev1.SecurityActionEmpty{}}})
	if !cComp.Ok || cComp.AccountAdopted == nil || !cComp.Status.IsAuthority || !cComp.Status.LeaseValid {
		t.Fatalf("CompleteRecovery failed: %+v", cComp)
	}
	if _, stillOld := c.eng.head.Core.Member(cOldMemberID); stillOld {
		t.Fatal("expected RetiringMemberID to be removed from head after CompleteRecovery")
	}
	if !slices.Contains(c.eng.head.Core.Removed, cOldMemberID) || !slices.Contains(c.eng.head.Core.Removed, b.eng.selfDeviceID) {
		t.Fatal("expected both old authority B and retired self C-old in Removed list")
	}
	if c.eng.BuildWorkflowStatus().RecoveryObjectsPending {
		t.Fatal("expected RecoveryObjectsPending cleared after republishRecoveryObjects")
	}
	repref := relay.prefs[c.eng.pin.Account+":project-prefs"]
	if repref == nil || repref.Revision != 2 {
		t.Fatalf("expected republished project-prefs revision 2, got %+v", repref)
	}

	// Verify golden recoveryDonePrefix KeySign bytes
	var sawRecCompleteSign bool
	for _, sc := range c.signCalls {
		if bytes.HasPrefix(sc.Msg, []byte(recoveryDonePrefix)) {
			sawRecCompleteSign = true
		}
	}
	if !sawRecCompleteSign {
		t.Fatal("expected recoveryDonePrefix KeySign golden call on recovery completion")
	}

	// Regression Fix 1: claimOperationCAS must preserve rec.RecoveryProofs after emergency recovery
	// so post-recovery restarts, lease renewals, and authority rotations do not degrade to ErrStale.
	wantProofs := len(c.eng.wf.record.RecoveryProofs)
	if wantProofs == 0 {
		t.Fatal("expected CompleteRecovery to persist non-empty RecoveryProofs")
	}
	c.restart("https://relay.example.com", false)
	bootAfterRec := c.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	if !bootAfterRec.Ok || bootAfterRec.Status.Bootstrap != corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_READY || !bootAfterRec.Status.IsAuthority {
		t.Fatalf("expected post-recovery READY authority on restart, got %+v", bootAfterRec)
	}
	if len(c.eng.wf.record.RecoveryProofs) != wantProofs {
		t.Fatalf("expected RecoveryProofs count %d after restart bootstrap, got %d", wantProofs, len(c.eng.wf.record.RecoveryProofs))
	}
	c.nowMs += 60_000
	relay.nowMs = c.nowMs
	renewAfterRec := c.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_RenewLease{RenewLease: &corev1.SecurityBoolAction{Flag: true}},
	})
	if !renewAfterRec.Ok || !renewAfterRec.Status.LeaseValid {
		t.Fatalf("expected post-recovery RenewLease to succeed, got %+v", renewAfterRec)
	}
	rotAfterRec := c.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareRecoveryRotation{PrepareRecoveryRotation: &corev1.SecurityActionEmpty{}},
	})
	if !rotAfterRec.Ok || rotAfterRec.RecoveryCode == "" {
		t.Fatalf("expected post-recovery PrepareRecoveryRotation to succeed, got %+v", rotAfterRec)
	}
	if len(c.eng.wf.record.RecoveryProofs) != wantProofs {
		t.Fatalf("claimOperationCAS dropped RecoveryProofs: want %d, got %d", wantProofs, len(c.eng.wf.record.RecoveryProofs))
	}
	rotConfAfterRec := c.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ConfirmRecoveryRotation{ConfirmRecoveryRotation: &corev1.SecurityActionEmpty{}},
	})
	if !rotConfAfterRec.Ok {
		t.Fatalf("expected post-recovery ConfirmRecoveryRotation to succeed, got %+v", rotConfAfterRec)
	}
	c.restart("https://relay.example.com", false)
	bootAfterRot := c.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	if !bootAfterRot.Ok || !bootAfterRot.Status.IsAuthority || len(c.eng.wf.record.RecoveryProofs) <= wantProofs {
		t.Fatalf("expected preserved RecoveryProofs (> %d) after restart post-rotation, got %d (%+v)", wantProofs, len(c.eng.wf.record.RecoveryProofs), bootAfterRot)
	}
}

func TestConcurrentPrepareCreationUsesDistinctSlotsAndDiscardsLoser(t *testing.T) {
	relay := newFakeRelay(1_700_000_000_000)
	h1 := newFakeClientHarness(t, relay, 0x21)
	h2 := newFakeClientHarness(t, relay, 0x22)
	// Simulate two browser tabs backed by the same v4 IndexedDB store and candidate key space.
	h2.store = h1.store
	h2.candEd = h1.candEd
	h2.candX = h1.candX
	h2.ephemX = h1.ephemX

	b1 := h1.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}}})
	b2 := h2.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}}})
	if !b1.Ok || !b2.Ok || b1.Status.Bootstrap != corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_EMPTY || b2.Status.Bootstrap != corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_EMPTY {
		t.Fatalf("expected both tabs to start EMPTY, got b1=%+v b2=%+v", b1, b2)
	}

	p1 := h1.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareCreation{
			PrepareCreation: &corev1.SecurityPrepareCreationAction{Server: "https://relay.example.com", StoragePersisted: true},
		},
	})
	if !p1.Ok || p1.RecoveryCode == "" || len(h1.candEd) != 1 || len(h1.candX) != 1 {
		t.Fatalf("expected tab1 PrepareCreation to allocate 1 ed and 1 x candidate slot, got %+v ed=%d x=%d", p1, len(h1.candEd), len(h1.candX))
	}
	var slotEd1, slotX1 string
	for s := range h1.candEd {
		slotEd1 = s
	}
	for s := range h1.candX {
		slotX1 = s
	}
	if slotEd1 == "create-ed" || slotX1 == "create-x" || slotEd1 == "create-ed-wf-1" || slotX1 == "create-x-wf-1" {
		t.Fatalf("expected per-attempt random creation slots, got ed=%q x=%q", slotEd1, slotX1)
	}
	pubEd1 := slices.Clone(h1.candEd[slotEd1].Public().(ed25519.PublicKey))

	// Tab 2 races with PrepareCreation: it allocates its own slots, loses claimOperationCAS on CreateOnly,
	// discards its own loser slots, and leaves tab 1's candidate keys intact.
	p2 := h2.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareCreation{
			PrepareCreation: &corev1.SecurityPrepareCreationAction{Server: "https://relay.example.com", StoragePersisted: true},
		},
	})
	if p2.Ok {
		t.Fatalf("expected concurrent tab2 PrepareCreation to fail CAS claim")
	}
	if len(h1.candEd) != 1 || len(h1.candX) != 1 || h1.candEd[slotEd1] == nil || h1.candX[slotX1] == nil {
		t.Fatalf("expected tab1 candidate slots preserved after tab2 conflict, got ed=%v x=%v", h1.candEd, h1.candX)
	}
	if !bytes.Equal(h1.candEd[slotEd1].Public().(ed25519.PublicKey), pubEd1) {
		t.Fatal("tab2 clobbered tab1 candidate Ed25519 key")
	}

	_ = h1.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ConfirmCreationCodeSaved{ConfirmCreationCodeSaved: &corev1.SecurityActionEmpty{}}})
	cont := h1.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ContinueCreation{ContinueCreation: &corev1.SecurityStringAction{}}})
	if !cont.Ok || !cont.Status.IsAuthority {
		t.Fatalf("expected tab1 ContinueCreation to succeed using preserved candidate key, got %+v", cont)
	}
	if h1.deviceEd == nil || !bytes.Equal(h1.deviceEd.Public().(ed25519.PublicKey), pubEd1) {
		t.Fatal("expected promoted deviceEd key to match tab1's preserved candidate key")
	}
	if len(h1.candEd) != 0 || len(h1.candX) != 0 {
		t.Fatalf("expected all candidate slots cleared after promotion, got ed=%d x=%d", len(h1.candEd), len(h1.candX))
	}
}

func TestContinueRecoverySigningResumesUnsignedDraftAfterCrash(t *testing.T) {
	relay := newFakeRelay(1_700_000_000_000)
	a := newFakeClientHarness(t, relay, 0x71)
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}}})
	prepA := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareCreation{
			PrepareCreation: &corev1.SecurityPrepareCreationAction{Server: "https://relay.example.com"},
		},
	})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ConfirmCreationCodeSaved{ConfirmCreationCodeSaved: &corev1.SecurityActionEmpty{}}})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ContinueCreation{ContinueCreation: &corev1.SecurityStringAction{}}})

	b := newFakeClientHarness(t, relay, 0x72)
	_ = b.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}}})
	prepB := b.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareEmergencyRecovery{
			PrepareEmergencyRecovery: &corev1.SecurityServerCodeAction{Server: "https://relay.example.com", Code: prepA.RecoveryCode},
		},
	})
	if !prepB.Ok || prepB.Recovery != nil || len(b.eng.wf.pendingOp.Signed) == 0 {
		t.Fatalf("expected signed draft after PrepareEmergencyRecovery, got %+v", prepB)
	}

	// Simulate crash right after claimOperationCAS before Signed was written.
	key := mockStoreKey{bucket: bktOp, key: "pending"}
	entry := b.store[key]
	op, err := DecodePersistedOperation(entry.data)
	if err != nil {
		t.Fatal(err)
	}
	op.Signed = nil
	op.Recovery.TargetState = nil
	op.Recovery.Proof = nil
	entry.data, err = op.Encode()
	if err != nil {
		t.Fatal(err)
	}
	b.store[key] = entry

	b.restart("https://relay.example.com", false)
	boot := b.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}}})
	if !boot.Ok || boot.Status.Bootstrap != corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_PENDING_RECOVERY || boot.Status.PendingOp == nil || boot.Status.PendingOp.Signed {
		t.Fatalf("expected unsigned PENDING_RECOVERY draft on restart, got %+v", boot)
	}
	res := b.runAction(&corev1.SecurityWorkflowActionEvent{Action: &corev1.SecurityWorkflowActionEvent_ResumeRecovery{ResumeRecovery: &corev1.SecurityActionEmpty{}}})
	if !res.Ok || !res.ResumedDraft || res.Recovery != nil {
		t.Fatalf("expected ResumeRecovery on unsigned draft to signal ResumedDraft without Recovery, got %+v", res)
	}
	cont := b.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ContinueRecoverySigning{ContinueRecoverySigning: &corev1.SecurityStringAction{Value: prepA.RecoveryCode}},
	})
	if !cont.Ok || cont.Recovery == nil || cont.Recovery.Phase != v1.GetRecoveryContextResponse_PHASE_PENDING || cont.Recovery.RecoveryId == "" || cont.Recovery.ReadyMs <= b.nowMs {
		t.Fatalf("expected ContinueRecoverySigning to sign and submit recovery, got %+v", cont)
	}
	if len(b.eng.wf.pendingOp.Signed) == 0 {
		t.Fatal("expected Signed payload persisted after ContinueRecoverySigning")
	}
}

func TestStructuredSecurityErrorCodeAndNotifyDisplayValidation(t *testing.T) {
	if got := StructuredErrorCode(ErrLeaseExpired); got != corev1.SecurityErrorCode_SECURITY_ERROR_CODE_LEASE_EXPIRED {
		t.Fatalf("StructuredErrorCode(ErrLeaseExpired) = %v, want LEASE_EXPIRED", got)
	}
	if got := StructuredErrorCode(devicesec.ErrStale); got != corev1.SecurityErrorCode_SECURITY_ERROR_CODE_STALE {
		t.Fatalf("StructuredErrorCode(ErrStale) = %v, want STALE", got)
	}

	// Disk notification: valid critical & warning.
	diskNC := &v1.NotifyContent{
		Kind:        v1.NotifyKind_NOTIFY_KIND_MACHINE_DISK,
		MachineId:   "m1",
		MachineName: "MacBook",
		DiskStatus: &v1.DiskStatus{
			ObservedMs:     1_700_000_000_000,
			AvailableBytes: 1_500_000_000,
			Pressure:       v1.ResourcePressure_RESOURCE_PRESSURE_CRITICAL,
		},
	}
	diskEv := &corev1.SecurityOpenNotifyEvent{
		MachineId:    "m1",
		Tag:          "m1/NOTIFY_KIND_MACHINE_DISK",
		ExpectedKind: v1.NotifyKind_NOTIFY_KIND_MACHINE_DISK,
	}
	diskHdr := devicesec.Header{Machine: "m1", Request: "disk:1700000000000:3"}
	dOK := buildNotifyDisplay(diskEv, diskHdr, diskNC)
	if !dOK.Valid || !dOK.Critical || !dOK.ShowDetailedByDefault || dOK.Title.GetKey() != text.KeyMachineStatusNotifyCritical {
		t.Fatalf("expected valid critical disk display, got %+v", dOK)
	}

	// Scheduled notification: valid.
	schedNC := &v1.NotifyContent{
		Kind:           v1.NotifyKind_NOTIFY_KIND_SCHEDULED,
		SessionId:      "s1",
		MachineId:      "m1",
		SourceEventSeq: 7,
		Title:          "Daily check",
		Text:           "All green",
	}
	schedEv := &corev1.SecurityOpenNotifyEvent{
		SessionId:    "s1",
		Tag:          "s1/scheduled:7/NOTIFY_KIND_SCHEDULED",
		EventKey:     "scheduled:7",
		ExpectedKind: v1.NotifyKind_NOTIFY_KIND_SCHEDULED,
	}
	schedHdr := devicesec.Header{Machine: "m1", Session: "s1", Request: "scheduled:7"}
	sOK := buildNotifyDisplay(schedEv, schedHdr, schedNC)
	if !sOK.Valid || !sOK.ShowDetailedByDefault || sOK.Title.GetLiteral() != "Daily check" || sOK.Body.GetLiteral() != "All green" {
		t.Fatalf("expected valid scheduled display, got %+v", sOK)
	}

	// Turn ended with shared files suffix.
	turnNC := &v1.NotifyContent{
		Kind:            v1.NotifyKind_NOTIFY_KIND_TURN_ENDED,
		SessionId:       "s1",
		MachineId:       "m1",
		SourceEventSeq:  42,
		Text:            "Done",
		SharedFileCount: 2,
	}
	turnEv := &corev1.SecurityOpenNotifyEvent{
		SessionId:    "s1",
		Tag:          "s1/NOTIFY_KIND_TURN_ENDED",
		ExpectedKind: v1.NotifyKind_NOTIFY_KIND_TURN_ENDED,
	}
	turnHdr := devicesec.Header{Machine: "m1", Session: "s1", Request: "notify:2:42"}
	tOK := buildNotifyDisplay(turnEv, turnHdr, turnNC)
	if !tOK.Valid || tOK.ShowDetailedByDefault || tOK.SharedFileCount != 2 || len(tOK.GetFullGenericBody().GetJoin()) != 2 || tOK.GetFullGenericBody().GetJoin()[1].GetKey() != text.KeySharedFileNotificationFiles {
		t.Fatalf("expected valid turn ended display with shared files suffix, got %+v", tOK)
	}
	if got := tOK.FullGenericBody; got == nil || len(got.Join) != 2 || got.SeparatorLiteral != " " ||
		got.Join[0].GetKey() != text.KeyNotificationsAndroidAConversationTurnFinished ||
		got.Join[1].GetKey() != text.KeySharedFileNotificationFiles {
		t.Fatalf("expected FullGenericBody to join default generic body and shared files suffix, got %+v", got)
	}

	// Table-driven check for FullGenericBody with/without FallbackBody and SharedFileCount.
	strPtr := func(s string) *string { return &s }
	turnBodyCases := []struct {
		name      string
		fallback  *string
		files     uint32
		wantNil   bool
		wantLit   string
		wantKey   string
		wantJoin0 string
		wantJoin1 string
	}{
		{
			name:      "web fallback body with files",
			fallback:  strPtr("A turn ended"),
			files:     2,
			wantJoin0: "lit:A turn ended",
			wantJoin1: "key:" + text.KeySharedFileNotificationFiles,
		},
		{
			name:     "empty web fallback body with files yields suffix only",
			fallback: strPtr(""),
			files:    3,
			wantKey:  text.KeySharedFileNotificationFiles,
		},
		{
			name:     "web fallback body without files",
			fallback: strPtr("A turn ended"),
			files:    0,
			wantLit:  "A turn ended",
		},
		{
			name:     "empty web fallback body without files is empty",
			fallback: strPtr(""),
			files:    0,
			wantNil:  true,
		},
		{
			name:    "omitted fallback body without files uses default generic body",
			files:   0,
			wantKey: text.KeyNotificationsAndroidAConversationTurnFinished,
		},
	}
	for _, tc := range turnBodyCases {
		t.Run(tc.name, func(t *testing.T) {
			evCopy := &corev1.SecurityOpenNotifyEvent{
				SessionId:    turnEv.SessionId,
				Tag:          turnEv.Tag,
				ExpectedKind: turnEv.ExpectedKind,
				FallbackBody: tc.fallback,
			}
			ncCopy := &v1.NotifyContent{
				Kind:            turnNC.Kind,
				SessionId:       turnNC.SessionId,
				MachineId:       turnNC.MachineId,
				SourceEventSeq:  turnNC.SourceEventSeq,
				Text:            turnNC.Text,
				SharedFileCount: tc.files,
			}
			got := buildNotifyDisplay(evCopy, turnHdr, ncCopy).FullGenericBody
			if tc.wantNil {
				if got != nil {
					t.Fatalf("expected nil FullGenericBody, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected non-nil FullGenericBody")
			}
			if tc.wantLit != "" && got.GetLiteral() != tc.wantLit {
				t.Fatalf("FullGenericBody literal = %q, want %q", got.GetLiteral(), tc.wantLit)
			}
			if tc.wantKey != "" && got.GetKey() != tc.wantKey {
				t.Fatalf("FullGenericBody key = %q, want %q", got.GetKey(), tc.wantKey)
			}
			if tc.wantJoin0 != "" {
				if len(got.Join) != 2 || got.SeparatorLiteral != " " {
					t.Fatalf("expected 2-part space-joined FullGenericBody, got %+v", got)
				}
				part := func(p *corev1.Text) string {
					if p.GetKey() != "" {
						return "key:" + p.GetKey()
					}
					return "lit:" + p.GetLiteral()
				}
				if part(got.Join[0]) != tc.wantJoin0 || part(got.Join[1]) != tc.wantJoin1 {
					t.Fatalf("FullGenericBody join = [%s, %s], want [%s, %s]", part(got.Join[0]), part(got.Join[1]), tc.wantJoin0, tc.wantJoin1)
				}
			}
		})
	}

	// Approval attention notification.
	appNC := &v1.NotifyContent{
		Kind:           v1.NotifyKind_NOTIFY_KIND_ATTENTION,
		SessionId:      "s1",
		MachineId:      "m1",
		Title:          "Fix build",
		SourceEventSeq: 9,
		Approval: &v1.NotifyApproval{
			ApprovalId:  "ap-1",
			ToolName:    "Bash",
			Command:     "go test ./...",
			Description: "Run Go tests",
			DeadlineMs:  1_800_000_000_000,
			Truncated:   true,
		},
	}
	appEv := &corev1.SecurityOpenNotifyEvent{
		SessionId:    "s1",
		Tag:          "s1/NOTIFY_KIND_ATTENTION",
		ExpectedKind: v1.NotifyKind_NOTIFY_KIND_ATTENTION,
	}
	appHdr := devicesec.Header{Machine: "m1", Session: "s1", Request: "ap-1"}
	aOK := buildNotifyDisplay(appEv, appHdr, appNC)
	if !aOK.Valid || aOK.ShowDetailedByDefault || aOK.ApprovalId != "ap-1" || !aOK.Truncated || aOK.Title.GetKey() != text.KeyListStateRequestCommandPlain || aOK.BigText.GetLiteral() != "Run Go tests\ngo test ./..." {
		t.Fatalf("expected valid approval display, got %+v", aOK)
	}

	// Table-driven rejection cases: every missing/mismatched required field, disallowed kind,
	// and payload violation must fail closed with GenericBody set and no plaintext fields leaked.
	rejectCases := []struct {
		name           string
		ev             *corev1.SecurityOpenNotifyEvent
		h              devicesec.Header
		nc             *v1.NotifyContent
		wantGenericKey string
	}{
		{
			name:           "nil event",
			ev:             nil,
			h:              appHdr,
			nc:             appNC,
			wantGenericKey: text.KeyNotificationsAndroidASessionNeedsYourAttention,
		},
		{
			name:           "missing expected kind",
			ev:             &corev1.SecurityOpenNotifyEvent{MachineId: "m1", Tag: "m1/NOTIFY_KIND_MACHINE_DISK"},
			h:              diskHdr,
			nc:             diskNC,
			wantGenericKey: text.KeyMachineStatusHomeWarning,
		},
		{
			name:           "kind mismatch sets expected generic body",
			ev:             schedEv,
			h:              appHdr,
			nc:             appNC,
			wantGenericKey: text.KeyNotificationsScheduledTasksHaveNewResults,
		},
		{
			name: "security kind disallowed",
			ev: &corev1.SecurityOpenNotifyEvent{
				SessionId: "s1", Tag: "s1/NOTIFY_KIND_SECURITY", ExpectedKind: v1.NotifyKind_NOTIFY_KIND_SECURITY,
			},
			h:              devicesec.Header{Machine: "m1", Session: "s1", Request: "sec:1"},
			nc:             &v1.NotifyContent{Kind: v1.NotifyKind_NOTIFY_KIND_SECURITY, MachineId: "m1", SessionId: "s1", Title: "secret"},
			wantGenericKey: text.KeyNotificationsAndroidASessionNeedsYourAttention,
		},
		{
			name: "unknown kind disallowed",
			ev: &corev1.SecurityOpenNotifyEvent{
				SessionId: "s1", Tag: "s1/99", ExpectedKind: v1.NotifyKind(99),
			},
			h:              devicesec.Header{Machine: "m1", Session: "s1", Request: "x"},
			nc:             &v1.NotifyContent{Kind: v1.NotifyKind(99), MachineId: "m1", SessionId: "s1", Text: "secret"},
			wantGenericKey: text.KeyNotificationsAndroidASessionNeedsYourAttention,
		},
		{
			name: "disk missing machine_id in event",
			ev: &corev1.SecurityOpenNotifyEvent{
				Tag: "m1/NOTIFY_KIND_MACHINE_DISK", ExpectedKind: v1.NotifyKind_NOTIFY_KIND_MACHINE_DISK,
			},
			h:              diskHdr,
			nc:             diskNC,
			wantGenericKey: text.KeyMachineStatusHomeWarning,
		},
		{
			name: "disk missing tag in event",
			ev: &corev1.SecurityOpenNotifyEvent{
				MachineId: "m1", ExpectedKind: v1.NotifyKind_NOTIFY_KIND_MACHINE_DISK,
			},
			h:              diskHdr,
			nc:             diskNC,
			wantGenericKey: text.KeyMachineStatusHomeWarning,
		},
		{
			name:           "disk empty request id in header",
			ev:             diskEv,
			h:              devicesec.Header{Machine: "m1", Request: ""},
			nc:             diskNC,
			wantGenericKey: text.KeyMachineStatusHomeWarning,
		},
		{
			name:           "disk mismatched request id",
			ev:             diskEv,
			h:              devicesec.Header{Machine: "m1", Request: "disk:999:3"},
			nc:             diskNC,
			wantGenericKey: text.KeyMachineStatusHomeWarning,
		},
		{
			name: "disk available bytes over critical threshold",
			ev:   diskEv,
			h:    diskHdr,
			nc: &v1.NotifyContent{
				Kind: v1.NotifyKind_NOTIFY_KIND_MACHINE_DISK, MachineId: "m1", MachineName: "MacBook",
				DiskStatus: &v1.DiskStatus{ObservedMs: 1_700_000_000_000, AvailableBytes: 3_000_000_000, Pressure: v1.ResourcePressure_RESOURCE_PRESSURE_CRITICAL},
			},
			wantGenericKey: text.KeyMachineStatusHomeWarning,
		},
		{
			name: "disk non-empty session_id rejected",
			ev: &corev1.SecurityOpenNotifyEvent{
				MachineId: "m1", SessionId: "s1", Tag: "m1/NOTIFY_KIND_MACHINE_DISK", ExpectedKind: v1.NotifyKind_NOTIFY_KIND_MACHINE_DISK,
			},
			h:              diskHdr,
			nc:             diskNC,
			wantGenericKey: text.KeyMachineStatusHomeWarning,
		},
		{
			name: "scheduled missing event_key",
			ev: &corev1.SecurityOpenNotifyEvent{
				SessionId: "s1", Tag: "s1/scheduled:7/NOTIFY_KIND_SCHEDULED", ExpectedKind: v1.NotifyKind_NOTIFY_KIND_SCHEDULED,
			},
			h:              schedHdr,
			nc:             schedNC,
			wantGenericKey: text.KeyNotificationsScheduledTasksHaveNewResults,
		},
		{
			name: "scheduled mismatched event_key",
			ev: &corev1.SecurityOpenNotifyEvent{
				SessionId: "s1", Tag: "s1/scheduled:7/NOTIFY_KIND_SCHEDULED", EventKey: "scheduled:8", ExpectedKind: v1.NotifyKind_NOTIFY_KIND_SCHEDULED,
			},
			h:              schedHdr,
			nc:             schedNC,
			wantGenericKey: text.KeyNotificationsScheduledTasksHaveNewResults,
		},
		{
			name: "scheduled missing tag",
			ev: &corev1.SecurityOpenNotifyEvent{
				SessionId: "s1", EventKey: "scheduled:7", ExpectedKind: v1.NotifyKind_NOTIFY_KIND_SCHEDULED,
			},
			h:              schedHdr,
			nc:             schedNC,
			wantGenericKey: text.KeyNotificationsScheduledTasksHaveNewResults,
		},
		{
			name:           "scheduled empty request id",
			ev:             schedEv,
			h:              devicesec.Header{Machine: "m1", Session: "s1", Request: ""},
			nc:             schedNC,
			wantGenericKey: text.KeyNotificationsScheduledTasksHaveNewResults,
		},
		{
			name: "scheduled oversized title",
			ev:   schedEv,
			h:    schedHdr,
			nc: &v1.NotifyContent{
				Kind: v1.NotifyKind_NOTIFY_KIND_SCHEDULED, SessionId: "s1", MachineId: "m1", SourceEventSeq: 7,
				Title: strings.Repeat("a", 61), Text: "ok",
			},
			wantGenericKey: text.KeyNotificationsScheduledTasksHaveNewResults,
		},
		{
			name: "scheduled oversized text",
			ev:   schedEv,
			h:    schedHdr,
			nc: &v1.NotifyContent{
				Kind: v1.NotifyKind_NOTIFY_KIND_SCHEDULED, SessionId: "s1", MachineId: "m1", SourceEventSeq: 7,
				Title: "ok", Text: strings.Repeat("b", 201),
			},
			wantGenericKey: text.KeyNotificationsScheduledTasksHaveNewResults,
		},
		{
			name: "turn ended missing tag",
			ev: &corev1.SecurityOpenNotifyEvent{
				SessionId: "s1", ExpectedKind: v1.NotifyKind_NOTIFY_KIND_TURN_ENDED,
			},
			h:              turnHdr,
			nc:             turnNC,
			wantGenericKey: text.KeyNotificationsAndroidAConversationTurnFinished,
		},
		{
			name:           "turn ended empty request id",
			ev:             turnEv,
			h:              devicesec.Header{Machine: "m1", Session: "s1", Request: ""},
			nc:             turnNC,
			wantGenericKey: text.KeyNotificationsAndroidAConversationTurnFinished,
		},
		{
			name: "turn ended missing session_id in event",
			ev: &corev1.SecurityOpenNotifyEvent{
				Tag: "s1/NOTIFY_KIND_TURN_ENDED", ExpectedKind: v1.NotifyKind_NOTIFY_KIND_TURN_ENDED,
			},
			h:              turnHdr,
			nc:             turnNC,
			wantGenericKey: text.KeyNotificationsAndroidAConversationTurnFinished,
		},
		{
			name: "attention missing tag",
			ev: &corev1.SecurityOpenNotifyEvent{
				SessionId: "s1", ExpectedKind: v1.NotifyKind_NOTIFY_KIND_ATTENTION,
			},
			h:              appHdr,
			nc:             appNC,
			wantGenericKey: text.KeyNotificationsAndroidASessionNeedsYourAttention,
		},
		{
			name:           "attention empty request id",
			ev:             appEv,
			h:              devicesec.Header{Machine: "m1", Session: "s1", Request: ""},
			nc:             appNC,
			wantGenericKey: text.KeyNotificationsAndroidASessionNeedsYourAttention,
		},
		{
			name:           "attention mismatched approval request id",
			ev:             appEv,
			h:              devicesec.Header{Machine: "m1", Session: "s1", Request: "ap-other"},
			nc:             appNC,
			wantGenericKey: text.KeyNotificationsAndroidASessionNeedsYourAttention,
		},
		{
			name:           "attention header machine mismatch",
			ev:             appEv,
			h:              devicesec.Header{Machine: "m-other", Session: "s1", Request: "ap-1"},
			nc:             appNC,
			wantGenericKey: text.KeyNotificationsAndroidASessionNeedsYourAttention,
		},
	}

	for _, tc := range rejectCases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildNotifyDisplay(tc.ev, tc.h, tc.nc)
			if got.GetValid() {
				t.Fatalf("expected Valid=false, got %+v", got)
			}
			if got.GetFullGenericBody().GetKey() != tc.wantGenericKey {
				t.Fatalf("GenericBody key = %q, want %q", got.GetFullGenericBody().GetKey(), tc.wantGenericKey)
			}
			if got.GetTitle() != nil || got.GetBody() != nil || got.GetSubText() != nil || got.GetBigText() != nil ||
				got.GetApprovalId() != "" || got.GetTag() != "" || got.GetSessionId() != "" || got.GetMachineId() != "" {
				t.Fatalf("invalid display must not leak plaintext/routing fields: %+v", got)
			}
		})
	}

	// Verify finishNotifyPlain emits fail-closed display with no plaintext fields on invalid notifications.
	eng := NewEngine(&entropy.Pool{}, 0)
	plainBytes, err := proto.Marshal(&stdv1.NotifyContent{
		Kind:           stdv1.NotifyKind_NOTIFY_KIND_SCHEDULED,
		MachineId:      schedNC.MachineId,
		SessionId:      schedNC.SessionId,
		SourceEventSeq: schedNC.SourceEventSeq,
		Title:          schedNC.Title,
		Text:           schedNC.Text,
	})
	if err != nil {
		t.Fatal(err)
	}
	eng.finishNotifyPlain(
		&corev1.SecurityOpenNotifyEvent{
			OpId:         42,
			Envelope:     &v1.Envelope{Header: []byte("hdr")},
			SessionId:    "s1",
			Tag:          "s1/scheduled:7/NOTIFY_KIND_SCHEDULED",
			EventKey:     "scheduled:999", // mismatch -> invalid
			ExpectedKind: v1.NotifyKind_NOTIFY_KIND_SCHEDULED,
		},
		devicesec.Header{Purpose: devicesec.Notify, Machine: "m1", Session: "s1", Request: "scheduled:7"},
		plainBytes,
		false,
	)
	effs := eng.DrainEffects()
	if len(effs) != 1 || effs[0].GetSecurityNotifyOpened() == nil {
		t.Fatalf("expected 1 SecurityNotifyOpenedEffect, got %+v", effs)
	}
	opened := effs[0].GetSecurityNotifyOpened()
	if opened.GetDisplay().GetValid() || opened.GetDisplay().GetFullGenericBody().GetKey() != text.KeyNotificationsScheduledTasksHaveNewResults {
		t.Fatalf("expected invalid display with scheduled generic body, got %+v", opened.GetDisplay())
	}
	if opened.GetDisplay().GetTitle() != nil || opened.GetDisplay().GetBody() != nil || opened.GetDisplay().GetSessionId() != "" || opened.GetDisplay().GetMachineId() != "" {
		t.Fatalf("invalid SecurityNotifyDisplay must not carry plaintext fields, got %+v", opened.GetDisplay())
	}
}

func TestBootstrapRejectsMissingDeviceKeysAndClearResetsInvalid(t *testing.T) {
	relay := newFakeRelay(1_700_000_000_000)
	h := newFakeClientHarness(t, relay, 0x71)
	_ = h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	prep := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareCreation{
			PrepareCreation: &corev1.SecurityPrepareCreationAction{Server: "https://relay.example.com", StoragePersisted: true},
		},
	})
	if !prep.Ok {
		t.Fatalf("PrepareCreation failed: %+v", prep)
	}
	_ = h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ConfirmCreationCodeSaved{ConfirmCreationCodeSaved: &corev1.SecurityActionEmpty{}},
	})
	cont := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ContinueCreation{ContinueCreation: &corev1.SecurityStringAction{}},
	})
	if !cont.Ok || cont.Status.Bootstrap != corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_READY {
		t.Fatalf("ContinueCreation failed: %+v", cont)
	}
	advice := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_CheckExitAdvice{CheckExitAdvice: &corev1.SecurityActionEmpty{}},
	})
	if !advice.Ok || advice.ExitAdvice == nil || !advice.ExitAdvice.Authority ||
		advice.ExitAdvice.Message != text.KeySecurityThisDeviceIsTheOnlyManagingClientTransferManagement {
		t.Fatalf("expected device-neutral sole authority exit advice, got %+v", advice)
	}

	// Transient storage/Keystore/Keychain lock errors must NOT be classified as INVALID,
	// and retrying bootstrap after unlock must recover cleanly to READY without resetting.
	h.storageReadErr = "keystore temporarily locked"
	h.restart("https://relay.example.com", false)
	resLockedRead := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	if resLockedRead.Ok || resLockedRead.StructuredErrorCode != corev1.SecurityErrorCode_SECURITY_ERROR_CODE_STORAGE ||
		!resLockedRead.Retryable || resLockedRead.Status.Bootstrap == corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_INVALID {
		t.Fatalf("expected transient STORAGE error (not INVALID) on locked storage read, got %+v", resLockedRead)
	}
	h.storageReadErr = ""

	h.keyAgreeErr = "keychain interaction not allowed"
	h.restart("https://relay.example.com", false)
	resLockedAgree := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	if resLockedAgree.Ok || resLockedAgree.StructuredErrorCode != corev1.SecurityErrorCode_SECURITY_ERROR_CODE_STORAGE ||
		!resLockedAgree.Retryable || resLockedAgree.Status.Bootstrap == corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_INVALID {
		t.Fatalf("expected transient STORAGE error (not INVALID) on locked key agree, got %+v", resLockedAgree)
	}
	h.keyAgreeErr = ""

	resUnlocked := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	if !resUnlocked.Ok || resUnlocked.Status.Bootstrap != corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_READY {
		t.Fatalf("expected bootstrap retry after unlock to succeed as READY, got %+v", resUnlocked)
	}

	// Re-bootstrapping with a missing device Ed25519 key must fail as INVALID, not READY,
	// while checkExitAdvice still identifies this device as sole authority from the verified record.
	savedEd := h.deviceEd
	h.deviceEd = nil
	h.restart("https://relay.example.com", false)
	resEd := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	if resEd.Ok || resEd.Status.Bootstrap != corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_INVALID {
		t.Fatalf("expected INVALID bootstrap when device Ed key missing, got %+v", resEd)
	}
	adviceMissingKey := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_CheckExitAdvice{CheckExitAdvice: &corev1.SecurityActionEmpty{}},
	})
	if !adviceMissingKey.Ok || adviceMissingKey.ExitAdvice == nil || !adviceMissingKey.ExitAdvice.Authority ||
		adviceMissingKey.ExitAdvice.Message != text.KeySecurityThisDeviceIsTheOnlyManagingClientTransferManagement {
		t.Fatalf("expected authority exit advice even when device Ed key missing, got %+v", adviceMissingKey)
	}
	h.deviceEd = savedEd

	// Re-bootstrapping with a missing device X25519 key must fail as INVALID, not READY.
	savedX := h.deviceX
	h.deviceX = nil
	h.restart("https://relay.example.com", false)
	resX := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	if resX.Ok || resX.Status.Bootstrap != corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_INVALID {
		t.Fatalf("expected INVALID bootstrap when device X key missing, got %+v", resX)
	}
	h.deviceX = savedX

	// ClearSecurity must succeed from INVALID state and clear bootstrapInvalid.
	cleared := h.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ClearSecurity{ClearSecurity: &corev1.SecurityActionEmpty{}},
	})
	if !cleared.Ok || cleared.Status.Bootstrap != corev1.SecurityBootstrapKind_SECURITY_BOOTSTRAP_KIND_EMPTY {
		t.Fatalf("expected ClearSecurity to clear INVALID bootstrap state, got %+v", cleared)
	}
}

func TestReplacementCandidateMatchingAndDefaultSelection(t *testing.T) {
	now := int64(1_700_100_000_000)
	fpCases := []struct {
		name         string
		meta         *v1.DeviceMeta
		wantPlat     string
		wantModel    string
		wantSpecific bool
	}{
		{"android_specific_model", &v1.DeviceMeta{Platform: "android", Description: "Android 15 · Pixel 9"}, "android", "pixel 9", true},
		{"android_os_upgrade_same_model", &v1.DeviceMeta{Platform: "android", Description: "Android 14 · Pixel 9"}, "android", "pixel 9", true},
		{"android_missing_model", &v1.DeviceMeta{Platform: "android", Description: "Android 15"}, "android", "android 15", false},
		{"android_generic_model", &v1.DeviceMeta{Platform: "android", Description: "Android 15 · Android"}, "android", "android", false},
		{"ios_specific_iphone_model", &v1.DeviceMeta{Platform: "ios", Description: "iOS 18.4 · iPhone16,1"}, "ios", "iphone16,1", true},
		{"ios_specific_ipad_model", &v1.DeviceMeta{Platform: "ios", Description: "iOS 18.4 · iPad14,3"}, "ios", "ipad14,3", true},
		{"ios_generic_iphone_fallback", &v1.DeviceMeta{Platform: "ios", Description: "iOS 18.4 · iPhone"}, "ios", "iphone", false},
		{"ios_generic_ipad_fallback", &v1.DeviceMeta{Platform: "ios", Description: "iOS 18.4 · iPad"}, "ios", "ipad", false},
		{"web_coarse_descriptor", &v1.DeviceMeta{Platform: "web", Description: "Chrome on macOS"}, "web", "chrome on macos", false},
		{"empty_meta", nil, "", "", false},
	}
	for _, tc := range fpCases {
		t.Run("fingerprint/"+tc.name, func(t *testing.T) {
			p, m, spec := deviceModelFingerprint(tc.meta)
			if p != tc.wantPlat || m != tc.wantModel || spec != tc.wantSpecific {
				t.Fatalf("got (%q, %q, %v), want (%q, %q, %v)", p, m, spec, tc.wantPlat, tc.wantModel, tc.wantSpecific)
			}
		})
	}

	selCases := []struct {
		name        string
		specific    bool
		lastSeenMs  int64
		nowMs       int64
		wantDefault bool
	}{
		{"specific_inactive_48h", true, now - 48*3600_000, now, true},
		{"specific_inactive_exactly_24h", true, now - dayMs, now, true},
		{"specific_never_seen", true, 0, now, true},
		{"specific_active_23h59m", true, now - dayMs + 60_000, now, false},
		{"specific_active_just_now", true, now - 5_000, now, false},
		{"specific_active_future_skew", true, now + 60_000, now, false},
		{"non_specific_inactive_7d", false, now - 7*dayMs, now, false},
		{"non_specific_never_seen", false, 0, now, false},
		{"specific_unknown_now", true, now - 48*3600_000, 0, false},
	}
	for _, tc := range selCases {
		t.Run("default_selected/"+tc.name, func(t *testing.T) {
			if got := shouldDefaultSelectReplacement(tc.specific, tc.lastSeenMs, tc.nowMs); got != tc.wantDefault {
				t.Fatalf("got %v, want %v", got, tc.wantDefault)
			}
		})
	}

	timeCases := []struct {
		name       string
		lastSeenMs int64
		wantKey    string
		wantStyle  corev1.TimestampStyle
	}{
		{"no_record", 0, text.KeySettingsNoRecord, corev1.TimestampStyle_TIMESTAMP_STYLE_UNSPECIFIED},
		{"just_now", now - 30_000, text.KeyTimeJustNow, corev1.TimestampStyle_TIMESTAMP_STYLE_UNSPECIFIED},
		{"minutes_ago", now - 5*60_000, text.KeyTimeMinutesAgo, corev1.TimestampStyle_TIMESTAMP_STYLE_UNSPECIFIED},
		{"hours_ago", now - 3*3600_000, text.KeyTimeHoursAgo, corev1.TimestampStyle_TIMESTAMP_STYLE_UNSPECIFIED},
		{"yesterday", now - 30*3600_000, text.KeyTimeYesterday, corev1.TimestampStyle_TIMESTAMP_STYLE_UNSPECIFIED},
		{"older_date", now - 5*dayMs, "", corev1.TimestampStyle_TIMESTAMP_STYLE_DATE},
	}
	for _, tc := range timeCases {
		t.Run("last_active/"+tc.name, func(t *testing.T) {
			got := formatCandidateLastActive(tc.lastSeenMs, now)
			if tc.wantKey != "" && got.GetKey() != tc.wantKey {
				t.Fatalf("got key %q, want %q", got.GetKey(), tc.wantKey)
			}
			if tc.wantStyle != corev1.TimestampStyle_TIMESTAMP_STYLE_UNSPECIFIED && got.GetTimestamp().GetStyle() != tc.wantStyle {
				t.Fatalf("got timestamp style %v, want %v", got.GetTimestamp().GetStyle(), tc.wantStyle)
			}
		})
	}
}

func TestWorkflowPairingReplacementCandidatesAndPostApprovalRemoval(t *testing.T) {
	// Verify fingerprint normalization across OS upgrades and platforms.
	if p1, m1, s1 := deviceModelFingerprint(&v1.DeviceMeta{Platform: "android", Description: "Android 14 · Pixel 3"}); p1 != "android" || m1 != "pixel 3" || !s1 {
		t.Fatalf("unexpected android fingerprint: %q %q %v", p1, m1, s1)
	}
	if p2, m2, s2 := deviceModelFingerprint(&v1.DeviceMeta{Platform: "android", Description: "Android 15 · Pixel 3"}); p2 != "android" || m2 != "pixel 3" || !s2 {
		t.Fatalf("unexpected android upgrade fingerprint: %q %q %v", p2, m2, s2)
	}
	if p3, m3, s3 := deviceModelFingerprint(&v1.DeviceMeta{Platform: "web", Description: "Chrome on macOS"}); p3 != "web" || m3 != "chrome on macos" || s3 {
		t.Fatalf("unexpected web fingerprint: %q %q %v", p3, m3, s3)
	}

	relay := newFakeRelay(1_700_000_000_000)

	// Authority A (phone) creates the account.
	a := newFakeClientHarness(t, relay, 0x81)
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareCreation{
			PrepareCreation: &corev1.SecurityPrepareCreationAction{Server: "https://relay.example.com"},
		},
	})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ConfirmCreationCodeSaved{ConfirmCreationCodeSaved: &corev1.SecurityActionEmpty{}},
	})
	if cont := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ContinueCreation{ContinueCreation: &corev1.SecurityStringAction{}},
	}); !cont.Ok {
		t.Fatalf("ContinueCreation failed: %+v", cont)
	}

	// Pair initial web browser B and another device C (Android).
	pairClient := func(tag byte, platform, desc string) (*fakeClientHarness, string) {
		p := a.runAction(&corev1.SecurityWorkflowActionEvent{
			Action: &corev1.SecurityWorkflowActionEvent_StartPairing{StartPairing: &corev1.SecurityStartPairingAction{}},
		})
		if !p.Ok {
			t.Fatalf("StartPairing failed: %+v", p)
		}
		h := newFakeClientHarness(t, relay, tag)
		_ = h.runAction(&corev1.SecurityWorkflowActionEvent{
			Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
		})
		joinID := h.startAction(&corev1.SecurityWorkflowActionEvent{
			Action: &corev1.SecurityWorkflowActionEvent_StartJoinPairing{
				StartJoinPairing: &corev1.SecurityStartJoinPairingAction{
					Server: "https://relay.example.com", Code: p.Pairing.Code,
					Platform: platform, Description: desc,
				},
			},
		})
		_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
			Action: &corev1.SecurityWorkflowActionEvent_PollPairing{PollPairing: &corev1.SecurityStringAction{Value: p.Pairing.PairingId}},
		})
		h.pumpPollTicks(5, func() bool { return h.actionResults[joinID] != nil })
		confID := h.startAction(&corev1.SecurityWorkflowActionEvent{
			Action: &corev1.SecurityWorkflowActionEvent_ConfirmJoinSas{ConfirmJoinSas: &corev1.SecurityStringAction{Value: p.Pairing.PairingId}},
		})
		_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
			Action: &corev1.SecurityWorkflowActionEvent_ApprovePairing{
				ApprovePairing: &corev1.SecurityApprovePairingAction{PairingId: p.Pairing.PairingId, SasConfirmed: true},
			},
		})
		h.pumpPollTicks(5, func() bool { return h.actionResults[confID] != nil })
		_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
			Action: &corev1.SecurityWorkflowActionEvent_FinishPairingDelivery{FinishPairingDelivery: &corev1.SecurityStringAction{Value: p.Pairing.PairingId}},
		})
		return h, h.eng.selfDeviceID
	}

	_, oldWebID := pairClient(0x82, "web", "Chrome on macOS")
	_, otherAndroidID := pairClient(0x83, "android", "Android 14 · Pixel 3")

	// Supply device directory to Authority A (with matching oldWebID and non-matching otherAndroidID).
	dir := []DeviceOfferInfo{
		{ID: a.eng.selfDeviceID, Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, Meta: &v1.DeviceMeta{Platform: "web", Description: "Chrome on macOS"}},
		{ID: oldWebID, Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, Meta: &v1.DeviceMeta{Name: "Work Chrome", Platform: "web", Description: "Chrome on macOS"}},
		{ID: otherAndroidID, Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, Meta: &v1.DeviceMeta{Platform: "android", Description: "Android 14 · Pixel 3"}},
	}
	a.eng.SetDeviceDirectory(dir)

	// Re-pair the same web browser as a new identity B2 after local storage clear.
	p2 := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartPairing{StartPairing: &corev1.SecurityStartPairingAction{}},
	}, dir...)
	b2 := newFakeClientHarness(t, relay, 0x84)
	_ = b2.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	b2Join := b2.startAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartJoinPairing{
			StartJoinPairing: &corev1.SecurityStartJoinPairingAction{
				Server: "https://relay.example.com", Code: p2.Pairing.Code,
				Platform: "web", Description: "Chrome on macOS",
			},
		},
	})
	// Authority reveals SAS; then joiner B2 receives REVEALED and uploads encrypted joiner_meta.
	pollReveal := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PollPairing{PollPairing: &corev1.SecurityStringAction{Value: p2.Pairing.PairingId}},
	}, dir...)
	if !pollReveal.Ok || !pollReveal.Pairing.Joined || len(pollReveal.Pairing.SasEmoji) != 4 {
		t.Fatalf("expected joined SAS on first poll, got %+v", pollReveal)
	}
	b2.pumpPollTicks(5, func() bool { return b2.actionResults[b2Join] != nil })
	if b2.actionResults[b2Join] == nil || !b2.actionResults[b2Join].Ok {
		t.Fatalf("joiner B2 failed to reach SAS: %+v", b2.actionResults[b2Join])
	}

	// Authority polls again and receives decrypted joiner_meta + replacement candidate (only oldWebID, not self/authority or otherAndroidID).
	pollMeta := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PollPairing{PollPairing: &corev1.SecurityStringAction{Value: p2.Pairing.PairingId}},
	}, dir...)
	if !pollMeta.Ok || !pollMeta.Pairing.JoinerMetaReceived {
		t.Fatalf("expected joiner_meta received, got %+v", pollMeta.Pairing)
	}
	if len(pollMeta.Pairing.ReplacementCandidates) != 1 || pollMeta.Pairing.ReplacementCandidates[0].DeviceId != oldWebID || pollMeta.Pairing.ReplacementCandidates[0].DisplayName != "Work Chrome" {
		t.Fatalf("expected only oldWebID candidate, got %+v", pollMeta.Pairing.ReplacementCandidates)
	}
	if pollMeta.Pairing.ReplacementCandidates[0].DefaultSelected {
		t.Fatalf("expected coarse web candidate DefaultSelected=false, got %+v", pollMeta.Pairing.ReplacementCandidates[0])
	}
	if pollMeta.Pairing.ReplacementCandidates[0].GetLastActiveText() == nil {
		t.Fatalf("expected core-formatted LastActiveText on candidate, got %+v", pollMeta.Pairing.ReplacementCandidates[0])
	}
	if pollMeta.Pairing.GetReplaceOldLabel().GetKey() != text.KeySecurityAlsoRemoveOldDevices ||
		pollMeta.Pairing.GetReplaceOldHint().GetKey() != text.KeySecurityAlsoRemoveOldDevicesHint {
		t.Fatalf("expected Core i18n copy on pairing info, got label=%+v hint=%+v", pollMeta.Pairing.ReplaceOldLabel, pollMeta.Pairing.ReplaceOldHint)
	}

	// Confirm SAS on B2, approve on A with RemoveDeviceIds=[oldWebID].
	b2Conf := b2.startAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ConfirmJoinSas{ConfirmJoinSas: &corev1.SecurityStringAction{Value: p2.Pairing.PairingId}},
	})
	appRes := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ApprovePairing{
			ApprovePairing: &corev1.SecurityApprovePairingAction{
				PairingId:       p2.Pairing.PairingId,
				SasConfirmed:    true,
				RemoveDeviceIds: []string{oldWebID},
			},
		},
	}, dir...)
	if !appRes.Ok {
		t.Fatalf("ApprovePairing failed: %+v", appRes)
	}
	// Before delivery completes, oldWebID is still a member (removal waits until approval/delivery succeeds).
	if _, live := a.eng.head.Core.Member(oldWebID); !live {
		t.Fatalf("expected oldWebID to stay until pairing delivery succeeds")
	}

	b2.pumpPollTicks(5, func() bool { return b2.actionResults[b2Conf] != nil })
	if b2.actionResults[b2Conf] == nil || !b2.actionResults[b2Conf].Ok {
		t.Fatalf("B2 failed to finish registration: %+v", b2.actionResults[b2Conf])
	}
	finRes := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_FinishPairingDelivery{FinishPairingDelivery: &corev1.SecurityStringAction{Value: p2.Pairing.PairingId}},
	}, dir...)
	if !finRes.Ok || !finRes.Pairing.Delivered {
		t.Fatalf("FinishPairingDelivery failed: %+v", finRes)
	}

	// After delivery completes, oldWebID must be removed while B2 and otherAndroidID remain members.
	if _, live := a.eng.head.Core.Member(oldWebID); live {
		t.Fatalf("expected oldWebID to be removed after pairing delivery succeeded")
	}
	if _, live := a.eng.head.Core.Member(b2.eng.selfDeviceID); !live {
		t.Fatalf("expected newly paired B2 to remain a member")
	}
	if _, live := a.eng.head.Core.Member(otherAndroidID); !live {
		t.Fatalf("expected unrelated otherAndroidID to remain a member")
	}

	// Verify background ResumePendingOp delivery path (e.g. Web continuePairing timer)
	// also removes requested old devices once delivery completes.
	b2ID := b2.eng.selfDeviceID
	dir2 := []DeviceOfferInfo{
		{ID: a.eng.selfDeviceID, Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, Meta: &v1.DeviceMeta{Platform: "web", Description: "Chrome on macOS"}},
		{ID: b2ID, Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, Meta: &v1.DeviceMeta{Platform: "web", Description: "Chrome on macOS"}},
		{ID: otherAndroidID, Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, Meta: &v1.DeviceMeta{Platform: "android", Description: "Android 14 · Pixel 3"}},
	}
	a.eng.SetDeviceDirectory(dir2)
	p3 := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartPairing{StartPairing: &corev1.SecurityStartPairingAction{}},
	}, dir2...)
	b3 := newFakeClientHarness(t, relay, 0x85)
	_ = b3.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	b3Join := b3.startAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartJoinPairing{
			StartJoinPairing: &corev1.SecurityStartJoinPairingAction{
				Server: "https://relay.example.com", Code: p3.Pairing.Code,
				Platform: "web", Description: "Chrome on macOS",
			},
		},
	})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PollPairing{PollPairing: &corev1.SecurityStringAction{Value: p3.Pairing.PairingId}},
	}, dir2...)
	b3.pumpPollTicks(5, func() bool { return b3.actionResults[b3Join] != nil })
	pollMeta3 := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PollPairing{PollPairing: &corev1.SecurityStringAction{Value: p3.Pairing.PairingId}},
	}, dir2...)
	if !pollMeta3.Ok || len(pollMeta3.Pairing.ReplacementCandidates) != 1 || pollMeta3.Pairing.ReplacementCandidates[0].DeviceId != b2ID {
		t.Fatalf("expected b2ID candidate on re-pair, got %+v", pollMeta3.Pairing)
	}
	b3Conf := b3.startAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ConfirmJoinSas{ConfirmJoinSas: &corev1.SecurityStringAction{Value: p3.Pairing.PairingId}},
	})
	appRes3 := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ApprovePairing{
			ApprovePairing: &corev1.SecurityApprovePairingAction{
				PairingId:       p3.Pairing.PairingId,
				SasConfirmed:    true,
				RemoveDeviceIds: []string{b2ID},
			},
		},
	}, dir2...)
	if !appRes3.Ok {
		t.Fatalf("ApprovePairing (p3) failed: %+v", appRes3)
	}
	b3.pumpPollTicks(5, func() bool { return b3.actionResults[b3Conf] != nil })
	if b3.actionResults[b3Conf] == nil || !b3.actionResults[b3Conf].Ok {
		t.Fatalf("B3 failed to finish registration: %+v", b3.actionResults[b3Conf])
	}
	// Complete delivery via ResumePendingOp instead of FinishPairingDelivery!
	resOp := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ResumePendingOp{ResumePendingOp: &corev1.SecurityActionEmpty{}},
	}, dir2...)
	if !resOp.Ok {
		t.Fatalf("ResumePendingOp failed: %+v", resOp)
	}
	if _, live := a.eng.head.Core.Member(b2ID); live {
		t.Fatalf("expected b2ID to be removed when delivery completes via ResumePendingOp")
	}
	if _, live := a.eng.head.Core.Member(b3.eng.selfDeviceID); !live {
		t.Fatalf("expected B3 to remain a member")
	}
}

func TestApprovePairingRejectsInvalidRemoveDeviceIDs(t *testing.T) {
	relay := newFakeRelay(1_700_000_000_000)
	a := newFakeClientHarness(t, relay, 0x91)
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareCreation{
			PrepareCreation: &corev1.SecurityPrepareCreationAction{Server: "https://relay.example.com"},
		},
	})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ConfirmCreationCodeSaved{ConfirmCreationCodeSaved: &corev1.SecurityActionEmpty{}},
	})
	if cont := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ContinueCreation{ContinueCreation: &corev1.SecurityStringAction{}},
	}); !cont.Ok {
		t.Fatalf("ContinueCreation failed: %+v", cont)
	}

	pairMember := func(seed byte, kind v1.DeviceKind, platform, desc string) string {
		p := a.runAction(&corev1.SecurityWorkflowActionEvent{
			Action: &corev1.SecurityWorkflowActionEvent_StartPairing{StartPairing: &corev1.SecurityStartPairingAction{}},
		})
		if kind == v1.DeviceKind_DEVICE_KIND_MACHINE {
			machEd := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, 32))
			machX, _ := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{seed + 1}, 32))
			machEphem, _ := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{seed + 2}, 32))
			machMem, _ := devicesec.NewMember(machEd.Public().(ed25519.PublicKey), machX.PublicKey().Bytes(), devicesec.Machine)
			joinBytes, _ := (&v1.JoinPairingRequest{
				Code:                p.Pairing.Code,
				JoinerPublic:        machEphem.PublicKey().Bytes(),
				JoinerKind:          v1.DeviceKind_DEVICE_KIND_MACHINE,
				DevicePublicKey:     machMem.EdPublic[:],
				DeviceHpkePublicKey: machMem.XPublic[:],
				ProtocolVersion:     testRelayVersion,
			}).Marshal()
			if _, rpcErr := relay.call(corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_JOIN_PAIRING, "", joinBytes); rpcErr != nil {
				t.Fatalf("machine join failed: %+v", rpcErr)
			}
			_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
				Action: &corev1.SecurityWorkflowActionEvent_PollPairing{PollPairing: &corev1.SecurityStringAction{Value: p.Pairing.PairingId}},
			})
			app := a.runAction(&corev1.SecurityWorkflowActionEvent{
				Action: &corev1.SecurityWorkflowActionEvent_ApprovePairing{
					ApprovePairing: &corev1.SecurityApprovePairingAction{PairingId: p.Pairing.PairingId, SasConfirmed: true},
				},
			})
			if !app.Ok {
				t.Fatalf("machine approve failed: %+v", app)
			}
			relay.mu.Lock()
			if pr := relay.pairings[p.Pairing.PairingId]; pr != nil {
				pr.state = v1.PairingState_PAIRING_STATE_DELIVERED
			}
			relay.mu.Unlock()
			_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
				Action: &corev1.SecurityWorkflowActionEvent_FinishPairingDelivery{FinishPairingDelivery: &corev1.SecurityStringAction{Value: p.Pairing.PairingId}},
			})
			return machMem.ID
		}
		h := newFakeClientHarness(t, relay, seed)
		_ = h.runAction(&corev1.SecurityWorkflowActionEvent{
			Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
		})
		jID := h.startAction(&corev1.SecurityWorkflowActionEvent{
			Action: &corev1.SecurityWorkflowActionEvent_StartJoinPairing{
				StartJoinPairing: &corev1.SecurityStartJoinPairingAction{
					Server: "https://relay.example.com", Code: p.Pairing.Code, Platform: platform, Description: desc,
				},
			},
		})
		_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
			Action: &corev1.SecurityWorkflowActionEvent_PollPairing{PollPairing: &corev1.SecurityStringAction{Value: p.Pairing.PairingId}},
		})
		h.pumpPollTicks(5, func() bool { return h.actionResults[jID] != nil })
		cID := h.startAction(&corev1.SecurityWorkflowActionEvent{
			Action: &corev1.SecurityWorkflowActionEvent_ConfirmJoinSas{ConfirmJoinSas: &corev1.SecurityStringAction{Value: p.Pairing.PairingId}},
		})
		_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
			Action: &corev1.SecurityWorkflowActionEvent_ApprovePairing{
				ApprovePairing: &corev1.SecurityApprovePairingAction{PairingId: p.Pairing.PairingId, SasConfirmed: true},
			},
		})
		h.pumpPollTicks(5, func() bool { return h.actionResults[cID] != nil })
		_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
			Action: &corev1.SecurityWorkflowActionEvent_FinishPairingDelivery{FinishPairingDelivery: &corev1.SecurityStringAction{Value: p.Pairing.PairingId}},
		})
		return h.eng.selfDeviceID
	}

	oldWebID := pairMember(0x92, v1.DeviceKind_DEVICE_KIND_CLIENT, "web", "Chrome on macOS")
	otherClientID := pairMember(0x93, v1.DeviceKind_DEVICE_KIND_CLIENT, "android", "Android 15 · Pixel 9")
	machineID := pairMember(0x94, v1.DeviceKind_DEVICE_KIND_MACHINE, "", "")

	dir := []DeviceOfferInfo{
		{ID: a.eng.selfDeviceID, Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, Meta: &v1.DeviceMeta{Platform: "web", Description: "Chrome on macOS"}},
		{ID: oldWebID, Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, Meta: &v1.DeviceMeta{Platform: "web", Description: "Chrome on macOS"}},
		{ID: otherClientID, Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, Meta: &v1.DeviceMeta{Platform: "android", Description: "Android 15 · Pixel 9"}},
		{ID: machineID, Kind: v1.DeviceKind_DEVICE_KIND_MACHINE, Meta: &v1.DeviceMeta{Platform: "web", Description: "Chrome on macOS"}},
	}
	a.eng.SetDeviceDirectory(dir)

	p := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartPairing{StartPairing: &corev1.SecurityStartPairingAction{}},
	}, dir...)
	joiner := newFakeClientHarness(t, relay, 0x95)
	_ = joiner.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	jID := joiner.startAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartJoinPairing{
			StartJoinPairing: &corev1.SecurityStartJoinPairingAction{
				Server: "https://relay.example.com", Code: p.Pairing.Code, Platform: "web", Description: "Chrome on macOS",
			},
		},
	})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PollPairing{PollPairing: &corev1.SecurityStringAction{Value: p.Pairing.PairingId}},
	}, dir...)
	joiner.pumpPollTicks(5, func() bool { return joiner.actionResults[jID] != nil })
	pollMeta := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PollPairing{PollPairing: &corev1.SecurityStringAction{Value: p.Pairing.PairingId}},
	}, dir...)
	if !pollMeta.Ok || len(pollMeta.Pairing.ReplacementCandidates) != 1 || pollMeta.Pairing.ReplacementCandidates[0].DeviceId != oldWebID {
		t.Fatalf("expected only oldWebID candidate, got %+v", pollMeta.Pairing)
	}

	headBefore := a.eng.head.Core.Epoch
	candID := a.eng.wf.initPairings[p.Pairing.PairingId].candidateID
	invalidCases := []struct {
		name string
		ids  []string
	}{
		{"empty_id", []string{""}},
		{"self_authority", []string{a.eng.selfDeviceID}},
		{"machine_member", []string{machineID}},
		{"non_candidate_client", []string{otherClientID}},
		{"candidate_self", []string{candID}},
		{"mixed_valid_and_invalid", []string{oldWebID, machineID}},
	}
	for _, tc := range invalidCases {
		res := a.runAction(&corev1.SecurityWorkflowActionEvent{
			Action: &corev1.SecurityWorkflowActionEvent_ApprovePairing{
				ApprovePairing: &corev1.SecurityApprovePairingAction{
					PairingId:       p.Pairing.PairingId,
					SasConfirmed:    true,
					RemoveDeviceIds: tc.ids,
				},
			},
		}, dir...)
		if res.Ok {
			t.Fatalf("%s: expected ApprovePairing to reject invalid removeDeviceIds %v", tc.name, tc.ids)
		}
		if a.eng.head.Core.Epoch != headBefore {
			t.Fatalf("%s: head epoch changed on rejected approval (%d -> %d)", tc.name, headBefore, a.eng.head.Core.Epoch)
		}
		if a.eng.wf.pendingOp != nil {
			t.Fatalf("%s: expected no pendingOp after rejected approval, got %+v", tc.name, a.eng.wf.pendingOp)
		}
	}

	okRes := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ApprovePairing{
			ApprovePairing: &corev1.SecurityApprovePairingAction{
				PairingId:       p.Pairing.PairingId,
				SasConfirmed:    true,
				RemoveDeviceIds: []string{oldWebID},
			},
		},
	}, dir...)
	if !okRes.Ok {
		t.Fatalf("expected valid candidate subset approval to succeed, got %+v", okRes)
	}
}

func TestPairingOldDeviceRemovalFailurePersistsAndResumesAcrossRestart(t *testing.T) {
	relay := newFakeRelay(1_700_000_000_000)
	a := newFakeClientHarness(t, relay, 0xa1)
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PrepareCreation{
			PrepareCreation: &corev1.SecurityPrepareCreationAction{Server: "https://relay.example.com"},
		},
	})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ConfirmCreationCodeSaved{ConfirmCreationCodeSaved: &corev1.SecurityActionEmpty{}},
	})
	if cont := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ContinueCreation{ContinueCreation: &corev1.SecurityStringAction{}},
	}); !cont.Ok {
		t.Fatalf("ContinueCreation failed: %+v", cont)
	}

	// Enroll old Android device.
	p1 := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartPairing{StartPairing: &corev1.SecurityStartPairingAction{}},
	})
	oldPhone := newFakeClientHarness(t, relay, 0xa2)
	_ = oldPhone.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	j1 := oldPhone.startAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartJoinPairing{
			StartJoinPairing: &corev1.SecurityStartJoinPairingAction{
				Server: "https://relay.example.com", Code: p1.Pairing.Code,
				Platform: "android", Description: "Android 14 · Pixel 9",
			},
		},
	})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PollPairing{PollPairing: &corev1.SecurityStringAction{Value: p1.Pairing.PairingId}},
	})
	oldPhone.pumpPollTicks(5, func() bool { return oldPhone.actionResults[j1] != nil })
	c1 := oldPhone.startAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ConfirmJoinSas{ConfirmJoinSas: &corev1.SecurityStringAction{Value: p1.Pairing.PairingId}},
	})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ApprovePairing{
			ApprovePairing: &corev1.SecurityApprovePairingAction{PairingId: p1.Pairing.PairingId, SasConfirmed: true},
		},
	})
	oldPhone.pumpPollTicks(5, func() bool { return oldPhone.actionResults[c1] != nil })
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_FinishPairingDelivery{FinishPairingDelivery: &corev1.SecurityStringAction{Value: p1.Pairing.PairingId}},
	})
	oldPhoneID := oldPhone.eng.selfDeviceID

	// Re-pair same Pixel 9 model (inactive >24h -> DefaultSelected=true).
	dir := []DeviceOfferInfo{
		{ID: a.eng.selfDeviceID, Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, Meta: &v1.DeviceMeta{Platform: "ios", Description: "iOS 18.4 · iPhone16,1"}},
		{ID: oldPhoneID, Kind: v1.DeviceKind_DEVICE_KIND_CLIENT, LastSeenMs: a.nowMs - 2*dayMs, Meta: &v1.DeviceMeta{Platform: "android", Description: "Android 14 · Pixel 9"}},
	}
	a.eng.SetDeviceDirectory(dir)
	p2 := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartPairing{StartPairing: &corev1.SecurityStartPairingAction{}},
	}, dir...)
	newPhone := newFakeClientHarness(t, relay, 0xa3)
	_ = newPhone.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	j2 := newPhone.startAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_StartJoinPairing{
			StartJoinPairing: &corev1.SecurityStartJoinPairingAction{
				Server: "https://relay.example.com", Code: p2.Pairing.Code,
				Platform: "android", Description: "Android 15 · Pixel 9",
			},
		},
	})
	_ = a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PollPairing{PollPairing: &corev1.SecurityStringAction{Value: p2.Pairing.PairingId}},
	}, dir...)
	newPhone.pumpPollTicks(5, func() bool { return newPhone.actionResults[j2] != nil })
	pollMeta := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_PollPairing{PollPairing: &corev1.SecurityStringAction{Value: p2.Pairing.PairingId}},
	}, dir...)
	if !pollMeta.Ok || len(pollMeta.Pairing.ReplacementCandidates) != 1 || !pollMeta.Pairing.ReplacementCandidates[0].DefaultSelected {
		t.Fatalf("expected DefaultSelected=true for inactive specific Android model, got %+v", pollMeta.Pairing)
	}

	c2 := newPhone.startAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ConfirmJoinSas{ConfirmJoinSas: &corev1.SecurityStringAction{Value: p2.Pairing.PairingId}},
	})
	appRes := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ApprovePairing{
			ApprovePairing: &corev1.SecurityApprovePairingAction{
				PairingId:       p2.Pairing.PairingId,
				SasConfirmed:    true,
				RemoveDeviceIds: []string{oldPhoneID},
			},
		},
	}, dir...)
	if !appRes.Ok {
		t.Fatalf("ApprovePairing failed: %+v", appRes)
	}
	newPhone.pumpPollTicks(5, func() bool { return newPhone.actionResults[c2] != nil })

	// Inject a transient relay failure during the post-delivery old-device removal PutSecurityState.
	relay.mu.Lock()
	relay.failNextMethod = corev1.SecurityRpcMethod_SECURITY_RPC_METHOD_PUT_SECURITY_STATE
	relay.failNextCode = connectCodeUnavailable
	relay.mu.Unlock()

	finRes := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_FinishPairingDelivery{FinishPairingDelivery: &corev1.SecurityStringAction{Value: p2.Pairing.PairingId}},
	}, dir...)
	if !finRes.Ok || !finRes.Pairing.Delivered {
		t.Fatalf("expected pairing delivery to still succeed despite old-device removal failure, got %+v", finRes)
	}
	if finRes.Pairing.GetRemoveOldNotice().GetKey() != text.KeySecurityOldDevicesRemovalIncomplete {
		t.Fatalf("expected RemoveOldNotice when old-device removal failed, got %+v", finRes.Pairing.GetRemoveOldNotice())
	}
	if _, live := a.eng.head.Core.Member(oldPhoneID); !live {
		t.Fatalf("expected oldPhoneID to still be live after failed removal")
	}
	// Notice and pending list must persist inside the encrypted security record across restart.
	a.restart("https://relay.example.com", false)
	boot := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
	})
	if !boot.Ok || len(boot.Status.PendingRemoveDeviceIds) != 1 || boot.Status.PendingRemoveDeviceIds[0] != oldPhoneID {
		t.Fatalf("expected PendingRemoveDeviceIds persisted across restart, got %+v", boot.Status)
	}
	if boot.Status.GetPendingRemoveNotice().GetKey() != text.KeySecurityOldDevicesRemovalIncomplete {
		t.Fatalf("expected PendingRemoveNotice after restart, got %+v", boot.Status.GetPendingRemoveNotice())
	}

	// Resuming pending operations after restart must automatically finish removing oldPhoneID and clear the notice.
	resumed := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_ResumePendingOp{ResumePendingOp: &corev1.SecurityActionEmpty{}},
	})
	if !resumed.Ok {
		t.Fatalf("ResumePendingOp after restart failed: %+v", resumed)
	}
	if _, live := a.eng.head.Core.Member(oldPhoneID); live {
		t.Fatalf("expected oldPhoneID to be removed after restart resume")
	}
	st := a.runAction(&corev1.SecurityWorkflowActionEvent{
		Action: &corev1.SecurityWorkflowActionEvent_GetStatus{GetStatus: &corev1.SecurityActionEmpty{}},
	})
	if len(st.Status.PendingRemoveDeviceIds) != 0 || st.Status.PendingRemoveNotice != nil {
		t.Fatalf("expected PendingRemoveDeviceIds and notice cleared after successful removal, got %+v", st.Status)
	}
}
