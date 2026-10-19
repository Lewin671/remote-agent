package server

import (
	"context"
	"crypto/ed25519"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

func TestRejoiningMachineTransfersItsRetainedSession(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	oldKey, old, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.putSession(ctx, old, "retained-session", []byte("wrapped-key"), []byte("metadata"), 7, 0, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.tenant().putImage(ctx, old, "retained-session", "image", []byte("ciphertext"), time.Hour, 1000, 0); err != nil {
		t.Fatal(err)
	}
	if err := ts.removeViaState(t, old); err != nil {
		t.Fatal(err)
	}
	c := ts.authority(t)
	pub, _, _ := ed25519.GenerateKey(nil)
	raw, _, err := e2e.NewPairApproval(ts.acct(), randomHex(16), c.phoneEd, pub, pb.DeviceKind_DEVICE_KIND_MACHINE, time.Now().Add(time.Minute).UnixMilli())
	if err != nil {
		t.Fatal(err)
	}
	// Field 9 is the authority-signed replacement binding; use the wire form
	// so this regression executes against the pre-change protocol as well.
	raw = protowire.AppendTag(raw, 9, protowire.BytesType)
	raw = protowire.AppendString(raw, old)
	sig := ed25519.Sign(c.phoneEd, append([]byte(e2e.PairApprovalContext), raw...))
	reg, _ := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: ts.keys.AccountPublicKey(), DevicePublicKey: pub, Kind: pb.DeviceKind_DEVICE_KIND_MACHINE, CreatedMs: time.Now().UnixMilli()})
	req := &pb.RegisterDeviceRequest{ProtocolVersion: protocol.Version, Registration: reg, Approval: raw, ApprovalSignature: sig}
	res, err := ts.auth.RegisterDevice(ctx, connect.NewRequest(req))
	if err != nil {
		t.Fatal(err)
	}
	newID := res.Msg.DeviceId
	if _, err := ts.putSession(ctx, newID, "retained-session", []byte("new-key"), []byte("new-meta"), 8, 0, nil); err != nil {
		t.Fatalf("rejoined computer cannot continue its own session: %v", err)
	}
	for _, table := range []string{"sessions", "images"} {
		var owner string
		if err := ts.srv.store.db.QueryRow("SELECT machine_id FROM "+table+" WHERE account_id = ? AND session_id = ?", ts.acct(), "retained-session").Scan(&owner); err != nil || owner != newID {
			t.Fatalf("%s owner=%s err=%v", table, owner, err)
		}
	}
	if _, err := ts.auth.RegisterDevice(ctx, connect.NewRequest(req)); err != nil {
		t.Fatalf("lost response retry: %v", err)
	}
	ch, err := ts.auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: old}))
	if err == nil {
		_, err = ts.auth.Login(ctx, connect.NewRequest(&pb.LoginRequest{ProtocolVersion: protocol.Version, DeviceId: old, Challenge: ch.Msg.Challenge, Signature: e2e.SignLogin(oldKey, ch.Msg.Challenge)}))
	}
	if err == nil {
		t.Fatal("revoked identity restored")
	}
}

func TestReplacementAdmissionRejectsUnsafeSourcesAndRollsBack(t *testing.T) {
	for _, scenario := range []string{"current", "wrong-account", "not-revoked", "current-with-tombstone", "deleted-records", "client-candidate", "quota"} {
		t.Run(scenario, func(t *testing.T) {
			ts := newTestServer(t)
			ctx := context.Background()
			_, old, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ts.putSession(ctx, old, "retained", []byte("key"), nil, 1, 0, nil); err != nil {
				t.Fatal(err)
			}
			if scenario != "current" && scenario != "current-with-tombstone" {
				if err := ts.removeViaState(t, old); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "wrong-account":
				_, err = ts.srv.store.db.Exec(`UPDATE revoked_devices SET account_id = 'other-account' WHERE id = ?`, old)
			case "not-revoked":
				_, err = ts.srv.store.db.Exec(`DELETE FROM revoked_devices WHERE id = ?`, old)
			case "current-with-tombstone":
				_, err = ts.srv.store.db.Exec(`INSERT INTO revoked_devices (id,account_id,revoked_ms) VALUES (?,?,1)`, old, ts.acct())
			case "deleted-records":
				err = ts.tenant().deleteMachineRecords(ctx, old)
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "current-with-tombstone" {
				if _, err := ts.srv.store.db.Exec(`DELETE FROM devices WHERE id = ?`, old); err != nil {
					t.Fatal(err)
				}
			}
			pub, _, _ := ed25519.GenerateKey(nil)
			kind := pb.DeviceKind_DEVICE_KIND_MACHINE
			if scenario == "client-candidate" {
				kind = pb.DeviceKind_DEVICE_KIND_CLIENT
			}
			c := ts.authority(t)
			raw, _, err := e2e.NewPairApproval(ts.acct(), randomHex(16), c.phoneEd, pub, kind, time.Now().Add(time.Minute).UnixMilli())
			if err != nil {
				t.Fatal(err)
			}
			raw = protowire.AppendTag(raw, 9, protowire.BytesType)
			raw = protowire.AppendString(raw, old)
			sig := ed25519.Sign(c.phoneEd, append([]byte(e2e.PairApprovalContext), raw...))
			reg := &pb.DeviceRegistration{AccountPublicKey: ts.keys.AccountPublicKey(), DevicePublicKey: pub, Kind: kind, CreatedMs: time.Now().UnixMilli()}
			registration, _ := proto.Marshal(reg)
			request := &pb.RegisterDeviceRequest{ProtocolVersion: protocol.Version, Registration: registration, Approval: raw, ApprovalSignature: sig}
			if scenario == "quota" {
				a, err := parseApproval(raw, sig)
				if err != nil {
					t.Fatal(err)
				}
				err = ts.srv.store.addDevice(ctx, ts.acct(), nil, e2e.DeviceID(pub), reg, registration, []byte{}, func(u accountUsage) error {
					if u.machines != 1 || u.sessions != 1 {
						t.Fatalf("transfer duplicated quota: %+v", u)
					}
					return errNoRoom
				}, a)
				if err == nil {
					t.Fatal("quota failure ignored")
				}
				var owner string
				if err := ts.srv.store.db.QueryRow(`SELECT machine_id FROM sessions WHERE account_id = ? AND session_id = 'retained'`, ts.acct()).Scan(&owner); err != nil || owner != old {
					t.Fatalf("failed registration moved archive: %s %v", owner, err)
				}
				if _, err := ts.auth.RegisterDevice(ctx, connect.NewRequest(request)); err != nil {
					t.Fatalf("rollback consumed authorization: %v", err)
				}
			} else if _, err := ts.auth.RegisterDevice(ctx, connect.NewRequest(request)); err == nil {
				t.Fatal("unsafe replacement accepted")
			}
			if scenario != "quota" && scenario != "deleted-records" {
				var owner string
				if err := ts.srv.store.db.QueryRow(`SELECT machine_id FROM sessions WHERE account_id = ? AND session_id = 'retained'`, ts.acct()).Scan(&owner); err != nil || owner != old {
					t.Fatalf("rejected replacement changed owner: %s %v", owner, err)
				}
			}
		})
	}
}
