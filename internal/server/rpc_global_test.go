package server

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"connectrpc.com/connect"
)

func globalRPCQueueSize(s *Server) int {
	s.rpcMu.Lock()
	defer s.rpcMu.Unlock()
	return len(s.rpcWaiters)
}

func startGlobalRPC(s *Server, ctx context.Context, account string, pool rpcPool, forward bool) <-chan rpcAdmissionResult {
	done := make(chan rpcAdmissionResult, 1)
	go func() { a, err := s.admitRPC(ctx, account, pool, forward); done <- rpcAdmissionResult{a, err} }()
	return done
}

func fillGlobalRPCs(t *testing.T, s *Server, pool rpcPool) []*rpcAdmission {
	t.Helper()
	held := make([]*rpcAdmission, cap(s.globalRPCSlots(pool)))
	for i := range held {
		var err error
		held[i], err = s.admitRPC(context.Background(), "", pool, false)
		if err != nil {
			t.Fatal(err)
		}
	}
	return held
}

func TestGlobalRPCBurstFromOneAccount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newRPCAdmissionServer()
		held := fillGlobalRPCs(t, s, largeRPCPool)
		// Six images exceed the four-slot global pool and the two-slot share.
		entered := make(chan *rpcAdmission, 6)
		done := make(chan error, 6)
		for range 6 {
			go func() {
				a, err := s.admitRPC(context.Background(), "A", largeRPCPool, false)
				if err == nil {
					entered <- a
				}
				done <- err
			}()
		}
		synctest.Wait()
		if len(entered) != 0 || globalRPCQueueSize(s) != maxAccountLargeRPCRequests {
			t.Fatal("global wait decoded a body or did not wait")
		}
		held[0].release()
		held[1].release()
		for range 3 {
			synctest.Wait()
			if len(entered) != 2 {
				t.Fatalf("burst admissions=%d, want two", len(entered))
			}
			first, second := <-entered, <-entered
			time.Sleep(time.Second)
			first.release()
			second.release()
		}
		synctest.Wait()
		for range 6 {
			if err := <-done; err != nil {
				t.Fatalf("burst refused: %v", err)
			}
		}
		held[2].release()
		held[3].release()
		if len(s.accountRPCRequests) != 0 || len(s.rpcRequests) != 0 || globalRPCQueueSize(s) != 0 {
			t.Fatal("burst reservations leaked")
		}
	})
}

func TestGlobalRPCWaitUsesRemainingAccountDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newRPCAdmissionServer()
		held := fillGlobalRPCs(t, s, largeRPCPool)
		// Existing requests can hold account shares while waiting globally.
		for range maxAccountLargeRPCRequests {
			if err := s.waitAccountRPCs(context.Background(), "A", time.Now().Add(rpcAdmissionWaitTimeout), largeRPCPool); err != nil {
				t.Fatal(err)
			}
		}
		start := time.Now()
		done := startGlobalRPC(s, context.Background(), "A", largeRPCPool, true)
		synctest.Wait()
		time.Sleep(3 * time.Second)
		s.releaseAccountRPC("A", largeRPCPool)
		synctest.Wait()
		if globalRPCQueueSize(s) != 1 || len(s.forwardRPCRequests) != 0 {
			t.Fatal("global wait acquired a partial Forward reservation")
		}
		time.Sleep(2*time.Second - time.Nanosecond)
		select {
		case <-done:
			t.Fatal("admission timed out before the shared deadline")
		default:
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		got := <-done
		if got.admission != nil || connect.CodeOf(got.err) != connect.CodeResourceExhausted || time.Since(start) != rpcAdmissionWaitTimeout {
			t.Fatalf("shared deadline: %v %v after %v", got.admission, got.err, time.Since(start))
		}
		var ce *connect.Error
		if !errors.As(got.err, &ce) || ce.Meta().Get("Retry-After") != "1" {
			t.Fatal("admission refusal lost Retry-After")
		}
		if s.accountRPCRequests["A"] != [3]int{maxAccountLargeRPCRequests - 1, 0, 0} {
			t.Fatal("timeout released an existing owner's share")
		}
		s.releaseAccountRPC("A", largeRPCPool)
		for _, a := range held {
			a.release()
		}
	})
}

