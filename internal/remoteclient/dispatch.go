package remoteclient

import (
	"crypto/sha256"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
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
	first        weak.Pointer[pb.Envelope]
	signature    [sha256.Size]byte
	possible     int
	active       int
	priorUnknown bool
}

func (c *Client) historyLocked(identity string) *dispatchHistory {
	if c.attempts == nil {
		c.attempts = make(map[string]*dispatchHistory)
	}
	if c.attempts[identity] == nil {
		c.attempts[identity] = &dispatchHistory{}
	}
	return c.attempts[identity]
}

func (c *Client) rememberFirstSeal(identity string, env *pb.Envelope) {
	c.attemptsMu.Lock()
	defer c.attemptsMu.Unlock()
	if c.attempts[identity] != nil {
		return // Reusing an ID never creates new provenance or erases history.
	}
	history := c.historyLocked(identity)
	// A weak reference avoids retaining the command ciphertext in the history.
	history.first = weak.Make(env)
	history.signature = sha256.Sum256(env.Signature)
}

// Every logical call owns its preflight lifetime and every network attempt
// owns a distinct delivery. A preflight failure cannot consume another delivery.
type dispatchCall struct {
	client  *Client
	history *dispatchHistory
	closed  bool
}
type dispatchDelivery struct {
	call          *dispatchCall
	sent, settled bool
}

func (c *Client) openCall(env *pb.Envelope, prior []DispatchEvidence) *dispatchCall {
	identity := ""
	if env != nil {
		if h, err := deviceauth.ParseHeader(env.SecurityHeader); err == nil {
			h.Compression = 0
			key, _, _ := deviceauth.RequestIdentity(h, nil)
			identity = string(key[:])
		}
	}
	c.attemptsMu.Lock()
	defer c.attemptsMu.Unlock()
	var history *dispatchHistory
	if identity != "" {
		history = c.historyLocked(identity)
	} else {
		// Invalid headers may still belong to an earlier locally prepared object.
		for _, old := range c.attempts {
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
	return &dispatchCall{client: c, history: history}
}
func (c *dispatchCall) hasPrior() bool {
	c.client.attemptsMu.Lock()
	defer c.client.attemptsMu.Unlock()
	return c.history.priorUnknown || c.history.possible > 0
}
func (c *dispatchCall) uncertain() bool {
	c.client.attemptsMu.Lock()
	defer c.client.attemptsMu.Unlock()
	self := 1
	if c.closed {
		self = 0
	}
	return c.history.priorUnknown || c.history.possible > 0 || c.history.active > self
}
func (c *dispatchCall) close() {
	c.client.attemptsMu.Lock()
	defer c.client.attemptsMu.Unlock()
	if !c.closed {
		c.closed = true
		c.history.active--
	}
}
func (c *dispatchCall) delivery() *dispatchDelivery { return &dispatchDelivery{call: c} }
func (d *dispatchDelivery) dispatch() {
	d.call.client.attemptsMu.Lock()
	defer d.call.client.attemptsMu.Unlock()
	if !d.sent && !d.settled && !d.call.closed {
		d.sent = true
		d.call.history.possible++
	}
}
func (d *dispatchDelivery) notForwarded() bool {
	d.call.client.attemptsMu.Lock()
	if !d.settled {
		d.settled = true
		if d.sent {
			d.call.history.possible--
		}
	}
	d.call.client.attemptsMu.Unlock()
	return d.call.uncertain()
}
