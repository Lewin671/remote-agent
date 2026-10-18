package deviceauth

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite"
)

// localStore is a single-owner, synchronous local security database. EXCLUSIVE
// locking prevents two authority instances from issuing conflicting heads.
// Init is explicit; Open never creates a missing file or repairs corrupt state.
// A write error poisons the live store: commit outcome may be uncertain, so the
// caller must reopen and validate durable state before issuing anything else.
type localStore struct {
	mu            sync.Mutex
	db            *sql.DB
	poisoned      bool
	accepted      map[string]acceptedEntry
	acceptedFloor uint64
}

const storageVersion = 2
const maxLocalBytes = 32 << 20

func openStore(path string, initial any) (*localStore, error) {
	if initial != nil {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return nil, ErrStorage
		}
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
		if err != nil {
			return nil, ErrStorage
		}
		if f.Close() != nil {
			return nil, ErrStorage
		}
	} else {
		st, err := os.Lstat(path)
		if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() == 0 {
			return nil, ErrStorage
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, ErrStorage
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	fail := func() (*localStore, error) { db.Close(); return nil, ErrStorage }
	for _, q := range []string{"PRAGMA busy_timeout=0", "PRAGMA locking_mode=EXCLUSIVE", "PRAGMA journal_mode=DELETE", "PRAGMA synchronous=FULL", "PRAGMA foreign_keys=ON"} {
		if _, err := db.Exec(q); err != nil {
			return fail()
		}
	}
	s := &localStore{db: db}
	if initial != nil {
		if _, err := db.Exec("CREATE TABLE security(version INTEGER NOT NULL CHECK(version=2), snapshot BLOB NOT NULL, checksum BLOB NOT NULL); CREATE TABLE states(epoch INTEGER PRIMARY KEY, state BLOB NOT NULL); CREATE TABLE accepted(id BLOB PRIMARY KEY, deadline INTEGER NOT NULL, done INTEGER NOT NULL, result BLOB, failed INTEGER NOT NULL, checksum BLOB NOT NULL); CREATE TABLE accepted_meta(id INTEGER PRIMARY KEY CHECK(id=1),root BLOB NOT NULL,cutoff INTEGER NOT NULL); CREATE TABLE history_keys(epoch INTEGER PRIMARY KEY, secret BLOB NOT NULL, checksum BLOB NOT NULL)"); err != nil {
			return fail()
		}
		b, err := json.Marshal(initial)
		if err != nil || len(b) > maxLocalBytes {
			return fail()
		}
		if _, err := db.Exec("INSERT INTO accepted_meta VALUES(1,?,0)", acceptedRoot(map[string]acceptedEntry{}, 0)); err != nil {
			return fail()
		}
		if _, err := db.Exec("INSERT INTO security VALUES(?,?,?)", storageVersion, b, checksum(b)); err != nil {
			return fail()
		}
		dir, err := os.Open(filepath.Dir(path))
		if err != nil {
			return fail()
		}
		err = dir.Sync()
		dir.Close()
		if err != nil {
			return fail()
		}
	}
	if _, err := db.Exec("SELECT id,deadline,done,result,failed FROM accepted LIMIT 0"); err != nil {
		return fail()
	}
	var version int
	if db.QueryRow("SELECT version FROM security").Scan(&version) != nil || version != storageVersion {
		return fail()
	}
	// Acquire/retain the write lock even on Open; opening a second live store
	// must fail before either caller can use a cached security head.
	if _, err := db.Exec("UPDATE security SET version=version"); err != nil {
		return fail()
	}
	if err := s.loadAccepted(); err != nil {
		return fail()
	}
	return s, nil
}
func (s *localStore) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.poisoned = true
	return s.db.Close()
}
func (s *localStore) load(out any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return ErrStorage
	}
	var b, sum []byte
	if s.db.QueryRow("SELECT snapshot,checksum FROM security").Scan(&b, &sum) != nil || len(b) > maxLocalBytes || !bytes.Equal(sum, checksum(b)) {
		return ErrStorage
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if dec.Decode(out) != nil {
		return ErrStorage
	}
	if dec.Decode(new(any)) != io.EOF {
		return ErrStorage
	}
	return nil
}
func (s *localStore) save(snapshot any, state *State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return ErrStorage
	}
	b, err := json.Marshal(snapshot)
	if err != nil || len(b) > maxLocalBytes {
		return ErrCapacity
	}
	tx, err := s.db.Begin()
	if err != nil {
		s.poisoned = true
		return ErrStorage
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE security SET snapshot=?,checksum=?", b, checksum(b)); err == nil && state != nil {
		var raw []byte
		raw, err = state.MarshalBinary()
		if err == nil {
			_, err = tx.Exec("INSERT INTO states(epoch,state) VALUES(?,?)", state.Core.Epoch, raw)
		}
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		s.poisoned = true
		return ErrStorage
	}
	return nil
}
func (s *localStore) page(after uint64) ([]State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned || after > 1<<20 {
		return nil, ErrStorage
	}
	rows, err := s.db.Query("SELECT state FROM states WHERE epoch>? ORDER BY epoch LIMIT 2", after)
	if err != nil {
		return nil, ErrStorage
	}
	defer rows.Close()
	var out []State
	size := 0
	for rows.Next() {
		var b []byte
		if rows.Scan(&b) != nil {
			return nil, ErrStorage
		}
		size += len(b)
		if size > MaxStateBytes {
			break
		}
		state, err := ParseState(b)
		if err != nil {
			return nil, ErrStorage
		}
		out = append(out, state)
	}
	if rows.Err() != nil {
		return nil, ErrStorage
	}
	return out, nil
}
func clone[T any](in T) T {
	b, err := json.Marshal(in)
	if err != nil {
		panic("security: internal snapshot cannot encode")
	}
	var out T
	if json.Unmarshal(b, &out) != nil {
		panic("security: internal snapshot cannot decode")
	}
	return out
}

func checksum(b []byte) []byte { h := sha256.Sum256(b); return h[:] }
func (s *localStore) check() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return ErrStorage
	}
	return nil
}
