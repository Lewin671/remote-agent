package server

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

// TestUpgradedAccountCannotPinOverTheNetwork is finding S11: the 5→6
// migration marks existing accounts, and only an offline pin may create
// their genesis. Fresh accounts are not marked.
func TestUpgradedAccountCannotPinOverTheNetwork(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	priv, id, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ts.srv.store.db.ExecContext(ctx, `DELETE FROM security_states WHERE account_id = ?`, ts.acct()); err != nil {
		t.Fatal(err)
	}
	if _, err := ts.srv.store.db.ExecContext(ctx, `UPDATE accounts SET security_genesis = NULL, recovery_ed = NULL, offline_pin = 1 WHERE id = ?`, ts.acct()); err != nil {
		t.Fatal(err)
	}
	_, xpub, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	m, err := deviceauth.NewMember(priv.Public().(ed25519.PublicKey), xpub, deviceauth.Client)
	if err != nil {
		t.Fatal(err)
	}
	var recovery [32]byte
	recovery[0] = 5
	var domain deviceauth.Hash
	domain[0] = 6
	secret := make([]byte, 32)
	secret[0] = 7
	genesis, err := deviceauth.BuildGenesis(ts.acct(), domain, m, recovery, secret, priv, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := genesis.MarshalBinary()
	api := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken(ts.login(t, priv, id)))
	req := connect.NewRequest(&pb.PinSecurityDomainRequest{GenesisState: raw})
	_, err = api.PinSecurityDomain(ctx, req)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("a marked account pinned over the network: %v", err)
	}
	var stored []byte
	if err := ts.srv.store.db.QueryRowContext(ctx, `SELECT COALESCE(security_genesis, x'') FROM accounts WHERE id = ?`, ts.acct()).Scan(&stored); err != nil || len(stored) != 0 {
		t.Fatalf("the refused pin was stored: %x %v", stored, err)
	}
	// The same call succeeds when the account is not marked (a fresh account).
	if _, err := ts.srv.store.db.ExecContext(ctx, `UPDATE accounts SET offline_pin = 0 WHERE id = ?`, ts.acct()); err != nil {
		t.Fatal(err)
	}
	if _, err := api.PinSecurityDomain(ctx, req); err != nil {
		t.Fatalf("a fresh account was refused a network pin: %v", err)
	}
}

// TestSeededGenesisMustBelongToItsAccount is finding N17: a pinned genesis
// whose account does not match the row must not be seeded.
func TestSeededGenesisMustBelongToItsAccount(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	priv, _, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "setup")
	if err != nil {
		t.Fatal(err)
	}
	other := "fedcba9876543210fedcba9876543210"
	otherKey := make([]byte, 32)
	otherKey[0] = 0xee
	if _, err := ts.srv.store.db.ExecContext(ctx, `INSERT INTO accounts (id, public_key, plan, created_ms) VALUES (?, ?, 'owner', 0)`, other, otherKey); err != nil {
		t.Fatal(err)
	}
	_, xpub, _ := deviceauth.NewXKey()
	m, err := deviceauth.NewMember(priv.Public().(ed25519.PublicKey), xpub, deviceauth.Client)
	if err != nil {
		t.Fatal(err)
	}
	var recovery [32]byte
	recovery[0] = 1
	var domain deviceauth.Hash
	domain[0] = 2
	secret := make([]byte, 32)
	secret[0] = 3
	genesis, err := deviceauth.BuildGenesis(ts.acct(), domain, m, recovery, secret, priv, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := genesis.MarshalBinary()
	if _, err := ts.srv.store.db.ExecContext(ctx, `UPDATE accounts SET security_genesis = ? WHERE id = ?`, raw, other); err != nil {
		t.Fatal(err)
	}
	if err := ts.srv.seedSecurityChains(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := ts.srv.store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM security_states WHERE account_id = ?`, other).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a genesis of another account was seeded: %d %v", n, err)
	}
}
