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
	"sync/atomic"
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

// Backend is the part of the daemon the relay needs.
type Backend interface {
	// SessionIDs lists all sessions, including exited ones.
	SessionIDs() []string
	// SessionInfo describes a session.
	SessionInfo(id string) (*pb.Session, error)
	// EachEvents streams one stable snapshot after seq and returns the
	// channel closed when a later event is appended.
	EachEvents(id string, after uint64, fn func(*pb.Event) error) (<-chan struct{}, error)
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
	// Image may touch LRU metadata. Both callbacks belong to publication's
	// write lifetime; arbitrary commands and snapshots do not.
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
	backend     Backend
	calls       chan struct{} // bounds command workers across reconnects
	machineRPCs chan struct{} // bounds session uploads across reconnects
	// backlog admits one ended session at a time to its first sync after
	// connecting, so that the other RPC slot stays free for live sessions.
	backlog chan struct{}
	// Retry is the first reconnect delay; it doubles up to a minute.
	Retry time.Duration
	// syncInterval paces idle workers and completed-session checks. Tests can
	// shorten it without changing the production schedule.
	syncInterval time.Duration
	// keyMu makes reading or creating a session's key one step across
	// concurrent uploads and terminal requests.
	keyMu sync.Mutex

	mu        sync.Mutex
	connected bool
	// imageTries counts failed uploads per image across connections, so
	// that one image cannot hold its session's events back for long.
	imageTries map[string]int
	// sentMeta is the metadata last put for each session, kept across
	// connections: a reconnect then sends only what changed.
	sentMeta map[string]*pb.Session
	// owedNotify holds, per session, the events whose notification the
	// server had no capacity for or never answered. Their upload is not
	// repeated, so nothing else would bring them up again.
	owedNotify map[string][]*pb.Event
}

// New returns a relay for backend.
func New(backend Backend) *Relay {
	return &Relay{backend: backend, calls: make(chan struct{}, maxConcurrentCalls), machineRPCs: make(chan struct{}, maxConcurrentMachineRPCs), backlog: make(chan struct{}, 1), sentMeta: map[string]*pb.Session{}, owedNotify: map[string][]*pb.Event{}, Retry: time.Second, syncInterval: 5 * time.Second}
}

// Connected reports whether the relay currently has a stream to the server.
func (r *Relay) Connected() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.connected
}

