// Package remoteclient is a client of remote-agent-server in Go. It does
// what the web client does, so tests and scripts can drive daemons through
// the server end to end.
package remoteclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Lewin671/remote-agent/internal/netpolicy"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

// Client is a logged-in client device.
type Client struct {
	security *deviceauth.Remote
	Server   string
	DeviceID string
	keys     *e2e.Keys
	token    string
	api      remoteagentv1connect.ClientServiceClient
	// CommandTTL is how long a command stays valid.
	CommandTTL time.Duration
	device     ed25519.PrivateKey
}

// Register enrolls a client using an explicit currently enrolled device key.
// Used by automated end-to-end drivers; the shared account secret is insufficient.
func Register(ctx context.Context, secret e2e.Secret, authorizer ed25519.PrivateKey, pin deviceauth.Pin, authorizerX []byte, statePath string, authorizerExchange deviceauth.Exchange) (*Client, error) {
	keys, err := e2e.Derive(secret.Root)
	if err != nil {
		return nil, err
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return nil, err
	}
	reg, err := proto.Marshal(&pb.DeviceRegistration{
		AccountPublicKey: keys.AccountPublicKey(), DevicePublicKey: pub,
		Kind: pb.DeviceKind_DEVICE_KIND_CLIENT, CreatedMs: time.Now().UnixMilli(),
	})
	if err != nil {
		return nil, err
	}
	hc := netpolicy.Client(time.Minute)
	auth := remoteagentv1connect.NewAuthServiceClient(hc, secret.Server)
	if len(authorizer) != ed25519.PrivateKeySize {
		return nil, errors.New("an enrolled authorizing device is required")
	}
	approverID := e2e.DeviceID(authorizer.Public().(ed25519.PublicKey))
	challenge, err := auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: approverID}))
	if err != nil {
		return nil, err
	}
	logged, err := auth.Login(ctx, connect.NewRequest(&pb.LoginRequest{ProtocolVersion: 9, DeviceId: approverID, Challenge: challenge.Msg.Challenge, Signature: e2e.SignLogin(authorizer, challenge.Msg.Challenge)}))
	if err != nil {
		return nil, err
	}
	pc := remoteagentv1connect.NewPairingServiceClient(hc, secret.Server, connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+logged.Msg.Token)
			return next(ctx, req)
		}
	})))
	x, xpub, err := deviceauth.NewXKey()
	if err != nil {
		return nil, err
	}
	member, err := deviceauth.NewMember(pub, xpub, deviceauth.Client)
	if err != nil {
		return nil, err
	}
	approverAPI := remoteagentv1connect.NewClientServiceClient(hc, secret.Server, connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+logged.Msg.Token)
			return next(ctx, req)
		}
	})))
	exchangeAsApprover := func(method deviceauth.Method, body, out any) error {
		capsule, waiter, err := deviceauth.Request(pin, authorizer, method, body)
		if err != nil {
			return err
		}
		raw, err := capsule.MarshalBinary()
		if err != nil {
			return err
		}
		var response deviceauth.Capsule
		if authorizerExchange != nil {
			response, err = authorizerExchange(ctx, capsule)
		} else {
			var reply *connect.Response[pb.ForwardResponse]
			reply, err = approverAPI.Forward(ctx, connect.NewRequest(&pb.ForwardRequest{MachineId: pin.AuthorityID, SecurityRpc: raw}))
			if err == nil {
				response, err = deviceauth.ParseCapsule(reply.Msg.SecurityRpc)
			}
		}
		if err != nil {
			return err
		}
		// This automated driver was explicitly given the authorizer's signing key
		// and pinned authority out of band. Its RPC replies need that member's X key;
		// the production pairing flow receives this from its own local security store.
		return waiter.Reply(response, authorizerX, out)
	}
	trust, err := deviceauth.PairTrustDescriptor(pin, approverID)
	if err != nil {
		return nil, err
	}
	ephemeral, err := e2e.NewPairKey()
	if err != nil {
		return nil, err
	}
	created, err := pc.CreatePairing(ctx, connect.NewRequest(&pb.CreatePairingRequest{Commit: e2e.PairDomainCommit(ephemeral.Public(), trust)}))
	if err != nil {
		return nil, err
	}
	joinKey, err := e2e.NewPairKey()
	if err != nil {
		return nil, err
	}
	joined, err := pc.JoinPairing(ctx, connect.NewRequest(&pb.JoinPairingRequest{ProtocolVersion: 9, Code: created.Msg.Code, JoinerPublic: joinKey.Public(), JoinerKind: pb.DeviceKind_DEVICE_KIND_CLIENT, DevicePublicKey: pub, DeviceHpkePublicKey: xpub}))
	if err != nil {
		return nil, err
	}
	var pair deviceauth.PairContext
	if err := exchangeAsApprover(deviceauth.BeginPair, deviceauth.PairBegin{ID: created.Msg.PairingId, Candidate: member}, &pair); err != nil {
		return nil, err
	}
	if pair.Verify(pin) != nil {
		return nil, deviceauth.ErrUntrusted
	}
	contextBytes, err := json.Marshal(pair)
	if err != nil {
		return nil, err
	}
	if joined.Msg.PairingId != created.Msg.PairingId || !bytes.Equal(joined.Msg.Commit, e2e.PairDomainCommit(ephemeral.Public(), trust)) {
		return nil, deviceauth.ErrUntrusted
	}
	_, err = pc.RevealPairing(ctx, connect.NewRequest(&pb.RevealPairingRequest{PairingId: created.Msg.PairingId, InitiatorPublic: ephemeral.Public(), SecurityContext: contextBytes}))
	if err != nil {
		return nil, err
	}
	approval, signature, err := e2e.NewPairApproval(e2e.AccountID(keys.AccountPublicKey()), created.Msg.PairingId, authorizer, pub, pb.DeviceKind_DEVICE_KIND_CLIENT, created.Msg.ExpiresMs)
	if err != nil {
		return nil, err
	}
	shared, err := ephemeral.Derive(joinKey.Public(), e2e.PairDomainTranscript(created.Msg.PairingId, ephemeral.Public(), joinKey.Public(), pub, xpub, int32(pb.DeviceKind_DEVICE_KIND_CLIENT), trust))
	if err != nil {
		return nil, err
	}
	as, err := deviceauth.SignPair(pair, authorizer, false)
	if err != nil {
		return nil, err
	}
	cs, err := deviceauth.SignPair(pair, priv, true)
	if err != nil {
		return nil, err
	}
	var joinedState deviceauth.State
	if err := exchangeAsApprover(deviceauth.ApprovePair, deviceauth.PairApprove{ID: pair.ID, ApproverSignature: as, CandidateSignature: cs}, &joinedState); err != nil {
		return nil, err
	}
	joinedBytes, err := joinedState.MarshalBinary()
	if err != nil {
		return nil, err
	}
	payload, err := e2e.SealMessage(shared.Key, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_PAIRING, RequestId: created.Msg.PairingId, Sender: approverID}, &pb.PairingPayload{Root: secret.Root, Server: secret.Server, Approval: approval, ApprovalSignature: signature, SecurityState: joinedBytes})
	if err != nil {
		return nil, err
	}
	_, err = pc.ApprovePairing(ctx, connect.NewRequest(&pb.ApprovePairingRequest{PairingId: created.Msg.PairingId, Payload: payload, Approval: approval, ApprovalSignature: signature}))
	if err != nil {
		return nil, err
	}
	res, err := auth.RegisterDevice(ctx, connect.NewRequest(&pb.RegisterDeviceRequest{ProtocolVersion: 9, Registration: reg, Signature: keys.SignRegistration(reg), Approval: approval, ApprovalSignature: signature}))

	if err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}
	id := res.Msg.DeviceId
	ch, err := auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: id}))
	if err != nil {
		return nil, err
	}
	lr, err := auth.Login(ctx, connect.NewRequest(&pb.LoginRequest{ProtocolVersion: 9, DeviceId: id, Challenge: ch.Msg.Challenge, Signature: e2e.SignLogin(priv, ch.Msg.Challenge)}))
	if err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	c := &Client{Server: secret.Server, DeviceID: id, keys: keys, token: lr.Msg.Token, CommandTTL: time.Minute, device: priv}
	c.api = remoteagentv1connect.NewClientServiceClient(hc, secret.Server, connect.WithInterceptors(connect.UnaryInterceptorFunc(
		func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				req.Header().Set("Authorization", "Bearer "+c.token)
				return next(ctx, req)
			}
		})))
	endpoint, err := deviceauth.InitEndpoint(statePath, pin, joinedState, id, priv.Seed(), x)
	if err != nil {
		return nil, err
	}
	c.security = &deviceauth.Remote{Endpoint: endpoint, Exchange: func(ctx context.Context, capsule deviceauth.Capsule) (deviceauth.Capsule, error) {
		raw, err := capsule.MarshalBinary()
		if err != nil {
			return deviceauth.Capsule{}, err
		}
		res, err := c.api.Forward(ctx, connect.NewRequest(&pb.ForwardRequest{MachineId: capsule.To, SecurityRpc: raw}))
		if err != nil {
			return deviceauth.Capsule{}, err
		}
		return deviceauth.ParseCapsule(res.Msg.SecurityRpc)
	}}
	_, _ = pc.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: created.Msg.PairingId, CandidateToken: joined.Msg.CandidateToken, Received: true}))
	return c, nil
}

