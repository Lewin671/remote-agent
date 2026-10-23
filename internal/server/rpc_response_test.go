package server

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
)

type rpcDeadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (w *rpcDeadlineRecorder) SetWriteDeadline(d time.Time) error {
	w.deadlines = append(w.deadlines, d)
	return nil
}

func TestRPCWriteDeadlinePolicyAndLifetime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		for _, tc := range []struct {
			path    string
			timeout time.Duration
		}{
			{remoteagentv1connect.ClientServiceForwardProcedure, 30 * time.Second},
			{remoteagentv1connect.ClientServiceListMachinesProcedure, 30 * time.Second},
			{remoteagentv1connect.ClientServiceGetEventsProcedure, 60 * time.Second},
			{remoteagentv1connect.ClientServiceGetImageProcedure, 60 * time.Second},
			{remoteagentv1connect.ClientServiceListRemoteSessionsProcedure, 60 * time.Second},
			{"/v1/stream", 0}, {"/v1/term", 0}, {"/healthz", 0},
		} {
			s := &Server{rpcRequests: make(chan struct{}, maxConcurrentRPCRequests), smallRPCRequests: make(chan struct{}, maxConcurrentSmallRPCRequests)}
			w := &rpcDeadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
			h := s.limitRPCRequests(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
				if len(w.deadlines) != 0 {
					t.Fatal("write deadline started before handler responded")
				}
				// Even a full Forward wait must leave the complete write allowance.
				time.Sleep(30 * time.Second)
				start := time.Now()
				out.WriteHeader(http.StatusOK)
				out.Write([]byte("response"))
				time.Sleep(time.Second)
				http.NewResponseController(out).Flush()
				if tc.timeout != 0 && (len(w.deadlines) != 1 || !w.deadlines[0].Equal(start.Add(tc.timeout))) {
					t.Fatalf("%s: deadline must start at first write and not reset: %v", tc.path, w.deadlines)
				}
			}), rpcBodyReadTimeout)
			h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, tc.path, nil))
			if tc.timeout == 0 && len(w.deadlines) != 0 {
				t.Fatalf("%s acquired an RPC deadline", tc.path)
			}
		}
	})
}

type rpcPipeListener struct {
	conns  chan net.Conn
	closed chan struct{}
	once   sync.Once
	addr   net.Addr
}

func (l *rpcPipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}
func (l *rpcPipeListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *rpcPipeListener) Addr() net.Addr { return l.addr }

type rpcStalledConn struct {
	net.Conn
	started     chan struct{}
	allow       chan struct{}
	writeErrors chan error
	once        sync.Once
}

func (c *rpcStalledConn) Write(p []byte) (int, error) {
	c.once.Do(func() { close(c.started); <-c.allow })
	n, err := c.Conn.Write(p)
	select {
	case c.writeErrors <- err:
	default:
	}
	return n, err
}

func TestStalledRPCReaderReleasesAdmission(t *testing.T) {
	ts := newTestServer(t)
	a := ts.account(t, ts.keys)
	// net.Pipe has no kernel send buffer: withholding all response reads
	// deterministically stalls even a tiny response buffered by net/http.
	serverConn, clientConn := net.Pipe()
	stalled := &rpcStalledConn{Conn: serverConn, started: make(chan struct{}), allow: make(chan struct{}), writeErrors: make(chan error, 1)}
	var allowOnce sync.Once
	allowWrite := func() { allowOnce.Do(func() { close(stalled.allow) }) }
	defer allowWrite()
	l := &rpcPipeListener{conns: make(chan net.Conn, 1), closed: make(chan struct{}), addr: serverConn.LocalAddr()}
	l.conns <- stalled
	called := make(chan struct{})
	h := connect.NewUnaryHandler(remoteagentv1connect.ClientServiceListMachinesProcedure,
		func(ctx context.Context, r *connect.Request[pb.ListMachinesRequest]) (*connect.Response[pb.ListMachinesResponse], error) {
			close(called)
			return connect.NewResponse(&pb.ListMachinesResponse{}), nil
		})
	limited := ts.srv.limitRPCRequestsWithWriteTimeouts(h, rpcBodyReadTimeout, 100*time.Millisecond, 200*time.Millisecond)
	finished := make(chan struct{})
	hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { limited.ServeHTTP(w, r); close(finished) })}
	go hs.Serve(l)
	t.Cleanup(func() { clientConn.Close(); hs.Close() })
	_, err := fmt.Fprintf(clientConn, "POST %s HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/proto\r\nAuthorization: Bearer %s\r\nContent-Length: 0\r\n\r\n", remoteagentv1connect.ClientServiceListMachinesProcedure, a.ctok)
	if err != nil {
		t.Fatal(err)
	}
	<-called
	<-stalled.started
	if len(ts.srv.smallRPCRequests) != 1 {
		t.Fatal("RPC admission released before the buffered response write")
	}
	allowWrite()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("stalled response did not release its slot after the write deadline")
	}
	if err := <-stalled.writeErrors; err == nil {
		t.Fatal("stalled write succeeded without a reader")
	}
	if len(ts.srv.smallRPCRequests) != 0 {
		t.Fatal("small RPC slot leaked")
	}
	ts.srv.rpcMu.Lock()
	defer ts.srv.rpcMu.Unlock()
	if len(ts.srv.accountRPCRequests) != 0 {
		t.Fatalf("account RPC slot leaked: %v", ts.srv.accountRPCRequests)
	}
}

