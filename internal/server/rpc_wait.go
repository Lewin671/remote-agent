package server

import (
	"context"
	"time"
)

// rpcWaiter is queued only after a bounded waiting reservation is held.
type rpcWaiter struct {
	ctx      context.Context
	deadline time.Time
	pools    []rpcPool
	ready    chan struct{}
	granted  bool
	err      error
}

func rpcWaitFailure(ctx context.Context, deadline time.Time) error {
	if err := rpcContextError(ctx); err != nil {
		return err
	}
	if !time.Now().Before(deadline) {
		return errRPCAdmission
	}
	return nil
}

func (w *rpcWaiter) wait() {
	timer := time.NewTimer(time.Until(w.deadline))
	defer timer.Stop()
	select {
	case <-w.ready:
	case <-w.ctx.Done():
	case <-timer.C:
	}
}

func rpcWaiterAhead(queue []*rpcWaiter, pools []rpcPool) bool {
	for _, w := range queue {
		for _, older := range w.pools {
			for _, pool := range pools {
				if older == pool {
					return true
				}
			}
		}
	}
	return false
}

func rpcAccountShares() [3]int {
	return [3]int{maxAccountLargeRPCRequests, maxAccountSmallRPCRequests, maxAccountForwardRequests}
}

// One reservation follows a request across account and global waiting. A
// Forward counts against both pool allowances but only once relay-wide.
type rpcWaitReservation struct {
	account string
	pools   []rpcPool
	held    bool
}

func (s *Server) reserveRPCWaiterLocked(r *rpcWaitReservation) bool {
	if r.held {
		return true
	}
	if s.rpcWaiterCount >= maxRPCWaiters {
		return false
	}
	limits := rpcAccountShares()
	counts := s.accountRPCWaiters[r.account]
	for _, pool := range r.pools {
		if counts[pool] >= limits[pool] {
			return false
		}
		counts[pool]++
	}
	s.accountRPCWaiters[r.account] = counts
	s.rpcWaiterCount++
	r.held = true
	return true
}

func (s *Server) releaseRPCWaiter(r *rpcWaitReservation) {
	s.rpcMu.Lock()
	defer s.rpcMu.Unlock()
	if !r.held {
		return
	}
	counts := s.accountRPCWaiters[r.account]
	for _, pool := range r.pools {
		counts[pool]--
	}
	if counts == [3]int{} {
		delete(s.accountRPCWaiters, r.account)
	} else {
		s.accountRPCWaiters[r.account] = counts
	}
	s.rpcWaiterCount--
	r.held = false
}
