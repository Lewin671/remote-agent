package relay

import (
	"crypto/ed25519"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"google.golang.org/protobuf/proto"
)

// testSecurityKeys is the fixture chain's material a test needs to build a
// further signed state.
type testSecurityKeys struct {
	phone    ed25519.PrivateKey
	recovery ed25519.PrivateKey
	machine  deviceauth.Member
	secret   []byte
	states   []deviceauth.State
}

// initializeTestSecurity gives the test conn a local v3 chain with a phone
// authority and this machine as a member.
func initializeTestSecurity(t *testing.T, c *conn) {
	initializeTestSecurityKeys(t, c)
}

func initializeTestSecurityKeys(t *testing.T, c *conn) *testSecurityKeys {
	t.Helper()
	_, phoneEd, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	_, phoneXPub, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	phone, err := deviceauth.NewMember(phoneEd.Public().(ed25519.PublicKey), phoneXPub, deviceauth.Client)
	if err != nil {
		t.Fatal(err)
	}
	_, ed, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	x, xpub, err := deviceauth.NewXKey()
	if err != nil {
		t.Fatal(err)
	}
	machine, err := deviceauth.NewMember(ed.Public().(ed25519.PublicKey), xpub, deviceauth.Machine)
	if err != nil {
		t.Fatal(err)
	}
	_, rec, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	var recPub [32]byte
	copy(recPub[:], rec.Public().(ed25519.PublicKey))
	secret1 := make([]byte, 32)
	secret1[0] = 1
	var domain deviceauth.Hash
	domain[0] = 1
	now := uint64(time.Now().UnixMilli())
	genesis, err := deviceauth.BuildGenesis("0123456789abcdef0123456789abcdef", domain, phone, recPub, secret1, phoneEd, now)
	if err != nil {
		t.Fatal(err)
	}
	secret2 := make([]byte, 32)
	secret2[0] = 2
	withMachine, err := deviceauth.BuildPairState(genesis, machine, xpub, secret1, secret2, phoneEd, now+1000)
	if err != nil {
		t.Fatal(err)
	}
	pin, _ := genesis.Pin()
	states := []deviceauth.State{genesis, withMachine}
	lease, _ := deviceauth.BuildLease(withMachine, phoneEd, withMachine.Core.IssuedAt)
	endpoint, err := deviceauth.InitEndpoint("", pin, states, []*deviceauth.RecoveryStatement{nil, nil}, machine.ID, ed.Seed(), x, lease)
	if err != nil {
		t.Fatal(err)
	}
	if c.acct == nil {
		c.acct = &Account{}
	}
	c.acct.DeviceID = machine.ID
	c.acct.DeviceSeed = ed.Seed()
	c.security = &securitySession{endpoint: endpoint, syncNow: make(chan struct{}, 1)}
	t.Cleanup(func() { endpoint.Close() })
	return &testSecurityKeys{phone: phoneEd, recovery: rec, machine: machine, secret: secret2, states: states}
}

func openTestSecureMessage(c *conn, seed []byte, env *pb.Envelope, want *pb.Header, m proto.Message) (*pb.Header, error) {
	w, err := deviceauth.FromEnvelope(env)
	if err != nil {
		return nil, err
	}
	h, plain, err := c.security.endpoint.Open(w, seed, true, uint64(time.Now().UnixMilli()))
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
