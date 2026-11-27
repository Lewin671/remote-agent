package relay

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/relayproto"
	"github.com/Lewin671/remote-agent/internal/transport"
	"github.com/Lewin671/remote-agent/shared/devicesec"
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
	// ProbeWork is the connection check's work: the smallest real read an
	// ordinary command does (the settings, under the daemon's lock), so the
	// time it reports reflects queueing and a stuck or starved daemon rather
	// than how much the machine has stored.
	ProbeWork(ctx context.Context) error
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
	directSTUN     []string
	directLoopback bool
	directClock    func() time.Time
	termClock      func() time.Time
	termZeroLegTTL time.Duration
	termHubRef     atomic.Pointer[terminalHub]
	directRef      atomic.Pointer[directManager]
	// SchedulerNow and SchedulerAfter override the command scheduler clock and
	// timers when non-nil; tests use them to avoid wall-clock races.
	SchedulerNow   func() time.Time
	SchedulerAfter func(time.Duration, func()) func() bool

	observeApproval func(acquired bool, wait time.Duration)
	scheduling      atomic.Pointer[transport.Scheduler]
	security        atomic.Pointer[securitySession]
	observeRecovery func(recoveryEvent)
	recoveryBudget  *transport.Budget
	readyCount      atomic.Uint64
	backend         Backend
	machineRPCs     chan struct{} // bounds session uploads across reconnects
	// backlog admits one ended session at a time to its first sync after
	// connecting, so that the other RPC slot stays free for live sessions.
	backlog chan struct{}
	// Retry is the first reconnect delay; it doubles up to a minute.
	Retry time.Duration
	// syncInterval paces idle workers and completed-session checks. Tests can
	// shorten it without changing the production schedule.
	syncInterval     time.Duration
	securityInterval time.Duration
	timing           retryTiming
	// These durations are per relay so integration tests can exercise the
	// real timeout and retry paths without waiting on production intervals.
	networkChanges        func(context.Context) <-chan uint64
	wakeEvents            func(context.Context) <-chan struct{}
	routeAvailable        func(net.IP) bool
	beforeDialResult      func(uint64)
	beforeHello           func()
	onFrame               func(bool)
	recoveryNow           func() time.Time
	readyTimeout          time.Duration
	syncRetryBase         time.Duration
	publishSessionTimeout time.Duration
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

const defaultPublishSessionTimeout = 15 * time.Second

func (r *Relay) sessionPublishTimeout() time.Duration {
	if r != nil && r.publishSessionTimeout > 0 {
		return r.publishSessionTimeout
	}
	return defaultPublishSessionTimeout
}

// New returns a relay for backend.
func New(backend Backend) *Relay {
	return &Relay{backend: backend, machineRPCs: make(chan struct{}, maxConcurrentMachineRPCs), backlog: make(chan struct{}, 1), sentMeta: map[string]*pb.Session{}, owedNotify: map[string][]*pb.Event{}, Retry: time.Second, syncInterval: 5 * time.Second, securityInterval: 12 * time.Second, readyTimeout: 15 * time.Second, syncRetryBase: time.Second, publishSessionTimeout: defaultPublishSessionTimeout}
}

// Connected reports whether the relay currently has a stream to the server.
func (r *Relay) Connected() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.connected
}

