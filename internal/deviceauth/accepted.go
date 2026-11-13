package deviceauth

import (
	"bytes"
	"container/heap"
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/Lewin671/remote-agent/internal/i18n"
)

const (
	maxAccepted         = 65536 // 256 logical bytes per summary, independent of bodies.
	maxAcceptedBytes    = 16 << 20
	maxAcceptedBodies   = 8192
	maxVolatileAccepted = 8192
	maxVolatileBytes    = 16 << 20
	bodyOverhead        = 32 + 128 // key hash and logical entry/index overhead
	acceptedGrace       = uint64(10 * time.Minute / time.Millisecond)
	MaxWaiters          = 128 // Max simultaneous duplicate waiters across all paths.
	defaultWaitTimeout  = 25 * time.Second
)

type acceptanceState uint32

const (
	running acceptanceState = iota
	final
	endedUnknown
)

// Category is durable backend evidence. Wire exposure belongs to transport 0a/6.
type Category uint32

const (
	NoCategory Category = iota
	Executed
	BusinessConflict
)

type accepted struct {
	Deadline, RetainUntil uint64
	PayloadHash           Hash
	State                 acceptanceState
	Category              Category
	Available             bool
	BodyHash              Hash
	Result                []byte
	Persisted             bool
}

// ResultUnavailable preserves a committed category when its body cannot be kept.
// The current wire cannot encode it; callers must not invent a body or reexecute.
type ResultUnavailable struct{ Category Category }

func (*ResultUnavailable) Error() string { return i18n.SecurityResultBodyUnavailable("en") }

type runningCall struct {
	notify      sync.Once
	done        chan struct{}
	payload     Hash
	retainUntil uint64
}

// AcceptOptions separates authentication of old results from new admission.
// Acquire waits outside endpoint locks, only for fresh, authenticated work.
// It must obey ctx; its returned release function must be nonblocking.
// AllowNew is checked under the admission lock, including after slot acquisition.
type AcceptOptions struct {
	AllowNew func() bool
	Acquire  func(ctx context.Context) (release func(), err error)
}

type acceptedExpiry struct {
	key      string
	deadline uint64
}
type acceptedHeap []acceptedExpiry

func (h acceptedHeap) Len() int           { return len(h) }
func (h acceptedHeap) Less(i, j int) bool { return h[i].deadline < h[j].deadline }
func (h acceptedHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *acceptedHeap) Push(v any)        { *h = append(*h, v.(acceptedExpiry)) }
func (h *acceptedHeap) Pop() any          { v := (*h)[len(*h)-1]; *h = (*h)[:len(*h)-1]; return v }
func (h acceptedHeap) expired(i int, now uint64, visit func(string)) {
	if i >= len(h) || h[i].deadline > now {
		return
	}
	visit(h[i].key)
	h.expired(2*i+1, now, visit)
	h.expired(2*i+2, now, visit)
}
func acceptedEntrySize(_ string, a accepted) int {
	if !a.Available {
		return 0
	}
	return bodyOverhead + len(a.Result)
}

