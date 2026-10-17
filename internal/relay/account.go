// Package relay connects the daemon to remote-agent-server: it registers
// the machine, uploads sealed session events and metadata, and runs sealed
// commands forwarded from clients.
package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/bits"
	"net/http"

	"github.com/Lewin671/remote-agent/internal/netpolicy"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/paths"
	"google.golang.org/protobuf/proto"
)

// Account is this machine's membership in an account, account.json. It
// holds the root key and must stay private to the user.
type Account struct {
	Server string `json:"server"`
	// Root is the account root key.
	Root []byte `json:"root"`
	// DeviceSeed is the seed of this machine's Ed25519 device key.
	DeviceSeed []byte `json:"device_seed"`
	DeviceID   string `json:"device_id"`
	// Web is the web client's address, for pairing links.
	Web string `json:"web,omitempty"`
}

// ErrNoAccount means the machine has not joined an account.
var ErrNoAccount = errors.New("this machine has not joined an account; run `remote-agent account init` or `remote-agent account join`")

// LoadAccount reads account.json.
func LoadAccount() (*Account, error) {
	b, err := os.ReadFile(paths.Account())
	if os.IsNotExist(err) {
		return nil, ErrNoAccount
	}
	if err != nil {
		return nil, err
	}
	a := &Account{}
	if err := json.Unmarshal(b, a); err != nil {
		return nil, fmt.Errorf("%s: %w", paths.Account(), err)
	}
	if len(a.Root) != e2e.KeySize || len(a.DeviceSeed) != ed25519.SeedSize || a.Server == "" {
		return nil, fmt.Errorf("%s is incomplete", paths.Account())
	}
	return a, nil
}

// Save writes account.json.
func (a *Account) Save() error { return a.save() }

func (a *Account) save() error {
	if err := paths.EnsureDir(paths.Dir()); err != nil {
		return err
	}
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	return writeFileSync(paths.Account(), append(b, '\n'))
}

// DeviceKey returns this machine's Ed25519 key.
func (a *Account) DeviceKey() ed25519.PrivateKey { return ed25519.NewKeyFromSeed(a.DeviceSeed) }

// Secret returns what another device needs to join the account.
func (a *Account) Secret() e2e.Secret { return e2e.Secret{Root: a.Root, Server: a.Server} }

// CheckServerURL accepts https URLs, and http only for this computer.
func CheckServerURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return "", fmt.Errorf("server must be a URL like https://ra.example.com")
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

// Join registers this machine with the server, in an account that exists
// already, and saves account.json.
func Join(ctx context.Context, server string, root []byte, setupToken string) (*Account, error) {
	j, err := newJoiner(server, root)
	if err != nil {
		return nil, err
	}
	return j.register(ctx, &pb.RegisterDeviceRequest{SetupToken: setupToken})
}

// CreateAccount creates an account with a new root key on the server and
// registers this machine as its first device. The server says what it
// needs: a relay of one's own asks for its setup token, which askToken
// supplies; a relay open to anyone asks for a proof of work, which is
// solved here.
func CreateAccount(ctx context.Context, server, setupToken string, askToken func() (string, error)) (*Account, error) {
	j, err := newJoiner(server, e2e.NewKey())
	if err != nil {
		return nil, err
	}
	req := &pb.RegisterDeviceRequest{SetupToken: setupToken}
	for asked := false; ; {
		a, err := j.register(ctx, req)
		switch connect.CodeOf(err) {
		case connect.CodePermissionDenied:
			if asked || req.SetupToken != "" || askToken == nil || !strings.Contains(err.Error(), "setup token") {
				return nil, err
			}
			if req.SetupToken, err = askToken(); err != nil {
				return nil, err
			}
			asked = true
		case connect.CodeFailedPrecondition:
			if req.SignupProof != nil {
				return nil, err
			}
			if req.SignupProof, err = j.solveSignup(ctx); err != nil {
				return nil, err
			}
		default:
			return a, err
		}
	}
}

