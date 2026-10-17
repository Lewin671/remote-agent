package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"syscall"
	"time"

	"connectrpc.com/connect"
)

// Accounts. One relay holds many accounts that know nothing of each
// other (scope §2). An account is identified by its account key, the
// relay by nothing a client chooses: every request of a logged-in device
// is served from the account of its token, through a tenant, and no
// handler has a way to reach another account.

// Signup decides who may create an account on this relay.
type Signup string

const (
	// SignupToken creates accounts with the setup token, up to
	// MaxAccounts: a relay of one's own.
	SignupToken Signup = "token"
	// SignupOpen lets anyone create an account with a proof of work, up to
	// MaxAccounts: a relay for others to try remote-agent on.
	SignupOpen Signup = "open"
)

// plan is what an account may store. Zero means no limit.
type plan struct {
	name       string
	devices    int
	machines   int
	sessions   int
	eventBytes int64
	imageBytes int64
	// revoked bounds the devices an account removed, whose keys the relay
	// remembers for good; prefs the preference objects it stores.
	revoked int
	prefs   int
	// streams bounds the account's open streams, a main and a terminal
	// stream for each of its devices.
	streams int
}

// Plans: accounts made with the setup token belong to the relay's owner;
// accounts anyone made are for trying remote-agent out.
var plans = map[string]plan{
	"owner": {name: "owner", devices: 32, machines: 16, prefs: 16, streams: 64},
	"trial": {name: "trial", devices: 8, machines: 2, sessions: 200, eventBytes: 256 << 20, imageBytes: 64 << 20, revoked: 64, prefs: 16, streams: 16},
}

func planOf(name string) plan {
	if p, ok := plans[name]; ok {
		return p
	}
	return plans["trial"]
}

// errFull is the error of every refusal for lack of room.
func errFull(what string) error {
	return connect.NewError(connect.CodeResourceExhausted, errors.New(what))
}

// check refuses usage beyond the plan. The image quota is checked by
// putImage itself, which knows the size of the image.
func (p plan) check(u accountUsage) error {
	switch {
	case p.devices > 0 && u.devices > p.devices:
		return errFull(fmt.Sprintf("this account has its %d devices already; remove one first", p.devices))
	case p.machines > 0 && u.machines > p.machines:
		return errFull(fmt.Sprintf("this account has its %d computers already; remove one first", p.machines))
	case p.sessions > 0 && u.sessions > p.sessions:
		return errFull(fmt.Sprintf("this account has its %d sessions already on this relay", p.sessions))
	case p.eventBytes > 0 && u.eventBytes > p.eventBytes:
		return errFull("this account's storage on this relay is full")
	case p.revoked > 0 && u.revoked > p.revoked:
		return errFull(fmt.Sprintf("this account has removed %d devices on this relay, which takes no more", p.revoked))
	case p.prefs > 0 && u.prefs > p.prefs:
		return errFull(fmt.Sprintf("this account has its %d preference objects already", p.prefs))
	}
	return nil
}

// tenant is one account as a request sees it: its store, its streams
// and its plan.
type tenant struct {
	acct  string
	plan  plan
	store *tstore
	hub   accountHub
}

func (s *Server) tenantOf(d *device) *tenant {
	return &tenant{acct: d.account, plan: planOf(d.plan), store: s.store.tenant(d.account), hub: s.hub.account(d.account)}
}

// tenantFrom returns the tenant of a logged-in request.
func (s *Server) tenantFrom(ctx context.Context) *tenant {
	return s.tenantOf(deviceFrom(ctx))
}

// limit is the check of a tenant's quota and of the relay's disk, for the
// writes that make an account grow.
func (t *tenant) limit(s *Server) func(accountUsage) error {
	return func(u accountUsage) error {
		if s.diskLow.Load() {
			return errFull("this relay is out of storage; try again later")
		}
		return t.plan.check(u)
	}
}

// imageQuota is the bytes of images the tenant may store.
func (t *tenant) imageQuota(s *Server) int64 {
	if t.plan.imageBytes > 0 {
		return min(t.plan.imageBytes, s.cfg.ImageQuota)
	}
	return s.cfg.ImageQuota
}

// watchDisk keeps diskLow up to date: below MinFreeBytes of free space,
// accounts stop growing, so that the relay keeps serving what it holds.
func (s *Server) watchDisk(stop <-chan struct{}) {
	if s.cfg.MinFreeBytes <= 0 {
		return
	}
	dir := filepath.Dir(s.cfg.DBPath)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		var st syscall.Statfs_t
		if err := syscall.Statfs(dir, &st); err == nil {
			free := int64(st.Bavail) * int64(st.Bsize)
			low := free < s.cfg.MinFreeBytes
			if low != s.diskLow.Load() {
				slog.Warn("relay storage", "low", low, "free_bytes", free)
			}
			s.diskLow.Store(low)
		}
		select {
		case <-stop:
			return
		case <-t.C:
		}
	}
}

// sweepAccounts ends the streams, main and terminal, of devices whose
// account was suspended or deleted, or which were removed, by the
// operator's admin command while they were connected.
func (s *Server) sweepAccounts(stop <-chan struct{}) {
	t := time.NewTicker(s.cfg.SweepInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		for _, id := range s.streamDevices() {
			d, err := s.store.device(context.Background(), id)
			if err == nil {
				var a *accountRow
				if a, err = s.store.account(context.Background(), d.account); err == nil && a.status == accountActive {
					continue
				}
			}
			if errors.Is(err, errNoRows) || err == nil {
				s.revokeStreams(id)
			}
		}
	}
}
