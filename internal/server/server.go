// Package server implements remote-agent-server, the relay between
// clients and daemons. It authenticates devices, stores sealed events and
// forwards sealed commands. It never holds a key that opens an envelope,
// and it never logs envelope contents.
package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	connectcors "connectrpc.com/cors"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
)

// Config configures a Server.
type Config struct {
	// DBPath is the SQLite database file.
	DBPath string
	// SetupToken creates accounts of the owner's plan.
	SetupToken string
	// Signup decides who else may create accounts: SignupToken (the
	// default) only the holder of the setup token, SignupOpen anyone, with
	// a proof of work of SignupBits bits (20 by default).
	Signup     Signup
	SignupBits uint
	// MaxAccounts bounds the accounts on the relay: 1 by default with
	// SignupToken, 100 with SignupOpen.
	MaxAccounts int
	// TrustedProxies are the reverse proxies whose X-Forwarded-For gives
	// the client's address, for the limits per address.
	TrustedProxies []netip.Prefix
	// MinFreeBytes of free disk below which accounts stop growing; 0
	// turns the check off.
	MinFreeBytes int64
	// SweepInterval is how often streams of suspended or deleted accounts
	// are ended, 30 seconds by default.
	// AllowedOrigins are the web origins, like "http://localhost:5173",
	// allowed to call the API from a browser.
	AllowedOrigins []string
	// TokenTTL is how long a login lasts.
	TokenTTL time.Duration
	// ForwardTimeout bounds how long Forward waits for a daemon.
	ForwardTimeout time.Duration
	// PingInterval is how often stream connections are checked.
	PingInterval time.Duration
	// FCMServiceAccount is the path of a Firebase service account key file
	// for Android pushes; empty turns FCM off.
	FCMServiceAccount string
	// ImageRetention is how long image display copies are kept, 30 days
	// by default; ImageQuota bounds the bytes of stored images, 1 GiB by
	// default.
	ImageRetention time.Duration
	ImageQuota     int64
	SweepInterval  time.Duration
}

// Limits on what peers may send.
const (
	maxEnvelopeBytes = 1 << 20
	// Session plaintext is a 32-byte key or at most 64 KiB of metadata.
	// 4 KiB covers both headers, signature, nonce, AEAD tag and framing.
	maxSessionKeyEnvelopeBytes  = 4 << 10
	maxSessionMetaEnvelopeBytes = 68 << 10
	maxSessionResponseBytes     = 32 << 20
	maxUploadEvents             = 500
	maxRequestBytes             = 8 << 20
	maxAuthRequestBytes         = 16 << 10
	maxPairingRequestBytes      = 2 << 20
	maxFrameBytes               = 2 << 20
	defaultEventPage            = 200
	// Ended sessions per page of the listing.
	defaultSessionPage = 50
	maxSessionPage     = 200
	maxEventPage       = 1000
	// Bound memory as well as event count; legal envelopes can be 1 MiB.
	// A page is also what a client waits for before it shows anything
	// more, so it is kept to a few seconds of a slow link.
	maxEventPageBytes = 1 << 20
	// sendQueue is how many frames may wait for a slow stream peer before
	// it is disconnected; it catches up from history after reconnecting.
	// sendQueueBytes bounds their size as well: a frame of events can be
	// near 1 MiB. Shared broadcasts count against every recipient until
	// written, bounding distinct backlogs conservatively as well.
	sendQueue             = 256
	sendQueueBytes        = 16 << 20
	accountSendQueueBytes = 32 << 20
	relaySendQueueBytes   = 96 << 20
)

// Server is the relay.
type Server struct {
	cfg      Config
	store    *store
	hub      *hub
	term     *termHub
	push     *pusher
	pairing  *pairingService
	recovery *recoveryService

	stop       chan struct{}
	stopOnce   sync.Once
	ctx        context.Context
	cancel     context.CancelFunc
	activityMu sync.Mutex
	closing    bool
	activities sync.WaitGroup
	closeErr   error

	streamMu            sync.Mutex
	streams             map[string]map[*streamAuth]struct{}
	accountStreams      map[string]int
	pendingStreamHellos chan struct{}
	rpcRequests         chan struct{}
	smallRPCRequests    chan struct{}
	forwardRPCRequests  chan struct{}
	rpcMu               sync.Mutex
	accountRPCRequests  map[string][3]int

	challengeMu sync.Mutex
	challenges  map[string]challenge

	signup  *signupGuard
	diskLow atomic.Bool
}

type challenge struct {
	deviceID, account string
	expires           time.Time
}

