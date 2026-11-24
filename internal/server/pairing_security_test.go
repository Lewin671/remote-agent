package server

import (
	"connectrpc.com/connect"
	"context"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/relayproto"
	"github.com/Lewin671/remote-agent/shared/devicesec"
	"net/http"
	"testing"
	"time"
)

func TestPairingRateLimitDoesNotRevealValidCodes(t *testing.T) {
	ts := newTestServer(t)
	_, _, _ = ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	pc := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, ts.authority(t).phoneEd, ts.authority(t).phoneID)))
	anon := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, ts.url)
	key, _ := e2e.NewPairKey()
	created, err := pc.CreatePairing(context.Background(), connect.NewRequest(&pb.CreatePairingRequest{Commit: devicesec.PairDomainCommit(key.Public(), ts.pairTrust(t, ts.authority(t).phoneID))}))
	if err != nil {
		t.Fatal(err)
	}
	ts.srv.pairing.mu.Lock()
	for range badCodesPerMinute {
		ts.srv.pairing.badCodes = append(ts.srv.pairing.badCodes, time.Now())
	}
	ts.srv.pairing.mu.Unlock()
	for _, code := range []string{"invalid-code", created.Msg.Code} {
		_, err := anon.JoinPairing(context.Background(), connect.NewRequest(&pb.JoinPairingRequest{ProtocolVersion: relayproto.Version, Code: code, JoinerPublic: key.Public(), DeviceHpkePublicKey: key.Public(), DevicePublicKey: key.Public(), JoinerKind: pb.DeviceKind_DEVICE_KIND_CLIENT}))
		if connect.CodeOf(err) != connect.CodeResourceExhausted {
			t.Fatalf("rate-limited lookup leaked validity: %v", err)
		}
	}
}

