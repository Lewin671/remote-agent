package deviceauth

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Lewin671/remote-agent/shared/devicesec"
)

// The endpoint stores verified states, content keys, a time floor and the
// at-most-once acceptance log in SQLite. Sealing does not write storage.
const storageVersion = 4

const storeSchema = `
CREATE TABLE IF NOT EXISTS security (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	version INTEGER NOT NULL,
	pin_account TEXT NOT NULL,
	pin_domain BLOB NOT NULL,
	pin_genesis BLOB NOT NULL,
	member_id TEXT NOT NULL,
	ed_seed BLOB NOT NULL,
	x_private BLOB NOT NULL,
	secret BLOB,
	floor_ms INTEGER NOT NULL,
	floor_saved_ms INTEGER NOT NULL,
	member INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS lease (id INTEGER PRIMARY KEY CHECK(id=1), certificate BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS states (
	epoch INTEGER PRIMARY KEY,
	state BLOB NOT NULL,
	hash BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS recovery (
	epoch INTEGER PRIMARY KEY,
	statement BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS content_keys (
	content_epoch INTEGER PRIMARY KEY,
	secret BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS accepted (
 key BLOB PRIMARY KEY CHECK(length(key)=32),
 payload_hash BLOB NOT NULL CHECK(length(payload_hash)=32),
 deadline INTEGER NOT NULL,
 retain_until INTEGER NOT NULL,
 state INTEGER NOT NULL,
 category INTEGER NOT NULL,
 body_hash BLOB NOT NULL CHECK(length(body_hash)=32),
 available INTEGER NOT NULL,
 checksum BLOB NOT NULL CHECK(length(checksum)=32)
);
CREATE TABLE IF NOT EXISTS accepted_bodies (key BLOB PRIMARY KEY, result BLOB NOT NULL);
CREATE TABLE IF NOT EXISTS accepted_meta (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	cutoff INTEGER NOT NULL
);
`

// floorWriteInterval is how often the receive time floor is persisted in the
// background. Control envelopes advance Floor in memory on every admission;
// persistent write commands coalesce the security table update into their
// acceptance transaction when at least this interval has elapsed, while read-only
// and volatile requests never wait on SQLite on the hot path. A crash loses at
// most one flush interval plus one in-flight background flush, which only ever
// lowers the floor (never resurrects an expired lease once loaded).
const floorWriteInterval = uint64(time.Second / time.Millisecond)

type record struct {
	Lease         *devicesec.LeaseCertificate
	Version       int
	Pin           devicesec.Pin
	States        map[uint64]devicesec.State
	HeadEpoch     uint64
	MemberID      string
	EdSeed        []byte
	XPrivate      []byte
	Secret        []byte
	Member        bool
	Floor         uint64
	FloorSaved    uint64
	ContentKeys   map[uint64][]byte
	Recovery      map[uint64]devicesec.RecoveryStatement
	Accepted      map[string]accepted
	AcceptedFloor uint64
}

type Endpoint struct {
	execution            sync.Mutex
	floorWriteMu         sync.Mutex
	mu                   sync.Mutex
	path                 string
	db                   *sql.DB
	closed               bool
	data                 record
	acceptedBytes        int
	acceptedBodies       int
	volatileBytes        int
	volatileExpiry       acceptedHeap
	durableExpiry        acceptedHeap
	running              map[string]*runningCall
	waiters              int
	externalWaiters      map[string]int
	externalWaitersTotal int
	// Tests can shorten the production wait bound without sleeping.
	waitTimeout time.Duration
	floorStop   chan struct{}
	floorDone   chan struct{}
}

func chainHead(r record) devicesec.State { return r.States[r.HeadEpoch] }