// joiner registers one new device key, retrying with what the server asks for.
type joiner struct {
	server string
	root   []byte
	keys   *e2e.Keys
	pub    ed25519.PublicKey
	priv   ed25519.PrivateKey
	auth   remoteagentv1connect.AuthServiceClient
}

func newJoiner(server string, root []byte) (*joiner, error) {
	if _, err := LoadAccount(); err == nil {
		return nil, fmt.Errorf("this machine already joined an account (%s)", paths.Account())
	}
	server, err := CheckServerURL(server)
	if err != nil {
		return nil, err
	}
	keys, err := e2e.Derive(root)
	if err != nil {
		return nil, err
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, err
	}
	return &joiner{server: server, root: root, keys: keys, pub: pub, priv: priv,
		auth: remoteagentv1connect.NewAuthServiceClient(httpClient(), server)}, nil
}

func (j *joiner) register(ctx context.Context, req *pb.RegisterDeviceRequest) (*Account, error) {
	reg, err := proto.Marshal(&pb.DeviceRegistration{
		AccountPublicKey: j.keys.AccountPublicKey(),
		DevicePublicKey:  j.pub,
		Kind:             pb.DeviceKind_DEVICE_KIND_MACHINE,
		CreatedMs:        time.Now().UnixMilli(),
	})
	if err != nil {
		return nil, err
	}
	req = proto.CloneOf(req)
	req.Registration, req.Signature = reg, j.keys.SignRegistration(reg)
	res, err := j.auth.RegisterDevice(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	if res.Msg.DeviceId != e2e.DeviceID(j.pub) {
		return nil, errors.New("server returned an unexpected device ID")
	}
	a := &Account{Server: j.server, Root: j.root, DeviceSeed: j.priv.Seed(), DeviceID: res.Msg.DeviceId}
	if err := a.save(); err != nil {
		return nil, err
	}
	return a, nil
}

// maxSignupBits bounds the work a relay may ask for: 28 bits take minutes.
const maxSignupBits = 28

// solveSignup gets a signup challenge and finds a nonce for it.
func (j *joiner) solveSignup(ctx context.Context) (*pb.SignupProof, error) {
	res, err := j.auth.GetSignupChallenge(ctx, connect.NewRequest(&pb.GetSignupChallengeRequest{}))
	if err != nil {
		return nil, err
	}
	bits := res.Msg.DifficultyBits
	if bits > maxSignupBits {
		return nil, fmt.Errorf("the relay asks for a proof of work of %d bits, more than %d", bits, maxSignupBits)
	}
	nonce, err := SolveSignup(ctx, res.Msg.Challenge, uint(bits))
	if err != nil {
		return nil, err
	}
	return &pb.SignupProof{Challenge: res.Msg.Challenge, Nonce: nonce}, nil
}

// SolveSignup finds a nonce such that SHA-256(challenge || nonce) starts
// with n zero bits.
func SolveSignup(ctx context.Context, challenge []byte, n uint) ([]byte, error) {
	buf := append(append([]byte{}, challenge...), make([]byte, 8)...)
	nonce := buf[len(challenge):]
	for i := uint64(0); ; i++ {
		if i&0xffff == 0 && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		binary.BigEndian.PutUint64(nonce, i)
		sum := sha256.Sum256(buf)
		if leadingZeroBits(sum[:]) >= n {
			return bytes.Clone(nonce), nil
		}
	}
}

func leadingZeroBits(b []byte) uint {
	n := uint(0)
	for _, c := range b {
		if c != 0 {
			return n + uint(bits.LeadingZeros8(c))
		}
		n += 8
	}
	return n
}

func httpClient() *http.Client {
	return netpolicy.Client(60 * time.Second)
}

// writeFileSync writes a file atomically and durably with owner-only
// permissions.
func writeFileSync(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(path)); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
