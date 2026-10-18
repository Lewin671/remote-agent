package server

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"google.golang.org/protobuf/proto"
)

func TestApproverTransactionFailureIsNotAnAuthorizationDenial(t *testing.T) {
	ts := newTestServer(t)
	tx, err := ts.srv.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := checkApprover(context.Background(), tx, &pb.PairApproval{}); !errors.Is(err, sql.ErrTxDone) {
		t.Fatalf("database failure was classified as authorization denial: %v", err)
	}
}

// A revoked device is adversarial: it keeps the account root and can make
// arbitrarily many fresh device identities. Tombstoning its old ID is not enough.
func TestRemovedDeviceCannotRegisterNewDeviceKey(t *testing.T) {
	ts := newTestServer(t)
	owner, ownerID, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	if err != nil {
		t.Fatal(err)
	}
	_, removedID, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	if err != nil {
		t.Fatal(err)
	}
	admin := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, owner, ownerID)))
	if _, err := admin.RemoveDevice(context.Background(), connect.NewRequest(&pb.RemoveDeviceRequest{DeviceId: removedID})); err != nil {
		t.Fatal(err)
	}
	pub, _, _ := ed25519.GenerateKey(nil)
	raw, _ := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: ts.keys.AccountPublicKey(), DevicePublicKey: pub, Kind: pb.DeviceKind_DEVICE_KIND_CLIENT, CreatedMs: time.Now().UnixMilli()})
	_, err = ts.auth.RegisterDevice(context.Background(), connect.NewRequest(&pb.RegisterDeviceRequest{Registration: raw, Signature: ts.keys.SignRegistration(raw), SetupToken: "setup"}))
	if connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("removed root holder minted a fresh identity: %v", err)
	}
}

func candidateRequest(t *testing.T, ts *testServer) *pb.RegisterDeviceRequest {
	t.Helper()
	pub, _, _ := ed25519.GenerateKey(nil)
	raw, _ := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: ts.keys.AccountPublicKey(), DevicePublicKey: pub, Kind: pb.DeviceKind_DEVICE_KIND_CLIENT, CreatedMs: time.Now().UnixMilli()})
	return &pb.RegisterDeviceRequest{Registration: raw, Signature: ts.keys.SignRegistration(raw)}
}

func TestEnrollmentApprovalCannotBeSubstitutedOrUsedAfterRevocation(t *testing.T) {
	for _, scenario := range []string{"missing", "signature", "candidate", "kind", "account", "expired", "canceled", "approver-removed", "different-device-removed"} {
		t.Run(scenario, func(t *testing.T) {
			ts := newTestServer(t)
			_, ownerID, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
			if err != nil {
				t.Fatal(err)
			}
			_, otherID, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
			if err != nil {
				t.Fatal(err)
			}
			req := candidateRequest(t, ts)
			ts.admitRegistration(t, req)
			a, _ := parseApproval(req.Approval, req.ApprovalSignature)
			switch scenario {
			case "missing":
				req.Approval, req.ApprovalSignature = nil, nil
			case "signature":
				req.ApprovalSignature[0] ^= 1
			case "candidate":
				replacement := candidateRequest(t, ts)
				req.Registration, req.Signature = replacement.Registration, replacement.Signature
			case "kind":
				var r pb.DeviceRegistration
				_ = proto.Unmarshal(req.Registration, &r)
				r.Kind = pb.DeviceKind_DEVICE_KIND_MACHINE
				req.Registration, _ = proto.Marshal(&r)
				req.Signature = ts.keys.SignRegistration(req.Registration)
			case "account":
				keys, _ := e2e.Derive(e2e.NewKey())
				var r pb.DeviceRegistration
				_ = proto.Unmarshal(req.Registration, &r)
				r.AccountPublicKey = keys.AccountPublicKey()
				req.Registration, _ = proto.Marshal(&r)
				req.Signature = keys.SignRegistration(req.Registration)
				req.SetupToken = "" // Cannot use an approval to bootstrap another account.
			case "expired":
				_, err = ts.srv.store.db.Exec(`UPDATE pairing_admissions SET expires_ms = 1 WHERE pairing_id = ?`, a.info.PairingId)
			case "canceled":
				err = ts.srv.store.cancelAdmission(context.Background(), a.info.PairingId)
			case "approver-removed":
				_, err = ts.tenant().removeDevice(context.Background(), a.info.ApproverDeviceId)
			case "different-device-removed":
				target := ownerID
				if target == a.info.ApproverDeviceId {
					target = otherID
				}
				_, err = ts.tenant().removeDevice(context.Background(), target)
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = ts.auth.RegisterDevice(context.Background(), connect.NewRequest(req))
			if connect.CodeOf(err) != connect.CodePermissionDenied {
				t.Fatalf("invalid admission accepted: %v", err)
			}
		})
	}
}

func TestEnrollmentResponseLossRetryRequiresExactRegistration(t *testing.T) {
	ts := newTestServer(t)
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup"); err != nil {
		t.Fatal(err)
	}
	req := candidateRequest(t, ts)
	ts.admitRegistration(t, req)
	for range 2 {
		if _, err := ts.auth.RegisterDevice(context.Background(), connect.NewRequest(req)); err != nil {
			t.Fatal(err)
		}
	}
	var r pb.DeviceRegistration
	_ = proto.Unmarshal(req.Registration, &r)
	r.CreatedMs++
	req.Registration, _ = proto.Marshal(&r)
	req.Signature = ts.keys.SignRegistration(req.Registration)
	if _, err := ts.auth.RegisterDevice(context.Background(), connect.NewRequest(req)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("nonidentical retry accepted: %v", err)
	}
}

