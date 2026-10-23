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
	// Bound parked admission requests across both account and global waiting.
	maxRPCWaiters = 256
	// Token lookup precedes account/decoder admission, including for invalid tokens.
	maxConcurrentRPCAuthLookups = 64
	rpcAuthLookupTimeout        = 5 * time.Second
	// Bound account and global admission together before any request body is decoded.
	rpcAdmissionWaitTimeout = 5 * time.Second
	rpcBodyReadTimeout      = 15 * time.Second
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
	forwardGlobal  bool
}

func (s *Server) takeAccountRPCsLocked(account string, pools ...rpcPool) bool {
	limits := rpcAccountShares()
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

var errRPCGlobalAdmission = errors.New("too many in-flight RPC requests")

func (s *Server) admitRPC(ctx context.Context, account string, pool rpcPool, forward bool) (*rpcAdmission, error) {
	deadline := time.Now().Add(rpcAdmissionWaitTimeout)
	// Unauthenticated requests keep only their existing decoder admission.
	forward = forward && account != ""
	pools := []rpcPool{pool}
	if forward {
		pools = append(pools, forwardRPCPool)
	}
	waiting := &rpcWaitReservation{account: account, pools: pools}
	defer s.releaseRPCWaiter(waiting)
	if err := s.waitAccountRPCs(ctx, waiting, deadline); err != nil {
		return nil, err
	}
	a := &rpcAdmission{s: s, pool: pool, account: account, forwardAccount: forward}
	if err := s.waitGlobalRPCs(ctx, waiting, deadline); err != nil {
		a.release()
		return nil, err
	}
	a.slots = s.globalRPCSlots(pool)
	a.forwardGlobal = forward
	return a, nil
}

func (s *Server) releaseAccountRPC(account string, pool rpcPool) {
	if account == "" {
		return
	}
	s.rpcMu.Lock()
	defer s.rpcMu.Unlock()
	s.releaseAccountRPCsLocked(account, pool)
}

func (a *rpcAdmission) release() {
	if a.slots != nil {
		pools := []rpcPool{a.pool}
		if a.forwardGlobal {
			pools = append(pools, forwardRPCPool)
		}
		a.s.releaseGlobalRPCs(pools...)
	}
	a.s.releaseAccountRPC(a.account, a.pool)
	if a.decoder != nil {
		a.decoder.release()
	}
	if a.forwardAccount {
		a.s.releaseAccountRPC(a.account, forwardRPCPool)
	}
}

var errRPCAdmission = func() *connect.Error {
	err := connect.NewError(connect.CodeResourceExhausted, errors.New("too many in-flight RPC requests"))
	err.Meta().Set("Retry-After", "1")
	return err
}()

func (a *rpcAdmission) forward() error {
	if !a.forwardGlobal {
		return errRPCAdmission
	}
	// Both global slots were reserved before reading the body. Transfer the
	// forward reservation to the response lifetime without another wait.
	a.forwardAccount, a.forwardGlobal = false, false
	if a.pool == largeRPCPool {
		// Connect retains the decoded request through the response write.
		decoder := *a
		a.decoder = &decoder
	} else {
		a.release()
	}
	a.slots, a.pool = a.s.forwardRPCRequests, forwardRPCPool
	return nil
}

// preliminaryRPCAccount bounds database waiters before the normal admission
// limits can apply. Authentication still repeats after admission/body decoding.
func (s *Server) preliminaryRPCAccount(ctx context.Context, token string) (string, error) {
	if err := rpcContextError(ctx); err != nil {
		return "", err
	}
	if token == "" {
		return "", nil
	}
	select {
	case s.rpcAuthLookups <- struct{}{}:
		defer func() { <-s.rpcAuthLookups }()
	default:
		return "", errRPCGlobalAdmission
	}
	lookupCtx, cancel := context.WithTimeout(ctx, rpcAuthLookupTimeout)
	defer cancel()
	d, err := s.authenticate(lookupCtx, token)
	if err := rpcContextError(ctx); err != nil {
		return "", err
	}
	if lookupCtx.Err() != nil {
		return "", errRPCGlobalAdmission
	}
	// Invalid tokens retain unauthenticated admission.
	if err != nil {
		return "", nil
	}
	return d.account, nil
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
		account, err := s.preliminaryRPCAccount(r.Context(), bearerToken(r.Header.Get("Authorization")))
		if err == nil {
			admission, err = s.admitRPC(r.Context(), account, pool, r.URL.Path == remoteagentv1connect.ClientServiceForwardProcedure)
		}
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
