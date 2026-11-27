// Package testrelay provides a shared in-memory cryptographic relay fixture for
// Go tests in clientcore/sync and clientcore/app. All wire envelopes produced
// by this package are genuinely AES-256-GCM encrypted and Ed25519 signed using
// devicesec, with zero unsigned bypasses in production code.
package testrelay

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/sortutil"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/security"
	stdv1 "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"google.golang.org/protobuf/proto"
)

const ProjectPrefsObjectID = "project-prefs"

// Actor holds a deterministic Ed25519 + X25519 identity for a test client or machine.
type Actor struct {
	ID    string
	Ed    ed25519.PrivateKey
	EdPub ed25519.PublicKey
	X     *ecdh.PrivateKey
	XPub  [32]byte
}

func newActor(t testing.TB, tag byte) Actor {
	t.Helper()
	edPriv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{tag}, ed25519.SeedSize))
	edPub := edPriv.Public().(ed25519.PublicKey)
	xPriv, err := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{tag + 0x50}, 32))
	if err != nil {
		t.Fatal(err)
	}
	var xp [32]byte
	copy(xp[:], xPriv.PublicKey().Bytes())
	return Actor{
		ID:    devicesec.ID(edPub),
		Ed:    edPriv,
		EdPub: edPub,
		X:     xPriv,
		XPub:  xp,
	}
}

func (a Actor) member(k devicesec.Kind) devicesec.Member {
	return devicesec.Member{ID: a.ID, EdPublic: [32]byte(a.EdPub), XPublic: a.XPub, Kind: k}
}

type testHPKE struct{}

