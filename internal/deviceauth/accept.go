package deviceauth

import (
	"bytes"
	"errors"
	"slices"
)

var ErrUnknown = errors.New("security: backend outcome is unknown; operation was not repeated")
var ErrExpired = errors.New("security: expired control was not newly accepted")
var ErrBackend = errors.New("security: previously accepted operation failed")

const maxAccepted = 8192
const maxAcceptedBytes = 16 << 20

type acceptedEntry struct {
	Deadline uint64
	Done     bool
	Result   []byte
	Failed   bool
	Sum      Hash
}

func acceptedChecksum(id string, e acceptedEntry) []byte {
	var w encoder
	w.lp([]byte(id))
	w.u64(e.Deadline)
	if e.Done {
		w.u32(1)
	} else {
		w.u32(0)
	}
	w.lp(e.Result)
	if e.Failed {
		w.u32(1)
	} else {
		w.u32(0)
	}
	h := digest("ra2/accepted-entry\x00", w.b)
	return h[:]
}
func acceptedRoot(entries map[string]acceptedEntry, cutoff uint64) []byte {
	ids := make([]string, 0, len(entries))
	for id := range entries {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var w encoder
	w.u64(cutoff)
	w.u32(uint32(len(ids)))
	for _, id := range ids {
		w.lp([]byte(id))
		sum := entries[id].Sum
		w.raw(sum[:])
	}
	h := digest("ra2/accepted-log\x00", w.b)
	return h[:]
}
func acceptedSize(entries map[string]acceptedEntry) int {
	n := 0
	for id, e := range entries {
		n += len(id) + len(e.Result) + 128
	}
	return n
}
func (s *localStore) loadAccepted() error {
	rows, err := s.db.Query("SELECT id,deadline,done,result,failed,checksum FROM accepted")
	if err != nil {
		return ErrStorage
	}
	defer rows.Close()
	entries := map[string]acceptedEntry{}
	size := 0
	for rows.Next() {
		var id, sum, result []byte
		var deadline int64
		var done, failed int
		if rows.Scan(&id, &deadline, &done, &result, &failed, &sum) != nil || len(id) > 1024 || deadline <= 0 || done < 0 || done > 1 || failed < 0 || failed > 1 || len(result) > MaxCipherBytes {
			return ErrStorage
		}
		e := acceptedEntry{Deadline: uint64(deadline), Done: done == 1, Result: result, Failed: failed == 1}
		if !bytes.Equal(sum, acceptedChecksum(string(id), e)) {
			return ErrStorage
		}
		copy(e.Sum[:], sum)
		entries[string(id)] = e
		size += len(id) + len(result) + 128
		if len(entries) > maxAccepted || size > maxAcceptedBytes {
			return ErrStorage
		}
	}
	if rows.Err() != nil {
		return ErrStorage
	}
	rows.Close()
	var root []byte
	var cutoff int64
	if s.db.QueryRow("SELECT root,cutoff FROM accepted_meta WHERE id=1").Scan(&root, &cutoff) != nil || cutoff < 0 || !bytes.Equal(root, acceptedRoot(entries, uint64(cutoff))) {
		return ErrStorage
	}
	s.accepted = entries
	s.acceptedFloor = uint64(cutoff)
	return nil
}

// persistAccepted updates the manifest atomically with entries. The complete
// bounded manifest is checked at open, including deletions/missing records. A
// payload/ID bit flip cannot silently convert an accepted request into a new one.
func (s *localStore) persistAccepted(entries map[string]acceptedEntry, changed string, pruned []string, cutoff uint64) error {
	tx, err := s.db.Begin()
	if err != nil {
		s.poisoned = true
		return ErrStorage
	}
	defer tx.Rollback()
	for _, id := range pruned {
		if _, err = tx.Exec("DELETE FROM accepted WHERE id=?", []byte(id)); err != nil {
			break
		}
	}
	if err == nil {
		e := entries[changed]
		done, failed := 0, 0
		if e.Done {
			done = 1
		}
		if e.Failed {
			failed = 1
		}
		_, err = tx.Exec("INSERT INTO accepted VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET deadline=excluded.deadline,done=excluded.done,result=excluded.result,failed=excluded.failed,checksum=excluded.checksum", []byte(changed), e.Deadline, done, e.Result, failed, acceptedChecksum(changed, e))
	}
	if err == nil {
		_, err = tx.Exec("UPDATE accepted_meta SET root=?,cutoff=? WHERE id=1", acceptedRoot(entries, cutoff), cutoff)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		s.poisoned = true
		return ErrStorage
	}
	s.accepted = entries
	s.acceptedFloor = cutoff
	return nil
}
func copyAccepted(in map[string]acceptedEntry) map[string]acceptedEntry {
	out := make(map[string]acceptedEntry, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
func (s *localStore) start(id []byte, deadline, now uint64) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned || s.accepted == nil {
		return nil, false, ErrStorage
	}
	if deadline > 1<<63-1 || now > 1<<63-1 {
		return nil, false, ErrInvalid
	}
	if e, ok := s.accepted[string(id)]; ok {
		if !e.Done {
			return nil, false, ErrUnknown
		}
		if e.Failed {
			return nil, false, ErrBackend
		}
		return bytes.Clone(e.Result), false, nil
	}
	// Pruning remains irreversible even when a trusted OS corrects its wall
	// clock backwards. A relay cannot resurrect an expired, deleted entry.
	now = max(now, s.acceptedFloor)
	if deadline <= now {
		return nil, false, ErrExpired
	}
	entries := copyAccepted(s.accepted)
	pruned := []string{}
	for key, e := range entries {
		if e.Deadline <= now {
			delete(entries, key)
			pruned = append(pruned, key)
		}
	}
	if len(entries) >= maxAccepted {
		return nil, false, ErrCapacity
	}
	e := acceptedEntry{Deadline: deadline}
	copy(e.Sum[:], acceptedChecksum(string(id), e))
	entries[string(id)] = e
	if acceptedSize(entries) > maxAcceptedBytes {
		return nil, false, ErrCapacity
	}
	if err := s.persistAccepted(entries, string(id), pruned, now); err != nil {
		return nil, false, err
	}
	return nil, true, nil
}
func (s *localStore) finish(id, result []byte, failed bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.poisoned {
		return ErrStorage
	}
	e, ok := s.accepted[string(id)]
	if !ok || e.Done {
		return ErrInvalid
	}
	if len(result) > MaxCipherBytes {
		return ErrCapacity
	}
	entries := copyAccepted(s.accepted)
	e.Done = true
	e.Result = bytes.Clone(result)
	e.Failed = failed
	copy(e.Sum[:], acceptedChecksum(string(id), e))
	entries[string(id)] = e
	if acceptedSize(entries) > maxAcceptedBytes {
		return ErrCapacity
	}
	return s.persistAccepted(entries, string(id), nil, s.acceptedFloor)
}