func verifyChain(pin devicesec.Pin, states []devicesec.State, proofs []*devicesec.RecoveryStatement) error {
	if len(states) == 0 || len(states) > devicesec.MaxStates || len(proofs) != len(states) {
		return devicesec.ErrInvalid
	}
	if pin.VerifyGenesis(states[0]) != nil {
		return devicesec.ErrUntrusted
	}
	now := uint64(time.Now().UnixMilli())
	for i, s := range states {
		if s.Core.IssuedAt > now+devicesec.MaxClockSkew {
			return devicesec.ErrClock
		}
		if i > 0 {
			if err := devicesec.VerifyTransition(states[i-1], s, proofs[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

var memSeq atomic.Uint64

func dsn(path string) string {
	if path == "" {
		// A named shared memory database keeps its contents across the
		// database/sql connection lifetime; a bare ":memory:" is private to
		// one connection and would vanish when the pool opens another.
		return fmt.Sprintf("file:ra-mem-%d?mode=memory&cache=shared&_pragma=journal_mode(MEMORY)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)", memSeq.Add(1))
	}
	return "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
}

func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, ErrStorage
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func createStore(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(storeSchema); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO security (id, version, pin_account, pin_domain, pin_genesis, member_id, ed_seed, x_private, floor_ms, floor_saved_ms, member) VALUES (1, ?, '', X'', X'', '', X'', X'', 0, 0, 0)`, storageVersion); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO accepted_meta (id, cutoff) VALUES (1, 0)`); err != nil {
		return err
	}
	return tx.Commit()
}

// InitEndpoint verifies a chain from the pinned genesis and stores the local
// device state. An empty path keeps it in memory.
func InitEndpoint(path string, pin devicesec.Pin, states []devicesec.State, proofs []*devicesec.RecoveryStatement, id string, edSeed, xPrivate []byte, leases ...devicesec.LeaseCertificate) (*Endpoint, error) {
	if len(edSeed) != 32 || devicesec.ID(ed25519.NewKeyFromSeed(edSeed).Public().(ed25519.PublicKey)) != id || len(xPrivate) != 32 {
		return nil, devicesec.ErrInvalid
	}
	if err := verifyChain(pin, states, proofs); err != nil {
		return nil, err
	}
	head := states[len(states)-1]
	secret, err := OpenGrant(pin, head, id, xPrivate)
	if err != nil {
		return nil, err
	}
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, ErrStorage
		}
		if _, err := os.Stat(path); err == nil {
			// Re-pair only on a strict extension of the verified local chain.
			// A revoked device must join with a new identity. Neither path
			// erases local execution history or the receive-time floor.
			existing, err := OpenEndpoint(path)
			if err != nil {
				return nil, err
			}
			_, oldStillMember := head.Core.Member(existing.data.MemberID)
			sameIdentity := existing.data.MemberID == id && bytes.Equal(existing.data.EdSeed, edSeed)
			if existing.data.Pin != pin || (!sameIdentity && oldStillMember) || head.Core.Epoch <= existing.data.HeadEpoch {
				existing.Close()
				return nil, devicesec.ErrUntrusted
			}
			if err := existing.CheckExtension(states); err != nil {
				existing.Close()
				return nil, err
			}
			next, err := InitEndpoint("", pin, states, proofs, id, edSeed, xPrivate, leases...)
			if err != nil {
				existing.Close()
				return nil, err
			}
			defer next.Close()
			next.data.Accepted = existing.data.Accepted
			next.data.AcceptedFloor = existing.data.AcceptedFloor
			next.data.Floor, next.data.FloorSaved = existing.data.Floor, existing.data.FloorSaved
			for epoch, key := range existing.data.ContentKeys {
				next.data.ContentKeys[epoch] = bytes.Clone(key)
			}
			prior := existing.data
			existing.data = next.data
			if err := existing.persistRecord(false); err != nil {
				existing.data = prior
				existing.Close()
				return nil, err
			}
			return existing, nil
		} else if !os.IsNotExist(err) {
			return nil, ErrStorage
		}
	}

	dbPath := path
	if path != "" {
		dir, err := os.MkdirTemp(filepath.Dir(path), ".security-init-")
		if err != nil {
			return nil, ErrStorage
		}
		defer os.RemoveAll(dir)
		dbPath = filepath.Join(dir, "security.db")
	}
	db, err := openDB(dbPath)
	if err != nil {
		return nil, err
	}
	if err := createStore(db); err != nil {
		db.Close()
		return nil, ErrStorage
	}
	r := record{Version: storageVersion, Pin: pin, States: map[uint64]devicesec.State{}, HeadEpoch: head.Core.Epoch, MemberID: id, EdSeed: bytes.Clone(edSeed), XPrivate: bytes.Clone(xPrivate), Secret: secret, Member: true, ContentKeys: map[uint64][]byte{}, Recovery: map[uint64]devicesec.RecoveryStatement{}, Accepted: map[string]accepted{}}
	for i, s := range states {
		r.States[s.Core.Epoch] = s
		if proofs[i] != nil {
			r.Recovery[s.Core.Epoch] = *proofs[i]
		}
	}
	if len(leases) > 0 {
		l := leases[0]
		if err := l.Verify(head); err != nil {
			db.Close()
			return nil, err
		}
		r.Lease = &l
	}
	e := &Endpoint{path: dbPath, db: db, data: r}
	e.indexAccepted()
	if err := e.cacheChainKeys(); err != nil {
		db.Close()
		return nil, err
	}
	if err := e.persistAll(); err != nil {
		db.Close()
		return nil, err
	}
	if path != "" {
		if err := publishEndpoint(e, path); err != nil {
			e.Close()
			return nil, err
		}
		return OpenEndpoint(path)
	}
	return e, nil
}

// Publish only a complete, standalone database. Linking gives atomic creation
// without overwriting an endpoint that another initializer published first.
func publishEndpoint(e *Endpoint, path string) error {
	var busy, frames, checkpointed int
	if err := e.db.QueryRow(`PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &frames, &checkpointed); err != nil || busy != 0 {
		return ErrStorage
	}
	if err := e.Close(); err != nil {
		return ErrStorage
	}
	f, err := os.OpenFile(e.path, os.O_RDWR, 0)
	if err != nil {
		return ErrStorage
	}
	err = f.Chmod(0600)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return ErrStorage
	}
	if err := os.Link(e.path, path); err != nil {
		return ErrStorage
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return ErrStorage
	}
	err = dir.Sync()
	closeErr = dir.Close()
	if err != nil || closeErr != nil {
		return ErrStorage
	}
	return nil
}

// persistAll writes the header and every state of a fresh endpoint in one
// transaction, so a start cut short never leaves a partial chain.
func (e *Endpoint) persistAll() error {
	return e.persistRecord(true)
}

// Re-pair updates verified state without writing the live command log.
func (e *Endpoint) persistRecord(writeAccepted bool) error {
	d := e.data
	tx, err := e.db.Begin()
	if err != nil {
		return ErrStorage
	}
	defer tx.Rollback()
	if !writeAccepted {
		var epoch uint64
		var hash []byte
		if err := tx.QueryRow(`SELECT epoch,hash FROM states ORDER BY epoch DESC LIMIT 1`).Scan(&epoch, &hash); err != nil {
			return ErrStorage
		}
		state, ok := d.States[epoch]
		if !ok {
			return devicesec.ErrStale
		}
		h, err := state.Hash()
		if err != nil || !bytes.Equal(h[:], hash) {
			return devicesec.ErrStale
		}
		if err := tx.QueryRow(`SELECT MAX(floor_ms,?), MAX(floor_saved_ms,?) FROM security WHERE id=1`, d.Floor, d.FloorSaved).Scan(&d.Floor, &d.FloorSaved); err != nil {
			return ErrStorage
		}
		// A newer same-head lease survives a stale full-record write.
		var raw []byte
		err = tx.QueryRow(`SELECT certificate FROM lease WHERE id=1`).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return ErrStorage
		}
		if len(raw) > 0 {
			l, err := devicesec.ParseLease(raw)
			if err != nil {
				return err
			}
			if d.Lease == nil || (d.Lease.Head == l.Head && (l.IssuedAt > d.Lease.IssuedAt || l.NotAfter > d.Lease.NotAfter || (l.IssuedAt == d.Lease.IssuedAt && l.NotAfter != d.Lease.NotAfter))) {
				d.Lease = &l
			}
		}
	}
	if _, err := tx.Exec(`UPDATE security SET version=?, pin_account=?, pin_domain=?, pin_genesis=?, member_id=?, ed_seed=?, x_private=?, secret=?, floor_ms=MAX(floor_ms,?), floor_saved_ms=MAX(floor_saved_ms,?), member=? WHERE id=1`,
		d.Version, d.Pin.Account, d.Pin.Domain[:], d.Pin.Genesis[:], d.MemberID, d.EdSeed, d.XPrivate, d.Secret, d.Floor, d.FloorSaved, boolInt(d.Member)); err != nil {
		return ErrStorage
	}
	if d.Lease != nil {
		raw, err := d.Lease.MarshalBinary()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO lease(id,certificate) VALUES(1,?)`, raw); err != nil {
			return ErrStorage
		}
	}
	for ep, s := range d.States {
		raw, err := s.MarshalBinary()
		if err != nil {
			return err
		}
		h, err := s.Hash()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO states (epoch, state, hash) VALUES (?, ?, ?)`, ep, raw, h[:]); err != nil {
			return ErrStorage
		}
	}
	for ep, st := range d.Recovery {
		raw, err := st.MarshalBinary()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO recovery (epoch, statement) VALUES (?, ?)`, ep, raw); err != nil {
			return ErrStorage
		}
	}
	for ep, key := range d.ContentKeys {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO content_keys (content_epoch, secret) VALUES (?, ?)`, ep, key); err != nil {
			return ErrStorage
		}
	}
	// Re-pair can overlap the old daemon finishing or pruning commands.
	// Leave the entire live log and its pruning floor untouched in that case.
	if !writeAccepted {
		if err := tx.Commit(); err != nil {
			return ErrStorage
		}
		e.data.Floor, e.data.FloorSaved, e.data.Lease = d.Floor, d.FloorSaved, d.Lease
		return nil
	}
	for key, a := range d.Accepted {
		if !a.Persisted {
			continue
		}
		if err := writeSummary(tx, key, a); err != nil {
			return err
		}
		if a.Available {
			if _, err := tx.Exec(`INSERT OR REPLACE INTO accepted_bodies(key,result) VALUES(?,?)`, []byte(key), a.Result); err != nil {
				return ErrStorage
			}
		}
	}
	if _, err := tx.Exec(`INSERT INTO accepted_meta (id, cutoff) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET cutoff=MAX(cutoff,excluded.cutoff)`, d.AcceptedFloor); err != nil {
		return ErrStorage
	}
	if err := tx.Commit(); err != nil {
		return ErrStorage
	}
	return nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// OpenEndpoint loads the chain and validates it from the pinned genesis.
func OpenEndpoint(path string) (*Endpoint, error) {
	if path == "" {
		return nil, ErrStorage
	}
	if _, err := os.Stat(path); err != nil {
		return nil, ErrStorage
	}
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	r := record{States: map[uint64]devicesec.State{}, Recovery: map[uint64]devicesec.RecoveryStatement{}, Accepted: map[string]accepted{}, ContentKeys: map[uint64][]byte{}}
	var member int
	var pinDomain, pinGenesis []byte
	err = db.QueryRow(`SELECT version, pin_account, pin_domain, pin_genesis, member_id, ed_seed, x_private, secret, floor_ms, floor_saved_ms, member FROM security WHERE id=1`).
		Scan(&r.Version, &r.Pin.Account, &pinDomain, &pinGenesis, &r.MemberID, &r.EdSeed, &r.XPrivate, &r.Secret, &r.Floor, &r.FloorSaved, &member)
	if errors.Is(err, sql.ErrNoRows) {
		db.Close()
		return nil, ErrStorage
	}
	if err != nil {
		db.Close()
		return nil, ErrStorage
	}
	if r.Version != storageVersion {
		db.Close()
		return nil, ErrStorageVersion
	}
	if len(pinDomain) != 32 || len(pinGenesis) != 32 || len(r.EdSeed) != 32 || len(r.XPrivate) != 32 {
		db.Close()
		return nil, ErrStorage
	}
	copy(r.Pin.Domain[:], pinDomain)
	copy(r.Pin.Genesis[:], pinGenesis)
	r.Member = member != 0
	if err := loadAccepted(db, &r); err != nil {
		db.Close()
		return nil, err
	}
	rows, err := db.Query(`SELECT epoch, state FROM states ORDER BY epoch`)
	if err != nil {
		db.Close()
		return nil, ErrStorage
	}
	for rows.Next() {
		var ep uint64
		var raw []byte
		if err := rows.Scan(&ep, &raw); err != nil {
			rows.Close()
			db.Close()
			return nil, ErrStorage
		}
		s, err := devicesec.ParseState(raw)
		if err != nil || s.Core.Epoch != ep {
			rows.Close()
			db.Close()
			return nil, ErrStorage
		}
		r.States[ep] = s
		if ep > r.HeadEpoch {
			r.HeadEpoch = ep
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		db.Close()
		return nil, ErrStorage
	}
	rows.Close()
	rows, err = db.Query(`SELECT epoch, statement FROM recovery`)
	if err != nil {
		db.Close()
		return nil, ErrStorage
	}
	for rows.Next() {
		var ep uint64
		var raw []byte
		if err := rows.Scan(&ep, &raw); err != nil {
			rows.Close()
			db.Close()
			return nil, ErrStorage
		}
		st, err := devicesec.ParseRecoveryStatement(raw)
		if err != nil {
			rows.Close()
			db.Close()
			return nil, ErrStorage
		}
		r.Recovery[ep] = st
	}
	rows.Close()
	rows, err = db.Query(`SELECT content_epoch, secret FROM content_keys`)
	if err != nil {
		db.Close()
		return nil, ErrStorage
	}
	for rows.Next() {
		var ep uint64
		var key []byte
		if err := rows.Scan(&ep, &key); err != nil || len(key) != 32 {
			rows.Close()
			db.Close()
			return nil, ErrStorage
		}
		r.ContentKeys[ep] = key
	}
	rows.Close()
	var leaseRaw []byte
	if err := db.QueryRow(`SELECT certificate FROM lease WHERE id=1`).Scan(&leaseRaw); err == nil {
		l, err := devicesec.ParseLease(leaseRaw)
		if err != nil {
			db.Close()
			return nil, ErrStorage
		}
		// A lease for an older installed head remains on disk until replacement.
		// Verify it against the exact state it binds, never treat it as current.
		found := false
		for _, st := range r.States {
			h, _ := st.Hash()
			if h == l.Head && l.Verify(st) == nil {
				found = true
				break
			}
		}
		if !found {
			db.Close()
			return nil, ErrStorage
		}
		r.Lease = &l
	} else if !errors.Is(err, sql.ErrNoRows) {
		db.Close()
		return nil, ErrStorage
	}
	e := &Endpoint{path: path, db: db, data: r}
	e.indexAccepted()
	if err := e.validate(); err != nil {
		db.Close()
		return nil, err
	}
	return e, nil
}

func (e *Endpoint) validate() error {
	r := e.data
	if len(r.States) == 0 || r.HeadEpoch == 0 || r.States[r.HeadEpoch].Core.Epoch != r.HeadEpoch {
		return ErrStorage
	}
	if r.Pin.VerifyGenesis(r.States[1]) != nil {
		return ErrStorage
	}
	var prev devicesec.State
	for ep := uint64(1); ep <= r.HeadEpoch; ep++ {
		s, ok := r.States[ep]
		if !ok {
			return ErrStorage
		}
		if ep == 1 {
			if r.Pin.VerifyGenesis(s) != nil {
				return ErrStorage
			}
		} else {
			var proof *devicesec.RecoveryStatement
			if p, ok := r.Recovery[ep]; ok {
				proof = &p
			}
			if devicesec.VerifyTransition(prev, s, proof) != nil {
				return ErrStorage
			}
		}
		prev = s
	}
	if r.MemberID != devicesec.ID(ed25519.NewKeyFromSeed(r.EdSeed).Public().(ed25519.PublicKey)) {
		return ErrStorage
	}
	head := chainHead(r)
	_, present := head.Core.Member(r.MemberID)
	if present != r.Member {
		return ErrStorage
	}
	if r.Member {
		if len(r.Secret) != 32 {
			return ErrStorage
		}
		secret, err := OpenGrant(r.Pin, head, r.MemberID, r.XPrivate)
		if err != nil || !bytes.Equal(secret, r.Secret) {
			return ErrStorage
		}
	}
	return nil
}

// Save writes the endpoint to path with a fresh database, replacing any
// previous file there. It is used when an in-memory endpoint from a pairing
// join is committed to disk.
func (e *Endpoint) Save(path string) error {
	e.execution.Lock()
	defer e.execution.Unlock()
	e.floorWriteMu.Lock()
	defer e.floorWriteMu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return ErrStorage
	}
	if path == "" {
		e.path = path
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return ErrStorage
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return ErrStorage
		}
	}
	db, err := openDB(path)
	if err != nil {
		return err
	}
	if err := createStore(db); err != nil {
		db.Close()
		return ErrStorage
	}
	if e.db != nil {
		_ = e.db.Close()
	}
	e.db, e.path = db, path
	return e.persistAll()
}

func (e *Endpoint) Close() error {
	e.execution.Lock()
	defer e.execution.Unlock()
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	stop, done := e.floorStop, e.floorDone
	e.floorStop, e.floorDone = nil, nil
	// Retain the closed handle so concurrent storage users receive SQL errors.
	e.closed = true
	for _, run := range e.running {
		run.notify.Do(func() { close(run.done) })
	}
	e.mu.Unlock()

	if stop != nil {
		close(stop)
		<-done
	}

	e.floorWriteMu.Lock()
	defer e.floorWriteMu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.db != nil && e.data.Floor > e.data.FloorSaved {
		d := e.data
		if err := e.saveFloorLocked(&d); err == nil {
			e.data = d
		}
	}
	return e.db.Close()
}
func (e *Endpoint) Snapshot() (devicesec.Pin, devicesec.State, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.data.Pin, chainHead(e.data), e.data.MemberID
}

// LeaseExpiration reports the certificate installed for this exact head.
// Expired certificates retain their real expiration for diagnostics.
func (e *Endpoint) LeaseExpiration() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	if l := e.data.Lease; l != nil && l.Verify(chainHead(e.data)) == nil {
		return l.NotAfter
	}
	return 0
}

// VerifiedStates returns a detached, ordered copy for historical origin proofs.
func (e *Endpoint) VerifiedStates() []devicesec.State {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]devicesec.State, 0, len(e.data.States))
	states := cloneStates(e.data.States)
	for epoch := uint64(1); epoch <= e.data.HeadEpoch; epoch++ {
		out = append(out, states[epoch])
	}
	return out
}

func (e *Endpoint) Floor() uint64 { e.mu.Lock(); defer e.mu.Unlock(); return e.data.Floor }

// LastRecoveryEpoch is the newest state installed through a recovery
// statement, or 0 when the chain has none.
func (e *Endpoint) LastRecoveryEpoch() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	var last uint64
	for epoch := range e.data.Recovery {
		last = max(last, epoch)
	}
	return last
}
func (e *Endpoint) HeadEpoch() uint64 { e.mu.Lock(); defer e.mu.Unlock(); return e.data.HeadEpoch }
func (e *Endpoint) Removed() bool     { e.mu.Lock(); defer e.mu.Unlock(); return !e.data.Member }

// Install appends a contiguous chain segment. proofs[i] is set only when
// states[i] is a recovery transition. The new head, states and keys are
// persisted in one transaction before use.
func (e *Endpoint) Install(states []devicesec.State, proofs []*devicesec.RecoveryStatement) error {
	return e.InstallWithLease(states, proofs, nil, 0)
}

// InstallWithLease appends a contiguous chain segment and, when lease is
// non-nil, verifies and commits the matching lease certificate in the same
// SQLite transaction and mutex acquisition so concurrent callers never observe
// a newer head epoch paired with a stale lease. If the states are valid but
// the lease itself is rejected, the verified states are still persisted and
// the lease error is returned.
func (e *Endpoint) InstallWithLease(states []devicesec.State, proofs []*devicesec.RecoveryStatement, lease *devicesec.LeaseCertificate, now uint64) error {
	if len(states) == 0 || len(proofs) != len(states) {
		return devicesec.ErrInvalid
	}
	e.execution.Lock()
	defer e.execution.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return ErrStorage
	}
	d := e.data
	next := d
	next.States = cloneStates(d.States)
	next.Recovery = cloneRecovery(d.Recovery)
	next.ContentKeys = cloneKeys(d.ContentKeys)
	next.Secret = bytes.Clone(d.Secret)
	cacheDerivableKeys(&next)
	for i, s := range states {
		if s.Core.Epoch <= next.HeadEpoch {
			if old, ok := next.States[s.Core.Epoch]; ok {
				h1, _ := old.Hash()
				h2, _ := s.Hash()
				if h1 != h2 {
					return devicesec.ErrStale
				}
				continue
			}
			return devicesec.ErrStale
		}
		if s.Core.IssuedAt > uint64(time.Now().UnixMilli())+devicesec.MaxClockSkew {
			return devicesec.ErrClock
		}
		if s.Core.Epoch != next.HeadEpoch+1 || next.HeadEpoch >= devicesec.MaxStates {
			return devicesec.ErrStale
		}
		prev := next.States[next.HeadEpoch]
		if proofs[i] != nil {
			cacheDerivableKeys(&next)
		}
		if err := devicesec.VerifyTransition(prev, s, proofs[i]); err != nil {
			return err
		}
		_, member := s.Core.Member(d.MemberID)
		if member {
			secret, err := OpenGrant(d.Pin, s, d.MemberID, d.XPrivate)
			if err != nil {
				return err
			}
			if s.Core.ContentEpoch != prev.Core.ContentEpoch {
				// A batch can contain two content-epoch changes; compare
				// against the key of the previous state in the batch, not the
				// local head at the start of the batch.
				if proofs[i] == nil {
					old, err := devicesec.OpenPrevSecret(secret, s.PrevSecret)
					if err != nil || !bytes.Equal(old, next.Secret) {
						return devicesec.ErrUntrusted
					}
				}
				next.Secret = secret
				next.Member = true
			} else if next.Secret != nil {
				next.Secret = secret
				next.Member = true
			}
		} else {
			next.Member = false
		}
		next.States[s.Core.Epoch] = s
		if proofs[i] != nil {
			next.Recovery[s.Core.Epoch] = *proofs[i]
		}
		next.HeadEpoch = s.Core.Epoch
	}
	// Cache every content key that is still derivable, before this state is
	// used. Doing it at install time, in the same transaction, means a later
	// recovery boundary cannot make pre-recovery history unreadable on a
	// device that already had it.
	cacheDerivableKeys(&next)
	if lease != nil {
		leaseCopy := *lease
		leaseCopy.Signature = bytes.Clone(lease.Signature)
		leaseErr := leaseCopy.Verify(chainHead(next))
		if leaseErr == nil {
			var rawLease []byte
			rawLease, leaseErr = leaseCopy.MarshalBinary()
			if leaseErr == nil {
				committedLease, floor, err := e.persistInstallWithLease(d, next, leaseCopy, rawLease, now)
				if err == nil {
					if committedLease {
						next.Lease = &leaseCopy
						next.Floor = floor
						next.FloorSaved = floor
					} else if now > next.Floor {
						next.Floor = now
					}
					e.data = next
					return nil
				}
				leaseErr = err
			}
		}
		// Verified states (including removals) must still be persisted even if
		// the accompanying lease fails validation, rollback, clock, or lease-table
		// storage checks.
		if err := e.persistInstall(d, next); err != nil {
			return err
		}
		e.data = next
		return leaseErr
	}
	if err := e.persistInstall(d, next); err != nil {
		return err
	}
	e.data = next
	return nil
}

func cloneStates(in map[uint64]devicesec.State) map[uint64]devicesec.State {
	out := make(map[uint64]devicesec.State, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func cloneRecovery(in map[uint64]devicesec.RecoveryStatement) map[uint64]devicesec.RecoveryStatement {
	out := make(map[uint64]devicesec.RecoveryStatement, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func cloneKeys(in map[uint64][]byte) map[uint64][]byte {
	out := make(map[uint64][]byte, len(in))
	for k, v := range in {
		out[k] = bytes.Clone(v)
	}
	return out
}

func (e *Endpoint) persistInstall(old, next record) error {
	tx, err := e.db.Begin()
	if err != nil {
		return ErrStorage
	}
	defer tx.Rollback()
	if err := writeInstallTx(tx, old, next); err != nil {
		return err
	}
	return tx.Commit()
}

func (e *Endpoint) persistInstallWithLease(old, next record, l devicesec.LeaseCertificate, rawLease []byte, now uint64) (bool, uint64, error) {
	unchangedLease := old.HeadEpoch == next.HeadEpoch &&
		old.Lease != nil && old.Lease.Head == l.Head &&
		old.Lease.IssuedAt == l.IssuedAt && old.Lease.NotAfter == l.NotAfter &&
		bytes.Equal(old.Lease.Signature, l.Signature)
	if unchangedLease {
		return false, max(now, old.Floor), nil
	}
	tx, err := e.db.Begin()
	if err != nil {
		return false, 0, ErrStorage
	}
	defer tx.Rollback()
	if err := writeInstallTx(tx, old, next); err != nil {
		return false, 0, err
	}
	floor, err := writeLeaseTx(tx, old.Floor, l, rawLease, now)
	if err != nil {
		return false, 0, err
	}
	if err := tx.Commit(); err != nil {
		return false, 0, ErrStorage
	}
	return true, floor, nil
}

func writeInstallTx(tx *sql.Tx, old, next record) error {
	if err := checkPersistedHead(tx, old); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE security SET secret=?, member=? WHERE id=1`,
		next.Secret, boolInt(next.Member)); err != nil {
		return ErrStorage
	}
	for ep := old.HeadEpoch + 1; ep <= next.HeadEpoch; ep++ {
		s, ok := next.States[ep]
		if !ok {
			continue
		}
		raw, err := s.MarshalBinary()
		if err != nil {
			return err
		}
		h, err := s.Hash()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO states (epoch, state, hash) VALUES (?, ?, ?)`, ep, raw, h[:]); err != nil {
			return ErrStorage
		}
		if st, ok := next.Recovery[ep]; ok {
			raw, err := st.MarshalBinary()
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT OR REPLACE INTO recovery (epoch, statement) VALUES (?, ?)`, ep, raw); err != nil {
				return ErrStorage
			}
		}
	}
	for ep, key := range next.ContentKeys {
		if _, ok := old.ContentKeys[ep]; ok {
			continue
		}
		if _, err := tx.Exec(`INSERT OR REPLACE INTO content_keys (content_epoch, secret) VALUES (?, ?)`, ep, key); err != nil {
			return ErrStorage
		}
	}
	return nil
}