// Token is the bearer token, for tests that talk to the server directly.
func (c *Client) Token() string { return c.token }

// API is the raw client service.
func (c *Client) API() remoteagentv1connect.ClientServiceClient { return c.api }

// Machine is a decrypted machine.
type Machine struct {
	ID     string
	Online bool
	Meta   *pb.MachineMeta
}

// Machines lists machines with their metadata opened.
func (c *Client) Machines(ctx context.Context) ([]Machine, error) {
	res, err := c.api.ListMachines(ctx, connect.NewRequest(&pb.ListMachinesRequest{}))
	if err != nil {
		return nil, err
	}
	var out []Machine
	for _, m := range res.Msg.Machines {
		mm := Machine{ID: m.Id, Online: m.Online}
		if m.Meta != nil {
			meta := &pb.MachineMeta{}
			if _, err := c.openMessage(ctx, nil, m.Meta, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_MACHINE_META, MachineId: m.Id}, meta); err != nil {
				return nil, fmt.Errorf("machine %s: %w", m.Id, err)
			}
			mm.Meta = meta
		}
		out = append(out, mm)
	}
	return out, nil
}

// Session is a decrypted session.
type Session struct {
	MachineID string
	ID        string
	Key       []byte
	Info      *pb.Session
	LastSeq   uint64
}

