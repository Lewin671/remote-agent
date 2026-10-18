package relay

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// securityPath is daemon storage generation 3.
func securityPath() string { return filepath.Join(paths.Dir(), "security-v3.db") }

// role is the signed state's copy of this device's membership.
type securitySession struct {
	endpoint   *deviceauth.Endpoint
	client     remoteagentv1connect.ClientServiceClient
	syncNow    chan struct{}
	onRecovery func()
}

func openSecuritySession(acct *Account, client remoteagentv1connect.ClientServiceClient) (*securitySession, error) {
	if acct.Security == nil {
		return nil, errors.New("remote security is not initialized; run `remote-agent account join` with a code from the phone")
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
	return &securitySession{endpoint: endpoint, client: client, syncNow: make(chan struct{}, 1)}, nil
}

func (s *securitySession) Close() {
	if s.endpoint != nil {
		s.endpoint.Close()
	}
}

func securityHeader(h *pb.Header, purpose deviceauth.Purpose) deviceauth.Header {
	return deviceauth.Header{Kind: uint32(h.Kind), Purpose: purpose, Machine: h.MachineId, Session: h.SessionId, Request: h.RequestId, Seq: h.Seq, Deadline: uint64(h.DeadlineMs), Compression: uint32(h.Compression)}
}

// seal is local: the signed head and the content grant are already here, so
// there is no authority round trip.
func (s *securitySession) seal(h *pb.Header, purpose deviceauth.Purpose, seed, plain []byte) (*pb.Envelope, error) {
	wire, err := s.endpoint.Prepare(securityHeader(h, purpose), seed, plain)
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
func (s *securitySession) accept(ctx context.Context, w deviceauth.Wire, seed []byte, now uint64, fn func(deviceauth.Header, []byte) ([]byte, error)) ([]byte, error) {
	out, err := s.endpoint.Accept(w, seed, now, fn)
	if !errors.Is(err, deviceauth.ErrUnknown) {
		return out, err
	}
	s.requestSync()
	if err := s.syncOnce(ctx); err != nil {
		return nil, err
	}
	return s.endpoint.Accept(w, seed, now, fn)
}

// syncOnce pulls states after the local head, verifies and installs them.
// It reports whether a recovery boundary was installed, so the caller can
// re-publish current metadata under the new content key.
func (s *securitySession) syncOnce(ctx context.Context) error {
	_, recovered, err := s.pull(ctx)
	if err == nil && recovered && s.onRecovery != nil {
		s.onRecovery()
	}
	return err
}

func (s *securitySession) pull(ctx context.Context) (uint64, bool, error) {
	before := s.endpoint.HeadEpoch()
	_, beforeState, _ := s.endpoint.Snapshot()
	res, err := s.client.GetSecurityStates(ctx, connect.NewRequest(&pb.GetSecurityStatesRequest{AfterEpoch: before, Limit: 8}))
	if err != nil {
		return 0, false, err
	}
	if err := s.endpoint.Touch(uint64(time.Now().UnixMilli())); err != nil {
		return 0, false, err
	}
	if len(res.Msg.States) == 0 {
		return before, false, nil
	}
	states := make([]deviceauth.State, 0, len(res.Msg.States))
	proofs := make([]*deviceauth.RecoveryStatement, 0, len(res.Msg.States))
	recovered := false
	for _, in := range res.Msg.States {
		st, err := deviceauth.ParseState(in.State)
		if err != nil {
			return 0, false, err
		}
		var proof *deviceauth.RecoveryStatement
		if len(in.Recovery) > 0 {
			p, err := deviceauth.ParseRecoveryStatement(in.Recovery)
			if err != nil {
				return 0, false, err
			}
			proof = &p
			recovered = true
		}
		states = append(states, st)
		proofs = append(proofs, proof)
	}
	if err := s.endpoint.Install(states, proofs); err != nil {
		return 0, false, err
	}
	_, head, _ := s.endpoint.Snapshot()
	if head.Core.Epoch > beforeState.Core.Epoch && head.Core.AuthorityID != beforeState.Core.AuthorityID {
		recovered = true
	}
	_ = s.endpoint.Touch(uint64(time.Now().UnixMilli()))
	return head.Core.Epoch, recovered, nil
}

// securitySyncLoop polls every 60 seconds and immediately on an unknown
// state. It advances the time floor even when no control envelope arrives.
func (c *conn) securitySyncLoop(ctx context.Context) error {
	if err := c.security.endpoint.Touch(uint64(time.Now().UnixMilli())); err != nil {
		return fmt.Errorf("security time floor: %w", err)
	}
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case <-c.security.syncNow:
		}
		attempt, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := c.security.syncOnce(attempt)
		cancel()
		if err != nil {
			return fmt.Errorf("security synchronization: %w", err)
		}
		publishCtx, publishCancel := context.WithTimeout(ctx, 60*time.Second)
		err = c.ensurePublished(publishCtx)
		publishCancel()
		if err != nil {
			return fmt.Errorf("security republish: %w", err)
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
