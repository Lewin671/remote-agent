package deviceauth

// CheckPeer atomically checks both exact member records, the installed head,
// local membership and lease. Direct frame admission uses this same mutex as
// Install/InstallLease/Touch; a stale cached key cannot outlive an installation.
// The caller also checks its original process/connection and sync deadline.
type PeerBinding struct {
	Self, Peer Member
	Head       Hash
}

func (e *Endpoint) CheckPeer(self, peer Member, head Hash, now uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.checkPeerLocked(PeerBinding{self, peer, head}, now)
}
func (e *Endpoint) checkPeerLocked(binding PeerBinding, now uint64) error {
	self, peer, head := binding.Self, binding.Peer, binding.Head
	if e.closed || !e.data.Member || e.data.MemberID != self.ID || self.Kind == peer.Kind {
		return ErrUntrusted
	}
	current := chainHead(e.data)
	hash, err := current.Hash()
	if err != nil || hash != head {
		return ErrUntrusted
	}
	own, ok := current.Core.Member(self.ID)
	if !ok || own != self {
		return ErrUntrusted
	}
	remote, ok := current.Core.Member(peer.ID)
	if !ok || remote != peer {
		return ErrUntrusted
	}
	if now+MaxClockSkew < e.data.Floor {
		return ErrClock
	}
	return leaseValid(e.data, maxU64(now, e.data.Floor))
}
