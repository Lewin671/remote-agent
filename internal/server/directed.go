package server

import (
	"context"
	"math"
	"sync"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

// Directed traffic never borrows the push queue's capacity or its slow-reader
// disconnect policy. Reservations include the frame currently being written.
const (
	directedSignalBytes  = 24 << 20
	directedTotalBytes   = 32 << 20
	directedResidence    = time.Second
	directedWriteTimeout = 30 * time.Second
	directedRateKeys     = 4096
)

type directedFrame struct {
	data   []byte
	source *peer
	id     []byte
	until  time.Time
	class  int // signal, control, receipt
}
type directedQueue struct {
	out        [3][]directedFrame
	wake       chan struct{}
	expiryWake chan struct{}
	count      [3]int
	bytes      [3]int
	closed     bool
}
type directedBucket struct {
	at            time.Time
	frames, bytes float64
}
type directedRates struct {
	buckets [2]directedBucket
	touched time.Time
}
type directedHub struct {
	mu                sync.Mutex
	bytes             [2]int
	devices, accounts map[string]*directedRates
	sweep             time.Time
}

func (h *hub) initDirected(p *peer) {
	p.direct.wake = make(chan struct{}, 1)
	p.direct.expiryWake = make(chan struct{}, 1)
}
func (h *hub) directedTarget(source *peer, id string) *peer {
	h.mu.Lock()
	defer h.mu.Unlock()
	if source.dev.kind == pb.DeviceKind_DEVICE_KIND_CLIENT {
		if h.directClients[source.dev.id] != source {
			return nil
		}
		p := h.machines[id]
		if p != nil && p.dev.account == source.dev.account {
			return p
		}
	} else if source.dev.kind == pb.DeviceKind_DEVICE_KIND_MACHINE {
		if h.machines[source.dev.id] != source {
			return nil
		}
		// A device has one current directed receiver. Older client sockets may
		// still receive history, but cannot replace the newest process binding.
		p := h.directClients[id]
		if p != nil && p.dev.account == source.dev.account {
			return p
		}
	}
	return nil
}
func (d *directedHub) allow(source *peer, class, size int, now time.Time) bool {
	ok, _ := d.allowRetryAfter(source, class, size, now)
	return ok
}

func (d *directedHub) allowRetryAfter(source *peer, class, size int, now time.Time) (bool, uint32) {
	if d.devices == nil {
		d.devices = map[string]*directedRates{}
		d.accounts = map[string]*directedRates{}
	}
	if now.Sub(d.sweep) >= time.Minute {
		for _, entries := range []map[string]*directedRates{d.devices, d.accounts} {
			for key, value := range entries {
				if now.Sub(value.touched) >= time.Minute {
					delete(entries, key)
				}
			}
		}
		d.sweep = now
	}
	keys := []string{source.dev.id, source.dev.account}
	maps := []map[string]*directedRates{d.devices, d.accounts}
	var rates [2]*directedRates
	for i, entries := range maps {
		rates[i] = entries[keys[i]]
		if rates[i] == nil {
			if len(entries) >= directedRateKeys {
				return false, 1000
			}
			rates[i] = &directedRates{}
			entries[keys[i]] = rates[i]
		}
	}
	// Account and device reservations are atomic: a rejected frame does not
	// consume one bucket while leaving the other unchanged.
	var next [2]directedBucket
	denied := false
	var needSec float64
	for i, rate := range rates {
		fps, bps, burst := 256.0, float64(4<<20), 256.0
		if class == 1 {
			fps, bps, burst = 512, 512<<10, 1024
		}
		if i == 1 {
			fps, bps, burst = 1024, 16<<20, 1024
			if class == 1 {
				fps, bps, burst = 2048, 4<<20, 2048
			}
		}
		b := rate.buckets[class]
		if b.at.IsZero() {
			b = directedBucket{at: now, frames: burst, bytes: bps}
		}
		elapsed := max(0, now.Sub(b.at).Seconds())
		b.frames = min(burst, b.frames+elapsed*fps)
		b.bytes = min(bps, b.bytes+elapsed*bps)
		b.at = now
		next[i] = b
		rate.touched = now
		if b.frames < 1 {
			denied = true
			needSec = max(needSec, (1-b.frames)/fps)
		}
		if b.bytes < float64(size) {
			denied = true
			needSec = max(needSec, (float64(size)-b.bytes)/bps)
		}
	}
	if denied {
		ms := uint32(math.Ceil(needSec * 1000))
		if ms < 50 {
			ms = 50
		} else if ms > 1000 {
			ms = 1000
		}
		return false, ms
	}
	for i, rate := range rates {
		b := next[i]
		b.frames--
		b.bytes -= float64(size)
		rate.buckets[class] = b
	}
	return true, 0
}
func (h *hub) directed(source *peer, f *pb.DirectedPeerFrame, now time.Time) {
	if len(f.FrameId) != 16 {
		return
	}
	class := int(f.FrameClass) - 1
	if len(f.ToDevice) != 32 || class < 0 || class > 1 || len(f.ProtoReflect().GetUnknown()) != 0 || len(f.Sealed) == 0 {
		h.directedResult(source, f.FrameId, pb.PeerFrameResult_LIMITED, now)
		return
	}
	delivery := &pb.PeerFrameDelivery{FromDevice: []byte(source.dev.id), ToDevice: f.ToDevice, FrameClass: f.FrameClass, FrameId: f.FrameId, Sealed: f.Sealed}
	limit := 64 << 10
	if class == 1 {
		limit = 16 << 10
	}
	if proto.Size(delivery) > limit {
		h.directedResult(source, f.FrameId, pb.PeerFrameResult_LIMITED, now)
		return
	}
	data, err := proto.Marshal(&pb.ServerFrame{Kind: &pb.ServerFrame_PeerFrameDelivery{PeerFrameDelivery: delivery}})
	if err != nil {
		return
	}
	h.direct.mu.Lock()
	allowed, retryAfterMs := h.direct.allowRetryAfter(source, class, len(data), now)
	h.direct.mu.Unlock()
	if !allowed {
		h.directedResultWithRetry(source, f.FrameId, pb.PeerFrameResult_LIMITED, retryAfterMs, now)
		return
	}
	target := h.directedTarget(source, string(f.ToDevice))
	if target == nil {
		h.directedResult(source, f.FrameId, pb.PeerFrameResult_OFFLINE, now)
		return
	}
	q := directedFrame{data: data, source: source, id: append([]byte(nil), f.FrameId...), until: now.Add(directedResidence), class: class}
	h.direct.mu.Lock()
	status := pb.PeerFrameResult_LIMITED
	if target.direct.closed {
		status = pb.PeerFrameResult_OFFLINE
	} else if h.enqueueDirected(target, q) {
		status = pb.PeerFrameResult_UNSPECIFIED
	}
	h.direct.mu.Unlock()
	if status != pb.PeerFrameResult_UNSPECIFIED {
		h.directedResult(source, f.FrameId, status, now)
	}
}

// enqueueDirected is called with direct.mu held; false drops only this frame.
func (h *hub) enqueueDirected(p *peer, q directedFrame) bool {
	c := q.class
	global := min(c, 1)
	n := len(q.data)
	maxCount, maxBytes := 256, 4<<20
	if c == 1 {
		maxCount, maxBytes = 512, 1<<20
	}
	if c == 2 {
		maxCount, maxBytes = 512, 256<<10
	}
	if p.direct.closed || p.direct.count[c] >= maxCount || p.direct.bytes[c]+n > maxBytes || h.direct.bytes[0]+h.direct.bytes[1]+n > directedTotalBytes || (c == 0 && h.direct.bytes[0]+n > directedSignalBytes) {
		return false
	}
	p.direct.out[c] = append(p.direct.out[c], q)
	p.direct.count[c]++
	p.direct.bytes[c] += n
	h.direct.bytes[global] += n
	select {
	case p.direct.wake <- struct{}{}:
	default:
	}
	if len(p.direct.out[c]) == 1 && p.direct.expiryWake != nil {
		select {
		case p.direct.expiryWake <- struct{}{}:
		default:
		}
	}
	return true
}
func (h *hub) directedResult(p *peer, id []byte, status pb.PeerFrameResult_Status, now time.Time) {
	retry := uint32(0)
	if status == pb.PeerFrameResult_LIMITED {
		retry = 1000
	}
	h.directedResultWithRetry(p, id, status, retry, now)
}
func (h *hub) directedResultWithRetry(p *peer, id []byte, status pb.PeerFrameResult_Status, retry uint32, now time.Time) {
	if status != pb.PeerFrameResult_LIMITED {
		retry = 0
	}
	data, err := proto.Marshal(&pb.ServerFrame{Kind: &pb.ServerFrame_PeerFrameResult{PeerFrameResult: &pb.PeerFrameResult{FrameId: id, Status: status, RetryAfterMs: retry}}})
	if err != nil {
		return
	}
	h.direct.mu.Lock()
	defer h.direct.mu.Unlock()
	h.enqueueDirected(p, directedFrame{data: data, until: now.Add(directedResidence), class: 2})
}
func (h *hub) releaseDirected(p *peer, q directedFrame) {
	h.direct.mu.Lock()
	defer h.direct.mu.Unlock()
	if p.direct.closed {
		return
	}
	p.direct.count[q.class]--
	p.direct.bytes[q.class] -= len(q.data)
	h.direct.bytes[min(q.class, 1)] -= len(q.data)
}
func (h *hub) closeDirected(p *peer) {
	var pending []directedFrame
	h.direct.mu.Lock()
	if p.direct.closed {
		h.direct.mu.Unlock()
		return
	}
	p.direct.closed = true
	for c, queue := range p.direct.out {
		h.direct.bytes[min(c, 1)] -= p.direct.bytes[c]
		p.direct.bytes[c] = 0
		p.direct.count[c] = 0
		pending = append(pending, queue...)
		p.direct.out[c] = nil
	}
	h.direct.mu.Unlock()
	for _, q := range pending {
		if q.source != nil {
			h.directedResult(q.source, q.id, pb.PeerFrameResult_OFFLINE, time.Now())
		}
	}
}
func (h *hub) writeDirected(ctx context.Context, conn *websocket.Conn, p *peer, q directedFrame) error {
	defer h.releaseDirected(p, q)
	now := time.Now()
	if !now.Before(q.until) {
		if q.source != nil {
			h.directedResult(q.source, q.id, pb.PeerFrameResult_LIMITED, now)
		}
		return nil
	}
	wctx, cancel := context.WithTimeout(ctx, directedWriteTimeout)
	err := conn.Write(wctx, websocket.MessageBinary, q.data)
	cancel()
	if q.source != nil {
		status := pb.PeerFrameResult_DELIVERED
		if err != nil {
			status = pb.PeerFrameResult_OFFLINE
		}
		h.directedResult(q.source, q.id, status, time.Now())
	}
	return err
}

// The socket writer and residence timer remove frames under the same lock.
// Expiry keeps its one-second bound even while a main push blocks the writer.
func (h *hub) nextDirected(p *peer, next *int) (directedFrame, bool) {
	h.direct.mu.Lock()
	defer h.direct.mu.Unlock()
	if p.direct.closed {
		return directedFrame{}, false
	}
	for n := 0; n < 3; n++ {
		c := (*next + n) % 3
		if len(p.direct.out[c]) == 0 {
			continue
		}
		q := p.direct.out[c][0]
		p.direct.out[c][0] = directedFrame{}
		p.direct.out[c] = p.direct.out[c][1:]
		if len(p.direct.out[c]) == 0 {
			p.direct.out[c] = nil
		}
		*next = (c + 1) % 3
		return q, true
	}
	return directedFrame{}, false
}
func (h *hub) expireDirected(p *peer, now time.Time) {
	var expired []directedFrame
	h.direct.mu.Lock()
	for c := range p.direct.out {
		for len(p.direct.out[c]) > 0 && !now.Before(p.direct.out[c][0].until) {
			q := p.direct.out[c][0]
			p.direct.out[c][0] = directedFrame{}
			p.direct.out[c] = p.direct.out[c][1:]
			p.direct.count[c]--
			p.direct.bytes[c] -= len(q.data)
			h.direct.bytes[min(c, 1)] -= len(q.data)
			expired = append(expired, q)
		}
		if len(p.direct.out[c]) == 0 {
			p.direct.out[c] = nil
		}
	}
	h.direct.mu.Unlock()
	for _, q := range expired {
		if q.source != nil {
			h.directedResult(q.source, q.id, pb.PeerFrameResult_LIMITED, now)
		}
	}
}
func (h *hub) nextDirectedExpiry(p *peer) (time.Time, bool) {
	h.direct.mu.Lock()
	defer h.direct.mu.Unlock()
	if p.direct.closed {
		return time.Time{}, false
	}
	var earliest time.Time
	for c := range p.direct.out {
		if len(p.direct.out[c]) == 0 {
			continue
		}
		u := p.direct.out[c][0].until
		if earliest.IsZero() || u.Before(earliest) {
			earliest = u
		}
	}
	return earliest, !earliest.IsZero()
}
func (h *hub) directedExpiryLoop(ctx context.Context, p *peer) {
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		until, ok := h.nextDirectedExpiry(p)
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-p.direct.expiryWake:
				continue
			}
		}
		wait := time.Until(until)
		if wait <= 0 {
			h.expireDirected(p, time.Now())
			continue
		}
		if timer == nil {
			timer = time.NewTimer(wait)
		} else {
			timer.Reset(wait)
		}
		select {
		case <-ctx.Done():
			return
		case now := <-timer.C:
			h.expireDirected(p, now)
		case <-p.direct.expiryWake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
	}
}
