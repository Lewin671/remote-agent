package relay

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/paths"
	"google.golang.org/protobuf/proto"
)

// SecurityConfig is initialized locally or after SAS verification. It is never
// reconstructed from root possession, a relay list or a missing security DB.
type SecurityConfig struct {
	Pin       deviceauth.Pin `json:"pin"`
	XPrivate  []byte         `json:"x_private"`
	Authority bool           `json:"authority"`
}

func securityPath() string  { return filepath.Join(paths.Dir(), "security-v2.db") }
func authorityPath() string { return filepath.Join(paths.Dir(), "authority-v2.db") }

// InitSecurityAuthority preserves the existing account and signing identity.
// On an existing account this is an explicit one-time local setup command.
func (a *Account) InitSecurityAuthority() error {
	if a.Security != nil {
		return deviceauth.ErrConflict
	}
	if err := paths.EnsureDir(paths.Dir()); err != nil {
		return err
	}
	keys, err := e2e.Derive(a.Root)
	if err != nil {
		return err
	}
	x, xpub, err := deviceauth.NewXKey()
	if err != nil {
		return err
	}
	m, err := deviceauth.NewMember(a.DeviceKey().Public().(ed25519.PublicKey), xpub, deviceauth.Machine)
	if err != nil {
		return err
	}
	authority, err := deviceauth.InitAuthority(authorityPath(), e2e.AccountID(keys.AccountPublicKey()), m)
	if err != nil {
		return err
	}
	defer authority.Close()
	pin, state := authority.Snapshot()
	endpoint, err := deviceauth.InitEndpoint(securityPath(), pin, state, a.DeviceID, a.DeviceSeed, x)
	if err != nil {
		return err
	}
	if err = endpoint.Close(); err != nil {
		return err
	}
	a.Security = &SecurityConfig{Pin: pin, XPrivate: x, Authority: true}
	return a.Save()
}

// PublicSecurityGenesis exports only the signed public bootstrap state, never
// account secrets or authority private keys. Stop the daemon before calling it.
func (a *Account) PublicSecurityGenesis() ([]byte, error) {
	if a.Security == nil || !a.Security.Authority {
		return nil, deviceauth.ErrUntrusted
	}
	authority, err := deviceauth.OpenAuthority(authorityPath())
	if err != nil {
		return nil, err
	}
	defer authority.Close()
	pin, _ := authority.Snapshot()
	if pin != a.Security.Pin {
		return nil, deviceauth.ErrUntrusted
	}
	states, err := authority.StatePage(0)
	if err != nil {
		return nil, err
	}
	if len(states) == 0 || pin.Verify(states[0]) != nil || states[0].Core.Epoch != 1 {
		return nil, deviceauth.ErrUntrusted
	}
	return states[0].MarshalBinary()
}

type securitySession struct {
	remote    *deviceauth.Remote
	authority *deviceauth.Authority
}

