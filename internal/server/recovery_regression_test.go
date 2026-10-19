package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
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

func newRecoveryKey(t *testing.T, fill byte) (ed25519.PrivateKey, [32]byte) {
	t.Helper()
	seed := bytes.Repeat([]byte{fill}, 32)
	key := ed25519.NewKeyFromSeed(seed)
	var pub [32]byte
	copy(pub[:], key.Public().(ed25519.PublicKey))
	return key, pub
}

// recoveryFixture makes the test account's recovery key one this test knows,
// so it can play the phone that lost the old one.
func recoveryFixture(t *testing.T) (*testServer, deviceauth.State, ed25519.PrivateKey, *testChain) {
	t.Helper()
	ts := newTestServer(t)
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup"); err != nil {
		t.Fatal(err)
	}
	c := ts.authority(t)
	key, pub := newRecoveryKey(t, 0xa1)
	rotated, err := deviceauth.BuildRecoveryKeyRotation(c.states[len(c.states)-1], c.secret, pub, c.phoneEd, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.putState(t, rotated); err != nil {
		t.Fatal(err)
	}
	return ts, rotated, key, c
}

type recoveryBuild struct {
	state  deviceauth.State
	stmt   deviceauth.RecoveryStatement
	ed     ed25519.PrivateKey
	member deviceauth.Member
}

