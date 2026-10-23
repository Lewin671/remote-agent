package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
			entered := make(chan struct{}, tc.limit+1)
			release := make(chan struct{})
			defer func() { close(release) }()
			h := ts.srv.limitRPCRequests(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Reaching here means admission happened before any body read.
				entered <- struct{}{}
				<-release
			}), rpcBodyReadTimeout)
			done := make(chan struct{}, tc.limit+1)
			request := func(token string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodPost, remoteagentv1connect.ClientServiceGetEventsProcedure, strings.NewReader("x"))
				r.ContentLength = int64(tc.length)
				r.Header.Set("Content-Type", "application/proto")
				r.Header.Set("Authorization", "Bearer "+token)
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			for i := 0; i < tc.limit; i++ {
				// Machine and client tokens share one account's allowance.
				token := a.ctok
				if i%2 == 1 {
					token = a.mtok
				}
				go func() { request(token); done <- struct{}{} }()
				<-entered
			}
			// Refuse A immediately, but let B enter while A holds its share.
			rejected := make(chan *httptest.ResponseRecorder, 1)
			go func() { rejected <- request(a.ctok) }()
			var w *httptest.ResponseRecorder
			select {
			case w = <-rejected:
			case <-entered:
				t.Fatal("account A exceeded its share")
			}
			if w.Code != http.StatusTooManyRequests || !strings.Contains(w.Body.String(), "resource_exhausted") {
				t.Fatalf("account A excess: %d %s", w.Code, w.Body.String())
			}
			go func() { request(b.ctok); done <- struct{}{} }()
			<-entered
			close(release)
			for range tc.limit + 1 {
				<-done
			}
			release = make(chan struct{})
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
	if err := forward(a); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("excess forward: %v", err)
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
	ts := newTestServer(t)
	var held []*rpcAdmission
	defer func() {
		for _, a := range held {
			a.release()
		}
	}()
	for i := 0; i < maxConcurrentForwardRequests; i++ {
		account := fmt.Sprint(i / maxAccountForwardRequests)
		ts.srv.smallRPCRequests <- struct{}{}
		if !ts.srv.takeAccountRPC(account, smallRPCPool) {
			t.Fatal("decoder admission refused")
		}
		a := &rpcAdmission{s: ts.srv, slots: ts.srv.smallRPCRequests, pool: smallRPCPool, account: account}
		held = append(held, a)
		if err := a.forward(); err != nil {
			t.Fatal(err)
		}
	}
	ts.srv.smallRPCRequests <- struct{}{}
	account := "another-account"
	ts.srv.takeAccountRPC(account, smallRPCPool)
	extra := &rpcAdmission{s: ts.srv, slots: ts.srv.smallRPCRequests, pool: smallRPCPool, account: account}
	defer extra.release()
	if err := extra.forward(); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("global forward limit: %v", err)
	}
	held[0].release()
	held = held[1:]
	if err := extra.forward(); err != nil {
		t.Fatalf("released slot was not reusable: %v", err)
	}
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
	done := make(chan error, 1)
	go func() { done <- request() }()
	call := a.machineFrame.next(t, func(f *pb.ServerFrame) bool { return f.GetForwardCall() != nil }).GetForwardCall()
	if len(ts.srv.rpcRequests) != 1 || len(ts.srv.forwardRPCRequests) != 1 || len(ts.srv.smallRPCRequests) != 0 {
		t.Fatalf("compressed forward lost its decoder budget: large=%d forward=%d small=%d", len(ts.srv.rpcRequests), len(ts.srv.forwardRPCRequests), len(ts.srv.smallRPCRequests))
	}
	if err := request(); connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("second large request from account A: %v", err)
	}
	<-finished
	if _, err := a.client.ListMachines(ctx, connect.NewRequest(&pb.ListMachinesRequest{})); err != nil {
		t.Fatalf("large forward blocked small RPC: %v", err)
	}
	a.machineFrame.write(t, &pb.PeerFrame{Kind: &pb.PeerFrame_ForwardReply{ForwardReply: &pb.ForwardReply{CallId: call.CallId, Error: "test reply"}}})
	if err := <-done; connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("forward result: %v", err)
	}
	<-finished
	// A subsequent compressed call must reach the daemon, proving that both
	// its account and global decoder reservations were released.
	go func() { done <- request() }()
	call = a.machineFrame.next(t, func(f *pb.ServerFrame) bool { return f.GetForwardCall() != nil }).GetForwardCall()
	a.machineFrame.write(t, &pb.PeerFrame{Kind: &pb.PeerFrame_ForwardReply{ForwardReply: &pb.ForwardReply{CallId: call.CallId, Error: "test reply"}}})
	if err := <-done; connect.CodeOf(err) != connect.CodeAborted {
		t.Fatalf("reused forward slot: %v", err)
	}
	<-finished
}
