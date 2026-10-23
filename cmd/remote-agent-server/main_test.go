package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"testing/synctest"
)

// pipeListener keeps HTTP I/O inside the synctest bubble, so waiting for
// Shutdown and an active request is deterministic without polling timers.
type pipeListener struct {
	conn     net.Conn
	accepted bool
	closed   chan struct{}
	once     sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.conn, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}
func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *pipeListener) Addr() net.Addr { return &net.TCPAddr{} }

func TestServeHTTPWaitsForShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		defer func() {
			select {
			case <-release:
			default:
				close(release)
			}
		}()
		hs := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-release
			w.Write([]byte("finished"))
		})}
		serverConn, clientConn := net.Pipe()
		defer clientConn.Close()
		ln := &pipeListener{conn: serverConn, closed: make(chan struct{})}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- serveHTTP(ctx, hs, ln) }()
		response := make(chan error, 1)
		go func() {
			req, _ := http.NewRequest("GET", "http://example.com/", nil)
			err := req.Write(clientConn)
			if err == nil {
				var res *http.Response
				res, err = http.ReadResponse(bufio.NewReader(clientConn), req)
				if err == nil {
					_, err = io.ReadAll(res.Body)
					res.Body.Close()
				}
			}
			response <- err
		}()
		<-entered
		cancel()
		synctest.Wait()
		returned := false
		select {
		case err := <-done:
			returned = true
			t.Errorf("returned before active request completed: %v", err)
		default:
		}
		close(release)
		if err := <-response; err != nil {
			t.Fatal(err)
		}
		if !returned {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		}
	})
}
