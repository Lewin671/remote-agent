package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
)

func slowRPCBody(t *testing.T, base string) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(conn, "POST /remoteagent.v1.AuthService/Login HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/proto\r\nContent-Length: 1024\r\n\r\nx")
	if err != nil {
		conn.Close()
		t.Fatal(err)
	}
	return conn
}

func TestRPCDecoderConcurrencyIsBoundedBeforeBodyRead(t *testing.T) {
	ts := newTestServer(t)
	var conns []net.Conn
	defer func() {
		for _, conn := range conns {
			conn.Close()
		}
	}()
	for range maxConcurrentRPCRequests {
		conns = append(conns, slowRPCBody(t, ts.url))
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(ts.srv.rpcRequests) != maxConcurrentRPCRequests {
		if time.Now().After(deadline) {
			t.Fatal("slow request bodies did not enter the decoder")
		}
		time.Sleep(time.Millisecond)
	}
	req, _ := http.NewRequest(http.MethodPost, ts.url+"/remoteagent.v1.AuthService/Login", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/proto")
	res, err := (&http.Client{Timeout: time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("decoder admitted another request: %d", res.StatusCode)
	}
	// An excess request that withholds its body must be refused promptly,
	// without net/http draining that body after the handler returns.
	rejected := slowRPCBody(t, ts.url)
	defer rejected.Close()
	rejected.SetReadDeadline(time.Now().Add(time.Second))
	raw := make([]byte, 1024)
	n, err := rejected.Read(raw)
	if err != nil || !strings.Contains(string(raw[:n]), "503") {
		t.Fatalf("rejection waited for a slow body: %q %v", raw[:n], err)
	}
	// Health and WebSockets have independent budgets.
	res, err = http.Get(ts.url + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatal("slow RPC bodies blocked health")
	}
	for _, conn := range conns {
		conn.Close()
	}
	for len(ts.srv.rpcRequests) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("RPC slots leaked after client disconnect")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestSlowRPCBodyDeadlineReleasesDecoderSlot(t *testing.T) {
	ts := newTestServer(t)
	// Exercise the same middleware using a short deadline rather than making
	// the regression wait for the production 15-second deadline.
	_, handler := remoteagentv1connect.NewAuthServiceHandler(&authService{s: ts.srv}, connect.WithReadMaxBytes(maxAuthRequestBytes))
	hs := httptest.NewServer(ts.srv.limitRPCRequests(handler, 100*time.Millisecond))
	defer hs.Close()
	conn := slowRPCBody(t, hs.URL)
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 1024)
	if _, err := conn.Read(b); err != nil {
		t.Fatalf("server did not time out the slow body: %v", err)
	}
	deadline := time.Now().Add(time.Second)
	for len(ts.srv.rpcRequests) != 0 {
		if time.Now().After(deadline) {
			t.Fatal("timed-out decoder slot leaked")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAuthMessagesHaveSmallerDecodeLimit(t *testing.T) {
	ts := newTestServer(t)
	_, err := ts.auth.GetChallenge(context.Background(), connect.NewRequest(&pb.GetChallengeRequest{DeviceId: strings.Repeat("x", 32<<10)}))
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("oversized auth body reached service: %v", err)
	}
}
