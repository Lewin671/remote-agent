package remoteclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/netpolicy"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"google.golang.org/protobuf/proto"
)

// Authority is a synthetic phone for Go tests and the isolated development
// environment. It holds the account root and the phone's signing key; the
// production phone is the Android app.
type Authority struct {
	Server           string
	Root             []byte
	AccountPublicKey []byte
	Ed               ed25519.PrivateKey
	X                []byte
	Member           deviceauth.Member
	Recovery         ed25519.PrivateKey
	Pin              deviceauth.Pin
	States           []deviceauth.State
	Proofs           []*deviceauth.RecoveryStatement
	Secret           []byte
	Token            string

	api  remoteagentv1connect.ClientServiceClient
	auth remoteagentv1connect.AuthServiceClient
}

// Enrolled is one device that joined the test authority's account.
type Enrolled struct {
	ID       string
	Token    string
	Endpoint *deviceauth.Endpoint
	State    deviceauth.State
}

// NewAuthority creates an account with a phone client as its first device,
// builds the genesis state and pins it on the relay.
func NewAuthority(ctx context.Context, server, setupToken string) (*Authority, error) {
	server, err := checkServer(server)
	if err != nil {
		return nil, err
	}
	hc := netpolicy.Client(time.Minute)
	auth := remoteagentv1connect.NewAuthServiceClient(hc, server)
	root := e2e.NewKey()
	keys, err := e2e.Derive(root)
	if err != nil {
		return nil, err
	}
	_, phoneEd, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, err
	}
	phoneX, phoneXPub, err := deviceauth.NewXKey()
	if err != nil {
		return nil, err
	}
	phone, err := deviceauth.NewMember(phoneEd.Public().(ed25519.PublicKey), phoneXPub, deviceauth.Client)
	if err != nil {
		return nil, err
	}
	reg, err := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: keys.AccountPublicKey(), DevicePublicKey: phoneEd.Public().(ed25519.PublicKey), Kind: pb.DeviceKind_DEVICE_KIND_CLIENT, CreatedMs: time.Now().UnixMilli()})
	if err != nil {
		return nil, err
	}
	res, err := auth.RegisterDevice(ctx, connect.NewRequest(&pb.RegisterDeviceRequest{ProtocolVersion: protocol.Version, Registration: reg, Signature: keys.SignRegistration(reg), SetupToken: setupToken}))
	if err != nil {
		return nil, err
	}
	token, err := loginDevice(ctx, auth, res.Msg.DeviceId, phoneEd)
	if err != nil {
		return nil, err
	}
	_, recovery, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	var recoveryPub [32]byte
	copy(recoveryPub[:], recovery.Public().(ed25519.PublicKey))
	domain := deviceauth.Hash{}
	if _, err := rand.Read(domain[:]); err != nil {
		return nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	genesis, err := deviceauth.BuildGenesis(e2e.AccountID(keys.AccountPublicKey()), domain, phone, recoveryPub, secret, phoneEd, uint64(time.Now().UnixMilli()))
	if err != nil {
		return nil, err
	}
	pin, err := genesis.Pin()
	if err != nil {
		return nil, err
	}
	api := remoteagentv1connect.NewClientServiceClient(hc, server, connect.WithInterceptors(bearerToken(token)))
	raw, _ := genesis.MarshalBinary()
	if _, err := api.PinSecurityDomain(ctx, connect.NewRequest(&pb.PinSecurityDomainRequest{GenesisState: raw})); err != nil {
		return nil, err
	}
	return &Authority{
		Server: server, Root: root, AccountPublicKey: keys.AccountPublicKey(),
		Ed: phoneEd, X: phoneX, Member: phone, Recovery: recovery, Pin: pin,
		States: []deviceauth.State{genesis}, Proofs: []*deviceauth.RecoveryStatement{nil},
		Secret: secret, Token: token, api: api, auth: auth,
	}, nil
}

