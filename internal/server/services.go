package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"google.golang.org/protobuf/proto"
)

func invalid(format string, args ...any) error {
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf(format, args...))
}

// checkEnvelope checks an envelope's size and that its plaintext header,
// which the server may read, says it belongs where it is being put. The
// receiving client or daemon checks the authenticated header again; this
// only catches mistakes early.
func checkEnvelope(env *pb.Envelope, want *pb.Header) (*pb.Header, error) {
	if env == nil {
		return nil, invalid("missing envelope")
	}
	if proto.Size(env) > maxEnvelopeBytes {
		return nil, invalid("envelope larger than %d bytes", maxEnvelopeBytes)
	}
	h := &pb.Header{}
	if err := proto.Unmarshal(env.Header, h); err != nil {
		return nil, invalid("malformed envelope header")
	}
	if h.Version != e2e.Version {
		return nil, invalid("unsupported envelope version %d", h.Version)
	}
	if e2e.Expect(h, want) != nil {
		return nil, invalid("envelope header does not match the request")
	}
	return h, nil
}

type authService struct {
	remoteagentv1connect.UnimplementedAuthServiceHandler
	s *Server
}

func (a *authService) RegisterDevice(ctx context.Context, req *connect.Request[pb.RegisterDeviceRequest]) (*connect.Response[pb.RegisterDeviceResponse], error) {
	reg := &pb.DeviceRegistration{}
	if err := proto.Unmarshal(req.Msg.Registration, reg); err != nil {
		return nil, invalid("malformed registration")
	}
	if len(reg.AccountPublicKey) != ed25519.PublicKeySize || len(reg.DevicePublicKey) != ed25519.PublicKeySize {
		return nil, invalid("malformed public key")
	}
	if reg.Kind != pb.DeviceKind_DEVICE_KIND_MACHINE && reg.Kind != pb.DeviceKind_DEVICE_KIND_CLIENT {
		return nil, invalid("unknown device kind")
	}
	if !e2e.VerifyRegistration(reg.AccountPublicKey, req.Msg.Registration, req.Msg.Signature) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("bad account signature"))
	}
	acct := e2e.AccountID(reg.AccountPublicKey)
	var create *newAccount
	undo := func() {}
	pl := planOf("")
	existing, err := a.s.store.account(ctx, acct)
	switch {
	case errors.Is(err, errNoRows):
		if create, undo, err = a.s.newAccountFor(req); err != nil {
			return nil, err
		}
		create.publicKey, create.max = reg.AccountPublicKey, a.s.cfg.MaxAccounts
		pl = planOf(create.plan)
	case err != nil:
		return nil, internal(err)
	case existing.status != accountActive:
		return nil, connect.NewError(connect.CodePermissionDenied, errSuspended)
	default:
		pl = planOf(existing.plan)
	}
	id := e2e.DeviceID(reg.DevicePublicKey)
	limit := func(u accountUsage) error {
		if a.s.diskLow.Load() {
			return errFull("this relay is out of storage; try again later")
		}
		return pl.check(u, growsDevices)
	}
	if err := a.s.store.addDevice(ctx, acct, create, id, reg, req.Msg.Registration, req.Msg.Signature, limit); err != nil {
		// No account was made: the signup it was admitted for is not used up.
		undo()
		var apiErr *connect.Error
		switch {
		case errors.As(err, &apiErr):
			return nil, err
		case errors.Is(err, errDeviceRevoked), errors.Is(err, errRegistrationStale), errors.Is(err, errOtherAccount):
			return nil, connect.NewError(connect.CodePermissionDenied, err)
		case errors.Is(err, errNoRoom):
			return nil, connect.NewError(connect.CodeResourceExhausted, err)
		}
		return nil, internal(err)
	}
	if create != nil {
		slog.Info("account created", "account", acct, "plan", create.plan)
	}
	slog.Info("device registered", "account", acct, "device", id, "kind", reg.Kind.String())
	t := &tenant{acct: acct, plan: pl, store: a.s.store.tenant(acct), hub: a.s.hub.account(acct)}
	if d, err := t.store.deviceEntry(ctx, id); err == nil {
		t.hub.broadcast(&pb.ServerFrame{Kind: &pb.ServerFrame_DeviceUpdated{DeviceUpdated: &pb.DeviceUpdated{Device: d}}})
	}
	return connect.NewResponse(&pb.RegisterDeviceResponse{DeviceId: id}), nil
}

