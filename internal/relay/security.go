package relay

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/paths"
	"google.golang.org/protobuf/proto"
)

// SecurityConfig is initialized by pairing. It is never reconstructed from
// root possession, a relay list or a missing security database.
type SecurityConfig struct {
	Pin      deviceauth.Pin `json:"pin"`
	XPrivate []byte         `json:"x_private"`
}

// securityPath is daemon storage generation 4.
func securityPath() string { return filepath.Join(paths.Dir(), "security-v4.db") }

// CheckSecurityStorage refuses an old trust anchor before startup or pairing
// can create a fresh identity. The manual tool is the only upgrade path.
func CheckSecurityStorage() error {
	if _, err := os.Lstat(securityPath()); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return deviceauth.ErrStorage
	}
	if _, err := os.Lstat(filepath.Join(paths.Dir(), "security-v3.db")); !os.IsNotExist(err) {
		return deviceauth.ErrStorageVersion
	}
	return nil
}

// role is the signed state's copy of this device's membership.
type securitySession struct {
	clock         func() uint64
	endpoint      *deviceauth.Endpoint
	client        remoteagentv1connect.ClientServiceClient
	syncNow       chan struct{}
	blocked       atomic.Bool
	authMu        sync.Mutex
	authorization context.Context
	revoke        context.CancelFunc
	beforeSync    func() error
	onAuthLost    func()
	streams       map[uint64]*authorizedStream
	nextStream    uint64
}

func openSecuritySession(acct *Account, client remoteagentv1connect.ClientServiceClient) (*securitySession, error) {
	if acct.Security == nil {
		return nil, errors.New("remote security is not initialized; run `remote-agent account join` with a code from the authority client")
	}
	if err := CheckSecurityStorage(); err != nil {
		return nil, err
	}
	endpoint, err := deviceauth.OpenEndpoint(securityPath())
	if err != nil {
		return nil, err
	}
	pin, _, id := endpoint.Snapshot()
	if pin != acct.Security.Pin || id != acct.DeviceID {
		endpoint.Close()
		return nil, deviceauth.ErrUntrusted
	}
	if floor := endpoint.Floor(); floor > uint64(time.Now().UnixMilli())+deviceauth.MaxClockSkew {
		slog.Warn("relay: this computer's clock is behind the last seen time; remote control stays paused until it catches up", "behind_ms", floor-uint64(time.Now().UnixMilli()))
	}
	return &securitySession{endpoint: endpoint, client: client, syncNow: make(chan struct{}, 1)}, nil
}

func (s *securitySession) Close() {
	s.setAuthorized(false)
	if s.endpoint != nil {
		s.endpoint.Close()
	}
}

func securityHeader(h *pb.Header, purpose deviceauth.Purpose) deviceauth.Header {
	return deviceauth.Header{Kind: uint32(h.Kind), Purpose: purpose, Machine: h.MachineId, Session: h.SessionId, Request: h.RequestId, Seq: h.Seq, Deadline: uint64(h.DeadlineMs), Compression: uint32(h.Compression)}
}

func (s *securitySession) now() uint64 {
	if s.clock != nil {
		return s.clock()
	}
	return uint64(time.Now().UnixMilli())
}

// seal is local: the signed head and the content grant are already here, so
// there is no authority round trip.
func (s *securitySession) seal(h *pb.Header, purpose deviceauth.Purpose, seed, plain []byte) (*pb.Envelope, error) {
	return s.sealWith(s.endpoint.Prepare, h, purpose, seed, plain)
}

// sealRaw seals without compression, for payloads whose length must not
// depend on their content.
func (s *securitySession) sealRaw(h *pb.Header, purpose deviceauth.Purpose, seed, plain []byte) (*pb.Envelope, error) {
	return s.sealWith(s.endpoint.PrepareUncompressed, h, purpose, seed, plain)
}

func (s *securitySession) sealWith(prepare func(deviceauth.Header, []byte, []byte) (deviceauth.Ciphertext, error), h *pb.Header, purpose deviceauth.Purpose, seed, plain []byte) (*pb.Envelope, error) {
	if s.blocked.Load() {
		return nil, deviceauth.ErrUntrusted
	}
	if err := s.endpoint.CheckLease(s.now()); err != nil {
		return nil, err
	}
	wire, err := prepare(securityHeader(h, purpose), seed, plain)
	if err != nil {
		return nil, err
	}
	return wire.Envelope()
}

func (s *securitySession) sealMessage(h *pb.Header, purpose deviceauth.Purpose, seed []byte, m proto.Message) (*pb.Envelope, error) {
	b, err := proto.Marshal(m)
	if err != nil {
		return nil, err
	}
	return s.seal(h, purpose, seed, b)
}

