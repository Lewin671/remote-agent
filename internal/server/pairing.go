package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/protocol"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"google.golang.org/protobuf/proto"
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
	maxPairings         = 512
	maxAccountPairings  = 4
	// Wrong codes are bounded per address, so that one caller cannot
	// guess codes, and in total, so that many cannot; the budget per
	// address keeps one caller from using up everyone's.
	badCodesPerMinute     = 300
	badCodesPerMinuteAddr = 10
)

type pairing struct {
	id                    string
	code                  string
	account               string // the initiator's
	initiator             string // device ID
	commit                []byte
	expires               time.Time
	attempts              int
	candidate             []byte // hash of the joiner's token
	joinerPub             []byte
	devicePub             []byte
	deviceX               []byte
	trust                 []byte
	approval, approvalSig []byte
	payloadHash           [32]byte
	joinerKind            pb.DeviceKind
	initPub               []byte
	payload               *pb.Envelope
	canceled              bool
	delivered             bool
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

	mu           sync.Mutex
	byID         map[string]*pairing
	byCode       map[string]*pairing
	badCodes     []time.Time
	badCodesAddr map[string][]time.Time
}

func newPairingService(s *Server) *pairingService {
	return &pairingService{s: s, byID: map[string]*pairing{}, byCode: map[string]*pairing{}, badCodesAddr: map[string][]time.Time{}}
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
	// Serialize the membership check with the normal removal path.
	d, err = ps.s.authenticate(ctx, bearerToken(req.Header().Get("Authorization")))
	if err != nil {
		return nil, err
	}
	if err := ps.requireAuthority(ctx, d); err != nil {
		return nil, err
	}
	ps.sweepLocked()
	mine := 0
	for _, p := range ps.byID {
		if p.account == d.account {
			mine++
		}
	}
	if len(ps.byID) >= maxPairings || mine >= maxAccountPairings {
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
	p := &pairing{id: randomHex(16), code: code, account: d.account, initiator: d.id, commit: req.Msg.Commit, expires: time.Now().Add(pairingTTL)}
	ps.byID[p.id], ps.byCode[code] = p, p
	return connect.NewResponse(&pb.CreatePairingResponse{PairingId: p.id, Code: code, ExpiresMs: p.expires.UnixMilli(), ApproverPublicKey: bytes.Clone(d.publicKey)}), nil
}

func (ps *pairingService) JoinPairing(ctx context.Context, req *connect.Request[pb.JoinPairingRequest]) (*connect.Response[pb.JoinPairingResponse], error) {
	if req.Msg.ProtocolVersion != protocol.Version {
		return nil, protocol.Mismatch("the joining device", req.Msg.ProtocolVersion)
	}
	if len(req.Msg.JoinerPublic) != 32 || len(req.Msg.DevicePublicKey) != ed25519.PublicKeySize || len(req.Msg.DeviceHpkePublicKey) != 32 {
		return nil, invalid("the public key must be 32 bytes")
	}
	if req.Msg.JoinerKind != pb.DeviceKind_DEVICE_KIND_MACHINE && req.Msg.JoinerKind != pb.DeviceKind_DEVICE_KIND_CLIENT {
		return nil, invalid("unknown device kind")
	}
	code := strings.ReplaceAll(strings.TrimSpace(req.Msg.Code), " ", "")
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p, err := ps.codeLocked(code, req.Peer().Addr, req.Header())
	if err != nil {
		return nil, err
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
	p.devicePub = bytes.Clone(req.Msg.DevicePublicKey)
	p.deviceX = bytes.Clone(req.Msg.DeviceHpkePublicKey)
	p.joinerPub, p.joinerKind = bytes.Clone(req.Msg.JoinerPublic), req.Msg.JoinerKind
	return connect.NewResponse(&pb.JoinPairingResponse{PairingId: p.id, Commit: p.commit, CandidateToken: token}), nil
}

// requireAuthority makes the app's device page the only place that starts or
// approves a pairing on an honest relay.
func (ps *pairingService) requireAuthority(ctx context.Context, d *device) error {
	if d == nil {
		return errNoPairing
	}
	authority, err := ps.s.store.tenant(d.account).authorityDevice(ctx)
	if err != nil || authority != d.id {
		return connect.NewError(connect.CodePermissionDenied, errors.New("only the current authority client may manage pairings"))
	}
	return nil
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
	if err := ps.s.authorizeDevice(ctx, d, ""); err != nil {
		return nil, false, err
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
	actor, actorErr := ps.s.store.device(ctx, p.initiator)
	account, accountErr := ps.s.store.account(ctx, p.account)
	if actorErr != nil || actor.account != p.account || accountErr != nil || account.status != accountActive || time.Now().After(p.expires) {
		if err := ps.s.store.cancelAdmission(ctx, p.id); err != nil && !errors.Is(err, errEnrollmentCommitted) {
			return nil, internal(err)
		}
		p.canceled = true
		p.payload = nil
	}
	res := &pb.GetPairingResponse{State: p.state(), Commit: p.commit, JoinerPublic: p.joinerPub, JoinerKind: p.joinerKind, InitiatorPublic: p.initPub, DevicePublicKey: p.devicePub, DeviceHpkePublicKey: p.deviceX, Trust: p.trust}
	switch {
	case initiator || p.payload == nil:
	case req.Msg.Received:
		d, err := ps.s.store.device(ctx, e2e.DeviceID(p.devicePub))
		if err != nil || d.account != p.account {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("device registration is not complete"))
		}
		// The joiner has the key: drop it, and let the initiator know.
		p.payload, p.delivered = nil, true
		p.expires = time.Now().Add(pairingDeliveredTTL)
		delete(ps.byCode, p.code)
		res.State = p.state()
	default:
		res.Payload = p.payload
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
	if p.canceled || p.delivered || p.joinerPub == nil || p.initPub != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("nothing to reveal to yet"))
	}
	// The descriptor is public (account, domain, genesis hash, approver).
	// The server checks it against the commitment; the joiner checks it
	// again itself before trusting the chain in the payload.
	if len(req.Msg.Trust) == 0 || len(req.Msg.Trust) > 8<<10 {
		return nil, invalid("missing or oversized trust descriptor")
	}
	pin, approver, err := deviceauth.ParsePairTrustDescriptor(req.Msg.Trust)
	if err != nil || pin.Account != p.account || approver != p.initiator || !bytes.Equal(e2e.PairDomainCommit(req.Msg.InitiatorPublic, req.Msg.Trust), p.commit) {
		return nil, invalid("the trust descriptor does not match the commitment")
	}
	p.trust = bytes.Clone(req.Msg.Trust)
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
	payloadBytes, err := proto.MarshalOptions{Deterministic: true}.Marshal(req.Msg.Payload)
	if err != nil {
		return nil, invalid("malformed pairing payload")
	}
	payloadHash := sha256.Sum256(payloadBytes)
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if (p.payload != nil || p.delivered) && !p.canceled && bytes.Equal(p.approval, req.Msg.Approval) && bytes.Equal(p.approvalSig, req.Msg.ApprovalSignature) && p.payloadHash == payloadHash {
		return connect.NewResponse(&pb.ApprovePairingResponse{}), nil
	}
	if p.initPub == nil || p.canceled || p.delivered {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the pairing is not ready"))
	}
	approver, err := ps.s.store.device(ctx, p.initiator)
	if err != nil || ps.requireAuthority(ctx, approver) != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("only the current authority client may approve pairings"))
	}
	a, err := parseApproval(req.Msg.Approval, req.Msg.ApprovalSignature)
	if err != nil || a == nil {
		return nil, connect.NewError(connect.CodePermissionDenied, errAdmissionRequired)
	}
	cert := a.info
	if cert.AccountId != p.account || cert.PairingId != p.id || cert.ApproverDeviceId != p.initiator || !bytes.Equal(cert.CandidateDevicePublicKey, p.devicePub) || cert.CandidateKind != p.joinerKind || cert.ExpiresMs > p.expires.UnixMilli() || cert.ExpiresMs <= nowMs() {
		return nil, connect.NewError(connect.CodePermissionDenied, errAdmissionRequired)
	}
	if err := ps.s.store.approveAdmission(ctx, a, p.candidate); err != nil {
		var apiErr *connect.Error
		switch {
		case errors.As(err, &apiErr):
			return nil, err
		case errors.Is(err, errAdmissionRequired):
			return nil, connect.NewError(connect.CodePermissionDenied, err)
		default:
			return nil, internal(err)
		}
	}
	p.approval, p.approvalSig = bytes.Clone(a.raw), bytes.Clone(a.signature)
	p.payloadHash = payloadHash
	p.payload = proto.Clone(req.Msg.Payload).(*pb.Envelope)
	return connect.NewResponse(&pb.ApprovePairingResponse{}), nil
}

