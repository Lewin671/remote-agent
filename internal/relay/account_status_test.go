package relay

import (
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"testing"
)

func TestAccountStatusReportsInstalledLease(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	c := &conn{}
	keys := initializeTestSecurityKeys(t, c)
	pin, head, id := c.security.endpoint.Snapshot()
	cert, err := deviceauth.BuildLease(head, keys.phone, head.Core.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := deviceauth.InitEndpoint(securityPath(), pin, keys.states, []*deviceauth.RecoveryStatement{nil, nil}, id, c.acct.DeviceSeed, keys.x, cert)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.Close()
	c.acct.Security = &SecurityConfig{Pin: pin, XPrivate: keys.x}
	st, err := AccountStatusInfo(c.acct)
	if err != nil {
		t.Fatal(err)
	}
	if st.NotAfter != cert.NotAfter {
		t.Fatalf("lease expiration=%d want %d", st.NotAfter, cert.NotAfter)
	}
}
