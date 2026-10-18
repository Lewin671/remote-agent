package deviceauth

import (
	"bytes"
	"database/sql"
	"errors"
)

var ErrNoHistory = errors.New("security: historical epoch not cached locally")

func historyChecksum(epoch uint64, raw, secret []byte) []byte {
	var w encoder
	w.u64(epoch)
	w.lp(raw)
	w.lp(secret)
	h := digest("ra2/history-cache\x00", w.b)
	return h[:]
}
func (s *localStore) cacheHistory(state State, secret []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return ErrStorage
	}
	if len(secret) != 32 || state.Core.Epoch > maxEpochs {
		return ErrInvalid
	}
	raw, err := state.MarshalBinary()
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		s.poisoned = true
		return ErrStorage
	}
	defer tx.Rollback()
	var existing []byte
	err = tx.QueryRow("SELECT state FROM states WHERE epoch=?", state.Core.Epoch).Scan(&existing)
	if err == nil && !bytes.Equal(raw, existing) {
		return ErrUntrusted
	}
	if errors.Is(err, sql.ErrNoRows) {
		_, err = tx.Exec("INSERT INTO states VALUES(?,?)", state.Core.Epoch, raw)
	}
	if err == nil {
		_, err = tx.Exec("INSERT INTO history_keys VALUES(?,?,?) ON CONFLICT(epoch) DO UPDATE SET secret=excluded.secret,checksum=excluded.checksum", state.Core.Epoch, secret, historyChecksum(state.Core.Epoch, raw, secret))
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
func (s *localStore) history(epoch uint64) (State, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return State{}, nil, ErrStorage
	}
	var raw, secret, sum []byte
	err := s.db.QueryRow("SELECT states.state,history_keys.secret,history_keys.checksum FROM states JOIN history_keys USING(epoch) WHERE epoch=?", epoch).Scan(&raw, &secret, &sum)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, nil, ErrNoHistory
	}
	if err != nil || len(raw) > MaxStateBytes || len(secret) != 32 || !bytes.Equal(sum, historyChecksum(epoch, raw, secret)) {
		return State{}, nil, ErrStorage
	}
	state, err := ParseState(raw)
	if err != nil || state.Core.Epoch != epoch {
		return State{}, nil, ErrStorage
	}
	return state, secret, nil
}
func (e *Endpoint) cacheHistory(state State, key []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.data.Pin.Verify(state) != nil || state.Core.Epoch > e.data.State.Core.Epoch {
		return ErrUntrusted
	}
	return e.disk.cacheHistory(state, key)
}
func (e *Endpoint) openHistory(w Wire, seed []byte) (Header, []byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.disk.check(); err != nil {
		return Header{}, nil, err
	}
	h, err := ParseHeader(w.Header)
	if err != nil || h.Purpose.Control() {
		return h, nil, ErrInvalid
	}
	if h.Epoch > e.data.State.Core.Epoch {
		return h, nil, ErrStale
	}
	if h.Epoch == e.data.State.Core.Epoch {
		return w.Open(e.data.Pin, e.data.State, e.data.Secret, seed)
	}
	state, key, err := e.disk.history(h.Epoch)
	if err != nil {
		return h, nil, err
	}
	return w.Open(e.data.Pin, state, key, seed)
}