// Enroll registers a new device with a phone-signed approval, adds it to the
// chain and returns its endpoint and login token.
func (a *Authority) Enroll(ctx context.Context, ed ed25519.PrivateKey, x []byte, kind deviceauth.Kind) (*Enrolled, error) {
	xpub, err := deviceauth.XPublic(x)
	if err != nil {
		return nil, err
	}
	member, err := deviceauth.NewMember(ed.Public().(ed25519.PublicKey), xpub, kind)
	if err != nil {
		return nil, err
	}
	pbKind := pb.DeviceKind_DEVICE_KIND_MACHINE
	if kind == deviceauth.Client {
		pbKind = pb.DeviceKind_DEVICE_KIND_CLIENT
	}
	approval, sig, err := e2e.NewPairApproval(a.Pin.Account, randomID(), a.Ed, member.EdPublic[:], pbKind, time.Now().Add(time.Minute).UnixMilli())
	if err != nil {
		return nil, err
	}
	reg, err := proto.Marshal(&pb.DeviceRegistration{AccountPublicKey: a.AccountPublicKey, DevicePublicKey: member.EdPublic[:], Kind: pbKind, CreatedMs: time.Now().UnixMilli()})
	if err != nil {
		return nil, err
	}
	res, err := a.auth.RegisterDevice(ctx, connect.NewRequest(&pb.RegisterDeviceRequest{ProtocolVersion: protocol.Version, Registration: reg, Approval: approval, ApprovalSignature: sig}))
	if err != nil {
		return nil, err
	}
	state, err := a.addMember(ctx, member, xpub)
	if err != nil {
		return nil, err
	}
	token, err := loginDevice(ctx, a.auth, res.Msg.DeviceId, ed)
	if err != nil {
		return nil, err
	}
	endpoint, err := deviceauth.InitEndpoint("", a.Pin, a.States, a.Proofs, member.ID, ed.Seed(), x)
	if err != nil {
		return nil, err
	}
	return &Enrolled{ID: res.Msg.DeviceId, Token: token, Endpoint: endpoint, State: state}, nil
}

func (a *Authority) addMember(ctx context.Context, member deviceauth.Member, xpub []byte) (deviceauth.State, error) {
	newSecret := make([]byte, 32)
	if _, err := rand.Read(newSecret); err != nil {
		return deviceauth.State{}, err
	}
	parent := a.States[len(a.States)-1]
	state, err := deviceauth.BuildPairState(parent, member, xpub, a.Secret, newSecret, a.Ed, uint64(time.Now().UnixMilli()))
	if err != nil {
		return deviceauth.State{}, err
	}
	raw, _ := state.MarshalBinary()
	if _, err := a.api.PutSecurityState(ctx, connect.NewRequest(&pb.PutSecurityStateRequest{States: []*pb.SecurityState{{State: raw}}})); err != nil {
		return deviceauth.State{}, err
	}
	a.States = append(a.States, state)
	a.Proofs = append(a.Proofs, nil)
	a.Secret = newSecret
	return state, nil
}

// Client returns a client for an enrolled device.
func (e *Enrolled) Client(a *Authority) *Client {
	hc := netpolicy.Client(time.Minute)
	c := &Client{Server: a.Server, DeviceID: e.ID, token: e.Token, endpoint: e.Endpoint, CommandTTL: time.Minute}
	c.api = remoteagentv1connect.NewClientServiceClient(hc, a.Server, connect.WithInterceptors(bearerToken(e.Token)))
	return c
}

func loginDevice(ctx context.Context, auth remoteagentv1connect.AuthServiceClient, id string, ed ed25519.PrivateKey) (string, error) {
	ch, err := auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: id}))
	if err != nil {
		return "", err
	}
	res, err := auth.Login(ctx, connect.NewRequest(&pb.LoginRequest{ProtocolVersion: protocol.Version, DeviceId: id, Challenge: ch.Msg.Challenge, Signature: e2e.SignLogin(ed, ch.Msg.Challenge)}))
	if err != nil {
		return "", err
	}
	return res.Msg.Token, nil
}

func bearerToken(token string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	}
}

// checkServer accepts https, and http only for a loopback development relay.
func checkServer(raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("server must be a URL like https://ra.example.com")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if h := u.Hostname(); h != "localhost" && h != "127.0.0.1" && h != "::1" {
			return "", errors.New("the server must use https")
		}
	default:
		return "", errors.New("the server must use https")
	}
	return u.String(), nil
}

func randomID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
