package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"google.golang.org/protobuf/proto"
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

// TestRecoveryPendingCanBeReplacedForANewPhone covers a user who loses the
// pending new phone's key before completion.
func TestRecoveryPendingCanBeReplacedForANewPhone(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup"); err != nil {
		t.Fatal(err)
	}
	c := ts.authority(t)
	pc := remoteagentv1connect.NewRecoveryServiceClient(http.DefaultClient, ts.url)
	head := c.states[len(c.states)-1]
	build := func() (deviceauth.State, deviceauth.RecoveryStatement, ed25519.PrivateKey, deviceauth.Member) {
		_, ed, err := ed25519.GenerateKey(nil)
		if err != nil {
			t.Fatal(err)
		}
		_, xpub, err := deviceauth.NewXKey()
		if err != nil {
			t.Fatal(err)
		}
		m, err := deviceauth.NewMember(ed.Public().(ed25519.PublicKey), xpub, deviceauth.Client)
		if err != nil {
			t.Fatal(err)
		}
		members := []deviceauth.Member{m}
		for _, old := range head.Core.Members {
			if old.ID != head.Core.AuthorityID {
				members = append(members, old)
			}
		}
		secret := make([]byte, 32)
		_, _ = rand.Read(secret)
		var recPub [32]byte
		recPub[0] = byte(len(m.ID))
		state, stmt, err := deviceauth.BuildRecovery(head, m, members, secret, ed, c.recovery, recPub, uint64(time.Now().UnixMilli()))
		if err != nil {
			t.Fatal(err)
		}
		return state, stmt, ed, m
	}
	submit := func(state deviceauth.State, stmt deviceauth.RecoveryStatement) string {
		sraw, _ := state.MarshalBinary()
		stmtRaw, _ := stmt.MarshalBinary()
		res, err := pc.SubmitRecovery(ctx, connect.NewRequest(&pb.SubmitRecoveryRequest{Statement: stmtRaw, State: sraw}))
		if err != nil {
			t.Fatal(err)
		}
		return res.Msg.RecoveryId
	}
	firstState, firstStmt, _, _ := build()
	first := submit(firstState, firstStmt)
	secondState, secondStmt, secondEd, secondMember := build()
	second := submit(secondState, secondStmt)
	if first == second {
		t.Fatal("a different new phone reused the pending recovery ID")
	}
	if _, err := pc.GetRecoveryContext(ctx, connect.NewRequest(&pb.GetRecoveryContextRequest{RecoveryId: first})); err == nil {
		t.Fatal("replaced recovery still exists")
	}
	if _, err := ts.srv.store.db.ExecContext(ctx, `UPDATE security_recovery SET ready_ms = 0 WHERE recovery_id = ?`, second); err != nil {
		t.Fatal(err)
	}
	label := append([]byte("ra3/recovery-complete\x00"), []byte(second)...)
	done, err := pc.CompleteRecovery(ctx, connect.NewRequest(&pb.CompleteRecoveryRequest{RecoveryId: second, Signature: ed25519.Sign(secondEd, label)}))
	if err != nil || done.Msg.DeviceId != secondMember.ID {
		t.Fatalf("complete replacement: %v %v", done, err)
	}
}