// Sessions lists every session with its key and metadata opened, reading
// the listing page by page.
func (c *Client) Sessions(ctx context.Context) ([]Session, error) {
	var out []Session
	req := &pb.ListRemoteSessionsRequest{}
	for {
		res, err := c.api.ListRemoteSessions(ctx, connect.NewRequest(req))
		if err != nil {
			return nil, err
		}
		for _, rs := range res.Msg.Sessions {
			s, err := c.OpenSession(rs)
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		if res.Msg.Next == nil {
			return out, nil
		}
		req = &pb.ListRemoteSessionsRequest{After: res.Msg.Next}
	}
}

// OpenSession opens a session record.
func (c *Client) OpenSession(rs *pb.RemoteSession) (Session, error) {
	s := Session{MachineID: rs.MachineId, ID: rs.SessionId, LastSeq: rs.LastSeq}
	h, key, err := c.openEnvelope(context.Background(), nil, rs.Key, true)
	if err != nil {
		return s, fmt.Errorf("session %s key: %w", rs.SessionId, err)
	}
	if err := e2e.Expect(h, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_KEY, MachineId: rs.MachineId, SessionId: rs.SessionId}); err != nil {
		return s, err
	}
	s.Key = key
	if rs.Meta != nil {
		info := &pb.Session{}
		if _, err := c.openMessage(context.Background(), nil, rs.Meta, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_META, MachineId: rs.MachineId, SessionId: rs.SessionId}, info); err != nil {
			return s, fmt.Errorf("session %s meta: %w", rs.SessionId, err)
		}
		s.Info = info
	}
	return s, nil
}