func TestCanceledPairingCannotReleaseApprovedPayload(t *testing.T) {
	ts := newTestServer(t)
	_, _, _ = ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	pc := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, ts.authority(t).phoneEd, ts.authority(t).phoneID)))
	anon := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, ts.url)
	ctx := context.Background()
	key, _ := e2e.NewPairKey()
	created, err := pc.CreatePairing(ctx, connect.NewRequest(&pb.CreatePairingRequest{Commit: devicesec.PairDomainCommit(key.Public(), ts.pairTrust(t, ts.authority(t).phoneID))}))
	if err != nil {
		t.Fatal(err)
	}
	joined, err := anon.JoinPairing(ctx, connect.NewRequest(&pb.JoinPairingRequest{ProtocolVersion: relayproto.Version, Code: created.Msg.Code, JoinerPublic: key.Public(), DeviceHpkePublicKey: key.Public(), DevicePublicKey: key.Public(), JoinerKind: pb.DeviceKind_DEVICE_KIND_CLIENT}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.PairingId
	if _, err := pc.RevealPairing(ctx, connect.NewRequest(&pb.RevealPairingRequest{Trust: ts.pairTrust(t, ts.authority(t).phoneID), PairingId: id, InitiatorPublic: key.Public()})); err != nil {
		t.Fatal(err)
	}
	env, _ := e2e.Seal(e2e.NewKey(), &pb.Header{Version: 1, Kind: pb.PayloadKind_PAYLOAD_KIND_PAIRING, RequestId: id}, []byte("sealed account secret"))
	approval, sig, _ := e2e.NewPairApproval(ts.acct(), id, ts.authority(t).phoneEd, key.Public(), pb.DeviceKind_DEVICE_KIND_CLIENT, created.Msg.ExpiresMs)
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

func TestTransientAdmissionFailuresPreserveApprovalRetry(t *testing.T) {
	for _, scenario := range []struct {
		name, setup, recover string
		code                 connect.Code
	}{
		{"capacity", `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < ?)
INSERT INTO pairing_admissions (pairing_id, account_id, approver_device_id, candidate_device_id, candidate_kind, approval, signature, candidate_token_hash, expires_ms)
SELECT 'capacity-' || i, ?, 'synthetic-approver', 'synthetic-candidate', 2, X'01', X'01', zeroblob(32), ? FROM n`,
			`DELETE FROM pairing_admissions WHERE pairing_id LIKE 'capacity-%'`, connect.CodeResourceExhausted},
		{"database", `CREATE TRIGGER unavailable_admission BEFORE INSERT ON pairing_admissions BEGIN SELECT RAISE(ABORT, 'synthetic store failure'); END`,
			`DROP TRIGGER unavailable_admission`, connect.CodeInternal},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ts := newTestServer(t)
			ctx := context.Background()
			if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup"); err != nil {
				t.Fatal(err)
			}
			pc := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, ts.authority(t).phoneEd, ts.authority(t).phoneID)))
			anon := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, ts.url)
			key, _ := e2e.NewPairKey()
			created, err := pc.CreatePairing(ctx, connect.NewRequest(&pb.CreatePairingRequest{Commit: devicesec.PairDomainCommit(key.Public(), ts.pairTrust(t, ts.authority(t).phoneID))}))
			if err != nil {
				t.Fatal(err)
			}
			id := created.Msg.PairingId
			joined, err := anon.JoinPairing(ctx, connect.NewRequest(&pb.JoinPairingRequest{ProtocolVersion: relayproto.Version, Code: created.Msg.Code, JoinerPublic: key.Public(), DeviceHpkePublicKey: key.Public(), DevicePublicKey: key.Public(), JoinerKind: pb.DeviceKind_DEVICE_KIND_CLIENT}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = pc.RevealPairing(ctx, connect.NewRequest(&pb.RevealPairingRequest{Trust: ts.pairTrust(t, ts.authority(t).phoneID), PairingId: id, InitiatorPublic: key.Public()})); err != nil {
				t.Fatal(err)
			}
			env, _ := e2e.Seal(e2e.NewKey(), &pb.Header{Version: 1, Kind: pb.PayloadKind_PAYLOAD_KIND_PAIRING, RequestId: id}, []byte("synthetic payload"))
			raw, sig, _ := e2e.NewPairApproval(ts.acct(), id, ts.authority(t).phoneEd, key.Public(), pb.DeviceKind_DEVICE_KIND_CLIENT, created.Msg.ExpiresMs)
			req := &pb.ApprovePairingRequest{PairingId: id, Payload: env, Approval: raw, ApprovalSignature: sig}
			var args []any
			if scenario.name == "capacity" {
				args = []any{maxPairings, ts.acct(), created.Msg.ExpiresMs}
			}
			if _, err = ts.srv.store.db.ExecContext(ctx, scenario.setup, args...); err != nil {
				t.Fatal(err)
			}
			if _, err = pc.ApprovePairing(ctx, connect.NewRequest(req)); connect.CodeOf(err) != scenario.code {
				t.Fatalf("transient failure classified as %s, want %s: %v", connect.CodeOf(err), scenario.code, err)
			}
			pending, err := anon.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: id, CandidateToken: joined.Msg.CandidateToken}))
			if err != nil || pending.Msg.Payload != nil {
				t.Fatalf("failed persistence released payload: %v %v", pending, err)
			}
			if _, err = ts.srv.store.db.ExecContext(ctx, scenario.recover); err != nil {
				t.Fatal(err)
			}
			if _, err = pc.ApprovePairing(ctx, connect.NewRequest(req)); err != nil {
				t.Fatalf("same approved operation could not retry: %v", err)
			}
			approved, err := anon.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: id, CandidateToken: joined.Msg.CandidateToken}))
			if err != nil || approved.Msg.Payload == nil {
				t.Fatalf("successful retry did not release payload: %v %v", approved, err)
			}
		})
	}
}