func (s *securitySession) requestSync() {
	select {
	case s.syncNow <- struct{}{}:
	default:
	}
}

// open verifies an envelope against the local chain. An unknown state is
// fetched immediately and the open is retried once.
func (s *securitySession) open(ctx context.Context, w deviceauth.Wire, seed []byte, live bool, now uint64) (deviceauth.Header, []byte, error) {
	if live && s.blocked.Load() {
		return deviceauth.Header{}, nil, deviceauth.ErrUntrusted
	}
	h, plain, err := s.endpoint.Open(w, seed, live, now)
	if !errors.Is(err, deviceauth.ErrUnknown) {
		return h, plain, err
	}
	s.requestSync()
	if err := s.syncOnce(ctx); err != nil {
		return h, nil, err
	}
	return s.endpoint.Open(w, seed, live, now)
}

// accept is the at-most-once control gate. An unknown state is fetched first
// and the whole gate is retried once.
func (s *securitySession) accept(ctx context.Context, w deviceauth.Wire, seed []byte, now uint64, fn func(deviceauth.Header, []byte) (deviceauth.Completion, error)) ([]byte, error) {
	return s.acceptWith(ctx, w, seed, now, nil, fn)
}
func (s *securitySession) acceptWith(ctx context.Context, w deviceauth.Wire, seed []byte, now uint64, acquire func(context.Context) (func(), error), fn func(deviceauth.Header, []byte) (deviceauth.Completion, error)) ([]byte, error) {
	options := deviceauth.AcceptOptions{AllowNew: func() bool { return !s.blocked.Load() }, Acquire: acquire}
	out, err := s.endpoint.AcceptWith(ctx, w, seed, now, options, fn)
	if !errors.Is(err, deviceauth.ErrUnknown) {
		return out, err
	}
	s.requestSync()
	if err := s.syncOnce(ctx); err != nil {
		return nil, err
	}
	return s.endpoint.AcceptWith(ctx, w, seed, now, options, fn)
}

// sealResult is only used after authenticated acceptance lookup/completion.
// An HTTP authentication pause prevents new work, but cannot hide durable results.
func (s *securitySession) sealResult(h *pb.Header, seed, plain []byte, raw bool) (*pb.Envelope, error) {
	prepare := s.endpoint.Prepare
	if raw {
		prepare = s.endpoint.PrepareUncompressed
	}
	wire, err := prepare(securityHeader(h, deviceauth.Result), seed, plain)
	if err != nil {
		return nil, err
	}
	return wire.Envelope()
}

// syncOnce pulls states after the local head, verifies and installs them.
// The connection maintenance loop separately retries current checkpoints.
func (s *securitySession) syncOnce(ctx context.Context) error {
	if s.beforeSync != nil {
		if err := s.beforeSync(); err != nil {
			return err
		}
	}
	_, err := s.pull(ctx)
	if securityAuthenticationLost(err) || s.endpoint.CheckLease(s.now()) != nil {
		s.setAuthorized(false)
		if securityAuthenticationLost(err) && s.onAuthLost != nil {
			s.onAuthLost()
		}
	} else if err == nil {
		s.setAuthorized(true)
	}
	return err
}

func (s *securitySession) pull(ctx context.Context) (uint64, error) {
	for page := 0; page < deviceauth.MaxStates; page++ {
		after := s.endpoint.HeadEpoch()
		now := s.now()
		if err := s.endpoint.Touch(now); err != nil {
			return 0, err
		}
		res, err := s.client.GetSecurityStates(ctx, connect.NewRequest(&pb.GetSecurityStatesRequest{AfterEpoch: after, Limit: 8}))
		if err != nil {
			return 0, err
		}
		if res.Msg.HeadEpoch < after {
			return 0, deviceauth.ErrStale
		}
		states := make([]deviceauth.State, 0, len(res.Msg.States))
		proofs := make([]*deviceauth.RecoveryStatement, 0, len(res.Msg.States))
		for _, in := range res.Msg.States {
			st, err := deviceauth.ParseState(in.State)
			if err != nil {
				return 0, err
			}
			var proof *deviceauth.RecoveryStatement
			if len(in.Recovery) > 0 {
				p, err := deviceauth.ParseRecoveryStatement(in.Recovery)
				if err != nil {
					return 0, err
				}
				proof = &p
			}
			states = append(states, st)
			proofs = append(proofs, proof)
		}
		if len(states) > 0 {
			installErr := s.endpoint.Install(states, proofs)
			s.revalidateStreams()
			if installErr != nil {
				return 0, installErr
			}
		}
		head := s.endpoint.HeadEpoch()
		if head >= res.Msg.HeadEpoch {
			if len(res.Msg.Lease) > 0 {
				l, err := deviceauth.ParseLease(res.Msg.Lease)
				if err != nil {
					return 0, err
				}
				if err := s.endpoint.InstallLease(l, now); err != nil {
					return 0, err
				}
			}
			return head, nil
		}
		if head <= after {
			return 0, deviceauth.ErrStale
		}
	}
	return 0, deviceauth.ErrCapacity
}