func TestGlobalRPCFIFOAcrossAccounts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newRPCAdmissionServer()
		held := fillGlobalRPCs(t, s, largeRPCPool)
		first := startGlobalRPC(s, context.Background(), "A", largeRPCPool, false)
		synctest.Wait()
		ctx, cancel := context.WithCancel(context.Background())
		second := startGlobalRPC(s, ctx, "A", largeRPCPool, false)
		synctest.Wait()
		b := startGlobalRPC(s, context.Background(), "B", largeRPCPool, false)
		synctest.Wait()
		later := startGlobalRPC(s, context.Background(), "A", largeRPCPool, false)
		synctest.Wait()
		if globalRPCQueueSize(s) != 3 {
			t.Fatal("account waiters entered the global FIFO before owning a share")
		}
		held[0].release()
		synctest.Wait()
		a1 := <-first
		if a1.err != nil {
			t.Fatal(a1.err)
		}
		cancel()
		synctest.Wait()
		if got := <-second; got.admission != nil || connect.CodeOf(got.err) != connect.CodeCanceled {
			t.Fatalf("queued cancellation: %v", got.err)
		}
		// Cancellation lets the later A obtain its share, but B is already ahead.
		held[1].release()
		synctest.Wait()
		select {
		case got := <-later:
			if got.admission != nil {
				got.admission.release()
			}
			t.Fatalf("later A bypassed B: %v", got.err)
		default:
		}
		b1 := <-b
		if b1.err != nil {
			t.Fatal(b1.err)
		}
		held[2].release()
		synctest.Wait()
		a2 := <-later
		if a2.err != nil {
			t.Fatal(a2.err)
		}
		a1.admission.release()
		b1.admission.release()
		a2.admission.release()
		held[3].release()
		if len(s.accountRPCRequests) != 0 || globalRPCQueueSize(s) != 0 {
			t.Fatal("FIFO reservations leaked")
		}
	})
}

func TestGlobalRPCIndependentPoolsCanProceed(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newRPCAdmissionServer()
		held := fillGlobalRPCs(t, s, largeRPCPool)
		ctx, cancel := context.WithCancel(context.Background())
		pending := startGlobalRPC(s, ctx, "A", largeRPCPool, true)
		synctest.Wait()
		b, err := s.admitRPC(context.Background(), "B", smallRPCPool, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(s.forwardRPCRequests) != 0 {
			t.Fatal("multi-pool waiter held a global slot")
		}
		b.release()
		cancel()
		synctest.Wait()
		if got := <-pending; got.admission != nil || connect.CodeOf(got.err) != connect.CodeCanceled {
			t.Fatal(got.err)
		}
		for _, a := range held {
			a.release()
		}
	})
}

func TestGlobalRPCWaitCancellationReleasesOnlyOwnedSlots(t *testing.T) {
	for _, reason := range []string{"cancel queued", "request deadline", "cancel after grant"} {
		t.Run(reason, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := newRPCAdmissionServer()
				held := fillGlobalRPCs(t, s, largeRPCPool)
				ctx, cancel := context.WithCancel(context.Background())
				if reason == "request deadline" {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), time.Second)
				}
				defer cancel()
				pending := startGlobalRPC(s, ctx, "A", largeRPCPool, true)
				synctest.Wait()
				expected := maxConcurrentRPCRequests
				want := connect.CodeCanceled
				if reason == "cancel after grant" {
					// Keep the recipient from accepting the grant until cancellation is visible.
					s.rpcMu.Lock()
					<-s.rpcRequests
					s.dispatchGlobalRPCsLocked()
					cancel()
					s.rpcMu.Unlock()
					expected--
				} else if reason == "request deadline" {
					want = connect.CodeDeadlineExceeded
					time.Sleep(time.Second)
				} else {
					cancel()
				}
				synctest.Wait()
				got := <-pending
				if got.admission != nil || connect.CodeOf(got.err) != want {
					t.Fatalf("canceled global admission: %v %v", got.admission, got.err)
				}
				if len(s.rpcRequests) != expected || len(s.forwardRPCRequests) != 0 || len(s.accountRPCRequests) != 0 || globalRPCQueueSize(s) != 0 {
					t.Fatal("cancellation released another owner's slot or leaked a reservation")
				}
				if expected != len(held) {
					held = held[1:]
				}
				for _, a := range held {
					a.release()
				}
			})
		})
	}
}

func TestGlobalRPCFIFOForMultiPoolWaiters(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newRPCAdmissionServer()
		held := fillGlobalRPCs(t, s, forwardRPCPool)
		older := startGlobalRPC(s, context.Background(), "A", largeRPCPool, true)
		synctest.Wait()
		later := startGlobalRPC(s, context.Background(), "B", largeRPCPool, false)
		synctest.Wait()
		// B's decoder pool is free, but B shares it with the older waiter.
		// An unrelated small RPC can still pass both of them.
		select {
		case got := <-later:
			if got.admission != nil {
				got.admission.release()
			}
			t.Fatal("later request bypassed an older multi-pool waiter")
		default:
		}
		if len(s.rpcRequests) != 0 {
			t.Fatal("multi-pool admission partially reserved capacity")
		}
		small, err := s.admitRPC(context.Background(), "C", smallRPCPool, false)
		if err != nil {
			t.Fatal(err)
		}
		small.release()
		held[0].release()
		synctest.Wait()
		first, second := <-older, <-later
		if first.err != nil || second.err != nil {
			t.Fatalf("multi-pool FIFO: %v %v", first.err, second.err)
		}
		if err := first.admission.forward(); err != nil {
			t.Fatal(err)
		}
		first.admission.release()
		second.admission.release()
		for _, a := range held[1:] {
			a.release()
		}
		if globalRPCQueueSize(s) != 0 || len(s.accountRPCRequests) != 0 {
			t.Fatal("multi-pool reservations leaked")
		}
	})
}