func (a *authService) GetChallenge(ctx context.Context, req *connect.Request[pb.GetChallengeRequest]) (*connect.Response[pb.GetChallengeResponse], error) {
	if !validDeviceID(req.Msg.DeviceId) {
		return nil, invalid("malformed device ID")
	}
	dev, err := a.s.store.device(ctx, req.Msg.DeviceId)
	if errors.Is(err, errNoRows) {
		// Unknown devices get a challenge that is never stored, so they
		// take no space and cannot tell that the device does not exist;
		// the login fails like any other bad login.
		ch := make([]byte, 32)
		if _, err := rand.Read(ch); err != nil {
			return nil, internal(err)
		}
		return connect.NewResponse(&pb.GetChallengeResponse{Challenge: ch}), nil
	}
	if err != nil {
		return nil, internal(err)
	}
	ch, err := a.s.newChallenge(dev.id, dev.account)
	if err != nil {
		return nil, connect.NewError(connect.CodeResourceExhausted, err)
	}
	return connect.NewResponse(&pb.GetChallengeResponse{Challenge: ch}), nil
}

func (a *authService) Login(ctx context.Context, req *connect.Request[pb.LoginRequest]) (*connect.Response[pb.LoginResponse], error) {
	denied := connect.NewError(connect.CodePermissionDenied, errors.New("login failed"))
	if !a.s.takeChallenge(req.Msg.DeviceId, req.Msg.Challenge) {
		return nil, denied
	}
	d, err := a.s.store.device(ctx, req.Msg.DeviceId)
	if errors.Is(err, errNoRows) {
		return nil, denied
	}
	if err != nil {
		return nil, internal(err)
	}
	if !e2e.VerifyLogin(d.publicKey, req.Msg.Challenge, req.Msg.Signature) {
		return nil, denied
	}
	acct, err := a.s.store.account(ctx, d.account)
	if err != nil {
		return nil, internal(err)
	}
	if acct.status != accountActive {
		return nil, connect.NewError(connect.CodePermissionDenied, errSuspended)
	}
	token := newToken()
	expires := time.Now().Add(a.s.cfg.TokenTTL).UnixMilli()
	t := a.s.store.tenant(d.account)
	if err := t.addToken(ctx, hashToken(token), d.id, expires); err != nil {
		return nil, internal(err)
	}
	if err := t.touchAccount(ctx); err != nil {
		return nil, internal(err)
	}
	return connect.NewResponse(&pb.LoginResponse{Token: token, ExpiresMs: expires}), nil
}

type clientService struct {
	remoteagentv1connect.UnimplementedClientServiceHandler
	s *Server
}

func (c *clientService) ListMachines(ctx context.Context, req *connect.Request[pb.ListMachinesRequest]) (*connect.Response[pb.ListMachinesResponse], error) {
	t := c.s.tenantFrom(ctx)
	ms, err := t.store.machines(ctx)
	if err != nil {
		return nil, internal(err)
	}
	for _, m := range ms {
		m.Online = t.hub.online(m.Id)
	}
	return connect.NewResponse(&pb.ListMachinesResponse{Machines: ms}), nil
}

func (c *clientService) ListRemoteSessions(ctx context.Context, req *connect.Request[pb.ListRemoteSessionsRequest]) (*connect.Response[pb.ListRemoteSessionsResponse], error) {
	t := c.s.tenantFrom(ctx)
	if ids := req.Msg.SessionIds; len(ids) > 0 {
		if len(ids) > maxSessionPage {
			return nil, invalid("at most %d session IDs per request", maxSessionPage)
		}
		ss, err := t.store.sessionsByID(ctx, ids)
		if err != nil {
			return nil, internal(err)
		}
		return connect.NewResponse(&pb.ListRemoteSessionsResponse{Sessions: ss}), nil
	}
	limit := int(req.Msg.Limit)
	if limit <= 0 {
		limit = defaultSessionPage
	}
	limit = min(limit, maxSessionPage)
	var out []*pb.RemoteSession
	if req.Msg.After == nil {
		live, err := t.store.liveSessions(ctx)
		if err != nil {
			return nil, internal(err)
		}
		out = live
	}
	ended, next, err := t.store.endedSessions(ctx, req.Msg.After, limit)
	if err != nil {
		return nil, internal(err)
	}
	return connect.NewResponse(&pb.ListRemoteSessionsResponse{Sessions: append(out, ended...), Next: next}), nil
}