// cacheDerivableKeys opens the grant of every state that still names this
// device, so each content key the chain lets it read is cached before a
// recovery boundary can make the PrevSecret walk stop.
func cacheDerivableKeys(next *record) {
	// Walk the old chain, including epochs before this member's first grant.
	start := next.HeadEpoch
	for start > 0 {
		if _, ok := next.States[start].Core.Member(next.MemberID); ok {
			break
		}
		start--
	}
	key := bytes.Clone(next.Secret)
	for ep := start; ep >= 1; ep-- {
		state, ok := next.States[ep]
		if !ok {
			return
		}
		if len(key) != 32 {
			return
		}
		next.ContentKeys[state.Core.ContentEpoch] = bytes.Clone(key)
		prev, ok := next.States[ep-1]
		if !ok {
			return
		}
		if prev.Core.ContentEpoch != state.Core.ContentEpoch {
			if len(state.PrevSecret) == 0 {
				return
			}
			var err error
			key, err = devicesec.OpenPrevSecret(key, state.PrevSecret)
			if err != nil {
				return
			}
		}
	}
}

func (e *Endpoint) cacheChainKeys() error {
	d := e.data
	head := chainHead(d)
	for ep := head.Core.ContentEpoch; ep >= 1; ep-- {
		if key, ok := d.ContentKeys[ep]; ok && len(key) == 32 {
			continue
		}
		s, ok := stateWithContent(d, ep)
		if !ok {
			break
		}
		if s.Core.ContentEpoch == ep {
			key, err := OpenGrant(d.Pin, s, d.MemberID, d.XPrivate)
			if err == nil {
				d.ContentKeys[ep] = key
			}
		}
		if ep == 1 {
			break
		}
	}
	return nil
}
func stateWithContent(d record, content uint64) (devicesec.State, bool) {
	for ep := d.HeadEpoch; ep >= 1; ep-- {
		if s, ok := d.States[ep]; ok && s.Core.ContentEpoch == content {
			return s, true
		}
	}
	return devicesec.State{}, false
}