func (testHPKE) Seal(recipient, info, aad, plaintext []byte) ([]byte, []byte, error) {
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

// Fixture holds a complete real cryptographic security chain (1 client + N machines)
// and helpers for sealing wire relay responses and pumping Core security effects.
type Fixture struct {
	t testing.TB

	Account        string
	AccountPub     []byte
	Domain         devicesec.Hash
	Genesis  devicesec.Hash
	State          devicesec.State
	S0Raw          []byte
	S1Raw          []byte
	ExtraStates    [][]byte
	RecoveryProofs [][]byte
	LeaseRaw       []byte
	Secret   []byte

	Client   Actor
	Machines []Actor
	M1       string
	M2       string
	M3       string

	actorsByID   map[string]Actor
	sessionSeeds map[string][]byte
	sessionMach  map[string]string
}

// New constructs a Fixture with 1 authority client and 4 trusted machines (M1..M4).
func New(t testing.TB, nowMs int64) *Fixture {
	t.Helper()
	if nowMs <= 0 {
		nowMs = 1_791_078_000_000
	}
	client := newActor(t, 1)
	rec := newActor(t, 2)
	machines := []Actor{
		newActor(t, 11),
		newActor(t, 12),
		newActor(t, 13),
		newActor(t, 14),
	}
	dom := devicesec.Digest("testrelay-domain", []byte{1})
	curSec := bytes.Repeat([]byte{0x60}, 32)
	u0, err := devicesec.NewGenesis(client.ID, dom, client.member(devicesec.Client), [32]byte(rec.EdPub), curSec, uint64(nowMs), testHPKE{})
	if err != nil {
		t.Fatalf("NewGenesis: %v", err)
	}
	sb0, _ := u0.SigningBytes()
	u0.Signature = ed25519.Sign(client.Ed, sb0)
	s0Raw, _ := u0.MarshalBinary()
	curState, err := devicesec.ParseState(s0Raw)
	if err != nil {
		t.Fatalf("ParseState(s0): %v", err)
	}
	gen, _ := curState.Hash()

	statesRaw := [][]byte{s0Raw}
	members := []devicesec.Member{client.member(devicesec.Client)}
	for i, m := range machines {
		members = append(members, m.member(devicesec.Machine))
		sortutil.Sort(len(members), func(i, j int) bool { return members[i].ID < members[j].ID }, func(i, j int) { members[i], members[j] = members[j], members[i] })
		nextSec := bytes.Repeat([]byte{byte(0x61 + i)}, 32)
		u, err := devicesec.NewMembershipState(curState, members, nil, curSec, nextSec, uint64(nowMs+int64(i+1)), testHPKE{}, rand.Reader)
		if err != nil {
			t.Fatalf("NewMembershipState(%d): %v", i, err)
		}
		sb, _ := u.SigningBytes()
		u.Signature = ed25519.Sign(client.Ed, sb)
		raw, _ := u.MarshalBinary()
		st, err := devicesec.ParseState(raw)
		if err != nil {
			t.Fatalf("ParseState(%d): %v", i, err)
		}
		statesRaw = append(statesRaw, raw)
		curState = st
		curSec = nextSec
	}
	lease, err := devicesec.NewLease(curState, uint64(nowMs+10))
	if err != nil {
		t.Fatalf("NewLease: %v", err)
	}
	lb, _ := lease.SigningBytes()
	lease.Signature = ed25519.Sign(client.Ed, lb)
	leaseRaw, _ := lease.MarshalBinary()

	byID := map[string]Actor{client.ID: client}
	for _, m := range machines {
		byID[m.ID] = m
	}
	return &Fixture{
		t:            t,
		Account:      client.ID,
		Domain:       dom,
		Genesis:      gen,
		State:        curState,
		S0Raw:        statesRaw[0],
		S1Raw:        statesRaw[1],
		ExtraStates:  statesRaw[2:],
		LeaseRaw:     leaseRaw,
		Secret:       curSec,
		Client:       client,
		Machines:     machines,
		M1:           machines[0].ID,
		M2:           machines[1].ID,
		M3:           machines[2].ID,
		actorsByID:   byID,
		sessionSeeds: make(map[string][]byte),
		sessionMach:  make(map[string]string),
	}
}

// DetEntropy returns deterministic random bytes for Core entropy seeding.
func DetEntropy(n int) []byte {
	out := make([]byte, n)
	for off := 0; off < len(out); off += sha256.Size {
		var ctr [8]byte
		binary.LittleEndian.PutUint64(ctr[:], uint64(off/sha256.Size+1))
		sum := sha256.Sum256(ctr[:])
		copy(out[off:], sum[:])
	}
	return out
}

// PersistedRecord builds a valid security.PersistedRecord for the fixture's client.
func (f *Fixture) PersistedRecord() *security.PersistedRecord {
	var sec [32]byte
	copy(sec[:], f.Secret)
	states := [][]byte{f.S0Raw, f.S1Raw}
	states = append(states, f.ExtraStates...)
	acctPub := f.AccountPub
	if len(acctPub) == 0 {
		acctPub = f.Client.EdPub
	}
	return &security.PersistedRecord{
		Server: "https://relay.example.com",
		Pin: devicesec.Pin{
			Account: f.Account,
			Domain:  f.Domain,
			Genesis: f.Genesis,
		},
		AccountPublicKey: append([]byte(nil), acctPub...),
		MemberID:         f.Client.ID,
		Member:           true,
		States:           states,
		RecoveryProofs:   f.RecoveryProofs,
		Lease:            f.LeaseRaw,
		ContentKeys: []security.ContentKeyEntry{{
			Epoch:  f.State.Core.ContentEpoch,
			Secret: sec,
		}},
	}
}

// Bootstrap configures and bootstraps any sync.Engine or app.Core via dispatch,
// leaving it authenticated and with the real devicesec chain loaded.
func (f *Fixture) Bootstrap(dispatch func(*corev1.Event) []*corev1.Effect, nowMs int64) {
	f.t.Helper()
	raw, err := f.PersistedRecord().Encode()
	if err != nil {
		f.t.Fatal(err)
	}
	ids := make([]string, 32)
	floats := make([]float64, 32)
	for i := range ids {
		ids[i] = fmt.Sprintf("id-%d", i+1)
		floats[i] = 0.5
	}
	dispatch(&corev1.Event{
		NowMs: nowMs,
		Event: &corev1.Event_EntropyProvided{
			EntropyProvided: &corev1.EntropyProvided{
				RandomIds:   ids,
				UnitFloats:  floats,
				RandomBytes: DetEntropy(8192),
			},
		},
	})
	dispatch(&corev1.Event{
		NowMs: nowMs,
		Event: &corev1.Event_SecurityConfig{
			SecurityConfig: &corev1.SecurityConfigEvent{
				Account:      f.Account,
				DeviceId:     f.Client.ID,
				Server:       "https://relay.example.com",
				InlineCrypto: true,
				Foreground:   true,
			},
		},
	})
	bootEffs := dispatch(&corev1.Event{
		NowMs: nowMs,
		Event: &corev1.Event_SecurityAction{
			SecurityAction: &corev1.SecurityWorkflowActionEvent{
				ActionId: 1,
				Action:   &corev1.SecurityWorkflowActionEvent_Bootstrap{Bootstrap: &corev1.SecurityActionEmpty{}},
			},
		},
	})
	var storOp uint64
	for _, eff := range bootEffs {
		if st := eff.GetSecurityStorage(); st != nil {
			storOp = st.OpId
		}
	}
	if storOp == 0 {
		f.t.Fatal("testrelay.Bootstrap: expected SecurityStorageEffect")
	}
	storEffs := dispatch(&corev1.Event{
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
	f.Pump(dispatch, nowMs, storEffs)
}

// Pump answers SecurityKeySign, SecurityKeyAgree, SecurityCryptoBatch, and auth
// RelayRpcEffect (GET_CHALLENGE / LOGIN / GET_SECURITY_STATES) emitted by Core/Engine.
func (f *Fixture) Pump(dispatch func(*corev1.Event) []*corev1.Effect, nowMs int64, initEffs []*corev1.Effect) []*corev1.Effect {
	f.t.Helper()
	all := append([]*corev1.Effect(nil), initEffs...)
	for i := 0; i < len(all); i++ {
		eff := all[i]
		switch k := eff.Effect.(type) {
		case *corev1.Effect_SecurityKeySign:
			sig := ed25519.Sign(f.Client.Ed, k.SecurityKeySign.Message)
			all = append(all, dispatch(&corev1.Event{
				NowMs: nowMs,
				Event: &corev1.Event_SecurityKeySigned{
					SecurityKeySigned: &corev1.SecurityKeySignedEvent{OpId: k.SecurityKeySign.OpId, Signature: sig},
				},
			})...)
		case *corev1.Effect_SecurityKeyAgree:
			peer, err := ecdh.X25519().NewPublicKey(k.SecurityKeyAgree.PeerPublic)
			if err != nil {
				f.t.Fatal(err)
			}
			shared, err := f.Client.X.ECDH(peer)
			if err != nil {
				f.t.Fatal(err)
			}
			all = append(all, dispatch(&corev1.Event{
				NowMs: nowMs,
				Event: &corev1.Event_SecurityKeyAgreed{
					SecurityKeyAgreed: &corev1.SecurityKeyAgreedEvent{OpId: k.SecurityKeyAgree.OpId, Shared: shared},
				},
			})...)
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
			all = append(all, dispatch(&corev1.Event{
				NowMs: nowMs,
				Event: &corev1.Event_SecurityCryptoBatchDone{
					SecurityCryptoBatchDone: &corev1.SecurityCryptoBatchDoneEvent{OpId: k.SecurityCryptoBatch.OpId, Results: results},
				},
			})...)
		case *corev1.Effect_RelayRpc:
			switch k.RelayRpc.Method {
			case corev1.RelayRpcMethod_RELAY_RPC_METHOD_HEALTHZ:
				all = append(all, dispatch(&corev1.Event{
					NowMs: nowMs,
					Event: &corev1.Event_RelayRpcResult{
						RelayRpcResult: &corev1.RelayRpcResultEvent{OpId: k.RelayRpc.OpId, RelayEpoch: "test-epoch"},
					},
				})...)
			case corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_CHALLENGE:
				resp := MarshalStd(&stdv1.GetChallengeResponse{
					Challenge: bytes.Repeat([]byte{0x42}, 32),
				})
				all = append(all, dispatch(&corev1.Event{
					NowMs: nowMs,
					Event: &corev1.Event_RelayRpcResult{
						RelayRpcResult: &corev1.RelayRpcResultEvent{OpId: k.RelayRpc.OpId, ResponseBytes: resp},
					},
				})...)
			case corev1.RelayRpcMethod_RELAY_RPC_METHOD_LOGIN:
				resp := MarshalStd(&stdv1.LoginResponse{
					Token:     "test-token",
					ExpiresMs: nowMs + 86_400_000,
				})
				all = append(all, dispatch(&corev1.Event{
					NowMs: nowMs,
					Event: &corev1.Event_RelayRpcResult{
						RelayRpcResult: &corev1.RelayRpcResultEvent{OpId: k.RelayRpc.OpId, ResponseBytes: resp},
					},
				})...)
			case corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_SECURITY_STATES:
				respStates := []*stdv1.SecurityState{
					{State: f.S0Raw},
					{State: f.S1Raw},
				}
				for _, extra := range f.ExtraStates {
					respStates = append(respStates, &stdv1.SecurityState{State: extra})
				}
				resp := MarshalStd(&stdv1.GetSecurityStatesResponse{
					States:    respStates,
					HeadEpoch: f.State.Core.Epoch,
					Lease:     f.LeaseRaw,
				})
				all = append(all, dispatch(&corev1.Event{
					NowMs: nowMs,
					Event: &corev1.Event_RelayRpcResult{
						RelayRpcResult: &corev1.RelayRpcResultEvent{OpId: k.RelayRpc.OpId, ResponseBytes: resp},
					},
				})...)
			}
		}
	}
	return all
}

func (f *Fixture) actorFor(id string) Actor {
	if a, ok := f.actorsByID[id]; ok {
		return a
	}
	f.t.Fatalf("testrelay: unknown actor ID %q (use fx.Client.ID, fx.M1, fx.M2, fx.M3, ...)", id)
	return Actor{}
}

func (f *Fixture) sessionSeed(sid string) []byte {
	if s, ok := f.sessionSeeds[sid]; ok {
		return s
	}
	sum := sha256.Sum256([]byte("session-seed:" + sid))
	s := append([]byte(nil), sum[:]...)
	f.sessionSeeds[sid] = s
	return s
}

// OpenEnvelope decrypts a wire envelope sealed with f.Secret (e.g. ProjectPrefs, MachineMeta).
func (f *Fixture) OpenEnvelope(env *stdv1.Envelope) []byte {
	f.t.Helper()
	if env == nil {
		f.t.Fatal("testrelay.OpenEnvelope: nil envelope")
	}
	hdr, err := devicesec.ParseHeader(env.SecurityHeader)
	if err != nil {
		f.t.Fatalf("testrelay.OpenEnvelope: parse header: %v", err)
	}
	key, err := devicesec.DeriveKey(f.Secret, nil, hdr)
	if err != nil {
		f.t.Fatalf("testrelay.OpenEnvelope: content key: %v", err)
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		f.t.Fatal(err)
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		f.t.Fatal(err)
	}
	pt, err := aead.Open(nil, env.Nonce, env.Ciphertext, env.SecurityHeader)
	if err != nil {
		f.t.Fatalf("testrelay.OpenEnvelope: aead open: %v", err)
	}
	return pt
}

// NewWithRecovery builds a Fixture whose head state (S2) is an account recovery
// from S1, retaining only S2's content key in PersistedRecord so events sealed
// under S1 produce security.ErrRecoveryHistoryUnavailable (UnavailableEventWire).
func NewWithRecovery(t testing.TB, nowMs int64) (*Fixture, func(sid, mid string, seq uint64) *v1.StoredEvent) {
	t.Helper()
	if nowMs <= 0 {
		nowMs = 1_791_078_000_000
	}
	oldClient := newActor(t, 3)
	client := newActor(t, 1)
	rec1 := newActor(t, 2)
	rec2 := newActor(t, 4)
	machines := []Actor{
		newActor(t, 11),
		newActor(t, 12),
		newActor(t, 13),
		newActor(t, 14),
	}
	dom := devicesec.Digest("testrelay-recovery-domain", []byte{1})
	curSec := bytes.Repeat([]byte{0x4a}, 32)
	u0, err := devicesec.NewGenesis(oldClient.ID, dom, oldClient.member(devicesec.Client), [32]byte(rec1.EdPub), curSec, uint64(nowMs), testHPKE{})
	if err != nil {
		t.Fatalf("NewGenesis: %v", err)
	}
	sb0, _ := u0.SigningBytes()
	u0.Signature = ed25519.Sign(oldClient.Ed, sb0)
	s0Raw, _ := u0.MarshalBinary()
	curState, err := devicesec.ParseState(s0Raw)
	if err != nil {
		t.Fatalf("ParseState(s0): %v", err)
	}
	gen, _ := curState.Hash()

	statesRaw := [][]byte{s0Raw}
	mems1 := []devicesec.Member{oldClient.member(devicesec.Client)}
	for i, m := range machines {
		mems1 = append(mems1, m.member(devicesec.Machine))
		sortutil.Sort(len(mems1), func(i, j int) bool { return mems1[i].ID < mems1[j].ID }, func(i, j int) { mems1[i], mems1[j] = mems1[j], mems1[i] })
		nextSec := bytes.Repeat([]byte{byte(0x50 + i)}, 32)
		u, err := devicesec.NewMembershipState(curState, mems1, nil, curSec, nextSec, uint64(nowMs+int64(i+1)), testHPKE{}, rand.Reader)
		if err != nil {
			t.Fatalf("NewMembershipState(%d): %v", i, err)
		}
		sb, _ := u.SigningBytes()
		u.Signature = ed25519.Sign(oldClient.Ed, sb)
		raw, _ := u.MarshalBinary()
		st, err := devicesec.ParseState(raw)
		if err != nil {
			t.Fatalf("ParseState(%d): %v", i, err)
		}
		statesRaw = append(statesRaw, raw)
		curState = st
		curSec = nextSec
	}
	s1 := curState
	sec1 := curSec

	sec2 := bytes.Repeat([]byte{0x7a}, 32)
	mems2 := []devicesec.Member{client.member(devicesec.Client)}
	for _, m := range machines {
		mems2 = append(mems2, m.member(devicesec.Machine))
	}
	sortutil.Sort(len(mems2), func(i, j int) bool { return mems2[i].ID < mems2[j].ID }, func(i, j int) { mems2[i], mems2[j] = mems2[j], mems2[i] })
	u2, err := devicesec.NewRecovery(s1, client.member(devicesec.Client), mems2, sec2, [32]byte(rec2.EdPub), uint64(nowMs+10), testHPKE{})
	if err != nil {
		t.Fatalf("NewRecovery: %v", err)
	}
	sb2, _ := u2.SigningBytes()
	u2.Signature = ed25519.Sign(client.Ed, sb2)
	s2Raw, _ := u2.MarshalBinary()
	s2, _ := devicesec.ParseState(s2Raw)
	stmtU, err := devicesec.NewRecoveryStatement(s1, s2, rand.Reader)
	if err != nil {
		t.Fatalf("NewRecoveryStatement: %v", err)
	}
	stmtSB, _ := stmtU.SigningBytes()
	stmtU.Signature = ed25519.Sign(rec1.Ed, stmtSB)
	stmtRaw, _ := stmtU.MarshalBinary()
	statesRaw = append(statesRaw, s2Raw)
	proofs := make([][]byte, len(statesRaw))
	proofs[len(proofs)-1] = stmtRaw

	lease, err := devicesec.NewLease(s2, uint64(nowMs+10))
	if err != nil {
		t.Fatal(err)
	}
	lb, _ := lease.SigningBytes()
	lease.Signature = ed25519.Sign(client.Ed, lb)
	leaseRaw, _ := lease.MarshalBinary()

	byID := map[string]Actor{client.ID: client}
	for _, m := range machines {
		byID[m.ID] = m
	}
	fx := &Fixture{
		t:              t,
		Account:        oldClient.ID,
		AccountPub:     append([]byte(nil), oldClient.EdPub...),
		Domain:         dom,
		Genesis:        gen,
		State:          s2,
		S0Raw:          statesRaw[0],
		S1Raw:          statesRaw[1],
		ExtraStates:    statesRaw[2:],
		RecoveryProofs: proofs,
		LeaseRaw:       leaseRaw,
		Secret:         sec2,
		Client:       client,
		Machines:     machines,
		M1:           machines[0].ID,
		M2:           machines[1].ID,
		M3:           machines[2].ID,
		actorsByID:   byID,
		sessionSeeds: make(map[string][]byte),
		sessionMach:  make(map[string]string),
	}
	preRec := func(sid, mid string, seq uint64) *v1.StoredEvent {
		t.Helper()
		sh1, _ := s1.Hash()
		evRaw := (&v1.Event{Seq: seq, SessionId: sid, Kind: &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "old"}}}).AppendMarshal(nil)
		hdr := devicesec.Header{
			Account:      s1.Core.Account,
			Domain:       dom,
			Epoch:        s1.Core.Epoch,
			State:        sh1,
			ContentEpoch: s1.Core.ContentEpoch,
			Sender:       fx.actorFor(mid).ID,
			Kind:         devicesec.Event.Kind(),
			Purpose:      devicesec.Event,
			Machine:      mid,
			Session:      sid,
			Seq:          seq,
		}
		hb, key, nonce, err := devicesec.PrepareCiphertext(sec1, fx.sessionSeed(sid), hdr, len(evRaw), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		blk, _ := aes.NewCipher(key)
		aead, _ := cipher.NewGCM(blk)
		ct, om, err := devicesec.AssembleCiphertext(hb, nonce, aead.Seal(nil, nonce, evRaw, hb))
		if err != nil {
			t.Fatal(err)
		}
		ct.Origin = ed25519.Sign(fx.actorFor(mid).Ed, om)
		env, err := security.ToProtoEnvelope(ct)
		if err != nil {
			t.Fatal(err)
		}
		return &v1.StoredEvent{Seq: seq, Envelope: env}
	}
	return fx, preRec
}

// Seal seals plaintext with real AES-256-GCM + Ed25519 under the fixture's active state.
func (f *Fixture) Seal(signer Actor, seed, plain []byte, purpose devicesec.Purpose, machineID, sessionID, reqID string, seq uint64) *v1.Envelope {
	f.t.Helper()
	sh, _ := f.State.Hash()
	hdr := devicesec.Header{
		Account:      f.State.Core.Account,
		Domain:       f.Domain,
		Epoch:        f.State.Core.Epoch,
		State:        sh,
		ContentEpoch: f.State.Core.ContentEpoch,
		Sender:       signer.ID,
		Kind:         purpose.Kind(),
		Purpose:      purpose,
		Machine:      machineID,
		Session:      sessionID,
		Request:      reqID,
		Seq:          seq,
	}
	hb, key, nonce, err := devicesec.PrepareCiphertext(f.Secret, seed, hdr, len(plain), rand.Reader)
	if err != nil {
		f.t.Fatal(err)
	}
	blk, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(blk)
	ct, om, err := devicesec.AssembleCiphertext(hb, nonce, aead.Seal(nil, nonce, plain, hb))
	if err != nil {
		f.t.Fatal(err)
	}
	ct.Origin = ed25519.Sign(signer.Ed, om)
	env, err := security.ToProtoEnvelope(ct)
	if err != nil {
		f.t.Fatal(err)
	}
	return env
}

func (f *Fixture) MachineWire(id string, online bool, lastSeenMs int64, meta *v1.MachineMeta) *v1.Machine {
	m := &v1.Machine{Id: id, Online: online, LastSeenMs: lastSeenMs}
	if meta != nil {
		m.Meta = f.Seal(f.actorFor(id), nil, meta.AppendMarshal(nil), devicesec.MachineMeta, id, "", "", 1)
	}
	return m
}

func (f *Fixture) SessionWire(sid, mid string, lastSeq uint64, updatedMs, endedMs int64, meta *v1.Session, cp *v1.LiveSnapshot) *v1.RemoteSession {
	f.sessionMach[sid] = mid
	seed := f.sessionSeed(sid)
	signer := f.actorFor(mid)
	rs := &v1.RemoteSession{
		SessionId: sid,
		MachineId: mid,
		LastSeq:   lastSeq,
		UpdatedMs: updatedMs,
		EndedMs:   endedMs,
		Key:       f.Seal(signer, nil, seed, devicesec.SessionKey, mid, sid, "", 1),
	}
	var m *v1.Session
	if meta != nil {
		mCopy := *meta
		m = &mCopy
	} else if cp != nil {
		m = &v1.Session{Id: sid}
	}
	if m != nil {
		if m.Id == "" {
			m.Id = sid
		}
		if cp != nil {
			cpCopy := *cp
			if cpCopy.Session == nil {
				cpCopy.Session = &v1.Session{Id: sid, LastSeq: lastSeq}
			} else if cpCopy.Session.Id == "" {
				sCopy := *cpCopy.Session
				sCopy.Id = sid
				cpCopy.Session = &sCopy
			}
			maxSeq := max(lastSeq, cpCopy.Session.LastSeq)
			for _, a := range cpCopy.Actions {
				if a != nil {
					if a.SessionId == "" {
						a.SessionId = sid
					}
					if a.Seq > maxSeq {
						maxSeq = a.Seq
					}
				}
			}
			if m.LastSeq < maxSeq {
				m.LastSeq = maxSeq
			}
			m.CurrentCheckpoint = cpCopy.AppendMarshal(nil)
		}
		rs.Meta = f.Seal(signer, nil, m.AppendMarshal(nil), devicesec.SessionMeta, mid, sid, "", 1)
	}
	return rs
}

func (f *Fixture) DeviceWire(id string, kind v1.DeviceKind, createdMs, lastSeenMs int64, metaRev uint64, meta *v1.DeviceMeta) *v1.Device {
	d := &v1.Device{
		Id:         id,
		Kind:       kind,
		CreatedMs:  createdMs,
		LastSeenMs: lastSeenMs,
	}
	if meta != nil {
		if metaRev == 0 {
			metaRev = 1
		}
		signer, ok := f.actorsByID[id]
		if !ok {
			signer = f.Client
		}
		d.Meta = f.Seal(signer, nil, meta.AppendMarshal(nil), devicesec.DeviceMeta, "", "", id, metaRev)
	}
	return d
}

func (f *Fixture) PrefsWire(rev uint64, prefs *v1.ProjectPrefs) *v1.AccountPrefsObject {
	obj := &v1.AccountPrefsObject{
		ObjectId: ProjectPrefsObjectID,
		Revision: rev,
	}
	if prefs != nil {
		obj.Envelope = f.Seal(f.Client, nil, prefs.AppendMarshal(nil), devicesec.Prefs, "", "", ProjectPrefsObjectID, rev)
	}
	return obj
}

func (f *Fixture) StoredEventWire(sid string, seq uint64, ev *v1.Event) *v1.StoredEvent {
	mid := f.sessionMach[sid]
	if mid == "" {
		mid = f.M1
	}
	return f.StoredEventWireForMachine(sid, mid, seq, ev)
}

func (f *Fixture) StoredEventWireForMachine(sid, mid string, seq uint64, ev *v1.Event) *v1.StoredEvent {
	if ev == nil {
		ev = &v1.Event{
			Seq:       seq,
			SessionId: sid,
			Kind:      &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "ev"}},
		}
	} else {
		evCopy := *ev
		if evCopy.Seq == 0 {
			evCopy.Seq = seq
		}
		if evCopy.SessionId == "" {
			evCopy.SessionId = sid
		}
		if evCopy.Kind == nil {
			evCopy.Kind = &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "ev"}}
		}
		ev = &evCopy
	}
	env := f.Seal(f.actorFor(mid), f.sessionSeed(sid), ev.AppendMarshal(nil), devicesec.Event, mid, sid, "", seq)
	return &v1.StoredEvent{Seq: seq, Envelope: env}
}

