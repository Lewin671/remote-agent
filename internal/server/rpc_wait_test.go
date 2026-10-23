package server

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"connectrpc.com/connect"
)

type rpcWaitingSnapshot struct {
	total, accountQueued, globalQueued int
	waiting, active                    map[string][3]int
}

func snapshotRPCWaiting(s *Server) rpcWaitingSnapshot {
	s.rpcMu.Lock()
	defer s.rpcMu.Unlock()
	out := rpcWaitingSnapshot{total: s.rpcWaiterCount, globalQueued: len(s.rpcWaiters), waiting: maps.Clone(s.accountRPCWaiters), active: maps.Clone(s.accountRPCRequests)}
	for _, q := range s.accountRPCQueues {
		out.accountQueued += len(q)
	}
	return out
}
func holdRPCAccount(t *testing.T, s *Server, account string, pools ...rpcPool) {
	t.Helper()
	s.rpcMu.Lock()
	ok := s.takeAccountRPCsLocked(account, pools...)
	s.rpcMu.Unlock()
	if !ok {
		t.Fatal("fixture could not reserve an account share")
	}
}
func requireRPCWaitOverflow(t *testing.T, err error) {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodeResourceExhausted || ce.Meta().Get("Retry-After") != "1" {
		t.Fatalf("waiter overflow: %v", err)
	}
}
func requireNoRPCWaiters(t *testing.T, s *Server) {
	t.Helper()
	got := snapshotRPCWaiting(s)
	if got.total != 0 || got.accountQueued != 0 || got.globalQueued != 0 || len(got.waiting) != 0 {
		t.Fatalf("waiting reservations leaked: %+v", got)
	}
}

func TestRPCAccountWaiterOverflowIsImmediate(t *testing.T) {
	for _, tc := range []struct {
		name             string
		pool, heldPool   rpcPool
		forward          bool
		holders, waiters int
	}{
		{"small", smallRPCPool, smallRPCPool, false, maxAccountSmallRPCRequests, maxAccountSmallRPCRequests},
		{"large", largeRPCPool, largeRPCPool, false, maxAccountLargeRPCRequests, maxAccountLargeRPCRequests},
		// A Forward is also charged against its decoder's waiter allowance.
		{"forward", smallRPCPool, forwardRPCPool, true, maxAccountForwardRequests, maxAccountSmallRPCRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := newRPCAdmissionServer()
				for range tc.holders {
					holdRPCAccount(t, s, "A", tc.heldPool)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				pending := make([]<-chan rpcAdmissionResult, tc.waiters)
				for i := range pending {
					pending[i] = startGlobalRPC(s, ctx, "A", tc.pool, tc.forward)
				}
				synctest.Wait()
				before := snapshotRPCWaiting(s)
				if before.total != tc.waiters || before.accountQueued != tc.waiters {
					t.Fatalf("waiters were not bounded: %+v", before)
				}
				start := time.Now()
				a, err := s.admitRPC(ctx, "A", tc.pool, tc.forward)
				if a != nil {
					a.release()
					t.Fatal("overflow acquired a share")
				}
				requireRPCWaitOverflow(t, err)
				if !time.Now().Equal(start) {
					t.Fatal("overflow waited instead of refusing immediately")
				}
				after := snapshotRPCWaiting(s)
				if after.total != before.total || after.active["A"] != before.active["A"] || after.waiting["A"] != before.waiting["A"] {
					t.Fatal("overflow changed another request's reservations")
				}
				cancel()
				synctest.Wait()
				for _, done := range pending {
					if got := <-done; got.admission != nil || connect.CodeOf(got.err) != connect.CodeCanceled {
						t.Fatalf("cancel queued request: %v", got.err)
					}
				}
				requireNoRPCWaiters(t, s)
				for range tc.holders {
					s.releaseAccountRPC("A", tc.heldPool)
				}
			})
		})
	}
}