// ContentSecret returns the content key of an epoch, walking PrevSecret back
// from the head when it is not cached. A recovery boundary has no PrevSecret,
// so content before it is unavailable to a device that did not cache it.
func (e *Endpoint) ContentSecret(epoch uint64) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.contentSecretLocked(epoch)
}

func (e *Endpoint) contentSecretLocked(epoch uint64) ([]byte, error) {
	d := e.data
	if k, ok := d.ContentKeys[epoch]; ok && len(k) == 32 {
		return bytes.Clone(k), nil
	}
	head := chainHead(d)
	if head.Core.ContentEpoch < epoch {
		return nil, devicesec.ErrStale
	}
	start := head.Core.Epoch
	if !d.Member {
		for start > 0 {
			if _, ok := d.States[start].Core.Member(d.MemberID); ok {
				break
			}
			start--
		}
	}
	key := bytes.Clone(d.Secret)
	for ep := start; ep >= 1; ep-- {
		s, ok := d.States[ep]
		if !ok {
			return nil, ErrStorage
		}
		if s.Core.ContentEpoch == epoch {
			if key == nil {
				return nil, ErrUnavailable
			}
			e.cacheKeyLocked(epoch, key)
			d.ContentKeys[epoch] = bytes.Clone(key)
			return bytes.Clone(key), nil
		}
		prev, ok := d.States[ep-1]
		if !ok {
			return nil, ErrStorage
		}
		if prev.Core.ContentEpoch == s.Core.ContentEpoch {
			continue
		}
		if len(s.PrevSecret) == 0 {
			return nil, ErrUnavailable
		}
		if key == nil {
			return nil, ErrUnavailable
		}
		old, err := devicesec.OpenPrevSecret(key, s.PrevSecret)
		if err != nil {
			return nil, devicesec.ErrUntrusted
		}
		key = old
	}
	return nil, ErrUnavailable
}

