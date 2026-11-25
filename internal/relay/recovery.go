package relay

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http/httptrace"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/transport"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

type recoveryEvent uint8

const (
	recoveryHint recoveryEvent = iota
	recoverySuspect
	recoveryLost
	recoveryDial
	recoveryReady
	recoveryAborted
)

type dialReason uint8

const (
	dialReasonNone dialReason = iota
	dialReasonInitial
	dialReasonSuspect
	dialReasonLost
	dialReasonNetwork
	dialReasonWake
)

func (d dialReason) String() string {
	switch d {
	case dialReasonInitial:
		return "initial"
	case dialReasonSuspect:
		return "suspect"
	case dialReasonLost:
		return "lost"
	case dialReasonNetwork:
		return "network"
	case dialReasonWake:
		return "wake"
	default:
		return "unknown"
	}
}

func mergeDialReason(cur, next dialReason) dialReason {
	if next > cur {
		return next
	}
	return cur
}

// helloGate coordinates aborting a suspect-triggered replacement dial before
// StreamHello is written (once StreamHello reaches the server, hub.add cancels
// the previous machine leg).
type helloGate struct {
	mu        sync.Mutex
	aborted   bool
	committed bool
}

func (g *helloGate) abort() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.committed {
		return false
	}
	g.aborted = true
	return true
}

func (g *helloGate) commit(allow func() bool) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.aborted {
		return false
	}
	if allow != nil && !allow() {
		g.aborted = true
		return false
	}
	g.committed = true
	return true
}

func (r *Relay) observe(event recoveryEvent) {
	if r.observeRecovery != nil {
		r.observeRecovery(event)
	}
}

type mainPath struct {
	ws        *websocket.Conn
	token     string
	expiresMs int64
	localIP   net.IP
	liveness  *transport.AdaptiveLiveness
	cancel    context.CancelFunc
	exitErr   error
}
type dialResult struct {
	path              *mainPath
	generation        uint64
	token             string
	expiresMs         int64
	securityRefreshed bool
	policyViolation   bool
	aborted           bool
	err               error
}

const (
	tokenRefreshMarginMs = 86_400_000
	wakeJumpThreshold    = 3 * time.Second
)

func (r *Relay) isRouteAvailable(localIP net.IP) bool {
	if r.routeAvailable != nil {
		return r.routeAvailable(localIP)
	}
	return transport.RouteAvailable(localIP)
}

