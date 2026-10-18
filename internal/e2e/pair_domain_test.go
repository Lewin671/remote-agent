package e2e

import (
	"bytes"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"testing"
)

func TestPairDomainTrustIsCommittedBeforeRevealAndHasNoLateSASGrindingField(t *testing.T) {
	var p deviceauth.Pin
	p.Account = "0123456789abcdef0123456789abcdef"
	p.Domain[0] = 1
	p.Genesis[0] = 2
	p.AuthorityID = "11111111111111111111111111111111"
	p.AuthorityEd[0] = 3
	p.AuthorityX[0] = 4
	approver := "22222222222222222222222222222222"
	trust, err := deviceauth.PairTrustDescriptor(p, approver)
	if err != nil {
		t.Fatal(err)
	}
	initiator := bytes.Repeat([]byte{5}, 32)
	joiner := bytes.Repeat([]byte{6}, 32)
	ed := bytes.Repeat([]byte{7}, 32)
	x := bytes.Repeat([]byte{8}, 32)
	commit := PairDomainCommit(initiator, trust)
	transcript := PairDomainTranscript("synthetic-pair", initiator, joiner, ed, x, 2, trust)
	other := p
	other.AuthorityEd[0] ^= 1
	changedTrust, err := deviceauth.PairTrustDescriptor(other, approver)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(commit, PairDomainCommit(initiator, changedTrust)) || bytes.Equal(transcript, PairDomainTranscript("synthetic-pair", initiator, joiner, ed, x, 2, changedTrust)) {
		t.Fatal("authority substitution did not alter commitment and SAS")
	}
	changedX := bytes.Clone(x)
	changedX[0] ^= 1
	if bytes.Equal(transcript, PairDomainTranscript("synthetic-pair", initiator, joiner, ed, changedX, 2, trust)) {
		t.Fatal("recipient key not bound")
	}
	// Late operation fields are verified in the signed context but CANNOT be
	// input to the SAS. A fake authority must not grind their nonce after DH.
	ctx := deviceauth.PairContext{Account: p.Account, Domain: p.Domain, Genesis: p.Genesis, AuthorityID: p.AuthorityID, AuthorityEd: p.AuthorityEd, AuthorityX: p.AuthorityX, Approver: approver}
	before, _ := deviceauth.PairTrustDescriptor(ctx.Pin(), ctx.Approver)
	ctx.Nonce[0] = 99
	ctx.Expected[0] = 88
	ctx.Expires = 12345
	ctx.Signature = bytes.Repeat([]byte{9}, 64)
	after, _ := deviceauth.PairTrustDescriptor(ctx.Pin(), ctx.Approver)
	if !bytes.Equal(before, after) || !bytes.Equal(PairDomainCommit(initiator, before), PairDomainCommit(initiator, after)) {
		t.Fatal("a post-reveal field changes the committed trust")
	}
}
