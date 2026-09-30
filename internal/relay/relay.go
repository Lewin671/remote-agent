package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/paths"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

// Backend is the part of the daemon the relay needs.
type Backend interface {
	// SessionIDs lists all sessions, including exited ones.
	SessionIDs() []string
	// SessionInfo describes a session.
	SessionInfo(id string) (*pb.Session, error)
	// Events returns the session's events after seq and a channel that is
	// closed when more are appended.
	Events(id string, after uint64) ([]*pb.Event, <-chan struct{}, error)
	// SessionDir is the session's state directory.
	SessionDir(id string) string
	// Local serves the commands.
	Local() remoteagentv1connect.LocalServiceHandler
	// MachineMeta describes the machine.
	MachineMeta() *pb.MachineMeta
	// OpenTerminal starts streaming a session's terminal.
	OpenTerminal(sessionID string) (Terminal, error)
	// Image says what to do about an image's display copy before an
	// event that shows it is uploaded; ImageUploaded records the outcome.
	Image(sessionID, imageID string) ImageUpload
	ImageUploaded(sessionID, imageID string, ok bool, expiresMs int64)
}

// ImageUpload is the display copy of an image in a session.
type ImageUpload struct {
	// Payload is a serialized ImagePayload to seal and upload.
	Payload []byte
	// Done: it is on the server already.
	Done bool
	// Failed: there is none to upload; events show the preview only.
	Failed bool
}

// Relay keeps the daemon connected to the server.
type Relay struct {
	backend Backend
	cmds    *commandLog
	// Retry is the first reconnect delay; it doubles up to a minute.
	Retry time.Duration

	mu        sync.Mutex
	connected bool
	// imageTries counts failed uploads per image across connections, so
	// that one image cannot hold its session's events back for long.
	imageTries map[string]int
}

// New returns a relay for backend.
func New(backend Backend) *Relay {
	return &Relay{backend: backend, cmds: openCommandLog(paths.Commands()), Retry: time.Second}
}

// Connected reports whether the relay currently has a stream to the server.
func (r *Relay) Connected() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.connected
}

// Run connects and reconnects until ctx is done. Without an account it
// waits for one to be created.
func (r *Relay) Run(ctx context.Context) {
	delay := r.Retry
	logged := false
	for ctx.Err() == nil {
		acct, err := LoadAccount()
		if errors.Is(err, ErrNoAccount) {
			if !logged {
				slog.Info("relay: no account yet; waiting for `remote-agent account init` or `join`")
				logged = true
			}
			sleep(ctx, 5*time.Second)
			continue
		}
		if err != nil {
			slog.Error("relay: load account", "err", err)
			sleep(ctx, time.Minute)
			continue
		}
		start := time.Now()
		err = r.session(ctx, acct)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			delay = r.Retry
		}
		slog.Warn("relay: disconnected", "err", err, "retry_in", delay)
		sleep(ctx, delay+time.Duration(rand.Int64N(int64(delay/4+1))))
		delay = min(delay*2, time.Minute)
	}
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// conn is one connected period: the login token, clients and keys.
type conn struct {
	r       *Relay
	acct    *Account
	keys    *e2e.Keys
	machine remoteagentv1connect.MachineServiceClient
	ws      *websocket.Conn
	token   string
}

// session logs in, opens the stream, and serves until an error.
func (r *Relay) session(ctx context.Context, acct *Account) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	keys, err := e2e.Derive(acct.Root)
	if err != nil {
		return err
	}
	token, err := login(ctx, acct)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	hc := httpClient()
	c := &conn{r: r, acct: acct, keys: keys, token: token,
		machine: remoteagentv1connect.NewMachineServiceClient(hc, acct.Server, connect.WithInterceptors(bearer(token)))}

	wsURL := "ws" + strings.TrimPrefix(acct.Server, "http") + "/v1/stream"
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(4 << 20)
	c.ws = ws
	hello, _ := proto.Marshal(&pb.PeerFrame{Kind: &pb.PeerFrame_Hello{Hello: &pb.StreamHello{Token: token}}})
	if err := ws.Write(ctx, websocket.MessageBinary, hello); err != nil {
		return err
	}
	if f, err := readFrame(ctx, ws); err != nil {
		return err
	} else if f.GetReady() == nil {
		return errors.New("server did not accept the stream")
	}
	if err := c.putMachine(ctx, c.r.backend.MachineMeta()); err != nil {
		return err
	}
	r.mu.Lock()
	r.connected = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.connected = false
		r.mu.Unlock()
	}()
	slog.Info("relay: connected", "server", acct.Server, "machine", acct.DeviceID)

	errc := make(chan error, 2)
	go func() { errc <- c.readLoop(ctx) }()
	go func() { errc <- c.syncLoop(ctx) }()
	go func() {
		// Keep the connection alive through proxies and notice a dead one.
		t := time.NewTicker(25 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pctx, pcancel := context.WithTimeout(ctx, 20*time.Second)
				err := ws.Ping(pctx)
				pcancel()
				if err != nil {
					errc <- fmt.Errorf("ping: %w", err)
					return
				}
			}
		}
	}()
	return <-errc
}

