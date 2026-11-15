package server

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/protocol"
)

func TestProtocol15CannotLoginTo16(t *testing.T) {
	ts := newTestServer(t)
	private, id, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	challenge, err := ts.auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: id}))
	if err != nil {
		t.Fatal(err)
	}
	request := &pb.LoginRequest{DeviceId: id, Challenge: challenge.Msg.Challenge, Signature: e2e.SignLogin(private, challenge.Msg.Challenge), ProtocolVersion: 15}
	if protocol.Version != 16 {
		t.Fatal("test requires protocol 16")
	}
	if _, err = ts.auth.Login(ctx, connect.NewRequest(request)); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("old client not told to update: %v", err)
	}
	request.ProtocolVersion = 16
	if _, err = ts.auth.Login(ctx, connect.NewRequest(request)); err != nil {
		t.Fatalf("current client: %v", err)
	}
}

// Tokens have no binary/protocol field. This deliberately preserves the
// documented upgrade boundary: a pre-upgrade token may read history until it
// expires; login/pairing and the daemon's signed command gate enforce updates.
func TestProtocolUpgradeDoesNotInferVersionFromExistingToken(t *testing.T) {
	ts := newTestServer(t)
	_, id, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	if err != nil {
		t.Fatal(err)
	}
	token := newToken()
	if err = ts.srv.store.tenant(ts.acct()).addToken(context.Background(), hashToken(token), id, nowMs()+60000); err != nil {
		t.Fatal(err)
	}
	if d, err := ts.srv.authenticate(context.Background(), token); err != nil || d.id != id {
		t.Fatal("existing token unexpectedly rewritten/rejected", err)
	}
}
