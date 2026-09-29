package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/e2e"
)

// Pairing state lives in memory for a few minutes. The server only passes
// public keys, a commitment and a sealed payload along; the security of
// pairing rests on the commitment and the emoji the user compares, not on
// the server behaving (see internal/e2e/pair.go). The limits here keep an
// honest server from being used to guess codes.
const (
	pairingTTL      = 5 * time.Minute
	pairingAttempts = 3
	// pairingDeliveredTTL keeps a delivered pairing for the initiator to
	// see that it went through.
	pairingDeliveredTTL = time.Minute
	maxPairings     = 64
	// badCodesPerMinute bounds wrong codes across all callers.
	badCodesPerMinute = 30
)

type pairing struct {
	id         string
	code       string
	initiator  string // device ID
	commit     []byte
	expires    time.Time
	attempts   int
	candidate  []byte // hash of the joiner's token
	joinerPub  []byte
	joinerKind pb.DeviceKind
	initPub    []byte
	payload    *pb.Envelope
	canceled   bool
	delivered  bool
}

func (p *pairing) state() pb.PairingState {
	switch {
	case p.canceled:
		return pb.PairingState_PAIRING_STATE_CANCELED
	case p.delivered:
		return pb.PairingState_PAIRING_STATE_DELIVERED
	case p.payload != nil:
		return pb.PairingState_PAIRING_STATE_APPROVED
	case p.initPub != nil:
		return pb.PairingState_PAIRING_STATE_REVEALED
	case p.joinerPub != nil:
		return pb.PairingState_PAIRING_STATE_JOINED
	}
	return pb.PairingState_PAIRING_STATE_OPEN
}

type pairingService struct {
	s *Server

	mu       sync.Mutex
	byID     map[string]*pairing
	byCode   map[string]*pairing
	badCodes []time.Time
}

func newPairingService(s *Server) *pairingService {
	return &pairingService{s: s, byID: map[string]*pairing{}, byCode: map[string]*pairing{}}
}

func (ps *pairingService) sweepLocked() {
	now := time.Now()
	for id, p := range ps.byID {
		if now.After(p.expires) {
			delete(ps.byID, id)
			delete(ps.byCode, p.code)
		}
	}
}

func randomCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(100_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%08d", n), nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

var errNoPairing = connect.NewError(connect.CodeNotFound, errors.New("no such pairing, or it expired"))

func (ps *pairingService) CreatePairing(ctx context.Context, req *connect.Request[pb.CreatePairingRequest]) (*connect.Response[pb.CreatePairingResponse], error) {
	d, err := ps.s.authenticate(ctx, bearerToken(req.Header().Get("Authorization")))
	if err != nil {
		return nil, err
	}
	if len(req.Msg.Commit) != 32 {
		return nil, invalid("the commitment must be 32 bytes")
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.sweepLocked()
	if len(ps.byID) >= maxPairings {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many pairings in progress"))
	}
	var code string
	for {
		if code, err = randomCode(); err != nil {
			return nil, internal(err)
		}
		if ps.byCode[code] == nil {
			break
		}
	}
	p := &pairing{id: randomHex(16), code: code, initiator: d.id, commit: req.Msg.Commit, expires: time.Now().Add(pairingTTL)}
	ps.byID[p.id], ps.byCode[code] = p, p
	return connect.NewResponse(&pb.CreatePairingResponse{PairingId: p.id, Code: code, ExpiresMs: p.expires.UnixMilli()}), nil
}

func (ps *pairingService) JoinPairing(ctx context.Context, req *connect.Request[pb.JoinPairingRequest]) (*connect.Response[pb.JoinPairingResponse], error) {
	if len(req.Msg.JoinerPublic) != 32 {
		return nil, invalid("the public key must be 32 bytes")
	}
	if req.Msg.JoinerKind != pb.DeviceKind_DEVICE_KIND_MACHINE && req.Msg.JoinerKind != pb.DeviceKind_DEVICE_KIND_CLIENT {
		return nil, invalid("unknown device kind")
	}
	code := strings.ReplaceAll(strings.TrimSpace(req.Msg.Code), " ", "")
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.sweepLocked()
	p := ps.byCode[code]
	if p == nil {
		// Wrong codes are limited across all callers, so guessing an
		// open one within its five minutes is hopeless.
		now := time.Now()
		kept := ps.badCodes[:0]
		for _, t := range ps.badCodes {
			if now.Sub(t) < time.Minute {
				kept = append(kept, t)
			}
		}
		ps.badCodes = kept
		if len(ps.badCodes) >= badCodesPerMinute {
			return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many wrong codes; try again in a minute"))
		}
		ps.badCodes = append(ps.badCodes, now)
		return nil, errNoPairing
	}
	if p.canceled || p.joinerPub != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("another device is already joining with this code"))
	}
	p.attempts++
	if p.attempts > pairingAttempts {
		ps.dropLocked(p)
		return nil, errNoPairing
	}
	token := randomHex(32)
	p.candidate = hashToken(token)
	p.joinerPub, p.joinerKind = bytes.Clone(req.Msg.JoinerPublic), req.Msg.JoinerKind
	return connect.NewResponse(&pb.JoinPairingResponse{PairingId: p.id, Commit: p.commit, CandidateToken: token}), nil
}

