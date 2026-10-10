package server

import (
	"bytes"
	"compress/gzip"
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
	"google.golang.org/protobuf/proto"
)

// slowRPCBody starts a request that declares a large body and withholds it.
func slowRPCBody(t *testing.T, base string) net.Conn {
	t.Helper()
	return slowBody(t, base, 1<<20)
}

func slowBody(t *testing.T, base string, length int) net.Conn {
	t.Helper()
	conn, err := net.Dial("tcp", strings.TrimPrefix(base, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(conn, "POST /remoteagent.v1.AuthService/Login HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/proto\r\nContent-Length: %d\r\n\r\nx", length)
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
	// Small requests have their own budget: large bodies in flight do not
	// turn them away.
	if res.StatusCode == http.StatusServiceUnavailable {
		t.Fatal("large bodies in flight refused a small request")
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

func TestSmallRPCRequestsAreBoundedToo(t *testing.T) {
	ts := newTestServer(t)
	var conns []net.Conn
	defer func() {
		for _, conn := range conns {
			conn.Close()
		}
	}()
	for range maxConcurrentSmallRPCRequests {
		conns = append(conns, slowBody(t, ts.url, 1024))
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(ts.srv.smallRPCRequests) != maxConcurrentSmallRPCRequests {
		if time.Now().After(deadline) {
			t.Fatal("small request bodies did not enter the decoder")
		}
		time.Sleep(time.Millisecond)
	}
	if len(ts.srv.rpcRequests) != 0 {
		t.Fatal("small requests took slots meant for large ones")
	}
	rejected := slowBody(t, ts.url, 1024)
	defer rejected.Close()
	rejected.SetReadDeadline(time.Now().Add(time.Second))
	raw := make([]byte, 1024)
	n, err := rejected.Read(raw)
	if err != nil || !strings.Contains(string(raw[:n]), "503") {
		t.Fatalf("an excess small request was not refused: %q %v", raw[:n], err)
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

// A body that inflates to megabytes declares a few kilobytes. It must not
// be counted as a small request, of which many more decode at once.
func TestCompressedRPCBodyIsNotASmallRequest(t *testing.T) {
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
	msg, _ := proto.Marshal(&pb.GetEventsRequest{SessionId: strings.Repeat("a", 7<<20)})
	var body bytes.Buffer
	zw := gzip.NewWriter(&body)
	zw.Write(msg)
	zw.Close()
	if body.Len() > smallRPCBodyBytes {
		t.Fatalf("the compressed body is %d bytes, not a small one", body.Len())
	}
	for _, header := range []string{"Content-Encoding", "Connect-Content-Encoding", "Grpc-Encoding"} {
		req, _ := http.NewRequest(http.MethodPost, ts.url+"/remoteagent.v1.ClientService/GetEvents", bytes.NewReader(body.Bytes()))
		req.Header.Set("Content-Type", "application/proto")
		req.Header.Set(header, "gzip")
		res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("%s: a compressed body was admitted as a small request: %d", header, res.StatusCode)
		}
	}
	if len(ts.srv.smallRPCRequests) != 0 {
		t.Fatal("a compressed body took a small request's slot")
	}
}