// Volatile terminal frames have a separate bounded cache. They cannot consume
// the command body budget, even when their successful result is empty.
func (e *Endpoint) accountAcceptedBody(key string, a accepted, delta int) {
	if !a.Available {
		return
	}
	size := acceptedEntrySize(key, a) * delta
	if a.Persisted {
		e.acceptedBytes += size
		e.acceptedBodies += delta
	} else {
		e.volatileBytes += size
	}
}
func (e *Endpoint) indexAccepted() {
	e.acceptedBytes = 0
	e.acceptedBodies = 0
	e.volatileBytes = 0
	e.volatileExpiry = nil
	e.durableExpiry = nil
	for k, a := range e.data.Accepted {
		e.accountAcceptedBody(k, a, 1)
		h := &e.volatileExpiry
		if a.Persisted {
			h = &e.durableExpiry
		}
		*h = append(*h, acceptedExpiry{k, a.RetainUntil})
	}
	heap.Init(&e.volatileExpiry)
	heap.Init(&e.durableExpiry)
}
func (e *Endpoint) pruneAccepted(h *acceptedHeap, now uint64) {
	for h.Len() > 0 && (*h)[0].deadline <= now {
		k := heap.Pop(h).(acceptedExpiry).key
		e.accountAcceptedBody(k, e.data.Accepted[k], -1)
		delete(e.data.Accepted, k)
	}
}
func acceptedChecksum(key string, a accepted) []byte {
	e := encoder{}
	e.lp([]byte(key))
	e.raw(a.PayloadHash[:])
	e.u64(a.Deadline)
	e.u64(a.RetainUntil)
	e.u32(uint32(a.State))
	e.u32(uint32(a.Category))
	e.u32(uint32(boolInt(a.Available)))
	e.raw(a.BodyHash[:])
	h := digest("ra4/accepted-entry\x00", e.b)
	return h[:]
}
func writeSummary(tx *sql.Tx, key string, a accepted) error {
	_, err := tx.Exec(`INSERT INTO accepted(key,payload_hash,deadline,retain_until,state,category,body_hash,available,checksum) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(key) DO UPDATE SET state=excluded.state,category=excluded.category,body_hash=excluded.body_hash,available=excluded.available,checksum=excluded.checksum`, []byte(key), a.PayloadHash[:], a.Deadline, a.RetainUntil, a.State, a.Category, a.BodyHash[:], boolInt(a.Available), acceptedChecksum(key, a))
	if err != nil {
		return ErrStorage
	}
	return nil
}
func (e *Endpoint) persistAccepted(key string, a accepted, pruned []string, cutoff uint64) error {
	tx, err := e.db.Begin()
	if err != nil {
		return ErrStorage
	}
	defer tx.Rollback()
	for _, k := range pruned {
		if _, err = tx.Exec(`DELETE FROM accepted WHERE key=?`, []byte(k)); err != nil {
			return ErrStorage
		}
		if _, err = tx.Exec(`DELETE FROM accepted_bodies WHERE key=?`, []byte(k)); err != nil {
			return ErrStorage
		}
	}
	if err = writeSummary(tx, key, a); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE accepted_meta SET cutoff=? WHERE id=1`, cutoff); err != nil {
		return ErrStorage
	}
	if err = tx.Commit(); err != nil {
		return ErrStorage
	}
	return nil
}
func loadAccepted(db *sql.DB, r *record) error {
	rows, err := db.Query(`SELECT key,payload_hash,deadline,retain_until,state,category,body_hash,available,checksum FROM accepted`)
	if err != nil {
		return ErrStorage
	}
	for rows.Next() {
		var key, payload, body, sum []byte
		var a accepted
		var avail int
		if err = rows.Scan(&key, &payload, &a.Deadline, &a.RetainUntil, &a.State, &a.Category, &body, &avail, &sum); err != nil {
			rows.Close()
			return ErrStorage
		}
		if len(key) != 32 || len(payload) != 32 || len(body) != 32 || a.Deadline == 0 || a.RetainUntil != a.Deadline+acceptedGrace || a.State > endedUnknown || a.Category > BusinessConflict || avail < 0 || avail > 1 {
			rows.Close()
			return ErrStorage
		}
		if (a.State == final) != (a.Category != NoCategory) || a.State != final && avail != 0 {
			rows.Close()
			return ErrStorage
		}
		copy(a.PayloadHash[:], payload)
		copy(a.BodyHash[:], body)
		a.Available = avail == 1
		a.Persisted = true
		if !bytes.Equal(sum, acceptedChecksum(string(key), a)) {
			rows.Close()
			return ErrStorage
		}
		r.Accepted[string(key)] = a
		if len(r.Accepted) > maxAccepted {
			rows.Close()
			return ErrStorage
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ErrStorage
	}
	// Bodies are best effort and checked independently; damage never loses a category.
	rows, err = db.Query(`SELECT key,result FROM accepted_bodies`)
	if err != nil {
		return ErrStorage
	}
	total, count := 0, 0
	loaded := map[string]bool{}
	for rows.Next() {
		var k, b []byte
		if rows.Scan(&k, &b) != nil {
			rows.Close()
			return ErrStorage
		}
		a, ok := r.Accepted[string(k)]
		if !ok || !a.Available {
			continue
		}
		if len(b) > MaxCipherBytes || digest("ra4/result\x00", b) != a.BodyHash {
			continue
		}
		total += len(b) + bodyOverhead
		count++
		if total > maxAcceptedBytes || count > maxAcceptedBodies {
			rows.Close()
			return ErrStorage
		}
		loaded[string(k)] = true
		a.Result = b
		r.Accepted[string(k)] = a
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ErrStorage
	}
	for k, a := range r.Accepted {
		if a.Available && !loaded[k] {
			a.Available = false
			r.Accepted[k] = a
		}
	}
	if db.QueryRow(`SELECT cutoff FROM accepted_meta WHERE id=1`).Scan(&r.AcceptedFloor) != nil {
		return ErrStorage
	}
	return nil
}
func (e *Endpoint) startAccepted(key string, payload Hash, deadline, now uint64, persist bool) error {
	now = maxU64(now, e.data.AcceptedFloor)
	if deadline <= now {
		return ErrExpired
	}
	var pruned []string
	n := e.volatileExpiry.Len()
	h := &e.volatileExpiry
	if persist {
		n = e.durableExpiry.Len()
		h = &e.durableExpiry
	}
	h.expired(0, now, func(k string) { pruned = append(pruned, k) })
	limit := maxVolatileAccepted
	if persist {
		limit = maxAccepted
	}
	if n-len(pruned) >= limit {
		return ErrCapacity
	}
	retain := deadline
	if persist {
		retain += acceptedGrace
	}
	a := accepted{Deadline: deadline, RetainUntil: retain, PayloadHash: payload, Persisted: persist}
	if persist {
		if err := e.persistAccepted(key, a, pruned, now); err != nil {
			return err
		}
	}
	e.pruneAccepted(h, now)
	e.data.Accepted[key] = a
	heap.Push(h, acceptedExpiry{key, retain})
	e.data.AcceptedFloor = now
	return nil
}

// Try body and summary in one transaction. A body failure falls back to an
// independent summary-only transaction. A failed summary never leaves an orphan
// body consuming unaccounted space, nor a final result visible only in memory.
func (e *Endpoint) persistCompletion(key string, a accepted) error {
	tx, err := e.db.Begin()
	if err != nil {
		return ErrStorage
	}
	defer tx.Rollback()
	if a.Available {
		if _, err = tx.Exec(`INSERT INTO accepted_bodies(key,result) VALUES(?,?)`, []byte(key), append([]byte{}, a.Result...)); err != nil {
			return ErrStorage
		}
	}
	if err = writeSummary(tx, key, a); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return ErrStorage
	}
	return nil
}

func (e *Endpoint) finishAccepted(key string, c Completion, now uint64) error {
	a, ok := e.data.Accepted[key]
	if !ok || a.State != running || now >= a.RetainUntil {
		return ErrOutcomeUnknown
	}
	a.State = endedUnknown
	a.Category = NoCategory
	if c.final {
		a.State = final
		a.Category = c.category
		a.BodyHash = digest("ra4/result\x00", c.result)
	}
	size := bodyOverhead + len(c.result)
	fits := e.volatileBytes+size <= maxVolatileBytes
	if a.Persisted {
		fits = e.acceptedBodies < maxAcceptedBodies && e.acceptedBytes+size <= maxAcceptedBytes
	}
	if c.final && len(c.result) <= MaxCipherBytes-16 && fits {
		a.Available = true
		a.Result = bytes.Clone(c.result)
	}
	if a.Persisted {
		err := e.persistCompletion(key, a)
		if err != nil && a.Available {
			a.Available = false
			a.Result = nil
			err = e.persistCompletion(key, a)
		}
		if err != nil {
			old := e.data.Accepted[key]
			old.State = endedUnknown
			e.data.Accepted[key] = old
			return ErrOutcomeUnknown
		}
	}
	e.accountAcceptedBody(key, a, 1)
	e.data.Accepted[key] = a
	return nil
}
func acceptedKey(h Header) string {
	e := encoder{}
	e.str(h.Account)
	e.raw(h.Domain[:])
	e.str(h.Sender)
	e.u32(uint32(h.Purpose))
	e.str(h.Machine)
	e.str(h.Session)
	e.str(h.Request)
	e.u64(h.Seq)
	hash := digest("ra4/request-key\x00", e.b)
	return string(hash[:])
}
func payloadHash(h Header, plain []byte) (Hash, error) {
	plain, err := Unpack(h, plain)
	if err != nil {
		return Hash{}, err
	}
	e := encoder{}
	e.lp([]byte(acceptedKey(h)))
	e.u32(h.Kind)
	e.u64(h.Deadline)
	e.lp(plain)
	return digest("ra4/authorized-payload\x00", e.b), nil
}
func acceptedResult(a accepted) ([]byte, error) {
	if a.State != final {
		return nil, ErrOutcomeUnknown
	}
	if !a.Available {
		return nil, &ResultUnavailable{Category: a.Category}
	}
	return bytes.Clone(a.Result), nil
}
func (e *Endpoint) AcceptWith(ctx context.Context, w Wire, seed []byte, now uint64, options AcceptOptions, fn func(Header, []byte) (Completion, error)) ([]byte, error) {
	started := time.Now()
	h, plain, err := e.openAuthenticated(w, seed, false, now)
	if err != nil {
		return nil, err
	}
	if !h.Purpose.Control() {
		return nil, ErrInvalid
	}
	payload, err := payloadHash(h, plain)
	if err != nil {
		return nil, err
	}
	key := acceptedKey(h)
	var release func()
	releaseSlot := func() {
		if release != nil {
			release()
			release = nil
		}
	}
	defer releaseSlot()
	var clock uint64
	var clockAt time.Time
	acquired := false
	for {
		e.mu.Lock()
		observedAt := time.Now()
		observed := now + uint64(observedAt.Sub(started)/time.Millisecond)
		if e.closed {
			e.mu.Unlock()
			return nil, ErrStorage
		}
		if a, ok := e.data.Accepted[key]; ok {
			if a.PayloadHash != payload {
				e.mu.Unlock()
				return nil, ErrIdentityConflict
			}
			if run := e.running[key]; a.State == running && run != nil {
				releaseSlot()
				return e.waitLocked(ctx, key, run, observed)
			}
			out, err := acceptedResult(a)
			e.mu.Unlock()
			return out, err
		}
		// A callback may outlive pruning. Its in-memory identity is never evicted.
		if run := e.running[key]; run != nil {
			if run.payload != payload {
				e.mu.Unlock()
				return nil, ErrIdentityConflict
			}
			releaseSlot()
			return e.waitLocked(ctx, key, run, observed)
		}
		if err := ValidateBody(h, plain); err != nil {
			e.mu.Unlock()
			return nil, err
		}
		if options.AllowNew != nil && !options.AllowNew() {
			e.mu.Unlock()
			return nil, ErrUntrusted
		}
		clock = maxU64(maxU64(observed, e.data.Floor), e.data.AcceptedFloor)
		if observed+MaxClockSkew < e.data.Floor {
			e.mu.Unlock()
			return nil, ErrStale
		}
		if h.Deadline <= clock {
			e.mu.Unlock()
			return nil, ErrExpired
		}
		if h.Deadline-clock > 10*60*1000 {
			e.mu.Unlock()
			return nil, ErrInvalid
		}
		if err := leaseValid(e.data, clock); err != nil {
			e.mu.Unlock()
			return nil, err
		}
		if _, err := w.VerifyOrigin(e.data.States[h.Epoch], chainHead(e.data), true, clock); err != nil {
			e.mu.Unlock()
			return nil, err
		}
		if ctx.Err() != nil {
			e.mu.Unlock()
			return nil, ErrOutcomeUnknown
		}

		if options.Acquire == nil || acquired {
			clockAt = observedAt
			break
		}
		// No durable acceptance yet. A duplicate may be admitted while we wait;
		// recheck key/hash, membership, lease, pause and deadline after acquiring.
		remaining := time.Duration(h.Deadline-clock) * time.Millisecond
		e.mu.Unlock()
		waiting, cancel := context.WithTimeout(ctx, remaining)
		release, err = options.Acquire(waiting)
		cancel()
		if err != nil {
			return nil, err
		}
		acquired = true
	}
	e.data.Floor = clock
	if clock-e.data.FloorSaved >= floorWriteInterval {
		if err = e.saveFloorLocked(&e.data); err != nil {
			e.mu.Unlock()
			return nil, err
		}
	}
	readOnly := h.Purpose == Command && ReadOnlyCommand(h, plain)
	if !readOnly {
		if err = e.startAccepted(key, payload, h.Deadline, clock, h.Purpose == Command); err != nil {
			e.mu.Unlock()
			return nil, err
		}
	}
	if readOnly {
		e.mu.Unlock()
		c, _ := fn(h, plain)
		if !c.final {
			return nil, ErrOutcomeUnknown
		}
		return c.result, nil
	}
	if e.running == nil {
		e.running = map[string]*runningCall{}
	}
	run := &runningCall{done: make(chan struct{}), payload: payload, retainUntil: e.data.Accepted[key].RetainUntil}
	e.running[key] = run
	e.mu.Unlock()
	// Elapsed monotonic time prevents a late callback using its old admission time.
	defer func() { e.mu.Lock(); delete(e.running, key); run.notify.Do(func() { close(run.done) }); e.mu.Unlock() }()
	c, _ := fn(h, plain)
	e.mu.Lock()
	defer e.mu.Unlock()
	finished := maxU64(clock+uint64(time.Since(clockAt)/time.Millisecond), e.data.Floor)
	if e.closed {
		return nil, ErrOutcomeUnknown
	}
	if err = e.finishAccepted(key, c, finished); err != nil {
		return nil, ErrOutcomeUnknown
	}
	return acceptedResult(e.data.Accepted[key])
}
func (e *Endpoint) waitLocked(ctx context.Context, key string, run *runningCall, now uint64) ([]byte, error) {
	now = maxU64(maxU64(now, e.data.Floor), e.data.AcceptedFloor)
	if e.waiters >= MaxWaiters || now >= run.retainUntil {
		e.mu.Unlock()
		return nil, ErrOutcomeUnknown
	}
	timeout := e.waitTimeout
	if timeout == 0 {
		timeout = defaultWaitTimeout
	}
	remaining := time.Duration(run.retainUntil-now) * time.Millisecond
	if remaining < timeout {
		timeout = remaining
	}
	e.waiters++
	e.mu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	defer func() { e.mu.Lock(); e.waiters--; e.mu.Unlock() }()
	select {
	case <-ctx.Done():
		return nil, ErrOutcomeUnknown
	case <-timer.C:
		return nil, ErrOutcomeUnknown
	case <-run.done:
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, ErrOutcomeUnknown
	}
	a, ok := e.data.Accepted[key]
	if !ok {
		return nil, ErrOutcomeUnknown
	}
	return acceptedResult(a)
}

var (
	ErrIdentityConflict = errors.New(i18n.SecurityRequestIdentityConflict("en"))
	ErrStorageVersion   = errors.New(i18n.SecurityStorageV4Required("en"))
)
