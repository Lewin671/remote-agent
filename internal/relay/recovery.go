package relay

import (
	"context"
	"errors"
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
)

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
	err               error
}

const tokenRefreshMarginMs = 86_400_000

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
	origin := time.Now()
	elapsed := func() int64 { return time.Since(origin).Milliseconds() }
	var active *mainPath
	var generation uint64
	var dialing bool
	var candidateCancel context.CancelFunc
	var candidateLocalIP *atomic.Pointer[net.IP]
	var everReady bool
	var cachedToken string
	var cachedExpiresMs int64
	var wakeRefreshRequired bool
	var wakeGen uint64
	wanted := true
	results := make(chan dialResult, 1)
	ended := make(chan *mainPath, 2)
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
		if candidateCancel != nil {
			candidateCancel()
		}
		_ = d.security.endpoint.Touch(d.security.now())
		d.security.revalidateStreams()
		d.security.requestSync()
	}
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	lastTick := time.Now()
	for {
		now := time.Now()
		if wanted && !dialing {
			if policy.Take(now) {
				stopRetry()
				r.observe(recoveryDial)
				dialing = true
				wanted = false
				gen := generation
				token := cachedToken
				expiresMs := cachedExpiresMs
				needsLogin := token == "" || time.Now().UnixMilli()+tokenRefreshMarginMs >= expiresMs
				if needsLogin {
					token = ""
					expiresMs = 0
				}
				budget := r.readyTimeout
				if everReady && !needsLogin {
					budget = min(r.readyTimeout, 3*time.Second)
				}
				requireRefresh := wakeRefreshRequired
				attempt, abort := context.WithTimeout(ctx, budget)
				localIPPtr := &atomic.Pointer[net.IP]{}
				candidateCancel = abort
				candidateLocalIP = localIPPtr
				readers.Go(func() {
					res := r.dialMain(attempt, acct, d, requireRefresh, token, expiresMs, localIPPtr)
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
		case _, ok := <-network:
			if !ok {
				network = nil
				continue
			}
			r.observe(recoveryHint)
			d.security.networkChanged()
			generation++
			wanted = true
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
			now = time.Now()
			if now.Sub(lastTick) > transport.SuspectAfter || now.UnixNano()-lastTick.UnixNano() > int64(transport.SuspectAfter) {
				onWake()
			}
			lastTick = now
			if active != nil && active.liveness.Suspect(elapsed()) {
				if !wanted && !dialing {
					r.observe(recoverySuspect)
				}
				wanted = true
			}
		case old := <-ended:
			if websocket.CloseStatus(old.exitErr) == websocket.StatusPolicyViolation && old.token == cachedToken {
				cachedToken = ""
				cachedExpiresMs = 0
			}
			if old != active {
				continue
			}
			r.observe(recoveryLost)
			active = nil
			wanted = true
			r.mu.Lock()
			r.connected = false
			r.mu.Unlock()
		case result := <-results:
			dialing = false
			candidateCancel = nil
			candidateLocalIP = nil
			if result.policyViolation {
				cachedToken = ""
				cachedExpiresMs = 0
			} else if result.token != "" && result.expiresMs > cachedExpiresMs {
				cachedToken = result.token
				cachedExpiresMs = result.expiresMs
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
					continue
				}
			} else if result.generation != generation {
				if result.path != nil {
					result.path.ws.CloseNow()
				}
				wanted = true
				continue
			}
			if result.err != nil {
				policy.Failed(time.Now())
				wanted = true
				continue
			}
			p := result.path
			p.liveness = transport.NewAdaptiveLiveness(elapsed())
			everReady = true
			leg, closeLeg := context.WithCancel(ctx)
			p.cancel = closeLeg
			old := active
			active = p
			policy.Ready()
			wanted = false
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
					activity:     func(isHeartbeat bool) { p.liveness.OnFrame(elapsed(), isHeartbeat) },
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

func (r *Relay) dialMain(ctx context.Context, acct *Account, d *dispatcher, refresh bool, token string, expiresMs int64, dialLocalIP *atomic.Pointer[net.IP]) dialResult {
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