func buildRecovery(t *testing.T, parent deviceauth.State, oldRecovery ed25519.PrivateKey, fill byte) recoveryBuild {
	t.Helper()
	_, ed, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, xpub, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	member, err := deviceauth.NewMember(ed.Public().(ed25519.PublicKey), xpub, deviceauth.Client)
	if err != nil {
		t.Fatal(err)
	}
	members := []deviceauth.Member{member}
	for _, m := range parent.Core.Members {
		if m.ID != parent.Core.AuthorityID {
			members = append(members, m)
		}
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	_, newRecovery := newRecoveryKey(t, fill)
	state, stmt, err := deviceauth.BuildRecovery(parent, member, members, secret, ed, oldRecovery, newRecovery, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	return recoveryBuild{state, stmt, ed, member}
}

func submitRecovery(t *testing.T, pc remoteagentv1connect.RecoveryServiceClient, b recoveryBuild) string {
	t.Helper()
	sraw, _ := b.state.MarshalBinary()
	stmtRaw, _ := b.stmt.MarshalBinary()
	res, err := pc.SubmitRecovery(context.Background(), connect.NewRequest(&pb.SubmitRecoveryRequest{Statement: stmtRaw, State: sraw}))
	if err != nil {
		t.Fatal(err)
	}
	return res.Msg.RecoveryId
}

func completeRecovery(t *testing.T, ts *testServer, pc remoteagentv1connect.RecoveryServiceClient, id string, b recoveryBuild) {
	t.Helper()
	if _, err := ts.srv.store.db.ExecContext(context.Background(), `UPDATE security_recovery SET ready_ms = 0 WHERE recovery_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	label := append([]byte("ra3/recovery-complete\x00"), []byte(id)...)
	if _, err := pc.CompleteRecovery(context.Background(), connect.NewRequest(&pb.CompleteRecoveryRequest{RecoveryId: id, Signature: ed25519.Sign(b.ed, label)})); err != nil {
		t.Fatal(err)
	}
}

// TestRecoveryCanHappenASecondTime is finding B1 sequence A: after a
// completed recovery the new phone's code must be able to recover again.
func TestRecoveryCanHappenASecondTime(t *testing.T) {
	ts, head, oldRecovery, _ := recoveryFixture(t)
	pc := remoteagentv1connect.NewRecoveryServiceClient(http.DefaultClient, ts.url)

	first := buildRecovery(t, head, oldRecovery, 0xb1)
	id := submitRecovery(t, pc, first)
	completeRecovery(t, ts, pc, id, first)

	// The completed row must not stand in for a new recovery.
	key, _ := newRecoveryKey(t, 0xb1)
	got, err := getRecoveryContext(t, pc, key, mustPub(t, key), "")
	if err != nil {
		t.Fatalf("context after completion: %v", err)
	}
	if got.Msg.Phase == pb.GetRecoveryContextResponse_PHASE_PUBLISHED {
		t.Fatal("a completed recovery was offered as the pending one")
	}

	second := buildRecovery(t, first.state, key, 0xb2)
	id2 := submitRecovery(t, pc, second)
	completeRecovery(t, ts, pc, id2, second)
	now, _, _ := ts.srv.store.tenant(ts.acct()).securityHead(context.Background())
	if now.Core.AuthorityID != second.member.ID {
		t.Fatalf("second recovery did not take over: authority %s", now.Core.AuthorityID)
	}
}

// TestRecoveryAfterVetoNeedsNoOldCode is finding B1 sequence B: a veto
// rotates the code, and the new code's holder must be able to submit.
func TestRecoveryAfterVetoNeedsNoOldCode(t *testing.T) {
	ts, head, oldRecovery, c := recoveryFixture(t)
	pc := remoteagentv1connect.NewRecoveryServiceClient(http.DefaultClient, ts.url)

	first := buildRecovery(t, head, oldRecovery, 0xc1)
	_ = submitRecovery(t, pc, first)

	// The old phone vetoes with a fresh code (K3) and the pending statement
	// is rejected.
	vetoKey, vetoPub := newRecoveryKey(t, 0xc2)
	veto, err := deviceauth.BuildRecoveryKeyRotation(head, c.secret, vetoPub, c.phoneEd, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	if err := ts.putState(t, veto); err != nil {
		t.Fatal(err)
	}
	got, err := getRecoveryContext(t, pc, vetoKey, mustPub(t, vetoKey), "")
	if err != nil {
		t.Fatalf("context with the new code: %v", err)
	}
	if got.Msg.Phase == pb.GetRecoveryContextResponse_PHASE_REJECTED {
		t.Fatal("the vetoed recovery was still offered as pending")
	}

	// The phone holding K3 submits a fresh recovery from the vetoed head.
	second := buildRecovery(t, veto, vetoKey, 0xc3)
	id := submitRecovery(t, pc, second)
	completeRecovery(t, ts, pc, id, second)
	now, _, _ := ts.srv.store.tenant(ts.acct()).securityHead(context.Background())
	if now.Core.AuthorityID != second.member.ID {
		t.Fatalf("post-veto recovery did not take over: authority %s", now.Core.AuthorityID)
	}
}

func mustPub(t *testing.T, key ed25519.PrivateKey) []byte {
	t.Helper()
	return key.Public().(ed25519.PublicKey)
}

// TestRegisterDeviceCannotWipeAnotherDevice is finding B2: a holder of the
// account key and a device's public key must not be able to re-register that
// device with an approval of its own.
func TestRegisterDeviceCannotWipeAnotherDevice(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup"); err != nil {
		t.Fatal(err)
	}
	victimEd, victimID, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	if err != nil {
		t.Fatal(err)
	}
	victimPub := victimEd.Public().(ed25519.PublicKey)
	if _, err := ts.srv.store.db.ExecContext(ctx, `INSERT INTO tokens (hash, account_id, device_id, expires_ms) VALUES (?, ?, ?, ?)`, bytes.Repeat([]byte{1}, 32), ts.acct(), victimID, time.Now().Add(time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.srv.store.db.ExecContext(ctx, `INSERT INTO push_subs (device_id, account_id, subscription, kinds) VALUES (?, ?, '{}', 1)`, victimID, ts.acct()); err != nil {
		t.Fatal(err)
	}

	// An attacker signs a self-consistent approval with their own key,
	// naming the victim as the candidate.
	_, attacker, _ := ed25519.GenerateKey(nil)
	approval, approvalSig, err := e2e.NewPairApproval(ts.acct(), randomHex(16), attacker, victimPub, pb.DeviceKind_DEVICE_KIND_CLIENT, time.Now().Add(time.Minute).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	reg, _ := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: ts.keys.AccountPublicKey(), DevicePublicKey: victimPub, Kind: pb.DeviceKind_DEVICE_KIND_CLIENT, CreatedMs: time.Now().UnixMilli()})
	_, err = ts.auth.RegisterDevice(ctx, connect.NewRequest(&pb.RegisterDeviceRequest{ProtocolVersion: protocol.Version, Registration: reg, Approval: approval, ApprovalSignature: approvalSig}))
	if err == nil {
		t.Fatal("an unauthorized replacement was accepted")
	}
	var tokens, subs int
	if err := ts.srv.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tokens WHERE device_id = ?`, victimID).Scan(&tokens); err != nil {
		t.Fatal(err)
	}
	if err := ts.srv.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM push_subs WHERE device_id = ?`, victimID).Scan(&subs); err != nil {
		t.Fatal(err)
	}
	if tokens != 1 || subs != 1 {
		t.Fatalf("attacker changed the victim's rows: tokens=%d subs=%d", tokens, subs)
	}
}

// TestRecoveryContextNeedsAKeyBoundSignature is finding N18: the lookup must
// prove possession of the recovery key over a server challenge, and a rotated
// key can no longer read the account.
func TestRecoveryContextNeedsAKeyBoundSignature(t *testing.T) {
	ts, head, oldRecovery, _ := recoveryFixture(t)
	pc := remoteagentv1connect.NewRecoveryServiceClient(http.DefaultClient, ts.url)
	ctx := context.Background()
	ch, err := pc.GetRecoveryChallenge(ctx, connect.NewRequest(&pb.GetRecoveryChallengeRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	_, attacker, _ := ed25519.GenerateKey(nil)
	req := &pb.GetRecoveryContextRequest{RecoveryEd: mustPub(t, oldRecovery), Challenge: ch.Msg.Challenge, Signature: ed25519.Sign(attacker, recoveryContextBytes(ch.Msg.Challenge, mustPub(t, oldRecovery), ""))}
	if _, err := pc.GetRecoveryContext(ctx, connect.NewRequest(req)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("an unbound signature was accepted: %v", err)
	}
	if _, err := pc.GetRecoveryContext(ctx, connect.NewRequest(&pb.GetRecoveryContextRequest{RecoveryEd: mustPub(t, oldRecovery)})); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("a lookup without a challenge was accepted: %v", err)
	}

	first := buildRecovery(t, head, oldRecovery, 0xd1)
	id := submitRecovery(t, pc, first)
	completeRecovery(t, ts, pc, id, first)
	// The old code is no longer the account's recovery key: it cannot read
	// the chain any more, even with a correct signature.
	ch, err = pc.GetRecoveryChallenge(ctx, connect.NewRequest(&pb.GetRecoveryChallengeRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	req = &pb.GetRecoveryContextRequest{RecoveryEd: mustPub(t, oldRecovery), Challenge: ch.Msg.Challenge, Signature: ed25519.Sign(oldRecovery, recoveryContextBytes(ch.Msg.Challenge, mustPub(t, oldRecovery), ""))}
	if _, err := pc.GetRecoveryContext(ctx, connect.NewRequest(req)); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("a rotated-out recovery key still read the account: %v", err)
	}
}

func TestCompletedRecoveryRetryAfterNextSubmission(t *testing.T) {
	ts, head, key, _ := recoveryFixture(t)
	pc := remoteagentv1connect.NewRecoveryServiceClient(http.DefaultClient, ts.url)
	first := buildRecovery(t, head, key, 0xb1)
	id := submitRecovery(t, pc, first)
	completeRecovery(t, ts, pc, id, first)
	nextKey, _ := newRecoveryKey(t, 0xb1)
	second := buildRecovery(t, first.state, nextKey, 0xb2)
	_ = submitRecovery(t, pc, second)
	label := append([]byte("ra3/recovery-complete\x00"), []byte(id)...)
	res, err := pc.CompleteRecovery(context.Background(), connect.NewRequest(&pb.CompleteRecoveryRequest{RecoveryId: id, Signature: ed25519.Sign(first.ed, label)}))
	if err != nil || res.Msg.DeviceId != first.member.ID {
		t.Fatalf("lost completion response cannot be retried: %v", err)
	}
}
func TestExistingAdmissionRequiresExactUncanceledApproval(t *testing.T) {
	ts, _, _, _ := recoveryFixture(t)
	ctx := context.Background()
	db := ts.srv.store.db
	a := &enrollmentApproval{info: &pb.PairApproval{PairingId: "round3-admission", ApproverDeviceId: "synthetic-authority"}, raw: []byte("approved"), signature: []byte("signature")}
	_, err := db.Exec(`INSERT INTO pairing_admissions(pairing_id,account_id,approver_device_id,candidate_device_id,candidate_kind,approval,signature,candidate_token_hash,expires_ms) VALUES(?,?,?,?,?,?,?,?,?)`, a.info.PairingId, ts.acct(), a.info.ApproverDeviceId, "synthetic-candidate", 2, a.raw, a.signature, make([]byte, 32), nowMs()+60000)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"different-bytes", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			if mode == "different-bytes" {
				a.raw = []byte("another-approved-value")
			} else {
				a.raw = []byte("approved")
				_, err = db.Exec(`UPDATE pairing_admissions SET canceled_ms=1 WHERE pairing_id=?`, a.info.PairingId)
				if err != nil {
					t.Fatal(err)
				}
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if err := requireAdmission(ctx, tx, a, ts.acct(), "synthetic-candidate", pb.DeviceKind_DEVICE_KIND_CLIENT); err == nil {
				t.Fatal("invalid admission reused")
			}
		})
	}
}

func TestArchivedRecoveryDoesNotExposeChainToRemovedPhone(t *testing.T) {
	ts, head, key, _ := recoveryFixture(t)
	pc := remoteagentv1connect.NewRecoveryServiceClient(http.DefaultClient, ts.url)
	first := buildRecovery(t, head, key, 0xb1)
	id := submitRecovery(t, pc, first)
	completeRecovery(t, ts, pc, id, first)
	nextKey, _ := newRecoveryKey(t, 0xb1)
	second := buildRecovery(t, first.state, nextKey, 0xb2)
	id2 := submitRecovery(t, pc, second)
	if _, err := getRecoveryContext(t, pc, first.ed, nil, id); err != nil {
		t.Fatalf("current phone cannot resume archived recovery: %v", err)
	}
	completeRecovery(t, ts, pc, id2, second)
	if _, err := getRecoveryContext(t, pc, first.ed, nil, id); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("removed phone read archived context: %v", err)
	}
}
