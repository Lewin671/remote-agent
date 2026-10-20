// Test-only oracle. It builds cores with production Go builders, substitutes
// fixed RFC 9180 HPKE inputs / AES-GCM nonces, then signs and verifies with Go.
// No production randomness or shared vector is changed.
package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/hpke"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"slices"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	da "github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"google.golang.org/protobuf/proto"
)

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
func check(err error) {
	if err != nil {
		panic(err)
	}
}
func fill(n, b int) []byte         { return bytes.Repeat([]byte{byte(b)}, n) }
func key(b int) ed25519.PrivateKey { return ed25519.NewKeyFromSeed(fill(32, b)) }
func public(b int) [32]byte        { return [32]byte(key(b).Public().(ed25519.PublicKey)) }
func member(b, x int, kind da.Kind) da.Member {
	return must(da.NewMember(key(b).Public().(ed25519.PublicKey), must(da.XPublic(fill(32, x))), kind))
}
func u16(n int) []byte            { return binary.BigEndian.AppendUint16(nil, uint16(n)) }
func u64(n uint64) []byte         { return binary.BigEndian.AppendUint64(nil, n) }
func lp(b []byte) []byte          { return append(binary.BigEndian.AppendUint32(nil, uint32(len(b))), b...) }
func join(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
func hash(parts ...[]byte) []byte { sum := sha256.Sum256(join(parts...)); return sum[:] }
func extract(suite, salt []byte, label string, ikm []byte) []byte {
	return must(hkdf.Extract(sha256.New, join([]byte("HPKE-v1"), suite, []byte(label), ikm), salt))
}
func expand(suite, prk []byte, label string, info []byte, n int) []byte {
	return must(hkdf.Expand(sha256.New, prk, string(join(u16(n), []byte("HPKE-v1"), suite, []byte(label), info)), n))
}
func gcm(secret []byte) cipher.AEAD { return must(cipher.NewGCM(must(aes.NewCipher(secret)))) }
func prev(newSecret, oldSecret []byte) []byte {
	nonce := fill(12, 0x72)
	return append(nonce, gcm(newSecret).Seal(nil, nonce, oldSecret, []byte("ra3/prev-secret\x00"))...)
}

// RFC 9180 sections 4, 5.1, 5.2. Only used to supply deterministic sender
// inputs, which crypto/hpke.NewSender deliberately does not expose. Every
// resulting grant is independently opened by production da.Pin.OpenGrant.
func grant(c da.Core, ch []byte, m da.Member, secret []byte) da.Grant {
	ek := must(hpke.DHKEM(ecdh.X25519()).DeriveKeyPair(fill(32, 0x71)))
	enc := ek.PublicKey().Bytes()
	priv := must(ecdh.X25519().NewPrivateKey(must(ek.Bytes())))
	dh := must(priv.ECDH(must(ecdh.X25519().NewPublicKey(m.XPublic[:]))))
	kemSuite := join([]byte("KEM"), u16(0x20))
	shared := expand(kemSuite, extract(kemSuite, nil, "eae_prk", dh), "shared_secret", join(enc, m.XPublic[:]), 32)
	suite := join([]byte("HPKE"), u16(0x20), u16(1), u16(2))
	ctx := join(lp([]byte(c.Account)), c.Domain[:], u64(c.ContentEpoch), ch, lp([]byte(m.ID)))
	info := join([]byte("ra3/grant-info\x00"), ctx)
	ks := join([]byte{0}, extract(suite, nil, "psk_id_hash", nil), extract(suite, nil, "info_hash", info))
	root := extract(suite, shared, "secret", nil)
	k := expand(suite, root, "key", ks, 32)
	nonce := expand(suite, root, "base_nonce", ks, 12)
	return da.Grant{Recipient: m.ID, Enc: [32]byte(enc), Cipher: gcm(k).Seal(nil, nonce, secret, join([]byte("ra3/grant-aad\x00"), ctx))}
}
func canonical(s da.State, secret, previous []byte, signer ed25519.PrivateKey) da.State {
	s.PrevSecret = previous
	ch := hash([]byte("ra3/core\x00"), must(s.Core.MarshalBinary()))
	s.Grants = nil
	for _, m := range s.Core.Members {
		s.Grants = append(s.Grants, grant(s.Core, ch, m, secret))
	}
	h := must(s.Hash())
	c := s.Core
	signed := join([]byte("ra3/state-sig\x00"), lp([]byte(c.Account)), c.Domain[:], c.Parent[:], u64(c.Epoch), u64(c.ContentEpoch), u64(c.IssuedAt), lp([]byte(c.AuthorityID)), c.RecoveryEd[:], h[:])
	s.Signature = ed25519.Sign(signer, signed)
	return s
}

type vector struct {
	States                                                     map[string][]byte
	Recovery, Lease, HandoffLease, Approval, ApprovalSignature []byte
	NormalApproval, NormalApprovalSignature                    []byte
	RecoveryCode                                               string
}

var order = []string{"genesis", "pairMachine", "pairClient", "remove", "rotateX", "veto", "handoff", "recovery"}

func code(seed, genesis []byte) string {
	const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	payload := join(genesis[:10], seed)
	out := ""
	bits := 0
	value := uint32(0)
	for _, b := range payload {
		value = value<<8 | uint32(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			out += string(alphabet[value>>bits&31])
		}
	}
	if bits > 0 {
		out += string(alphabet[value<<(5-bits)&31])
	}
	groups := []string{}
	for len(out) > 4 {
		groups = append(groups, out[:4])
		out = out[4:]
	}
	groups = append(groups, out)
	return string(bytes.Join(func() [][]byte {
		a := [][]byte{}
		for _, g := range groups {
			a = append(a, []byte(g))
		}
		return a
	}(), []byte("-")))
}
func generate() vector {
	authority := member(0x11, 0x21, da.Client)
	machine := member(0x12, 0x22, da.Machine)
	client := member(0x13, 0x23, da.Client)
	incoming := member(0x14, 0x24, da.Client)
	signer := key(0x11)
	var domain da.Hash
	copy(domain[:], fill(32, 0x51))
	now := uint64(1700000000000)
	account := da.ID(publicBytes(0x61))
	states := map[string]da.State{}
	g := canonical(must(da.BuildGenesis(account, domain, authority, public(0x31), fill(32, 0x41), signer, now)), fill(32, 0x41), nil, signer)
	states["genesis"] = g
	p := canonical(must(da.BuildPairState(g, machine, machine.XPublic[:], fill(32, 0x41), fill(32, 0x42), signer, now+1)), fill(32, 0x42), prev(fill(32, 0x42), fill(32, 0x41)), signer)
	states["pairMachine"] = p
	p2 := canonical(must(da.BuildPairState(p, client, client.XPublic[:], fill(32, 0x42), fill(32, 0x43), signer, now+2)), fill(32, 0x43), prev(fill(32, 0x43), fill(32, 0x42)), signer)
	states["pairClient"] = p2
	members := []da.Member{client, authority}
	removed := []string{machine.ID}
	r := canonical(must(da.BuildMembershipState(p2, members, removed, fill(32, 0x43), fill(32, 0x44), signer, now+3)), fill(32, 0x44), prev(fill(32, 0x44), fill(32, 0x43)), signer)
	states["remove"] = r
	rotated := client
	copy(rotated.XPublic[:], must(da.XPublic(fill(32, 0x25))))
	rot := canonical(must(da.BuildMembershipState(r, []da.Member{rotated, authority}, removed, fill(32, 0x44), fill(32, 0x45), signer, 1)), fill(32, 0x45), prev(fill(32, 0x45), fill(32, 0x44)), signer)
	states["rotateX"] = rot
	v := canonical(must(da.BuildRecoveryKeyRotation(rot, fill(32, 0x45), public(0x32), signer, 1)), fill(32, 0x45), nil, signer)
	states["veto"] = v
	h := canonical(must(da.BuildHandoff(v, client.ID, public(0x33), fill(32, 0x45), fill(32, 0x46), signer, now+4)), fill(32, 0x46), prev(fill(32, 0x46), fill(32, 0x45)), signer)
	states["handoff"] = h
	rec, _, err := da.BuildRecovery(v, incoming, []da.Member{rotated, incoming}, fill(32, 0x47), key(0x14), key(0x32), public(0x33), 1)
	check(err)
	rec = canonical(rec, fill(32, 0x47), nil, key(0x14))
	states["recovery"] = rec
	ph := must(v.Hash())
	rh := must(rec.Hash())
	nonce := da.Hash(fill(32, 0x73))
	statement := da.RecoveryStatement{Account: account, Domain: domain, Parent: ph, NewAuthorityID: incoming.ID, NewRecoveryEd: public(0x33), NewMember: incoming, IssuedAt: rec.Core.IssuedAt, StateHash: rh, Nonce: nonce}
	check(statement.Sign(key(0x32)))
	lease := must(da.BuildLease(v, signer, now+100))
	hl := must(da.BuildLease(h, key(0x13), now+4))
	a := &pb.PairApproval{AccountId: account, PairingId: "0123456789abcdef0123456789abcdef", ApproverDeviceId: authority.ID, ApproverPublicKey: authority.EdPublic[:], CandidateDevicePublicKey: machine.EdPublic[:], CandidateKind: pb.DeviceKind_DEVICE_KIND_MACHINE, ExpiresMs: int64(now + 300000), Nonce: fill(16, 0x74), ReplacesDeviceId: machine.ID}
	// A replacement approval needs a fresh candidate distinct from the retired ID.
	a.CandidateDevicePublicKey = incoming.EdPublic[:]
	approval := must(proto.MarshalOptions{Deterministic: true}.Marshal(a))
	signature := ed25519.Sign(signer, join([]byte(e2e.PairApprovalContext), approval))
	normal := proto.Clone(a).(*pb.PairApproval)
	normal.CandidateKind = pb.DeviceKind_DEVICE_KIND_CLIENT
	normal.ReplacesDeviceId = ""
	normalApproval := must(proto.MarshalOptions{Deterministic: true}.Marshal(normal))
	normalSignature := ed25519.Sign(signer, join([]byte(e2e.PairApprovalContext), normalApproval))
	result := vector{States: map[string][]byte{}, Recovery: must(statement.MarshalBinary()), Lease: must(lease.MarshalBinary()), HandoffLease: must(hl.MarshalBinary()), Approval: approval, ApprovalSignature: signature}
	result.NormalApproval, result.NormalApprovalSignature = normalApproval, normalSignature
	for name, s := range states {
		result.States[name] = must(s.MarshalBinary())
	}
	gh := must(g.Hash())
	result.RecoveryCode = code(fill(32, 0x31), gh[:])
	verify(result)
	return result
}
func publicBytes(b int) []byte { p := public(b); return p[:] }
func verify(v vector) {
	states := map[string]da.State{}
	for name, raw := range v.States {
		s := must(da.ParseState(raw))
		if !bytes.Equal(raw, must(s.MarshalBinary())) {
			panic("noncanonical state")
		}
		states[name] = s
	}
	genesis := states["genesis"]
	pin := must(genesis.Pin())
	check(pin.VerifyGenesis(genesis))
	for i := 1; i < len(order)-1; i++ {
		check(da.VerifyTransition(states[order[i-1]], states[order[i]], nil))
	}
	statement := must(da.ParseRecoveryStatement(v.Recovery))
	check(da.VerifyTransition(states["veto"], states["recovery"], &statement))
	check(must(da.ParseLease(v.Lease)).Verify(states["veto"]))
	check(must(da.ParseLease(v.HandoffLease)).Verify(states["handoff"]))
	_, err := e2e.VerifyPairApproval(v.Approval, v.ApprovalSignature)
	check(err)
	_, err = e2e.VerifyPairApproval(v.NormalApproval, v.NormalApprovalSignature)
	check(err)
	// Verify every grant with production crypto/hpke, including rotated X.
	for i, name := range order {
		s := states[name]
		secretByte := 0x41 + i
		if name == "veto" {
			secretByte = 0x45
		}
		if name == "handoff" {
			secretByte = 0x46
		}
		if name == "recovery" {
			secretByte = 0x47
		}
		for _, m := range s.Core.Members {
			x := map[string]int{member(0x11, 0x21, da.Client).ID: 0x21, member(0x12, 0x22, da.Machine).ID: 0x22, member(0x13, 0x23, da.Client).ID: 0x23, member(0x14, 0x24, da.Client).ID: 0x24}[m.ID]
			if m.ID == member(0x13, 0x23, da.Client).ID && i >= 4 {
				x = 0x25
			}
			if !bytes.Equal(must(pin.OpenGrant(s, m.ID, fill(32, x))), fill(32, secretByte)) {
				panic("grant plaintext mismatch")
			}
		}
		if len(s.PrevSecret) > 0 {
			oldByte := secretByte - 1
			if name == "handoff" {
				oldByte = 0x45
			}
			opened := must(gcm(fill(32, secretByte)).Open(nil, s.PrevSecret[:12], s.PrevSecret[12:], []byte("ra3/prev-secret\x00")))
			if !bytes.Equal(opened, fill(32, oldByte)) {
				panic("previous secret mismatch")
			}
		}
	}
	// Confirm the signer did not alter the same-head lease or recovery parent.
	if states["veto"].Core.ContentEpoch != states["rotateX"].Core.ContentEpoch {
		panic("veto content epoch")
	}
	if !slices.Contains(states["recovery"].Core.Removed, states["veto"].Core.AuthorityID) {
		panic("missing tombstone")
	}
}
func main() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintln(os.Stderr, r)
			os.Exit(1)
		}
	}()
	if len(os.Args) > 1 && os.Args[1] == "verify" {
		var v vector
		check(json.NewDecoder(os.Stdin).Decode(&v))
		verify(v)
		fmt.Println("verified: states, leases, recovery, approval, grants and PrevSecret")
		return
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	check(encoder.Encode(generate()))
}
