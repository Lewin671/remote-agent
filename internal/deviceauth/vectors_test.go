package deviceauth

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

type sharedVector struct {
	Pin         Pin
	State       []byte
	NextState   []byte
	WriterSeed  string
	WriterX     string
	RecipientID string
	Secret      string
	SessionSeed string
	Key         string
	Header      []byte
	Wire        []byte
	Plain       []byte
	Request     Capsule
	Reply       Capsule
	Waiter      Waiter
}

func TestSharedVectors(t *testing.T) {
	const file = "testdata/domain-v2.json"
	if os.Getenv("UPDATE_SECURITY_VECTORS") == "1" {
		writeVector(t, file)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var v sharedVector
	if json.Unmarshal(raw, &v) != nil {
		t.Fatal("invalid vector")
	}
	s, err := ParseState(v.State)
	if err != nil || v.Pin.Verify(s) != nil {
		t.Fatal(err)
	}
	next, err := ParseState(v.NextState)
	if err != nil || v.Pin.Advance(s, next) != nil {
		t.Fatal(err)
	}
	w, err := ParseWire(v.Wire)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := hex.DecodeString(v.SessionSeed)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := hex.DecodeString(v.Secret)
	_, plain, err := w.Open(v.Pin, next, secret, seed)
	if err != nil || !bytes.Equal(plain, v.Plain) {
		t.Fatal(err)
	}
	h, err := ParseHeader(v.Header)
	if err != nil {
		t.Fatal(err)
	}
	key, err := DeriveKey(secret, seed, h)
	if err != nil || hex.EncodeToString(key) != v.Key {
		t.Fatal("KDF vector differs", err)
	}
	x, _ := hex.DecodeString(v.WriterX)
	opened, err := v.Pin.OpenGrant(next, v.RecipientID, x)
	if err != nil || !bytes.Equal(opened, secret) {
		t.Fatal(err)
	}
	var result AuthorizeReply
	if err := v.Waiter.Reply(v.Reply, x, &result); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result.Signature, w.AuthSignature) {
		t.Fatal("RPC reply auth differs")
	}
	env, err := w.Envelope()
	if err != nil {
		t.Fatal(err)
	}
	round, err := FromEnvelope(env)
	if err != nil || !bytes.Equal(round.Header, w.Header) {
		t.Fatal(err)
	}
	env.Header = append(env.Header, 0)
	if _, err := FromEnvelope(env); err == nil {
		t.Fatal("mutated routing projection accepted")
	}
}
func writeVector(t *testing.T, file string) {
	t.Helper()
	edseed := bytes.Repeat([]byte{0x21}, 32)
	ed := ed25519.NewKeyFromSeed(edseed)
	x := bytes.Repeat([]byte{0x22}, 32)
	xkey, err := ecdh.X25519().NewPrivateKey(x)
	if err != nil {
		t.Fatal(err)
	}
	m, err := NewMember(ed.Public().(ed25519.PublicKey), xkey.PublicKey().Bytes(), Machine)
	if err != nil {
		t.Fatal(err)
	}
	ae := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x41}, 32))
	ax, err := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{0x42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	var domain Hash
	copy(domain[:], bytes.Repeat([]byte{0x11}, 32))
	core := Core{Account: "0123456789abcdef0123456789abcdef", Domain: domain, AuthorityID: m.ID, Epoch: 1, ContentEpoch: 1, Members: []Member{m}}
	copy(core.AuthorityEd[:], ae.Public().(ed25519.PublicKey))
	copy(core.AuthorityX[:], ax.PublicKey().Bytes())
	s, err := BuildState(core, bytes.Repeat([]byte{0x51}, 32), ae)
	if err != nil {
		t.Fatal(err)
	}
	pin, err := s.Pin()
	if err != nil {
		t.Fatal(err)
	}
	clientSeed := bytes.Repeat([]byte{0x31}, 32)
	cx, err := ecdh.X25519().NewPrivateKey(bytes.Repeat([]byte{0x32}, 32))
	if err != nil {
		t.Fatal(err)
	}
	clientEd := ed25519.NewKeyFromSeed(clientSeed)
	client, err := NewMember(clientEd.Public().(ed25519.PublicKey), cx.PublicKey().Bytes(), Client)
	if err != nil {
		t.Fatal(err)
	}
	core.Parent, _ = s.Hash()
	core.Epoch = 2
	core.ContentEpoch = 2
	core.Members = append(core.Members, client)
	if core.Members[0].ID > core.Members[1].ID {
		core.Members[0], core.Members[1] = core.Members[1], core.Members[0]
	}
	secret := bytes.Repeat([]byte{0x52}, 32)
	next, err := BuildState(core, secret, ae)
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := next.Hash()
	h := Header{Account: core.Account, Domain: domain, Epoch: 2, State: sh, ContentEpoch: 2, Sender: m.ID, Kind: 1, Purpose: Event, Machine: m.ID, Session: "session-vector", Seq: 7, Counter: 17}
	seed := bytes.Repeat([]byte{0x61}, 32)
	plain := encoded(t, &pb.Event{SessionId: h.Session, Seq: h.Seq, Kind: &pb.Event_AgentMessage{AgentMessage: &pb.AgentMessage{Text: "synthetic vector"}}})
	cipher, err := Seal(secret, seed, h, plain, ed)
	if err != nil {
		t.Fatal(err)
	}
	nonce := Hash{1}
	auth := WriteAuth{Header: h, WriterKind: Machine, HeaderHash: digest("", cipher.Header), Commit: mustCommit(t, cipher), Nonce: nonce}
	ab, err := auth.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	wire := Wire{Ciphertext: cipher, Authorization: ab, AuthSignature: ed25519.Sign(ae, writeSignBytes(ab))}
	key, err := DeriveKey(secret, seed, h)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := AuthorizationRequest(cipher)
	capsule, waiter, err := Request(pin, ed, Authorize, req)
	if err != nil {
		t.Fatal(err)
	}
	response := rpcResponse{Nonce: waiter.Nonce, RequestHash: waiter.RequestHash}
	response.Body, _ = json.Marshal(AuthorizeReply{wire.Authorization, wire.AuthSignature})
	replyCapsule := Capsule{From: pin.AuthorityID, To: m.ID, Method: Authorize}
	rb, err := response.signBytes(pin, replyCapsule)
	if err != nil {
		t.Fatal(err)
	}
	response.Signature = ed25519.Sign(ae, rb)
	rj, _ := json.Marshal(response)
	replyCapsule, err = sealCapsule(pin, pin.AuthorityID, m.ID, Authorize, true, m.XPublic[:], rj)
	if err != nil {
		t.Fatal(err)
	}
	sb, _ := s.MarshalBinary()
	nb, _ := next.MarshalBinary()
	wb, _ := wire.MarshalBinary()
	v := sharedVector{pin, sb, nb, hex.EncodeToString(edseed), hex.EncodeToString(x), m.ID, hex.EncodeToString(secret), hex.EncodeToString(seed), hex.EncodeToString(key), cipher.Header, wb, plain, capsule, replyCapsule, waiter}
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(file, append(raw, '\n'), 0644); err != nil {
		t.Fatal(err)
	}
}
func mustCommit(t *testing.T, c Ciphertext) Hash {
	t.Helper()
	h, err := c.Commitment()
	if err != nil {
		t.Fatal(err)
	}
	return h
}

