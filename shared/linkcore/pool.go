package linkcore

// Pool belongs to the daemon process (or one client), never to a socket. Both
// uses and replacement PCs share its reservations until their actual close.
// It does not lock; a pool shared by several paths is guarded by its owner.
type Pool struct {
	MaxPeers int
	peers    map[string]*usage
	total    [2]int
}
type usage struct {
	owner string
	uses  map[uint64]int

	pcs           int
	bytes, frames [2]int
}

func NewPool(maxPeers int) Pool { return Pool{MaxPeers: maxPeers, peers: map[string]*usage{}} }

// Acquire reserves one PC of use for peer, owned by one connection round.
func (p *Pool) Acquire(peer, owner string, use uint64) bool {
	u := p.peers[peer]
	if u == nil {
		if len(p.peers) >= p.MaxPeers {
			return false
		}
		u = &usage{owner: owner, uses: map[uint64]int{}}
		if p.peers == nil {
			p.peers = map[string]*usage{}
		}
		p.peers[peer] = u
	}
	if u.pcs >= 2 || u.owner != owner || u.uses[use] >= 1 {
		return false
	}
	u.pcs++
	u.uses[use]++
	return true
}
func (p *Pool) Release(peer string, use uint64) {
	u := p.peers[peer]
	u.pcs--
	u.uses[use]--
	p.prune(peer, u)
}
func (p *Pool) prune(peer string, u *usage) {
	if u.pcs == 0 && u.bytes == [2]int{} && u.frames == [2]int{} {
		delete(p.peers, peer)
	}
}

// Reserve charges retained bytes and frames in one direction (0 send,
// 1 receive) against the peer and process caps.
func (p *Pool) Reserve(peer string, direction, bytes, frames int) bool {
	u := p.peers[peer]
	if u == nil || bytes < 0 || frames < 0 || u.bytes[direction]+bytes > 2<<20 || u.frames[direction]+frames > 128 || p.total[direction]+bytes > 16<<20 {
		return false
	}
	u.bytes[direction] += bytes
	u.frames[direction] += frames
	p.total[direction] += bytes
	return true
}
func (p *Pool) Free(peer string, direction, bytes, frames int) {
	u := p.peers[peer]
	u.bytes[direction] -= bytes
	u.frames[direction] -= frames
	p.total[direction] -= bytes
	p.prune(peer, u)
}

// Idle reports that no peer holds a PC or a reservation.
func (p *Pool) Idle() bool { return len(p.peers) == 0 && p.total == [2]int{} }

// Reserved is the process total held in one direction.
func (p *Pool) Reserved(direction int) int { return p.total[direction] }

func (p *Pool) OwnerAvailable(peer, owner string) bool {
	u := p.peers[peer]
	return u == nil || u.owner == owner
}