func (e *Endpoint) cacheKeyLocked(epoch uint64, key []byte) {
	if e.db == nil {
		return
	}
	_, _ = e.db.Exec(`INSERT OR REPLACE INTO content_keys (content_epoch, secret) VALUES (?, ?)`, epoch, key)
}

// Touch advances the cached time floor and persists it at most once per second.
// Call it at startup and before fetching security states, even when the fetch fails.
func (e *Endpoint) Touch(now uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.data
	changed := now > d.Floor
	if changed {
		d.Floor = now
	}
	if d.Floor > d.FloorSaved && d.Floor-d.FloorSaved >= floorWriteInterval {
		if err := e.saveFloorLocked(&d); err != nil {
			return err
		}
		changed = true
	}
	if changed {
		e.data = d
	}
	return nil
}

func (e *Endpoint) saveFloorLocked(d *record) error {
	if e.db == nil {
		d.FloorSaved = d.Floor
		return nil
	}
	if err := e.db.QueryRow(`UPDATE security SET floor_ms=MAX(floor_ms,?), floor_saved_ms=MAX(floor_saved_ms,?) WHERE id=1 RETURNING floor_ms, floor_saved_ms`, d.Floor, d.Floor).Scan(&d.Floor, &d.FloorSaved); err != nil {
		return ErrStorage
	}
	return nil
}

