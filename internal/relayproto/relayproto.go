// Package relayproto holds the protocol version spoken between devices
// (daemons and clients) and the relay server.
package relayproto

import (
	"fmt"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// Version is the relay protocol version checked by the server at its entry
// points (RegisterDevice, Login, PeekPairing, JoinPairing). Peers and relays
// of different versions refuse each other rather than falling back feature by
// feature. Raise it with any change to the relay-visible surface that an older
// relay or client would misread, in step with clientcore, web, Android and iOS.
const Version = 17

// Mismatch is the error returned by the relay when a device speaks a different
// relay protocol version.
func Mismatch(peer string, got uint32) error {
	ce := connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf(
		"%s speaks relay protocol version %d and this relay speaks version %d; update to a matching release", peer, got, Version))
	if d, err := connect.NewErrorDetail(&pb.RelayErrorDetail{
		Reason:             pb.RelayErrorReason_RELAY_ERROR_REASON_PROTOCOL_MISMATCH,
		ServerRelayVersion: Version,
	}); err == nil {
		ce.AddDetail(d)
	}
	return ce
}
