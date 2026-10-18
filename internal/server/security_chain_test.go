package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

// TestSecurityChainMembershipAndRemoval covers the server's chain storage,
// member enforcement and removal cleanup through the public API.
func TestSecurityChainMembershipAndRemoval(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	mp, mid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if err != nil {
		t.Fatal(err)
	}
	mtok := ts.login(t, mp, mid)
	cp, cid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	if err != nil {
		t.Fatal(err)
	}
	client := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, cp, cid)))
	states, err := client.GetSecurityStates(ctx, connect.NewRequest(&pb.GetSecurityStatesRequest{}))
	if err != nil || states.Msg.HeadEpoch < 2 || len(states.Msg.States) != int(states.Msg.HeadEpoch) {
		t.Fatalf("states: %v %v", states, err)
	}
	if err := ts.removeViaState(t, mid); err != nil {
		t.Fatal(err)
	}
	machine := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(mtok))
	if _, err := machine.GetSecurityStates(ctx, connect.NewRequest(&pb.GetSecurityStatesRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated && connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("removed machine still authenticated: %v", err)
	}
}

// TestSecurityChainRejectsForkRollbackAndTampering covers the malicious relay
// cases for the chain itself.
func TestSecurityChainRejectsForkRollbackAndTampering(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup"); err != nil {
		t.Fatal(err)
	}
	c := ts.authority(t)
	tok := ts.login(t, c.phoneEd, c.phoneID)
	api := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(tok))
	head := c.states[len(c.states)-1]

	// A state signed by the wrong key is refused.
	bad := head
	bad.Signature = append([]byte(nil), head.Signature...)
	bad.Signature[0] ^= 1
	raw, _ := bad.MarshalBinary()
	if _, err := api.PutSecurityState(ctx, connect.NewRequest(&pb.PutSecurityStateRequest{States: []*pb.SecurityState{{State: raw}}})); err == nil {
		t.Fatal("tampered signature accepted")
	}
	// Rollback to an older stored state is idempotent, not an error.
	old, _ := c.states[1].MarshalBinary()
	if _, err := api.PutSecurityState(ctx, connect.NewRequest(&pb.PutSecurityStateRequest{States: []*pb.SecurityState{{State: old}}})); err != nil {
		t.Fatalf("idempotent rollback: %v", err)
	}
	// A gap is refused.
	next := head
	next.Core.Epoch += 2
	gap, err := next.MarshalBinary()
	if err == nil {
		if _, err := api.PutSecurityState(ctx, connect.NewRequest(&pb.PutSecurityStateRequest{States: []*pb.SecurityState{{State: gap}}})); err == nil {
			t.Fatal("gap accepted")
		}
	}
}

// TestRecoveryPendingVetoAndComplete covers the 48-hour recovery service.
func TestRecoveryPendingVetoAndComplete(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	_, mid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if err != nil {
		t.Fatal(err)
	}
	c := ts.authority(t)
	pc := remoteagentv1connect.NewRecoveryServiceClient(http.DefaultClient, ts.url)

	_, newEd, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, nxpub, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	newMember, err := deviceauth.NewMember(newEd.Public().(ed25519.PublicKey), nxpub, deviceauth.Client)
	if err != nil {
		t.Fatal(err)
	}
	build := func() (deviceauth.State, deviceauth.RecoveryStatement) {
		head := c.states[len(c.states)-1]
		members := []deviceauth.Member{newMember}
		for _, m := range head.Core.Members {
			if m.ID != c.phoneID {
				members = append(members, m)
			}
		}
		var recoveryPub [32]byte
		recoveryPub[0] = 9
		secret := make([]byte, 32)
		_, _ = rand.Read(secret)
		state, stmt, err := deviceauth.BuildRecovery(head, newMember, members, secret, newEd, c.recovery, recoveryPub, uint64(time.Now().UnixMilli()))
		if err != nil {
			t.Fatal(err)
		}
		return state, stmt
	}
	submit := func(state deviceauth.State, stmt deviceauth.RecoveryStatement) (*connect.Response[pb.SubmitRecoveryResponse], error) {
		sraw, _ := state.MarshalBinary()
		stmtRaw, err := stmt.MarshalBinary()
		if err != nil {
			t.Fatalf("marshal statement: %v", err)
		}
		if _, err := deviceauth.ParseRecoveryStatement(stmtRaw); err != nil {
			t.Fatalf("round trip statement: %v", err)
		}
		return pc.SubmitRecovery(ctx, connect.NewRequest(&pb.SubmitRecoveryRequest{Statement: stmtRaw, State: sraw}))
	}

	state, stmt := build()
	sub, err := submit(state, stmt)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pc.GetRecoveryContext(ctx, connect.NewRequest(&pb.GetRecoveryContextRequest{RecoveryId: sub.Msg.RecoveryId}))
	if err != nil || got.Msg.Phase != pb.GetRecoveryContextResponse_PHASE_PENDING {
		t.Fatalf("pending: %v %v", got, err)
	}
	// The old phone can veto by publishing any new state.
	if err := ts.removeViaState(t, mid); err != nil {
		t.Fatal(err)
	}
	got, err = pc.GetRecoveryContext(ctx, connect.NewRequest(&pb.GetRecoveryContextRequest{RecoveryId: sub.Msg.RecoveryId}))
	if err != nil || got.Msg.Phase != pb.GetRecoveryContextResponse_PHASE_REJECTED {
		t.Fatalf("veto: %v %v", got, err)
	}

	// A fresh submission can complete once the delay has passed.
	state, stmt = build()
	sub, err = submit(state, stmt)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.srv.store.db.ExecContext(ctx, `UPDATE security_recovery SET ready_ms = 0 WHERE recovery_id = ?`, sub.Msg.RecoveryId); err != nil {
		t.Fatal(err)
	}
	label := append([]byte("ra3/recovery-complete\x00"), []byte(sub.Msg.RecoveryId)...)
	done, err := pc.CompleteRecovery(ctx, connect.NewRequest(&pb.CompleteRecoveryRequest{RecoveryId: sub.Msg.RecoveryId, Signature: ed25519.Sign(newEd, label)}))
	if err != nil || done.Msg.DeviceId != newMember.ID || done.Msg.Token == "" {
		t.Fatalf("complete: %v %v", done, err)
	}
	// Idempotent retry issues no second device.
	again, err := pc.CompleteRecovery(ctx, connect.NewRequest(&pb.CompleteRecoveryRequest{RecoveryId: sub.Msg.RecoveryId, Signature: ed25519.Sign(newEd, label)}))
	if err != nil || again.Msg.DeviceId != newMember.ID {
		t.Fatalf("retry: %v %v", again, err)
	}
	var n int
	if err := ts.srv.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM devices WHERE id = ?`, newMember.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("recovered device rows: %d %v", n, err)
	}
	// The old authority's login is gone.
	if _, err := ts.srv.store.device(ctx, c.phoneID); !errors.Is(err, errNoRows) {
		t.Fatalf("old authority still registered: %v", err)
	}
	// The new phone can read the chain and has the new recovery key.
	api := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(done.Msg.Token))
	if _, err := api.GetSecurityStates(ctx, connect.NewRequest(&pb.GetSecurityStatesRequest{})); err != nil {
		t.Fatalf("new phone: %v", err)
	}
}
