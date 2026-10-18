package server

import (
	"encoding/json"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
)

// These transport-only fixtures do not assert membership. Real context, grant
// and endpoint verification are exercised in deviceauth and relay integration
// tests; an opaque relay is not an authority and cannot confer such trust.
func routingPairContext() deviceauth.PairContext {
	return deviceauth.PairContext{Account: "00000000000000000000000000000001", Domain: deviceauth.Hash{1}, Genesis: deviceauth.Hash{2}, AuthorityID: "00000000000000000000000000000002", AuthorityEd: [32]byte{3}, AuthorityX: [32]byte{4}, Approver: "00000000000000000000000000000003"}
}
func routingPairContextBytes() []byte { b, _ := json.Marshal(routingPairContext()); return b }
func routingPairCommit(pub []byte) []byte {
	p := routingPairContext()
	trust, _ := deviceauth.PairTrustDescriptor(p.Pin(), p.Approver)
	return e2e.PairDomainCommit(pub, trust)
}