func (e *Endpoint) ensureFloorFlusherLocked() {
	if e.db == nil || e.closed || e.floorStop != nil {
		return
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	e.floorStop = stop
	e.floorDone = done
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Duration(floorWriteInterval) * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				_ = e.flushFloor()
			}
		}
	}()
}

// flushFloor persists any in-memory floor advance without holding e.mu across fsync.
func (e *Endpoint) flushFloor() error {
	e.floorWriteMu.Lock()
	defer e.floorWriteMu.Unlock()

	e.mu.Lock()
	if e.closed || e.db == nil || e.data.Floor <= e.data.FloorSaved {
		e.mu.Unlock()
		return nil
	}
	db := e.db
	floor := e.data.Floor
	e.mu.Unlock()

	var persistedFloor, persistedSaved uint64
	if err := db.QueryRow(
		`UPDATE security SET floor_ms=MAX(floor_ms,?), floor_saved_ms=MAX(floor_saved_ms,?) WHERE id=1 RETURNING floor_ms, floor_saved_ms`,
		floor, floor,
	).Scan(&persistedFloor, &persistedSaved); err != nil {
		return ErrStorage
	}

	e.mu.Lock()
	if !e.closed && e.db == db {
		e.data.Floor = max(e.data.Floor, persistedFloor)
		e.data.FloorSaved = max(e.data.FloorSaved, persistedSaved)
	}
	e.mu.Unlock()
	return nil
}

