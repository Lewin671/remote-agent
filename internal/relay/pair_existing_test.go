package relay

import (
	"testing"

	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

// TestJoiningAccountAllowsSameAccountNewDomain covers the protocol 9 upgrade
// and protocol 10 recovery: both replace the security domain of the same
// account, and account join must overwrite the old security block.
func TestJoiningAccountAllowsSameAccountNewDomain(t *testing.T) {
	var oldDomain, newDomain, oldGenesis, newGenesis deviceauth.Hash
	oldDomain[0], newDomain[0] = 1, 2
	oldGenesis[0], newGenesis[0] = 3, 4
	existing := &Account{Security: &SecurityConfig{Pin: deviceauth.Pin{Account: "0123456789abcdef0123456789abcdef", Domain: oldDomain, Genesis: oldGenesis}}}
	same := deviceauth.Pin{Account: existing.Security.Pin.Account, Domain: newDomain, Genesis: newGenesis}
	if err := checkJoiningAccount(existing, same); err != nil {
		t.Fatalf("same account with a new security domain was refused: %v", err)
	}
	other := deviceauth.Pin{Account: "fedcba9876543210fedcba9876543210", Domain: newDomain, Genesis: newGenesis}
	if err := checkJoiningAccount(existing, other); err == nil {
		t.Fatal("a code for another account replaced the existing keys")
	}
	if err := checkJoiningAccount(nil, same); err != nil {
		t.Fatalf("a fresh account was refused: %v", err)
	}
}
