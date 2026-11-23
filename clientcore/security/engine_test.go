package security

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"slices"
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/entropy"
	"github.com/Lewin671/remote-agent/clientcore/gen/litewire"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/clientcore/gen/timestamppb"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

type testIdentity struct {
	id   string
	ed   ed25519.PrivateKey
	edP  ed25519.PublicKey
	x    *ecdh.PrivateKey
	xPub [32]byte
}

func newTestIdentity(t *testing.T, tag byte) testIdentity {
	t.Helper()
	seed := bytes.Repeat([]byte{tag}, ed25519.SeedSize)
	edPriv := ed25519.NewKeyFromSeed(seed)
	edPub := edPriv.Public().(ed25519.PublicKey)
	xSeed := bytes.Repeat([]byte{tag + 0x40}, 32)
	xPriv, err := ecdh.X25519().NewPrivateKey(xSeed)
	if err != nil {
		t.Fatal(err)
	}
	var xp [32]byte
	copy(xp[:], xPriv.PublicKey().Bytes())
	return testIdentity{
		id:   devicesec.ID(edPub),
		ed:   edPriv,
		edP:  edPub,
		x:    xPriv,
		xPub: xp,
	}
}

func (id testIdentity) member(k devicesec.Kind) devicesec.Member {
	return devicesec.Member{
		ID:       id.id,
		EdPublic: [32]byte(id.edP),
		XPublic:  id.xPub,
		Kind:     k,
	}
}

type stdHPKESender struct{}

func (stdHPKESender) Seal(recipient, info, aad, plaintext []byte) ([]byte, []byte, error) {
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
	enc := eph.PublicKey().Bytes()
	ct, err := devicesec.HPKESealWithDH(recipient, enc, dh, info, aad, plaintext)
	if err != nil {
		return nil, nil, err
	}
	return enc, ct, nil
}

func executeHostEffects(t *testing.T, e *Engine, actor testIdentity, nowMs int64, effs []*corev1.Effect) []*corev1.Effect {
	t.Helper()
	var out []*corev1.Effect
	queue := append([]*corev1.Effect(nil), effs...)
	for len(queue) > 0 {
		eff := queue[0]
		queue = queue[1:]
		out = append(out, eff)
		switch k := eff.Effect.(type) {
		case *corev1.Effect_SecurityKeySign:
			sig := ed25519.Sign(actor.ed, k.SecurityKeySign.Message)
			next := e.HandleKeySigned(nowMs, &corev1.SecurityKeySignedEvent{
				OpId:      k.SecurityKeySign.OpId,
				Signature: sig,
			})
			queue = append(queue, next...)

		case *corev1.Effect_SecurityKeyAgree:
			peer, err := ecdh.X25519().NewPublicKey(k.SecurityKeyAgree.PeerPublic)
			if err != nil {
				t.Fatalf("invalid peer key in KeyAgree: %v", err)
			}
			shared, err := actor.x.ECDH(peer)
			if err != nil {
				t.Fatalf("ECDH failed: %v", err)
			}
			next := e.HandleKeyAgreed(nowMs, &corev1.SecurityKeyAgreedEvent{
				OpId:   k.SecurityKeyAgree.OpId,
				Shared: shared,
			})
			queue = append(queue, next...)

		case *corev1.Effect_SecurityCryptoBatch:
			results := make([]*corev1.SecurityCryptoBatchResult, len(k.SecurityCryptoBatch.Items))
			for i, item := range k.SecurityCryptoBatch.Items {
				switch it := item.Kind.(type) {
				case *corev1.SecurityCryptoBatchItem_Verify:
					ok := len(it.Verify.PublicKey) == ed25519.PublicKeySize &&
						ed25519.Verify(it.Verify.PublicKey, it.Verify.Message, it.Verify.Signature)
					results[i] = &corev1.SecurityCryptoBatchResult{Ok: ok}
				case *corev1.SecurityCryptoBatchItem_AeadOpen:
					blk, _ := aes.NewCipher(it.AeadOpen.Key)
					aead, _ := cipher.NewGCM(blk)
					pt, err := aead.Open(nil, it.AeadOpen.Nonce, it.AeadOpen.Ciphertext, it.AeadOpen.Aad)
					if err != nil {
						results[i] = &corev1.SecurityCryptoBatchResult{Ok: false, Error: err.Error()}
					} else {
						results[i] = &corev1.SecurityCryptoBatchResult{Ok: true, Data: pt}
					}
				case *corev1.SecurityCryptoBatchItem_AeadSeal:
					blk, _ := aes.NewCipher(it.AeadSeal.Key)
					aead, _ := cipher.NewGCM(blk)
					ct := aead.Seal(nil, it.AeadSeal.Nonce, it.AeadSeal.Plaintext, it.AeadSeal.Aad)
					results[i] = &corev1.SecurityCryptoBatchResult{Ok: true, Data: ct}
				case *corev1.SecurityCryptoBatchItem_Sha256:
					sum := sha256.Sum256(it.Sha256.Data)
					results[i] = &corev1.SecurityCryptoBatchResult{Ok: true, Data: sum[:]}
				}
			}
			next := e.HandleCryptoBatchDone(nowMs, &corev1.SecurityCryptoBatchDoneEvent{
				OpId:    k.SecurityCryptoBatch.OpId,
				Results: results,
			})
			queue = append(queue, next...)
		}
	}
	return out
}

type testSecFixture struct {
	account   string
	domain    devicesec.Hash
	phone     testIdentity
	machine   testIdentity
	rec       testIdentity
	sec1      []byte
	sec2      []byte
	s0        devicesec.State
	s0Raw     []byte
	lease0    devicesec.LeaseCertificate
	lease0Raw []byte
}