func (ps *pairingService) CancelPairing(ctx context.Context, req *connect.Request[pb.CancelPairingRequest]) (*connect.Response[pb.CancelPairingResponse], error) {
	p, _, err := ps.find(ctx, req.Header().Get("Authorization"), req.Msg.PairingId, req.Msg.CandidateToken)
	if err != nil {
		// A server restart drops only ephemeral pairing state. Persisted
		// approvals must remain cancelable by the original candidate or approver.
		if connect.CodeOf(err) != connect.CodeNotFound {
			return nil, err
		}
		var account, approver string
		var candidateHash []byte
		if e := ps.s.store.db.QueryRowContext(ctx, `SELECT account_id, approver_device_id, candidate_token_hash FROM pairing_admissions WHERE pairing_id = ? AND expires_ms > ?`, req.Msg.PairingId, nowMs()).Scan(&account, &approver, &candidateHash); e != nil {
			return nil, errNoPairing
		}
		allowed := req.Msg.CandidateToken != "" && len(candidateHash) == 32 && subtle.ConstantTimeCompare(candidateHash, hashToken(req.Msg.CandidateToken)) == 1
		if !allowed && req.Msg.CandidateToken == "" {
			d, e := ps.s.authenticate(ctx, bearerToken(req.Header().Get("Authorization")))
			allowed = e == nil && d.account == account && d.id == approver
		}
		if !allowed {
			return nil, errNoPairing
		}
		if e := ps.s.store.cancelAdmission(ctx, req.Msg.PairingId); e != nil {
			return nil, connect.NewError(connect.CodeFailedPrecondition, e)
		}
		return connect.NewResponse(&pb.CancelPairingResponse{}), nil
	}
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if err := ps.s.store.cancelAdmission(ctx, p.id); err != nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	}
	p.canceled = true
	p.payload = nil
	// Keep it briefly so the other side sees the cancellation.
	p.expires = time.Now().Add(30 * time.Second)
	delete(ps.byCode, p.code)
	return connect.NewResponse(&pb.CancelPairingResponse{}), nil
}