func TestRPCWaiterAllowanceIncludesGlobalQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newRPCAdmissionServer()
		held := fillGlobalRPCs(t, s, largeRPCPool)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		pending := make([]<-chan rpcAdmissionResult, maxAccountLargeRPCRequests)
		for i := range pending {
			pending[i] = startGlobalRPC(s, ctx, "A", largeRPCPool, true)
		}
		synctest.Wait()
		before := snapshotRPCWaiting(s)
		if before.total != maxAccountLargeRPCRequests || before.globalQueued != maxAccountLargeRPCRequests || before.waiting["A"] != [3]int{maxAccountLargeRPCRequests, 0, maxAccountLargeRPCRequests} {
			t.Fatalf("global waiters were not charged to both pools: %+v", before)
		}
		start := time.Now()
		a, err := s.admitRPC(ctx, "A", largeRPCPool, true)
		if a != nil {
			a.release()
			t.Fatal("global queue overflow acquired capacity")
		}
		requireRPCWaitOverflow(t, err)
		if !time.Now().Equal(start) {
			t.Fatal("account and global stages received separate waiter allowances")
		}
		cancel()
		synctest.Wait()
		for _, done := range pending {
			if got := <-done; connect.CodeOf(got.err) != connect.CodeCanceled {
				t.Fatal(got.err)
			}
		}
		requireNoRPCWaiters(t, s)
		if len(snapshotRPCWaiting(s).active) != 0 {
			t.Fatal("global cancellation leaked account shares")
		}
		for _, a := range held {
			a.release()
		}
	})
}

func TestRPCAccountReleaseSignalsOnlyOneWaiter(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newRPCAdmissionServer()
		for range maxAccountSmallRPCRequests {
			holdRPCAccount(t, s, "A", smallRPCPool)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		pending := make([]<-chan rpcAdmissionResult, maxAccountSmallRPCRequests)
		for i := range pending {
			pending[i] = startGlobalRPC(s, ctx, "A", smallRPCPool, false)
			synctest.Wait()
		}
		s.rpcMu.Lock()
		queue := slices.Clone(s.accountRPCQueues["A"])
		s.rpcMu.Unlock()
		s.releaseAccountRPC("A", smallRPCPool)
		synctest.Wait()
		signaled := 0
		for _, w := range queue {
			select {
			case <-w.ready:
				signaled++
			default:
			}
		}
		if signaled != 1 {
			t.Fatalf("one released share signaled %d waiters", signaled)
		}
		first := <-pending[0]
		if first.err != nil {
			t.Fatal(first.err)
		}
		for _, done := range pending[1:] {
			select {
			case got := <-done:
				if got.admission != nil {
					got.admission.release()
				}
				t.Fatal("a later waiter bypassed FIFO or woke without a share")
			default:
			}
		}
		got := snapshotRPCWaiting(s)
		if got.total != maxAccountSmallRPCRequests-1 || got.accountQueued != maxAccountSmallRPCRequests-1 {
			t.Fatalf("unrelated waiters were woken: %+v", got)
		}
		cancel()
		synctest.Wait()
		for _, done := range pending[1:] {
			if got := <-done; connect.CodeOf(got.err) != connect.CodeCanceled {
				t.Fatal(got.err)
			}
		}
		first.admission.release()
		for range maxAccountSmallRPCRequests - 1 {
			s.releaseAccountRPC("A", smallRPCPool)
		}
		requireNoRPCWaiters(t, s)
	})
}