// New opens the database and returns a server.
func New(cfg Config) (*Server, error) {
	if cfg.TokenTTL == 0 {
		cfg.TokenTTL = 30 * 24 * time.Hour
	}
	if cfg.ForwardTimeout == 0 {
		cfg.ForwardTimeout = 30 * time.Second
	}
	if cfg.PingInterval == 0 {
		cfg.PingInterval = 20 * time.Second
	}
	if cfg.ImageRetention == 0 {
		cfg.ImageRetention = defaultImageRetention
	}
	if cfg.ImageQuota == 0 {
		cfg.ImageQuota = defaultImageQuota
	}
	switch cfg.Signup {
	case "":
		cfg.Signup = SignupToken
	case SignupToken, SignupOpen:
	default:
		return nil, fmt.Errorf("unknown signup mode %q", cfg.Signup)
	}
	if cfg.MaxAccounts == 0 {
		cfg.MaxAccounts = 1
		if cfg.Signup == SignupOpen {
			cfg.MaxAccounts = 100
		}
	}
	if cfg.SignupBits == 0 {
		cfg.SignupBits = defaultSignupBits
	}
	if cfg.SweepInterval == 0 {
		cfg.SweepInterval = 30 * time.Second
	}
	st, err := openStore(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Server{ctx: ctx, cancel: cancel, cfg: cfg, store: st, signup: newSignupGuard(), challenges: map[string]challenge{}, streams: map[string]map[*streamAuth]struct{}{}, accountStreams: map[string]int{}, pendingStreamHellos: make(chan struct{}, maxPendingStreamHellos), rpcRequests: make(chan struct{}, maxConcurrentRPCRequests), smallRPCRequests: make(chan struct{}, maxConcurrentSmallRPCRequests), stop: make(chan struct{})}
	s.hub = newHub(s)
	s.forwardRPCRequests = make(chan struct{}, maxConcurrentForwardRequests)
	s.accountRPCRequests = make(map[string][3]int)
	s.term = newTermHub(s)
	s.pairing = newPairingService(s)
	s.recovery = newRecoveryService(s)
	if s.push, err = newPusher(s); err != nil {
		cancel()
		st.Close()
		return nil, err
	}
	if err := s.seedSecurityChains(context.Background()); err != nil {
		cancel()
		st.Close()
		return nil, err
	}
	s.startWorker(func() { s.expireLoop(s.stop) })
	s.startWorker(func() { s.watchDisk(s.stop) })
	s.startWorker(func() { s.sweepAccounts(s.stop) })
	return s, nil
}

// Close cancels streams, requests and background work, waits for their
// completion, then closes storage. Concurrent callers share the result.
func (s *Server) Close() error {
	s.stopOnce.Do(func() {
		s.activityMu.Lock()
		s.closing = true
		s.cancel()
		close(s.stop)
		s.activityMu.Unlock()
		s.streamMu.Lock()
		for _, streams := range s.streams {
			for a := range streams {
				a.cancel()
			}
		}
		s.streamMu.Unlock()
		s.activities.Wait()
		s.closeErr = s.store.Close()
	})
	return s.closeErr
}

func (s *Server) beginActivity() bool {
	s.activityMu.Lock()
	defer s.activityMu.Unlock()
	if s.closing {
		return false
	}
	s.activities.Add(1)
	return true
}

func (s *Server) startWorker(fn func()) bool {
	if !s.beginActivity() {
		return false
	}
	go func() { defer s.activities.Done(); fn() }()
	return true
}

func (s *Server) trackRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.beginActivity() {
			http.Error(w, "server is shutting down", http.StatusServiceUnavailable)
			return
		}
		defer s.activities.Done()
		ctx, cancel := context.WithCancel(r.Context())
		stop := context.AfterFunc(s.ctx, cancel)
		defer stop()
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Handler returns the HTTP handler serving the API and the stream.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	opts := []connect.HandlerOption{connect.WithReadMaxBytes(maxRequestBytes)}
	authed := append(opts, connect.WithInterceptors(s.authInterceptor()))
	mux.Handle(remoteagentv1connect.NewAuthServiceHandler(&authService{s: s}, connect.WithReadMaxBytes(maxAuthRequestBytes)))
	// Pairing authenticates per call: the joiner has no login yet.
	mux.Handle(remoteagentv1connect.NewPairingServiceHandler(s.pairing, connect.WithReadMaxBytes(maxPairingRequestBytes)))
	mux.Handle(remoteagentv1connect.NewRecoveryServiceHandler(s.recovery, connect.WithReadMaxBytes(maxRequestBytes)))
	mux.Handle(remoteagentv1connect.NewClientServiceHandler(&clientService{s: s}, authed...))
	mux.Handle(remoteagentv1connect.NewMachineServiceHandler(&machineService{s: s}, authed...))
	mux.HandleFunc("/v1/stream", s.hub.serve)
	mux.HandleFunc("/v1/term", s.term.serve)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok\n")) })
	return s.trackRequests(s.cors(s.limitRPCRequests(mux, rpcBodyReadTimeout)))
}

