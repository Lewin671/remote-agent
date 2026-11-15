package transport

// FairQueue is bounded by retained payload bytes and entries, with round robin
// peers and flows within each peer. Its owner supplies synchronization.
type FairQueue[T comparable] struct {
	Entries, Bytes, PeerEntries, PeerBytes int
	EmptyPeerException                     bool
	items                                  []queueItem[T]
	peers                                  []string
	flows                                  map[string][]string
	bytes                                  int
}
type queueItem[T comparable] struct {
	peer, flow string
	bytes      int
	value      T
}

func (q *FairQueue[T]) Push(peer, flow string, size int, value T) bool {
	count, used := 0, 0
	for _, item := range q.items {
		if item.peer == peer {
			count++
			used += item.bytes
		}
	}
	if size < 0 || len(q.items) >= q.Entries || size > q.Bytes-q.bytes || count >= q.PeerEntries {
		return false
	}
	if size > q.PeerBytes-used && !(q.EmptyPeerException && count == 0) {
		return false
	}
	if q.flows == nil {
		q.flows = map[string][]string{}
	}
	if count == 0 {
		q.peers = append(q.peers, peer)
	}
	found := false
	for _, f := range q.flows[peer] {
		if f == flow {
			found = true
		}
	}
	if !found {
		q.flows[peer] = append(q.flows[peer], flow)
	}
	q.items = append(q.items, queueItem[T]{peer, flow, size, value})
	q.bytes += size
	return true
}
func rotate(values []string, i int) []string {
	value := values[i]
	copy(values[i:], values[i+1:])
	values[len(values)-1] = value
	return values
}
func (q *FairQueue[T]) Pop(eligible func(T) bool) (T, bool) {
	for pi, peer := range q.peers {
		for fi, flow := range q.flows[peer] {
			for _, item := range q.items {
				if item.peer != peer || item.flow != flow {
					continue
				}
				if !eligible(item.value) {
					continue
				}
				q.peers = rotate(q.peers, pi)
				q.flows[peer] = rotate(q.flows[peer], fi)
				q.Remove(item.value)
				return item.value, true
			}
		}
	}
	var zero T
	return zero, false
}
func (q *FairQueue[T]) Remove(value T) bool {
	for i, item := range q.items {
		if item.value != value {
			continue
		}
		q.bytes -= item.bytes
		copy(q.items[i:], q.items[i+1:])
		q.items[len(q.items)-1] = queueItem[T]{}
		q.items = q.items[:len(q.items)-1]
		peerLeft, flowLeft := false, false
		for _, other := range q.items {
			if other.peer == item.peer {
				peerLeft = true
				flowLeft = flowLeft || other.flow == item.flow
			}
		}
		if !flowLeft {
			for j, f := range q.flows[item.peer] {
				if f == item.flow {
					q.flows[item.peer] = append(q.flows[item.peer][:j], q.flows[item.peer][j+1:]...)
					break
				}
			}
		}
		if !peerLeft {
			delete(q.flows, item.peer)
			for j, p := range q.peers {
				if p == item.peer {
					q.peers = append(q.peers[:j], q.peers[j+1:]...)
					break
				}
			}
		}
		return true
	}
	return false
}
func (q *FairQueue[T]) Len() int       { return len(q.items) }
func (q *FairQueue[T]) UsedBytes() int { return q.bytes }
