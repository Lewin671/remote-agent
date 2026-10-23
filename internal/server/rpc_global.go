package server

import (
	"context"
	"slices"
	"time"
)

type rpcGlobalWaiter struct {
	ctx      context.Context
	deadline time.Time
	pools    []rpcPool
	ready    chan struct{}
	granted  bool
	err      error
}

func (s *Server) globalRPCSlots(pool rpcPool) chan struct{} {
	switch pool {
	case smallRPCPool:
		return s.smallRPCRequests
	case forwardRPCPool:
		return s.forwardRPCRequests
	default:
		return s.rpcRequests
	}
}

func (s *Server) takeGlobalRPCsLocked(pools []rpcPool) bool {
	for _, pool := range pools {
		slots := s.globalRPCSlots(pool)
		if len(slots) == cap(slots) {
			return false
		}
	}
	for _, pool := range pools {
		s.globalRPCSlots(pool) <- struct{}{}
	}
	return true
}

// Authenticated waiters are FIFO for every shared pool. Independent pools
// can advance; an older multi-pool waiter reserves ordering, not capacity.
func (s *Server) dispatchGlobalRPCsLocked() {
	var blocked [3]bool
	old := s.rpcWaiters
	pending := old[:0]
	for _, w := range old {
		w.err = rpcContextError(w.ctx)
		if w.err == nil && !time.Now().Before(w.deadline) {
			w.err = errRPCAdmission
		}
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
		if eligible && s.takeGlobalRPCsLocked(w.pools) {
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
	s.rpcWaiters = pending
}

func (s *Server) waitGlobalRPCs(ctx context.Context, account string, deadline time.Time, pools []rpcPool) error {
	s.rpcMu.Lock()
	if account == "" {
		// Login and pairing retain their immediate, non-queued global admission.
		defer s.rpcMu.Unlock()
		if err := rpcContextError(ctx); err != nil {
			return err
		}
		if s.takeGlobalRPCsLocked(pools) {
			return nil
		}
		return errRPCGlobalAdmission
	}
	w := &rpcGlobalWaiter{ctx: ctx, deadline: deadline, pools: pools, ready: make(chan struct{})}
	s.rpcWaiters = append(s.rpcWaiters, w)
	s.dispatchGlobalRPCsLocked()
	s.rpcMu.Unlock()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case <-w.ready:
	case <-ctx.Done():
	case <-timer.C:
	}
	s.rpcMu.Lock()
	defer s.rpcMu.Unlock()
	err := rpcContextError(ctx)
	if err == nil && !time.Now().Before(deadline) {
		err = errRPCAdmission
	}
	if err == nil {
		err = w.err
	}
	if err != nil {
		if w.granted {
			for _, pool := range pools {
				<-s.globalRPCSlots(pool)
			}
		} else {
			s.rpcWaiters = slices.DeleteFunc(s.rpcWaiters, func(candidate *rpcGlobalWaiter) bool { return candidate == w })
		}
		s.dispatchGlobalRPCsLocked()
	}
	return err
}

func (s *Server) releaseGlobalRPCs(pools ...rpcPool) {
	s.rpcMu.Lock()
	defer s.rpcMu.Unlock()
	for _, pool := range pools {
		<-s.globalRPCSlots(pool)
	}
	s.dispatchGlobalRPCsLocked()
}
