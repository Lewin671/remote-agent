package relay_test

import (
	"connectrpc.com/connect"
	"context"
	"crypto/rand"
	"encoding/hex"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
)

func forwardRequest(client *remoteclient.Client, message *pb.ForwardRequest) *connect.Request[pb.ForwardRequest] {
	var nonce [16]byte
	rand.Read(nonce[:])
	message.AttemptId = hex.EncodeToString(nonce[:])
	epoch, err := client.RelayEpoch(context.Background())
	if err != nil {
		panic(err)
	}
	message.RelayEpoch = epoch
	r := connect.NewRequest(message)
	r.Header().Set("Remote-Agent-Attempt", message.AttemptId)
	return r
}
