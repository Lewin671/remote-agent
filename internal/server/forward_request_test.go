package server

import (
	"connectrpc.com/connect"
	"crypto/rand"
	"encoding/hex"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

func forwardRequest(message *pb.ForwardRequest, epoch string) *connect.Request[pb.ForwardRequest] {
	var nonce [16]byte
	rand.Read(nonce[:])
	message.AttemptId = hex.EncodeToString(nonce[:])
	message.RelayEpoch = epoch
	r := connect.NewRequest(message)
	r.Header().Set("Remote-Agent-Attempt", message.AttemptId)
	return r
}
