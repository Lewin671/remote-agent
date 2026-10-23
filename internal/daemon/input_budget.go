package daemon

import (
	"errors"

	"connectrpc.com/connect"
)

const (
	maxPendingInputs     = 256
	maxSessionInputBytes = 32 << 20
	maxDaemonInputBytes  = 128 << 20
)

func (q *queueState) pendingUsage() (count int, bytes int64) {
	for _, inputs := range [][]input{q.Queue, q.Held} {
		count += len(inputs)
		for _, in := range inputs {
			bytes += int64(len(in.Text))
		}
	}
	return
}

// Reservations cover admission through image ownership and queue persistence,
// including a new session's first prompt before that session exists.
type inputReservation struct {
	d      *Daemon
	bytes  int64
	active bool
}

func inputCapacityError() error {
	return connect.NewError(connect.CodeResourceExhausted, errors.New("pending input limit reached (256 inputs or 32 MiB per session, 128 MiB per daemon); wait for or cancel pending input"))
}

// Hold the session mutex when q belongs to a live session. inputMu never
// acquires session locks, and is never held over filesystem operations.
func (d *Daemon) reserveInput(id string, q *queueState, text string) (*inputReservation, error) {
	d.inputMu.Lock()
	defer d.inputMu.Unlock()
	count, bytes := 0, int64(0)
	if q != nil {
		count, bytes = q.pendingUsage()
		d.trackInputBytesLocked(id, bytes)
	}
	n := int64(len(text))
	if count >= maxPendingInputs || bytes+n > maxSessionInputBytes || d.inputBytes+n > maxDaemonInputBytes {
		return nil, inputCapacityError()
	}
	d.inputBytes += n
	return &inputReservation{d: d, bytes: n, active: true}, nil
}

func (d *Daemon) trackInputBytesLocked(id string, bytes int64) {
	if d.inputQueues == nil {
		d.inputQueues = map[string]int64{}
	}
	d.inputBytes += bytes - d.inputQueues[id]
	if bytes == 0 {
		delete(d.inputQueues, id)
	} else {
		d.inputQueues[id] = bytes
	}
}

func (s *session) trackPendingInput() {
	_, bytes := s.queue.pendingUsage()
	s.d.inputMu.Lock()
	s.d.trackInputBytesLocked(s.meta.ID, bytes)
	s.d.inputMu.Unlock()
}

func (r *inputReservation) release() {
	if r == nil {
		return
	}
	r.d.inputMu.Lock()
	defer r.d.inputMu.Unlock()
	if r.active {
		r.d.inputBytes -= r.bytes
		r.active = false
	}
}

func (r *inputReservation) commit(s *session) {
	_, bytes := s.queue.pendingUsage()
	r.d.inputMu.Lock()
	defer r.d.inputMu.Unlock()
	r.d.trackInputBytesLocked(s.meta.ID, bytes)
	if r.active {
		r.d.inputBytes -= r.bytes
		r.active = false
	}
}
