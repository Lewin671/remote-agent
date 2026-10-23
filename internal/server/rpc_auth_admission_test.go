package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
)

// Acquire every real read connection before entering a synctest bubble. Only
// database/sql's context-aware pool wait runs under the fake clock.
func stalledRPCAuthServer(t *testing.T) *Server {
	t.Helper()
	st, _, _ := poolTestStore(t)
	for range 4 {
		c, err := st.reader.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.Close() })
	}
	s := newRPCAdmissionServer()
	s.store = st
	return s
}

type unreadRPCAuthBody struct{ t *testing.T }

func (b unreadRPCAuthBody) Read([]byte) (int, error) {
	b.t.Error("read body before authentication admission completed")
	return 0, io.EOF
}

func rpcAuthRequest(t *testing.T, ctx context.Context, h http.Handler, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, remoteagentv1connect.ClientServiceGetEventsProcedure, unreadRPCAuthBody{t})
	req.ContentLength = 1
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

func assertRPCAuthOverload(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "1" || rec.Header().Get("Connection") != "close" {
		t.Fatalf("authentication overload: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
	}
}

func assertRPCAuthSlotsReleased(t *testing.T, s *Server) {
	t.Helper()
	s.rpcMu.Lock()
	defer s.rpcMu.Unlock()
	if len(s.rpcAuthLookups) != 0 || len(s.rpcRequests) != 0 || len(s.smallRPCRequests) != 0 || len(s.forwardRPCRequests) != 0 || s.rpcWaiterCount != 0 || len(s.accountRPCRequests) != 0 {
		t.Fatal("authentication refusal leaked slots or entered normal admission")
	}
}

func TestRPCPreAuthLookupLimit(t *testing.T) {
	s := stalledRPCAuthServer(t)
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer func() { cancel(); synctest.Wait() }()
		h := s.limitRPCRequests(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("stalled lookup reached RPC handler") }), rpcBodyReadTimeout)
		start := time.Now()
		done := make(chan *httptest.ResponseRecorder, maxConcurrentRPCAuthLookups)
		before := s.store.reader.Stats().WaitCount
		for i := 0; i < maxConcurrentRPCAuthLookups; i++ {
			token := "invalid-token"
			if i%2 == 0 {
				token = "test-token"
			}
			go func() { done <- rpcAuthRequest(t, ctx, h, token) }()
		}
		synctest.Wait()
		if len(s.rpcAuthLookups) != 64 || s.store.reader.Stats().WaitCount-before != 64 {
			t.Fatalf("lookups=%d, DB waiters=%d; want 64", len(s.rpcAuthLookups), s.store.reader.Stats().WaitCount-before)
		}
		overflow := make(chan *httptest.ResponseRecorder, 1)
		go func() { overflow <- rpcAuthRequest(t, ctx, h, "another-invalid-token") }()
		synctest.Wait()
		select {
		case rec := <-overflow:
			assertRPCAuthOverload(t, rec)
		default:
			t.Fatal("lookup beyond cap waited instead of being refused immediately")
		}
		if !time.Now().Equal(start) || s.store.reader.Stats().WaitCount-before != 64 {
			t.Fatal("overflow waited or reached the database pool")
		}
		cancel()
		synctest.Wait()
		for range maxConcurrentRPCAuthLookups {
			if rec := <-done; !strings.Contains(rec.Body.String(), "canceled") {
				t.Fatalf("canceled lookup: %d %s", rec.Code, rec.Body.String())
			}
		}
		assertRPCAuthSlotsReleased(t, s)
	})
}

func TestRPCPreAuthLookupDeadline(t *testing.T) {
	for _, token := range []string{"test-token", "invalid-token"} {
		t.Run(token, func(t *testing.T) {
			s := stalledRPCAuthServer(t)
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer func() { cancel(); synctest.Wait() }()
				h := s.limitRPCRequests(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("timed-out lookup reached RPC handler") }), rpcBodyReadTimeout)
				start := time.Now()
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- rpcAuthRequest(t, ctx, h, token) }()
				synctest.Wait()
				time.Sleep(5*time.Second - time.Nanosecond)
				synctest.Wait()
				select {
				case rec := <-done:
					t.Fatalf("lookup returned before deadline: %d", rec.Code)
				default:
				}
				time.Sleep(time.Nanosecond)
				synctest.Wait()
				select {
				case rec := <-done:
					assertRPCAuthOverload(t, rec)
				default:
					t.Fatal("stalled lookup did not honor the 5 second deadline")
				}
				if time.Since(start) != 5*time.Second || ctx.Err() != nil {
					t.Fatal("lookup timeout changed the request's own context")
				}
				assertRPCAuthSlotsReleased(t, s)
			})
		})
	}
}

func TestRPCPreAuthLookupRequestCancellation(t *testing.T) {
	s := stalledRPCAuthServer(t)
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer func() { cancel(); synctest.Wait() }()
		h := s.limitRPCRequests(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("canceled lookup reached RPC handler") }), rpcBodyReadTimeout)
		start := time.Now()
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- rpcAuthRequest(t, ctx, h, "test-token") }()
		synctest.Wait()
		cancel()
		synctest.Wait()
		select {
		case rec := <-done:
			if !strings.Contains(rec.Body.String(), "canceled") {
				t.Fatalf("request cancellation: %d %s", rec.Code, rec.Body.String())
			}
		default:
			t.Fatal("lookup ignored request cancellation")
		}
		if !time.Now().Equal(start) {
			t.Fatal("lookup waited after request cancellation")
		}
		assertRPCAuthSlotsReleased(t, s)
	})
}

func TestRPCPreAuthLookupValidAndInvalidTokens(t *testing.T) {
	ts := newTestServer(t)
	a := ts.account(t, ts.keys)
	if cap(ts.srv.rpcAuthLookups) != 64 {
		t.Fatalf("production auth lookup cap = %d", cap(ts.srv.rpcAuthLookups))
	}
	result, err := a.client.ListMachines(context.Background(), connect.NewRequest(&pb.ListMachinesRequest{}))
	if err != nil || len(result.Msg.Machines) != 1 || result.Msg.Machines[0].Id != a.mid {
		t.Fatalf("valid request: %v, %v", result, err)
	}
	invalid := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, ts.url, withToken("invalid-token"))
	if _, err := invalid.ListMachines(context.Background(), connect.NewRequest(&pb.ListMachinesRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("invalid token: %v", err)
	}
	// Requests with no token and health checks must not need a lookup slot.
	for range cap(ts.srv.rpcAuthLookups) {
		ts.srv.rpcAuthLookups <- struct{}{}
	}
	defer func() {
		for range cap(ts.srv.rpcAuthLookups) {
			<-ts.srv.rpcAuthLookups
		}
	}()
	for _, path := range []string{"/healthz", remoteagentv1connect.AuthServiceGetChallengeProcedure} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(""))
		req.Header.Set("Content-Type", "application/proto")
		ts.srv.Handler().ServeHTTP(rec, req)
		if rec.Code == http.StatusServiceUnavailable {
			t.Fatalf("%s incorrectly needed an auth lookup slot", path)
		}
	}
}