// Run connects and reconnects until ctx is done. Without an account it
// waits for one to be created. Return drains admitted publication mutations;
// canceled readers/command callbacks cannot start new publication writes.
func (r *Relay) Run(ctx context.Context) {
	delay := r.Retry
	logged := false
	for ctx.Err() == nil {
		registrationCtx, registrationCancel := context.WithTimeout(ctx, 10*time.Second)
		registrationErr := ResumeRegistration(registrationCtx)
		registrationCancel()
		if err := registrationErr; err != nil {
			// Pending enrollment must not gate an already installed account.
			slog.Warn("relay: registration pending; retry or explicitly discard with `remote-agent account discard-registration`", "err", err)
		}
		acct, err := LoadAccount()
		if errors.Is(err, ErrNoAccount) {
			if !logged {
				slog.Info("relay: no account yet; waiting for `remote-agent account join` with a code from the phone")
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
		if securityAuthenticationLost(err) || connect.CodeOf(err) == connect.CodeFailedPrecondition {
			slog.Warn("relay: authorization refused; re-pair with `remote-agent account join` and a new phone code", "err", err)
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
	lifetime          context.Context // stream lifetime, independent of one command deadline
	writes            connectionWrites
	security          *securitySession
	r                 *Relay
	acct              *Account
	machine           remoteagentv1connect.MachineServiceClient
	publishMu         sync.Mutex
	checkpointPending atomic.Bool
	ws                *websocket.Conn
	token             string
}

// session logs in, opens the stream, and serves until an error.
func (r *Relay) session(ctx context.Context, acct *Account) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	token, err := login(ctx, acct)
	if err != nil {
		return fmt.Errorf("login: %w", err)
	}
	hc := httpClient()
	c := &conn{r: r, acct: acct, token: token, lifetime: ctx,
		machine: remoteagentv1connect.NewMachineServiceClient(hc, acct.Server, connect.WithInterceptors(bearer(token), r.limitMachineRPCs()))}

	securityClient := remoteagentv1connect.NewClientServiceClient(hc, acct.Server, connect.WithInterceptors(bearer(token)))
	c.security, err = openSecuritySession(acct, securityClient)
	if err != nil {
		return err
	}
	defer func() {
		cancel()
		c.writes.close()
		c.security.Close()
	}()

	wsURL := "ws" + strings.TrimPrefix(acct.Server, "http") + "/v1/stream"
	ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: httpClient()})
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	defer ws.CloseNow()
	ws.SetReadLimit(4 << 20)
	c.ws = ws
	readyCtx, readyCancel := context.WithTimeout(ctx, 15*time.Second)
	defer readyCancel()
	hello, _ := proto.Marshal(&pb.PeerFrame{Kind: &pb.PeerFrame_Hello{Hello: &pb.StreamHello{Token: token}}})
	if err := ws.Write(readyCtx, websocket.MessageBinary, hello); err != nil {
		return err
	}
	if f, err := readFrame(readyCtx, ws); err != nil {
		return err
	} else if f.GetReady() == nil {
		return errors.New("server did not accept the stream")
	}
	errc := make(chan error, 4)
	// Start consuming immediately after Ready, including during slow startup RPCs.
	go func() { errc <- c.readLoop(ctx); cancel() }()
	r.mu.Lock()
	r.connected = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.connected = false
		r.mu.Unlock()
	}()
	slog.Info("relay: connected", "server", acct.Server, "machine", acct.DeviceID)

	if err := c.security.syncOnce(ctx); err != nil {
		if securityAuthenticationLost(err) {
			return err
		}
		// The stream itself is up. Installed states still serve events and
		// the terminal; remote control pauses until a later sync succeeds.
		slog.Warn("relay: starting without the newest security state; control remains valid until the local lease expires", "err", err)
	}
	meta := c.r.backend.MachineMeta()
	if err := c.ensurePublished(ctx); err != nil {
		slog.Warn("relay: current checkpoint pending; retrying during synchronization", "err", err)
	}
	if err := c.putMachine(ctx, meta); err != nil {
		return err
	}
	go func() { errc <- c.syncLoop(ctx, meta) }()
	go func() { errc <- c.securitySyncLoop(ctx) }()
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
	res, err := auth.Login(ctx, connect.NewRequest(&pb.LoginRequest{ProtocolVersion: protocol.Version,
		DeviceId: acct.DeviceID, Challenge: ch.Msg.Challenge, Signature: e2e.SignLogin(acct.DeviceKey(), ch.Msg.Challenge),
	}))
	if err != nil {
		return "", err
	}
	return res.Msg.Token, nil
}

// Keep history synchronization below the server's RPC admission limit, leaving
// room for client requests. Acquire per call, not per long-lived session sync.
const maxConcurrentMachineRPCs = 2

func (r *Relay) limitMachineRPCs() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case r.machineRPCs <- struct{}{}:
				defer func() { <-r.machineRPCs }()
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			return next(ctx, req)
		}
	}
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

