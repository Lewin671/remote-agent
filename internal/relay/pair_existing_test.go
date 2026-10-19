package relay

import (
	"crypto/ed25519"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"testing"

	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

// TestJoiningAccountDomainRules covers the protocol 9 upgrade and protocol 10
// recovery. A protocol 10 store pins the exact domain and genesis, so a forged
// genesis for the same account is refused. Only a protocol 9 account, which
// has no store yet, accepts a rebuilt domain from the same account.
func TestJoiningAccountDomainRules(t *testing.T) {
	var oldDomain, newDomain, oldGenesis, newGenesis deviceauth.Hash
	oldDomain[0], newDomain[0] = 1, 2
	oldGenesis[0], newGenesis[0] = 3, 4
	existing := &Account{Security: &SecurityConfig{Pin: deviceauth.Pin{Account: "0123456789abcdef0123456789abcdef", Domain: oldDomain, Genesis: oldGenesis}}}
	same := deviceauth.Pin{Account: existing.Security.Pin.Account, Domain: oldDomain, Genesis: oldGenesis}
	if err := checkJoiningAccount(existing, same, true); err != nil {
		t.Fatalf("same pin was refused: %v", err)
	}
	otherDomain := deviceauth.Pin{Account: existing.Security.Pin.Account, Domain: newDomain, Genesis: newGenesis}
	if err := checkJoiningAccount(existing, otherDomain, true); err == nil {
		t.Fatal("a forged genesis replaced a protocol 10 pin")
	}
	// Protocol 9 has no security store, so its domain is being rebuilt.
	if err := checkJoiningAccount(existing, otherDomain, false); err != nil {
		t.Fatalf("protocol 9 upgrade refused: %v", err)
	}
	other := deviceauth.Pin{Account: "fedcba9876543210fedcba9876543210", Domain: oldDomain, Genesis: oldGenesis}
	if err := checkJoiningAccount(existing, other, false); err == nil {
		t.Fatal("a code for another account replaced the existing keys")
	}
	if err := checkJoiningAccount(nil, same, true); err != nil {
		t.Fatalf("a fresh account was refused: %v", err)
	}
}

// TestRemovedDeviceRejoinsWithANewIdentity covers finding S10: a removed
// device must not present its revoked key when it joins again.
func TestRemovedDeviceRejoinsWithANewIdentity(t *testing.T) {
	_, seed, _ := ed25519.GenerateKey(nil)
	existing := &Account{DeviceSeed: seed.Seed()}
	pub, priv, err := joiningIdentity(existing, false)
	if err != nil {
		t.Fatal(err)
	}
	if !pub.Equal(existing.DeviceKey().Public()) ||
		!priv.Equal(existing.DeviceKey()) {
		t.Fatal("an ordinary re-pair replaced the device identity")
	}
	pub, priv, err = joiningIdentity(existing, true)
	if err != nil {
		t.Fatal(err)
	}
	if pub.Equal(existing.DeviceKey().Public()) {
		t.Fatal("a removed device reused its revoked identity")
	}
	if !pub.Equal(priv.Public()) {
		t.Fatal("the fresh key pair does not match")
	}
}

func TestPairingReplacementLineageAfterExplicitArchiveDeletion(t *testing.T) {
	prior := &Account{Replacements: []*pb.MachineReplacementProof{{Approval: []byte("prior signed replacement")}}}
	for _, tc := range []struct {
		name     string
		removed  bool
		replaces string
		want     int
	}{
		{"ordinary X rotation", false, "", 1},
		{"authorized archive replacement", true, "revoked-identity", 2},
		{"fresh enrollment after record deletion", true, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, sig := []byte{1}, []byte{2}
			got := pairingReplacements(prior, tc.removed, &pb.PairApproval{ReplacesDeviceId: tc.replaces}, raw, sig)
			if len(got) != tc.want {
				t.Fatalf("lineage length=%d want %d", len(got), tc.want)
			}
			if tc.replaces != "" {
				raw[0] = 9
				sig[0] = 9
				if got[len(got)-1].Approval[0] != 1 || got[len(got)-1].Signature[0] != 2 {
					t.Fatal("signed proof was not copied")
				}
			}
		})
	}
}
