package server

import (
	"net/http"
	"strings"
	"time"
)

const (
	// A legal upload is 8 MiB before decoding. Limit simultaneous decoders
	// before Connect reads a body, including on unauthenticated RPCs.
	maxConcurrentRPCRequests = 4
	// Requests that declare a small body cost little to decode, and most
	// of them (a command forwarded to a machine, a page of history going
	// out over a slow link) spend their time waiting, not decoding. They
	// have their own, larger budget, so that a few of them in flight do
	// not turn the rest away.
	smallRPCBodyBytes             = 64 << 10
	maxConcurrentSmallRPCRequests = 32
	rpcBodyReadTimeout            = 15 * time.Second
)

func (s *Server) limitRPCRequests(next http.Handler, readTimeout time.Duration) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/remoteagent.v1.") {
			next.ServeHTTP(w, r)
			return
		}
		slots := s.rpcRequests
		if r.ContentLength >= 0 && r.ContentLength <= smallRPCBodyBytes {
			slots = s.smallRPCRequests
			// The declared length is not trusted to bound the read.
			r.Body = http.MaxBytesReader(w, r.Body, smallRPCBodyBytes)
		}
		select {
		case slots <- struct{}{}:
			defer func() { <-slots }()
		default:
			// Refuse without draining an attacker-controlled unfinished body.
			// On HTTP/2 this deadline belongs to the rejected stream alone.
			w.Header().Set("Connection", "close")
			_ = http.NewResponseController(w).SetReadDeadline(time.Now())
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many in-flight RPC requests", http.StatusServiceUnavailable)
			return
		}
		// A context deadline alone cannot unblock net/http's request body read.
		// Apply a transport read deadline, then clear it for keep-alive reuse.
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(time.Now().Add(readTimeout)); err == nil {
			defer controller.SetReadDeadline(time.Time{})
		}
		next.ServeHTTP(w, r)
	})
}