func (s *securitySession) Close() {
	s.remote.Endpoint.Close()
	if s.authority != nil {
		s.authority.Close()
	}
}
func openSecuritySession(acct *Account, client remoteagentv1connect.ClientServiceClient) (*securitySession, error) {
	if acct.Security == nil {
		return nil, errors.New("remote security is not initialized; run `remote-agent account security-init` on the trusted management computer, then pair retained devices")
	}
	endpoint, err := deviceauth.OpenEndpoint(securityPath())
	if err != nil {
		return nil, err
	}
	pin, _, id := endpoint.Snapshot()
	if pin != acct.Security.Pin || id != acct.DeviceID {
		endpoint.Close()
		return nil, deviceauth.ErrUntrusted
	}
	s := &securitySession{}
	if acct.Security.Authority {
		s.authority, err = deviceauth.OpenAuthority(authorityPath())
		if err != nil {
			endpoint.Close()
			return nil, err
		}
		ap, _ := s.authority.Snapshot()
		if ap != pin {
			s.authority.Close()
			endpoint.Close()
			return nil, deviceauth.ErrUntrusted
		}
	}
	exchange := func(ctx context.Context, c deviceauth.Capsule) (deviceauth.Capsule, error) {
		if s.authority != nil && c.To == acct.DeviceID {
			return s.authority.Handle(c)
		}
		b, err := c.MarshalBinary()
		if err != nil {
			return deviceauth.Capsule{}, err
		}
		reply, err := client.Forward(ctx, connect.NewRequest(&pb.ForwardRequest{MachineId: c.To, SecurityRpc: b}))
		if err != nil {
			return deviceauth.Capsule{}, err
		}
		if reply.Msg.Result != nil {
			return deviceauth.Capsule{}, deviceauth.ErrUntrusted
		}
		return deviceauth.ParseCapsule(reply.Msg.SecurityRpc)
	}
	s.remote = &deviceauth.Remote{Endpoint: endpoint, Exchange: exchange}
	return s, nil
}
func securityHeader(h *pb.Header, purpose deviceauth.Purpose) deviceauth.Header {
	return deviceauth.Header{Kind: uint32(h.Kind), Purpose: purpose, Machine: h.MachineId, Session: h.SessionId, Request: h.RequestId, Seq: h.Seq, Deadline: uint64(h.DeadlineMs)}
}
func (s *securitySession) seal(ctx context.Context, h *pb.Header, purpose deviceauth.Purpose, seed, plain []byte) (*pb.Envelope, error) {
	var wire deviceauth.Wire
	var err error
	if purpose.Mutable() {
		wire, err = s.remote.SealMutable(ctx, securityHeader(h, purpose), plain)
	} else {
		wire, err = s.remote.Seal(ctx, securityHeader(h, purpose), seed, plain)
	}
	if err != nil {
		return nil, err
	}
	return wire.Envelope()
}
func (s *securitySession) sealMessage(ctx context.Context, h *pb.Header, purpose deviceauth.Purpose, seed []byte, m proto.Message) (*pb.Envelope, error) {
	b, err := proto.Marshal(m)
	if err != nil {
		return nil, err
	}
	return s.seal(ctx, h, purpose, seed, b)
}

func (c *conn) handleForward(ctx context.Context, call *pb.ForwardCall) {
	if len(call.SecurityRpc) == 0 {
		c.handleCall(ctx, call)
		return
	}
	fail := func() { c.reply(ctx, &pb.ForwardReply{CallId: call.CallId, Error: "security RPC refused"}) }
	if call.Command != nil || c.security == nil || c.security.authority == nil {
		fail()
		return
	}
	req, err := deviceauth.ParseCapsule(call.SecurityRpc)
	if err != nil {
		fail()
		return
	}
	reply, err := c.security.authority.Handle(req)
	if err != nil {
		fail()
		return
	}
	raw, err := reply.MarshalBinary()
	if err != nil {
		fail()
		return
	}
	c.reply(ctx, &pb.ForwardReply{CallId: call.CallId, SecurityRpc: raw})
}

func headerOfSecurity(env *pb.Envelope) *pb.Header {
	w, err := deviceauth.FromEnvelope(env)
	if err != nil {
		return &pb.Header{}
	}
	h, err := deviceauth.ParseHeader(w.Header)
	if err != nil {
		return &pb.Header{}
	}
	return h.Projection()
}

// callAuthority is for explicit local setup/pairing commands. It uses this
// device's own independently stored recipient key and the already pinned public
// authority; it does not open the running daemon's exclusive security store.
func (a *Account) callAuthority(ctx context.Context, method deviceauth.Method, body, out any) error {
	if a.Security == nil {
		return deviceauth.ErrStorage
	}
	token, err := login(ctx, a)
	if err != nil {
		return err
	}
	client := remoteagentv1connect.NewClientServiceClient(httpClient(), a.Server, connect.WithInterceptors(bearer(token)))
	capsule, waiter, err := deviceauth.Request(a.Security.Pin, a.DeviceKey(), method, body)
	if err != nil {
		return err
	}
	raw, err := capsule.MarshalBinary()
	if err != nil {
		return err
	}
	reply, err := client.Forward(ctx, connect.NewRequest(&pb.ForwardRequest{MachineId: a.Security.Pin.AuthorityID, SecurityRpc: raw}))
	if err != nil {
		return err
	}
	response, err := deviceauth.ParseCapsule(reply.Msg.SecurityRpc)
	if err != nil {
		return err
	}
	return waiter.Reply(response, a.Security.XPrivate, out)
}

// Idle executors participate too: membership changes cannot depend on a user
// sending another command or on a session producing another event.
func (c *conn) securitySyncLoop(ctx context.Context) error {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			attempt, cancel := context.WithTimeout(ctx, 15*time.Second)
			err := c.security.remote.Sync(attempt)
			cancel()
			if err != nil {
				return fmt.Errorf("security synchronization: %w", err)
			}
		}
	}
}