// Events returns all events after seq, opened and checked.
func (c *Client) Events(ctx context.Context, s Session, after uint64) ([]*pb.Event, error) {
	var out []*pb.Event
	for {
		res, err := c.api.GetEvents(ctx, connect.NewRequest(&pb.GetEventsRequest{SessionId: s.ID, AfterSeq: after}))
		if err != nil {
			return nil, err
		}
		for _, se := range res.Msg.Events {
			e, err := c.OpenEvent(s, se)
			if err != nil {
				return nil, err
			}
			if e.Seq != after+1 {
				return nil, fmt.Errorf("event %d follows %d", e.Seq, after)
			}
			after = e.Seq
			out = append(out, e)
		}
		if !res.Msg.More {
			return out, nil
		}
	}
}

// OpenEvent opens one stored event of s.
func (c *Client) OpenEvent(s Session, se *pb.StoredEvent) (*pb.Event, error) {
	e := &pb.Event{}
	if _, err := c.openMessage(context.Background(), s.Key, se.Envelope, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT, MachineId: s.MachineID, SessionId: s.ID, Seq: se.Seq,
	}, e); err != nil {
		return nil, fmt.Errorf("event %d: %w", se.Seq, err)
	}
	if e.Seq != se.Seq || e.SessionId != s.ID {
		return nil, e2e.ErrPlace
	}
	return e, nil
}

// NewRequestID returns a random request ID.
func NewRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// CommandError is an error the daemon reported for a command.
type CommandError struct {
	Code, Message string
}

func (e *CommandError) Error() string { return e.Code + ": " + e.Message }

// Seal builds a sealed command for the given request ID and deadline.
func (c *Client) Seal(machineID, sessionID, requestID string, deadline time.Time, cmd *pb.Command) (*pb.Envelope, error) {
	if cmd.ProtocolVersion == 0 {
		cmd.ProtocolVersion = protocol.Version
	}
	plain, err := proto.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	wire, err := c.security.Seal(context.Background(), deviceauth.Header{Kind: 5, Purpose: deviceauth.Command, Machine: machineID, Session: sessionID, Request: requestID, Deadline: uint64(deadline.UnixMilli())}, nil, plain)
	if err != nil {
		return nil, err
	}
	return wire.Envelope()
}

// Keys are the account's derived keys.
func (c *Client) Keys() *e2e.Keys { return c.keys }

// Do sends cmd to the session's machine and returns the opened result.
func (c *Client) Do(ctx context.Context, s Session, cmd *pb.Command) (*pb.CommandResult, error) {
	env, err := c.Seal(s.MachineID, s.ID, NewRequestID(), time.Now().Add(c.CommandTTL), cmd)
	if err != nil {
		return nil, err
	}
	return c.Forward(ctx, s.MachineID, env)
}

// DoMachine sends a command for the machine itself, such as creating a
// session; its header names no session.
func (c *Client) DoMachine(ctx context.Context, machineID string, cmd *pb.Command) (*pb.CommandResult, error) {
	env, err := c.Seal(machineID, "", NewRequestID(), time.Now().Add(c.CommandTTL), cmd)
	if err != nil {
		return nil, err
	}
	return c.Forward(ctx, machineID, env)
}

