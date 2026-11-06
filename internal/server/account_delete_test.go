package server

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/e2e"
)

func deletionProof(t *testing.T, ts *testServer, key ed25519.PrivateKey, id string) *pb.DeleteAccountRequest {
	t.Helper()
	_, head, err := ts.tenant().securityHead(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ch, err := ts.auth.GetChallenge(context.Background(), connect.NewRequest(&pb.GetChallengeRequest{DeviceId: id}))
	if err != nil {
		t.Fatal(err)
	}
	return &pb.DeleteAccountRequest{StateHash: head[:], Challenge: ch.Msg.Challenge,
		Signature: ed25519.Sign(key, e2e.AccountDeletionMessage(ts.acct(), head[:], ch.Msg.Challenge))}
}

func TestAccountDeletionProofs(t *testing.T) {
	for _, scenario := range []string{"valid", "non-authority", "login-proof", "tampered", "wrong-account", "expired", "wrong-device", "stale-head", "rollback"} {
		t.Run(scenario, func(t *testing.T) {
			ts := newTestServer(t)
			ctx := context.Background()
			cp, cid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
			if err != nil {
				t.Fatal(err)
			}
			authority := ts.authority(t)
			key, id := authority.phoneEd, authority.phoneID
			if scenario == "non-authority" {
				key, id = cp, cid
			}
			token := ts.login(t, key, id)
			client := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(token))
			if scenario == "valid" || scenario == "rollback" {
				// Opaque payloads are deliberately seeded: deletion must remove their
				// bytes without decrypting or depending on the envelope contents.
				for _, sql := range []string{
					`INSERT INTO sessions(account_id,session_id,machine_id,key,updated_ms) VALUES (?,'session','computer',X'01',1)`,
					`INSERT INTO events(account_id,session_id,seq,envelope) VALUES (?,'session',1,X'02')`,
					`INSERT INTO images(account_id,session_id,image_id,machine_id,envelope,bytes,created_ms,expires_ms) VALUES (?,'session','image','computer',X'03',1,1,2)`,
					`INSERT INTO account_prefs(account_id,object_id,revision,envelope,updated_ms) VALUES (?,'prefs',1,X'04',1)`,
					`INSERT INTO task_owned_sessions(account_id,session_id) VALUES (?,'session')`,
					`INSERT INTO fcm_targets(account_id,device_id,token,kinds) VALUES (?,'target','token',1)`,
					`INSERT INTO push_subs(account_id,device_id,subscription,kinds) VALUES (?,'target','sealed',1)`,
				} {
					if _, err := ts.srv.store.db.Exec(sql, ts.acct()); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := ts.srv.store.db.Exec(`INSERT INTO accounts(id,public_key,plan,created_ms) VALUES ('other',X'11','trial',1)`); err != nil {
					t.Fatal(err)
				}
			}
			req := deletionProof(t, ts, key, id)
			switch scenario {
			case "login-proof":
				req.Signature = ed25519.Sign(key, append([]byte("remote-agent/v1/login\x00"), req.Challenge...))
			case "tampered":
				req.Signature[0] ^= 1
			case "wrong-account":
				req.Signature = ed25519.Sign(key, e2e.AccountDeletionMessage("another-account", req.StateHash, req.Challenge))
			case "expired":
				ts.srv.challengeMu.Lock()
				ch := ts.srv.challenges[string(req.Challenge)]
				ch.expires = time.Now().Add(-time.Second)
				ts.srv.challenges[string(req.Challenge)] = ch
				ts.srv.challengeMu.Unlock()
			case "wrong-device":
				ch, err := ts.auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: cid}))
				if err != nil {
					t.Fatal(err)
				}
				req.Challenge = ch.Msg.Challenge
				req.Signature = ed25519.Sign(key, e2e.AccountDeletionMessage(ts.acct(), req.StateHash, req.Challenge))
			case "stale-head":
				if _, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, ""); err != nil {
					t.Fatal(err)
				}
			case "rollback":
				if _, err := ts.srv.store.db.Exec(`CREATE TRIGGER block_account_delete BEFORE DELETE ON security_states BEGIN SELECT RAISE(ABORT,'injected deletion failure'); END`); err != nil {
					t.Fatal(err)
				}
			}
			_, err = client.DeleteAccount(ctx, connect.NewRequest(req))
			if scenario != "valid" {
				if err == nil {
					t.Fatal("invalid deletion succeeded")
				}
				if _, err := ts.srv.store.account(ctx, ts.acct()); err != nil {
					t.Fatalf("account lost after rejected deletion: %v", err)
				}
				if _, err := ts.srv.store.tokenDevice(ctx, hashToken(token)); err != nil {
					t.Fatalf("token lost after rejected deletion: %v", err)
				}
				if scenario == "rollback" {
					var n int
					if err := ts.srv.store.db.QueryRow(`SELECT COUNT(*) FROM events WHERE account_id=?`, ts.acct()).Scan(&n); err != nil || n != 1 {
						t.Fatalf("partial deletion after rollback: %d %v", n, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ts.srv.store.account(ctx, ts.acct()); !errors.Is(err, errNoRows) {
				t.Fatalf("account still present: %v", err)
			}
			for _, table := range accountTables {
				var n int
				if err := ts.srv.store.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE account_id=?`, ts.acct()).Scan(&n); err != nil || n != 0 {
					t.Fatalf("%s retains %d rows: %v", table, n, err)
				}
			}
			if _, err := client.DeleteAccount(ctx, connect.NewRequest(req)); err == nil {
				t.Fatal("deleted token or proof was reusable")
			}
			if _, err := ts.srv.store.account(ctx, "other"); err != nil {
				t.Fatalf("another account was affected: %v", err)
			}
			// Writes admitted before deletion must not resurrect routing metadata.
			if err := ts.tenant().addToken(ctx, []byte("late-token"), id, time.Now().Add(time.Hour).UnixMilli()); err == nil {
				t.Fatal("late login resurrected token")
			}
			if _, _, err := ts.tenant().putPrefs(ctx, "late", 0, []byte("sealed"), nil); err == nil {
				t.Fatal("late prefs resurrected account data")
			}
			if err := ts.srv.push.setFCM(ctx, ts.tenant(), id, "late-target", []pb.NotifyKind{pb.NotifyKind_NOTIFY_KIND_SCHEDULED}); err == nil {
				t.Fatal("late push target resurrected account data")
			}
		})
	}
}

// Push-only fixtures do not perform device enrollment, but still own records
// through real account rows, as every authenticated production request does.
func seedPushAccount(t *testing.T, ts *testServer, account string) {
	t.Helper()
	if _, err := ts.srv.store.db.Exec(`INSERT OR IGNORE INTO accounts(id,public_key,plan,created_ms) VALUES (?,?,'trial',1)`, account, []byte("fixture-public-key:"+account)); err != nil {
		t.Fatal(err)
	}
}