func TestAdmissionConsumptionRollsBackAndSurvivesReopen(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup"); err != nil {
		t.Fatal(err)
	}
	req := candidateRequest(t, ts)
	ts.admitRegistration(t, req)
	a, _ := parseApproval(req.Approval, req.ApprovalSignature)
	var r pb.DeviceRegistration
	_ = proto.Unmarshal(req.Registration, &r)
	id := e2e.DeviceID(r.DevicePublicKey)
	quota := errors.New("synthetic quota")
	if err := ts.srv.store.addDevice(ctx, ts.acct(), nil, id, &r, req.Registration, req.Signature, func(accountUsage) error { return quota }, a); !errors.Is(err, quota) {
		t.Fatal(err)
	}
	reopened, err := openStore(ts.srv.cfg.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.addDevice(ctx, ts.acct(), nil, id, &r, req.Registration, req.Signature, nil, a); err != nil {
		t.Fatalf("grant lost or burned by failed insertion: %v", err)
	}
}

func TestCancelAndRegistrationHaveOneTransactionOutcome(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup"); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		req := candidateRequest(t, ts)
		ts.admitRegistration(t, req)
		a, _ := parseApproval(req.Approval, req.ApprovalSignature)
		var registerErr, cancelErr error
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Go(func() { <-start; _, registerErr = ts.auth.RegisterDevice(ctx, connect.NewRequest(req)) })
		wg.Go(func() { <-start; cancelErr = ts.srv.store.cancelAdmission(ctx, a.info.PairingId) })
		close(start)
		wg.Wait()
		if registerErr == nil && cancelErr == nil {
			t.Fatal("reported canceled after enrollment committed")
		}
		if registerErr != nil && cancelErr != nil {
			t.Fatalf("neither operation completed: register=%v cancel=%v", registerErr, cancelErr)
		}
	}
}

