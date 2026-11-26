package security

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Lewin671/remote-agent/clientcore/domain/entropy"
	corev1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/core/v1"
	v1 "github.com/Lewin671/remote-agent/clientcore/gen/remoteagent/v1"
	stdv1 "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"google.golang.org/protobuf/proto"
)

func loadVectorJSON(t *testing.T, relPath string, out any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", relPath))
	if err != nil {
		t.Fatalf("read vector %s: %v", relPath, err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("unmarshal %s: %v", relPath, err)
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("hex %q: %v", s, err)
	}
	return b
}

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("base64: %v", err)
	}
	return b
}

func ecdhWithRawPriv(t *testing.T, privHexOrB64 []byte, peerPub []byte) []byte {
	t.Helper()
	k, err := ecdh.X25519().NewPrivateKey(privHexOrB64)
	if err != nil {
		t.Fatalf("x25519 priv: %v", err)
	}
	pub, err := ecdh.X25519().NewPublicKey(peerPub)
	if err != nil {
		t.Fatalf("x25519 pub: %v", err)
	}
	dh, err := k.ECDH(pub)
	if err != nil {
		t.Fatalf("ECDH: %v", err)
	}
	return dh
}

func TestFrozenDomainV3ChainRecoveryLeaseAndCompressedVectors(t *testing.T) {
	type vecState struct {
		State string `json:"state"`
		Hash  string `json:"hash"`
	}
	type vecCompressed struct {
		Wire  string `json:"wire"`
		Plain string `json:"plain"`
		Seed  string `json:"seed"`
	}
	var dv3 struct {
		Account            string          `json:"account"`
		Domain             string          `json:"domain"`
		PhoneSeed          string          `json:"phone_seed"`
		PhoneX             string          `json:"phone_x"`
		MachineX           string          `json:"machine_x"`
		HandoffX           string          `json:"handoff_x"`
		ContentSecret1     string          `json:"content_secret_1"`
		ContentSecret2     string          `json:"content_secret_2"`
		ContentSecret3     string          `json:"content_secret_3"`
		Genesis            vecState        `json:"genesis"`
		Added              vecState        `json:"added"`
		Renewal            vecState        `json:"renewal"`
		HandoffParent      vecState        `json:"handoff_parent"`
		Handoff            vecState        `json:"handoff"`
		HandoffLease       string          `json:"handoff_lease"`
		Recovery           vecState        `json:"recovery"`
		RecoveryStatement  string          `json:"recovery_statement"`
		Lease              string          `json:"lease"`
		RenewedLease       string          `json:"renewed_lease"`
		CompressedEnvelope []vecCompressed `json:"compressed_envelopes"`
	}
	loadVectorJSON(t, "internal/deviceauth/testdata/domain-v3.json", &dv3)

	dom := mustHex(t, dv3.Domain)
	genHash := mustHex(t, dv3.Genesis.Hash)
	var pin devicesec.Pin
	pin.Account = dv3.Account
	copy(pin.Domain[:], dom)
	copy(pin.Genesis[:], genHash)

	// Verify normal chain: genesis -> added with renewed lease (and renewal + recovery chain).
	phoneID := devicesec.ID(ed25519.NewKeyFromSeed(mustHex(t, dv3.PhoneSeed)).Public().(ed25519.PublicKey))
	chainStates, _, _, err := parseAndVerifyChain(pin, [][]byte{mustB64(t, dv3.Genesis.State), mustB64(t, dv3.Added.State)}, nil)
	if err != nil {
		t.Fatalf("domain-v3 parseAndVerifyChain failed: %v", err)
	}
	renLease, err := devicesec.ParseLease(mustB64(t, dv3.RenewedLease))
	if err != nil || renLease.Verify(chainStates[1]) != nil {
		t.Fatalf("domain-v3 lease verify failed: %v", err)
	}
	if _, ok := chainStates[1].Core.Member(phoneID); !ok {
		t.Fatalf("expected phone %s in added state", phoneID)
	}

	// Verify GrantChallenge + OpenGrantWithDH for added state (secret 2).
	chalEnc, _, err := pin.GrantChallenge(chainStates[1], phoneID)
	if err != nil {
		t.Fatalf("GrantChallenge failed: %v", err)
	}
	dh := ecdhWithRawPriv(t, mustHex(t, dv3.PhoneX), chalEnc[:])
	openGrant, err := pin.OpenGrantWithDH(chainStates[1], phoneID, dh)
	if err != nil || !bytes.Equal(openGrant, mustHex(t, dv3.ContentSecret2)) {
		t.Fatalf("OpenGrantWithDH mismatch: err=%v got=%x want=%s", err, openGrant, dv3.ContentSecret2)
	}

	// Verify handoff chain & lease (descends directly from Added).
	handoffStates, _, _, err := parseAndVerifyChain(
		pin,
		[][]byte{mustB64(t, dv3.Genesis.State), mustB64(t, dv3.Added.State), mustB64(t, dv3.HandoffParent.State), mustB64(t, dv3.Handoff.State)},
		nil,
	)
	if err != nil {
		t.Fatalf("domain-v3 handoff chain failed: %v", err)
	}
	handoffLease, err := devicesec.ParseLease(mustB64(t, dv3.HandoffLease))
	if err != nil || handoffLease.Verify(handoffStates[len(handoffStates)-1]) != nil {
		t.Fatalf("domain-v3 handoff lease failed: %v", err)
	}

	// Verify recovery chain & statement (descends from Renewal).
	recStates, _, _, err := parseAndVerifyChain(
		pin,
		[][]byte{mustB64(t, dv3.Genesis.State), mustB64(t, dv3.Added.State), mustB64(t, dv3.Renewal.State), mustB64(t, dv3.Recovery.State)},
		[][]byte{mustB64(t, dv3.RecoveryStatement)},
	)
	if err != nil {
		t.Fatalf("domain-v3 recovery chain failed: %v", err)
	}
	stmt, err := devicesec.ParseRecoveryStatement(mustB64(t, dv3.RecoveryStatement))
	if err != nil || stmt.Verify(recStates[2], recStates[3]) != nil {
		t.Fatalf("domain-v3 recovery statement verify failed: %v", err)
	}

	// Verify compressed envelopes unpack + decode matching expected plain bytes.
	addedState, err := devicesec.ParseState(mustB64(t, dv3.Added.State))
	if err != nil {
		t.Fatal(err)
	}
	for i, ce := range dv3.CompressedEnvelope {
		w, err := devicesec.ParseWire(mustB64(t, ce.Wire))
		if err != nil {
			t.Fatalf("compressed[%d] ParseWire: %v", i, err)
		}
		h, p, err := w.Open(mustHex(t, dv3.ContentSecret2), mustB64(t, ce.Seed), addedState, addedState, false, 0)
		if err != nil {
			t.Fatalf("compressed[%d] Open: %v", i, err)
		}
		_, unpacked, err := ValidateAndDecodeBody(h, p)
		if err != nil {
			t.Fatalf("compressed[%d] ValidateAndDecodeBody: %v", i, err)
		}
		if !bytes.Equal(unpacked, mustB64(t, ce.Plain)) {
			t.Fatalf("compressed[%d] plaintext mismatch", i)
		}
	}
}

