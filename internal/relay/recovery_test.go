package relay_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

func TestRecoveryNetworkChangeRetiresOnlyTheObsoleteSecurityRound(t *testing.T) {
	var armed, held atomic.Bool
	entered, canceled := make(chan struct{}), make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if armed.Load() && strings.HasSuffix(r.URL.Path, "/GetSecurityStates") && held.CompareAndSwap(false, true) {
				_, _ = io.Copy(io.Discard, r.Body)
				r.Body.Close()
				close(entered)
				select {
				case <-r.Context().Done():
					close(canceled)
				case <-release:
				}
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	e.backend.add(t, "security-recovery")
	changes := make(chan uint64, 1)
	ready := make(chan struct{}, 4)
	armed.Store(true)
	e.startRelayBackend(e.backend, func(r *relay.Relay) {
		relay.SetNetworkChangesForTest(r, changes)
		relay.ObserveRecoveryForTest(r, func(event string) {
			if event == "ready" {
				ready <- struct{}{}
			}
		})
	})
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("security round did not start")
	}
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("initial path did not authenticate")
	}
	changes <- 1
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("old security HTTP still blocks the new network")
	}
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("new authenticated path waited for the obsolete round")
	}
	s := e.session(t, "security-recovery", 0)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := e.client.Do(ctx, s, &pb.Command{Kind: &pb.Command_RenameSession{RenameSession: &pb.RenameSessionRequest{SessionId: s.ID, Title: "recovered"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestRecoveryBlackholeBecomesUsableWithoutSocketClose(t *testing.T) {
	var attempts atomic.Int32
	firstReady := make(chan time.Time, 1)
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/stream" || attempts.Add(1) > 1 {
				next.ServeHTTP(w, r)
				return
			}
			ws, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer ws.CloseNow()
			if _, _, err = ws.Read(r.Context()); err != nil {
				return
			}
			ready, _ := proto.Marshal(&pb.ServerFrame{Kind: &pb.ServerFrame_Ready{Ready: &pb.Ready{}}})
			if ws.Write(r.Context(), websocket.MessageBinary, ready) != nil {
				return
			}
			// Establish a 1s heartbeat cadence before the leg goes silent.
			hb, _ := proto.Marshal(&pb.ServerFrame{Kind: &pb.ServerFrame_Heartbeat{Heartbeat: &pb.Heartbeat{}}})
			for i := 0; i < 2; i++ {
				if ws.Write(r.Context(), websocket.MessageBinary, hb) != nil {
					return
				}
				if i == 0 {
					time.Sleep(1050 * time.Millisecond)
				}
			}
			firstReady <- time.Now()
			// A silent application path; neither EOF nor a reset wakes the reader.
			_, _, _ = ws.Read(r.Context())
		})
	})
	e.backend.add(t, "recovery")
	e.startRelay()
	started := <-firstReady
	e.online(t, true)
	s := e.session(t, "recovery", 0)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := e.client.Do(ctx, s, &pb.Command{Kind: &pb.Command_RenameSession{RenameSession: &pb.RenameSessionRequest{SessionId: s.ID, Title: "recovered"}}}); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 6*time.Second {
		t.Fatalf("silent path recovered too late: %v", elapsed)
	}
	if attempts.Load() != 2 {
		t.Fatalf("unexpected probes: %d", attempts.Load())
	}
}