// session supervises at most an active and a candidate relay leg. Only this
// owner publishes Connected and installs terminal credentials. Old readers
// retire independently of the shared endpoint's execution/terminal workers.
func (r *Relay) session(parent context.Context, acct *Account, d *dispatcher) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop := context.AfterFunc(d.maintenance, cancel)
	defer stop()
	watch := r.networkChanges
	if watch == nil {
		watch = transport.NetworkChanges
	}
	network := watch(ctx)
	var wakes <-chan struct{}
	if r.wakeEvents != nil {
		wakes = r.wakeEvents(ctx)
	}
	policy := transport.Recovery{Budget: r.recoveryBudget}
	nowFn := r.recoveryNow
	if nowFn == nil {
		nowFn = time.Now
	}
	origin := nowFn()
	elapsed := func() int64 { return nowFn().Sub(origin).Milliseconds() }
	var active *mainPath
	var generation uint64
	var dialing bool
	var dialingReason dialReason
	var candidateCancel context.CancelFunc
	var candidateLocalIP *atomic.Pointer[net.IP]
	var candidateGate *helloGate
	var everReady bool
	var cachedToken string
	var cachedExpiresMs int64
	var wakeRefreshRequired bool
	var wakeGen uint64
	wanted := true
	wantedReason := dialReasonInitial
	results := make(chan dialResult, 1)
	ended := make(chan *mainPath, 2)
	activeFrame := make(chan *mainPath, 1)
	var readers sync.WaitGroup
	var retryTimer *time.Timer
	var retryC <-chan time.Time
	stopRetry := func() {
		if retryTimer != nil {
			retryTimer.Stop()
			retryTimer = nil
			retryC = nil
		}
	}
	defer stopRetry()
	defer func() {
		cancel()
		if candidateCancel != nil {
			candidateCancel()
		}
		if active != nil {
			active.cancel()
			active.ws.CloseNow()
		}
		readers.Wait()
		// A dial can finish at the same instant cancellation wins the select.
		select {
		case result := <-results:
			if result.path != nil {
				result.path.ws.CloseNow()
			}
		default:
		}
		r.mu.Lock()
		r.connected = false
		r.mu.Unlock()
	}()
	onWake := func() {
		d.security.wake()
		generation++
		wakeRefreshRequired = true
		wakeGen = generation
		wanted = true
		wantedReason = mergeDialReason(wantedReason, dialReasonWake)
		if candidateCancel != nil {
			candidateCancel()
		}
		_ = d.security.endpoint.Touch(d.security.now())
		d.security.revalidateStreams()
		d.security.requestSync()
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	lastWallTick := time.Now()
	for {
		now := nowFn()
		if wanted && !dialing {
			if wantedReason == dialReasonSuspect && active != nil && !active.liveness.Suspect(elapsed()) {
				wanted = false
				wantedReason = dialReasonNone
				stopRetry()
			}
		}
		if wanted && !dialing {
			if policy.Take(now) {
				stopRetry()
				r.observe(recoveryDial)
				dialing = true
				dialingReason = wantedReason
				wanted = false
				wantedReason = dialReasonNone
				gen := generation
				token := cachedToken
				expiresMs := cachedExpiresMs
				needsLogin := token == "" || time.Now().UnixMilli()+tokenRefreshMarginMs >= expiresMs
				if needsLogin {
					token = ""
					expiresMs = 0
				}
				budget := r.readyTimeout
				if everReady && !needsLogin && dialingReason == dialReasonNetwork {
					budget = min(r.readyTimeout, 3*time.Second)
				}
				requireRefresh := wakeRefreshRequired
				attempt, abort := context.WithTimeout(ctx, budget)
				localIPPtr := &atomic.Pointer[net.IP]{}
				gate := &helloGate{}
				var allowHello func() bool
				if dialingReason == dialReasonSuspect && active != nil {
					suspectLeg := active
					suspectFrameAt := suspectLeg.liveness.LastFrameAt()
					allowHello = func() bool {
						return suspectLeg.liveness.LastFrameAt() == suspectFrameAt
					}
				}
				candidateCancel = abort
				candidateLocalIP = localIPPtr
				candidateGate = gate
				readers.Go(func() {
					res := r.dialMain(attempt, acct, d, requireRefresh, token, expiresMs, localIPPtr, gate, allowHello)
					res.generation = gen
					abort()
					if r.beforeDialResult != nil {
						r.beforeDialResult(gen)
					}
					select {
					case results <- res:
					case <-ctx.Done():
						if res.path != nil {
							res.path.ws.CloseNow()
						}
					}
				})
			} else if retryTimer == nil {
				if delay := policy.Delay(now); delay > 0 {
					retryTimer = time.NewTimer(delay)
					retryC = retryTimer.C
				}
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-retryC:
			retryTimer = nil
			retryC = nil
		case p := <-activeFrame:
			if p != active || active == nil {
				continue
			}
			nowMs := elapsed()
			if active.liveness.Suspect(nowMs) {
				continue
			}
			if wanted && wantedReason == dialReasonSuspect {
				wanted = false
				wantedReason = dialReasonNone
				stopRetry()
			}
			if dialing && dialingReason == dialReasonSuspect && candidateGate != nil && candidateGate.abort() {
				if candidateCancel != nil {
					candidateCancel()
				}
				slog.Info("relay: kept active stream leg after traffic resumed",
					"reason", "suspect_recovered",
					"age_ms", active.liveness.AgeMs(nowMs),
				)
			}
		case _, ok := <-network:
			if !ok {
				network = nil
				continue
			}
			r.observe(recoveryHint)
			d.security.networkChanged()
			generation++
			wanted = true
			wantedReason = mergeDialReason(wantedReason, dialReasonNetwork)
			if candidateCancel != nil {
				keepDial := false
				if candidateLocalIP != nil {
					if ip := candidateLocalIP.Load(); ip != nil && r.isRouteAvailable(*ip) {
						keepDial = true
					}
				}
				if !keepDial {
					candidateCancel()
				}
			}
			// Persist observed time before another leg is allowed to receive.
			if err := d.security.endpoint.Touch(d.security.now()); err != nil {
				d.security.revalidateStreams()
			}
			d.security.requestSync()
		case _, ok := <-wakes:
			if !ok {
				wakes = nil
				continue
			}
			onWake()
		case <-tick.C:
			wallNow := time.Now()
			if wallNow.Sub(lastWallTick) > wakeJumpThreshold || wallNow.UnixNano()-lastWallTick.UnixNano() > int64(wakeJumpThreshold) {
				onWake()
			}
			lastWallTick = wallNow
			nowMs := elapsed()
			if active != nil && active.liveness.Suspect(nowMs) {
				if !wanted && !dialing {
					r.observe(recoverySuspect)
					slog.Info("relay: stream leg suspect",
						"reason", dialReasonSuspect.String(),
						"silence_ms", active.liveness.SilenceMs(nowMs),
						"threshold_ms", active.liveness.ThresholdMs(),
						"age_ms", active.liveness.AgeMs(nowMs),
					)
				}
				wanted = true
				wantedReason = mergeDialReason(wantedReason, dialReasonSuspect)
			}
		case old := <-ended:
			if websocket.CloseStatus(old.exitErr) == websocket.StatusPolicyViolation && old.token == cachedToken {
				cachedToken = ""
				cachedExpiresMs = 0
			}
			if old != active {
				continue
			}
			nowMs := elapsed()
			slog.Info("relay: stream leg lost",
				"reason", dialReasonLost.String(),
				"silence_ms", old.liveness.SilenceMs(nowMs),
				"age_ms", old.liveness.AgeMs(nowMs),
				"err", old.exitErr,
			)
			r.observe(recoveryLost)
			active = nil
			wanted = true
			wantedReason = mergeDialReason(wantedReason, dialReasonLost)
			r.mu.Lock()
			r.connected = false
			r.mu.Unlock()
		case result := <-results:
			reason := dialingReason
			dialing = false
			dialingReason = dialReasonNone
			candidateCancel = nil
			candidateLocalIP = nil
			candidateGate = nil
			if result.policyViolation {
				cachedToken = ""
				cachedExpiresMs = 0
			} else if result.token != "" && result.expiresMs > cachedExpiresMs {
				cachedToken = result.token
				cachedExpiresMs = result.expiresMs
			}
			if result.aborted {
				if result.path != nil {
					result.path.ws.CloseNow()
				}
				r.observe(recoveryAborted)
				continue
			}
			if result.err == nil && result.path != nil {
				if result.securityRefreshed && result.generation >= wakeGen {
					wakeRefreshRequired = false
				}
				wakePending := wakeRefreshRequired && result.generation < wakeGen
				keep := !wakePending && (result.generation == generation || r.isRouteAvailable(result.path.localIP))
				if !keep {
					result.path.ws.CloseNow()
					wanted = true
					wantedReason = mergeDialReason(wantedReason, reason)
					continue
				}
			} else if result.generation != generation {
				if result.path != nil {
					result.path.ws.CloseNow()
				}
				wanted = true
				wantedReason = mergeDialReason(wantedReason, reason)
				continue
			}
			if result.err != nil {
				if reason == dialReasonSuspect && active != nil && !active.liveness.Suspect(elapsed()) {
					continue
				}
				policy.Failed(nowFn())
				wanted = true
				wantedReason = mergeDialReason(wantedReason, reason)
				continue
			}
			p := result.path
			nowMs := elapsed()
			p.liveness = transport.NewAdaptiveLiveness(nowMs)
			everReady = true
			leg, closeLeg := context.WithCancel(ctx)
			p.cancel = closeLeg
			old := active
			active = p
			policy.Ready()
			wanted = false
			wantedReason = dialReasonNone
			if old != nil {
				slog.Info("relay: replaced stream leg",
					"reason", reason.String(),
					"silence_ms", old.liveness.SilenceMs(nowMs),
					"age_ms", old.liveness.AgeMs(nowMs),
				)
			}
			d.link.Router.Attach(func(ctx context.Context, route transport.StreamRoute) (transport.Path, error) {
				return openRelayStream(ctx, acct.Server, p.token, route)
			})
			r.mu.Lock()
			r.connected = true
			r.mu.Unlock()
			r.observe(recoveryReady)
			r.readyCount.Add(1)
			d.ready()
			readers.Go(func() {
				defer closeLeg()
				defer p.ws.CloseNow()
				c := relaySession{
					pathLifetime: leg,
					dispatcher:   d,
					path:         transport.NewRelayPath(p.ws),
					activity: func(isHeartbeat bool) {
						p.liveness.OnFrame(elapsed(), isHeartbeat)
						if r.onFrame != nil {
							r.onFrame(isHeartbeat)
						}
						select {
						case activeFrame <- p:
						default:
						}
					},
				}
				p.exitErr = c.readLoop(leg)
				select {
				case ended <- p:
				case <-ctx.Done():
				}
			})
			if old != nil {
				old.cancel()
				old.ws.CloseNow()
			}
		}
	}
}

func (r *Relay) dialMain(ctx context.Context, acct *Account, d *dispatcher, refresh bool, token string, expiresMs int64, dialLocalIP *atomic.Pointer[net.IP], gate *helloGate, allowHello func() bool) dialResult {
	var res dialResult
	if err := d.security.endpoint.Touch(d.security.now()); err != nil {
		res.err = err
		return res
	}
	if refresh {
		if err := d.security.refresh(ctx); err != nil {
			res.err = err
			return res
		}
		res.securityRefreshed = true
	}
	if token == "" {
		var err error
		token, expiresMs, err = loginWithExpiry(ctx, acct)
		if err != nil {
			res.err = err
			return res
		}
		res.token = token
		res.expiresMs = expiresMs
	}
	var localIP net.IP
	dialCtx := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			if addr, ok := info.Conn.LocalAddr().(*net.TCPAddr); ok {
				localIP = addr.IP
				if dialLocalIP != nil {
					ipCopy := append(net.IP(nil), addr.IP...)
					dialLocalIP.Store(&ipCopy)
				}
			}
		},
	})
	ws, _, err := websocket.Dial(dialCtx, "ws"+strings.TrimPrefix(acct.Server, "http")+"/v1/stream", &websocket.DialOptions{HTTPClient: httpClient()})
	if err != nil {
		if gate != nil {
			gate.mu.Lock()
			res.aborted = gate.aborted
			gate.mu.Unlock()
		}
		res.policyViolation = websocket.CloseStatus(err) == websocket.StatusPolicyViolation
		res.err = err
		return res
	}
	accepted := false
	defer func() {
		if !accepted {
			ws.CloseNow()
		}
	}()
	ws.SetReadLimit(4 << 20)
	if r.beforeHello != nil {
		r.beforeHello()
	}
	if gate != nil && !gate.commit(allowHello) {
		res.aborted = true
		return res
	}
	hello, _ := proto.Marshal(&pb.PeerFrame{Kind: &pb.PeerFrame_Hello{Hello: &pb.StreamHello{Token: token}}})
	if err = ws.Write(ctx, websocket.MessageBinary, hello); err != nil {
		res.policyViolation = websocket.CloseStatus(err) == websocket.StatusPolicyViolation
		res.err = err
		return res
	}
	f, err := readFrame(ctx, ws)
	if err != nil {
		res.policyViolation = websocket.CloseStatus(err) == websocket.StatusPolicyViolation
		res.err = err
		return res
	}
	if f.GetReady() == nil {
		res.err = errors.New("server did not accept the stream")
		return res
	}
	accepted = true
	res.path = &mainPath{ws: ws, token: token, expiresMs: expiresMs, localIP: localIP}
	return res
}