// cors lets the allowed web origins call the API.
func (s *Server) cors(next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range s.cfg.AllowedOrigins {
		allowed[strings.TrimRight(o, "/")] = true
	}
	methods := strings.Join(connectcors.AllowedMethods(), ", ")
	headers := strings.Join(append(connectcors.AllowedHeaders(), "Authorization"), ", ")
	exposed := strings.Join(connectcors.ExposedHeaders(), ", ")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && allowed[origin] {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Expose-Headers", exposed)
			if r.Method == http.MethodOptions {
				h.Set("Access-Control-Allow-Methods", methods)
				h.Set("Access-Control-Allow-Headers", headers)
				h.Set("Access-Control-Max-Age", "7200")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// originPatterns are the allowed origins as host patterns for the
// WebSocket origin check.
func (s *Server) originPatterns() []string {
	var out []string
	for _, o := range s.cfg.AllowedOrigins {
		if u, err := url.Parse(o); err == nil && u.Host != "" {
			out = append(out, u.Host)
		}
	}
	return out
}

type ctxKey struct{}

// deviceFrom returns the authenticated device of a request.
func deviceFrom(ctx context.Context) *device {
	d, _ := ctx.Value(ctxKey{}).(*device)
	return d
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func newToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

// authenticate resolves a bearer token to its device.
func (s *Server) authenticate(ctx context.Context, token string) (*device, error) {
	if token == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("missing token"))
	}
	d, err := s.store.tokenDevice(ctx, hashToken(token))
	if errors.Is(err, errNoRows) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid or expired token"))
	}
	if errors.Is(err, errSuspended) {
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	}
	if err != nil {
		return nil, internal(err)
	}
	return d, nil
}

func (s *Server) authInterceptor() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			token, _ := strings.CutPrefix(req.Header().Get("Authorization"), "Bearer ")
			d, err := s.authenticate(ctx, token)
			if err != nil {
				return nil, err
			}
			if err := s.authorizeDevice(ctx, d, req.Spec().Procedure); err != nil {
				return nil, err
			}
			if req.Spec().Procedure == remoteagentv1connect.ClientServiceForwardProcedure {
				if admission, ok := ctx.Value(rpcAdmissionKey{}).(*rpcAdmission); ok {
					if err := admission.forward(); err != nil {
						return nil, err
					}
				}
			}
			return next(context.WithValue(ctx, ctxKey{}, d), req)
		}
	}
}

// internal logs err and hides it from the caller. Errors never carry
// content, only IDs, so logging them is safe.
func internal(err error) error {
	slog.Error("internal error", "err", err)
	return connect.NewError(connect.CodeInternal, errors.New("internal error"))
}

// Login challenges can be requested without logging in, so they are
// bounded: only registered devices get stored challenges, each at most
// maxDeviceChallenges and each account maxAccountChallenges, the oldest
// giving way, so that no account can take the others' logins. The relay
// holds the share of every account it may have, up to maxChallenges.
const (
	maxDeviceChallenges  = 8
	maxAccountChallenges = 32
	maxChallenges        = 1 << 16
)

func (s *Server) challengeRoom() int {
	return min(max(s.cfg.MaxAccounts, 1)*maxAccountChallenges, maxChallenges)
}

var errTooManyChallenges = errors.New("too many pending logins; try again in a minute")

// newChallenge issues a one-time login challenge for a registered device.
func (s *Server) newChallenge(deviceID, account string) ([]byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	s.challengeMu.Lock()
	defer s.challengeMu.Unlock()
	now := time.Now()
	var mine, ours []string
	for k, c := range s.challenges {
		switch {
		case now.After(c.expires):
			delete(s.challenges, k)
		case c.deviceID == deviceID:
			mine = append(mine, k)
			ours = append(ours, k)
		case c.account == account:
			ours = append(ours, k)
		}
	}
	oldest := func(keys []string) string {
		o := keys[0]
		for _, k := range keys[1:] {
			if s.challenges[k].expires.Before(s.challenges[o].expires) {
				o = k
			}
		}
		return o
	}
	switch {
	case len(mine) >= maxDeviceChallenges:
		delete(s.challenges, oldest(mine))
	case len(ours) >= maxAccountChallenges:
		delete(s.challenges, oldest(ours))
	}
	if len(s.challenges) >= s.challengeRoom() {
		return nil, errTooManyChallenges
	}
	s.challenges[string(b)] = challenge{deviceID: deviceID, account: account, expires: now.Add(time.Minute)}
	return b, nil
}

func (s *Server) pendingChallenges() int {
	s.challengeMu.Lock()
	defer s.challengeMu.Unlock()
	return len(s.challenges)
}

// takeChallenge consumes a challenge issued to deviceID.
func (s *Server) takeChallenge(deviceID string, b []byte) bool {
	s.challengeMu.Lock()
	defer s.challengeMu.Unlock()
	c, ok := s.challenges[string(b)]
	if !ok {
		return false
	}
	delete(s.challenges, string(b))
	return subtle.ConstantTimeCompare([]byte(c.deviceID), []byte(deviceID)) == 1 && time.Now().Before(c.expires)
}