// ---- Standard wire marshaling helpers (shared by sync and app tests) ----

func MarshalStd(m proto.Message) []byte {
	b, err := proto.Marshal(m)
	if err != nil {
		panic(err)
	}
	return b
}

func MustRaw[T any, PT interface {
	*T
	Marshal() ([]byte, error)
}](m PT) []byte {
	if m == nil {
		return nil
	}
	b, err := m.Marshal()
	if err != nil {
		panic(err)
	}
	return b
}

func MustRawSlice[T any, PT interface {
	*T
	Marshal() ([]byte, error)
}](ms []PT) [][]byte {
	if len(ms) == 0 {
		return nil
	}
	out := make([][]byte, 0, len(ms))
	for _, m := range ms {
		if m != nil {
			out = append(out, MustRaw(m))
		}
	}
	return out
}

func ToStdEnv(e *v1.Envelope) *stdv1.Envelope {
	if e == nil {
		return nil
	}
	return &stdv1.Envelope{
		Header:         e.Header,
		Ciphertext:     e.Ciphertext,
		SecurityHeader: e.SecurityHeader,
		Nonce:          e.Nonce,
		Signature:      e.Signature,
	}
}

func FromStdEnv(e *stdv1.Envelope) *v1.Envelope {
	if e == nil {
		return nil
	}
	return &v1.Envelope{
		Header:         e.Header,
		Ciphertext:     e.Ciphertext,
		SecurityHeader: e.SecurityHeader,
		Nonce:          e.Nonce,
		Signature:      e.Signature,
	}
}