// Prepare compresses and seals an uncompressed payload with a random nonce
// and no storage write.
func (e *Endpoint) Prepare(h devicesec.Header, sessionSeed, plain []byte) (devicesec.Ciphertext, error) {
	return e.prepare(h, sessionSeed, plain, true)
}

// PrepareUncompressed seals without compression, so the ciphertext's
// length depends only on the payload's length (a shared file's blocks).
func (e *Endpoint) PrepareUncompressed(h devicesec.Header, sessionSeed, plain []byte) (devicesec.Ciphertext, error) {
	return e.prepare(h, sessionSeed, plain, false)
}

func (e *Endpoint) prepare(h devicesec.Header, sessionSeed, plain []byte, compress bool) (devicesec.Ciphertext, error) {
	if h.Compression != 0 || len(plain) > devicesec.MaxCipherBytes-16 {
		return devicesec.Ciphertext{}, devicesec.ErrInvalid
	}
	// Compression can be CPU-intensive; keep it outside the endpoint gate.
	if compress && h.Purpose != devicesec.SessionKey && h.Purpose != devicesec.Image && h.Purpose.Kind() != 0 && len(plain) > 256 {
		if packed := pack(plain); packed != nil {
			plain, h.Compression = packed, 1
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.data
	if !d.Member || len(d.Secret) != 32 {
		return devicesec.Ciphertext{}, devicesec.ErrUntrusted
	}
	head := chainHead(d)
	sh, _ := head.Hash()
	h.Account = d.Pin.Account
	h.Domain = d.Pin.Domain
	h.Epoch = head.Core.Epoch
	h.State = sh
	h.ContentEpoch = head.Core.ContentEpoch
	h.Sender = d.MemberID
	// Check the caller's scope before sealing, so a bad header
	// cannot seal for another scope.
	if h.Scope(head) != nil {
		return devicesec.Ciphertext{}, devicesec.ErrUntrusted
	}
	c, err := Seal(d.Secret, sessionSeed, h, plain, ed25519.NewKeyFromSeed(d.EdSeed))
	if err != nil {
		return devicesec.Ciphertext{}, err
	}
	e.data = d
	return c, nil
}

// Open opens a live or stored envelope. Live control envelopes additionally
// require the head lease to be valid and the envelope's content epoch to be
// the head's. Stored history is verified against its own state only.
func (e *Endpoint) Open(w devicesec.Wire, sessionSeed []byte, live bool, now uint64) (devicesec.Header, []byte, error) {
	h, plain, err := e.openAuthenticated(w, sessionSeed, live, now)
	if err != nil {
		return h, nil, err
	}
	if err := ValidateBody(h, plain); err != nil {
		return h, nil, err
	}
	return h, plain, nil
}

// Acceptance compares authenticated payload identity before business structure
// validation. Public history readers still use Open's full body checks.
func (e *Endpoint) openAuthenticated(w devicesec.Wire, sessionSeed []byte, live bool, now uint64) (devicesec.Header, []byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, err := devicesec.ParseHeader(w.Header)
	if err != nil {
		return h, nil, err
	}
	s, ok := e.data.States[h.Epoch]
	if !ok {
		return h, nil, ErrUnknown
	}
	// A clock rolled back behind the persisted floor pauses remote control
	// until it catches up; viewing history and events keep working.
	if live && h.Purpose.Control() && (now+devicesec.MaxClockSkew < e.data.Floor || leaseValid(e.data, max(now, e.data.Floor)) != nil) {
		return h, nil, devicesec.ErrStale
	}
	head := chainHead(e.data)
	secret, err := e.contentSecretLocked(h.ContentEpoch)
	if err != nil {
		return h, nil, err
	}
	// Body validation is the caller's: Open and AcceptWith apply it.
	return w.Open(secret, sessionSeed, s, head, live, max(now, e.data.Floor))
}

// Accept uses process-local running coordination and durable at-most-once admission.
func (e *Endpoint) Accept(w devicesec.Wire, sessionSeed []byte, now uint64, fn func(devicesec.Header, []byte) (Completion, error)) ([]byte, error) {
	return e.AcceptWith(context.Background(), w, sessionSeed, now, AcceptOptions{}, fn)
}

// CheckExtension preserves the complete local chain when a computer re-pairs.
func (e *Endpoint) CheckExtension(states []devicesec.State) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(states) < int(e.data.HeadEpoch) {
		return fmt.Errorf("pairing chain rolls back local state; manual handling is required")
	}
	for ep := uint64(1); ep <= e.data.HeadEpoch; ep++ {
		old, ok := e.data.States[ep]
		if !ok {
			return ErrStorage
		}
		a, _ := old.Hash()
		b, err := states[ep-1].Hash()
		if err != nil || states[ep-1].Core.Epoch != ep || a != b {
			return fmt.Errorf("pairing chain forks local state at epoch %d; manual handling is required", ep)
		}
	}
	return nil
}

// Read the CAS authority inside the write transaction, including across handles.
func checkPersistedHead(tx *sql.Tx, expected record) error {
	var hash []byte
	if err := tx.QueryRow(`SELECT hash FROM states ORDER BY epoch DESC LIMIT 1`).Scan(&hash); err != nil {
		return ErrStorage
	}
	h, err := chainHead(expected).Hash()
	if err != nil || !bytes.Equal(hash, h[:]) {
		return devicesec.ErrStale
	}
	var id string
	if err := tx.QueryRow(`SELECT member_id FROM security WHERE id=1`).Scan(&id); err != nil {
		return ErrStorage
	}
	if id != expected.MemberID {
		return devicesec.ErrStale
	}
	return nil
}
