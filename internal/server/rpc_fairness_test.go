package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
)

func TestRPCAccountAdmissionBeforeBodyRead(t *testing.T) {
	ts := newTestServer(t)
	ts.srv.cfg.MaxAccounts = 10
	a := ts.account(t, ts.keys)
	keys, _ := e2e.Derive(e2e.NewKey())
	b := ts.account(t, keys)
	for _, tc := range []struct {
		name          string
		length, limit int
		pool          rpcPool
	}{
		{"small", 1024, maxAccountSmallRPCRequests, smallRPCPool},
		{"large", smallRPCBodyBytes + 1, maxAccountLargeRPCRequests, largeRPCPool},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entered := make(chan string, tc.limit+2)
			release := make(chan struct{})
			var once sync.Once
			unblock := func() { once.Do(func() { close(release) }) }
			defer unblock()
			h := ts.srv.limitRPCRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Reaching here means admission happened before any body read.
				entered <- r.Header.Get("Authorization")
				<-release
			}), rpcBodyReadTimeout)
			done := make(chan struct{}, tc.limit+1)
			request := func(ctx context.Context, token string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodPost, remoteagentv1connect.ClientServiceGetEventsProcedure, strings.NewReader("x"))
				r.ContentLength = int64(tc.length)
				r.Header.Set("Content-Type", "application/proto")
				r.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r.WithContext(ctx))
				return w
			}
			for i := 0; i < tc.limit; i++ {
				// Machine and client tokens share one account's allowance.
				token := a.ctok
				if i%2 == 1 {
					token = a.mtok
				}
				go func() { request(context.Background(), token); done <- struct{}{} }()
				<-entered
			}
			// A's excess request waits; B can use the remaining global share.
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			rejected := make(chan *httptest.ResponseRecorder, 1)
			go func() { rejected <- request(ctx, a.ctok) }()
			go func() { request(context.Background(), b.ctok); done <- struct{}{} }()
			if got := <-entered; got != "Bearer "+b.ctok {
				t.Fatal("account A exceeded its share before a slot was released")
			}
			cancel()
			if w := <-rejected; !strings.Contains(w.Body.String(), "canceled") {
				t.Fatalf("canceled wait: %d %s", w.Code, w.Body.String())
			}
			unblock()
			for range tc.limit + 1 {
				<-done
			}
			ts.srv.rpcMu.Lock()
			defer ts.srv.rpcMu.Unlock()
			if len(ts.srv.accountRPCRequests) != 0 {
				t.Fatalf("account slots leaked: %v", ts.srv.accountRPCRequests)
			}
		})
	}
}

func TestWaitingForwardsHaveSeparateAdmission(t *testing.T) {
	ts := newTestServer(t)
	ts.srv.cfg.MaxAccounts = 10
	a := ts.account(t, ts.keys)
	keys, _ := e2e.Derive(e2e.NewKey())
	b := ts.account(t, keys)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, maxAccountForwardRequests)
	forward := func(a *testAccount) error {
		env, err := e2e.Seal(a.keys.Command, &pb.Header{Version: deviceauth.Version, Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND,
			MachineId: a.mid, RequestId: "0123456789abcdef0123456789abcdef", DeadlineMs: time.Now().Add(time.Minute).UnixMilli(), Sender: a.cid}, []byte("cmd"))
		if err != nil {
			return err
		}
		_, err = a.client.Forward(ctx, connect.NewRequest(&pb.ForwardRequest{MachineId: a.mid, Command: env}))
		return err
	}
	for range maxAccountForwardRequests {
		go func() { done <- forward(a) }()
		a.machineFrame.next(t, func(f *pb.ServerFrame) bool { return f.GetForwardCall() != nil })
	}
	if len(ts.srv.smallRPCRequests) != 0 || len(ts.srv.rpcRequests) != 0 {
		t.Fatalf("waiting forwards retained decoder slots: small=%d large=%d", len(ts.srv.smallRPCRequests), len(ts.srv.rpcRequests))
	}
	if _, err := a.client.ListMachines(ctx, connect.NewRequest(&pb.ListMachinesRequest{})); err != nil {
		t.Fatalf("waiting forwards blocked a small RPC: %v", err)
	}
	bDone := make(chan error, 1)
	go func() { bDone <- forward(b) }()
	call := b.machineFrame.next(t, func(f *pb.ServerFrame) bool { return f.GetForwardCall() != nil }).GetForwardCall()
	b.machineFrame.write(t, &pb.PeerFrame{Kind: &pb.PeerFrame_ForwardReply{ForwardReply: &pb.ForwardReply{CallId: call.CallId, Error: "test reply"}}})
	if err := <-bDone; connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("account B forward: %v", err)
	}
	cancel()
	for range maxAccountForwardRequests {
		<-done
	}
}

func TestForwardAdmissionGlobalBoundAndRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newRPCAdmissionServer()
		held := make([]*rpcAdmission, maxConcurrentForwardRequests)
		for i := range held {
			a, err := s.admitRPC(context.Background(), fmt.Sprint(i/maxAccountForwardRequests), smallRPCPool, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.forward(); err != nil {
				t.Fatal(err)
			}
			held[i] = a
		}
		done := make(chan rpcAdmissionResult, 1)
		go func() {
			a, err := s.admitRPC(context.Background(), "another-account", smallRPCPool, true)
			done <- rpcAdmissionResult{a, err}
		}()
		synctest.Wait()
		if len(s.smallRPCRequests) != 0 {
			t.Fatal("forward waiter reserved a decoder before global admission")
		}
		select {
		case <-done:
			t.Fatal("forward burst did not wait")
		default:
		}
		held[0].release()
		synctest.Wait()
		got := <-done
		if got.err != nil {
			t.Fatal(got.err)
		}
		if err := got.admission.forward(); err != nil {
			t.Fatal(err)
		}
		got.admission.release()
		for _, a := range held[1:] {
			a.release()
		}
	})
}

func TestCompressedForwardRetainsLargeDecoderAdmission(t *testing.T) {
	ts := newTestServer(t)
	a := ts.account(t, ts.keys)
	finished := make(chan struct{}, 3)
	handler := ts.srv.Handler()
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r); finished <- struct{}{} }))
	defer hs.Close()
	client := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, hs.URL, withToken(a.ctok), connect.WithSendGzip())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env, err := e2e.Seal(a.keys.Command, &pb.Header{Version: deviceauth.Version, Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND,
		MachineId: a.mid, RequestId: "0123456789abcdef0123456789abcdef", DeadlineMs: time.Now().Add(time.Minute).UnixMilli(), Sender: a.cid}, []byte("cmd"))
	if err != nil {
		t.Fatal(err)
	}
	request := func() error {
		_, err := client.Forward(ctx, connect.NewRequest(&pb.ForwardRequest{MachineId: a.mid, Command: env}))
		return err
	}
	done := make(chan error, maxAccountLargeRPCRequests)
	calls := make([]*pb.ForwardCall, 0, maxAccountLargeRPCRequests)
	for range maxAccountLargeRPCRequests {
		go func() { done <- request() }()
		calls = append(calls, a.machineFrame.next(t, func(f *pb.ServerFrame) bool { return f.GetForwardCall() != nil }).GetForwardCall())
	}
	if len(ts.srv.rpcRequests) != maxAccountLargeRPCRequests || len(ts.srv.forwardRPCRequests) != maxAccountLargeRPCRequests || len(ts.srv.smallRPCRequests) != 0 {
		t.Fatalf("compressed forward lost its decoder budget: large=%d forward=%d small=%d", len(ts.srv.rpcRequests), len(ts.srv.forwardRPCRequests), len(ts.srv.smallRPCRequests))
	}
	if _, err := a.client.ListMachines(ctx, connect.NewRequest(&pb.ListMachinesRequest{})); err != nil {
		t.Fatal(err)
	}
	reply := func(call *pb.ForwardCall) {
		a.machineFrame.write(t, &pb.PeerFrame{Kind: &pb.PeerFrame_ForwardReply{ForwardReply: &pb.ForwardReply{CallId: call.CallId, Error: "test reply"}}})
	}
	for _, call := range calls {
		reply(call)
	}
	for range calls {
		if err := <-done; connect.CodeOf(err) != connect.CodeAborted {
			t.Fatal(err)
		}
		<-finished
	}
	ts.srv.rpcMu.Lock()
	if len(ts.srv.accountRPCRequests) != 0 {
		ts.srv.rpcMu.Unlock()
		t.Fatal("account reservations leaked")
	}
	ts.srv.rpcMu.Unlock()
	go func() { done <- request() }()
	reply(a.machineFrame.next(t, func(f *pb.ServerFrame) bool { return f.GetForwardCall() != nil }).GetForwardCall())
	if err := <-done; connect.CodeOf(err) != connect.CodeAborted {
		t.Fatal(err)
	}
	<-finished
}