func ToStdMachine(m *v1.Machine) *stdv1.Machine {
	if m == nil {
		return nil
	}
	return &stdv1.Machine{Id: m.Id, Online: m.Online, LastSeenMs: m.LastSeenMs, Meta: ToStdEnv(m.Meta)}
}

func ToStdSession(s *v1.RemoteSession) *stdv1.RemoteSession {
	if s == nil {
		return nil
	}
	return &stdv1.RemoteSession{
		SessionId: s.SessionId, MachineId: s.MachineId, LastSeq: s.LastSeq,
		UpdatedMs: s.UpdatedMs, EndedMs: s.EndedMs, Key: ToStdEnv(s.Key), Meta: ToStdEnv(s.Meta),
	}
}

func ToStdDevice(d *v1.Device) *stdv1.Device {
	if d == nil {
		return nil
	}
	return &stdv1.Device{
		Id: d.Id, Kind: stdv1.DeviceKind(d.Kind), CreatedMs: d.CreatedMs, LastSeenMs: d.LastSeenMs, Meta: ToStdEnv(d.Meta),
	}
}

func ToStdStoredEvent(ev *v1.StoredEvent) *stdv1.StoredEvent {
	if ev == nil {
		return nil
	}
	return &stdv1.StoredEvent{Seq: ev.Seq, Envelope: ToStdEnv(ev.Envelope)}
}

