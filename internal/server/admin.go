package server

import (
	"context"
	"errors"
	"time"
)

// Admin is the operator's access to the accounts of a relay, for the
// admin command. It opens the relay's database directly, next to the
// running server: SQLite serializes their writes, and the server ends the
// streams of a suspended or deleted account within its sweep interval.
type Admin struct{ st *store }

// AccountInfo describes an account for the operator; it holds nothing
// that the server does not see anyway.
type AccountInfo struct {
	ID         string
	Plan       string
	Suspended  bool
	Created    time.Time
	LastActive time.Time
	Devices    int
	Machines   int
	Sessions   int
	EventBytes int64
	ImageBytes int64
}

// ErrNoAccount means no account has the ID given.
var ErrNoAccount = errors.New("no such account")

// OpenAdmin opens the database of a relay.
func OpenAdmin(dbPath string) (*Admin, error) {
	st, err := openStore(dbPath)
	if err != nil {
		return nil, err
	}
	return &Admin{st: st}, nil
}

func (a *Admin) Close() error { return a.st.Close() }

// Accounts lists every account, the oldest first.
func (a *Admin) Accounts(ctx context.Context) ([]AccountInfo, error) {
	rows, err := a.st.accounts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]AccountInfo, 0, len(rows))
	for _, r := range rows {
		u, err := a.st.tenant(r.id).usage(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, AccountInfo{
			ID: r.id, Plan: r.plan, Suspended: r.status != accountActive,
			Created: time.UnixMilli(r.createdMs), LastActive: time.UnixMilli(r.lastActive),
			Devices: u.devices, Machines: u.machines, Sessions: u.sessions, EventBytes: u.eventBytes, ImageBytes: u.imageBytes,
		})
	}
	return out, nil
}

// SetSuspended suspends or resumes an account: a suspended account's
// devices cannot log in or register, and what it stored is kept.
func (a *Admin) SetSuspended(ctx context.Context, id string, suspended bool) error {
	status := accountActive
	if suspended {
		status = accountSuspended
	}
	if err := a.st.setAccountStatus(ctx, id, status); errors.Is(err, errNoRows) {
		return ErrNoAccount
	} else {
		return err
	}
}

// SetPlan moves an account to another plan.
func (a *Admin) SetPlan(ctx context.Context, id, plan string) error {
	if _, ok := plans[plan]; !ok {
		return errors.New("unknown plan; use owner or trial")
	}
	res, err := a.st.db.ExecContext(ctx, `UPDATE accounts SET plan = ? WHERE id = ?`, plan, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNoAccount
	}
	return nil
}

// Delete deletes an account and everything the relay stores for it.
func (a *Admin) Delete(ctx context.Context, id string) error {
	if err := a.st.deleteAccount(ctx, id); errors.Is(err, errNoRows) {
		return ErrNoAccount
	} else {
		return err
	}
}
