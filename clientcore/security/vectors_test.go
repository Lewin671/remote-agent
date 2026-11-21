package security

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

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
	pin := &corev1.SecPin{Account: dv3.Account, Domain: dom, Genesis: genHash}

	// Verify normal chain: genesis -> added with renewed lease (and renewal + recovery chain).
	phoneID := devicesec.ID(ed25519.NewKeyFromSeed(mustHex(t, dv3.PhoneSeed)).Public().(ed25519.PublicKey))
	chainResp := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_VerifyChain{
			VerifyChain: &corev1.SecVerifyChainCall{
				Pin:      pin,
				States:   [][]byte{mustB64(t, dv3.Genesis.State), mustB64(t, dv3.Added.State)},
				Lease:    mustB64(t, dv3.RenewedLease),
				MemberId: phoneID,
			},
		},
	})
	if chainResp.Error != "" || chainResp.ParsedState == nil {
		t.Fatalf("domain-v3 verify_chain failed: %s", chainResp.Error)
	}

	// Verify GrantChallenge + OpenGrantWithDH for added state (secret 2).
	chal := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_GrantChallenge{
			GrantChallenge: &corev1.SecGrantChallengeCall{
				Pin:      pin,
				State:    mustB64(t, dv3.Added.State),
				MemberId: phoneID,
			},
		},
	})
	if chal.Error != "" {
		t.Fatalf("grant_challenge failed: %s", chal.Error)
	}
	dh := ecdhWithRawPriv(t, mustHex(t, dv3.PhoneX), chal.Data)
	openGrant := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_OpenGrantWithDh{
			OpenGrantWithDh: &corev1.SecOpenGrantWithDHCall{
				Pin:      pin,
				State:    mustB64(t, dv3.Added.State),
				MemberId: phoneID,
				Dh:       dh,
			},
		},
	})
	if openGrant.Error != "" || !bytes.Equal(openGrant.Data, mustHex(t, dv3.ContentSecret2)) {
		t.Fatalf("open_grant_with_dh mismatch: err=%s got=%x want=%s", openGrant.Error, openGrant.Data, dv3.ContentSecret2)
	}

	// Verify handoff chain & lease (descends directly from Added).
	handoffResp := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_VerifyChain{
			VerifyChain: &corev1.SecVerifyChainCall{
				Pin:    pin,
				States: [][]byte{mustB64(t, dv3.Genesis.State), mustB64(t, dv3.Added.State), mustB64(t, dv3.HandoffParent.State), mustB64(t, dv3.Handoff.State)},
				Lease:  mustB64(t, dv3.HandoffLease),
			},
		},
	})
	if handoffResp.Error != "" {
		t.Fatalf("domain-v3 handoff verify_chain failed: %s", handoffResp.Error)
	}

	// Verify recovery chain & statement (descends from Renewal).
	fullRecResp := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_VerifyChain{
			VerifyChain: &corev1.SecVerifyChainCall{
				Pin:            pin,
				States:         [][]byte{mustB64(t, dv3.Genesis.State), mustB64(t, dv3.Added.State), mustB64(t, dv3.Renewal.State), mustB64(t, dv3.Recovery.State)},
				RecoveryProofs: [][]byte{mustB64(t, dv3.RecoveryStatement)},
			},
		},
	})
	if fullRecResp.Error != "" {
		t.Fatalf("domain-v3 recovery verify_chain failed: %s", fullRecResp.Error)
	}
	recResp := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_VerifyRecoveryStatement{
			VerifyRecoveryStatement: &corev1.SecVerifyRecoveryStatementCall{
				Statement:   mustB64(t, dv3.RecoveryStatement),
				ParentState: mustB64(t, dv3.Renewal.State),
				NextState:   mustB64(t, dv3.Recovery.State),
			},
		},
	})
	if recResp.Error != "" {
		t.Fatalf("domain-v3 verify_recovery_statement failed: %s", recResp.Error)
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
	chainPin := pinToProto(devicesec.Pin{Account: s0.Core.Account, Domain: s0.Core.Domain, Genesis: [32]byte(mustHex(t, cv3.States[0].Hash))})
	if r := SecCall(&corev1.SecCallRequest{Call: &corev1.SecCallRequest_VerifyChain{VerifyChain: &corev1.SecVerifyChainCall{Pin: chainPin, States: rawStates}}}); r.Error != "" {
		t.Fatalf("chain-v3 failed: %s", r.Error)
	}
	if r := SecCall(&corev1.SecCallRequest{Call: &corev1.SecCallRequest_VerifyChain{VerifyChain: &corev1.SecVerifyChainCall{Pin: chainPin, States: append(append([][]byte(nil), rawStates...), mustB64(t, cv3.Fork.State))}}}); r.Error == "" {
		t.Fatalf("expected fork state to be rejected")
	}
	if r := SecCall(&corev1.SecCallRequest{Call: &corev1.SecCallRequest_VerifyChain{VerifyChain: &corev1.SecVerifyChainCall{Pin: chainPin, States: append(append([][]byte(nil), rawStates...), mustB64(t, cv3.BadReplay.State))}}}); r.Error == "" {
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
	rrPin := pinToProto(devicesec.Pin{Account: rrS0.Core.Account, Domain: rrS0.Core.Domain, Genesis: [32]byte(mustHex(t, rrv3.States[0].Hash))})
	var rrChain [][]byte
	for _, st := range rrv3.States {
		rrChain = append(rrChain, mustB64(t, st.State))
	}
	rrChain = append(rrChain, mustB64(t, rrv3.Recovery.State))
	if r := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_VerifyChain{
			VerifyChain: &corev1.SecVerifyChainCall{
				Pin:            rrPin,
				States:         rrChain,
				RecoveryProofs: [][]byte{mustB64(t, rrv3.RecoveryStatement)},
			},
		},
	}); r.Error != "" {
		t.Fatalf("recovery-removed-v3 failed: %s", r.Error)
	}
	// Missing recovery proof must fail.
	if r := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_VerifyChain{
			VerifyChain: &corev1.SecVerifyChainCall{
				Pin:    rrPin,
				States: rrChain,
			},
		},
	}); r.Error == "" {
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
	mrStates := make([][]byte, len(mrv.States))
	for i, s := range mrv.States {
		mrStates[i] = mustB64(t, s)
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
	mrOK := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_MachineReplacementOrigins{
			MachineReplacementOrigins: &corev1.SecMachineReplacementOriginsCall{
				Account:         mrv.Account,
				TargetMachineId: mrv.Target,
				States:          mrStates,
				Proofs:          toProofs(mrv.Proofs),
			},
		},
	})
	if mrOK.Error != "" {
		t.Fatalf("machine-replacement-vectors failed: %s", mrOK.Error)
	}
	var foundOld bool
	for _, s := range mrOK.Strings {
		if s == mrv.Old {
			foundOld = true
		}
	}
	if !foundOld {
		t.Fatalf("expected old machine %s in replacement origins %v", mrv.Old, mrOK.Strings)
	}
	mrSkew := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_MachineReplacementOrigins{
			MachineReplacementOrigins: &corev1.SecMachineReplacementOriginsCall{
				Account:         mrv.Account,
				TargetMachineId: mrv.Target,
				States:          mrStates,
				Proofs:          toProofs(mrv.ClockSkewProofs),
			},
		},
	})
	if mrSkew.Error != "" {
		t.Fatalf("clock_skew_proofs failed: %s", mrSkew.Error)
	}
	mrExp := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_MachineReplacementOrigins{
			MachineReplacementOrigins: &corev1.SecMachineReplacementOriginsCall{
				Account:         mrv.Account,
				TargetMachineId: mrv.Target,
				States:          mrStates,
				Proofs:          toProofs(mrv.ExpiredProofs),
			},
		},
	})
	if mrExp.Error == "" {
		t.Fatalf("expected expired_proofs to be rejected")
	}

	// 4. pair-approval-vectors.json + v3-vectors.json + SecCall builder round-trips.
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
	paResp := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_VerifyPairApproval{
			VerifyPairApproval: &corev1.SecVerifyPairApprovalCall{
				Approval:  mustHex(t, pav.Approval),
				Signature: mustHex(t, pav.Signature),
			},
		},
	})
	if paResp.Error != "" || paResp.ParsedApproval == nil || paResp.ParsedApproval.PairingId != pav.PairingID {
		t.Fatalf("pair_approval vector failed: err=%s", paResp.Error)
	}
	ptResp := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_PairIdentityTranscript{
			PairIdentityTranscript: &corev1.SecPairIdentityTranscriptCall{
				PairingId:       pav.PairingID,
				InitiatorPublic: mustHex(t, pav.InitiatorPublic),
				JoinerPublic:    mustHex(t, pav.JoinerPublic),
				DeviceEdPublic:  mustHex(t, pav.CandidatePublic),
				JoinerKind:      int32(v1.DeviceKind_DEVICE_KIND_CLIENT),
			},
		},
	})
	if !bytes.Equal(ptResp.Data, mustHex(t, pav.Transcript)) {
		t.Fatalf("pair_identity_transcript vector mismatch: got=%x want=%s", ptResp.Data, pav.Transcript)
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

func TestSecCallBuildersNotifyAndHelpersRoundTrip(t *testing.T) {
	nowMs := uint64(1_700_000_000_000)
	phone := newTestIdentity(t, 11)
	rec := newTestIdentity(t, 12)
	dom := devicesec.Digest("seccall-dom", []byte{1})
	sec1 := bytes.Repeat([]byte{0x12}, 32)

	eph, _ := ecdh.X25519().GenerateKey(rand.Reader)
	dh, _ := eph.ECDH(phone.x.PublicKey())

	genResp := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_BuildState{
			BuildState: &corev1.SecBuildStateCall{
				Kind:       1,
				Account:    phone.id,
				Domain:     dom[:],
				RecoveryEd: phone.edP,
				Secret:     sec1,
				NowMs:      nowMs,
				Member: &corev1.SecMember{
					Id:       phone.id,
					EdPublic: phone.edP,
					XPublic:  phone.xPub[:],
					Kind:     uint32(devicesec.Client),
				},
				Kems: []*corev1.SecKemPair{{
					MemberId: phone.id,
					Enc:      eph.PublicKey().Bytes(),
					Dh:       dh,
				}},
			},
		},
	})
	if genResp.Error != "" || genResp.UnsignedState == nil {
		t.Fatalf("BuildState(Genesis) failed: %s", genResp.Error)
	}
	sig0 := ed25519.Sign(phone.ed, genResp.UnsignedState.SigningBytes)
	mResp := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_MarshalState{
			MarshalState: &corev1.SecMarshalStateCall{
				UnsignedCore: genResp.UnsignedState.UnsignedCore,
				Grants:       genResp.UnsignedState.Grants,
				PrevSecret:   genResp.UnsignedState.PrevSecret,
				Signature:    sig0,
			},
		},
	})
	if mResp.Error != "" || mResp.ParsedState == nil {
		t.Fatalf("MarshalState failed: %s", mResp.Error)
	}
	pin := &corev1.SecPin{Account: phone.id, Domain: dom[:], Genesis: mResp.ParsedState.StateHash}

	// Build & open a NotifyContent envelope via SecCall.
	machine := newTestIdentity(t, 13)
	_ = rec
	ephM, _ := ecdh.X25519().GenerateKey(rand.Reader)
	dhM, _ := ephM.ECDH(machine.x.PublicKey())
	sec2 := bytes.Repeat([]byte{0x34}, 32)
	addResp := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_BuildState{
			BuildState: &corev1.SecBuildStateCall{
				Kind:            2,
				ParentState:     mResp.Data,
				OldSecret:       sec1,
				Secret:          sec2,
				NowMs:           nowMs + 10,
				PrevSecretNonce: bytes.Repeat([]byte{0xaa}, 12),
				Members: []*corev1.SecMember{
					{Id: phone.id, EdPublic: phone.edP, XPublic: phone.xPub[:], Kind: uint32(devicesec.Client)},
					{Id: machine.id, EdPublic: machine.edP, XPublic: machine.xPub[:], Kind: uint32(devicesec.Machine)},
				},
				Kems: []*corev1.SecKemPair{
					{MemberId: phone.id, Enc: eph.PublicKey().Bytes(), Dh: dh},
					{MemberId: machine.id, Enc: ephM.PublicKey().Bytes(), Dh: dhM},
				},
			},
		},
	})
	if addResp.Error != "" {
		t.Fatalf("BuildState(Membership) failed: %s", addResp.Error)
	}
	m2Resp := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_MarshalState{
			MarshalState: &corev1.SecMarshalStateCall{
				UnsignedCore: addResp.UnsignedState.UnsignedCore,
				Grants:       addResp.UnsignedState.Grants,
				PrevSecret:   addResp.UnsignedState.PrevSecret,
				Signature:    ed25519.Sign(phone.ed, addResp.UnsignedState.SigningBytes),
			},
		},
	})
	if m2Resp.Error != "" {
		t.Fatalf("MarshalState(2) failed: %s", m2Resp.Error)
	}

	// Lease unsigned + marshal_lease + verify_lease.
	lu := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_NewLeaseUnsigned{
			NewLeaseUnsigned: &corev1.SecNewLeaseUnsignedCall{HeadState: m2Resp.Data, NowMs: nowMs + 10},
		},
	})
	if lu.Error != "" {
		t.Fatalf("NewLeaseUnsigned failed: %s", lu.Error)
	}
	lm := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_MarshalLease{
			MarshalLease: &corev1.SecMarshalLeaseCall{
				Domain:    lu.ParsedLease.Domain,
				Head:      lu.ParsedLease.Head,
				IssuedAt:  lu.ParsedLease.IssuedAt,
				NotAfter:  lu.ParsedLease.NotAfter,
				Signature: ed25519.Sign(phone.ed, lu.Data),
			},
		},
	})
	if lm.Error != "" {
		t.Fatalf("MarshalLease failed: %s", lm.Error)
	}

	// Seal NotifyContent and open via OpenNotifyEnvelope.
	nPlain, err := proto.Marshal(&stdv1.NotifyContent{
		Kind:        stdv1.NotifyKind_NOTIFY_KIND_TURN_ENDED,
		MachineId:   machine.id,
		SessionId:   "sess-n1",
		MachineName: "devbox",
		Title:       "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	var sh32 devicesec.Hash
	copy(sh32[:], m2Resp.ParsedState.StateHash)
	nh := devicesec.Header{
		Account: phone.id, Domain: dom, Epoch: m2Resp.ParsedState.Epoch, State: sh32,
		ContentEpoch: m2Resp.ParsedState.ContentEpoch, Sender: machine.id,
		Kind: devicesec.Notify.Kind(), Purpose: devicesec.Notify,
		Machine: machine.id, Session: "sess-n1", Request: "notify-req-1",
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
	openN := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_OpenNotifyEnvelope{
			OpenNotifyEnvelope: &corev1.SecOpenNotifyEnvelopeCall{
				Pin:      pin,
				States:   [][]byte{mResp.Data, m2Resp.Data},
				MemberId: phone.id,
				Dh:       dh,
				Envelope: nEnv,
			},
		},
	})
	var gotNotify v1.NotifyContent
	if openN.Error != "" || len(openN.Data) == 0 || !bytes.Equal(openN.SecondaryData, sec2) || gotNotify.Unmarshal(openN.Data) != nil || gotNotify.Title != "done" {
		t.Fatalf("OpenNotifyEnvelope failed: err=%s dataLen=%d title=%q", openN.Error, len(openN.Data), gotNotify.Title)
	}

	// Verify ed25519 seed & account_delete_signing_bytes helpers.
	seed := bytes.Repeat([]byte{0x15}, 32)
	pubR := SecCall(&corev1.SecCallRequest{Call: &corev1.SecCallRequest_Ed25519SeedPublic{Ed25519SeedPublic: &corev1.SecEd25519SeedPublicCall{Seed: seed}}})
	delR := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_AccountDeleteSigningBytes{
			AccountDeleteSigningBytes: &corev1.SecAccountDeleteSigningBytesCall{
				Account:   devicesec.ID(pubR.Data),
				HeadHash:  m2Resp.ParsedState.StateHash,
				Challenge: []byte("chal-1"),
			},
		},
	})
	sigDel := SecCall(&corev1.SecCallRequest{
		Call: &corev1.SecCallRequest_Ed25519SeedSign{
			Ed25519SeedSign: &corev1.SecEd25519SeedSignCall{Seed: seed, Message: delR.Data},
		},
	})
	if !ed25519.Verify(pubR.Data, delR.Data, sigDel.Data) {
		t.Fatalf("Ed25519SeedSign verification failed")
	}
}