func ToStdPrefsObj(p *v1.AccountPrefsObject) *stdv1.AccountPrefsObject {
	if p == nil {
		return nil
	}
	return &stdv1.AccountPrefsObject{ObjectId: p.ObjectId, Revision: p.Revision, Envelope: ToStdEnv(p.Envelope)}
}

func ToStdCursor(c *v1.SessionCursor) *stdv1.SessionCursor {
	if c == nil {
		return nil
	}
	return &stdv1.SessionCursor{EndedMs: c.EndedMs, SessionId: c.SessionId}
}

func MarshalListMachinesResp(ms []*v1.Machine) []byte {
	out := make([]*stdv1.Machine, len(ms))
	for i, m := range ms {
		out[i] = ToStdMachine(m)
	}
	return MarshalStd(&stdv1.ListMachinesResponse{Machines: out})
}

func MarshalListSessionsResp(ss []*v1.RemoteSession, next *v1.SessionCursor) []byte {
	out := make([]*stdv1.RemoteSession, len(ss))
	for i, s := range ss {
		out[i] = ToStdSession(s)
	}
	return MarshalStd(&stdv1.ListRemoteSessionsResponse{Sessions: out, Next: ToStdCursor(next)})
}

func MarshalListDevicesResp(ds []*v1.Device) []byte {
	out := make([]*stdv1.Device, len(ds))
	for i, d := range ds {
		out[i] = ToStdDevice(d)
	}
	return MarshalStd(&stdv1.ListDevicesResponse{Devices: out})
}

