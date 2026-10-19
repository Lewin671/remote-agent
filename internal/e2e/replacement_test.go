package e2e

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

type replacementVector struct {
	Account         string                        `json:"account"`
	Old             string                        `json:"old"`
	Target          string                        `json:"target"`
	X               []byte                        `json:"x"`
	States          [][]byte                      `json:"states"`
	Proofs          []*pb.MachineReplacementProof `json:"proofs"`
	ClockSkewProofs []*pb.MachineReplacementProof `json:"clock_skew_proofs"`
	ExpiredProofs   []*pb.MachineReplacementProof `json:"expired_proofs"`
}

func TestMachineReplacementProofVectors(t *testing.T) {
	const path = "testdata/machine-replacement-vectors.json"
	if os.Getenv("UPDATE_REPLACEMENT_VECTORS") == "1" {
		var input map[string]json.RawMessage
		b, err := os.ReadFile("../deviceauth/testdata/domain-v3.json")
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &input); err != nil {
			t.Fatal(err)
		}
		field := func(k string) string { var s string; json.Unmarshal(input[k], &s); return s }
		key := func(k string) []byte { b, _ := hex.DecodeString(field(k)); return b }
		decode := func(k string) deviceauth.State {
			var v struct {
				State []byte `json:"state"`
			}
			json.Unmarshal(input[k], &v)
			s, e := deviceauth.ParseState(v.State)
			if e != nil {
				t.Fatal(e)
			}
			return s
		}
		genesis, head := decode("genesis"), decode("added")
		signer := ed25519.NewKeyFromSeed(key("phone_seed"))
		secret := key("content_secret_2")
		var old string
		for _, m := range head.Core.Members {
			if m.Kind == deviceauth.Machine {
				old = m.ID
			}
		}
		v := replacementVector{Account: head.Core.Account, Old: old}
		states := []deviceauth.State{genesis, head}
		for i := byte(0); i < 2; i++ {
			keep := []deviceauth.Member{}
			for _, m := range head.Core.Members {
				if m.ID != old {
					keep = append(keep, m)
				}
			}
			removed := append(append([]string{}, head.Core.Removed...), old)
			slices.Sort(removed)
			nextSecret := bytes.Repeat([]byte{91 + i}, 32)
			gone, e := deviceauth.BuildMembershipState(head, keep, removed, secret, nextSecret, signer, head.Core.IssuedAt+1000)
			if e != nil {
				t.Fatal(e)
			}
			ed := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{81 + i}, 32))
			x, xpub, e := deviceauth.NewXKey()
			if e != nil {
				t.Fatal(e)
			}
			member, e := deviceauth.NewMember(ed.Public().(ed25519.PublicKey), xpub, deviceauth.Machine)
			if e != nil {
				t.Fatal(e)
			}
			secret = bytes.Repeat([]byte{101 + i}, 32)
			next, e := deviceauth.BuildPairState(gone, member, xpub, nextSecret, secret, signer, gone.Core.IssuedAt+1000)
			if e != nil {
				t.Fatal(e)
			}
			raw, sig, e := NewPairApprovalReplacing(v.Account, "11111111111111111111111111111111", signer, member.EdPublic[:], pb.DeviceKind_DEVICE_KIND_MACHINE, int64(next.Core.IssuedAt+300000), old)
			if e != nil {
				t.Fatal(e)
			}
			v.Proofs = append(v.Proofs, &pb.MachineReplacementProof{Approval: raw, Signature: sig})
			for _, variant := range []struct {
				expiry int64
				into   *[]*pb.MachineReplacementProof
			}{
				{int64(next.Core.IssuedAt) - 1, &v.ClockSkewProofs},
				{int64(next.Core.IssuedAt) - deviceauth.MaxClockSkew - 1, &v.ExpiredProofs},
			} {
				raw, sig, err := NewPairApprovalReplacing(v.Account, "11111111111111111111111111111111", signer, member.EdPublic[:], pb.DeviceKind_DEVICE_KIND_MACHINE, variant.expiry, old)
				if err != nil {
					t.Fatal(err)
				}
				*variant.into = append(*variant.into, &pb.MachineReplacementProof{Approval: raw, Signature: sig})
			}
			v.X = x
			v.Target = member.ID
			states = append(states, gone, next)
			head = next
			old = member.ID
		}
		for _, s := range states {
			b, _ := s.MarshalBinary()
			v.States = append(v.States, b)
		}
		data, _ := json.MarshalIndent(v, "", "  ")
		if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var v replacementVector
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	states := []deviceauth.State{}
	for _, raw := range v.States {
		s, e := deviceauth.ParseState(raw)
		if e != nil {
			t.Fatal(e)
		}
		states = append(states, s)
	}
	pin, _ := states[0].Pin()
	if err := pin.VerifyGenesis(states[0]); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < len(states); i++ {
		if err := deviceauth.VerifyTransition(states[i-1], states[i], nil); err != nil {
			t.Fatal(err)
		}
	}
	origins, err := MachineReplacementOrigins(v.Account, v.Target, states, v.Proofs)
	if err != nil || len(origins) != 2 || !origins[v.Old] {
		t.Fatalf("valid replacement lineage: %v %v", origins, err)
	}
	skewOrigins, err := MachineReplacementOrigins(v.Account, v.Target, states, v.ClockSkewProofs)
	if err != nil || !skewOrigins[v.Old] {
		t.Fatalf("protocol clock skew rejected: %v", err)
	}
	if _, err := MachineReplacementOrigins(v.Account, v.Target, states, v.ExpiredProofs); err == nil {
		t.Fatal("expired admission at membership time accepted")
	}
	bad := append([]*pb.MachineReplacementProof{}, v.Proofs...)
	bad[0] = &pb.MachineReplacementProof{Approval: bytes.Clone(v.Proofs[0].Approval), Signature: bytes.Clone(v.Proofs[0].Signature)}
	bad[0].Signature[0] ^= 1
	if _, err := MachineReplacementOrigins(v.Account, v.Target, states, bad); err == nil {
		t.Fatal("forged proof accepted")
	}
	if _, err := MachineReplacementOrigins(v.Account, v.Old, states, v.Proofs); err == nil {
		t.Fatal("wrong target accepted")
	}
	origins, err = MachineReplacementOrigins(v.Account, v.Target, states, nil)
	if err != nil || origins[v.Old] {
		t.Fatal("missing proof authorized replacement")
	}
}