func newRPCAdmissionServer() *Server {
	return &Server{rpcRequests: make(chan struct{}, maxConcurrentRPCRequests), smallRPCRequests: make(chan struct{}, maxConcurrentSmallRPCRequests),
		forwardRPCRequests: make(chan struct{}, maxConcurrentForwardRequests), accountRPCRequests: make(map[string][3]int), accountRPCWaiters: make(map[string][3]int), accountRPCQueues: make(map[string][]*rpcWaiter)}
}

type rpcAdmissionResult struct {
	admission *rpcAdmission
	err       error
}

func TestRPCAccountBurstWaitsWithoutGlobalSlots(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pool    rpcPool
		forward bool
		limit   int
	}{
		{"small", smallRPCPool, false, maxAccountSmallRPCRequests},
		{"large", largeRPCPool, false, maxAccountLargeRPCRequests},
		{"forward", smallRPCPool, true, maxAccountForwardRequests},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := newRPCAdmissionServer()
				acquire := func(account string) (*rpcAdmission, error) {
					a, err := s.admitRPC(context.Background(), account, tc.pool, tc.forward)
					if err == nil && tc.forward {
						err = a.forward()
					}
					return a, err
				}
				held := make([]*rpcAdmission, tc.limit)
				for i := range held {
					var err error
					held[i], err = acquire("A")
					if err != nil {
						t.Fatal(err)
					}
				}
				counts := s.accountRPCRequests["A"]
				global := [3]int{len(s.rpcRequests), len(s.smallRPCRequests), len(s.forwardRPCRequests)}
				done := make(chan rpcAdmissionResult, 1)
				go func() { a, err := acquire("A"); done <- rpcAdmissionResult{a, err} }()
				synctest.Wait()
				select {
				case got := <-done:
					t.Fatalf("burst did not wait: %v", got.err)
				default:
				}
				if got := [3]int{len(s.rpcRequests), len(s.smallRPCRequests), len(s.forwardRPCRequests)}; got != global {
					t.Fatalf("waiting request held global slots: %v -> %v", global, got)
				}
				if s.accountRPCRequests["A"] != counts {
					t.Fatal("waiting request reserved account capacity")
				}
				b, err := acquire("B")
				if err != nil {
					t.Fatalf("B blocked by A's waiter: %v", err)
				}
				b.release()
				time.Sleep(rpcAdmissionWaitTimeout / 2)
				held[0].release()
				synctest.Wait()
				got := <-done
				if got.err != nil || got.admission == nil {
					t.Fatalf("burst failed after capacity returned: %v", got.err)
				}
				got.admission.release()
				for _, a := range held[1:] {
					a.release()
				}
				if len(s.accountRPCRequests) != 0 {
					t.Fatalf("slots leaked: %v", s.accountRPCRequests)
				}
			})
		})
	}
}

func TestRPCAccountWaitTimeoutAndCancellation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pool    rpcPool
		forward bool
		limit   int
	}{
		{"small", smallRPCPool, false, maxAccountSmallRPCRequests},
		{"large", largeRPCPool, false, maxAccountLargeRPCRequests},
		{"forward", smallRPCPool, true, maxAccountForwardRequests},
	} {
		for _, reason := range []string{"timeout", "cancel", "request deadline"} {
			t.Run(tc.name+"/"+reason, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					s := newRPCAdmissionServer()
					held := make([]*rpcAdmission, tc.limit)
					for i := range held {
						var err error
						held[i], err = s.admitRPC(context.Background(), "A", tc.pool, tc.forward)
						if err == nil && tc.forward {
							err = held[i].forward()
						}
						if err != nil {
							t.Fatal(err)
						}
					}
					counts := s.accountRPCRequests["A"]
					ctx, cancel := context.WithCancel(context.Background())
					if reason == "request deadline" {
						cancel()
						ctx, cancel = context.WithTimeout(context.Background(), time.Second)
					}
					defer cancel()
					done := make(chan rpcAdmissionResult, 1)
					go func() { a, err := s.admitRPC(ctx, "A", tc.pool, tc.forward); done <- rpcAdmissionResult{a, err} }()
					synctest.Wait()
					want := connect.CodeResourceExhausted
					if reason == "cancel" {
						want = connect.CodeCanceled
						cancel()
					} else if reason == "request deadline" {
						want = connect.CodeDeadlineExceeded
						time.Sleep(time.Second)
					} else {
						time.Sleep(rpcAdmissionWaitTimeout / 2)
						// An unrelated account's release must not restart the five-second budget.
						b, err := s.admitRPC(context.Background(), "B", smallRPCPool, false)
						if err != nil {
							t.Fatal(err)
						}
						b.release()
						synctest.Wait()
						time.Sleep(rpcAdmissionWaitTimeout/2 - time.Nanosecond)
						select {
						case <-done:
							t.Fatal("account wait ended before five seconds")
						default:
						}
						time.Sleep(time.Nanosecond)
					}
					synctest.Wait()
					got := <-done
					if got.admission != nil || connect.CodeOf(got.err) != want {
						t.Fatalf("wait result: %v %v, want %v", got.admission, got.err, want)
					}
					if s.accountRPCRequests["A"] != counts {
						t.Fatalf("failed wait released an owner's reservation: %v -> %v", counts, s.accountRPCRequests["A"])
					}
					for _, a := range held {
						a.release()
					}
					if len(s.accountRPCRequests) != 0 || len(s.rpcRequests) != 0 || len(s.smallRPCRequests) != 0 || len(s.forwardRPCRequests) != 0 {
						t.Fatal("reservation leaked after failed wait")
					}
					canceled, stop := context.WithCancel(context.Background())
					stop()
					if a, err := s.admitRPC(canceled, "A", tc.pool, tc.forward); a != nil || connect.CodeOf(err) != connect.CodeCanceled {
						t.Fatal("canceled request acquired available capacity")
					}
				})
			})
		}
	}
}

