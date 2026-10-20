package main

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"slices"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	da "github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"google.golang.org/protobuf/proto"
)

type Entry struct {
	Name  string
	State []byte
	Proof []byte
	Lease []byte
}
type iosVector struct {
	Entries           []Entry
	Approval          []byte
	ApprovalSignature []byte
	RecoveryCode      string
}

const now uint64 = 1700000000000

func generateIOS() iosVector {
	admin, incoming, machine, recovered := iosMember(1, da.Client), iosMember(2, da.Client), iosMember(3, da.Machine), iosMember(4, da.Client)
	recovery := key(21)
	pub := [32]byte(recovery.Public().(ed25519.PublicKey))
	nextPub := [32]byte(key(22).Public().(ed25519.PublicKey))
	finalPub := [32]byte(key(23).Public().(ed25519.PublicKey))
	domain := da.Hash(fill(32, 50))
	account := e2e.DeviceID(key(30).Public().(ed25519.PublicKey))
	var v iosVector
	add := func(name string, s da.State, proof []byte, signer ed25519.PrivateKey) {
		l := must(da.BuildLease(s, signer, max(now+100, s.Core.IssuedAt)))
		v.Entries = append(v.Entries, Entry{name, must(s.MarshalBinary()), proof, must(l.MarshalBinary())})
	}
	g := fixed(must(da.BuildGenesis(account, domain, admin, pub, fill(32, 40), key(1), now)), fill(32, 40), key(1), nil)
	add("genesis", g, nil, key(1))
	p := fixed(must(da.BuildPairState(g, incoming, incoming.XPublic[:], fill(32, 40), fill(32, 41), key(1), now-10)), fill(32, 41), key(1), iosPrev(fill(32, 40), fill(32, 41)))
	add("pair", p, nil, key(1))
	m := fixed(must(da.BuildMembershipState(p, append(slices.Clone(p.Core.Members), machine), p.Core.Removed, fill(32, 41), fill(32, 42), key(1), now+1)), fill(32, 42), key(1), iosPrev(fill(32, 41), fill(32, 42)))
	add("membership", m, nil, key(1))
	members := slices.Clone(m.Core.Members)
	for i := range members {
		if members[i].ID == machine.ID {
			members[i].XPublic = [32]byte(must(da.XPublic(fill(32, 70))))
		}
	}
	r := fixed(must(da.BuildMembershipState(m, members, m.Core.Removed, fill(32, 42), fill(32, 43), key(1), now+2)), fill(32, 43), key(1), iosPrev(fill(32, 42), fill(32, 43)))
	add("rotateX", r, nil, key(1))
	members = slices.DeleteFunc(slices.Clone(r.Core.Members), func(m da.Member) bool { return m.ID == machine.ID })
	rm := fixed(must(da.BuildMembershipState(r, members, []string{machine.ID}, fill(32, 43), fill(32, 44), key(1), now+3)), fill(32, 44), key(1), iosPrev(fill(32, 43), fill(32, 44)))
	add("remove", rm, nil, key(1))
	veto := fixed(must(da.BuildRecoveryKeyRotation(rm, fill(32, 44), nextPub, key(1), now+4)), fill(32, 44), key(1), nil)
	add("veto", veto, nil, key(1))
	h := fixed(must(da.BuildHandoff(veto, incoming.ID, finalPub, fill(32, 44), fill(32, 45), key(1), now+5)), fill(32, 45), key(1), iosPrev(fill(32, 44), fill(32, 45)))
	add("handoff", h, nil, key(2))
	s, proof, err := da.BuildRecovery(h, recovered, []da.Member{recovered}, fill(32, 46), key(4), key(23), pub, now+6)
	if err != nil {
		panic(err)
	}
	s = fixed(s, fill(32, 46), key(4), nil)
	proof.StateHash = must(s.Hash())
	proof.Nonce = da.Hash(fill(32, 88))
	if err := proof.Sign(key(23)); err != nil {
		panic(err)
	}
	add("recovery", s, must(proof.MarshalBinary()), key(4))
	a := &pb.PairApproval{AccountId: account, PairingId: "0123456789abcdef0123456789abcdef", ApproverDeviceId: admin.ID, ApproverPublicKey: admin.EdPublic[:], CandidateDevicePublicKey: machine.EdPublic[:], CandidateKind: pb.DeviceKind_DEVICE_KIND_MACHINE, ExpiresMs: int64(now + 300000), Nonce: fill(16, 88), ReplacesDeviceId: recovered.ID}
	v.Approval = must(proto.MarshalOptions{Deterministic: true}.Marshal(a))
	v.ApprovalSignature = ed25519.Sign(key(1), join([]byte(e2e.PairApprovalContext), v.Approval))
	gh := must(g.Hash())
	v.RecoveryCode = code(fill(32, 21), gh[:])
	return v
}
func verifyIOS(v iosVector) {
	expected := generateIOS()
	if len(v.Entries) != len(expected.Entries) {
		panic("entry count")
	}
	var parent da.State
	var pin da.Pin
	for i, e := range v.Entries {
		want := expected.Entries[i]
		s := must(da.ParseState(e.State))
		reference := must(da.ParseState(want.State))
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
			var proof *da.RecoveryStatement
			if len(e.Proof) > 0 {
				r := must(da.ParseRecoveryStatement(e.Proof))
				ref := must(da.ParseRecoveryStatement(want.Proof))
				r.Signature = ref.Signature
				if !bytes.Equal(must(r.MarshalBinary()), want.Proof) {
					panic("recovery byte mismatch")
				}
				r = must(da.ParseRecoveryStatement(e.Proof))
				proof = &r
			}
			if err := da.VerifyTransition(parent, s, proof); err != nil {
				panic(fmt.Errorf("%s: %w", e.Name, err))
			}
		}
		l := must(da.ParseLease(e.Lease))
		if err := l.Verify(s); err != nil {
			panic(err)
		}
		refLease := must(da.ParseLease(want.Lease))
		l.Signature = refLease.Signature
		if !bytes.Equal(must(l.MarshalBinary()), want.Lease) {
			panic("lease byte mismatch")
		}
		// The production Go HPKE recipient opens every Swift grant.
		for _, m := range s.Core.Members {
			var x []byte
			for b := 1; b <= 4; b++ {
				if iosMember(b, m.Kind).ID == m.ID {
					x = fill(32, b+10)
				}
			}
			if e.Name == "rotateX" && m.ID == iosMember(3, da.Machine).ID {
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

func iosMember(b int, kind da.Kind) da.Member { return member(b, b+10, kind) }
func iosPrev(oldSecret, newSecret []byte) []byte {
	return previousSecret(newSecret, oldSecret, fill(12, 88))
}
func fixed(s da.State, secret []byte, signer ed25519.PrivateKey, previous []byte) da.State {
	return fixedState(s, secret, previous, signer, fill(32, 99))
}