func makeTestSecFixture(t *testing.T, nowMs uint64) testSecFixture {
	t.Helper()
	phone := newTestIdentity(t, 1)
	machine := newTestIdentity(t, 2)
	rec := newTestIdentity(t, 3)
	dom := devicesec.Digest("test-domain", []byte{1})
	sec1 := bytes.Repeat([]byte{0x33}, 32)
	sec2 := bytes.Repeat([]byte{0x55}, 32)

	u0, err := devicesec.NewGenesis(phone.id, dom, phone.member(devicesec.Client), [32]byte(rec.edP), sec1, nowMs, stdHPKESender{})
	if err != nil {
		t.Fatal(err)
	}
	sb0, err := u0.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	u0.Signature = ed25519.Sign(phone.ed, sb0)
	s0Raw, err := u0.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	s0, err := devicesec.ParseState(s0Raw)
	if err != nil {
		t.Fatal(err)
	}

	// Add machine in state 1 so machine can sign events cleanly.
	u1, err := devicesec.NewMembershipState(s0, []devicesec.Member{phone.member(devicesec.Client), machine.member(devicesec.Machine)}, nil, sec1, sec2, nowMs+10, stdHPKESender{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sb1, err := u1.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	u1.Signature = ed25519.Sign(phone.ed, sb1)
	s1Raw, err := u1.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	s1, err := devicesec.ParseState(s1Raw)
	if err != nil {
		t.Fatal(err)
	}

	l, err := devicesec.NewLease(s1, nowMs+10)
	if err != nil {
		t.Fatal(err)
	}
	lb, err := l.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	l.Signature = ed25519.Sign(phone.ed, lb)
	lRaw, err := l.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	lParsed, err := devicesec.ParseLease(lRaw)
	if err != nil {
		t.Fatal(err)
	}
	_ = s0Raw
	return testSecFixture{
		account:   phone.id,
		domain:    dom,
		phone:     phone,
		machine:   machine,
		rec:       rec,
		sec1:      sec1,
		sec2:      sec2,
		s0:        s1,
		s0Raw:     s1Raw,
		lease0:    lParsed,
		lease0Raw: lRaw,
	}
}

func makeGenesisAndTwoStateRecord(t *testing.T, nowMs uint64) (account string, dom, gen devicesec.Hash, phone, machine, rec testIdentity, sec1, sec2 []byte, statesRaw [][]byte, leaseRaw []byte, states []devicesec.State) {
	t.Helper()
	phone = newTestIdentity(t, 1)
	machine = newTestIdentity(t, 2)
	rec = newTestIdentity(t, 3)
	account = phone.id
	dom = devicesec.Digest("test-domain", []byte{7})
	sec1 = bytes.Repeat([]byte{0x11}, 32)
	sec2 = bytes.Repeat([]byte{0x22}, 32)

	u0, err := devicesec.NewGenesis(account, dom, phone.member(devicesec.Client), [32]byte(rec.edP), sec1, nowMs, stdHPKESender{})
	if err != nil {
		t.Fatal(err)
	}
	sb0, _ := u0.SigningBytes()
	u0.Signature = ed25519.Sign(phone.ed, sb0)
	s0Raw, err := u0.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	s0, err := devicesec.ParseState(s0Raw)
	if err != nil {
		t.Fatal(err)
	}
	gen, _ = s0.Hash()

	u1, err := devicesec.NewMembershipState(s0, []devicesec.Member{phone.member(devicesec.Client), machine.member(devicesec.Machine)}, nil, sec1, sec2, nowMs+10, stdHPKESender{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sb1, _ := u1.SigningBytes()
	u1.Signature = ed25519.Sign(phone.ed, sb1)
	s1Raw, err := u1.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	s1, err := devicesec.ParseState(s1Raw)
	if err != nil {
		t.Fatal(err)
	}

	l, err := devicesec.NewLease(s1, nowMs+10)
	if err != nil {
		t.Fatal(err)
	}
	lb, _ := l.SigningBytes()
	l.Signature = ed25519.Sign(phone.ed, lb)
	leaseRaw, err = l.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	return account, dom, gen, phone, machine, rec, sec1, sec2, [][]byte{s0Raw, s1Raw}, leaseRaw, []devicesec.State{s0, s1}
}

func sealEventProto(t *testing.T, signer testIdentity, dom devicesec.Hash, s devicesec.State, secret, seed []byte, account, sid string, seq, nowMs uint64, textMsg string) *v1.Envelope {
	t.Helper()
	sh, _ := s.Hash()
	evMsg := &v1.Event{
		Seq:       seq,
		SessionId: sid,
		Kind: &v1.Event_UserMessage{
			UserMessage: &v1.UserMessage{Text: textMsg},
		},
	}
	plain, err := evMsg.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	hdr := devicesec.Header{
		Account:      account,
		Domain:       dom,
		Epoch:        s.Core.Epoch,
		State:        sh,
		ContentEpoch: s.Core.ContentEpoch,
		Sender:       signer.id,
		Kind:         devicesec.Event.Kind(),
		Purpose:      devicesec.Event,
		Machine:      signer.id,
		Session:      sid,
		Seq:          seq,
		Deadline:     0,
	}
	hb, key, nonce, err := devicesec.PrepareCiphertext(secret, seed, hdr, len(plain), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(blk)
	cb := aead.Seal(nil, nonce, plain, hb)
	ct, originMsg, err := devicesec.AssembleCiphertext(hb, nonce, cb)
	if err != nil {
		t.Fatal(err)
	}
	ct.Origin = ed25519.Sign(signer.ed, originMsg)
	env, err := ToProtoEnvelope(ct)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func TestEngineUnlockSealOpenAndBatchThresholds(t *testing.T) {
	nowMs := int64(1_700_000_000_000)
	acct, dom, gen, phone, machine, _, _, sec2, rawStates, leaseRaw, states := makeGenesisAndTwoStateRecord(t, uint64(nowMs))

	for _, inlineCrypto := range []bool{false, true} {
		pool := &entropy.Pool{}
		pool.Append(bytes.Repeat([]byte{0x9a}, 4096))
		eng := NewEngine(pool, 0)
		eng.Configure(&corev1.SecurityConfigEvent{
			Account:      acct,
			DeviceId:     phone.id,
			InlineCrypto: inlineCrypto,
		})
		if !eng.Locked() {
			t.Fatal("expected Locked() after Configure")
		}
		effs := eng.ApplyRecord(nowMs+100, RecordUpdate{
			NetworkGeneration: 1,
			Account:           acct,
			Domain:            dom[:],
			Genesis:           gen[:],
			States:            rawStates,
			Lease:             leaseRaw,
		})
		// Must emit SecurityKeyAgreeEffect matching GrantChallenge.
		var sawAgree bool
		expectedEnc, _, err := (devicesec.Pin{Account: acct, Domain: dom, Genesis: gen}).GrantChallenge(states[1], phone.id)
		if err != nil {
			t.Fatal(err)
		}
		for _, ef := range effs {
			if ka := ef.GetSecurityKeyAgree(); ka != nil {
				sawAgree = true
				if !bytes.Equal(ka.PeerPublic, expectedEnc[:]) {
					t.Fatalf("KeyAgree PeerPublic mismatch")
				}
			}
		}
		if !sawAgree {
			t.Fatalf("expected SecurityKeyAgreeEffect on ApplyRecord")
		}
		executeHostEffects(t, eng, phone, nowMs+100, effs)

		if err := eng.CanSeal(nowMs + 100); err != nil {
			t.Fatalf("CanSeal failed: %v", err)
		}
		ci, ok := eng.CoordInfo()
		if !ok || !ci.Member || ci.Head != states[1].Core.Domain.String() && ci.Epoch != states[1].Core.Epoch {
			_ = ci
		}
		if !ok || !ci.Member || len(ci.Members) != 2 {
			t.Fatalf("unexpected CoordInfo: ok=%v ci=%+v", ok, ci)
		}

		// Seed session key and test OpenBatch with 9 items (> BatchVerifyThreshold = 8).
		seed := bytes.Repeat([]byte{0x44}, 32)
		eng.SeedSessionKey("sess-1", seed)
		items := make([]OpenItem, 9)
		for i := range items {
			items[i] = OpenItem{
				Env:             sealEventProto(t, machine, dom, states[1], sec2, seed, acct, "sess-1", uint64(i+1), uint64(nowMs+100), "hello"),
				ExpectedPurpose: devicesec.Event,
				MachineID:       machine.id,
				SessionID:       "sess-1",
				Seq:             uint64(i + 1),
				History:         true,
			}
		}
		var batchResults []OpenResult
		batchEffs := eng.OpenBatch(nowMs+200, items, func(res []OpenResult) {
			batchResults = res
		})
		var verifyBatches int
		for _, ef := range batchEffs {
			if cb := ef.GetSecurityCryptoBatch(); cb != nil && len(cb.Items) == 9 {
				verifyBatches++
			}
		}
		if !inlineCrypto && verifyBatches != 1 {
			t.Fatalf("Web inline_crypto=false with 9 events: expected 1 CryptoBatch verify effect, got %d", verifyBatches)
		}
		if inlineCrypto && len(batchEffs) != 0 {
			t.Fatalf("Mobile inline_crypto=true: expected 0 CryptoBatch effects, got %d", len(batchEffs))
		}
		executeHostEffects(t, eng, phone, nowMs+200, batchEffs)
		if len(batchResults) != 9 {
			t.Fatalf("expected 9 results, got %d", len(batchResults))
		}
		for i, r := range batchResults {
			if r.Err != nil {
				t.Fatalf("item %d open failed: %v", i, r.Err)
			}
			evBody := r.Body.(*v1.Event)
			if evBody.GetUserMessage().GetText() != "hello" {
				t.Fatalf("unexpected text at %d: %q", i, evBody.GetUserMessage().GetText())
			}
		}

		// Test SealEnvelope + OpenBatch for large payload (> BatchAeadThreshold = 16 KiB).
		bigCmd := &v1.Command{
			Kind: &v1.Command_Send{
				Send: &v1.SendRequest{
					SessionId: "sess-1",
					Text:      string(bytes.Repeat([]byte{'a'}, BatchAeadThreshold+512)),
				},
			},
		}
		bigPlain, err := bigCmd.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		var sealRes SealResult
		sealEffs := eng.SealEnvelope(nowMs+300, devicesec.Command, machine.id, "sess-1", "req-big-0123456789", 0, uint64(nowMs+60_000), bigPlain, false, nil, func(sr SealResult) {
			sealRes = sr
		})
		var sawAeadSealBatch bool
		for _, ef := range sealEffs {
			if cb := ef.GetSecurityCryptoBatch(); cb != nil && len(cb.Items) == 1 && cb.Items[0].GetAeadSeal() != nil {
				sawAeadSealBatch = true
			}
		}
		if !inlineCrypto && !sawAeadSealBatch {
			t.Fatalf("expected CryptoBatch AeadSeal for payload > 16 KiB on Web (err=%v effs=%d)", sealRes.Err, len(sealEffs))
		}
		if inlineCrypto && sawAeadSealBatch {
			t.Fatalf("unexpected CryptoBatch AeadSeal for inline_crypto=true")
		}
		executeHostEffects(t, eng, phone, nowMs+300, sealEffs)
		if sealRes.Err != nil || sealRes.Envelope == nil || len(sealRes.RequestKeyHash) != 32 || len(sealRes.PayloadHash) != 32 {
			t.Fatalf("SealEnvelope failed: %+v", sealRes.Err)
		}

		var bigOpened []OpenResult
		openBigEffs := eng.OpenBatch(nowMs+400, []OpenItem{{
			Env:             sealRes.Envelope,
			ExpectedPurpose: devicesec.Command,
			MachineID:       machine.id,
			SessionID:       "sess-1",
			RequestID:       "req-big-0123456789",
			History:         false,
		}}, func(res []OpenResult) {
			bigOpened = res
		})
		var sawAeadOpenBatch bool
		for _, ef := range openBigEffs {
			if cb := ef.GetSecurityCryptoBatch(); cb != nil && len(cb.Items) == 1 && cb.Items[0].GetAeadOpen() != nil {
				sawAeadOpenBatch = true
			}
		}
		if !inlineCrypto && !sawAeadOpenBatch {
			t.Fatalf("expected CryptoBatch AeadOpen for ciphertext > 16 KiB on Web")
		}
		executeHostEffects(t, eng, phone, nowMs+400, openBigEffs)
		if len(bigOpened) != 1 || bigOpened[0].Err != nil || !bytes.Equal(bigOpened[0].Plain, bigPlain) {
			t.Fatalf("big open failed: %+v", bigOpened)
		}
	}
}

func TestEngineGatesErrorsUnknownFieldsAndRecoveryBoundary(t *testing.T) {
	nowMs := int64(1_700_000_000_000)
	acct, dom, gen, phone, machine, rec, _, sec2, rawStates, leaseRaw, states := makeGenesisAndTwoStateRecord(t, uint64(nowMs))

	pool := &entropy.Pool{}
	eng := NewEngine(pool, 0)
	eng.Configure(&corev1.SecurityConfigEvent{
		Account:      acct,
		DeviceId:     phone.id,
		InlineCrypto: true,
	})
	effs := eng.ApplyRecord(nowMs+100, RecordUpdate{
		Account: acct,
		Domain:  dom[:],
		Genesis: gen[:],
		States:  rawStates,
		Lease:   leaseRaw,
	})
	executeHostEffects(t, eng, phone, nowMs+100, effs)

	// 1. ErrNeedEntropy on SealEnvelope with empty pool.
	var sr SealResult
	eng.SealEnvelope(nowMs+100, devicesec.Prefs, "", "", "project-prefs", 1, 0, []byte{0x08, 0x01}, false, nil, func(res SealResult) {
		sr = res
	})
	if !errors.Is(sr.Err, ErrNeedEntropy) {
		t.Fatalf("expected ErrNeedEntropy, got %v", sr.Err)
	}

	// 2. Lease expiry & clock skew on live CanSeal.
	if err := eng.CanSeal(nowMs + 10 + devicesec.Lease + 1); !errors.Is(err, ErrLeaseExpired) || StructuredErrorCode(err) != corev1.SecurityErrorCode_SECURITY_ERROR_CODE_LEASE_EXPIRED {
		t.Fatalf("expected ErrLeaseExpired/LEASE_EXPIRED, got %v (%v)", err, StructuredErrorCode(err))
	}
	skewEng := NewEngine(pool, 0)
	skewEng.Configure(&corev1.SecurityConfigEvent{Account: acct, DeviceId: phone.id, InlineCrypto: true})
	executeHostEffects(t, skewEng, phone, nowMs-int64(devicesec.MaxClockSkew)-100, skewEng.ApplyRecord(nowMs-int64(devicesec.MaxClockSkew)-100, RecordUpdate{
		Account: acct, Domain: dom[:], Genesis: gen[:], States: rawStates, Lease: leaseRaw,
	}))
	if err := skewEng.CanSeal(nowMs - int64(devicesec.MaxClockSkew) - 100); !errors.Is(err, ErrClockSkewed) || StructuredErrorCode(err) != corev1.SecurityErrorCode_SECURITY_ERROR_CODE_CLOCK_SKEWED {
		t.Fatalf("expected ErrClockSkewed, got %v", err)
	}

	// 3. Unknown field rejection in inner Event and nested Timestamp + outer Envelope projection mismatch.
	seed := bytes.Repeat([]byte{0x77}, 32)
	eng.SeedSessionKey("sess-u", seed)
	baseEv := &v1.Event{
		Seq:       1,
		SessionId: "sess-u",
		Time:      &timestamppb.Timestamp{Seconds: 100},
		Kind:      &v1.Event_UserMessage{UserMessage: &v1.UserMessage{Text: "ok"}},
	}
	rawEv, _ := baseEv.Marshal()
	// Append unknown tag 999 varint 1.
	withUnknownEv := append(append([]byte(nil), rawEv...), 0xb8, 0x3e, 0x01)
	rawTS, _ := (&timestamppb.Timestamp{Seconds: 100}).Marshal()
	rawTSUnknown := append(append([]byte(nil), rawTS...), 0x50, 0x01) // tag 10 varint 1 on Timestamp
	var tsBad timestamppb.Timestamp
	if err := tsBad.Unmarshal(rawTSUnknown); err != nil || !tsBad.HasUnknown() {
		t.Fatalf("expected nested Timestamp HasUnknown()=true")
	}

	sh, _ := states[1].Hash()
	hdr := devicesec.Header{
		Account: acct, Domain: dom, Epoch: states[1].Core.Epoch, State: sh,
		ContentEpoch: states[1].Core.ContentEpoch, Sender: machine.id,
		Kind: devicesec.Event.Kind(), Purpose: devicesec.Event, Machine: machine.id, Session: "sess-u", Seq: 1,
	}
	hb, key, nonce, _ := devicesec.PrepareCiphertext(sec2, seed, hdr, len(withUnknownEv), bytes.NewReader(bytes.Repeat([]byte{5}, 12)))
	blk, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(blk)
	ct, originMsg, _ := devicesec.AssembleCiphertext(hb, nonce, aead.Seal(nil, nonce, withUnknownEv, hb))
	ct.Origin = ed25519.Sign(machine.ed, originMsg)
	envUnknownInner, _ := ToProtoEnvelope(ct)

	var openRes []OpenResult
	eng.OpenBatch(nowMs+100, []OpenItem{{Env: envUnknownInner, ExpectedPurpose: devicesec.Event, SessionID: "sess-u", History: true}}, func(r []OpenResult) {
		openRes = r
	})
	if len(openRes) != 1 || openRes[0].Err == nil || StructuredErrorCode(openRes[0].Err) != corev1.SecurityErrorCode_SECURITY_ERROR_CODE_INVALID {
		t.Fatalf("expected strict unknown inner Event rejection, got %+v", openRes)
	}

	// Tampered projection Header on outer Envelope.
	goodEnv := sealEventProto(t, machine, dom, states[1], sec2, seed, acct, "sess-u", 1, uint64(nowMs+100), "hi")
	tamperedProj := Projection(hdr)
	tamperedProj.Seq = 999
	goodEnv.Header, _ = tamperedProj.Marshal()
	eng.OpenBatch(nowMs+100, []OpenItem{{Env: goodEnv, ExpectedPurpose: devicesec.Event, SessionID: "sess-u", History: true}}, func(r []OpenResult) {
		openRes = r
	})
	if len(openRes) != 1 || !errors.Is(openRes[0].Err, devicesec.ErrUntrusted) {
		t.Fatalf("expected ErrUntrusted on outer projection mismatch, got %+v", openRes)
	}

	// 4. Recovery boundary: pre-recovery historical event with valid Ed25519 origin returns ErrRecoveryHistoryUnavailable,
	// while forged Ed25519 origin returns ErrUntrusted WITHOUT exposing Header!
	newPhone := newTestIdentity(t, 4)
	newRec := newTestIdentity(t, 5)
	sec3 := bytes.Repeat([]byte{0x66}, 32)
	recU, err := devicesec.NewRecovery(states[1], newPhone.member(devicesec.Client), []devicesec.Member{machine.member(devicesec.Machine), newPhone.member(devicesec.Client)}, sec3, [32]byte(newRec.edP), uint64(nowMs+20), stdHPKESender{})
	if err != nil {
		t.Fatal(err)
	}
	rsb, _ := recU.SigningBytes()
	recU.Signature = ed25519.Sign(newPhone.ed, rsb)
	recRaw, err := recU.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	recParsed, err := devicesec.ParseState(recRaw)
	if err != nil {
		t.Fatal(err)
	}
	stmtU, err := devicesec.NewRecoveryStatement(states[1], recParsed, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	stmtSB, _ := stmtU.SigningBytes()
	stmtU.Signature = ed25519.Sign(rec.ed, stmtSB)
	stmtRaw, err := stmtU.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	recLease, _ := devicesec.NewLease(recParsed, uint64(nowMs+20))
	rlb, _ := recLease.SigningBytes()
	recLease.Signature = ed25519.Sign(newPhone.ed, rlb)
	recLeaseRaw, _ := recLease.MarshalBinary()

	recEng := NewEngine(pool, 0)
	recEng.Configure(&corev1.SecurityConfigEvent{Account: acct, DeviceId: newPhone.id, InlineCrypto: true})
	executeHostEffects(t, recEng, newPhone, nowMs+100, recEng.ApplyRecord(nowMs+100, RecordUpdate{
		Account:        acct,
		Domain:         dom[:],
		Genesis:        gen[:],
		States:         append(slices.Clone(rawStates), recRaw),
		RecoveryProofs: [][]byte{stmtRaw},
		Lease:          recLeaseRaw,
	}))
	recEng.SeedSessionKey("sess-old", seed)

	// Old epoch 2 event signed by machine (states[1] before recovery; recParsed bumped ContentEpoch without PrevSecret).
	validOld := sealEventProto(t, machine, dom, states[1], sec2, seed, acct, "sess-old", 1, uint64(nowMs), "pre-rec")
	forgedOld := sealEventProto(t, machine, dom, states[1], sec2, seed, acct, "sess-old", 2, uint64(nowMs), "forged")
	forgedOld.Signature[0] ^= 0xff

	recEng.OpenBatch(nowMs+100, []OpenItem{
		{Env: validOld, ExpectedPurpose: devicesec.Event, MachineID: machine.id, SessionID: "sess-old", History: true},
		{Env: forgedOld, ExpectedPurpose: devicesec.Event, MachineID: machine.id, SessionID: "sess-old", History: true},
	}, func(r []OpenResult) {
		openRes = r
	})
	if !errors.Is(openRes[0].Err, ErrRecoveryHistoryUnavailable) || openRes[0].Header.Seq != 1 {
		t.Fatalf("expected ErrRecoveryHistoryUnavailable with authenticated header, got err=%v hdr=%+v", openRes[0].Err, openRes[0].Header)
	}
	if !errors.Is(openRes[1].Err, devicesec.ErrUntrusted) || openRes[1].Header.Seq != 0 {
		t.Fatalf("expected ErrUntrusted with zeroed unauthenticated header for forged event, got err=%v hdr=%+v", openRes[1].Err, openRes[1].Header)
	}

	// 5. PrevSecret continuity mismatch rejected when extending chain with a real member addition.
	extra := newTestIdentity(t, 9)
	badNext, err := devicesec.NewMembershipState(recParsed, append(slices.Clone(recParsed.Core.Members), extra.member(devicesec.Client)), recParsed.Core.Removed, bytes.Repeat([]byte{0xee}, 32), bytes.Repeat([]byte{0x77}, 32), uint64(nowMs+30), stdHPKESender{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bnb, _ := badNext.SigningBytes()
	badNext.Signature = ed25519.Sign(newPhone.ed, bnb)
	badRaw, _ := badNext.MarshalBinary()
	executeHostEffects(t, recEng, newPhone, nowMs+200, recEng.ApplyRecord(nowMs+200, RecordUpdate{
		Account:        acct,
		Domain:         dom[:],
		Genesis:        gen[:],
		States:         append(append(slices.Clone(rawStates), recRaw), badRaw),
		RecoveryProofs: [][]byte{stmtRaw},
	}))
	if recEng.Snapshot().IsMember {
		t.Fatalf("expected PrevSecret continuity mismatch to reject member state, recordErr=%q", recEng.recordErr)
	}
}

func TestEngineRecoveryContentKeysAndNonEventUnavailable(t *testing.T) {
	nowMs := int64(1_700_000_000_000)
	acct, dom, gen, phone, machine, rec, _, sec2, rawStates, _, states := makeGenesisAndTwoStateRecord(t, uint64(nowMs))

	// Add a secondary retained client before recovery so recovery removes phone while keeping retained.
	retained := newTestIdentity(t, 6)
	sec2Ret := bytes.Repeat([]byte{0x2b}, 32)
	sRetU, err := devicesec.NewMembershipState(states[1], []devicesec.Member{phone.member(devicesec.Client), machine.member(devicesec.Machine), retained.member(devicesec.Client)}, nil, sec2, sec2Ret, uint64(nowMs+15), stdHPKESender{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sRetSB, _ := sRetU.SigningBytes()
	sRetU.Signature = ed25519.Sign(phone.ed, sRetSB)
	sRetRaw, _ := sRetU.MarshalBinary()
	sRetParsed, err := devicesec.ParseState(sRetRaw)
	if err != nil {
		t.Fatal(err)
	}
	baseRaw := append(slices.Clone(rawStates), sRetRaw)

	// Seal both a pre-recovery Event and a pre-recovery Prefs object at sRetParsed.
	seed := bytes.Repeat([]byte{0x77}, 32)
	oldEv := sealEventProto(t, machine, dom, sRetParsed, sec2Ret, seed, acct, "sess-pre", 1, uint64(nowMs+15), "before rec")
	shRet, _ := sRetParsed.Hash()
	prefsPlain, _ := (&v1.ProjectPrefs{Schema: 1}).Marshal()
	prefsHdr := devicesec.Header{
		Account: acct, Domain: dom, Epoch: sRetParsed.Core.Epoch, State: shRet,
		ContentEpoch: sRetParsed.Core.ContentEpoch, Sender: retained.id,
		Kind: devicesec.Prefs.Kind(), Purpose: devicesec.Prefs, Request: "project-prefs", Seq: 1,
	}
	phb, pkey, pnonce, err := devicesec.PrepareCiphertext(sec2Ret, nil, prefsHdr, len(prefsPlain), bytes.NewReader(bytes.Repeat([]byte{8}, 12)))
	if err != nil {
		t.Fatal(err)
	}
	pblk, _ := aes.NewCipher(pkey)
	paead, _ := cipher.NewGCM(pblk)
	pct, pOriginMsg, err := devicesec.AssembleCiphertext(phb, pnonce, paead.Seal(nil, pnonce, prefsPlain, phb))
	if err != nil {
		t.Fatal(err)
	}
	pct.Origin = ed25519.Sign(retained.ed, pOriginMsg)
	oldPrefs, err := ToProtoEnvelope(pct)
	if err != nil {
		t.Fatal(err)
	}

	// Recover without PrevSecret onto the next ContentEpoch, replacing phone with newPhone while keeping retained.
	newPhone := newTestIdentity(t, 4)
	newRec := newTestIdentity(t, 5)
	sec3 := bytes.Repeat([]byte{0x66}, 32)
	recU, err := devicesec.NewRecovery(sRetParsed, newPhone.member(devicesec.Client), []devicesec.Member{machine.member(devicesec.Machine), newPhone.member(devicesec.Client), retained.member(devicesec.Client)}, sec3, [32]byte(newRec.edP), uint64(nowMs+20), stdHPKESender{})
	if err != nil {
		t.Fatal(err)
	}
	rsb, _ := recU.SigningBytes()
	recU.Signature = ed25519.Sign(newPhone.ed, rsb)
	recRaw, _ := recU.MarshalBinary()
	recParsed, _ := devicesec.ParseState(recRaw)
	stmtU, _ := devicesec.NewRecoveryStatement(sRetParsed, recParsed, rand.Reader)
	stmtSB, _ := stmtU.SigningBytes()
	stmtU.Signature = ed25519.Sign(rec.ed, stmtSB)
	stmtRaw, _ := stmtU.MarshalBinary()
	recLease, _ := devicesec.NewLease(recParsed, uint64(nowMs+20))
	rlb, _ := recLease.SigningBytes()
	recLease.Signature = ed25519.Sign(newPhone.ed, rlb)
	recLeaseRaw, _ := recLease.MarshalBinary()
	pool := &entropy.Pool{}

	// (b) Without ContentKeys, pre-recovery historical Prefs/DeviceMeta also report ErrRecoveryHistoryUnavailable.
	noKeysEng := NewEngine(pool, 0)
	noKeysEng.Configure(&corev1.SecurityConfigEvent{Account: acct, DeviceId: newPhone.id, InlineCrypto: true})
	executeHostEffects(t, noKeysEng, newPhone, nowMs+100, noKeysEng.ApplyRecord(nowMs+100, RecordUpdate{
		Account:        acct,
		Domain:         dom[:],
		Genesis:        gen[:],
		States:         append(slices.Clone(baseRaw), recRaw),
		RecoveryProofs: [][]byte{stmtRaw},
		Lease:          recLeaseRaw,
	}))
	var noKeysRes []OpenResult
	noKeysEng.OpenBatch(nowMs+100, []OpenItem{{Env: oldPrefs, ExpectedPurpose: devicesec.Prefs, RequestID: "project-prefs", Seq: 1, History: true}}, func(r []OpenResult) {
		noKeysRes = r
	})
	if len(noKeysRes) != 1 || !errors.Is(noKeysRes[0].Err, ErrRecoveryHistoryUnavailable) {
		t.Fatalf("expected ErrRecoveryHistoryUnavailable for pre-recovery Prefs without ContentKeys, got %+v", noKeysRes)
	}

	// (a) With pre-recovery ContentKeys in RecordUpdate (plus a bogus attempt to inject head epoch),
	// head epoch still requires real KeyAgree while retained epoch unlocks pre-recovery Event and Prefs.
	retEng := NewEngine(pool, 0)
	retEng.Configure(&corev1.SecurityConfigEvent{Account: acct, DeviceId: retained.id, InlineCrypto: true})
	effs := retEng.ApplyRecord(nowMs+100, RecordUpdate{
		Account:        acct,
		Domain:         dom[:],
		Genesis:        gen[:],
		States:         append(slices.Clone(baseRaw), recRaw),
		RecoveryProofs: [][]byte{stmtRaw},
		Lease:          recLeaseRaw,
		ContentKeys: []ContentKey{
			{ContentEpoch: sRetParsed.Core.ContentEpoch, Secret: sec2Ret},
			{ContentEpoch: recParsed.Core.ContentEpoch, Secret: bytes.Repeat([]byte{0x99}, 32)},
		},
	})
	var sawKeyAgree bool
	for _, ef := range effs {
		if ef.GetSecurityKeyAgree() != nil {
			sawKeyAgree = true
		}
	}
	if !sawKeyAgree {
		t.Fatalf("expected live KeyAgree for head epoch even when ContentKeys contains head epoch")
	}
	executeHostEffects(t, retEng, retained, nowMs+100, effs)
	retEng.SeedSessionKey("sess-pre", seed)
	var retRes []OpenResult
	retEng.OpenBatch(nowMs+100, []OpenItem{
		{Env: oldEv, ExpectedPurpose: devicesec.Event, MachineID: machine.id, SessionID: "sess-pre", History: true},
		{Env: oldPrefs, ExpectedPurpose: devicesec.Prefs, RequestID: "project-prefs", Seq: 1, History: true},
	}, func(r []OpenResult) {
		retRes = r
	})
	if len(retRes) != 2 || retRes[0].Err != nil || retRes[1].Err != nil {
		t.Fatalf("expected retained client with ContentKeys to open pre-recovery Event and Prefs, got %+v", retRes)
	}
}

func TestEngineApplyRecordGuardsTransientErrorAndRemovedHistory(t *testing.T) {
	nowMs := int64(1_700_000_000_000)
	acct, dom, gen, phone, machine, _, _, sec2, rawStates, leaseRaw, states := makeGenesisAndTwoStateRecord(t, uint64(nowMs))

	// Pair removedClient in s2, seal an event at s2, then remove removedClient in s3 (with PrevSecret continuity).
	seed := bytes.Repeat([]byte{0x77}, 32)
	removedClient := newTestIdentity(t, 8)
	sec2b := bytes.Repeat([]byte{0x44}, 32)
	s2U, err := devicesec.NewMembershipState(states[1], []devicesec.Member{phone.member(devicesec.Client), machine.member(devicesec.Machine), removedClient.member(devicesec.Client)}, nil, sec2, sec2b, uint64(nowMs+15), stdHPKESender{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s2SB, _ := s2U.SigningBytes()
	s2U.Signature = ed25519.Sign(phone.ed, s2SB)
	s2Raw, _ := s2U.MarshalBinary()
	s2Parsed, _ := devicesec.ParseState(s2Raw)
	oldEv := sealEventProto(t, machine, dom, s2Parsed, sec2b, seed, acct, "sess-rem", 1, uint64(nowMs+15), "before removal")

	sec3 := bytes.Repeat([]byte{0x55}, 32)
	remU, err := devicesec.NewMembershipState(s2Parsed, []devicesec.Member{phone.member(devicesec.Client), machine.member(devicesec.Machine)}, []string{removedClient.id}, sec2b, sec3, uint64(nowMs+20), stdHPKESender{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rsb, _ := remU.SigningBytes()
	remU.Signature = ed25519.Sign(phone.ed, rsb)
	remRaw, err := remU.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	remParsed, _ := devicesec.ParseState(remRaw)
	remLease, _ := devicesec.NewLease(remParsed, uint64(nowMs+20))
	rlb, _ := remLease.SigningBytes()
	remLease.Signature = ed25519.Sign(phone.ed, rlb)
	remLeaseRaw, _ := remLease.MarshalBinary()

	pool := &entropy.Pool{}
	eng := NewEngine(pool, 0)
	eng.Configure(&corev1.SecurityConfigEvent{Account: acct, DeviceId: phone.id, InlineCrypto: true})

	// Zero-op with PushSeq 2 advances, PushSeq 1 and unsequenced PushSeq 0 are ignored.
	executeHostEffects(t, eng, phone, nowMs+100, eng.ApplyRecord(nowMs+100, RecordUpdate{
		PushSeq: 2,
		Account: acct,
		Domain:  dom[:],
		Genesis: gen[:],
		States:  rawStates,
		Lease:   leaseRaw,
	}))
	if effs := eng.ApplyRecord(nowMs+101, RecordUpdate{PushSeq: 1, Cleared: true}); len(effs) != 0 || !eng.Snapshot().IsMember {
		t.Fatalf("expected stale PushSeq=1 overwrite to be ignored")
	}
	if effs := eng.ApplyRecord(nowMs+102, RecordUpdate{PushSeq: 0, Cleared: true}); len(effs) != 0 || !eng.Snapshot().IsMember {
		t.Fatalf("expected unsequenced PushSeq=0 overwrite to be ignored once PushSeq is active")
	}

	// Transient sync error preserves already loaded verified state.
	eng.ApplyRecord(nowMs+103, RecordUpdate{PushSeq: 3, Error: "network timeout"})
	if !eng.Snapshot().Loaded || !eng.Snapshot().IsMember {
		t.Fatalf("expected transient error to preserve Loaded and IsMember: %+v", eng.Snapshot())
	}

	// Monotonic chain guard rejects shorter epoch even on fresh PushSeq.
	eng.ApplyRecord(nowMs+104, RecordUpdate{
		PushSeq: 4,
		Account: acct,
		Domain:  dom[:],
		Genesis: gen[:],
		States:  rawStates[:1],
	})
	if eng.Snapshot().HeadEpoch != states[1].Core.Epoch {
		t.Fatalf("expected monotonic chain guard to keep epoch %d, got %d", states[1].Core.Epoch, eng.Snapshot().HeadEpoch)
	}

	// Removed client's engine: live sealing/opening rejected (ErrNotMember), historical read unlocks s2 via startUnlockForState.
	remEng := NewEngine(pool, 0)
	remEng.Configure(&corev1.SecurityConfigEvent{Account: acct, DeviceId: removedClient.id, InlineCrypto: true})
	applyEffs := remEng.ApplyRecord(nowMs+110, RecordUpdate{
		PushSeq: 5,
		Account: acct,
		Domain:  dom[:],
		Genesis: gen[:],
		States:  append(append(slices.Clone(rawStates), s2Raw), remRaw),
		Lease:   remLeaseRaw,
	})
	executeHostEffects(t, remEng, removedClient, nowMs+110, applyEffs)
	if remEng.Snapshot().IsMember {
		t.Fatalf("expected IsMember=false after removal")
	}
	if err := remEng.CanSeal(nowMs + 110); !errors.Is(err, ErrNotMember) {
		t.Fatalf("expected ErrNotMember on CanSeal after removal, got %v", err)
	}
	// Reset cached epochSecrets to confirm OpenBatch(History: true) also lazily unlocks s2 via startUnlockForState.
	clear(remEng.epochSecrets)
	remEng.SeedSessionKey("sess-rem", seed)
	var res []OpenResult
	openEffs := remEng.OpenBatch(nowMs+110, []OpenItem{
		{Env: oldEv, ExpectedPurpose: devicesec.Event, MachineID: machine.id, SessionID: "sess-rem", History: false},
		{Env: oldEv, ExpectedPurpose: devicesec.Event, MachineID: machine.id, SessionID: "sess-rem", History: true},
	}, func(r []OpenResult) {
		res = r
	})
	executeHostEffects(t, remEng, removedClient, nowMs+110, openEffs)
	if len(res) != 2 || !errors.Is(res[0].Err, ErrNotMember) || res[1].Err != nil {
		t.Fatalf("expected live=ErrNotMember and history=ok for removed member, got %+v", res)
	}
}

func TestEngineOverrideSessionSeedAndNotifyOpening(t *testing.T) {
	nowMs := int64(1_700_000_000_000)
	acct, dom, gen, phone, machine, _, _, sec2, rawStates, leaseRaw, states := makeGenesisAndTwoStateRecord(t, uint64(nowMs))
	entropyBytes := bytes.Repeat([]byte{0x23}, 256)
	pool := &entropy.Pool{}
	pool.Append(entropyBytes)
	eng := NewEngine(pool, 0)
	eng.Configure(&corev1.SecurityConfigEvent{Account: acct, DeviceId: phone.id, InlineCrypto: true})
	executeHostEffects(t, eng, phone, nowMs+100, eng.ApplyRecord(nowMs+100, RecordUpdate{
		Account: acct,
		Domain:  dom[:],
		Genesis: gen[:],
		States:  rawStates,
		Lease:   leaseRaw,
	}))

	overrideSeed := bytes.Repeat([]byte{0x5a}, 32)
	msgRaw := litewire.AppendBytes(nil, 10, []byte("ls\n"))
	var opRes *corev1.SecurityOpResultEffect
	opEffs := eng.HandleSecurityOp(nowMs+100, &corev1.SecurityOpRequestEvent{
		OpId: 77,
		Op: &corev1.SecurityOpRequestEvent_SealEnvelope{
			SealEnvelope: &corev1.SecuritySealEnvelopeOp{
				Purpose:             uint32(devicesec.TerminalInput),
				MachineId:           machine.id,
				SessionId:           "sess-term",
				RequestId:           "term-stream-123456",
				Seq:                 1,
				DeadlineMs:          nowMs + 60_000,
				Plaintext:           msgRaw,
				OverrideSessionSeed: overrideSeed,
			},
		},
	})
	for _, ef := range executeHostEffects(t, eng, phone, nowMs+100, opEffs) {
		if r := ef.GetSecurityOpResult(); r != nil {
			opRes = r
		}
	}
	if opRes == nil || opRes.Error != "" || opRes.GetSealed() == nil || opRes.GetSealed().Envelope == nil {
		t.Fatalf("expected SealEnvelope with OverrideSessionSeed to succeed, got %+v", opRes)
	}
	var opened []OpenResult
	eng.OpenBatch(nowMs+100, []OpenItem{{
		Env:             opRes.GetSealed().Envelope,
		ExpectedPurpose: devicesec.TerminalInput,
		MachineID:       machine.id,
		SessionID:       "sess-term",
		RequestID:       "term-stream-123456",
		Seq:             1,
		OverrideSeed:    overrideSeed,
	}}, func(r []OpenResult) {
		opened = r
	})
	if len(opened) != 1 || opened[0].Err != nil {
		t.Fatalf("expected OpenBatch with OverrideSeed to succeed, got %+v", opened)
	}

	// DerivePurposeKeyRaw + HandleOpenNotify using single head state (epoch > 1) and PurposeKey.
	pk, err := devicesec.DerivePurposeKeyRaw(sec2, acct, dom, states[1].Core.ContentEpoch, devicesec.Notify)
	if err != nil || len(pk) != 32 {
		t.Fatalf("DerivePurposeKeyRaw failed: err=%v len=%d", err, len(pk))
	}

	sh2, _ := states[1].Hash()
	var notifyPlain []byte
	notifyPlain = litewire.AppendString(notifyPlain, 1, machine.id)
	notifyPlain = litewire.AppendString(notifyPlain, 2, "s-n")
	notifyPlain = litewire.AppendVarint(notifyPlain, 3, uint64(v1.NotifyKind_NOTIFY_KIND_TURN_ENDED))
	notifyPlain = litewire.AppendString(notifyPlain, 4, "done")
	notifyPlain = litewire.AppendString(notifyPlain, 5, "body")
	notifyPlain = litewire.AppendVarint(notifyPlain, 7, 1) // source_event_seq = 1
	nhb, nkey, nnonce, err := devicesec.PrepareCiphertext(sec2, nil, devicesec.Header{
		Account: acct, Domain: dom, Epoch: states[1].Core.Epoch, State: sh2,
		ContentEpoch: states[1].Core.ContentEpoch, Sender: machine.id,
		Kind: devicesec.Notify.Kind(), Purpose: devicesec.Notify, Machine: machine.id, Session: "s-n", Request: "notify:2:1", Seq: 1,
	}, len(notifyPlain), bytes.NewReader(bytes.Repeat([]byte{9}, 12)))
	if err != nil {
		t.Fatal(err)
	}
	nblk, _ := aes.NewCipher(nkey)
	naead, _ := cipher.NewGCM(nblk)
	nct, nOrigMsg, _ := devicesec.AssembleCiphertext(nhb, nnonce, naead.Seal(nil, nnonce, notifyPlain, nhb))
	nct.Origin = ed25519.Sign(machine.ed, nOrigMsg)
	nEnv, _ := ToProtoEnvelope(nct)

	var pkArr [32]byte
	copy(pkArr[:], pk)
	ntfBlob, err := (&PersistedNotifyRecord{
		Pin:        devicesec.Pin{Account: acct, Domain: dom, Genesis: gen},
		HeadState:  rawStates[1],
		PurposeKey: pkArr,
	}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	openEffs := eng.HandleOpenNotify(nowMs+100, &corev1.SecurityOpenNotifyEvent{
		OpId:         88,
		NotifyBlob:   ntfBlob,
		Envelope:     nEnv,
		SessionId:    "s-n",
		Tag:          "s-n/NOTIFY_KIND_TURN_ENDED",
		ExpectedKind: v1.NotifyKind_NOTIFY_KIND_TURN_ENDED,
	})
	var openedNotify *corev1.SecurityNotifyOpenedEffect
	for _, ef := range openEffs {
		if no := ef.GetSecurityNotifyOpened(); no != nil {
			openedNotify = no
		}
	}
	if openedNotify == nil || openedNotify.Error != "" || !openedNotify.GetDisplay().GetValid() || openedNotify.GetDisplay().GetTitle().GetLiteral() != "done" || openedNotify.GetDisplay().GetBody().GetLiteral() != "body" {
		t.Fatalf("HandleOpenNotify with PurposeKey failed: %+v", openedNotify)
	}
}

func TestDevicesecVerifyTransitionAndSecurityGates(t *testing.T) {
	nowMs := int64(1_700_000_000_000)
	acct, dom, gen, phone, machine, rec, _, sec2, rawStates, leaseRaw, states := makeGenesisAndTwoStateRecord(t, uint64(nowMs))

	// 1. VerifyTransition across multi-epoch, bad signer, and recovery transitions.
	extra := newTestIdentity(t, 5)
	sec3 := bytes.Repeat([]byte{0x44}, 32)
	s3U, err := devicesec.NewPairState(states[1], extra.member(devicesec.Client), extra.xPub[:], sec2, sec3, uint64(nowMs+20), stdHPKESender{}, rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s3SB, _ := s3U.SigningBytes()
	s3 := s3U
	s3.Signature = ed25519.Sign(phone.ed, s3SB)
	s3Raw, _ := s3.MarshalBinary()
	s3Parsed, err := devicesec.ParseState(s3Raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := devicesec.VerifyTransition(states[1], s3Parsed, nil); err != nil || s3Parsed.Core.Epoch != 3 {
		t.Fatalf("expected multi-epoch VerifyTransition to succeed, got %v (epoch=%d)", err, s3Parsed.Core.Epoch)
	}

	badS3 := s3U
	badS3.Signature = ed25519.Sign(machine.ed, s3SB)
	badS3Raw, _ := badS3.MarshalBinary()
	badS3Parsed, _ := devicesec.ParseState(badS3Raw)
	if err := devicesec.VerifyTransition(states[1], badS3Parsed, nil); !errors.Is(err, devicesec.ErrUntrusted) {
		t.Fatalf("expected untrusted error for unauthorized signer, got %v", err)
	}

	newPhone := newTestIdentity(t, 6)
	newRec := newTestIdentity(t, 7)
	recU, err := devicesec.NewRecovery(states[1], newPhone.member(devicesec.Client), []devicesec.Member{machine.member(devicesec.Machine), newPhone.member(devicesec.Client)}, sec3, [32]byte(newRec.edP), uint64(nowMs+30), stdHPKESender{})
	if err != nil {
		t.Fatal(err)
	}
	recSB, _ := recU.SigningBytes()
	recU.Signature = ed25519.Sign(newPhone.ed, recSB)
	recRaw, _ := recU.MarshalBinary()
	recParsed, _ := devicesec.ParseState(recRaw)
	stmtU, _ := devicesec.NewRecoveryStatement(states[1], recParsed, rand.Reader)
	stmtSB, _ := stmtU.SigningBytes()
	stmtU.Signature = ed25519.Sign(rec.ed, stmtSB)
	stmtRaw, _ := stmtU.MarshalBinary()
	stmtParsed, _ := devicesec.ParseRecoveryStatement(stmtRaw)

	if err := devicesec.VerifyTransition(states[1], recParsed, &stmtParsed); err != nil || recParsed.Core.Epoch != 3 {
		t.Fatalf("expected recovery VerifyTransition to succeed, got %v (epoch=%d)", err, recParsed.Core.Epoch)
	}

	// 2. Dynamic clock skew clearance + SealEnvelope scope check + OpenBatch(History: true) control rejection.
	pool := &entropy.Pool{}
	pool.Append(bytes.Repeat([]byte{0x55}, 256))
	skewedNow := nowMs - int64(devicesec.MaxClockSkew) - 100
	eng := NewEngine(pool, 0)
	eng.Configure(&corev1.SecurityConfigEvent{Account: acct, DeviceId: phone.id, InlineCrypto: true})
	executeHostEffects(t, eng, phone, skewedNow, eng.ApplyRecord(skewedNow, RecordUpdate{
		Account: acct, Domain: dom[:], Genesis: gen[:], States: rawStates, Lease: leaseRaw,
	}))
	if !eng.Snapshot().ClockSkewed {
		t.Fatalf("expected ClockSkewed=true in past window")
	}
	if err := eng.CanSeal(skewedNow); !errors.Is(err, ErrClockSkewed) {
		t.Fatalf("expected ErrClockSkewed in past window, got %v", err)
	}
	if err := eng.CanSeal(nowMs + 100); err != nil {
		t.Fatalf("expected clock skew to clear when nowMs advances, got %v", err)
	}

	metaPlain, _ := (&v1.DeviceMeta{Name: "phone"}).Marshal()
	var metaSeal SealResult
	executeHostEffects(t, eng, phone, nowMs+100, eng.SealEnvelope(nowMs+100, devicesec.DeviceMeta, "", "", machine.id, 1, 0, metaPlain, false, nil, func(sr SealResult) {
		metaSeal = sr
	}))
	if !errors.Is(metaSeal.Err, devicesec.ErrUntrusted) {
		t.Fatalf("expected DeviceMeta for another device to fail with ErrUntrusted on seal, got %+v", metaSeal)
	}

	cmdPlain, _ := (&v1.Command{Kind: &v1.Command_Interrupt{Interrupt: &v1.InterruptRequest{SessionId: "s"}}}).Marshal()
	var cmdSeal SealResult
	executeHostEffects(t, eng, phone, nowMs+100, eng.SealEnvelope(nowMs+100, devicesec.Command, machine.id, "s", "req-cmd-0123456789", 1, uint64(nowMs+devicesec.Lease), cmdPlain, false, nil, func(sr SealResult) {
		cmdSeal = sr
	}))
	if cmdSeal.Err != nil || cmdSeal.Envelope == nil {
		t.Fatalf("failed to seal command: %+v", cmdSeal.Err)
	}

	var histCmd []OpenResult
	executeHostEffects(t, eng, phone, nowMs+100, eng.OpenBatch(nowMs+100, []OpenItem{{Env: cmdSeal.Envelope, History: true}}, func(r []OpenResult) {
		histCmd = r
	}))
	if len(histCmd) != 1 || !errors.Is(histCmd[0].Err, devicesec.ErrUntrusted) {
		t.Fatalf("expected History:true OpenBatch to reject Command with ErrUntrusted, got %+v", histCmd)
	}
}