func TestForwardAccountWaitPrecedesLargeDecoderAdmission(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newRPCAdmissionServer()
		held := make([]*rpcAdmission, maxAccountForwardRequests)
		for i := range held {
			a, err := s.admitRPC(context.Background(), "A", smallRPCPool, true)
			if err != nil {
				t.Fatal(err)
			}
			if err := a.forward(); err != nil {
				t.Fatal(err)
			}
			held[i] = a
		}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan rpcAdmissionResult, 1)
		go func() { a, err := s.admitRPC(ctx, "A", largeRPCPool, true); done <- rpcAdmissionResult{a, err} }()
		synctest.Wait()
		if len(s.rpcRequests) != 0 || len(s.smallRPCRequests) != 0 || s.accountRPCRequests["A"] != [3]int{0, 0, maxAccountForwardRequests} {
			t.Fatal("forward waiter reserved decoder capacity before its forward share")
		}
		cancel()
		synctest.Wait()
		if got := <-done; got.admission != nil || connect.CodeOf(got.err) != connect.CodeCanceled {
			t.Fatalf("cancel: %v", got.err)
		}
		for _, a := range held {
			a.release()
		}
	})
}

func TestRPCGlobalRefusalRollsBackAccountReservations(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newRPCAdmissionServer()
		held := make([]*rpcAdmission, maxConcurrentSmallRPCRequests)
		for i := range held {
			var err error
			held[i], err = s.admitRPC(context.Background(), "", smallRPCPool, false)
			if err != nil {
				t.Fatal(err)
			}
		}
		if a, err := s.admitRPC(context.Background(), "A", smallRPCPool, true); a != nil || connect.CodeOf(err) != connect.CodeResourceExhausted {
			t.Fatalf("global rejection: %v %v", a, err)
		}
		if len(s.accountRPCRequests) != 0 || len(s.forwardRPCRequests) != 0 {
			t.Fatal("global refusal leaked reservations")
		}
		for _, a := range held {
			a.release()
		}
	})
}

func TestNormalMultiDeviceRPCBurst(t *testing.T) {
	// Workload examples, not a bound on arbitrary numbers of devices or tabs.
	for _, tc := range []struct {
		name    string
		pool    rpcPool
		forward bool
		count   int
	}{
		{"three clients and two machines reading", smallRPCPool, false, 24},
		{"two machines uploading", largeRPCPool, false, 2},
		{"three six-image drafts plus six probes and three live polls", smallRPCPool, true, 27},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s := newRPCAdmissionServer()
				start := time.Now()
				var held []*rpcAdmission
				defer func() {
					for _, a := range held {
						a.release()
					}
				}()
				for range tc.count {
					a, err := s.admitRPC(context.Background(), "A", tc.pool, tc.forward)
					if err != nil {
						t.Fatalf("ordinary burst refused: %v", err)
					}
					held = append(held, a)
					if tc.forward {
						if err := a.forward(); err != nil {
							t.Fatal(err)
						}
					}
				}
				if !time.Now().Equal(start) {
					t.Fatal("ordinary burst waited for account capacity")
				}
			})
		})
	}
}