func TestHTTP2StalledRPCDeadlineIsStreamScoped(t *testing.T) {
	ts := newTestServer(t)
	finished := make(chan struct{}, 1)
	h := connect.NewUnaryHandler(remoteagentv1connect.ClientServiceGetImageProcedure,
		func(ctx context.Context, r *connect.Request[pb.GetImageRequest]) (*connect.Response[pb.GetImageResponse], error) {
			// Larger than the explicitly small HTTP/2 receive window; compression
			// is not requested, so the transport must wait for the reader.
			return connect.NewResponse(&pb.GetImageResponse{Image: &pb.Envelope{Ciphertext: make([]byte, 256<<10)}}), nil
		})
	limited := ts.srv.limitRPCRequestsWithWriteTimeouts(h, rpcBodyReadTimeout, 100*time.Millisecond, 200*time.Millisecond)
	mux := http.NewServeMux()
	mux.HandleFunc(remoteagentv1connect.ClientServiceGetImageProcedure, func(w http.ResponseWriter, r *http.Request) { limited.ServeHTTP(w, r); finished <- struct{}{} })
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	hs := httptest.NewUnstartedServer(mux)
	hs.EnableHTTP2 = true
	hs.StartTLS()
	defer hs.Close()
	tr := &http.Transport{
		TLSClientConfig:    hs.Client().Transport.(*http.Transport).TLSClientConfig.Clone(),
		ForceAttemptHTTP2:  true,
		DisableCompression: true,
		HTTP2:              &http.HTTP2Config{MaxReceiveBufferPerStream: 64 << 10},
		MaxConnsPerHost:    1,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	req, _ := http.NewRequest(http.MethodPost, hs.URL+remoteagentv1connect.ClientServiceGetImageProcedure, strings.NewReader(""))
	req.Header.Set("Content-Type", "application/proto")
	req.Header.Set("Accept-Encoding", "identity")
	connections := make(chan net.Conn, 2)
	trace := &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { connections <- info.Conn }}
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), trace))
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	firstConn := <-connections
	if res.ProtoMajor != 2 || res.StatusCode != http.StatusOK {
		t.Fatalf("not a successful HTTP/2 response: %s %d", res.Proto, res.StatusCode)
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP/2 stalled response retained its slot")
	}
	if len(ts.srv.smallRPCRequests) != 0 {
		t.Fatal("HTTP/2 decoder slot leaked")
	}
	// Reuse the one HTTP/2 connection while the unread response remains open.
	healthReq, _ := http.NewRequest(http.MethodGet, hs.URL+"/healthz", nil)
	healthReq = healthReq.WithContext(httptrace.WithClientTrace(healthReq.Context(), trace))
	health, err := client.Do(healthReq)
	if err != nil {
		t.Fatalf("deadline closed unrelated HTTP/2 streams: %v", err)
	}
	defer health.Body.Close()
	if nextConn := <-connections; nextConn != firstConn {
		t.Fatal("stream deadline closed the HTTP/2 connection")
	}
	if _, err := io.ReadAll(health.Body); err != nil || health.StatusCode != http.StatusOK {
		t.Fatalf("health after stream timeout: %d %v", health.StatusCode, err)
	}
	if _, err := io.ReadAll(res.Body); err == nil {
		t.Fatal("stalled response completed instead of timing out")
	}
}
