package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"math/bits"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// Signup on a relay open to anyone. A new account costs a proof of work,
// which keeps scripts from creating accounts in bulk, and each address
// may create only a few in a while, which keeps one person from taking
// the relay's room. The setup token still creates accounts of the owner's
// plan, without either.
const (
	signupChallengeTTL     = 5 * time.Minute
	maxSignupChallenges    = 1024
	signupChallengesPerMin = 10 // per address
	signupsPerHour         = 3  // per address
	signupsPerHourTotal    = 30
	maxSignupNonceBytes    = 64
	defaultSignupBits      = 20
)

type signupGuard struct {
	mu         sync.Mutex
	challenges map[string]time.Time // challenge -> expiry
	asked      map[string][]time.Time
	created    map[string][]time.Time
	total      []time.Time
}

func newSignupGuard() *signupGuard {
	return &signupGuard{challenges: map[string]time.Time{}, asked: map[string][]time.Time{}, created: map[string][]time.Time{}}
}

// recent keeps the times within window and reports whether fewer than n remain.
func recent(ts []time.Time, now time.Time, window time.Duration, n int) ([]time.Time, bool) {
	kept := ts[:0]
	for _, t := range ts {
		if now.Sub(t) < window {
			kept = append(kept, t)
		}
	}
	return kept, len(kept) < n
}

func (g *signupGuard) sweepLocked(now time.Time) {
	for c, exp := range g.challenges {
		if now.After(exp) {
			delete(g.challenges, c)
		}
	}
	for k, ts := range g.asked {
		if g.asked[k], _ = recent(ts, now, time.Minute, 0); len(g.asked[k]) == 0 {
			delete(g.asked, k)
		}
	}
	for k, ts := range g.created {
		if g.created[k], _ = recent(ts, now, time.Hour, 0); len(g.created[k]) == 0 {
			delete(g.created, k)
		}
	}
}

func (g *signupGuard) challenge(addr string) ([]byte, time.Time, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	g.sweepLocked(now)
	var ok bool
	if g.asked[addr], ok = recent(g.asked[addr], now, time.Minute, signupChallengesPerMin); !ok {
		return nil, time.Time{}, errFull("too many signup attempts from this address; try again in a minute")
	}
	if len(g.challenges) >= maxSignupChallenges {
		return nil, time.Time{}, errFull("too many signups in progress; try again in a minute")
	}
	c := make([]byte, 32)
	if _, err := rand.Read(c); err != nil {
		panic(err)
	}
	exp := now.Add(signupChallengeTTL)
	g.challenges[string(c)] = exp
	g.asked[addr] = append(g.asked[addr], now)
	return c, exp, nil
}

// admit consumes a solved challenge and counts a new account for addr. It
// refuses an address or a relay that created too many accounts lately.
func (g *signupGuard) admit(addr string, p *pb.SignupProof, bits uint) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	g.sweepLocked(now)
	var ok bool
	if g.created[addr], ok = recent(g.created[addr], now, time.Hour, signupsPerHour); !ok {
		return errFull("too many new accounts from this address; try again later")
	}
	if g.total, ok = recent(g.total, now, time.Hour, signupsPerHourTotal); !ok {
		return errFull("this relay takes no more new accounts for now; try again later")
	}
	exp, found := g.challenges[string(p.GetChallenge())]
	if !found || now.After(exp) || len(p.GetNonce()) > maxSignupNonceBytes || !solved(p.GetChallenge(), p.GetNonce(), bits) {
		return connect.NewError(connect.CodePermissionDenied, errors.New("the proof of work is wrong or expired; try again"))
	}
	delete(g.challenges, string(p.GetChallenge()))
	g.created[addr] = append(g.created[addr], now)
	g.total = append(g.total, now)
	return nil
}

// solved reports whether SHA-256(challenge || nonce) starts with n zero bits.
func solved(challenge, nonce []byte, n uint) bool {
	sum := sha256.Sum256(append(append([]byte{}, challenge...), nonce...))
	zero := uint(0)
	for _, b := range sum {
		if b != 0 {
			zero += uint(bits.LeadingZeros8(b))
			break
		}
		zero += 8
	}
	return zero >= n
}

// newAccountFor decides whether a registration may create its account,
// and with which plan.
func (s *Server) newAccountFor(req *connect.Request[pb.RegisterDeviceRequest]) (*newAccount, error) {
	tok := s.cfg.SetupToken
	if tok != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(req.Msg.SetupToken)) == 1 {
		return &newAccount{plan: "owner"}, nil
	}
	if s.cfg.Signup != SignupOpen {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("no account yet; a valid setup token is required"))
	}
	if req.Msg.SignupProof == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			errors.New("this relay creates accounts with a proof of work; update remote-agent to create one here"))
	}
	if s.diskLow.Load() {
		return nil, errFull("this relay is out of storage; try again later")
	}
	if err := s.signup.admit(s.clientAddr(req.Peer().Addr, req.Header()), req.Msg.SignupProof, s.cfg.SignupBits); err != nil {
		return nil, err
	}
	return &newAccount{plan: "trial"}, nil
}

func (a *authService) GetSignupChallenge(ctx context.Context, req *connect.Request[pb.GetSignupChallengeRequest]) (*connect.Response[pb.GetSignupChallengeResponse], error) {
	if a.s.cfg.Signup != SignupOpen {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this relay creates accounts with a setup token"))
	}
	c, exp, err := a.s.signup.challenge(a.s.clientAddr(req.Peer().Addr, req.Header()))
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.GetSignupChallengeResponse{Challenge: c, DifficultyBits: uint32(a.s.cfg.SignupBits), ExpiresMs: exp.UnixMilli()}), nil
}

// clientAddr is who a request comes from, for the limits per address:
// the peer, or behind a trusted proxy, the last address the proxy added
// to X-Forwarded-For. IPv6 addresses count by their /64.
func (s *Server) clientAddr(peer string, h http.Header) string {
	addr := parseAddr(peer)
	if addr.IsValid() && s.trusted(addr) {
		hops := strings.Split(strings.Join(h.Values("X-Forwarded-For"), ","), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
			if err != nil {
				break
			}
			addr = a.Unmap()
			if !s.trusted(addr) {
				break
			}
		}
	}
	if !addr.IsValid() {
		return "unknown"
	}
	if addr.Is6() {
		p, _ := addr.Prefix(64)
		return p.String()
	}
	return addr.String()
}

func parseAddr(hostport string) netip.Addr {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap()
}

func (s *Server) trusted(a netip.Addr) bool {
	for _, p := range s.cfg.TrustedProxies {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
