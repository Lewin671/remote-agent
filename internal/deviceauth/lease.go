package deviceauth

import (
	"bytes"
	"database/sql"
	"errors"

	"github.com/Lewin671/remote-agent/shared/devicesec"
)

func leaseValid(r record, now uint64) error {
	h, _ := chainHead(r).Hash()
	if r.Lease == nil || r.Lease.Head != h {
		return devicesec.ErrStale
	}
	return r.Lease.Valid(chainHead(r), now)
}

// InstallLease durably accepts a verified certificate. A newer member head
// invalidates the prior certificate automatically; the signed chain is untouched.
func (e *Endpoint) InstallLease(l devicesec.LeaseCertificate, now uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := l.Verify(chainHead(e.data)); err != nil {
		return err
	}
	raw, err := l.MarshalBinary()
	if err != nil {
		return err
	}
	floor := max(now, e.data.Floor)
	tx, err := e.db.Begin()
	if err != nil {
		return ErrStorage
	}
	defer tx.Rollback()
	if err := checkPersistedHead(tx, e.data); err != nil {
		return err
	}
	var current []byte
	err = tx.QueryRow(`SELECT certificate FROM lease WHERE id=1`).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ErrStorage
	}
	if len(current) > 0 {
		committed, err := devicesec.ParseLease(current)
		if err != nil {
			return err
		}
		if committed.Head == l.Head && (committed.IssuedAt > l.IssuedAt || committed.NotAfter > l.NotAfter || (committed.IssuedAt == l.IssuedAt && committed.NotAfter != l.NotAfter)) {
			return devicesec.ErrStale
		}
	}
	if err := tx.QueryRow(`SELECT MAX(floor_ms,?) FROM security WHERE id=1`, floor).Scan(&floor); err != nil {
		return ErrStorage
	}
	if l.IssuedAt > floor+devicesec.MaxClockSkew {
		return devicesec.ErrClock
	}
	if _, err = tx.Exec(`INSERT OR REPLACE INTO lease(id,certificate) VALUES(1,?)`, raw); err != nil {
		return ErrStorage
	}
	if _, err = tx.Exec(`UPDATE security SET floor_ms=?,floor_saved_ms=? WHERE id=1`, floor, floor); err != nil {
		return ErrStorage
	}
	if err = tx.Commit(); err != nil {
		return ErrStorage
	}
	e.data.Lease = &l
	e.data.Floor = floor
	e.data.FloorSaved = floor
	return nil
}
func (e *Endpoint) Lease() *devicesec.LeaseCertificate {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.data.Lease == nil {
		return nil
	}
	l := *e.data.Lease
	l.Signature = bytes.Clone(l.Signature)
	return &l
}
func (e *Endpoint) CheckLease(now uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if now+devicesec.MaxClockSkew < e.data.Floor {
		return devicesec.ErrClock
	}
	return leaseValid(e.data, max(now, e.data.Floor))
}