type completionVector struct {
	Pin        Pin
	Original   DeleteRequest
	Completion Completion
	Chain      [][]byte
}

func TestSharedCompletionVectors(t *testing.T) {
	const file = "testdata/revocation-v2.json"
	if os.Getenv("UPDATE_SECURITY_VECTORS") == "1" {
		f := setup(t, Machine, Client)
		request := deleteReq(t, f, f.members[2], f.members[2])
		g, err := f.a.Delete(request)
		if err != nil {
			t.Fatal(err)
		}
		second := deleteReq(t, f, f.members[0], f.members[1])
		g2, err := f.a.Delete(second)
		if err != nil {
			t.Fatal(err)
		}
		fences, err := f.a.FenceRequests()
		if err != nil || len(fences) != 1 {
			t.Fatal(err)
		}
		receipt, err := f.members[0].e.Fence(fences[0], []State{g.Head, g2.Head})
		if err != nil {
			t.Fatal(err)
		}
		done, err := f.a.Ack(receipt)
		if err != nil || done == nil {
			t.Fatal(err)
		}
		b1, _ := g.Head.MarshalBinary()
		b2, _ := g2.Head.MarshalBinary()
		v := completionVector{f.pin, request, *done, [][]byte{b1, b2}}
		raw, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(file, append(raw, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var v completionVector
	if err = json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	chain := []State{}
	for _, raw := range v.Chain {
		s, err := ParseState(raw)
		if err != nil {
			t.Fatal(err)
		}
		chain = append(chain, s)
	}
	if err := v.Completion.Verify(v.Pin, v.Original, chain); err != nil {
		t.Fatal(err)
	}
	bad := clone(v.Completion)
	bad.Group.Receipts[0].Revision--
	if err := bad.Verify(v.Pin, v.Original, chain); err == nil {
		t.Fatal("old group ACK accepted")
	}
}
