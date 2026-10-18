package relay

import (
	"context"
	"crypto/ed25519"
	"path/filepath"
	"testing"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"google.golang.org/protobuf/proto"
)

func initializeTestSecurity(t *testing.T, c *conn) {
	t.Helper()
	pub, ed, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	x, xp, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	m, err := deviceauth.NewMember(pub, xp, deviceauth.Machine)
	if err != nil {
		t.Fatal(err)
	}
	if c.keys == nil {
		c.keys, err = e2e.Derive(e2e.NewKey())
		if err != nil {
			t.Fatal(err)
		}
	}
	if c.acct == nil {
		c.acct = &Account{}
	}
	c.acct.DeviceID = m.ID
	c.acct.DeviceSeed = ed.Seed()
	dir := t.TempDir()
	a, err := deviceauth.InitAuthority(filepath.Join(dir, "authority.db"), e2e.AccountID(c.keys.AccountPublicKey()), m)
	if err != nil {
		t.Fatal(err)
	}
	p, s := a.Snapshot()
	endpoint, err := deviceauth.InitEndpoint(filepath.Join(dir, "endpoint.db"), p, s, m.ID, ed.Seed(), x)
	if err != nil {
		a.Close()
		t.Fatal(err)
	}
	c.security = &securitySession{authority: a, remote: &deviceauth.Remote{Endpoint: endpoint, Exchange: func(_ context.Context, c deviceauth.Capsule) (deviceauth.Capsule, error) { return a.Handle(c) }}}
	t.Cleanup(func() { c.security.Close() })
}
func openTestSecureMessage(c *conn, seed []byte, env *pb.Envelope, want *pb.Header, m proto.Message) (*pb.Header, error) {
	w, err := deviceauth.FromEnvelope(env)
	if err != nil {
		return nil, err
	}
	h, plain, err := c.security.remote.Endpoint.Open(w, seed)
	if err != nil {
		return nil, err
	}
	if err = e2e.Expect(h.Projection(), want); err != nil {
		return nil, err
	}
	plain, err = deviceauth.Unpack(h, plain)
	if err != nil {
		return nil, err
	}
	return h.Projection(), proto.Unmarshal(plain, m)
}