func MarshalGetEventsResp(evs []*v1.StoredEvent, more bool) []byte {
	out := make([]*stdv1.StoredEvent, len(evs))
	for i, ev := range evs {
		out[i] = ToStdStoredEvent(ev)
	}
	return MarshalStd(&stdv1.GetEventsResponse{Events: out, More: more})
}

func MarshalGetPrefsResp(obj *v1.AccountPrefsObject) []byte {
	return MarshalStd(&stdv1.GetAccountPrefsResponse{Object: ToStdPrefsObj(obj)})
}

func MarshalPutPrefsResp(applied bool, obj *v1.AccountPrefsObject) []byte {
	return MarshalStd(&stdv1.PutAccountPrefsResponse{Applied: applied, Object: ToStdPrefsObj(obj)})
}

func MarshalForwardResp(attemptID, out, reason string, res *v1.Envelope) []byte {
	return MarshalStd(&stdv1.ForwardResponse{
		AttemptId: attemptID, Outcome: out, Reason: reason, Result: ToStdEnv(res),
	})
}

func MarshalServerFrame(sf *v1.ServerFrame) []byte {
	if sf == nil {
		return nil
	}
	var out stdv1.ServerFrame
	switch k := sf.Kind.(type) {
	case *v1.ServerFrame_Ready:
		var st int64
		if k.Ready != nil {
			st = k.Ready.ServerTimeMs
		}
		out.Kind = &stdv1.ServerFrame_Ready{Ready: &stdv1.Ready{ServerTimeMs: st}}
	case *v1.ServerFrame_Heartbeat:
		out.Kind = &stdv1.ServerFrame_Heartbeat{Heartbeat: &stdv1.Heartbeat{}}
	case *v1.ServerFrame_MachineUpdated:
		if k.MachineUpdated != nil {
			out.Kind = &stdv1.ServerFrame_MachineUpdated{MachineUpdated: &stdv1.MachineUpdated{Machine: ToStdMachine(k.MachineUpdated.Machine)}}
		}
	case *v1.ServerFrame_MachineRemoved:
		if k.MachineRemoved != nil {
			out.Kind = &stdv1.ServerFrame_MachineRemoved{MachineRemoved: &stdv1.MachineRemoved{MachineId: k.MachineRemoved.MachineId}}
		}
	case *v1.ServerFrame_SessionUpdated:
		if k.SessionUpdated != nil {
			out.Kind = &stdv1.ServerFrame_SessionUpdated{SessionUpdated: &stdv1.SessionUpdated{Session: ToStdSession(k.SessionUpdated.Session)}}
		}
	case *v1.ServerFrame_EventsAppended:
		if k.EventsAppended != nil {
			evs := make([]*stdv1.StoredEvent, len(k.EventsAppended.Events))
			for i, ev := range k.EventsAppended.Events {
				evs[i] = ToStdStoredEvent(ev)
			}
			out.Kind = &stdv1.ServerFrame_EventsAppended{EventsAppended: &stdv1.EventsAppended{SessionId: k.EventsAppended.SessionId, Events: evs}}
		}
	case *v1.ServerFrame_AccountPrefsUpdated:
		if k.AccountPrefsUpdated != nil {
			out.Kind = &stdv1.ServerFrame_AccountPrefsUpdated{AccountPrefsUpdated: &stdv1.AccountPrefsUpdated{Object: ToStdPrefsObj(k.AccountPrefsUpdated.Object)}}
		}
	case *v1.ServerFrame_DeviceUpdated:
		if k.DeviceUpdated != nil {
			out.Kind = &stdv1.ServerFrame_DeviceUpdated{DeviceUpdated: &stdv1.DeviceUpdated{Device: ToStdDevice(k.DeviceUpdated.Device)}}
		}
	case *v1.ServerFrame_DeviceRemoved:
		if k.DeviceRemoved != nil {
			out.Kind = &stdv1.ServerFrame_DeviceRemoved{DeviceRemoved: &stdv1.DeviceRemoved{DeviceId: k.DeviceRemoved.DeviceId}}
		}
	}
	return MarshalStd(&out)
}

