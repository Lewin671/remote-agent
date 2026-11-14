package relay

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/transport"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

type mainPath struct {
	ws         *websocket.Conn
	token      string
	generation uint64
	last       atomic.Int64
	cancel     context.CancelFunc
}
type dialResult struct {
	path       *mainPath
	generation uint64
	err        error
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
	policy := transport.Recovery{Budget: r.recoveryBudget}
	origin := time.Now()
	elapsed := func() int64 { return time.Since(origin).Nanoseconds() }
	var active *mainPath
	var generation uint64
	var dialing bool
	var candidateCancel context.CancelFunc
	wanted := true
	results := make(chan dialResult, 1)
	ended := make(chan *mainPath, 2)
	var readers sync.WaitGroup
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
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	lastTick := time.Now()
	for {
		now := time.Now()
		if wanted && !dialing && policy.Take(now) {
			dialing = true
			wanted = false
			gen := generation
			attempt, abort := context.WithTimeout(ctx, min(r.readyTimeout, 3*time.Second))
			candidateCancel = abort
			readers.Go(func() {
				p, err := r.dialMain(attempt, acct, d, gen > 0)
				abort()
				select {
				case results <- dialResult{p, gen, err}:
				case <-ctx.Done():
					if p != nil {
						p.ws.CloseNow()
					}
				}
			})
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-network:
			if !ok {
				network = nil
				continue
			}
			generation++
			wanted = true
			if candidateCancel != nil {
				candidateCancel()
			}
			// Persist observed time before another leg is allowed to receive.
			if err := d.security.endpoint.Touch(d.security.now()); err != nil {
				d.security.revalidateStreams()
			}
			d.security.requestSync()
		case <-tick.C:
			now = time.Now()
			if now.Sub(lastTick) > transport.SuspectAfter || now.UnixNano()-lastTick.UnixNano() > int64(transport.SuspectAfter) {
				generation++
				wanted = true
				if candidateCancel != nil {
					candidateCancel()
				}
				_ = d.security.endpoint.Touch(d.security.now())
				d.security.revalidateStreams()
				d.security.requestSync()
			}
			lastTick = now
			if active != nil && elapsed()-active.last.Load() >= int64(transport.SuspectAfter) {
				wanted = true
			}
		case old := <-ended:
			if old != active {
				continue
			}
			active = nil
			wanted = true
			r.mu.Lock()
			r.connected = false
			r.mu.Unlock()
		case result := <-results:
			dialing = false
			candidateCancel = nil
			if result.generation != generation {
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
			p.generation = generation
			p.last.Store(elapsed())
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
			r.readyCount.Add(1)
			d.ready()
			readers.Go(func() {
				defer closeLeg()
				defer p.ws.CloseNow()
				c := relaySession{pathLifetime: leg, dispatcher: d, path: transport.NewRelayPath(p.ws), activity: func() { p.last.Store(elapsed()) }}
				_ = c.readLoop(leg)
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

func (r *Relay) dialMain(ctx context.Context, acct *Account, d *dispatcher, refresh bool) (*mainPath, error) {
	if err := d.security.endpoint.Touch(d.security.now()); err != nil {
		return nil, err
	}
	if refresh {
		if err := d.security.refresh(ctx); err != nil {
			return nil, err
		}
	}
	token, err := login(ctx, acct)
	if err != nil {
		return nil, err
	}
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(acct.Server, "http")+"/v1/stream", &websocket.DialOptions{HTTPClient: httpClient()})
	if err != nil {
		return nil, err
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
		return nil, err
	}
	f, err := readFrame(ctx, ws)
	if err != nil {
		return nil, err
	}
	if f.GetReady() == nil {
		return nil, errors.New("server did not accept the stream")
	}
	accepted = true
	return &mainPath{ws: ws, token: token}, nil
}
