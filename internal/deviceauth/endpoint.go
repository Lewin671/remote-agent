package deviceauth

import (
	"bytes"
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
)

// The endpoint store is SQLite. The per-envelope path never writes it: a
// sender reserves a block of counters durably before using any of them, and
// a receiver reserves a block of accepted counters per sender before
// accepting any of them. The time floor and any pending reservations are
// flushed at most once per second (Touch) or with the next reservation, so
// steady traffic costs no disk write per message.
const storageVersion = 3

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
	counter INTEGER NOT NULL,
	block_end INTEGER NOT NULL,
	counter_epoch INTEGER NOT NULL,
	floor_ms INTEGER NOT NULL,
	floor_saved_ms INTEGER NOT NULL,
	member INTEGER NOT NULL
);
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
CREATE TABLE IF NOT EXISTS senders (
	sender TEXT PRIMARY KEY,
	high_water INTEGER NOT NULL
);
`

// floorWriteInterval is how often the receive time floor is persisted. Any
// control envelope advanced it in memory meanwhile; a restart loses at most
// this much, which only ever lowers the floor (never resurrects an expired
// lease once the older, lower value is loaded).
const floorWriteInterval = uint64(time.Second / time.Millisecond)

type record struct {
	Version      int
	Pin          Pin
	States       map[uint64]State
	HeadEpoch    uint64
	MemberID     string
	EdSeed       []byte
	XPrivate     []byte
	Secret       []byte
	Member       bool
	Counter      uint64
	BlockEnd     uint64
	CounterEpoch uint64
	Floor        uint64
	FloorSaved   uint64
	ContentKeys  map[uint64][]byte
	Recovery     map[uint64]RecoveryStatement
	Accepted     map[string]accepted
	HighWater    map[string]uint64
}

type accepted struct {
	Deadline uint64
	Done     bool
	Result   []byte
	Failed   bool
}

type Endpoint struct {
	mu   sync.Mutex
	path string
	db   *sql.DB
	data record
}

func chainHead(r record) State { return r.States[r.HeadEpoch] }

func verifyChain(pin Pin, states []State, proofs []*RecoveryStatement) error {
	if len(states) == 0 || len(states) > MaxStates || len(proofs) != len(states) {
		return ErrInvalid
	}
	if pin.VerifyGenesis(states[0]) != nil {
		return ErrUntrusted
	}
	for i := 1; i < len(states); i++ {
		if err := VerifyTransition(states[i-1], states[i], proofs[i]); err != nil {
			return err
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
	if _, err := tx.Exec(`INSERT INTO security (id, version, pin_account, pin_domain, pin_genesis, member_id, ed_seed, x_private, counter, block_end, counter_epoch, floor_ms, floor_saved_ms, member) VALUES (1, ?, '', X'', X'', '', X'', X'', 0, 0, 0, 0, 0, 0)`, storageVersion); err != nil {
		return err
	}
	return tx.Commit()
}

// InitEndpoint verifies a chain from the pinned genesis and stores the local
// device state. An empty path keeps it in memory.
func InitEndpoint(path string, pin Pin, states []State, proofs []*RecoveryStatement, id string, edSeed, xPrivate []byte) (*Endpoint, error) {
	if len(edSeed) != 32 || ID(ed25519.NewKeyFromSeed(edSeed).Public().(ed25519.PublicKey)) != id || len(xPrivate) != 32 {
		return nil, ErrInvalid
	}
	if err := verifyChain(pin, states, proofs); err != nil {
		return nil, err
	}
	head := states[len(states)-1]
	secret, err := pin.OpenGrant(head, id, xPrivate)
	if err != nil {
		return nil, err
	}
	if path != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, ErrStorage
		}
		// A new endpoint replaces whatever was there: pairing again starts a
		// new state, and the old counters must never be reused.
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
				return nil, ErrStorage
			}
		}
	} else if path == "" {
		// in memory
	}
	db, err := openDB(path)
	if err != nil {
		return nil, err
	}
	if err := createStore(db); err != nil {
		db.Close()
		return nil, ErrStorage
	}
	r := record{Version: storageVersion, Pin: pin, States: map[uint64]State{}, HeadEpoch: head.Core.Epoch, MemberID: id, EdSeed: bytes.Clone(edSeed), XPrivate: bytes.Clone(xPrivate), Secret: secret, Member: true, CounterEpoch: head.Core.ContentEpoch, ContentKeys: map[uint64][]byte{}, Recovery: map[uint64]RecoveryStatement{}, Accepted: map[string]accepted{}, HighWater: map[string]uint64{}}
	for i, s := range states {
		r.States[s.Core.Epoch] = s
		if proofs[i] != nil {
			r.Recovery[s.Core.Epoch] = *proofs[i]
		}
	}
	e := &Endpoint{path: path, db: db, data: r}
	if err := e.cacheChainKeys(); err != nil {
		db.Close()
		return nil, err
	}
	if err := e.persistAll(); err != nil {
		db.Close()
		return nil, err
	}
	return e, nil
}

// persistAll writes the header and every state of a fresh endpoint in one
// transaction, so a start cut short never leaves a partial chain.
func (e *Endpoint) persistAll() error {
	d := e.data
	tx, err := e.db.Begin()
	if err != nil {
		return ErrStorage
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE security SET version=?, pin_account=?, pin_domain=?, pin_genesis=?, member_id=?, ed_seed=?, x_private=?, secret=?, counter=?, block_end=?, counter_epoch=?, floor_ms=?, floor_saved_ms=?, member=? WHERE id=1`,
		d.Version, d.Pin.Account, d.Pin.Domain[:], d.Pin.Genesis[:], d.MemberID, d.EdSeed, d.XPrivate, d.Secret, d.Counter, d.BlockEnd, d.CounterEpoch, d.Floor, d.FloorSaved, boolInt(d.Member)); err != nil {
		return ErrStorage
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
	for sender, hw := range d.HighWater {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO senders (sender, high_water) VALUES (?, ?)`, sender, hw); err != nil {
			return ErrStorage
		}
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
	r := record{States: map[uint64]State{}, Recovery: map[uint64]RecoveryStatement{}, Accepted: map[string]accepted{}, ContentKeys: map[uint64][]byte{}, HighWater: map[string]uint64{}}
	var member int
	var pinDomain, pinGenesis []byte
	err = db.QueryRow(`SELECT version, pin_account, pin_domain, pin_genesis, member_id, ed_seed, x_private, secret, counter, block_end, counter_epoch, floor_ms, floor_saved_ms, member FROM security WHERE id=1`).
		Scan(&r.Version, &r.Pin.Account, &pinDomain, &pinGenesis, &r.MemberID, &r.EdSeed, &r.XPrivate, &r.Secret, &r.Counter, &r.BlockEnd, &r.CounterEpoch, &r.Floor, &r.FloorSaved, &member)
	if errors.Is(err, sql.ErrNoRows) {
		db.Close()
		return nil, ErrStorage
	}
	if err != nil {
		db.Close()
		return nil, ErrStorage
	}
	if r.Version != storageVersion || len(pinDomain) != 32 || len(pinGenesis) != 32 || len(r.EdSeed) != 32 || len(r.XPrivate) != 32 {
		db.Close()
		return nil, ErrStorage
	}
	copy(r.Pin.Domain[:], pinDomain)
	copy(r.Pin.Genesis[:], pinGenesis)
	r.Member = member != 0
	// Counter values in the last reserved block are never handed out again
	// after a restart: start at the durable block end, not at the stored
	// in-use counter.
	r.Counter = r.BlockEnd
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
		s, err := ParseState(raw)
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
		st, err := ParseRecoveryStatement(raw)
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
	rows, err = db.Query(`SELECT sender, high_water FROM senders`)
	if err != nil {
		db.Close()
		return nil, ErrStorage
	}
	for rows.Next() {
		var sender string
		var hw uint64
		if err := rows.Scan(&sender, &hw); err != nil {
			rows.Close()
			db.Close()
			return nil, ErrStorage
		}
		r.HighWater[sender] = hw
	}
	rows.Close()
	e := &Endpoint{path: path, db: db, data: r}
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
	var prev State
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
			var proof *RecoveryStatement
			if p, ok := r.Recovery[ep]; ok {
				proof = &p
			}
			if VerifyTransition(prev, s, proof) != nil {
				return ErrStorage
			}
		}
		prev = s
	}
	if r.MemberID != ID(ed25519.NewKeyFromSeed(r.EdSeed).Public().(ed25519.PublicKey)) {
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
		secret, err := r.Pin.OpenGrant(head, r.MemberID, r.XPrivate)
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
	e.mu.Lock()
	defer e.mu.Unlock()
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
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.db == nil {
		return nil
	}
	err := e.db.Close()
	e.db = nil
	return err
}
func (e *Endpoint) Snapshot() (Pin, State, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.data.Pin, chainHead(e.data), e.data.MemberID
}
func (e *Endpoint) StateCount() int   { e.mu.Lock(); defer e.mu.Unlock(); return len(e.data.States) }
func (e *Endpoint) HeadEpoch() uint64 { e.mu.Lock(); defer e.mu.Unlock(); return e.data.HeadEpoch }
func (e *Endpoint) Removed() bool     { e.mu.Lock(); defer e.mu.Unlock(); return !e.data.Member }

// Install appends a contiguous chain segment. proofs[i] is set only when
// states[i] is a recovery transition. The new head, states, keys and any
// counter reset are persisted in one transaction before use.
func (e *Endpoint) Install(states []State, proofs []*RecoveryStatement) error {
	if len(states) == 0 || len(proofs) != len(states) {
		return ErrInvalid
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.data
	next := d
	next.States = cloneStates(d.States)
	next.Recovery = cloneRecovery(d.Recovery)
	next.ContentKeys = cloneKeys(d.ContentKeys)
	next.Secret = bytes.Clone(d.Secret)
	for i, s := range states {
		if s.Core.Epoch <= next.HeadEpoch {
			if old, ok := next.States[s.Core.Epoch]; ok {
				h1, _ := old.Hash()
				h2, _ := s.Hash()
				if h1 != h2 {
					return ErrStale
				}
				continue
			}
			return ErrStale
		}
		if s.Core.Epoch != next.HeadEpoch+1 || next.HeadEpoch >= MaxStates {
			return ErrStale
		}
		prev := next.States[next.HeadEpoch]
		if err := VerifyTransition(prev, s, proofs[i]); err != nil {
			return err
		}
		_, member := s.Core.Member(d.MemberID)
		if member {
			secret, err := d.Pin.OpenGrant(s, d.MemberID, d.XPrivate)
			if err != nil {
				return err
			}
			if s.Core.ContentEpoch != prev.Core.ContentEpoch {
				if proofs[i] == nil {
					old, err := openPrevSecret(secret, s.PrevSecret)
					if err != nil || !bytes.Equal(old, d.Secret) {
						return ErrUntrusted
					}
				}
				next.ContentKeys[prev.Core.ContentEpoch] = bytes.Clone(d.Secret)
				next.Secret = secret
				next.Counter = 0
				next.BlockEnd = 0
				next.CounterEpoch = s.Core.ContentEpoch
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
	if err := e.persistInstall(d, next); err != nil {
		return err
	}
	e.data = next
	return nil
}

func cloneStates(in map[uint64]State) map[uint64]State {
	out := make(map[uint64]State, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func cloneRecovery(in map[uint64]RecoveryStatement) map[uint64]RecoveryStatement {
	out := make(map[uint64]RecoveryStatement, len(in))
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
	if _, err := tx.Exec(`UPDATE security SET secret=?, counter=?, block_end=?, counter_epoch=?, member=? WHERE id=1`,
		next.Secret, next.Counter, next.BlockEnd, next.CounterEpoch, boolInt(next.Member)); err != nil {
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
	return tx.Commit()
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
			key, err := d.Pin.OpenGrant(s, d.MemberID, d.XPrivate)
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
func stateWithContent(d record, content uint64) (State, bool) {
	for ep := d.HeadEpoch; ep >= 1; ep-- {
		if s, ok := d.States[ep]; ok && s.Core.ContentEpoch == content {
			return s, true
		}
	}
	return State{}, false
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
		return nil, ErrStale
	}
	key := bytes.Clone(d.Secret)
	for ep := head.Core.Epoch; ep >= 1; ep-- {
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
		old, err := openPrevSecret(key, s.PrevSecret)
		if err != nil {
			return nil, ErrUntrusted
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

// Touch persists the time floor. Call it at startup and on every successful
// state synchronization. It writes at most once per second.
func (e *Endpoint) Touch(now uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.data
	if now <= d.Floor {
		return nil
	}
	d.Floor = now
	if now-d.FloorSaved >= floorWriteInterval {
		if err := e.saveFloorLocked(d); err != nil {
			return err
		}
	}
	e.data = d
	return nil
}

func (e *Endpoint) saveFloorLocked(d record) error {
	if e.db == nil {
		d.FloorSaved = d.Floor
		return nil
	}
	if _, err := e.db.Exec(`UPDATE security SET floor_ms=?, floor_saved_ms=? WHERE id=1`, d.Floor, d.Floor); err != nil {
		return ErrStorage
	}
	d.FloorSaved = d.Floor
	return nil
}

// Prepare seals one envelope. Only a new counter block is written to disk.
func (e *Endpoint) Prepare(h Header, sessionSeed, plain []byte) (Ciphertext, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.data
	if !d.Member || len(d.Secret) != 32 {
		return Ciphertext{}, ErrUntrusted
	}
	head := chainHead(d)
	if head.Core.ContentEpoch != d.CounterEpoch {
		d.Counter, d.BlockEnd, d.CounterEpoch = 0, 0, head.Core.ContentEpoch
	}
	if d.Counter >= d.BlockEnd {
		if d.BlockEnd > ^uint64(0)-CounterBlock {
			return Ciphertext{}, ErrCapacity
		}
		if err := e.reserveSendBlockLocked(&d); err != nil {
			return Ciphertext{}, err
		}
	}
	counter := d.Counter + 1
	d.Counter = counter
	sh, _ := head.Hash()
	h.Account = d.Pin.Account
	h.Domain = d.Pin.Domain
	h.Epoch = head.Core.Epoch
	h.State = sh
	h.ContentEpoch = head.Core.ContentEpoch
	h.Sender = d.MemberID
	h.Counter = counter
	if h.Scope(head) != nil {
		return Ciphertext{}, ErrUntrusted
	}
	// Consume the in-memory counter even if sealing fails, so the durable
	// block reservation stays consistent with what this endpoint hands out.
	e.data = d
	c, err := Seal(d.Secret, sessionSeed, h, plain, ed25519.NewKeyFromSeed(d.EdSeed))
	if err != nil {
		return Ciphertext{}, err
	}
	e.data = d
	return c, nil
}

func (e *Endpoint) reserveSendBlockLocked(d *record) error {
	next := d.BlockEnd + CounterBlock
	if e.db != nil {
		res, err := e.db.Exec(`UPDATE security SET block_end=? WHERE id=1 AND block_end=?`, next, d.BlockEnd)
		if err != nil {
			return ErrStorage
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return ErrStorage
		}
	}
	d.BlockEnd = next
	return nil
}

// Open opens a live or stored envelope. Live control envelopes additionally
// require the head lease to be valid and the envelope's content epoch to be
// the head's. Stored history is verified against its own state only.
func (e *Endpoint) Open(w Wire, sessionSeed []byte, live bool, now uint64) (Header, []byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	h, err := ParseHeader(w.Header)
	if err != nil {
		return h, nil, err
	}
	s, ok := e.data.States[h.Epoch]
	if !ok {
		return h, nil, ErrUnknown
	}
	head := chainHead(e.data)
	secret, err := e.contentSecretLocked(h.ContentEpoch)
	if err != nil {
		return h, nil, err
	}
	h, plain, err := w.Open(secret, sessionSeed, s, head, live, maxU64(now, e.data.Floor))
	return h, plain, err
}

// Accept applies a control envelope at most once. Replay protection is a
// durable per-sender high-water counter reserved a block at a time before
// the operation runs; the accepted table only reuses a result while the
// process is up. After a restart a repeated counter is refused (its outcome
// is unknown) rather than run again.
func (e *Endpoint) Accept(w Wire, sessionSeed []byte, now uint64, fn func(Header, []byte) ([]byte, error)) ([]byte, error) {
	e.mu.Lock()
	h, err := ParseHeader(w.Header)
	if err != nil {
		e.mu.Unlock()
		return nil, err
	}
	if !h.Purpose.Control() {
		e.mu.Unlock()
		return nil, ErrInvalid
	}
	now = maxU64(now, e.data.Floor)
	if h.Deadline <= now || h.Deadline-now > 10*60*1000 {
		e.mu.Unlock()
		return nil, ErrInvalid
	}
	if e.data.Floor < now {
		d := e.data
		d.Floor = now
		if now-d.FloorSaved >= floorWriteInterval {
			if err := e.saveFloorLocked(d); err != nil {
				e.mu.Unlock()
				return nil, err
			}
		}
		e.data = d
	}
	key := h.Sender + "/" + itoa(uint64(h.Purpose)) + "/" + h.Machine + "/" + h.Session + "/" + h.Request + "/" + itoa(h.Seq)
	if a, ok := e.data.Accepted[key]; ok && a.Done {
		e.mu.Unlock()
		if a.Failed {
			return nil, errors.New("security: previously accepted operation failed")
		}
		return a.Result, nil
	}
	if _, ok := e.data.States[h.Epoch]; !ok {
		e.mu.Unlock()
		return nil, ErrUnknown
	}
	e.mu.Unlock()

	// Open verifies the state, live membership, lease and ciphertext. An
	// unknown state or an expired lease must not consume the replay counter,
	// so the caller can fetch the state and try the same envelope again.
	_, plain, err := e.Open(w, sessionSeed, true, now)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	if a, ok := e.data.Accepted[key]; ok && a.Done {
		e.mu.Unlock()
		if a.Failed {
			return nil, errors.New("security: previously accepted operation failed")
		}
		return a.Result, nil
	}
	if h.Purpose == Command {
		if err := e.reserveReplayLocked(&e.data, h.Sender, h.Counter); err != nil {
			e.mu.Unlock()
			return nil, err
		}
	}
	e.mu.Unlock()

	out, callErr := fn(h, plain)

	e.mu.Lock()
	defer e.mu.Unlock()
	d := e.data
	d.Accepted[key] = accepted{Deadline: h.Deadline, Done: true, Result: bytes.Clone(out), Failed: callErr != nil}
	if len(d.Accepted) > 8192 {
		return nil, ErrCapacity
	}
	e.data = d
	return out, callErr
}

// reserveReplayLocked refuses a command whose counter a previous run already
// executed and persists the new exact high-water before the command runs. A
// crash therefore cannot make an executed command acceptable again. Commands
// are user actions, not per-message traffic; terminal frames are scoped to a
// stream ID and handled by the in-memory accepted table instead.
func (e *Endpoint) reserveReplayLocked(d *record, sender string, counter uint64) error {
	if counter == 0 {
		return ErrInvalid
	}
	if counter <= d.HighWater[sender] {
		return ErrStale
	}
	if e.db != nil {
		if _, err := e.db.Exec(`INSERT INTO senders (sender, high_water) VALUES (?, ?) ON CONFLICT (sender) DO UPDATE SET high_water=excluded.high_water`, sender, counter); err != nil {
			return ErrStorage
		}
	}
	d.HighWater[sender] = counter
	return nil
}

func itoa(n uint64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