func TestPairingJoinerMetaForwardingValidationAndCleanup(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup"); err != nil {
		t.Fatal(err)
	}
	pc := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, ts.authority(t).phoneEd, ts.authority(t).phoneID)))
	anon := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, ts.url)
	key, _ := e2e.NewPairKey()
	created, err := pc.CreatePairing(ctx, connect.NewRequest(&pb.CreatePairingRequest{Commit: devicesec.PairDomainCommit(key.Public(), ts.pairTrust(t, ts.authority(t).phoneID))}))
	if err != nil {
		t.Fatal(err)
	}
	id := created.Msg.PairingId
	joined, err := anon.JoinPairing(ctx, connect.NewRequest(&pb.JoinPairingRequest{
		ProtocolVersion:     relayproto.Version,
		Code:                created.Msg.Code,
		JoinerPublic:        key.Public(),
		DeviceHpkePublicKey: key.Public(),
		DevicePublicKey:     key.Public(),
		JoinerKind:          pb.DeviceKind_DEVICE_KIND_CLIENT,
	}))
	if err != nil {
		t.Fatal(err)
	}
	// Before reveal, joiner_meta is ignored.
	metaKey := e2e.NewKey()
	validMeta, err := e2e.Seal(metaKey, &pb.Header{Version: 1, Kind: pb.PayloadKind_PAYLOAD_KIND_PAIRING, RequestId: id, Seq: 1}, []byte("opaque-meta"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := anon.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: id, CandidateToken: joined.Msg.CandidateToken, JoinerMeta: validMeta})); err != nil {
		t.Fatal(err)
	}
	if _, err := pc.RevealPairing(ctx, connect.NewRequest(&pb.RevealPairingRequest{Trust: ts.pairTrust(t, ts.authority(t).phoneID), PairingId: id, InitiatorPublic: key.Public()})); err != nil {
		t.Fatal(err)
	}
	initRes, err := pc.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: id}))
	if err != nil {
		t.Fatal(err)
	}
	if initRes.Msg.JoinerMeta != nil {
		t.Fatalf("expected nil joiner_meta before post-reveal upload, got %v", initRes.Msg.JoinerMeta)
	}
	// Oversized metadata is rejected.
	oversized, err := e2e.Seal(metaKey, &pb.Header{Version: 1, Kind: pb.PayloadKind_PAYLOAD_KIND_PAIRING, RequestId: id, Seq: 1}, make([]byte, maxDeviceMetaBytes+1))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := anon.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: id, CandidateToken: joined.Msg.CandidateToken, JoinerMeta: oversized})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid argument for oversized joiner_meta, got %v", err)
	}
	// Wrong envelope request_id is rejected.
	wrongReq, err := e2e.Seal(metaKey, &pb.Header{Version: 1, Kind: pb.PayloadKind_PAYLOAD_KIND_PAIRING, RequestId: "wrong-pairing-id", Seq: 1}, []byte("bad"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := anon.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: id, CandidateToken: joined.Msg.CandidateToken, JoinerMeta: wrongReq})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected invalid argument for wrong envelope request_id, got %v", err)
	}
	// Valid encrypted metadata after reveal is stored and visible ONLY to initiator.
	joinerRes, err := anon.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: id, CandidateToken: joined.Msg.CandidateToken, JoinerMeta: validMeta}))
	if err != nil {
		t.Fatal(err)
	}
	if joinerRes.Msg.JoinerMeta != nil {
		t.Fatalf("joiner should not receive joiner_meta back")
	}
	initRes, err = pc.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: id}))
	if err != nil {
		t.Fatal(err)
	}
	if initRes.Msg.JoinerMeta == nil || len(initRes.Msg.JoinerMeta.Ciphertext) != len(validMeta.Ciphertext) {
		t.Fatalf("initiator did not receive forwarded joiner_meta: %v", initRes.Msg.JoinerMeta)
	}
	// Cancel clears joiner_meta.
	if _, err := pc.CancelPairing(ctx, connect.NewRequest(&pb.CancelPairingRequest{PairingId: id})); err != nil {
		t.Fatal(err)
	}
	initRes, err = pc.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: id}))
	if err != nil {
		t.Fatal(err)
	}
	if initRes.Msg.JoinerMeta != nil {
		t.Fatalf("expected joiner_meta cleared on cancel, got %v", initRes.Msg.JoinerMeta)
	}
}