func RawFrame(legID uint64, sf *v1.ServerFrame) *corev1.Event_StreamRawFrame {
	return &corev1.Event_StreamRawFrame{
		StreamRawFrame: &corev1.StreamRawFrameEvent{
			LegId:          legID,
			RawServerFrame: MarshalServerFrame(sf),
		},
	}
}

func RawStdFrame(legID uint64, sf *stdv1.ServerFrame) *corev1.Event_StreamRawFrame {
	return &corev1.Event_StreamRawFrame{
		StreamRawFrame: &corev1.StreamRawFrameEvent{
			LegId:          legID,
			RawServerFrame: MarshalStd(sf),
		},
	}
}

func MachineUpdatedFrame(legID uint64, m *v1.Machine) *corev1.Event_StreamRawFrame {
	return RawStdFrame(legID, &stdv1.ServerFrame{
		Kind: &stdv1.ServerFrame_MachineUpdated{MachineUpdated: &stdv1.MachineUpdated{Machine: ToStdMachine(m)}},
	})
}

func SessionUpdatedFrame(legID uint64, s *v1.RemoteSession) *corev1.Event_StreamRawFrame {
	return RawStdFrame(legID, &stdv1.ServerFrame{
		Kind: &stdv1.ServerFrame_SessionUpdated{SessionUpdated: &stdv1.SessionUpdated{Session: ToStdSession(s)}},
	})
}

func EventsAppendedFrame(legID uint64, sid string, evs ...*v1.StoredEvent) *corev1.Event_StreamRawFrame {
	out := make([]*stdv1.StoredEvent, len(evs))
	for i, ev := range evs {
		out[i] = ToStdStoredEvent(ev)
	}
	return RawStdFrame(legID, &stdv1.ServerFrame{
		Kind: &stdv1.ServerFrame_EventsAppended{EventsAppended: &stdv1.EventsAppended{SessionId: sid, Events: out}},
	})
}

// RefreshOps holds the 3+1 RelayRpc op IDs emitted when Core refreshes lists.
type RefreshOps struct {
	Machines uint64
	Sessions uint64
	Devices  uint64
	Prefs    uint64
}

func FindRefreshOps(effs []*corev1.Effect) RefreshOps {
	var ops RefreshOps
	for _, eff := range effs {
		if r := eff.GetRelayRpc(); r != nil {
			switch r.Method {
			case corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_MACHINES:
				ops.Machines = r.OpId
			case corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_REMOTE_SESSIONS:
				ops.Sessions = r.OpId
			case corev1.RelayRpcMethod_RELAY_RPC_METHOD_LIST_DEVICES:
				ops.Devices = r.OpId
			case corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_ACCOUNT_PREFS:
				ops.Prefs = r.OpId
			}
		}
	}
	return ops
}