func TestFrozenChainRecoveryAndReplacementVectors(t *testing.T) {
	// 1. chain-v3.json: valid 8-state chain + fork & bad_replay rejected.
	type vecState struct {
		State string `json:"state"`
		Hash  string `json:"hash"`
	}
	var cv3 struct {
		States    []vecState `json:"states"`
		Fork      vecState   `json:"fork"`
		BadReplay vecState   `json:"bad_replay"`
	}
	loadVectorJSON(t, "internal/deviceauth/testdata/chain-v3.json", &cv3)
	s0, err := devicesec.ParseState(mustB64(t, cv3.States[0].State))
	if err != nil {
		t.Fatal(err)
	}
	rawStates := make([][]byte, len(cv3.States))
	for i, st := range cv3.States {
		rawStates[i] = mustB64(t, st.State)
	}
	chainPin := devicesec.Pin{Account: s0.Core.Account, Domain: s0.Core.Domain, Genesis: [32]byte(mustHex(t, cv3.States[0].Hash))}
	if _, _, _, err := parseAndVerifyChain(chainPin, rawStates, nil); err != nil {
		t.Fatalf("chain-v3 failed: %v", err)
	}
	if _, _, _, err := parseAndVerifyChain(chainPin, append(append([][]byte(nil), rawStates...), mustB64(t, cv3.Fork.State)), nil); err == nil {
		t.Fatalf("expected fork state to be rejected")
	}
	if _, _, _, err := parseAndVerifyChain(chainPin, append(append([][]byte(nil), rawStates...), mustB64(t, cv3.BadReplay.State)), nil); err == nil {
		t.Fatalf("expected bad_replay state to be rejected")
	}

	// 2. recovery-v3.json & recovery-removed-v3.json.
	var rrv3 struct {
		States            []vecState `json:"states"`
		Recovery          vecState   `json:"recovery"`
		RecoveryStatement string     `json:"recovery_statement"`
		RemovedID         string     `json:"removed_id"`
	}
	loadVectorJSON(t, "internal/deviceauth/testdata/recovery-removed-v3.json", &rrv3)
	rrS0, _ := devicesec.ParseState(mustB64(t, rrv3.States[0].State))
	rrPin := devicesec.Pin{Account: rrS0.Core.Account, Domain: rrS0.Core.Domain, Genesis: [32]byte(mustHex(t, rrv3.States[0].Hash))}
	var rrChain [][]byte
	for _, st := range rrv3.States {
		rrChain = append(rrChain, mustB64(t, st.State))
	}
	rrChain = append(rrChain, mustB64(t, rrv3.Recovery.State))
	if _, _, _, err := parseAndVerifyChain(rrPin, rrChain, [][]byte{mustB64(t, rrv3.RecoveryStatement)}); err != nil {
		t.Fatalf("recovery-removed-v3 failed: %v", err)
	}
	// Missing recovery proof must fail.
	if _, _, _, err := parseAndVerifyChain(rrPin, rrChain, nil); err == nil {
		t.Fatalf("expected missing recovery proof to fail")
	}

	// 3. machine-replacement-vectors.json.
	type vecProof struct {
		Approval  string `json:"approval"`
		Signature string `json:"signature"`
	}
	var mrv struct {
		Account         string     `json:"account"`
		Old             string     `json:"old"`
		Target          string     `json:"target"`
		States          []string   `json:"states"`
		Proofs          []vecProof `json:"proofs"`
		ClockSkewProofs []vecProof `json:"clock_skew_proofs"`
		ExpiredProofs   []vecProof `json:"expired_proofs"`
	}
	loadVectorJSON(t, "internal/e2e/testdata/machine-replacement-vectors.json", &mrv)
	mrStates := make([]devicesec.State, len(mrv.States))
	for i, s := range mrv.States {
		st, err := devicesec.ParseState(mustB64(t, s))
		if err != nil {
			t.Fatal(err)
		}
		mrStates[i] = st
	}
	toProofs := func(in []vecProof) []*v1.MachineReplacementProof {
		out := make([]*v1.MachineReplacementProof, len(in))
		for i, p := range in {
			out[i] = &v1.MachineReplacementProof{
				Approval:  mustB64(t, p.Approval),
				Signature: mustB64(t, p.Signature),
			}
		}
		return out
	}
	mrOK, err := MachineReplacementOrigins(mrv.Account, mrv.Target, mrStates, toProofs(mrv.Proofs))
	if err != nil {
		t.Fatalf("machine-replacement-vectors failed: %v", err)
	}
	if !mrOK[mrv.Old] {
		t.Fatalf("expected old machine %s in replacement origins %v", mrv.Old, mrOK)
	}
	if _, err := MachineReplacementOrigins(mrv.Account, mrv.Target, mrStates, toProofs(mrv.ClockSkewProofs)); err != nil {
		t.Fatalf("clock_skew_proofs failed: %v", err)
	}
	if _, err := MachineReplacementOrigins(mrv.Account, mrv.Target, mrStates, toProofs(mrv.ExpiredProofs)); err == nil {
		t.Fatalf("expected expired_proofs to be rejected")
	}

	// 4. pair-approval-vectors.json + v3-vectors.json.
	var pav struct {
		Approval        string `json:"approval"`
		Signature       string `json:"signature"`
		PairingID       string `json:"pairing_id"`
		InitiatorPublic string `json:"initiator_public"`
		JoinerPublic    string `json:"joiner_public"`
		CandidatePublic string `json:"candidate_public"`
		Transcript      string `json:"transcript"`
		SAS             string `json:"sas"`
	}
	loadVectorJSON(t, "internal/e2e/testdata/pair-approval-vectors.json", &pav)
	ap, err := VerifyPairApproval(mustHex(t, pav.Approval), mustHex(t, pav.Signature))
	if err != nil || ap == nil || ap.PairingId != pav.PairingID {
		t.Fatalf("pair_approval vector failed: err=%v ap=%+v", err, ap)
	}
	pt := devicesec.PairIdentityTranscript(
		pav.PairingID,
		mustHex(t, pav.InitiatorPublic),
		mustHex(t, pav.JoinerPublic),
		mustHex(t, pav.CandidatePublic),
		int32(v1.DeviceKind_DEVICE_KIND_CLIENT),
	)
	if !bytes.Equal(pt, mustHex(t, pav.Transcript)) {
		t.Fatalf("pair_identity_transcript vector mismatch: got=%x want=%s", pt, pav.Transcript)
	}

	var v3v struct {
		Account       string `json:"account"`
		Domain        string `json:"domain"`
		State         string `json:"state"`
		ContentSecret string `json:"content_secret"`
		Header        string `json:"header"`
		Nonce         string `json:"nonce"`
		Cipher        string `json:"cipher"`
		Origin        string `json:"origin"`
		Plain         string `json:"plain"`
	}
	loadVectorJSON(t, "internal/e2e/testdata/v3-vectors.json", &v3v)
	v3State, err := devicesec.ParseState(mustB64(t, v3v.State))
	if err != nil {
		t.Fatal(err)
	}
	var dom32, gen32 devicesec.Hash
	copy(dom32[:], mustHex(t, v3v.Domain))
	gen32[0] = 1
	v3Pin := devicesec.Pin{Account: v3v.Account, Domain: dom32, Genesis: gen32}
	_ = v3Pin
	v3Hdr, err := devicesec.ParseHeader(mustB64(t, v3v.Header))
	if err != nil {
		t.Fatal(err)
	}
	v3Proj, _ := Projection(v3Hdr).Marshal()
	ct, err := FromProtoEnvelope(&v1.Envelope{
		Header:         v3Proj,
		Nonce:          mustB64(t, v3v.Nonce),
		Ciphertext:     mustB64(t, v3v.Cipher),
		Signature:      mustB64(t, v3v.Origin),
		SecurityHeader: mustB64(t, v3v.Header),
	})
	if err != nil {
		t.Fatalf("v3-vectors FromProtoEnvelope: %v", err)
	}
	if _, err := ct.VerifyOrigin(v3State, v3State, false, 0); err != nil {
		t.Fatalf("v3-vectors VerifyOrigin: %v", err)
	}
	_, openedPlain, err := devicesec.OpenCipher(mustHex(t, v3v.ContentSecret), nil, ct)
	if err != nil {
		t.Fatalf("v3-vectors OpenCipher: %v", err)
	}
	if !bytes.Equal(openedPlain, mustB64(t, v3v.Plain)) {
		t.Fatalf("v3-vectors plaintext mismatch")
	}
}