func TestRecoveryNetworkHintKeepsHealthyTerminalAndExecution(t *testing.T) {
	var attempts atomic.Int32
	replacement := make(chan struct{}, 1)
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/stream" && attempts.Add(1) == 2 {
				replacement <- struct{}{}
			}
			next.ServeHTTP(w, r)
		})
	})
	e.backend.add(t, "recovery")
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	free := func() { releaseOnce.Do(func() { close(release) }) }
	defer free()
	e.backend.sendResult = func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	changes := make(chan uint64, 1)
	running := e.startRelayBackend(e.backend, func(r *relay.Relay) { relay.SetNetworkChangesForTest(r, changes) })
	e.online(t, true)
	s := e.session(t, "recovery", 0)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	term, err := e.client.OpenTerminal(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close()
	if _, err = term.Read(ctx); err != nil {
		t.Fatal(err)
	}
	e.backend.mu.Lock()
	native := e.backend.term
	e.backend.mu.Unlock()
	envelope, err := e.client.Seal(s.MachineID, s.ID, remoteclient.NewRequestID(), time.Now().Add(time.Minute), &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: s.ID, Text: "once", RequestId: "recovery-message"}}})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { _, err := e.client.Forward(ctx, s.MachineID, envelope); finished <- err }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("handler did not start")
	}
	changes <- 1
	select {
	case <-replacement:
	case <-ctx.Done():
		t.Fatal("network hint did not wake a new path")
	}
	waitFor(t, "authenticated replacement", func() bool { return relay.ReadyCountForTest(running) >= 2 })
	// The original handler is deliberately still blocked. A fresh harmless
	// operation proves the replacement is authenticated and executing calls.
	_, err = e.client.Do(ctx, s, &pb.Command{Kind: &pb.Command_RenameSession{RenameSession: &pb.RenameSessionRequest{SessionId: s.ID, Title: "new path"}}})
	if err != nil {
		t.Fatal(err)
	}
	if e.backend.sendCount() != 1 {
		t.Fatal("admitted operation repeated")
	}
	native.out <- []byte("healthy")
	f, err := term.Read(ctx)
	if err != nil || string(f.GetOutput()) != "healthy" {
		t.Fatalf("healthy terminal lost: %v", err)
	}
	if err = term.Send(ctx, &pb.TerminalFrame{Kind: &pb.TerminalFrame_Input{Input: []byte("once")}}); err != nil {
		t.Fatal(err)
	}
	select {
	case b := <-native.input:
		if string(b) != "once" {
			t.Fatal("input changed")
		}
	case <-ctx.Done():
		t.Fatal("input lost")
	}
	free()
	select {
	case <-finished:
	case <-ctx.Done():
		t.Fatal("original waiter remained stuck")
	}
	if _, err = e.client.Forward(ctx, s.MachineID, envelope, remoteclient.MayHaveBeenDispatched); err != nil {
		t.Fatal(err)
	}
	if e.backend.sendCount() != 1 {
		t.Fatal("original request was not deduplicated")
	}

}

func TestRecoveryDoesNotSuspectBeforeTwoHeartbeats(t *testing.T) {
	var attempts atomic.Int32
	firstReady := make(chan struct{}, 1)
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/stream" || attempts.Add(1) > 1 {
				next.ServeHTTP(w, r)
				return
			}
			ws, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			defer ws.CloseNow()
			if _, _, err = ws.Read(r.Context()); err != nil {
				return
			}
			ready, _ := proto.Marshal(&pb.ServerFrame{Kind: &pb.ServerFrame_Ready{Ready: &pb.Ready{}}})
			if ws.Write(r.Context(), websocket.MessageBinary, ready) != nil {
				return
			}
			hb, _ := proto.Marshal(&pb.ServerFrame{Kind: &pb.ServerFrame_Heartbeat{Heartbeat: &pb.Heartbeat{}}})
			if ws.Write(r.Context(), websocket.MessageBinary, hb) != nil {
				return
			}
			close(firstReady)
			// Protocol 16 may send directed peer probes while the server stays
			// silent. Drain them without ending this deliberately quiet leg.
			for {
				if _, _, err := ws.Read(r.Context()); err != nil {
					return
				}
			}
		})
	})
	e.backend.add(t, "no-suspect")
	e.startRelay()
	<-firstReady
	// With only 1 heartbeat observed, silence threshold must remain at 60s rather than 3s.
	time.Sleep(3500 * time.Millisecond)
	if got := attempts.Load(); got != 1 {
		t.Fatalf("suspected leg before 2 heartbeats: attempts=%d", got)
	}
}

func TestRecoveryPreservesReadyCandidateOnHintUnlessRouteGone(t *testing.T) {
	for _, tc := range []struct {
		name           string
		routeAvailable bool
		wantAttempts   int32
	}{
		{name: "valid_route_kept", routeAvailable: true, wantAttempts: 2},
		{name: "gone_route_discarded", routeAvailable: false, wantAttempts: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts atomic.Int32
			e := setupWithHandler(t, func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/v1/stream" {
						attempts.Add(1)
					}
					next.ServeHTTP(w, r)
				})
			})
			e.backend.add(t, "candidate")
			changes := make(chan uint64, 4)
			ready := make(chan struct{}, 8)
			hintSeen := make(chan struct{}, 8)
			var injected atomic.Bool
			e.startRelayBackend(e.backend, func(r *relay.Relay) {
				relay.SetNetworkChangesForTest(r, changes)
				relay.SetRouteAvailableForTest(r, func(net.IP) bool { return tc.routeAvailable })
				relay.ObserveRecoveryForTest(r, func(ev string) {
					if ev == "ready" {
						ready <- struct{}{}
					} else if ev == "hint" {
						hintSeen <- struct{}{}
					}
				})
				relay.SetBeforeDialResultForTest(r, func(gen uint64) {
					if gen == 1 && injected.CompareAndSwap(false, true) {
						changes <- 2
						select {
						case <-hintSeen:
						case <-time.After(2 * time.Second):
						}
					}
				})
			})
			select {
			case <-ready:
			case <-time.After(3 * time.Second):
				t.Fatal("initial ready timeout")
			}
			changes <- 1
			select {
			case <-ready:
			case <-time.After(3 * time.Second):
				t.Fatal("recovery ready timeout")
			}
			time.Sleep(200 * time.Millisecond)
			if got := attempts.Load(); got != tc.wantAttempts {
				t.Fatalf("%s: /v1/stream attempts=%d, want %d", tc.name, got, tc.wantAttempts)
			}
		})
	}
}