func MustFindRefreshOps(t testing.TB, effs []*corev1.Effect) RefreshOps {
	t.Helper()
	ops := FindRefreshOps(effs)
	if ops.Machines == 0 || ops.Sessions == 0 || ops.Devices == 0 {
		t.Fatalf("expected all 3 refresh relay RPCs (machines=%d sessions=%d devices=%d)", ops.Machines, ops.Sessions, ops.Devices)
	}
	return ops
}

func FindRelayRpc(effs []*corev1.Effect, method corev1.RelayRpcMethod) *corev1.RelayRpcEffect {
	for _, eff := range effs {
		if r := eff.GetRelayRpc(); r != nil && r.Method == method {
			return r
		}
	}
	return nil
}

func FindGetEventsOp(effs []*corev1.Effect, sid string) (uint64, uint64) {
	for _, eff := range effs {
		if r := eff.GetRelayRpc(); r != nil && r.Method == corev1.RelayRpcMethod_RELAY_RPC_METHOD_GET_EVENTS {
			var req stdv1.GetEventsRequest
			if proto.Unmarshal(r.RequestBytes, &req) == nil && (sid == "" || req.SessionId == sid) {
				return r.OpId, req.AfterSeq
			}
		}
	}
	return 0, 0
}

func FindOpenStreamLeg(effs []*corev1.Effect) uint64 {
	for _, eff := range effs {
		if os := eff.GetOpenStream(); os != nil {
			return os.LegId
		}
	}
	return 0
}

// ---- Core-native SeedDemoWorld builders for screen/app UI tests ----

func DemoMachine(id string, online bool, lastSeenMs int64, meta *v1.MachineMeta) *corev1.DemoMachine {
	dm := &corev1.DemoMachine{MachineId: id, Online: online, LastSeenMs: lastSeenMs}
	if meta != nil {
		dm.RawMeta = meta.AppendMarshal(nil)
	}
	return dm
}

func DemoSession(sid, mid string, lastSeq uint64, updatedMs, endedMs int64, meta *v1.Session, cp *v1.LiveSnapshot) *corev1.DemoSession {
	ds := &corev1.DemoSession{
		SessionId:     sid,
		MachineId:     mid,
		LastSeq:       lastSeq,
		UpdatedMs:     updatedMs,
		ListedEndedMs: endedMs,
	}
	if meta != nil {
		mCopy := *meta
		if mCopy.Id == "" {
			mCopy.Id = sid
		}
		ds.RawMeta = mCopy.AppendMarshal(nil)
	}
	if cp != nil {
		cpCopy := *cp
		if cpCopy.Session == nil {
			cpCopy.Session = &v1.Session{Id: sid, LastSeq: lastSeq}
		} else if cpCopy.Session.Id == "" {
			sCopy := *cpCopy.Session
			sCopy.Id = sid
			cpCopy.Session = &sCopy
		}
		for _, a := range cpCopy.Actions {
			if a != nil && a.SessionId == "" {
				a.SessionId = sid
			}
		}
		ds.RawCheckpoint = cpCopy.AppendMarshal(nil)
	}
	return ds
}

func DemoSessionEvents(sid string, evs ...*v1.Event) *corev1.DemoSessionEvents {
	se := &corev1.DemoSessionEvents{SessionId: sid}
	for _, ev := range evs {
		if ev == nil {
			continue
		}
		cp := *ev
		if cp.SessionId == "" {
			cp.SessionId = sid
		}
		se.RawEvents = append(se.RawEvents, cp.AppendMarshal(nil))
	}
	return se
}

func DemoSessionEventsSeq(sid string, seq uint64, ev *v1.Event) *corev1.DemoSessionEvents {
	if ev == nil {
		ev = &v1.Event{Seq: seq, SessionId: sid}
	} else {
		cp := *ev
		if cp.Seq == 0 {
			cp.Seq = seq
		}
		if cp.SessionId == "" {
			cp.SessionId = sid
		}
		ev = &cp
	}
	return &corev1.DemoSessionEvents{SessionId: sid, RawEvents: [][]byte{ev.AppendMarshal(nil)}}
}

func DemoMachineEvent(ms ...*corev1.DemoMachine) *corev1.Event_SeedDemoWorld {
	return &corev1.Event_SeedDemoWorld{SeedDemoWorld: &corev1.SeedDemoWorldEvent{Machines: ms}}
}

func DemoSessionEvent(ss ...*corev1.DemoSession) *corev1.Event_SeedDemoWorld {
	return &corev1.Event_SeedDemoWorld{SeedDemoWorld: &corev1.SeedDemoWorldEvent{Sessions: ss}}
}

func DemoEventsEvent(sid string, evs ...*v1.Event) *corev1.Event_SeedDemoWorld {
	return &corev1.Event_SeedDemoWorld{SeedDemoWorld: &corev1.SeedDemoWorldEvent{
		SessionEvents: []*corev1.DemoSessionEvents{DemoSessionEvents(sid, evs...)},
	}}
}

func DemoPrefsEvent(rev uint64, prefs *v1.ProjectPrefs) *corev1.Event_SeedDemoWorld {
	var raw []byte
	if prefs != nil {
		raw = prefs.AppendMarshal(nil)
	}
	return &corev1.Event_SeedDemoWorld{SeedDemoWorld: &corev1.SeedDemoWorldEvent{
		RawPrefs:      raw,
		PrefsRevision: rev,
	}}
}