func (c *clientService) GetEvents(ctx context.Context, req *connect.Request[pb.GetEventsRequest]) (*connect.Response[pb.GetEventsResponse], error) {
	limit := int(req.Msg.Limit)
	if limit <= 0 {
		limit = defaultEventPage
	}
	limit = min(limit, maxEventPage)
	evs, more, err := c.s.tenantFrom(ctx).store.events(ctx, req.Msg.SessionId, req.Msg.AfterSeq, limit)
	if err != nil {
		return nil, internal(err)
	}
	return connect.NewResponse(&pb.GetEventsResponse{Events: evs, More: more}), nil
}

func (c *clientService) Forward(ctx context.Context, req *connect.Request[pb.ForwardRequest]) (*connect.Response[pb.ForwardResponse], error) {
	dev := deviceFrom(ctx)
	// The sender in the sealed header must be the device that is logged
	// in. The daemon checks the device's signature itself; this catches a
	// client that claims to be another device early.
	if _, err := checkEnvelope(req.Msg.Command, &pb.Header{
		Kind: pb.PayloadKind_PAYLOAD_KIND_COMMAND, MachineId: req.Msg.MachineId,
	}); err != nil {
		return nil, err
	}
	if h := headerOf(req.Msg.Command); h.Sender != dev.id {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("the command's sender is not the logged-in device"))
	}
	t := c.s.tenantOf(dev)
	reg, err := t.store.registration(ctx, dev.id)
	if err != nil {
		return nil, internal(err)
	}
	res, err := t.hub.forward(ctx, req.Msg.MachineId, &pb.ForwardCall{
		Command: req.Msg.Command, SenderDeviceId: dev.id, SenderRegistration: reg.raw, SenderRegistrationSignature: reg.sig,
	})
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&pb.ForwardResponse{Result: res}), nil
}

type machineService struct {
	remoteagentv1connect.UnimplementedMachineServiceHandler
	s *Server
}

// machineFrom returns the calling daemon's machine ID.
func machineFrom(ctx context.Context) (string, error) {
	d := deviceFrom(ctx)
	if d == nil || d.kind != pb.DeviceKind_DEVICE_KIND_MACHINE {
		return "", connect.NewError(connect.CodePermissionDenied, errors.New("only machines may call this"))
	}
	return d.id, nil
}

func (m *machineService) PutMachine(ctx context.Context, req *connect.Request[pb.PutMachineRequest]) (*connect.Response[pb.PutMachineResponse], error) {
	id, err := machineFrom(ctx)
	if err != nil {
		return nil, err
	}
	h, err := checkEnvelope(req.Msg.Meta, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_MACHINE_META, MachineId: id})
	if err != nil {
		return nil, err
	}
	b, _ := proto.Marshal(req.Msg.Meta)
	t := m.s.tenantFrom(ctx)
	if err := t.store.putMachineMeta(ctx, id, b, h.Seq); err != nil {
		return nil, internal(err)
	}
	t.hub.machineChanged(id)
	return connect.NewResponse(&pb.PutMachineResponse{}), nil
}