// Forward sends a sealed command and opens the result.
func (c *Client) Forward(ctx context.Context, machineID string, env *pb.Envelope) (*pb.CommandResult, error) {
	if env == nil {
		return nil, deviceauth.ErrInvalid
	}
	h := &pb.Header{}
	if err := proto.Unmarshal(env.Header, h); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	res, err := c.api.Forward(ctx, connect.NewRequest(&pb.ForwardRequest{MachineId: machineID, Command: env}))
	if err != nil {
		return nil, &CommandError{Code: "unknown", Message: "the relay supplied no authenticated outcome; retry the same request"}
	}
	out := &pb.CommandResult{}
	if _, err := c.openMessage(ctx, nil, res.Msg.Result, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND_RESULT, MachineId: machineID, SessionId: h.SessionId, RequestId: h.RequestId,
	}, out); err != nil {
		return nil, &CommandError{Code: "unknown", Message: "the command result failed authentication; retry the same request"}
	}
	if out.ErrorCode != "" {
		return out, &CommandError{Code: out.ErrorCode, Message: out.ErrorMessage}
	}
	return out, nil
}

// Stream opens the push stream. Frames arrive on the returned channel,
// which is closed when the stream ends.
func (c *Client) Stream(ctx context.Context) (<-chan *pb.ServerFrame, error) {
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(c.Server, "http")+"/v1/stream", &websocket.DialOptions{HTTPClient: netpolicy.Client(time.Minute)})
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(4 << 20)
	hello, _ := proto.Marshal(&pb.PeerFrame{Kind: &pb.PeerFrame_Hello{Hello: &pb.StreamHello{Token: c.token}}})
	if err := ws.Write(ctx, websocket.MessageBinary, hello); err != nil {
		ws.CloseNow()
		return nil, err
	}
	out := make(chan *pb.ServerFrame, 64)
	ready := make(chan error, 1)
	go func() {
		defer close(out)
		defer ws.CloseNow()
		first := true
		for {
			_, b, err := ws.Read(ctx)
			if err != nil {
				if first {
					ready <- err
				}
				return
			}
			f := &pb.ServerFrame{}
			if err := proto.Unmarshal(b, f); err != nil {
				if first {
					ready <- fmt.Errorf("stream frame: %w", err)
				}
				return
			}
			if first {
				first = false
				if f.GetReady() == nil {
					ready <- errors.New("stream not ready")
					return
				}
				ready <- nil
				continue
			}
			select {
			case out <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	select {
	case err := <-ready:
		if err != nil {
			return nil, err
		}
	case <-ctx.Done():
		ws.CloseNow()
		return nil, ctx.Err()
	}
	return out, nil
}

// Term is an open terminal stream.
type Term struct {
	c      *Client
	s      Session
	ws     *websocket.Conn
	stream string
	last   uint64

	mu  sync.Mutex // orders sends so frames leave in seq order
	seq uint64
}

// OpenTerminal joins a session's terminal stream.
func (c *Client) OpenTerminal(ctx context.Context, s Session) (*Term, error) {
	stream := NewRequestID()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(c.Server, "http")+"/v1/term", &websocket.DialOptions{HTTPClient: netpolicy.Client(time.Minute)})
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(4 << 20)
	hello, _ := proto.Marshal(&pb.TermHello{Token: c.token, StreamId: stream, MachineId: s.MachineID})
	if err := ws.Write(ctx, websocket.MessageBinary, hello); err != nil {
		ws.CloseNow()
		return nil, err
	}
	if _, err := c.Do(ctx, s, &pb.Command{Kind: &pb.Command_OpenTerminal{OpenTerminal: &pb.OpenTerminalRequest{SessionId: s.ID, StreamId: stream}}}); err != nil {
		ws.CloseNow()
		return nil, err
	}
	return &Term{c: c, s: s, ws: ws, stream: stream}, nil
}

// Read returns the next frame from the daemon.
func (t *Term) Read(ctx context.Context) (*pb.TerminalFrame, error) {
	_, b, err := t.ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	env := &pb.Envelope{}
	if err := proto.Unmarshal(b, env); err != nil {
		return nil, err
	}
	f := &pb.TerminalFrame{}
	h, err := t.c.openMessage(ctx, t.s.Key, env, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_TERMINAL_OUTPUT, MachineId: t.s.MachineID, SessionId: t.s.ID, RequestId: t.stream,
	}, f)
	if err != nil {
		return nil, err
	}
	if h.Seq <= t.last {
		return nil, errors.New("terminal frame out of order")
	}
	t.last = h.Seq
	return f, nil
}

