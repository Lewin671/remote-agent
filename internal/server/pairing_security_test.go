package server

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"net/http"
	"testing"
	"time"
)

func TestPairingRateLimitDoesNotRevealValidCodes(t *testing.T) {
	ts := newTestServer(t)
	cp, cid, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	pc := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, cp, cid)))
	anon := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, ts.url)
	key, _ := e2e.NewPairKey()
	created, err := pc.CreatePairing(context.Background(), connect.NewRequest(&pb.CreatePairingRequest{Commit: e2e.PairCommit(key.Public())}))
	if err != nil {
		t.Fatal(err)
	}
	ts.srv.pairing.mu.Lock()
	for range badCodesPerMinute {
		ts.srv.pairing.badCodes = append(ts.srv.pairing.badCodes, time.Now())
	}
	ts.srv.pairing.mu.Unlock()
	for _, code := range []string{"invalid-code", created.Msg.Code} {
		_, err := anon.JoinPairing(context.Background(), connect.NewRequest(&pb.JoinPairingRequest{Code: code, JoinerPublic: key.Public(), DevicePublicKey: key.Public(), JoinerKind: pb.DeviceKind_DEVICE_KIND_CLIENT}))
		if connect.CodeOf(err) != connect.CodeResourceExhausted {
			t.Fatalf("rate-limited lookup leaked validity: %v", err)
		}
	}
}

func TestCanceledPairingCannotReleaseApprovedPayload(t *testing.T) {
	ts := newTestServer(t)
	cp, cid, _ := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	pc := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, cp, cid)))
	anon := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, ts.url)
	ctx := context.Background()
	key, _ := e2e.NewPairKey()
	created, err := pc.CreatePairing(ctx, connect.NewRequest(&pb.CreatePairingRequest{Commit: e2e.PairCommit(key.Public())}))
	if err != nil {
		t.Fatal(err)
	}
	joined, err := anon.JoinPairing(ctx, connect.NewRequest(&pb.JoinPairingRequest{Code: created.Msg.Code, JoinerPublic: key.Public(), DevicePublicKey: key.Public(), JoinerKind: pb.DeviceKind_DEVICE_KIND_CLIENT}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.PairingId
	if _, err := pc.RevealPairing(ctx, connect.NewRequest(&pb.RevealPairingRequest{PairingId: id, InitiatorPublic: key.Public()})); err != nil {
		t.Fatal(err)
	}
	env, _ := e2e.Seal(e2e.NewKey(), &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_PAIRING, RequestId: id}, []byte("sealed account secret"))
	approval, sig, _ := e2e.NewPairApproval(ts.acct(), id, cp, key.Public(), pb.DeviceKind_DEVICE_KIND_CLIENT, created.Msg.ExpiresMs)
	if _, err := pc.ApprovePairing(ctx, connect.NewRequest(&pb.ApprovePairingRequest{PairingId: id, Payload: env, Approval: approval, ApprovalSignature: sig})); err != nil {
		t.Fatal(err)
	}
	if _, err := pc.CancelPairing(ctx, connect.NewRequest(&pb.CancelPairingRequest{PairingId: id})); err != nil {
		t.Fatal(err)
	}
	res, err := anon.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: id, CandidateToken: joined.Msg.CandidateToken}))
	if err != nil || res.Msg.Payload != nil || res.Msg.State != pb.PairingState_PAIRING_STATE_CANCELED {
		t.Fatalf("canceled pairing still delivered secret: %v %v", res, err)
	}
}
