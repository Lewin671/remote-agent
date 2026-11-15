package direct

import "sync"

// Pool belongs to the daemon process (or one client), never to a socket. Both
// uses and replacement PCs share its reservations until their actual close.
type Pool struct {
	mu       sync.Mutex
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

func NewPool(maxPeers int) *Pool { return &Pool{MaxPeers: maxPeers, peers: map[string]*usage{}} }
func (p *Pool) acquire(peer, owner string, use uint64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	u := p.peers[peer]
	if u == nil {
		if len(p.peers) >= p.MaxPeers {
			return false
		}
		u = &usage{owner: owner, uses: map[uint64]int{}}
		p.peers[peer] = u
	}
	if u.pcs >= 2 || u.owner != owner || u.uses[use] >= 1 {
		return false
	}
	u.pcs++
	u.uses[use]++
	return true
}
func (p *Pool) release(peer string, use uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
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
func (p *Pool) reserve(peer string, direction, bytes, frames int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	u := p.peers[peer]
	if u == nil || bytes < 0 || frames < 0 || u.bytes[direction]+bytes > 2<<20 || u.frames[direction]+frames > 128 || p.total[direction]+bytes > 16<<20 {
		return false
	}
	u.bytes[direction] += bytes
	u.frames[direction] += frames
	p.total[direction] += bytes
	return true
}
func (p *Pool) free(peer string, direction, bytes, frames int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	u := p.peers[peer]
	u.bytes[direction] -= bytes
	u.frames[direction] -= frames
	p.total[direction] -= bytes
	p.prune(peer, u)
}

func (p *Pool) ownerAvailable(peer, owner string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	u := p.peers[peer]
	return u == nil || u.owner == owner
}