func TestRecoveryDialBudgetFirstLoginVsCachedNetworkChange(t *testing.T) {
	var relayStarted, firstReady atomic.Bool
	var streamAttempts atomic.Int32
	stream2Canceled := make(chan time.Duration, 1)
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if relayStarted.Load() && !firstReady.Load() && strings.HasSuffix(r.URL.Path, "/Login") {
				// Longer than 3s, shorter than 5.5s readyTimeout: succeeds only while initial dial keeps >3s budget.
				select {
				case <-time.After(3200 * time.Millisecond):
				case <-r.Context().Done():
					return
				}
			}
			if relayStarted.Load() && r.URL.Path == "/v1/stream" {
				n := streamAttempts.Add(1)
				if n == 2 {
					// Hang the second dial; with a valid cached token, recovery dial context must cancel at 3s (<< 5.5s readyTimeout).
					started := time.Now()
					<-r.Context().Done()
					stream2Canceled <- time.Since(started)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	e.backend.add(t, "budget")
	changes := make(chan uint64, 1)
	ready := make(chan time.Time, 4)
	dials := make(chan time.Time, 8)
	relayStarted.Store(true)
	e.startRelayBackend(e.backend, func(r *relay.Relay) {
		relay.SetReadyTimeoutForTest(r, 5500*time.Millisecond)
		relay.SetNetworkChangesForTest(r, changes)
		relay.ObserveRecoveryForTest(r, func(ev string) {
			if ev == "ready" {
				ready <- time.Now()
			} else if ev == "dial" {
				dials <- time.Now()
			}
		})
	})
	<-dials // initial dial
	select {
	case <-ready:
	case <-time.After(5200 * time.Millisecond):
		t.Fatal("first dial did not keep full readyTimeout (>3s)")
	}
	firstReady.Store(true)
	if streamAttempts.Load() != 1 || len(dials) != 0 {
		t.Fatalf("first dial retried unexpectedly: streamAttempts=%d, extraDials=%d", streamAttempts.Load(), len(dials))
	}

	changes <- 1
	select {
	case elapsed := <-stream2Canceled:
		if elapsed > 3800*time.Millisecond {
			t.Fatalf("cached network recovery dial did not narrow to 3s budget: elapsed=%v", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cached network recovery dial did not time out within 5s")
	}
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not become ready after narrowed dial timeout")
	}
}

func TestRecoverySecurityFailureBlocksWakeNotNetworkChange(t *testing.T) {
	var failSecurity atomic.Bool
	var secCalls atomic.Int32
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/GetSecurityStates") {
				secCalls.Add(1)
				if failSecurity.Load() {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	})
	e.backend.add(t, "security-gate")
	changes := make(chan uint64, 2)
	wakes := make(chan struct{}, 2)
	ready := make(chan struct{}, 8)
	e.startRelayBackend(e.backend, func(r *relay.Relay) {
		relay.SetSecurityIntervalForTest(r, 40*time.Millisecond)
		relay.SetNetworkChangesForTest(r, changes)
		relay.SetWakeEventsForTest(r, wakes)
		relay.ObserveRecoveryForTest(r, func(ev string) {
			if ev == "ready" {
				ready <- struct{}{}
			}
		})
	})
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("initial ready timeout")
	}
	failSecurity.Store(true)
	beforeCalls := secCalls.Load()
	changes <- 1
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("pure network change must not be blocked by GetSecurityStates failure")
	}
	waitFor(t, "background security sync poll", func() bool { return secCalls.Load() > beforeCalls })

	wakes <- struct{}{}
	select {
	case <-ready:
		t.Fatal("wake must remain blocked while GetSecurityStates fails")
	case <-time.After(350 * time.Millisecond):
	}
	failSecurity.Store(false)
	select {
	case <-ready:
	case <-time.After(3 * time.Second):
		t.Fatal("wake did not recover once GetSecurityStates succeeded")
	}
}
