//go:build dev

package relay

import (
	"context"
	"crypto/ed25519"

	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
)

// DevInit creates a development account with a synthetic client authority and
// enrolls this machine as its first computer. It exists only for the isolated
// development environment and the distribution smoke test, and only in
// binaries built with the dev build tag; release binaries do not contain it.
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
