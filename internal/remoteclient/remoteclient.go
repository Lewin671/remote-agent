// Package remoteclient is a client of remote-agent-server in Go. It does
// what the web client does, so tests and scripts can drive daemons through
// the server end to end.
package remoteclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"google.golang.org/protobuf/proto"
)

// Client is a logged-in client device.
type Client struct {
	Server   string
	DeviceID string
	keys     *e2e.Keys
	token    string
	api      remoteagentv1connect.ClientServiceClient
	// CommandTTL is how long a command stays valid.
	CommandTTL time.Duration
}

// Register creates a client device for the account in secret and logs in.
func Register(ctx context.Context, secret e2e.Secret) (*Client, error) {
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
	hc := &http.Client{Timeout: time.Minute}
	auth := remoteagentv1connect.NewAuthServiceClient(hc, secret.Server)
	res, err := auth.RegisterDevice(ctx, connect.NewRequest(&pb.RegisterDeviceRequest{Registration: reg, Signature: keys.SignRegistration(reg)}))
	if err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}
	id := res.Msg.DeviceId
	ch, err := auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: id}))
	if err != nil {
		return nil, err
	}
	lr, err := auth.Login(ctx, connect.NewRequest(&pb.LoginRequest{DeviceId: id, Challenge: ch.Msg.Challenge, Signature: e2e.SignLogin(priv, ch.Msg.Challenge)}))
	if err != nil {
		return nil, fmt.Errorf("login: %w", err)
	}
	c := &Client{Server: secret.Server, DeviceID: id, keys: keys, token: lr.Msg.Token, CommandTTL: time.Minute}
	c.api = remoteagentv1connect.NewClientServiceClient(hc, secret.Server, connect.WithInterceptors(connect.UnaryInterceptorFunc(
		func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				req.Header().Set("Authorization", "Bearer "+c.token)
				return next(ctx, req)
			}
		})))
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
			if _, err := e2e.OpenMessage(c.keys.Meta, m.Meta, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_MACHINE_META, MachineId: m.Id}, meta); err != nil {
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

// Sessions lists sessions with their keys and metadata opened.
func (c *Client) Sessions(ctx context.Context) ([]Session, error) {
	res, err := c.api.ListRemoteSessions(ctx, connect.NewRequest(&pb.ListRemoteSessionsRequest{}))
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, rs := range res.Msg.Sessions {
		s, err := c.OpenSession(rs)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// OpenSession opens a session record.
func (c *Client) OpenSession(rs *pb.RemoteSession) (Session, error) {
	s := Session{MachineID: rs.MachineId, ID: rs.SessionId, LastSeq: rs.LastSeq}
	h, key, err := e2e.Open(c.keys.Wrap, rs.Key)
	if err != nil {
		return s, fmt.Errorf("session %s key: %w", rs.SessionId, err)
	}
	if err := e2e.Expect(h, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_KEY, MachineId: rs.MachineId, SessionId: rs.SessionId}); err != nil {
		return s, err
	}
	s.Key = key
	if rs.Meta != nil {
		info := &pb.Session{}
		if _, err := e2e.OpenMessage(key, rs.Meta, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_META, MachineId: rs.MachineId, SessionId: rs.SessionId}, info); err != nil {
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
	if _, err := e2e.OpenMessage(s.Key, se.Envelope, &pb.Header{
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
	return e2e.SealMessage(c.keys.Command, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND, MachineId: machineID, SessionId: sessionID,
		RequestId: requestID, DeadlineMs: deadline.UnixMilli(), Sender: c.DeviceID,
	}, cmd)
}

// Do sends cmd to the session's machine and returns the opened result.
func (c *Client) Do(ctx context.Context, s Session, cmd *pb.Command) (*pb.CommandResult, error) {
	env, err := c.Seal(s.MachineID, s.ID, NewRequestID(), time.Now().Add(c.CommandTTL), cmd)
	if err != nil {
		return nil, err
	}
	return c.Forward(ctx, s.MachineID, env)
}

// Forward sends a sealed command and opens the result.
func (c *Client) Forward(ctx context.Context, machineID string, env *pb.Envelope) (*pb.CommandResult, error) {
	h := &pb.Header{}
	if err := proto.Unmarshal(env.Header, h); err != nil {
		return nil, err
	}
	res, err := c.api.Forward(ctx, connect.NewRequest(&pb.ForwardRequest{MachineId: machineID, Command: env}))
	if err != nil {
		return nil, err
	}
	out := &pb.CommandResult{}
	if _, err := e2e.OpenMessage(c.keys.Command, res.Msg.Result, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND_RESULT, MachineId: machineID, SessionId: h.SessionId, RequestId: h.RequestId,
	}, out); err != nil {
		return nil, err
	}
	if out.ErrorCode != "" {
		return out, &CommandError{Code: out.ErrorCode, Message: out.ErrorMessage}
	}
	return out, nil
}

// Stream opens the push stream. Frames arrive on the returned channel,
// which is closed when the stream ends.
func (c *Client) Stream(ctx context.Context) (<-chan *pb.ServerFrame, error) {
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(c.Server, "http")+"/v1/stream", nil)
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
			if proto.Unmarshal(b, f) != nil {
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
	if err := <-ready; err != nil {
		return nil, err
	}
	return out, nil
}