func (m *machineService) PutSession(ctx context.Context, req *connect.Request[pb.PutSessionRequest]) (*connect.Response[pb.PutSessionResponse], error) {
	id, err := machineFrom(ctx)
	if err != nil {
		return nil, err
	}
	sid := req.Msg.SessionId
	if sid == "" {
		return nil, invalid("missing session ID")
	}
	var key, meta []byte
	var revision uint64
	if req.Msg.Key != nil {
		if _, err := checkEnvelope(req.Msg.Key, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_KEY, MachineId: id, SessionId: sid}); err != nil {
			return nil, err
		}
		key, _ = proto.Marshal(req.Msg.Key)
	}
	if req.Msg.Meta != nil {
		h, err := checkEnvelope(req.Msg.Meta, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_SESSION_META, MachineId: id, SessionId: sid})
		if err != nil {
			return nil, err
		}
		meta, _ = proto.Marshal(req.Msg.Meta)
		revision = h.Seq
	}
	if req.Msg.EndedMs < 0 {
		return nil, invalid("ended_ms must not be negative")
	}
	t := m.s.tenantFrom(ctx)
	rs, err := t.store.putSession(ctx, id, sid, key, meta, revision, req.Msg.EndedMs, t.limit(m.s, growsSessions))
	var apiErr *connect.Error
	switch {
	case errors.As(err, &apiErr):
		return nil, err
	case errors.Is(err, errNoKey):
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, errOtherMachine), errors.Is(err, errNoMachine):
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	case err != nil:
		return nil, internal(err)
	}
	t.hub.broadcast(&pb.ServerFrame{Kind: &pb.ServerFrame_SessionUpdated{SessionUpdated: &pb.SessionUpdated{Session: rs}}})
	return connect.NewResponse(&pb.PutSessionResponse{}), nil
}

func (m *machineService) UploadEvents(ctx context.Context, req *connect.Request[pb.UploadEventsRequest]) (*connect.Response[pb.UploadEventsResponse], error) {
	id, err := machineFrom(ctx)
	if err != nil {
		return nil, err
	}
	sid := req.Msg.SessionId
	if len(req.Msg.Events) > maxUploadEvents {
		return nil, invalid("at most %d events per upload", maxUploadEvents)
	}
	for _, e := range req.Msg.Events {
		if _, err := checkEnvelope(e.Envelope, &pb.Header{
			Kind: pb.PayloadKind_PAYLOAD_KIND_EVENT, MachineId: id, SessionId: sid, Seq: e.Seq,
		}); err != nil {
			return nil, err
		}
		if e.Seq == 0 {
			return nil, invalid("event seq must start at 1")
		}
	}
	t := m.s.tenantFrom(ctx)
	added, rs, err := t.store.appendEvents(ctx, id, sid, req.Msg.Events, t.limit(m.s, growsEvents))
	var gap *gapError
	var apiErr *connect.Error
	switch {
	case errors.As(err, &apiErr):
		return nil, err
	case errors.Is(err, errNoRows):
		return nil, connect.NewError(connect.CodeNotFound, errors.New("unknown session; put it first"))
	case errors.Is(err, errOtherMachine):
		return nil, connect.NewError(connect.CodePermissionDenied, err)
	case errors.As(err, &gap):
		return nil, connect.NewError(connect.CodeFailedPrecondition, err)
	case err != nil:
		return nil, internal(err)
	}
	if len(added) > 0 {
		t.hub.broadcast(&pb.ServerFrame{Kind: &pb.ServerFrame_EventsAppended{EventsAppended: &pb.EventsAppended{
			MachineId: id, SessionId: sid, Events: added,
		}}})
		t.hub.broadcast(&pb.ServerFrame{Kind: &pb.ServerFrame_SessionUpdated{SessionUpdated: &pb.SessionUpdated{Session: rs}}})
	}
	return connect.NewResponse(&pb.UploadEventsResponse{LastSeq: rs.LastSeq}), nil
}

func (m *machineService) ListSessionHeads(ctx context.Context, req *connect.Request[pb.ListSessionHeadsRequest]) (*connect.Response[pb.ListSessionHeadsResponse], error) {
	id, err := machineFrom(ctx)
	if err != nil {
		return nil, err
	}
	heads, err := m.s.tenantFrom(ctx).store.sessionHeads(ctx, id)
	if err != nil {
		return nil, internal(err)
	}
	return connect.NewResponse(&pb.ListSessionHeadsResponse{Sessions: heads}), nil
}

// headerOf decodes an envelope's plaintext header; checkEnvelope has
// accepted it before.
func headerOf(env *pb.Envelope) *pb.Header {
	h := &pb.Header{}
	_ = proto.Unmarshal(env.GetHeader(), h)
	return h
}

// validDeviceID reports whether id looks like e2e.DeviceID's output.
func validDeviceID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
