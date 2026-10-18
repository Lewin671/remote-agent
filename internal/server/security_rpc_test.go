package server

import (
	"bytes"
	"context"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

func TestSecurityRPCTransportIsOpaqueAndBounded(t *testing.T) {
	_, mid, cid, _, client, stream := newV2Env(t)
	capsule := deviceauth.Capsule{From: cid, To: mid, Method: deviceauth.Catalog, Enc: bytes.Repeat([]byte{1}, 32), Cipher: bytes.Repeat([]byte{2}, 64)}
	raw, err := capsule.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		res, err := client.Forward(context.Background(), connect.NewRequest(&pb.ForwardRequest{MachineId: mid, SecurityRpc: raw}))
		if err == nil && !bytes.Equal(res.Msg.SecurityRpc, []byte("opaque response")) {
			t.Error("transport changed reply")
		}
		done <- err
	}()
	frame := stream.next(t, func(f *pb.ServerFrame) bool { return f.GetForwardCall() != nil })
	call := frame.GetForwardCall()
	if call.Command != nil || !bytes.Equal(call.SecurityRpc, raw) {
		t.Fatal("transport changed request")
	}
	stream.write(t, &pb.PeerFrame{Kind: &pb.PeerFrame_ForwardReply{ForwardReply: &pb.ForwardReply{CallId: call.CallId, SecurityRpc: []byte("opaque response")}}})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, req := range []*pb.ForwardRequest{{MachineId: mid, SecurityRpc: raw, Command: &pb.Envelope{}}, {MachineId: mid, SecurityRpc: make([]byte, deviceauth.MaxRPCBytes+513)}, {MachineId: cid, SecurityRpc: raw}} {
		if _, err := client.Forward(context.Background(), connect.NewRequest(req)); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("invalid RPC not rejected: %v", err)
		}
	}
}