func TestRecoveryChainCap(t *testing.T) {
	if err := checkRecoveryEpoch(deviceauth.MaxStates); err != nil {
		t.Fatal(err)
	}
	if err := checkRecoveryEpoch(deviceauth.MaxStates + 1); err == nil {
		t.Fatal("recovery past the chain cap allowed")
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
	if !bytes.Equal(got.Msg.AccountPublicKey, ts.keys.AccountPublicKey()) {
		t.Fatalf("recovery context account key %x", got.Msg.AccountPublicKey)
	}
	// The old phone can veto by publishing any new state.
	if err := ts.removeViaState(t, mid); err != nil {
		t.Fatal(err)
	}
	got, err = pc.GetRecoveryContext(ctx, connect.NewRequest(&pb.GetRecoveryContextRequest{RecoveryId: sub.Msg.RecoveryId}))
	if err != nil || got.Msg.Phase != pb.GetRecoveryContextResponse_PHASE_REJECTED {
		t.Fatalf("veto: %v %v", got, err)
	}
	vetoLabel := append([]byte("ra3/recovery-complete\x00"), []byte(sub.Msg.RecoveryId)...)
	if _, err := pc.CompleteRecovery(ctx, connect.NewRequest(&pb.CompleteRecoveryRequest{RecoveryId: sub.Msg.RecoveryId, Signature: ed25519.Sign(newEd, vetoLabel)})); err == nil {
		t.Fatal("vetoed recovery completed")
	}
	if _, err := ts.srv.store.device(ctx, c.phoneID); err != nil {
		t.Fatalf("vetoed recovery removed the old authority: %v", err)
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
	// An in-flight pairing created by the old authority is cancelled by the recovery.
	pairClient := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, c.phoneEd, c.phoneID)))
	pkey, err := e2e.NewPairKey()
	if err != nil {
		t.Fatal(err)
	}
	createdPairing, err := pairClient.CreatePairing(ctx, connect.NewRequest(&pb.CreatePairingRequest{Commit: e2e.PairDomainCommit(pkey.Public(), ts.pairTrust(t, c.phoneID))}))
	if err != nil {
		t.Fatal(err)
	}
	pairingID := createdPairing.Msg.PairingId
	if _, err := ts.srv.store.db.ExecContext(ctx, `INSERT INTO pairing_admissions (pairing_id, account_id, approver_device_id, candidate_device_id, candidate_kind, approval, signature, candidate_token_hash, expires_ms) VALUES (?, ?, ?, ?, ?, X'01', X'01', zeroblob(32), ?)`,
		pairingID, ts.acct(), c.phoneID, randomHex(16), int32(pb.DeviceKind_DEVICE_KIND_MACHINE), time.Now().Add(time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	label := append([]byte("ra3/recovery-complete\x00"), []byte(sub.Msg.RecoveryId)...)
	done, err := pc.CompleteRecovery(ctx, connect.NewRequest(&pb.CompleteRecoveryRequest{RecoveryId: sub.Msg.RecoveryId, Signature: ed25519.Sign(newEd, label)}))
	if err != nil || done.Msg.DeviceId != newMember.ID || done.Msg.Token == "" {
		t.Fatalf("complete: %v %v", done, err)
	}
	if !bytes.Equal(done.Msg.AccountPublicKey, ts.keys.AccountPublicKey()) {
		t.Fatalf("complete account key %x", done.Msg.AccountPublicKey)
	}
	ts.srv.pairing.mu.Lock()
	pairing := ts.srv.pairing.byID[pairingID]
	ts.srv.pairing.mu.Unlock()
	if pairing == nil || !pairing.canceled {
		t.Fatalf("in-flight pairing survived recovery: %+v", pairing)
	}
	var canceled int64
	if err := ts.srv.store.db.QueryRowContext(ctx, `SELECT canceled_ms FROM pairing_admissions WHERE pairing_id = ?`, pairingID).Scan(&canceled); err != nil || canceled == 0 {
		t.Fatalf("pairing admission not cancelled: %d %v", canceled, err)
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

func (ts *testServer) putState(t *testing.T, states ...deviceauth.State) error {
	t.Helper()
	c := ts.authority(t)
	var in []*pb.SecurityState
	for _, s := range states {
		raw, err := s.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		in = append(in, &pb.SecurityState{State: raw})
	}
	api := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, c.phoneEd, c.phoneID)))
	_, err := api.PutSecurityState(context.Background(), connect.NewRequest(&pb.PutSecurityStateRequest{States: in}))
	return err
}

func TestStreamMembershipRejectsNonMember(t *testing.T) {
	ts := newTestServer(t)
	_, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	if err != nil {
		t.Fatal(err)
	}
	c := ts.authority(t)
	pub, priv, _ := ed25519.GenerateKey(nil)
	_, xpub, _ := deviceauth.NewXKey()
	approval, sig, err := e2e.NewPairApproval(ts.acct(), randomHex(16), c.phoneEd, pub, pb.DeviceKind_DEVICE_KIND_CLIENT, time.Now().Add(time.Minute).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: ts.keys.AccountPublicKey(), DevicePublicKey: pub, Kind: pb.DeviceKind_DEVICE_KIND_CLIENT})
	res, err := ts.auth.RegisterDevice(context.Background(), connect.NewRequest(&pb.RegisterDeviceRequest{ProtocolVersion: protocol.Version, Registration: reg, Approval: approval, ApprovalSignature: sig}))
	if err != nil {
		t.Fatal(err)
	}
	tok := ts.login(t, priv, res.Msg.DeviceId)
	if _, _, _, err := ts.srv.beginStream(context.Background(), tok); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("non-member stream: %v", err)
	}
	_ = xpub
}