// Authentication loss on the WS login is reported by Relay.Run. Independent
// HTTP credentials renew Unauthenticated responses inside their own component.
func securityAuthenticationLost(err error) bool {
	return connect.CodeOf(err) == connect.CodeUnauthenticated || connect.CodeOf(err) == connect.CodePermissionDenied
}

// syncLoop retains the current 60/15-second timing. The shorter direct
// liveness budget belongs to phase 1. It starts independently of WS readiness.
func (s *securitySession) syncLoop(ctx context.Context, interval time.Duration, initialDone chan<- struct{}) error {
	var warn warningGate
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	s.requestSync()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case <-s.syncNow:
		}
		attempt, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := s.syncOnce(attempt)
		cancel()
		if initialDone != nil {
			close(initialDone)
			initialDone = nil
		}
		if err != nil && ctx.Err() == nil && warn.allow(time.Now()) {
			slog.Warn("relay: security synchronization failed; retrying next sync", "code", connect.CodeOf(err).String())
		}
	}
}

func publishedEpochPath() string { return filepath.Join(paths.Dir(), "security-published") }

func readPublishedEpoch() (uint64, error) {
	b, err := os.ReadFile(publishedEpochPath())
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
}

func writePublishedEpoch(epoch uint64) error {
	return writeFileSync(publishedEpochPath(), []byte(strconv.FormatUint(epoch, 10)))
}

// headerOfSecurity decodes the sealed header's routing projection.
func headerOfSecurity(env *pb.Envelope) *pb.Header {
	w, err := deviceauth.FromEnvelope(env)
	if err != nil {
		return &pb.Header{}
	}
	h, err := deviceauth.ParseHeader(w.Header)
	if err != nil {
		return &pb.Header{}
	}
	return h.Projection()
}

// Authentication loss pauses control and existing streams without stopping
// the process endpoint or its accepted executions. Successful independent sync
// can admit fresh streams again; it never resurrects a retired terminal.
func (s *securitySession) setAuthorized(valid bool) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if !valid {
		s.blocked.Store(true)
		if s.revoke != nil {
			s.revoke()
		}
	} else if s.blocked.Swap(false) {
		s.authorization, s.revoke = context.WithCancel(context.Background())
	}
}

type authorizedStream struct {
	peer   deviceauth.Member
	cancel context.CancelFunc
}
type streamAuthorizationKey struct{}

func (s *securitySession) peerCurrent(peer deviceauth.Member) bool {
	_, head, _ := s.endpoint.Snapshot()
	current, ok := head.Core.Member(peer.ID)
	return ok && current.Kind == deviceauth.Client && current == peer
}
func (s *securitySession) revalidateStreams() {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	for id, stream := range s.streams {
		if !s.peerCurrent(stream.peer) {
			stream.cancel()
			delete(s.streams, id)
		}
	}
}
func (s *securitySession) confirmStream(ctx context.Context) error {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if s.blocked.Load() {
		return deviceauth.ErrUntrusted
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	peer, ok := ctx.Value(streamAuthorizationKey{}).(deviceauth.Member)
	if !ok || !s.peerCurrent(peer) {
		return deviceauth.ErrUntrusted
	}
	return s.endpoint.CheckLease(s.now())
}
func (s *securitySession) streamContext(parent context.Context, peerID string) (context.Context, context.CancelFunc, error) {
	s.authMu.Lock()
	defer s.authMu.Unlock()
	if s.blocked.Load() {
		return nil, nil, deviceauth.ErrUntrusted
	}
	if err := s.endpoint.CheckLease(s.now()); err != nil {
		return nil, nil, err
	}
	_, head, _ := s.endpoint.Snapshot()
	peer, ok := head.Core.Member(peerID)
	if !ok || peer.Kind != deviceauth.Client {
		return nil, nil, deviceauth.ErrUntrusted
	}
	if s.authorization == nil {
		s.authorization, s.revoke = context.WithCancel(context.Background())
	}
	authorization := s.authorization
	ctx, cancel := context.WithCancel(context.WithValue(parent, streamAuthorizationKey{}, peer))
	stop := context.AfterFunc(authorization, cancel)
	if s.streams == nil {
		s.streams = map[uint64]*authorizedStream{}
	}
	s.nextStream++
	id := s.nextStream
	s.streams[id] = &authorizedStream{peer, cancel}
	return ctx, func() { stop(); cancel(); s.authMu.Lock(); delete(s.streams, id); s.authMu.Unlock() }, nil
}
