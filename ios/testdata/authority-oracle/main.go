// Test-only oracle. It uses Go's production builders for transition fields and
// production verifiers for Swift output. RFC 9180 sealing below fixes the test
// ephemeral key without changing crypto/rand or any production implementation.
package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"slices"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	d "github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"google.golang.org/protobuf/proto"
)

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
func fill(n int, b byte) []byte     { return bytes.Repeat([]byte{b}, n) }
func key(b byte) ed25519.PrivateKey { return ed25519.NewKeyFromSeed(fill(32, b)) }
func member(b byte, kind d.Kind) d.Member {
	return must(d.NewMember(key(b).Public().(ed25519.PublicKey), must(d.XPublic(fill(32, b+10))), kind))
}
func hash(b []byte) d.Hash        { return sha256.Sum256(b) }
func u32(n int) []byte            { return binary.BigEndian.AppendUint32(nil, uint32(n)) }
func u64(n uint64) []byte         { return binary.BigEndian.AppendUint64(nil, n) }
func join(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
func lp(b []byte) []byte          { return join(u32(len(b)), b) }
func str(s string) []byte         { return lp([]byte(s)) }
func extract(suite []byte, label string, salt, ikm []byte) []byte {
	return must(hkdf.Extract(sha256.New, join([]byte("HPKE-v1"), suite, []byte(label), ikm), salt))
}
func expand(suite []byte, label string, prk, info []byte, n uint16) []byte {
	data := join(binary.BigEndian.AppendUint16(nil, n), []byte("HPKE-v1"), suite, []byte(label), info)
	return must(hkdf.Expand(sha256.New, prk, string(data), int(n)))
}
func aead(key, nonce, aad, plain []byte) []byte {
	c := must(aes.NewCipher(key))
	g := must(cipher.NewGCM(c))
	return g.Seal(nil, nonce, plain, aad)
}
func grant(m d.Member, c d.Core, ch d.Hash, secret []byte) d.Grant {
	ephemeral := must(ecdh.X25519().NewPrivateKey(fill(32, 99)))
	enc := ephemeral.PublicKey().Bytes()
	dh := must(ephemeral.ECDH(must(ecdh.X25519().NewPublicKey(m.XPublic[:]))))
	kem := []byte{'K', 'E', 'M', 0, 32}
	shared := expand(kem, "shared_secret", extract(kem, "eae_prk", nil, dh), join(enc, m.XPublic[:]), 32)
	suite := []byte{'H', 'P', 'K', 'E', 0, 32, 0, 1, 0, 2}
	ctx := join(str(c.Account), c.Domain[:], u64(c.ContentEpoch), ch[:], str(m.ID))
	info := join([]byte("ra3/grant-info\x00"), ctx)
	schedule := join([]byte{0}, extract(suite, "psk_id_hash", nil, nil), extract(suite, "info_hash", nil, info))
	sec := extract(suite, "secret", shared, nil)
	k := expand(suite, "key", sec, schedule, 32)
	nonce := expand(suite, "base_nonce", sec, schedule, 12)
	g := d.Grant{Recipient: m.ID, Cipher: aead(k, nonce, join([]byte("ra3/grant-aad\x00"), ctx), secret)}
	copy(g.Enc[:], enc)
	return g
}
func prev(old, new []byte) []byte {
	nonce := fill(12, 88)
	return join(nonce, aead(new, nonce, []byte("ra3/prev-secret\x00"), old))
}
func stateSign(s d.State) []byte {
	c := s.Core
	h := must(s.Hash())
	return join([]byte("ra3/state-sig\x00"), str(c.Account), c.Domain[:], c.Parent[:], u64(c.Epoch), u64(c.ContentEpoch), u64(c.IssuedAt), str(c.AuthorityID), c.RecoveryEd[:], h[:])
}
func fixed(s d.State, secret []byte, signer ed25519.PrivateKey, p []byte) d.State {
	c := s.Core
	ch := hash(join([]byte("ra3/core\x00"), must(c.MarshalBinary())))
	s.Grants = nil
	s.PrevSecret = p
	for _, m := range c.Members {
		s.Grants = append(s.Grants, grant(m, c, ch, secret))
	}
	s.Signature = ed25519.Sign(signer, stateSign(s))
	return s
}

type Entry struct {
	Name  string
	State []byte
	Proof []byte
	Lease []byte
}
type Vector struct {
	Entries           []Entry
	Approval          []byte
	ApprovalSignature []byte
	RecoveryCode      string
}

const now uint64 = 1700000000000

func generate() Vector {
	admin, incoming, machine, recovered := member(1, d.Client), member(2, d.Client), member(3, d.Machine), member(4, d.Client)
	recovery := key(21)
	pub := [32]byte(recovery.Public().(ed25519.PublicKey))
	nextPub := [32]byte(key(22).Public().(ed25519.PublicKey))
	finalPub := [32]byte(key(23).Public().(ed25519.PublicKey))
	domain := d.Hash(fill(32, 50))
	account := e2e.DeviceID(key(30).Public().(ed25519.PublicKey))
	var v Vector
	add := func(name string, s d.State, proof []byte, signer ed25519.PrivateKey) {
		l := must(d.BuildLease(s, signer, max(now+100, s.Core.IssuedAt)))
		v.Entries = append(v.Entries, Entry{name, must(s.MarshalBinary()), proof, must(l.MarshalBinary())})
	}
	g := fixed(must(d.BuildGenesis(account, domain, admin, pub, fill(32, 40), key(1), now)), fill(32, 40), key(1), nil)
	add("genesis", g, nil, key(1))
	p := fixed(must(d.BuildPairState(g, incoming, incoming.XPublic[:], fill(32, 40), fill(32, 41), key(1), now-10)), fill(32, 41), key(1), prev(fill(32, 40), fill(32, 41)))
	add("pair", p, nil, key(1))
	m := fixed(must(d.BuildMembershipState(p, append(slices.Clone(p.Core.Members), machine), p.Core.Removed, fill(32, 41), fill(32, 42), key(1), now+1)), fill(32, 42), key(1), prev(fill(32, 41), fill(32, 42)))
	add("membership", m, nil, key(1))
	members := slices.Clone(m.Core.Members)
	for i := range members {
		if members[i].ID == machine.ID {
			members[i].XPublic = [32]byte(must(d.XPublic(fill(32, 70))))
		}
	}
	r := fixed(must(d.BuildMembershipState(m, members, m.Core.Removed, fill(32, 42), fill(32, 43), key(1), now+2)), fill(32, 43), key(1), prev(fill(32, 42), fill(32, 43)))
	add("rotateX", r, nil, key(1))
	members = slices.DeleteFunc(slices.Clone(r.Core.Members), func(m d.Member) bool { return m.ID == machine.ID })
	rm := fixed(must(d.BuildMembershipState(r, members, []string{machine.ID}, fill(32, 43), fill(32, 44), key(1), now+3)), fill(32, 44), key(1), prev(fill(32, 43), fill(32, 44)))
	add("remove", rm, nil, key(1))
	veto := fixed(must(d.BuildRecoveryKeyRotation(rm, fill(32, 44), nextPub, key(1), now+4)), fill(32, 44), key(1), nil)
	add("veto", veto, nil, key(1))
	h := fixed(must(d.BuildHandoff(veto, incoming.ID, finalPub, fill(32, 44), fill(32, 45), key(1), now+5)), fill(32, 45), key(1), prev(fill(32, 44), fill(32, 45)))
	add("handoff", h, nil, key(2))
	s, proof, err := d.BuildRecovery(h, recovered, []d.Member{recovered}, fill(32, 46), key(4), key(23), pub, now+6)
	if err != nil {
		panic(err)
	}
	s = fixed(s, fill(32, 46), key(4), nil)
	proof.StateHash = must(s.Hash())
	proof.Nonce = d.Hash(fill(32, 88))
	if err := proof.Sign(key(23)); err != nil {
		panic(err)
	}
	add("recovery", s, must(proof.MarshalBinary()), key(4))
	a := &pb.PairApproval{AccountId: account, PairingId: "0123456789abcdef0123456789abcdef", ApproverDeviceId: admin.ID, ApproverPublicKey: admin.EdPublic[:], CandidateDevicePublicKey: machine.EdPublic[:], CandidateKind: pb.DeviceKind_DEVICE_KIND_MACHINE, ExpiresMs: int64(now + 300000), Nonce: fill(16, 88), ReplacesDeviceId: recovered.ID}
	v.Approval = must(proto.MarshalOptions{Deterministic: true}.Marshal(a))
	v.ApprovalSignature = ed25519.Sign(key(1), join([]byte(e2e.PairApprovalContext), v.Approval))
	// Crockford Base32 (same payload as Android, no checksum).
	gh := must(g.Hash())
	payload := join(gh[:10], fill(32, 21))
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	var bits uint
	var value uint32
	var code []byte
	for _, b := range payload {
		value = value<<8 | uint32(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			code = append(code, alphabet[value>>bits&31])
		}
	}
	if bits > 0 {
		code = append(code, alphabet[value<<(5-bits)&31])
	}
	var groups []string
	for len(code) > 0 {
		n := min(4, len(code))
		groups = append(groups, string(code[:n]))
		code = code[n:]
	}
	v.RecoveryCode = string(bytes.Join(func() [][]byte {
		var b [][]byte
		for _, g := range groups {
			b = append(b, []byte(g))
		}
		return b
	}(), []byte("-")))
	return v
}
func verify(v Vector) {
	expected := generate()
	if len(v.Entries) != len(expected.Entries) {
		panic("entry count")
	}
	var parent d.State
	var pin d.Pin
	for i, e := range v.Entries {
		want := expected.Entries[i]
		s := must(d.ParseState(e.State))
		reference := must(d.ParseState(want.State))
		// CryptoKit may randomize Ed25519 signatures. Compare all serialized fields
		// with only the signature replaced by Go's valid signature.
		actual := s
		saved := actual.Signature
		actual.Signature = reference.Signature
		if e.Name != want.Name || !bytes.Equal(must(actual.MarshalBinary()), want.State) {
			panic("state byte mismatch: " + e.Name)
		}
		actual.Signature = saved
		if i == 0 {
			pin = must(s.Pin())
			if err := pin.VerifyGenesis(s); err != nil {
				panic(err)
			}
		} else {
			var proof *d.RecoveryStatement
			if len(e.Proof) > 0 {
				r := must(d.ParseRecoveryStatement(e.Proof))
				ref := must(d.ParseRecoveryStatement(want.Proof))
				r.Signature = ref.Signature
				if !bytes.Equal(must(r.MarshalBinary()), want.Proof) {
					panic("recovery byte mismatch")
				}
				r = must(d.ParseRecoveryStatement(e.Proof))
				proof = &r
			}
			if err := d.VerifyTransition(parent, s, proof); err != nil {
				panic(fmt.Errorf("%s: %w", e.Name, err))
			}
		}
		l := must(d.ParseLease(e.Lease))
		if err := l.Verify(s); err != nil {
			panic(err)
		}
		refLease := must(d.ParseLease(want.Lease))
		l.Signature = refLease.Signature
		if !bytes.Equal(must(l.MarshalBinary()), want.Lease) {
			panic("lease byte mismatch")
		}
		// The production Go HPKE recipient opens every Swift grant.
		for _, m := range s.Core.Members {
			var x []byte
			for b := byte(1); b <= 4; b++ {
				if member(b, m.Kind).ID == m.ID {
					x = fill(32, b+10)
				}
			}
			if e.Name == "rotateX" && m.ID == member(3, d.Machine).ID {
				x = fill(32, 70)
			}
			if len(x) == 0 {
				panic("unknown member")
			}
			if _, err := pin.OpenGrant(s, m.ID, x); err != nil {
				panic(err)
			}
		}
		parent = s
	}
	if !bytes.Equal(v.Approval, expected.Approval) {
		panic("approval byte mismatch")
	}
	if _, err := e2e.VerifyPairApproval(v.Approval, v.ApprovalSignature); err != nil {
		panic(err)
	}
	if v.RecoveryCode != expected.RecoveryCode {
		panic("recovery code mismatch")
	}
}
func main() {
	if len(os.Args) > 1 && os.Args[1] == "verify" {
		var v Vector
		if err := json.NewDecoder(os.Stdin).Decode(&v); err != nil {
			panic(err)
		}
		verify(v)
		fmt.Println("Swift authority bytes and signatures verified by Go")
		return
	}
	v := generate()
	verify(v)
	out := json.NewEncoder(os.Stdout)
	out.SetIndent("", "  ")
	if err := out.Encode(v); err != nil {
		panic(err)
	}
}
