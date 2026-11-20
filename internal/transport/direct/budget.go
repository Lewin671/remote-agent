package direct

import (
	"sync"

	"github.com/Lewin671/remote-agent/shared/linkcore"
)

// Pool is the daemon process's linkcore.Pool. Paths and sessions on many
// goroutines share it, so the daemon guards it with a mutex.
type Pool struct {
	mu   sync.Mutex
	core linkcore.Pool
}

func NewPool(maxPeers int) *Pool { return &Pool{core: linkcore.NewPool(maxPeers)} }

func (p *Pool) acquire(peer, owner string, use uint64) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.core.Acquire(peer, owner, use)
}
func (p *Pool) release(peer string, use uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.core.Release(peer, use)
}
func (p *Pool) reserve(peer string, direction, bytes, frames int) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.core.Reserve(peer, direction, bytes, frames)
}
func (p *Pool) free(peer string, direction, bytes, frames int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.core.Free(peer, direction, bytes, frames)
}
func (p *Pool) reserved(direction int) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.core.Reserved(direction)
}
func (p *Pool) ownerAvailable(peer, owner string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.core.OwnerAvailable(peer, owner)
}