func TestRPCWaiterReservationIsAtomicForMultiplePools(t *testing.T) {
	for pool, limit := range rpcAccountShares() {
		t.Run(fmt.Sprint(pool), func(t *testing.T) {
			s := newRPCAdmissionServer()
			var held []*rpcWaitReservation
			for range limit {
				r := &rpcWaitReservation{account: "A", pools: []rpcPool{rpcPool(pool)}}
				s.rpcMu.Lock()
				ok := s.reserveRPCWaiterLocked(r)
				s.rpcMu.Unlock()
				if !ok {
					t.Fatal("waiter allowance refused too early")
				}
				held = append(held, r)
			}
			overflow := &rpcWaitReservation{account: "A", pools: []rpcPool{rpcPool(pool)}}
			s.rpcMu.Lock()
			ok := s.reserveRPCWaiterLocked(overflow)
			s.rpcMu.Unlock()
			if ok || overflow.held {
				t.Fatal("per-pool waiter allowance exceeded its account share")
			}
			for _, r := range held {
				s.releaseRPCWaiter(r)
			}
			requireNoRPCWaiters(t, s)
		})
	}
	s := newRPCAdmissionServer()
	var held []*rpcWaitReservation
	for range maxAccountLargeRPCRequests {
		r := &rpcWaitReservation{account: "A", pools: []rpcPool{largeRPCPool, forwardRPCPool}}
		s.rpcMu.Lock()
		ok := s.reserveRPCWaiterLocked(r)
		s.rpcMu.Unlock()
		if !ok {
			t.Fatal("large Forward waiter refused too early")
		}
		held = append(held, r)
	}
	before := snapshotRPCWaiting(s)
	// Check Forward first, then fail on Large: neither counter may change.
	overflow := &rpcWaitReservation{account: "A", pools: []rpcPool{forwardRPCPool, largeRPCPool}}
	s.rpcMu.Lock()
	ok := s.reserveRPCWaiterLocked(overflow)
	s.rpcMu.Unlock()
	after := snapshotRPCWaiting(s)
	if ok || after.total != before.total || after.waiting["A"] != before.waiting["A"] {
		t.Fatal("failed multi-pool reservation changed a counter")
	}
	for _, r := range held {
		s.releaseRPCWaiter(r)
	}
	requireNoRPCWaiters(t, s)
}

func TestRPCWaiterRelayCapBoundsParkedRequests(t *testing.T) {
	for _, stage := range []string{"account", "global", "mixed"} {
		t.Run(stage, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := newRPCAdmissionServer()
				var globals []*rpcAdmission
				if stage != "account" {
					globals = fillGlobalRPCs(t, s, smallRPCPool)
				}
				heldAccounts := map[string]bool{}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var live atomic.Int32
				results := make(chan error, maxRPCWaiters)
				for i := 0; i < maxRPCWaiters; i++ {
					account := fmt.Sprintf("global-%d", i/maxAccountSmallRPCRequests)
					if stage == "account" || stage == "mixed" && i < maxRPCWaiters/2 {
						account = fmt.Sprintf("held-%d", i/maxAccountSmallRPCRequests)
						if !heldAccounts[account] {
							for range maxAccountSmallRPCRequests {
								holdRPCAccount(t, s, account, smallRPCPool)
							}
							heldAccounts[account] = true
						}
					}
					live.Add(1)
					go func() {
						defer live.Add(-1)
						a, err := s.admitRPC(ctx, account, smallRPCPool, false)
						if a != nil {
							a.release()
						}
						results <- err
					}()
				}
				synctest.Wait()
				got := snapshotRPCWaiting(s)
				if got.total != maxRPCWaiters || got.accountQueued+got.globalQueued != maxRPCWaiters || live.Load() != maxRPCWaiters {
					t.Fatalf("relay cap setup: %+v live=%d", got, live.Load())
				}
				if stage == "account" {
					for range maxAccountSmallRPCRequests {
						holdRPCAccount(t, s, "overflow", smallRPCPool)
					}
					heldAccounts["overflow"] = true
				}
				// Overflow goroutines must finish immediately, leaving only the capped
				// admission waiters parked; runtime-wide goroutine counts are unrelated.
				const excess = 16
				overflow := make(chan error, excess)
				start := time.Now()
				for range excess {
					live.Add(1)
					go func() {
						defer live.Add(-1)
						a, err := s.admitRPC(ctx, "overflow", smallRPCPool, false)
						if a != nil {
							a.release()
						}
						overflow <- err
					}()
				}
				synctest.Wait()
				if live.Load() != maxRPCWaiters || !time.Now().Equal(start) {
					t.Fatalf("overflow parked extra requests: live=%d", live.Load())
				}
				for range excess {
					requireRPCWaitOverflow(t, <-overflow)
				}
				if snapshotRPCWaiting(s).total != maxRPCWaiters {
					t.Fatal("relay overflow changed waiting reservations")
				}
				// The waiting limit must not reject work that can run immediately.
				pool := largeRPCPool
				if stage == "account" {
					pool = smallRPCPool
				}
				admitted, err := s.admitRPC(ctx, "independent", pool, false)
				if err != nil {
					t.Fatalf("waiter cap blocked free capacity: %v", err)
				}
				if snapshotRPCWaiting(s).total != maxRPCWaiters {
					t.Fatal("immediate work consumed a waiting allowance")
				}
				admitted.release()
				cancel()
				synctest.Wait()
				for range maxRPCWaiters {
					if err := <-results; connect.CodeOf(err) != connect.CodeCanceled {
						t.Fatalf("parked request: %v", err)
					}
				}
				if live.Load() != 0 {
					t.Fatal("admission goroutines remained after cancellation")
				}
				requireNoRPCWaiters(t, s)
				for account := range heldAccounts {
					for range maxAccountSmallRPCRequests {
						s.releaseAccountRPC(account, smallRPCPool)
					}
				}
				for _, a := range globals {
					a.release()
				}
				if len(snapshotRPCWaiting(s).active) != 0 {
					t.Fatal("account shares leaked")
				}
			})
		})
	}
}

