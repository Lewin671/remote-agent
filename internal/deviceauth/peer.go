package deviceauth

import "github.com/Lewin671/remote-agent/shared/devicesec"

// CheckPeer atomically checks both exact member records, the installed head,
// local membership and lease. Direct frame admission uses this same mutex as
// Install/InstallLease/Touch; a stale cached key cannot outlive an installation.
// The caller also checks its original process/connection and sync deadline.
type PeerBinding struct {
	Self, Peer devicesec.Member
	Head       devicesec.Hash
}

func (e *Endpoint) CheckPeer(self, peer devicesec.Member, head devicesec.Hash, now uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.checkPeerLocked(PeerBinding{self, peer, head}, now)
}
func (e *Endpoint) checkPeerLocked(binding PeerBinding, now uint64) error {
	self, peer, head := binding.Self, binding.Peer, binding.Head
	if e.closed || !e.data.Member || e.data.MemberID != self.ID || self.Kind == peer.Kind {
		return devicesec.ErrUntrusted
	}
	current := chainHead(e.data)
	hash, err := current.Hash()
	if err != nil || hash != head {
		return devicesec.ErrUntrusted
	}
	own, ok := current.Core.Member(self.ID)
	if !ok || own != self {
		return devicesec.ErrUntrusted
	}
	remote, ok := current.Core.Member(peer.ID)
	if !ok || remote != peer {
		return devicesec.ErrUntrusted
	}
	if now+devicesec.MaxClockSkew < e.data.Floor {
		return devicesec.ErrClock
	}
	return leaseValid(e.data, max(now, e.data.Floor))
}