func (ps *pairingService) dropLocked(p *pairing) {
	delete(ps.byID, p.id)
	delete(ps.byCode, p.code)
}

// find returns the pairing if the caller is its initiator (by login) or
// its joiner (by candidate token).
func (ps *pairingService) find(ctx context.Context, header, id, candidate string) (*pairing, bool, error) {
	ps.mu.Lock()
	ps.sweepLocked()
	p := ps.byID[id]
	ps.mu.Unlock()
	if p == nil {
		return nil, false, errNoPairing
	}
	if candidate != "" {
		ps.mu.Lock()
		ok := p.candidate != nil && subtle.ConstantTimeCompare(p.candidate, hashToken(candidate)) == 1
		ps.mu.Unlock()
		if !ok {
			return nil, false, errNoPairing
		}
		return p, false, nil
	}
	d, err := ps.s.authenticate(ctx, bearerToken(header))
	if err != nil {
		return nil, false, err
	}
	if d.id != p.initiator {
		return nil, false, errNoPairing
	}
	return p, true, nil
}

func (ps *pairingService) GetPairing(ctx context.Context, req *connect.Request[pb.GetPairingRequest]) (*connect.Response[pb.GetPairingResponse], error) {
	p, initiator, err := ps.find(ctx, req.Header().Get("Authorization"), req.Msg.PairingId, req.Msg.CandidateToken)
	if err != nil {
		return nil, err
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	res := &pb.GetPairingResponse{State: p.state(), Commit: p.commit, JoinerPublic: p.joinerPub, JoinerKind: p.joinerKind, InitiatorPublic: p.initPub}
	if !initiator && p.payload != nil {
		// Delivered once. The initiator learns of it from the state.
		res.Payload = p.payload
		p.payload, p.delivered = nil, true
		p.expires = time.Now().Add(pairingDeliveredTTL)
		delete(ps.byCode, p.code)
	}
	return connect.NewResponse(res), nil
}

func (ps *pairingService) RevealPairing(ctx context.Context, req *connect.Request[pb.RevealPairingRequest]) (*connect.Response[pb.RevealPairingResponse], error) {
	p, initiator, err := ps.find(ctx, req.Header().Get("Authorization"), req.Msg.PairingId, "")
	if err != nil || !initiator {
		return nil, errNoPairing
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if p.joinerPub == nil || p.initPub != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("nothing to reveal to yet"))
	}
	// The server checks the commitment too, but the joiner does not rely
	// on it: it checks the key against the commitment itself.
	if !bytes.Equal(e2e.PairCommit(req.Msg.InitiatorPublic), p.commit) {
		return nil, invalid("the key does not match the commitment")
	}
	p.initPub = bytes.Clone(req.Msg.InitiatorPublic)
	return connect.NewResponse(&pb.RevealPairingResponse{}), nil
}

func (ps *pairingService) ApprovePairing(ctx context.Context, req *connect.Request[pb.ApprovePairingRequest]) (*connect.Response[pb.ApprovePairingResponse], error) {
	p, initiator, err := ps.find(ctx, req.Header().Get("Authorization"), req.Msg.PairingId, "")
	if err != nil || !initiator {
		return nil, errNoPairing
	}
	if _, err := checkEnvelope(req.Msg.Payload, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_PAIRING, RequestId: p.id}); err != nil {
		return nil, err
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if p.initPub == nil || p.canceled || p.delivered {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the pairing is not ready"))
	}
	p.payload = req.Msg.Payload
	return connect.NewResponse(&pb.ApprovePairingResponse{}), nil
}

func (ps *pairingService) CancelPairing(ctx context.Context, req *connect.Request[pb.CancelPairingRequest]) (*connect.Response[pb.CancelPairingResponse], error) {
	p, _, err := ps.find(ctx, req.Header().Get("Authorization"), req.Msg.PairingId, req.Msg.CandidateToken)
	if err != nil {
		return nil, err
	}
	ps.mu.Lock()
	p.canceled = true
	// Keep it briefly so the other side sees the cancellation.
	p.expires = time.Now().Add(30 * time.Second)
	delete(ps.byCode, p.code)
	ps.mu.Unlock()
	return connect.NewResponse(&pb.CancelPairingResponse{}), nil
}

func bearerToken(h string) string {
	t, _ := strings.CutPrefix(h, "Bearer ")
	return t
}