func TestAuthorityRotationUpdatesServerRecoveryKey(t *testing.T) {
	ts := newTestServer(t)
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup"); err != nil {
		t.Fatal(err)
	}
	c := ts.authority(t)
	var newRecovery [32]byte
	newRecovery[0] = 0x42
	rotated, err := deviceauth.BuildRenewalRotating(c.states[len(c.states)-1], c.secret, newRecovery, c.phoneEd, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.putState(t, rotated); err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := ts.srv.store.db.QueryRowContext(context.Background(), `SELECT recovery_ed FROM accounts WHERE id = ?`, ts.acct()).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, newRecovery[:]) {
		t.Fatalf("recovery_ed = %x", stored)
	}
}

func TestStateFromTheFutureRejected(t *testing.T) {
	ts := newTestServer(t)
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup"); err != nil {
		t.Fatal(err)
	}
	c := ts.authority(t)
	future := uint64(time.Now().Add(10 * time.Minute).UnixMilli())
	state, err := deviceauth.BuildRenewal(c.states[len(c.states)-1], c.secret, c.phoneEd, future)
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.putState(t, state); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("future state: %v", err)
	}
}

func TestRemovedHalfAddedMemberIsTombstoned(t *testing.T) {
	ts := newTestServer(t)
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup"); err != nil {
		t.Fatal(err)
	}
	c := ts.authority(t)
	pub, _, _ := ed25519.GenerateKey(nil)
	_, xpub, _ := deviceauth.NewXKey()
	ghost, err := deviceauth.NewMember(pub, xpub, deviceauth.Machine)
	if err != nil {
		t.Fatal(err)
	}
	newSecret := make([]byte, 32)
	newSecret[0] = 0x91
	added, err := deviceauth.BuildPairState(c.states[len(c.states)-1], ghost, xpub, c.secret, newSecret, c.phoneEd, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.putState(t, added); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.srv.store.db.ExecContext(context.Background(), `INSERT INTO pairing_admissions (pairing_id, account_id, approver_device_id, candidate_device_id, candidate_kind, approval, signature, candidate_token_hash, expires_ms) VALUES (?, ?, ?, ?, ?, X'01', X'01', zeroblob(32), ?)`,
		randomHex(16), ts.acct(), c.phoneID, ghost.ID, int32(pb.DeviceKind_DEVICE_KIND_MACHINE), time.Now().Add(time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	newSecret2 := make([]byte, 32)
	newSecret2[0] = 0x92
	members := []deviceauth.Member{}
	for _, m := range added.Core.Members {
		if m.ID != ghost.ID {
			members = append(members, m)
		}
	}
	nextRemoved := append([]string(nil), added.Core.Removed...)
	nextRemoved = append(nextRemoved, ghost.ID)
	removed, err := deviceauth.BuildMembershipState(added, members, nextRemoved, newSecret, newSecret2, c.phoneEd, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	if err := deviceauth.VerifyTransition(added, removed, nil); err != nil {
		t.Fatalf("local removal transition: %v", err)
	}
	if err := ts.putState(t, removed); err != nil {
		t.Fatal(err)
	}
	var tomb int
	if err := ts.srv.store.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM revoked_devices WHERE id = ?`, ghost.ID).Scan(&tomb); err != nil || tomb != 1 {
		t.Fatalf("tombstone: %d %v", tomb, err)
	}
	var canceled int64
	if err := ts.srv.store.db.QueryRowContext(context.Background(), `SELECT COALESCE(MAX(canceled_ms),0) FROM pairing_admissions WHERE account_id = ?`, ts.acct()).Scan(&canceled); err != nil || canceled == 0 {
		t.Fatalf("admission not canceled: %d %v", canceled, err)
	}
}

func TestRecoveryTombstoneStopsCompletion(t *testing.T) {
	ts := newTestServer(t)
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup"); err != nil {
		t.Fatal(err)
	}
	c := ts.authority(t)
	pc := remoteagentv1connect.NewRecoveryServiceClient(http.DefaultClient, ts.url)
	_, newEd, _ := ed25519.GenerateKey(nil)
	_, nxpub, _ := deviceauth.NewXKey()
	newMember, err := deviceauth.NewMember(newEd.Public().(ed25519.PublicKey), nxpub, deviceauth.Client)
	if err != nil {
		t.Fatal(err)
	}
	var recPub [32]byte
	recPub[0] = 3
	secret := make([]byte, 32)
	secret[0] = 4
	head := c.states[len(c.states)-1]
	members := []deviceauth.Member{newMember}
	for _, m := range head.Core.Members {
		if m.ID != head.Core.AuthorityID {
			members = append(members, m)
		}
	}
	state, stmt, err := deviceauth.BuildRecovery(head, newMember, members, secret, newEd, c.recovery, recPub, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := state.MarshalBinary()
	stmtRaw, _ := stmt.MarshalBinary()
	sub, err := pc.SubmitRecovery(context.Background(), connect.NewRequest(&pb.SubmitRecoveryRequest{Statement: stmtRaw, State: raw}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.srv.store.db.ExecContext(context.Background(), `INSERT INTO revoked_devices (id, account_id, revoked_ms) VALUES (?, ?, ?)`, newMember.ID, ts.acct(), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.srv.store.db.ExecContext(context.Background(), `UPDATE security_recovery SET ready_ms = 0 WHERE recovery_id = ?`, sub.Msg.RecoveryId); err != nil {
		t.Fatal(err)
	}
	label := append([]byte("ra3/recovery-complete\x00"), []byte(sub.Msg.RecoveryId)...)
	if _, err := pc.CompleteRecovery(context.Background(), connect.NewRequest(&pb.CompleteRecoveryRequest{RecoveryId: sub.Msg.RecoveryId, Signature: ed25519.Sign(newEd, label)})); err == nil {
		t.Fatal("tombstoned recovered device completed")
	}
	if _, err := ts.srv.store.device(context.Background(), c.phoneID); err != nil {
		t.Fatalf("old authority removed by failed recovery: %v", err)
	}
	// A device owned by another account must not be adopted by recovery.
	if _, err := ts.srv.store.db.ExecContext(context.Background(), `DELETE FROM revoked_devices WHERE id = ?`, newMember.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.srv.store.db.ExecContext(context.Background(), `INSERT INTO accounts (id, public_key, plan, created_ms) VALUES ('other', X'01', 'owner', 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.srv.store.db.ExecContext(context.Background(), `INSERT INTO devices (id, account_id, public_key, kind, registration, signature, created_ms) VALUES (?, 'other', X'01', 2, X'01', X'01', 0)`, newMember.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pc.CompleteRecovery(context.Background(), connect.NewRequest(&pb.CompleteRecoveryRequest{RecoveryId: sub.Msg.RecoveryId, Signature: ed25519.Sign(newEd, label)})); err == nil {
		t.Fatal("recovery adopted a device of another account")
	}
	if _, err := ts.srv.store.device(context.Background(), c.phoneID); err != nil {
		t.Fatalf("old authority removed by cross-account recovery: %v", err)
	}
}

// The new phone learns the account's public key before it submits anything.
func TestRecoveryContextExposesAccountPublicKey(t *testing.T) {
	ts := newTestServer(t)
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup"); err != nil {
		t.Fatal(err)
	}
	c := ts.authority(t)
	pc := remoteagentv1connect.NewRecoveryServiceClient(http.DefaultClient, ts.url)
	res, err := pc.GetRecoveryContext(context.Background(), connect.NewRequest(&pb.GetRecoveryContextRequest{
		RecoveryEd: c.recovery.Public().(ed25519.PublicKey),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res.Msg.AccountPublicKey, ts.keys.AccountPublicKey()) {
		t.Fatalf("account key %x", res.Msg.AccountPublicKey)
	}
	if res.Msg.Phase != pb.GetRecoveryContextResponse_PHASE_UNSPECIFIED {
		t.Fatalf("phase %v", res.Msg.Phase)
	}
}

// TestStateFromTheFutureIsAClockWarning covers the phone clock being ahead:
// the relay refuses the state and says that the clock, not the account, is
// wrong.
func TestStateFromTheFutureIsAClockWarning(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup"); err != nil {
		t.Fatal(err)
	}
	c := ts.authority(t)
	tok := ts.login(t, c.phoneEd, c.phoneID)
	api := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(tok))
	next, err := deviceauth.BuildRenewal(c.states[len(c.states)-1], c.secret, c.phoneEd, uint64(time.Now().Add(10*time.Minute).UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := next.MarshalBinary()
	_, err = api.PutSecurityState(ctx, connect.NewRequest(&pb.PutSecurityStateRequest{States: []*pb.SecurityState{{State: raw}}}))
	if err == nil {
		t.Fatal("a state from the future was accepted")
	}
	if !strings.Contains(err.Error(), "from the future") || !strings.Contains(err.Error(), "clock") {
		t.Fatalf("future state error = %v, want a clock warning", err)
	}
}
