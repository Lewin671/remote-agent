package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
)

const (
	// A legal upload is 8 MiB before decoding. Limit simultaneous decoders
	// before Connect reads a body, including on unauthenticated RPCs.
	maxConcurrentRPCRequests = 4
	// Small uncompressed bodies need less decoder memory than uploads.
	smallRPCBodyBytes             = 64 << 10
	maxConcurrentSmallRPCRequests = 32
	// Cover multi-device reads while leaving eight slots for other accounts.
	maxAccountSmallRPCRequests = 24
	// Share the four memory-heavy decoders equally between two busy accounts.
	maxAccountLargeRPCRequests = 2
	// Waiting commands need no decoder slot, but still retain bounded state.
	maxConcurrentForwardRequests = 64
	// Cover three six-image drafts plus probes and live polls, leaving half the relay pool.
	maxAccountForwardRequests = 32
	// Absorb short bursts without retaining global slots or decoded request bodies.
	accountRPCWaitTimeout = 5 * time.Second
	rpcBodyReadTimeout    = 15 * time.Second
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
	s              *Server
	slots          chan struct{}
	pool           rpcPool
	account        string
	decoder        *rpcAdmission
	forwardAccount bool
}

func (s *Server) takeAccountRPCsLocked(account string, pools ...rpcPool) bool {
	limits := [3]int{maxAccountLargeRPCRequests, maxAccountSmallRPCRequests, maxAccountForwardRequests}
	counts := s.accountRPCRequests[account]
	for _, pool := range pools {
		if counts[pool] >= limits[pool] {
			return false
		}
		counts[pool]++
	}
	s.accountRPCRequests[account] = counts
	return true
}

func rpcContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		code := connect.CodeCanceled
		if errors.Is(err, context.DeadlineExceeded) {
			code = connect.CodeDeadlineExceeded
		}
		return connect.NewError(code, err)
	}
	return nil
}

// Reserve all account shares together, before decoding or taking any global
// slot. A Forward reserves its waiting share here too, not after body decoding.
func (s *Server) waitAccountRPCs(ctx context.Context, account string, pools ...rpcPool) error {
	if account == "" {
		return rpcContextError(ctx)
	}
	deadline := time.Now().Add(accountRPCWaitTimeout)
	timer := time.NewTimer(accountRPCWaitTimeout)
	defer timer.Stop()
	for {
		s.rpcMu.Lock()
		if err := rpcContextError(ctx); err != nil {
			s.rpcMu.Unlock()
			return err
		}
		if !time.Now().Before(deadline) {
			s.rpcMu.Unlock()
			return errRPCAdmission
		}
		if s.takeAccountRPCsLocked(account, pools...) {
			s.rpcMu.Unlock()
			return nil
		}
		changed := s.rpcChanged
		s.rpcMu.Unlock()
		select {
		case <-ctx.Done():
			return rpcContextError(ctx)
		case <-timer.C:
			if err := rpcContextError(ctx); err != nil {
				return err
			}
			return errRPCAdmission
		case <-changed:
		}
	}
}

var errRPCGlobalAdmission = errors.New("too many in-flight RPC requests")

func (s *Server) admitRPC(ctx context.Context, account string, pool rpcPool, forward bool) (*rpcAdmission, error) {
	pools := []rpcPool{pool}
	if forward {
		pools = append(pools, forwardRPCPool)
	}
	if err := s.waitAccountRPCs(ctx, account, pools...); err != nil {
		return nil, err
	}
	a := &rpcAdmission{s: s, pool: pool, account: account, forwardAccount: forward}
	if err := rpcContextError(ctx); err != nil {
		a.release()
		return nil, err
	}
	slots := s.rpcRequests
	if pool == smallRPCPool {
		slots = s.smallRPCRequests
	}
	select {
	case slots <- struct{}{}:
		a.slots = slots
		return a, nil
	default:
		a.release()
		return nil, errRPCGlobalAdmission
	}
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
	close(s.rpcChanged)
	s.rpcChanged = make(chan struct{})
}

func (a *rpcAdmission) release() {
	if a.slots != nil {
		<-a.slots
	}
	a.s.releaseAccountRPC(a.account, a.pool)
	if a.decoder != nil {
		a.decoder.release()
	}
	if a.forwardAccount {
		a.s.releaseAccountRPC(a.account, forwardRPCPool)
	}
}

var errRPCAdmission = connect.NewError(connect.CodeResourceExhausted, errors.New("too many in-flight RPC requests"))

func (a *rpcAdmission) forward() error {
	select {
	case a.s.forwardRPCRequests <- struct{}{}:
		a.forwardAccount = false // Its reservation is now owned by the forward pool.
		if a.pool == largeRPCPool {
			// Connect retains the decoded request through the response write.
			// Keep its large-body memory budget, including compressed bodies.
			decoder := *a
			a.decoder = &decoder
		} else {
			a.release()
		}
		a.slots, a.pool = a.s.forwardRPCRequests, forwardRPCPool
		return nil
	default:
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
		pool := largeRPCPool
		if r.ContentLength >= 0 && r.ContentLength <= smallRPCBodyBytes && !compressedRPC(r.Header) {
			pool = smallRPCPool
			// The declared length is not trusted to bound the read.
			r.Body = http.MaxBytesReader(response.ResponseWriter, r.Body, smallRPCBodyBytes)
		}
		// Determine the account before waiting; authentication is repeated after
		// decoding to catch revocation while the request was waiting or reading.
		account := ""
		if token := bearerToken(r.Header.Get("Authorization")); token != "" {
			if d, err := s.authenticate(r.Context(), token); err == nil {
				account = d.account
			}
		}
		var err error
		admission, err = s.admitRPC(r.Context(), account, pool, r.URL.Path == remoteagentv1connect.ClientServiceForwardProcedure)
		controller := http.NewResponseController(w)
		if err != nil {
			// Refuse without draining an unfinished body. On HTTP/2 the deadline
			// belongs to this rejected stream alone.
			w.Header().Set("Connection", "close")
			_ = controller.SetReadDeadline(time.Now())
			w.Header().Set("Retry-After", "1")
			if errors.Is(err, errRPCGlobalAdmission) {
				http.Error(w, err.Error(), http.StatusServiceUnavailable)
			} else {
				_ = errorWriter.Write(w, r, err)
			}
			return
		}
		// Start the full body-read allowance only after account admission.
		if err := controller.SetReadDeadline(time.Now().Add(readTimeout)); err == nil {
			defer controller.SetReadDeadline(time.Time{})
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