func bearerToken(h string) string {
	t, _ := strings.CutPrefix(h, "Bearer ")
	return t
}

func (ps *pairingService) codeLocked(code, peer string, header http.Header) (*pairing, error) {
	ps.sweepLocked()
	// Apply the limit before looking up a code. Otherwise callers that have
	// exhausted the budget can still test unlimited codes and distinguish
	// valid ones from the rate-limited invalid responses.
	now := time.Now()
	addr := ps.s.clientAddr(peer, header)
	for a, ts := range ps.badCodesAddr {
		if ps.badCodesAddr[a], _ = recent(ts, now, time.Minute, 0); len(ps.badCodesAddr[a]) == 0 {
			delete(ps.badCodesAddr, a)
		}
	}
	var okAll, okAddr bool
	ps.badCodes, okAll = recent(ps.badCodes, now, time.Minute, badCodesPerMinute)
	_, okAddr = recent(ps.badCodesAddr[addr], now, time.Minute, badCodesPerMinuteAddr)
	if !okAll || !okAddr {
		return nil, connect.NewError(connect.CodeResourceExhausted, errors.New("too many wrong codes; try again in a minute"))
	}
	p := ps.byCode[code]
	if p == nil {
		ps.badCodes = append(ps.badCodes, now)
		ps.badCodesAddr[addr] = append(ps.badCodesAddr[addr], now)
		return nil, errNoPairing
	}
	return p, nil
}
func (ps *pairingService) PeekPairing(ctx context.Context, req *connect.Request[pb.PeekPairingRequest]) (*connect.Response[pb.PeekPairingResponse], error) {
	if req.Msg.ProtocolVersion != protocol.Version {
		return nil, protocol.Mismatch("the joining device", req.Msg.ProtocolVersion)
	}
	code := strings.ReplaceAll(strings.TrimSpace(req.Msg.Code), " ", "")
	ps.mu.Lock()
	defer ps.mu.Unlock()
	p, err := ps.codeLocked(code, req.Peer().Addr, req.Header())
	if err != nil {
		return nil, err
	}
	if p.canceled || p.joinerPub != nil {
		return nil, errNoPairing
	}
	return connect.NewResponse(&pb.PeekPairingResponse{PairingId: p.id, Commit: bytes.Clone(p.commit)}), nil
}
