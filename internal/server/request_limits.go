package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
)

const (
	// A legal upload is 8 MiB before decoding. Limit simultaneous decoders
	// before Connect reads a body, including on unauthenticated RPCs.
	maxConcurrentRPCRequests = 4
	// Small uncompressed bodies need less decoder memory than uploads.
	smallRPCBodyBytes             = 64 << 10
	maxConcurrentSmallRPCRequests = 32
	// Leave at least three quarters of the small pool for other accounts.
	maxAccountSmallRPCRequests = 8
	// A single account may use only one of the four large decoders.
	maxAccountLargeRPCRequests = 1
	// Waiting commands need no decoder slot, but still retain bounded state.
	maxConcurrentForwardRequests = 64
	// Leave forwarding capacity for other accounts while a daemon is slow.
	maxAccountForwardRequests = 8
	rpcBodyReadTimeout        = 15 * time.Second
)

type rpcPool int

const (
	largeRPCPool rpcPool = iota
	smallRPCPool
	forwardRPCPool
)

type rpcAdmissionKey struct{}

// The admission follows the HTTP response lifetime, including Connect's
// serialization and writes after its unary interceptor returns.
type rpcAdmission struct {
	s       *Server
	slots   chan struct{}
	pool    rpcPool
	account string
}

func (s *Server) takeAccountRPC(account string, pool rpcPool) bool {
	if account == "" {
		return true
	}
	limits := [3]int{maxAccountLargeRPCRequests, maxAccountSmallRPCRequests, maxAccountForwardRequests}
	s.rpcMu.Lock()
	defer s.rpcMu.Unlock()
	counts := s.accountRPCRequests[account]
	if counts[pool] >= limits[pool] {
		return false
	}
	counts[pool]++
	s.accountRPCRequests[account] = counts
	return true
}

func (s *Server) releaseAccountRPC(account string, pool rpcPool) {
	if account == "" {
		return
	}
	s.rpcMu.Lock()
	defer s.rpcMu.Unlock()
	counts := s.accountRPCRequests[account]
	counts[pool]--
	if counts == [3]int{} {
		delete(s.accountRPCRequests, account)
	} else {
		s.accountRPCRequests[account] = counts
	}
}

func (a *rpcAdmission) release() {
	<-a.slots
	a.s.releaseAccountRPC(a.account, a.pool)
}

var errRPCAdmission = connect.NewError(connect.CodeResourceExhausted, errors.New("too many in-flight RPC requests"))

func (a *rpcAdmission) forward() error {
	if !a.s.takeAccountRPC(a.account, forwardRPCPool) {
		return errRPCAdmission
	}
	select {
	case a.s.forwardRPCRequests <- struct{}{}:
		a.release()
		a.slots, a.pool = a.s.forwardRPCRequests, forwardRPCPool
		return nil
	default:
		a.s.releaseAccountRPC(a.account, forwardRPCPool)
		return errRPCAdmission
	}
}

func (s *Server) limitRPCRequests(next http.Handler, readTimeout time.Duration) http.Handler {
	return s.limitRPCRequestsWithWriteTimeouts(next, readTimeout, rpcResponseWriteTimeout, largeRPCResponseWriteTimeout)
}

func (s *Server) limitRPCRequestsWithWriteTimeouts(next http.Handler, readTimeout, writeTimeout, largeWriteTimeout time.Duration) http.Handler {
	errorWriter := connect.NewErrorWriter()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/remoteagent.v1.") {
			next.ServeHTTP(w, r)
			return
		}
		response := &rpcResponseWriter{ResponseWriter: w, timeout: rpcWriteTimeout(r.URL.Path, writeTimeout, largeWriteTimeout)}
		w = response
		var admission *rpcAdmission
		defer func() {
			// Flush net/http's response buffer while admission and the write
			// deadline still apply, including for a small Connect response.
			_ = response.FlushError()
			if admission != nil {
				admission.release()
			}
		}()
		slots := s.rpcRequests
		pool := largeRPCPool
		if r.ContentLength >= 0 && r.ContentLength <= smallRPCBodyBytes && !compressedRPC(r.Header) {
			slots = s.smallRPCRequests
			pool = smallRPCPool
			// The declared length is not trusted to bound the read.
			r.Body = http.MaxBytesReader(response.ResponseWriter, r.Body, smallRPCBodyBytes)
		}
		select {
		case slots <- struct{}{}:
		default:
			// Refuse without draining an attacker-controlled unfinished body.
			// On HTTP/2 this deadline belongs to the rejected stream alone.
			w.Header().Set("Connection", "close")
			_ = http.NewResponseController(w).SetReadDeadline(time.Now())
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many in-flight RPC requests", http.StatusServiceUnavailable)
			return
		}
		admission = &rpcAdmission{s: s, slots: slots, pool: pool}
		// A context deadline alone cannot unblock net/http's request body read.
		// Apply a transport read deadline, then clear it for keep-alive reuse.
		controller := http.NewResponseController(w)
		if err := controller.SetReadDeadline(time.Now().Add(readTimeout)); err == nil {
			defer controller.SetReadDeadline(time.Time{})
		}
		// Resolve valid bearer tokens before reading bodies so one account
		// cannot fill the decoders with unfinished requests. Services still
		// authenticate after decoding to catch revocation during a slow read.
		if token := bearerToken(r.Header.Get("Authorization")); token != "" {
			if d, err := s.authenticate(r.Context(), token); err == nil {
				if !s.takeAccountRPC(d.account, pool) {
					w.Header().Set("Connection", "close")
					_ = controller.SetReadDeadline(time.Now())
					_ = errorWriter.Write(w, r, errRPCAdmission)
					return
				}
				admission.account = d.account
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), rpcAdmissionKey{}, admission)))
	})
}

// compressedRPC reports whether a request says its messages are compressed,
// in any of the protocols the Connect handlers speak.
func compressedRPC(h http.Header) bool {
	for _, k := range []string{"Content-Encoding", "Connect-Content-Encoding", "Grpc-Encoding"} {
		if v := h.Get(k); v != "" && v != "identity" {
			return true
		}
	}
	return false
}