// publishCurrentCheckpoint re-seals current machine and session metadata under
// the new content key after a recovery boundary. Old events and images are
// never re-encrypted: the new phone must not receive old plaintext. The
// published content epoch is written last, so a crash before it makes the
// next sync retry the whole republish.
func (c *conn) publishCurrentCheckpoint(ctx context.Context) (retErr error) {
	// A full pass is also the retry of checkpoints that failed during ordinary
	// session sync. Individual writers only set this flag; they never clear it.
	c.checkpointPending.Store(false)
	defer func() {
		if retErr != nil {
			c.checkpointPending.Store(true)
		}
	}()
	_, head, _ := c.security.endpoint.Snapshot()
	startedEpoch := head.Core.ContentEpoch
	if err := c.putMachine(ctx, c.r.backend.MachineMeta()); err != nil {
		return err
	}
	ids := c.r.backend.SessionIDs()
	floors := map[string]uint64{}
	if len(ids) > 0 {
		res, err := c.machine.ListSessionHeads(ctx, connect.NewRequest(&pb.ListSessionHeadsRequest{}))
		if err != nil {
			return err
		}
		for _, h := range res.Msg.Sessions {
			floors[h.SessionId] = h.MetaRevision
		}
	}
	var firstErr error
	for _, id := range ids {
		info, err := c.r.backend.SessionInfo(id)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		key, err := c.sessionKey(id, true)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		rev, err := c.putSession(ctx, id, key, info, floors[id])
		if err != nil {
			slog.Warn("relay: republish session after recovery", "session", id, "err", err)
			if firstErr == nil {
				firstErr = err
			}
		} else {
			c.noteSentMeta(id, key, syncMeta(info), rev)
		}
	}
	if firstErr != nil {
		return firstErr
	}
	if c.checkpointPending.Load() {
		return errors.New("current checkpoints remain pending")
	}
	return c.writeDisk(func() error { return writePublishedEpoch(startedEpoch) })
}

// ensurePublished republishes current metadata on the first run and after a
// recovery, even if the process crashed before the callback ran. Ordinary
// epochs (adding or removing a device, renewing the lease) reuse the server's
// stored metadata: only a recovery needs every session re-sealed.
func (c *conn) ensurePublished(ctx context.Context) error {
	c.publishMu.Lock()
	defer c.publishMu.Unlock()
	marker, err := readPublishedEpoch()
	_, head, _ := c.security.endpoint.Snapshot()
	if err == nil && head.Core.ContentEpoch == marker && !c.checkpointPending.Load() {
		return nil
	}
	return c.publishCurrentCheckpoint(ctx)
}

func (c *conn) putMachine(ctx context.Context, meta *pb.MachineMeta) error {
	env, err := c.security.sealMessage(&pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_MACHINE_META, MachineId: c.acct.DeviceID, Seq: revision(), Sender: c.acct.DeviceID,
	}, deviceauth.MachineMeta, nil, meta)
	if err != nil {
		return err
	}
	_, err = c.machine.PutMachine(ctx, connect.NewRequest(&pb.PutMachineRequest{Meta: env}))
	return err
}

// maxConcurrentCalls bounds memory and goroutines even when an untrusted
// server floods the stream with large commands or repeats an inflight one.
const maxConcurrentCalls = 8

func (c *conn) readLoop(ctx context.Context) error {
	return readForwardCalls(ctx, c.r.calls, func() (*pb.ServerFrame, error) {
		return readFrame(ctx, c.ws)
	}, c.handleCall)
}

func readForwardCalls(ctx context.Context, slots chan struct{}, read func() (*pb.ServerFrame, error), handle func(context.Context, *pb.ForwardCall)) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		f, err := read()
		if err != nil {
			return err
		}
		if call := f.GetForwardCall(); call != nil {
			// Backpressure retains at most one extra frame. A disconnected
			// session must not wait indefinitely for a previous command.
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				return ctx.Err()
			}
			if ctx.Err() != nil {
				<-slots
				return ctx.Err()
			}
			go func() {
				defer func() { <-slots }()
				handle(ctx, call)
			}()
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
// gets, so a replacement would make later events unreadable. That holds
// across connections too: two of them creating the key at once would each
// write their own, and the file could end up with the one the server
// did not keep.
func (c *conn) sessionKey(id string, onServer bool) ([]byte, error) {
	dir := c.r.backend.SessionDir(id)
	if dir == "" {
		return nil, fmt.Errorf("session %s has no state directory", id)
	}
	path := filepath.Join(dir, "relay.key")
	c.r.keyMu.Lock()
	defer c.r.keyMu.Unlock()
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
	if err := c.writeDisk(func() error { return writeFileSync(path, key) }); err != nil {
		return nil, err
	}
	return key, nil
}
