package relay

import (
	"errors"

	"github.com/Lewin671/remote-agent/internal/deviceauth"
)

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
		Authority: head.Core.AuthorityID, HeadEpoch: head.Core.Epoch, NotAfter: endpoint.LeaseExpiration(),
	}, nil
}