func login(ctx context.Context, acct *Account) (string, error) {
	auth := remoteagentv1connect.NewAuthServiceClient(httpClient(), acct.Server)
	ch, err := auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: acct.DeviceID}))
	if err != nil {
		return "", err
	}
	res, err := auth.Login(ctx, connect.NewRequest(&pb.LoginRequest{
		DeviceId: acct.DeviceID, Challenge: ch.Msg.Challenge, Signature: e2e.SignLogin(acct.DeviceKey(), ch.Msg.Challenge),
	}))
	if err != nil {
		return "", err
	}
	return res.Msg.Token, nil
}

func bearer(token string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	}
}

func readFrame(ctx context.Context, ws *websocket.Conn) (*pb.ServerFrame, error) {
	typ, b, err := ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	f := &pb.ServerFrame{}
	if typ != websocket.MessageBinary || proto.Unmarshal(b, f) != nil {
		return nil, errors.New("malformed frame from server")
	}
	return f, nil
}

// revision returns a metadata revision that grows across restarts.
var revMu sync.Mutex
var lastRev uint64

func revision() uint64 {
	revMu.Lock()
	defer revMu.Unlock()
	lastRev = max(lastRev+1, uint64(time.Now().UnixMicro()))
	return lastRev
}

func (c *conn) putMachine(ctx context.Context, meta *pb.MachineMeta) error {
	env, err := e2e.SealMessage(c.keys.Meta, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_MACHINE_META, MachineId: c.acct.DeviceID, Seq: revision(), Sender: c.acct.DeviceID,
	}, meta)
	if err != nil {
		return err
	}
	_, err = c.machine.PutMachine(ctx, connect.NewRequest(&pb.PutMachineRequest{Meta: env}))
	return err
}

func (c *conn) readLoop(ctx context.Context) error {
	for {
		f, err := readFrame(ctx, c.ws)
		if err != nil {
			return err
		}
		if call := f.GetForwardCall(); call != nil {
			go c.handleCall(ctx, call)
		}
	}
}

func (c *conn) reply(ctx context.Context, r *pb.ForwardReply) {
	b, err := proto.Marshal(&pb.PeerFrame{Kind: &pb.PeerFrame_ForwardReply{ForwardReply: r}})
	if err != nil {
		return
	}
	wctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := c.ws.Write(wctx, websocket.MessageBinary, b); err != nil {
		slog.Warn("relay: send reply", "call", r.CallId, "err", err)
	}
}

// errKeyLost means the server holds a session whose key is missing here.
var errKeyLost = errors.New("the session's key is missing locally but the server already has the session")

// sessionKey returns the session's key. A new key is only created for a
// session the server does not have yet: the server keeps the first key it
// gets, so a replacement would make later events unreadable.
func (c *conn) sessionKey(id string, onServer bool) ([]byte, error) {
	dir := c.r.backend.SessionDir(id)
	if dir == "" {
		return nil, fmt.Errorf("session %s has no state directory", id)
	}
	path := filepath.Join(dir, "relay.key")
	b, err := os.ReadFile(path)
	if err == nil && len(b) == e2e.KeySize {
		return b, nil
	}
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if onServer {
		return nil, errKeyLost
	}
	key := e2e.NewKey()
	if err := writeFileSync(path, key); err != nil {
		return nil, err
	}
	return key, nil
}
