package linkcore

const (
	// DedupMaxPerPeer bounds the number of verified ciphertext hashes retained per peer.
	DedupMaxPerPeer = 4_096
	// DedupTTLMs is how long a verified ciphertext hash remains in the dedup cache (10 s).
	DedupTTLMs int64 = 10_000
)

type dedupEntry struct {
	hash  [32]byte
	until int64
}

type peerDedup struct {
	seen  map[[32]byte]int64
	order []dedupEntry
}

// CipherDedup is a bounded per-peer verified ciphertext SHA-256 cache (docs/transport.md §3).
// Only frames that have already passed cryptographic verification may be recorded via RecordVerified.
// Unverified frames must never enter this cache. Cache hits drop byte-identical duplicates without
// advancing business or ACK watermarks; evicted hashes fall through to full verification.
type CipherDedup struct {
	maxPerPeer int
	ttlMs      int64
	peers      map[string]*peerDedup
}

// NewCipherDedup creates a CipherDedup with the standard 4,096/peer, 10s bounds.
func NewCipherDedup() *CipherDedup {
	return &CipherDedup{
		maxPerPeer: DedupMaxPerPeer,
		ttlMs:      DedupTTLMs,
		peers:      map[string]*peerDedup{},
	}
}

// Seen reports whether hash was previously recorded as verified for peer and has not expired.
func (d *CipherDedup) Seen(peer string, hash [32]byte, now int64) bool {
	if d == nil || peer == "" {
		return false
	}
	p := d.peers[peer]
	if p == nil {
		return false
	}
	until, ok := p.seen[hash]
	if !ok {
		return false
	}
	if now >= until {
		d.prunePeer(peer, p, now)
		return false
	}
	return true
}

// RecordVerified inserts an already-authenticated ciphertext hash for peer.
func (d *CipherDedup) RecordVerified(peer string, hash [32]byte, now int64) {
	if d == nil || peer == "" {
		return
	}
	if d.maxPerPeer <= 0 {
		d.maxPerPeer = DedupMaxPerPeer
	}
	if d.ttlMs <= 0 {
		d.ttlMs = DedupTTLMs
	}
	if d.peers == nil {
		d.peers = map[string]*peerDedup{}
	}
	p := d.peers[peer]
	if p == nil {
		p = &peerDedup{seen: map[[32]byte]int64{}}
		d.peers[peer] = p
	} else {
		d.prunePeerEntries(p, now)
	}
	if _, exists := p.seen[hash]; exists {
		return
	}
	for len(p.seen) >= d.maxPerPeer && len(p.order) > 0 {
		head := p.order[0]
		p.order = p.order[1:]
		if exp, ok := p.seen[head.hash]; ok && exp == head.until {
			delete(p.seen, head.hash)
		}
	}
	until := now + d.ttlMs
	p.seen[hash] = until
	p.order = append(p.order, dedupEntry{hash: hash, until: until})
}

// ClearPeer removes all cached hashes for peer (e.g., on peer key/instance change or disconnect).
func (d *CipherDedup) ClearPeer(peer string) {
	if d == nil {
		return
	}
	delete(d.peers, peer)
}

// Count returns the number of currently retained hashes for peer.
func (d *CipherDedup) Count(peer string) int {
	if d == nil || d.peers == nil {
		return 0
	}
	p := d.peers[peer]
	if p == nil {
		return 0
	}
	return len(p.seen)
}

func (d *CipherDedup) prunePeerEntries(p *peerDedup, now int64) {
	i := 0
	for i < len(p.order) && now >= p.order[i].until {
		e := p.order[i]
		if exp, ok := p.seen[e.hash]; ok && exp == e.until {
			delete(p.seen, e.hash)
		}
		i++
	}
	if i > 0 {
		p.order = p.order[i:]
	}
}

func (d *CipherDedup) prunePeer(peer string, p *peerDedup, now int64) {
	d.prunePeerEntries(p, now)
	if len(p.seen) == 0 {
		delete(d.peers, peer)
	}
}