// Send seals and sends a frame to the daemon.
func (t *Term) Send(ctx context.Context, f *pb.TerminalFrame) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seq++
	return t.send(ctx, t.seq, f)
}

// SendRaw sends a frame with the given sequence number, for tests.
func (t *Term) SendRaw(ctx context.Context, seq uint64, f *pb.TerminalFrame) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.send(ctx, seq, f)
}

func (t *Term) send(ctx context.Context, seq uint64, f *pb.TerminalFrame) error {
	purpose := deviceauth.TerminalInput
	switch f.Kind.(type) {
	case *pb.TerminalFrame_Resize:
		purpose = deviceauth.TerminalResize
	case *pb.TerminalFrame_HistoryRequest:
		purpose = deviceauth.TerminalHistory
	}
	plain, err := proto.Marshal(f)
	if err != nil {
		return err
	}
	wire, err := t.c.security.Seal(ctx, deviceauth.Header{Kind: 8, Purpose: purpose, Machine: t.s.MachineID, Session: t.s.ID, Request: t.stream, Seq: seq, Deadline: uint64(time.Now().Add(time.Minute).UnixMilli())}, t.s.Key, plain)
	if err != nil {
		return err
	}
	env, err := wire.Envelope()
	if err != nil {
		return err
	}
	b, _ := proto.Marshal(env)
	return t.ws.Write(ctx, websocket.MessageBinary, b)
}

// Close leaves the stream.
func (t *Term) Close() { t.ws.Close(websocket.StatusNormalClosure, "") }

// Image fetches and opens the display copy of an image in s. expired
// reports that the server no longer has it.
func (c *Client) Image(ctx context.Context, s Session, imageID string) (img *pb.ImagePayload, expired bool, err error) {
	res, err := c.api.GetImage(ctx, connect.NewRequest(&pb.GetImageRequest{SessionId: s.ID, ImageId: imageID}))
	if err != nil {
		return nil, false, err
	}
	if res.Msg.Expired {
		return nil, true, nil
	}
	img = &pb.ImagePayload{}
	if _, err := c.openMessage(ctx, s.Key, res.Msg.Image, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_IMAGE, MachineId: s.MachineID, SessionId: s.ID, RequestId: imageID,
	}, img); err != nil {
		return nil, false, err
	}
	return img, false, nil
}

func (c *Client) Close() error { return c.security.Endpoint.Close() }
func (c *Client) openEnvelope(ctx context.Context, seed []byte, env *pb.Envelope, history bool) (*pb.Header, []byte, error) {
	wire, err := deviceauth.FromEnvelope(env)
	if err != nil {
		return nil, nil, err
	}
	var h deviceauth.Header
	var plain []byte
	if history {
		h, plain, err = c.security.OpenHistory(ctx, wire, seed)
	} else {
		h, plain, err = c.security.OpenCurrent(ctx, wire, seed)
	}
	if err != nil {
		return nil, nil, err
	}
	plain, err = deviceauth.Unpack(h, plain)
	if err != nil {
		return nil, nil, err
	}
	return h.Projection(), plain, nil
}
func (c *Client) openMessage(ctx context.Context, seed []byte, env *pb.Envelope, want *pb.Header, m proto.Message) (*pb.Header, error) {
	history := true // Delayed results keep exact request/stream/sequence provenance.
	h, plain, err := c.openEnvelope(ctx, seed, env, history)
	if err != nil {
		return nil, err
	}
	if err = e2e.Expect(h, want); err != nil {
		return nil, err
	}
	if proto.Unmarshal(plain, m) != nil {
		return nil, deviceauth.ErrInvalid
	}
	return h, nil
}