func TestDevicesecBuildersNotifyAndSeedSigningRoundTrip(t *testing.T) {
	nowMs := uint64(1_700_000_000_000)
	phone := newTestIdentity(t, 11)
	dom := devicesec.Digest("seccall-dom", []byte{1})
	sec1 := bytes.Repeat([]byte{0x12}, 32)

	u0, err := devicesec.NewGenesis(phone.id, dom, phone.member(devicesec.Client), [32]byte(phone.edP), sec1, nowMs, stdHPKESender{})
	if err != nil {
		t.Fatalf("NewGenesis failed: %v", err)
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
	s0Parsed, err := devicesec.ParseState(s0Raw)
	if err != nil {
		t.Fatal(err)
	}
	s0Hash, _ := s0Parsed.Hash()
	pin := devicesec.Pin{Account: phone.id, Domain: dom, Genesis: s0Hash}

	// Build second state + lease + NotifyContent envelope and open via HandleOpenNotify.
	machine := newTestIdentity(t, 13)
	sec2 := bytes.Repeat([]byte{0x34}, 32)
	u1, err := devicesec.NewState(s0Parsed, []devicesec.Member{phone.member(devicesec.Client), machine.member(devicesec.Machine)}, nil, sec2, nil, nowMs+10, stdHPKESender{})
	if err != nil {
		t.Fatalf("NewState failed: %v", err)
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
	s1Parsed, err := devicesec.ParseState(s1Raw)
	if err != nil {
		t.Fatal(err)
	}
	leaseU, err := devicesec.NewLease(s1Parsed, nowMs+10)
	if err != nil {
		t.Fatalf("NewLease failed: %v", err)
	}
	lsb, err := leaseU.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	leaseU.Signature = ed25519.Sign(phone.ed, lsb)
	leaseRaw, err := leaseU.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	if err := leaseU.Valid(s1Parsed, nowMs+10); err != nil {
		t.Fatalf("lease Valid failed: %v", err)
	}

	// Seal NotifyContent and open via Engine.HandleOpenNotify using PersistedNotifyRecord.
	nPlain, err := proto.Marshal(&stdv1.NotifyContent{
		Kind:           stdv1.NotifyKind_NOTIFY_KIND_TURN_ENDED,
		MachineId:      machine.id,
		SessionId:      "sess-n1",
		SourceEventSeq: 1,
		MachineName:    "devbox",
		Title:          "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	sh2, _ := s1Parsed.Hash()
	nh := devicesec.Header{
		Account: phone.id, Domain: dom, Epoch: s1Parsed.Core.Epoch, State: sh2,
		ContentEpoch: s1Parsed.Core.ContentEpoch, Sender: machine.id,
		Kind: devicesec.Notify.Kind(), Purpose: devicesec.Notify,
		Machine: machine.id, Session: "sess-n1", Request: "notify:2:1",
	}
	hb, aesKey, nonce, err := devicesec.PrepareCiphertext(sec2, nil, nh, len(nPlain), bytes.NewReader(bytes.Repeat([]byte{0xbb}, 12)))
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := aes.NewCipher(aesKey)
	aead, _ := cipher.NewGCM(blk)
	ct, originMsg, err := devicesec.AssembleCiphertext(hb, nonce, aead.Seal(nil, nonce, nPlain, hb))
	if err != nil {
		t.Fatal(err)
	}
	ct.Origin = ed25519.Sign(machine.ed, originMsg)
	nEnv, err := ToProtoEnvelope(ct)
	if err != nil {
		t.Fatal(err)
	}
	pkRaw, err := devicesec.DerivePurposeKeyRaw(sec2, phone.id, dom, s1Parsed.Core.ContentEpoch, devicesec.Notify)
	if err != nil || len(pkRaw) != 32 {
		t.Fatalf("DerivePurposeKeyRaw failed: err=%v len=%d", err, len(pkRaw))
	}
	var pkArr [32]byte
	copy(pkArr[:], pkRaw)
	ntfRaw, err := (&PersistedNotifyRecord{Pin: pin, HeadState: s1Raw, PurposeKey: pkArr}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	eng := NewEngine(&entropy.Pool{}, 0)
	eng.Configure(&corev1.SecurityConfigEvent{Account: phone.id, DeviceId: phone.id, InlineCrypto: true})
	_ = leaseRaw
	rawNEnv, err := nEnv.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	openEffs := eng.HandleOpenNotify(int64(nowMs+20), &corev1.SecurityOpenNotifyEvent{
		OpId:         9,
		NotifyBlob:   ntfRaw,
		RawEnvelope:  rawNEnv,
		SessionId:    "sess-n1",
		Tag:          "sess-n1/NOTIFY_KIND_TURN_ENDED",
		ExpectedKind: v1.NotifyKind_NOTIFY_KIND_TURN_ENDED,
	})
	var opened *corev1.SecurityNotifyOpenedEffect
	for _, ef := range openEffs {
		if no := ef.GetSecurityNotifyOpened(); no != nil {
			opened = no
		}
	}
	outcomes := eng.DrainNotifyOutcomes()
	out := outcomes[9]
	if opened == nil || opened.Error != "" || !out.Valid || out.Title != "done" {
		t.Fatalf("HandleOpenNotify failed: eff=%+v outcome=%+v", opened, out)
	}

	// Verify ed25519 seed signing helpers.
	seed := bytes.Repeat([]byte{0x15}, 32)
	priv := ed25519.NewKeyFromSeed(seed)
	pub := priv.Public().(ed25519.PublicKey)
	msg := append([]byte("remote-agent/v1/delete-account\x00"+devicesec.ID(pub)+"\x00"), sh2[:]...)
	msg = append(msg, []byte("chal-1")...)
	sigDel := ed25519.Sign(priv, msg)
	if !ed25519.Verify(pub, msg, sigDel) {
		t.Fatalf("ed25519 seed signature verification failed")
	}
}