// Run connects and reconnects until ctx is done. Without an account it
// waits for one to be created. The process endpoint and admitted work survive
// reconnects. Shutdown cancels and drains their owners before closing storage.
func (r *Relay) Run(ctx context.Context) {
	var dispatcher *dispatcher
	defer func() {
		if dispatcher != nil {
			dispatcher.close()
		}
	}()
	timing := r.retryTime()
	backoff := retryBackoff{base: r.Retry, jitter: timing.jitter}
	var registrationWarn, initializeWarn, connectionWarn warningGate
	logged := false
	recovering := false
	for ctx.Err() == nil {
		registrationCtx, registrationCancel := context.WithTimeout(ctx, 10*time.Second)
		registrationErr := ResumeRegistration(registrationCtx)
		registrationCancel()
		if err := registrationErr; err != nil {
			// Pending enrollment must not gate an already installed account.
			if registrationWarn.allow(timing.now()) {
				slog.Warn("relay: registration pending; retry or explicitly discard with `remote-agent account discard-registration`", "code", connect.CodeOf(err).String())
			}
		}
		acct, err := LoadAccount()
		if errors.Is(err, ErrNoAccount) {
			if dispatcher != nil {
				dispatcher.close()
				dispatcher = nil
			}
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
		if dispatcher != nil && !sameAccount(dispatcher.acct, acct) {
			dispatcher.retire()
			dispatcher = nil
			recovering = true
		}
		if dispatcher == nil {
			dispatcher, err = r.newDispatcher(ctx, acct, recovering)
			if err != nil {
				if initializeWarn.allow(timing.now()) {
					slog.Warn("relay: security initialization failed", "code", connect.CodeOf(err).String())
				}
				timing.wait(ctx, backoff.failure())
				continue
			}
		}
		start := timing.now()
		err = r.session(ctx, acct, dispatcher)
		if dispatcher.reloadRequested.Load() && ctx.Err() == nil {
			dispatcher.retire()
			dispatcher = nil
			recovering = true
		} else if dispatcher != nil && !dispatcher.security.blocked.Load() {
			recovering = false
		}

		if ctx.Err() != nil {
			return
		}
		if timing.now().Sub(start) > time.Minute {
			backoff.reset()
		}
		delay := backoff.failure()
		if connectionWarn.allow(timing.now()) {
			slog.Warn("relay: disconnected", "code", connect.CodeOf(err).String(), "retry_in", delay)
		}
		timing.wait(ctx, delay)
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

// conn is the process publication component. Its HTTP credentials and
// lifetime are independent of the main relay path and the shared endpoint.
type conn struct {
	diskAlertNow  func() time.Time
	nextDiskAlert time.Time // publisher syncLoop owns the volatile retry deadline

	lifetime            context.Context // stream lifetime, independent of one command deadline
	writes              connectionWrites
	security            *securitySession
	r                   *Relay
	acct                *Account
	machine             remoteagentv1connect.MachineServiceClient
	publishMu           sync.Mutex
	checkpointPending   atomic.Bool
	checkpointMu        sync.Mutex
	dirtyCheckpoints    map[string]bool
	publishedEpoch      uint64
	publishedInProgress bool
	publishedMachine    *pb.MachineMeta
	publishedSessions   map[string]*pb.Session
	publishNow          chan struct{}
	onSyncReady         func()
}

func (c *conn) markCheckpointDirty(id string) {
	c.checkpointMu.Lock()
	if c.dirtyCheckpoints == nil {
		c.dirtyCheckpoints = map[string]bool{}
	}
	c.dirtyCheckpoints[id] = true
	c.checkpointMu.Unlock()
	c.checkpointPending.Store(true)
}

func (c *conn) clearCheckpointDirty(id string) {
	c.checkpointMu.Lock()
	delete(c.dirtyCheckpoints, id)
	c.checkpointMu.Unlock()
}

func (c *conn) isCheckpointDirty(id string) bool {
	c.checkpointMu.Lock()
	dirty := c.dirtyCheckpoints[id]
	c.checkpointMu.Unlock()
	return dirty
}

func (c *conn) hasDirtyCheckpoints() bool {
	c.checkpointMu.Lock()
	n := len(c.dirtyCheckpoints)
	c.checkpointMu.Unlock()
	return n > 0
}

type republishBatchError struct {
	err error
}

func (e *republishBatchError) Error() string { return e.err.Error() }
func (e *republishBatchError) Unwrap() error { return e.err }

var errCheckpointsPending = errors.New("current checkpoints remain pending")

func login(ctx context.Context, acct *Account) (string, error) {
	token, _, err := loginWithExpiry(ctx, acct)
	return token, err
}

func loginWithExpiry(ctx context.Context, acct *Account) (string, int64, error) {
	auth := remoteagentv1connect.NewAuthServiceClient(httpClient(), acct.Server)
	ch, err := auth.GetChallenge(ctx, connect.NewRequest(&pb.GetChallengeRequest{DeviceId: acct.DeviceID}))
	if err != nil {
		return "", 0, err
	}
	res, err := auth.Login(ctx, connect.NewRequest(&pb.LoginRequest{ProtocolVersion: relayproto.Version,
		DeviceId: acct.DeviceID, Challenge: ch.Msg.Challenge, Signature: e2e.SignLogin(acct.DeviceKey(), ch.Msg.Challenge),
	}))
	if err != nil {
		return "", 0, err
	}
	return res.Msg.Token, res.Msg.ExpiresMs, nil
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
	b, err := transport.NewRelayPath(ws).Read(ctx)
	if err != nil {
		return nil, err
	}
	f := &pb.ServerFrame{}
	if proto.Unmarshal(b, f) != nil {
		return nil, transport.ErrInvalidFrame
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
// next sync retry the whole republish. Each session uses an independent
// timeout and converged unchanged sessions are skipped on subsequent retries.
func (c *conn) publishCurrentCheckpoint(ctx context.Context, startedEpoch uint64, markerMatches bool) (retErr error) {
	// A full pass is also the retry of checkpoints that failed during ordinary
	// session sync. Individual writers only set this flag; they never clear it.
	c.checkpointPending.Store(false)
	defer func() {
		if retErr != nil {
			c.checkpointPending.Store(true)
		}
	}()
	if !c.publishedInProgress || c.publishedEpoch != startedEpoch {
		if !markerMatches || c.publishedEpoch != startedEpoch {
			c.publishedMachine = nil
			c.publishedSessions = map[string]*pb.Session{}
		}
		c.publishedEpoch = startedEpoch
		c.publishedInProgress = true
	}
	if c.publishedSessions == nil {
		c.publishedSessions = map[string]*pb.Session{}
	}

	opTimeout := c.r.sessionPublishTimeout()
	machineMeta := c.r.backend.MachineMeta()
	if c.publishedMachine == nil || !proto.Equal(c.publishedMachine, machineMeta) {
		mctx, mcancel := context.WithTimeout(ctx, opTimeout)
		err := c.putMachine(mctx, machineMeta)
		mcancel()
		if err != nil {
			return err
		}
		c.publishedMachine = proto.CloneOf(machineMeta)
	}

	ids := c.r.backend.SessionIDs()
	activeIDs := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		activeIDs[id] = struct{}{}
	}
	for id := range c.publishedSessions {
		if _, ok := activeIDs[id]; !ok {
			delete(c.publishedSessions, id)
		}
	}
	c.checkpointMu.Lock()
	for id := range c.dirtyCheckpoints {
		if _, ok := activeIDs[id]; !ok {
			delete(c.dirtyCheckpoints, id)
		}
	}
	c.checkpointMu.Unlock()

	type pendingSession struct {
		id      string
		info    *pb.Session
		curMeta *pb.Session
	}
	var pending []pendingSession
	var firstErr error
	failed := 0
	deferred := 0
	published := 0
	for _, id := range ids {
		info, err := c.r.backend.SessionInfo(id)
		if err != nil {
			failed++
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		curMeta := syncMeta(info)
		if !c.isCheckpointDirty(id) {
			if prev, ok := c.publishedSessions[id]; ok && proto.Equal(prev, curMeta) {
				continue
			}
		}
		pending = append(pending, pendingSession{id: id, info: info, curMeta: curMeta})
	}

	if len(pending) > 0 {
		lctx, lcancel := context.WithTimeout(ctx, opTimeout)
		res, err := c.machine.ListSessionHeads(lctx, connect.NewRequest(&pb.ListSessionHeadsRequest{}))
		lcancel()
		if err != nil {
			return err
		}
		floors := make(map[string]uint64, len(res.Msg.Sessions))
		for _, h := range res.Msg.Sessions {
			floors[h.SessionId] = h.MetaRevision
		}
		for i, p := range pending {
			if err := ctx.Err(); err != nil {
				deferred += len(pending) - i
				if firstErr == nil {
					firstErr = err
				}
				break
			}
			floor := floors[p.id]
			wasDirty := c.isCheckpointDirty(p.id)
			key, err := c.sessionKey(p.id, true)
			if err != nil {
				delete(c.publishedSessions, p.id)
				if wasDirty {
					c.markCheckpointDirty(p.id)
				}
				failed++
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			c.clearCheckpointDirty(p.id)
			sctx, scancel := context.WithTimeout(ctx, opTimeout)
			rev, err := c.putSession(sctx, p.id, key, p.info, floor)
			scancel()
			if err != nil {
				delete(c.publishedSessions, p.id)
				if wasDirty {
					c.markCheckpointDirty(p.id)
				}
				failed++
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			c.noteSentMeta(p.id, key, p.curMeta, rev)
			if c.isCheckpointDirty(p.id) {
				delete(c.publishedSessions, p.id)
				failed++
				if firstErr == nil {
					firstErr = errCheckpointsPending
				}
				continue
			}
			c.publishedSessions[p.id] = p.curMeta
			published++
		}
	}

	if failed > 0 || deferred > 0 {
		slog.Warn("relay: republish sessions incomplete",
			"failed", failed,
			"deferred", deferred,
			"published", published,
			"total", len(ids),
			"first_err", firstErr,
		)
		return &republishBatchError{err: firstErr}
	}
	if c.checkpointPending.Load() || c.hasDirtyCheckpoints() {
		return errCheckpointsPending
	}
	if err := c.writeDisk(func() error { return writePublishedEpoch(startedEpoch) }); err != nil {
		return err
	}
	c.publishedInProgress = false
	return nil
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
	markerMatches := err == nil && head.Core.ContentEpoch == marker
	if markerMatches && !c.checkpointPending.Load() {
		return nil
	}
	return c.publishCurrentCheckpoint(ctx, head.Core.ContentEpoch, markerMatches)
}

func (c *conn) putMachine(ctx context.Context, meta *pb.MachineMeta) error {
	env, err := c.security.sealMessage(&pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_MACHINE_META, MachineId: c.acct.DeviceID, Seq: revision(), Sender: c.acct.DeviceID,
	}, devicesec.MachineMeta, nil, meta)
	if err != nil {
		return err
	}
	_, err = c.machine.PutMachine(ctx, connect.NewRequest(&pb.PutMachineRequest{Meta: env}))
	return err
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
