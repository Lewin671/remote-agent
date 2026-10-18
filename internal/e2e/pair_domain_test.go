package e2e

import (
	"bytes"
	"testing"

	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

func TestPairDomainTrustIsCommittedBeforeReveal(t *testing.T) {
	p := deviceauth.Pin{Account: "0123456789abcdef0123456789abcdef"}
	p.Domain[0] = 1
	p.Genesis[0] = 2
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
	other.Genesis[0] ^= 1
	changedTrust, err := deviceauth.PairTrustDescriptor(other, approver)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(commit, PairDomainCommit(initiator, changedTrust)) || bytes.Equal(transcript, PairDomainTranscript("synthetic-pair", initiator, joiner, ed, x, 2, changedTrust)) {
		t.Fatal("genesis substitution did not alter commitment and SAS")
	}
	changedX := bytes.Clone(x)
	changedX[0] ^= 1
	if bytes.Equal(transcript, PairDomainTranscript("synthetic-pair", initiator, joiner, ed, changedX, 2, trust)) {
		t.Fatal("recipient key not bound")
	}
}
