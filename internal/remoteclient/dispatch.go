package remoteclient

import (
	"crypto/sha256"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"math"
	"sync"
	"time"
	"weak"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// DispatchEvidence describes the caller's knowledge before this Forward.
// Omitting it uses conservative provenance tracking; its zero value also
// preserves uncertainty. NeverDispatched is an explicit caller assertion.
type DispatchEvidence uint8

const (
	MayHaveBeenDispatched DispatchEvidence = iota
	NeverDispatched
)

type dispatchHistory struct {
	first               weak.Pointer[pb.Envelope]
	signature           [sha256.Size]byte
	possible, active    int
	priorUnknown        bool
	until, elapsedUntil int64
}

// A detached index lets idle cleanup outlive Client.Close without retaining
// the Client, its endpoint, credentials, or command ciphertext.
type dispatchRegistry struct {
	mu         sync.Mutex
	entries    map[string]*dispatchHistory
	clock      func() (wall, elapsed int64)
	schedule   func(func()) func()
	cancel     func()
	generation uint64
	floor      int64
}

func newDispatchRegistry() *dispatchRegistry {
	started := time.Now()
	return &dispatchRegistry{entries: make(map[string]*dispatchHistory),
		clock:    func() (int64, int64) { return time.Now().UnixMilli(), time.Since(started).Milliseconds() },
		schedule: func(run func()) func() { timer := time.AfterFunc(30*time.Second, run); return func() { timer.Stop() } },
	}
}
func (c *Client) dispatchRegistry() *dispatchRegistry {
	c.attemptsMu.Lock()
	defer c.attemptsMu.Unlock()
	if c.attempts == nil {
		c.attempts = newDispatchRegistry()
	}
	return c.attempts
}
func (r *dispatchRegistry) sweep() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked()
}
func (r *dispatchRegistry) sweepLocked() {
	wall, elapsed := r.clock()
	r.floor = max(r.floor, wall)
	for key, h := range r.entries {
		if h.active == 0 && wall >= h.until && elapsed >= h.elapsedUntil {
			delete(r.entries, key)
		}
	}
	if len(r.entries) == 0 && r.cancel != nil {
		r.generation++
		r.cancel()
		r.cancel = nil
	}
	r.armLocked()
}
func (r *dispatchRegistry) armLocked() {
	if r.cancel != nil || len(r.entries) == 0 {
		return
	}
	r.generation++
	generation := r.generation
	r.cancel = r.schedule(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if generation != r.generation {
			return
		}
		r.cancel = nil
		r.sweepLocked()
	})
}
func (r *dispatchRegistry) historyLocked(identity string, deadline uint64) *dispatchHistory {
	wall, elapsed := r.clock()
	r.floor = max(r.floor, wall)
	grace := deviceauth.ResultRetention.Milliseconds()
	valid := deadline > 0 && deadline <= uint64(math.MaxInt64-grace)
	until := int64(0)
	if valid {
		until = int64(deadline) + grace
	}
	if h := r.entries[identity]; h != nil {
		// A changed declaration may not shorten another copy's known lifetime.
		if valid && until > h.until {
			h.until = until
			h.elapsedUntil = max(h.elapsedUntil, elapsed+max(int64(0), until-wall))
		}
		return h
	}
	h := &dispatchHistory{}
	if valid && until > r.floor {
		h.until = until
		h.elapsedUntil = elapsed + (until - wall)
		r.entries[identity] = h
		r.armLocked()
	} else {
		// An expired/reopened authorization cannot acquire fresh provenance merely
		// because its old index entry was reaped, including after a clock rollback.
		h.priorUnknown = true
	}
	return h
}
func (c *Client) rememberFirstSeal(identity string, env *pb.Envelope, deadline uint64) {
	r := c.dispatchRegistry()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked()
	if r.entries[identity] != nil {
		r.historyLocked(identity, deadline)
		return
	}
	h := r.historyLocked(identity, deadline)
	h.first = weak.Make(env)
	h.signature = sha256.Sum256(env.Signature)
}

// Every logical call owns its preflight lifetime and every network attempt
// owns a distinct delivery. A preflight failure cannot consume another delivery.
type dispatchCall struct {
	registry *dispatchRegistry
	history  *dispatchHistory
	closed   bool
}
type dispatchDelivery struct {
	call          *dispatchCall
	sent, settled bool
}

func (c *Client) openCall(env *pb.Envelope, prior []DispatchEvidence) *dispatchCall {
	identity := ""
	var deadline uint64
	if env != nil {
		if h, err := deviceauth.ParseHeader(env.SecurityHeader); err == nil {
			h.Compression = 0
			key, _, _ := deviceauth.RequestIdentity(h, nil)
			identity = string(key[:])
			deadline = h.Deadline
		}
	}
	r := c.dispatchRegistry()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepLocked()
	var history *dispatchHistory
	if identity != "" {
		history = r.historyLocked(identity, deadline)
	} else {
		// Invalid headers may still belong to an earlier locally prepared object.
		for _, old := range r.entries {
			if env != nil && old.first == weak.Make(env) {
				history = old
				break
			}
		}
		if history == nil {
			history = &dispatchHistory{priorUnknown: true}
		}
	}
	never := len(prior) == 1 && prior[0] == NeverDispatched
	fresh := env != nil && len(prior) == 0 && history.first == weak.Make(env) && history.signature == sha256.Sum256(env.Signature)
	if !never && !fresh {
		history.priorUnknown = true
	}
	history.active++
	return &dispatchCall{registry: r, history: history}
}
func (c *dispatchCall) hasPrior() bool {
	c.registry.mu.Lock()
	defer c.registry.mu.Unlock()
	return c.history.priorUnknown || c.history.possible > 0
}
func (c *dispatchCall) uncertain() bool {
	c.registry.mu.Lock()
	defer c.registry.mu.Unlock()
	self := 1
	if c.closed {
		self = 0
	}
	return c.history.priorUnknown || c.history.possible > 0 || c.history.active > self
}
func (c *dispatchCall) close() {
	c.registry.mu.Lock()
	defer c.registry.mu.Unlock()
	if !c.closed {
		c.closed = true
		c.history.active--
		c.registry.sweepLocked()
	}
}
func (c *dispatchCall) delivery() *dispatchDelivery { return &dispatchDelivery{call: c} }
func (d *dispatchDelivery) dispatch() {
	d.call.registry.mu.Lock()
	defer d.call.registry.mu.Unlock()
	if !d.sent && !d.settled && !d.call.closed {
		d.sent = true
		d.call.history.possible++
	}
}
func (d *dispatchDelivery) notForwarded() bool {
	d.call.registry.mu.Lock()
	if !d.settled {
		d.settled = true
		if d.sent {
			d.call.history.possible--
		}
	}
	d.call.registry.mu.Unlock()
	return d.call.uncertain()
}
