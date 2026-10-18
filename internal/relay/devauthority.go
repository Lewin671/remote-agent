package relay

import (
	"context"
	"crypto/ed25519"
	"errors"

	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
)

// DevInit creates a development account with a synthetic phone authority and
// enrolls this machine as its first computer. It exists only for the isolated
// development environment and the distribution smoke test; the CLI hides it
// and requires RA_DEV_AUTHORITY=1.
func DevInit(ctx context.Context, server, setupToken string) (*Account, error) {
	authority, err := remoteclient.NewAuthority(ctx, server, setupToken)
	if err != nil {
		return nil, err
	}
	_, ed, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, err
	}
	x, _, err := deviceauth.NewXKey()
	if err != nil {
		return nil, err
	}
	enrolled, err := authority.Enroll(ctx, ed, x, deviceauth.Machine)
	if err != nil {
		return nil, err
	}
	acct := &Account{Server: authority.Server, Root: authority.Root, DeviceSeed: ed.Seed(), DeviceID: enrolled.ID, Security: &SecurityConfig{Pin: authority.Pin, XPrivate: x}}
	if err := enrolled.Endpoint.Save(securityPath()); err != nil {
		return nil, err
	}
	if err := acct.Save(); err != nil {
		return nil, err
	}
	return acct, nil
}

// AccountStatus is what `remote-agent account status` prints.
type AccountStatus struct {
	Server    string
	DeviceID  string
	AccountID string
	Kind      string
	Authority string
	HeadEpoch uint64
	NotAfter  uint64
}

func AccountStatusInfo(a *Account) (AccountStatus, error) {
	var out AccountStatus
	if a.Security == nil {
		return out, errors.New("this machine has no security state; join again from the phone")
	}
	endpoint, err := deviceauth.OpenEndpoint(securityPath())
	if err != nil {
		return out, err
	}
	defer endpoint.Close()
	pin, head, id := endpoint.Snapshot()
	if pin != a.Security.Pin || id != a.DeviceID {
		return out, deviceauth.ErrUntrusted
	}
	m, ok := head.Core.Member(id)
	if !ok {
		return out, deviceauth.ErrUntrusted
	}
	kind := "machine"
	if m.Kind == deviceauth.Client {
		kind = "client"
	}
	return AccountStatus{
		Server: a.Server, DeviceID: id, AccountID: pin.Account, Kind: kind,
		Authority: head.Core.AuthorityID, HeadEpoch: head.Core.Epoch, NotAfter: head.Core.NotAfter,
	}, nil
}
