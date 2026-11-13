package remoteclient

import (
	"crypto/sha256"
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

func (c *Client) beginDispatch(identity string, env *pb.Envelope, prior []DispatchEvidence) {
	c.attemptsMu.Lock()
	defer c.attemptsMu.Unlock()
	history := c.historyLocked(identity)
	never := len(prior) == 1 && prior[0] == NeverDispatched
	fresh := len(prior) == 0 && history.first == weak.Make(env) && history.signature == sha256.Sum256(env.Signature)
	if !never && !fresh {
		history.priorUnknown = true
	}
	history.possible++
}

// A relay refusal removes this attempt only. Restored history and any other
// unresolved or still-running dispatch continue to require the original ID.
func (c *Client) notForwarded(identity string) bool {
	c.attemptsMu.Lock()
	defer c.attemptsMu.Unlock()
	history := c.attempts[identity]
	history.possible--
	return history.priorUnknown || history.possible > 0
}
