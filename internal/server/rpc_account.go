package server

import (
	"context"
	"slices"
	"time"
)

// Reserve all account shares together, before decoding or taking any global
// slot. Each waiter is handed its shares before its own channel is signaled.
func (s *Server) waitAccountRPCs(ctx context.Context, r *rpcWaitReservation, deadline time.Time) error {
	if r.account == "" {
		return rpcContextError(ctx)
	}
	s.rpcMu.Lock()
	if err := rpcWaitFailure(ctx, deadline); err != nil {
		s.rpcMu.Unlock()
		return err
	}
	s.dispatchAccountRPCsLocked(r.account)
	if !rpcWaiterAhead(s.accountRPCQueues[r.account], r.pools) && s.takeAccountRPCsLocked(r.account, r.pools...) {
		s.rpcMu.Unlock()
		return nil
	}
	if !s.reserveRPCWaiterLocked(r) {
		s.rpcMu.Unlock()
		return errRPCAdmission
	}
	w := &rpcWaiter{ctx: ctx, deadline: deadline, pools: r.pools, ready: make(chan struct{})}
	s.accountRPCQueues[r.account] = append(s.accountRPCQueues[r.account], w)
	s.rpcMu.Unlock()
	w.wait()
	s.rpcMu.Lock()
	defer s.rpcMu.Unlock()
	err := rpcWaitFailure(ctx, deadline)
	if err == nil {
		err = w.err
	}
	if err != nil {
		if w.granted {
			s.releaseAccountRPCsLocked(r.account, r.pools...)
		} else {
			pending := slices.DeleteFunc(s.accountRPCQueues[r.account], func(candidate *rpcWaiter) bool { return candidate == w })
			if len(pending) == 0 {
				delete(s.accountRPCQueues, r.account)
			} else {
				s.accountRPCQueues[r.account] = pending
			}
			s.dispatchAccountRPCsLocked(r.account)
		}
	}
	return err
}

func (s *Server) dispatchAccountRPCsLocked(account string) {
	var blocked [3]bool
	old := s.accountRPCQueues[account]
	pending := old[:0]
	for _, w := range old {
		w.err = rpcWaitFailure(w.ctx, w.deadline)
		if w.err != nil {
			close(w.ready)
			continue
		}
		eligible := true
		for _, pool := range w.pools {
			if blocked[pool] {
				eligible = false
			}
		}
		if eligible && s.takeAccountRPCsLocked(account, w.pools...) {
			w.granted = true
			close(w.ready)
		} else {
			for _, pool := range w.pools {
				blocked[pool] = true
			}
			pending = append(pending, w)
		}
	}
	clear(old[len(pending):])
	if len(pending) == 0 {
		delete(s.accountRPCQueues, account)
	} else {
		s.accountRPCQueues[account] = pending
	}
}

func (s *Server) releaseAccountRPCsLocked(account string, pools ...rpcPool) {
	counts := s.accountRPCRequests[account]
	for _, pool := range pools {
		counts[pool]--
	}
	if counts == [3]int{} {
		delete(s.accountRPCRequests, account)
	} else {
		s.accountRPCRequests[account] = counts
	}
	s.dispatchAccountRPCsLocked(account)
}
