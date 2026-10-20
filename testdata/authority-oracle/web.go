package main

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hpke"
	"slices"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	da "github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"google.golang.org/protobuf/proto"
)

type webVector struct {
	States                                                     map[string][]byte
	Recovery, Lease, HandoffLease, Approval, ApprovalSignature []byte
	NormalApproval, NormalApprovalSignature                    []byte
	RecoveryCode                                               string
}

var order = []string{"genesis", "pairMachine", "pairClient", "remove", "rotateX", "veto", "handoff", "recovery"}

func generateWeb() webVector {
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
	p := canonical(must(da.BuildPairState(g, machine, machine.XPublic[:], fill(32, 0x41), fill(32, 0x42), signer, now+1)), fill(32, 0x42), webPrev(fill(32, 0x42), fill(32, 0x41)), signer)
	states["pairMachine"] = p
	p2 := canonical(must(da.BuildPairState(p, client, client.XPublic[:], fill(32, 0x42), fill(32, 0x43), signer, now+2)), fill(32, 0x43), webPrev(fill(32, 0x43), fill(32, 0x42)), signer)
	states["pairClient"] = p2
	members := []da.Member{client, authority}
	removed := []string{machine.ID}
	r := canonical(must(da.BuildMembershipState(p2, members, removed, fill(32, 0x43), fill(32, 0x44), signer, now+3)), fill(32, 0x44), webPrev(fill(32, 0x44), fill(32, 0x43)), signer)
	states["remove"] = r
	rotated := client
	copy(rotated.XPublic[:], must(da.XPublic(fill(32, 0x25))))
	rot := canonical(must(da.BuildMembershipState(r, []da.Member{rotated, authority}, removed, fill(32, 0x44), fill(32, 0x45), signer, 1)), fill(32, 0x45), webPrev(fill(32, 0x45), fill(32, 0x44)), signer)
	states["rotateX"] = rot
	v := canonical(must(da.BuildRecoveryKeyRotation(rot, fill(32, 0x45), public(0x32), signer, 1)), fill(32, 0x45), nil, signer)
	states["veto"] = v
	h := canonical(must(da.BuildHandoff(v, client.ID, public(0x33), fill(32, 0x45), fill(32, 0x46), signer, now+4)), fill(32, 0x46), webPrev(fill(32, 0x46), fill(32, 0x45)), signer)
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
	result := webVector{States: map[string][]byte{}, Recovery: must(statement.MarshalBinary()), Lease: must(lease.MarshalBinary()), HandoffLease: must(hl.MarshalBinary()), Approval: approval, ApprovalSignature: signature}
	result.NormalApproval, result.NormalApprovalSignature = normalApproval, normalSignature
	for name, s := range states {
		result.States[name] = must(s.MarshalBinary())
	}
	gh := must(g.Hash())
	result.RecoveryCode = code(fill(32, 0x31), gh[:])
	verifyWeb(result)
	return result
}
func publicBytes(b int) []byte { p := public(b); return p[:] }
func verifyWeb(v webVector) {
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

func webPrev(newSecret, oldSecret []byte) []byte {
	return previousSecret(newSecret, oldSecret, fill(12, 0x72))
}
func canonical(s da.State, secret, previous []byte, signer ed25519.PrivateKey) da.State {
	ek := must(hpke.DHKEM(ecdh.X25519()).DeriveKeyPair(fill(32, 0x71)))
	return fixedState(s, secret, previous, signer, must(ek.Bytes()))
}