func TestRPCWaiterReservationSurvivesStageHandoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newRPCAdmissionServer()
		globals := fillGlobalRPCs(t, s, largeRPCPool)
		for range maxAccountLargeRPCRequests {
			holdRPCAccount(t, s, "A", largeRPCPool)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := startGlobalRPC(s, ctx, "A", largeRPCPool, true)
		synctest.Wait()
		var held []*rpcWaitReservation
		for i := 0; i < maxRPCWaiters-1; i++ {
			r := &rpcWaitReservation{account: fmt.Sprint(i / maxAccountSmallRPCRequests), pools: []rpcPool{smallRPCPool}}
			s.rpcMu.Lock()
			ok := s.reserveRPCWaiterLocked(r)
			s.rpcMu.Unlock()
			if !ok {
				t.Fatal("relay allowance filled too early")
			}
			held = append(held, r)
		}
		s.releaseAccountRPC("A", largeRPCPool)
		synctest.Wait()
		got := snapshotRPCWaiting(s)
		if got.total != maxRPCWaiters || got.globalQueued != 1 || got.accountQueued != 0 || got.waiting["A"] != [3]int{1, 0, 1} {
			t.Fatalf("handoff lost or duplicated the waiting reservation: %+v", got)
		}
		globals[0].release()
		synctest.Wait()
		result := <-done
		if result.err != nil {
			t.Fatal(result.err)
		}
		got = snapshotRPCWaiting(s)
		if got.total != maxRPCWaiters-1 || got.waiting["A"] != [3]int{} {
			t.Fatal("successful admission retained its waiting reservation")
		}
		result.admission.release()
		s.releaseAccountRPC("A", largeRPCPool)
		for _, r := range held {
			s.releaseRPCWaiter(r)
		}
		for _, a := range globals[1:] {
			a.release()
		}
		requireNoRPCWaiters(t, s)
	})
}

func TestRPCAccountWaiterCancellationAfterGrant(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newRPCAdmissionServer()
		for range maxAccountLargeRPCRequests {
			holdRPCAccount(t, s, "A", largeRPCPool)
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := startGlobalRPC(s, ctx, "A", largeRPCPool, true)
		synctest.Wait()
		s.rpcMu.Lock()
		s.releaseAccountRPCsLocked("A", largeRPCPool)
		cancel()
		s.rpcMu.Unlock()
		synctest.Wait()
		got := <-done
		if got.admission != nil || connect.CodeOf(got.err) != connect.CodeCanceled {
			t.Fatal(got.err)
		}
		if snapshotRPCWaiting(s).active["A"] != [3]int{maxAccountLargeRPCRequests - 1, 0, 0} || len(s.rpcRequests) != 0 {
			t.Fatal("cancellation released another request's share")
		}
		requireNoRPCWaiters(t, s)
		s.releaseAccountRPC("A", largeRPCPool)
	})
}