func TestConcurrentBootstrapCreatesOnlyOneUnapprovedDevice(t *testing.T) {
	ts := newTestServer(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		req := candidateRequest(t, ts)
		req.SetupToken = "setup"
		wg.Go(func() {
			<-start
			_, err := ts.auth.RegisterDevice(context.Background(), connect.NewRequest(req))
			results <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Fatal(err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("bootstrap admitted %d first devices", succeeded)
	}
}

func TestPersistedAdmissionCanBeCanceledWithoutEphemeralPairingState(t *testing.T) {
	for _, candidate := range []bool{false, true} {
		t.Run(map[bool]string{false: "approver", true: "candidate"}[candidate], func(t *testing.T) {
			ts := newTestServer(t)
			priv, id, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
			if err != nil {
				t.Fatal(err)
			}
			req := candidateRequest(t, ts)
			ts.admitRegistration(t, req)
			a, _ := parseApproval(req.Approval, req.ApprovalSignature)
			pc := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, priv, id)))
			cancel := &pb.CancelPairingRequest{PairingId: a.info.PairingId}
			if candidate {
				cancel.CandidateToken = "synthetic-candidate-token"
			}
			// The admission exists on disk; the ephemeral pairing was lost at restart.
			if _, err := pc.CancelPairing(context.Background(), connect.NewRequest(cancel)); err != nil {
				t.Fatal(err)
			}
			if _, err := ts.auth.RegisterDevice(context.Background(), connect.NewRequest(req)); connect.CodeOf(err) != connect.CodePermissionDenied {
				t.Fatalf("canceled persisted admission registered: %v", err)
			}
		})
	}
}

func TestUnrelatedCandidateCannotCancelPersistedAdmission(t *testing.T) {
	ts := newTestServer(t)
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup"); err != nil {
		t.Fatal(err)
	}
	req := candidateRequest(t, ts)
	ts.admitRegistration(t, req)
	a, _ := parseApproval(req.Approval, req.ApprovalSignature)
	pc := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, ts.url)
	if _, err := pc.CancelPairing(context.Background(), connect.NewRequest(&pb.CancelPairingRequest{PairingId: a.info.PairingId, CandidateToken: "other-candidate"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("unauthorized cancellation: %v", err)
	}
	if _, err := ts.auth.RegisterDevice(context.Background(), connect.NewRequest(req)); err != nil {
		t.Fatalf("unauthorized request canceled approval: %v", err)
	}
}

func TestSuspensionCancelsUndeliveredApprovalInsteadOfReleasingPayload(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup"); err != nil {
		t.Fatal(err)
	}
	req := candidateRequest(t, ts)
	ts.admitRegistration(t, req)
	a, _ := parseApproval(req.Approval, req.ApprovalSignature)
	ps := ts.srv.pairing
	ps.mu.Lock()
	ps.byID[a.info.PairingId] = &pairing{id: a.info.PairingId, account: ts.acct(), initiator: a.info.ApproverDeviceId, devicePub: a.info.CandidateDevicePublicKey, candidate: hashToken("synthetic-candidate-token"), expires: time.Now().Add(time.Minute), payload: &pb.Envelope{Ciphertext: []byte("synthetic encrypted payload")}}
	ps.mu.Unlock()
	if err := ts.srv.store.setAccountStatus(ctx, ts.acct(), accountSuspended); err != nil {
		t.Fatal(err)
	}
	pc := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, ts.url)
	res, err := pc.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: a.info.PairingId, CandidateToken: "synthetic-candidate-token"}))
	if err != nil || res.Msg.State != pb.PairingState_PAIRING_STATE_CANCELED || res.Msg.Payload != nil {
		t.Fatalf("suspended account released approval: %v %v", res, err)
	}
	if err := ts.srv.store.setAccountStatus(ctx, ts.acct(), accountActive); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.auth.RegisterDevice(ctx, connect.NewRequest(req)); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("canceled approval revived on resume: %v", err)
	}
}

func TestPriorEnrollmentLayoutNamesTheManualUpgradeWithoutChangingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE accounts(id TEXT PRIMARY KEY); INSERT INTO accounts VALUES ('synthetic'); PRAGMA user_version = 3;`); err != nil {
		t.Fatal(err)
	}
	if _, err := openStore(path); err == nil || !strings.Contains(err.Error(), "docs/self-hosting.md#upgrading-device-enrollment-protocol-8") {
		t.Fatalf("no actionable upgrade instruction: %v", err)
	}
	var version, accounts int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if version != 3 || accounts != 1 {
		t.Fatalf("opening an old layout modified it: layout=%d accounts=%d", version, accounts)
	}
}
