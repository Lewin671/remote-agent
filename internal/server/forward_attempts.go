package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"sync"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"google.golang.org/protobuf/proto"
)

const relayEpochHeader = "Remote-Agent-Relay-Epoch"
const maxForwardAttempts = 65536
const maxAccountForwardAttempts = 8192
const resultQueryGrace = int64(deviceauth.ResultRetention / time.Millisecond)
const attemptGrace = int64(time.Minute / time.Millisecond)
const maxAttemptDeadline = int64(10 * time.Minute / time.Millisecond)

type attemptPermitKey struct{}
type attemptState uint8

const (
	attemptPending attemptState = iota
	attemptRefused
	attemptEnqueued
)

type attemptRecord struct {
	account                              string
	body                                 [32]byte
	queryUntil, expires, monotonicExpiry int64
	state                                attemptState
	code                                 connect.Code
	message, reason                      string
}
type attemptLedger struct {
	mu                  sync.Mutex
	epoch               string
	entries             map[string]*attemptRecord
	accounts            map[string]int
	floor               int64
	limit, accountLimit int
	clock               func() (wall, monotonic int64)
}
type attemptPermit struct {
	ledger *attemptLedger
	entry  *attemptRecord
	id     string
}

func newAttemptLedger() *attemptLedger {
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		panic(err)
	}
	started := time.Now()
	return &attemptLedger{epoch: hex.EncodeToString(nonce[:]), entries: make(map[string]*attemptRecord), accounts: make(map[string]int), limit: maxForwardAttempts, accountLimit: maxAccountForwardAttempts,
		clock: func() (int64, int64) { return time.Now().UnixMilli(), time.Since(started).Milliseconds() }}
}
func attemptUnknown(reason string) error {
	return connect.NewError(connect.CodeAborted, errors.New(reason))
}

func (s *Server) prepareAttempt(dev *device, req *pb.ForwardRequest, header string) (*attemptPermit, error) {
	l := s.forwardAttempts
	if req.RelayEpoch == "" || req.RelayEpoch != l.epoch {
		return nil, attemptUnknown("relay_restarted")
	}
	if !validAttempt(req.AttemptId) || header != req.AttemptId {
		return nil, attemptUnknown("attempt_unsealed")
	}
	// This exception is determined by immutable body bytes, never live state.
	if _, err := checkEnvelope(req.Command, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND, MachineId: req.MachineId}); err != nil {
		return nil, notForwarded(err, req.AttemptId, l.epoch, "invalid_envelope")
	}
	h := headerOf(req.Command)
	// Hash only opaque envelope/routing bytes, never command plaintext.
	raw, err := proto.Marshal(req)
	if err != nil {
		return nil, attemptUnknown("attempt_unsealed")
	}
	digest := sha256.Sum256(raw)
	return l.reserve(dev.account, dev.id, req.AttemptId, req.RelayEpoch, h.DeadlineMs, digest)
}
func (l *attemptLedger) reserve(account, _, id, epoch string, deadline int64, body [32]byte) (*attemptPermit, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if epoch == "" || epoch != l.epoch {
		return nil, attemptUnknown("relay_restarted")
	}
	wall, mono := l.clock()
	l.floor = max(l.floor, wall)
	// Routing freshness includes retained-result lookup, but never changes the
	// command's execution deadline. Reject before adding durations to wire input.
	if deadline <= 0 || deadline > math.MaxInt64-resultQueryGrace-attemptGrace || deadline-l.floor > maxAttemptDeadline {
		return nil, attemptUnknown("attempt_expired")
	}
	queryUntil := deadline + resultQueryGrace
	// A cleanup/clock rollback cannot make an old body fresh again.
	if queryUntil <= l.floor {
		return nil, attemptUnknown("attempt_expired")
	}
	// Authorization headers can change across copies; the body attempt must
	// keep its decision even if a later copy authenticates as another device.
	key := id
	if old := l.entries[key]; old != nil {
		if old.body == body && old.state == attemptRefused {
			return nil, l.refusal(id, old)
		}
		return nil, attemptUnknown("attempt_duplicate")
	}
	for k, old := range l.entries {
		if wall >= old.expires && mono >= old.monotonicExpiry {
			delete(l.entries, k)
			l.accounts[old.account]--
			if l.accounts[old.account] == 0 {
				delete(l.accounts, old.account)
			}
		}
	}
	if len(l.entries) >= l.limit || l.accounts[account] >= l.accountLimit {
		return nil, attemptUnknown("attempt_capacity")
	}
	r := &attemptRecord{account: account, body: body, queryUntil: queryUntil, expires: queryUntil + attemptGrace, monotonicExpiry: mono + (queryUntil - l.floor) + attemptGrace}
	l.entries[key] = r
	l.accounts[account]++
	return &attemptPermit{ledger: l, entry: r, id: id}, nil
}
func (l *attemptLedger) refusal(id string, r *attemptRecord) error {
	return notForwarded(connect.NewError(r.code, errors.New(r.message)), id, l.epoch, r.reason)
}
func (p *attemptPermit) refuse(err error, reason string) error {
	p.ledger.mu.Lock()
	defer p.ledger.mu.Unlock()
	return p.refuseLocked(err, reason)
}
func (p *attemptPermit) refuseLocked(err error, reason string) error {
	if p.entry.state == attemptEnqueued {
		return attemptUnknown("attempt_duplicate")
	}
	if p.entry.state != attemptRefused {
		p.entry.state = attemptRefused
		p.entry.code = connect.CodeOf(err)
		p.entry.reason = reason
		var ce *connect.Error
		if errors.As(err, &ce) {
			p.entry.message = ce.Message()
		} else {
			p.entry.message = err.Error()
		}
		if len(p.entry.message) > 1024 {
			p.entry.message = reason
		}
	}
	return p.ledger.refusal(p.id, p.entry)
}
func (p *attemptPermit) enqueue(ctx context.Context, send func() bool) error {
	p.ledger.mu.Lock()
	defer p.ledger.mu.Unlock()
	if p.entry.state != attemptPending {
		return attemptUnknown("attempt_duplicate")
	}
	wall, _ := p.ledger.clock()
	p.ledger.floor = max(wall, p.ledger.floor)
	if p.entry.queryUntil <= p.ledger.floor {
		return attemptUnknown("attempt_expired")
	}
	if err := ctx.Err(); err != nil {
		return p.refuseLocked(connect.NewError(connect.CodeCanceled, err), "entry_refused")
	}
	if !send() {
		return p.refuseLocked(connect.NewError(connect.CodeUnavailable, errors.New("machine is not keeping up")), "queue_full")
	}
	p.entry.state = attemptEnqueued
	return nil
}
