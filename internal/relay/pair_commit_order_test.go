package relay

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
)

type changingPairRelay struct {
	candidate chan []byte
	remoteagentv1connect.UnimplementedPairingServiceHandler
	peeked atomic.Bool
	joined atomic.Bool
}

func (r *changingPairRelay) PeekPairing(_ context.Context, req *connect.Request[pb.PeekPairingRequest]) (*connect.Response[pb.PeekPairingResponse], error) {
	r.peeked.Store(true)
	return connect.NewResponse(&pb.PeekPairingResponse{PairingId: "frozen-before-dh-public", Commit: make([]byte, 32)}), nil
}
func (r *changingPairRelay) JoinPairing(_ context.Context, req *connect.Request[pb.JoinPairingRequest]) (*connect.Response[pb.JoinPairingResponse], error) {
	if r.candidate != nil {
		r.candidate <- bytes.Clone(req.Msg.DevicePublicKey)
	}
	if !r.peeked.Load() || len(req.Msg.JoinerPublic) != 32 {
		return nil, devicePairOrderError()
	}
	r.joined.Store(true)
	changed := make([]byte, 32)
	changed[0] = 1
	return connect.NewResponse(&pb.JoinPairingResponse{PairingId: "frozen-before-dh-public", Commit: changed, CandidateToken: "synthetic-token"}), nil
}
func devicePairOrderError() error {
	return connect.NewError(connect.CodeInvalidArgument, context.Canceled)
}
func TestManualPairingFreezesCommitBeforePublishingEphemeralPublicKey(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	r := &changingPairRelay{}
	mux := http.NewServeMux()
	path, h := remoteagentv1connect.NewPairingServiceHandler(r)
	mux.Handle(path, h)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	shown := false
	_, err := JoinByPairing(context.Background(), PairLink{Code: "12345678", Server: srv.URL}, func([]string) bool { shown = true; return false })
	if err == nil || !r.peeked.Load() || !r.joined.Load() || shown {
		t.Fatal("manual-code flow did not reject post-public-key commitment replacement", err)
	}
}

func TestRetainedPairingPreservesSigningIdentityAndAccount(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	pub, ed, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	r := &changingPairRelay{candidate: make(chan []byte, 1)}
	mux := http.NewServeMux()
	path, handler := remoteagentv1connect.NewPairingServiceHandler(r)
	mux.Handle(path, handler)
	authPath, authHandler := remoteagentv1connect.NewAuthServiceHandler(&retainedTestAuth{public: pub})
	mux.Handle(authPath, authHandler)
	server := httptest.NewServer(mux)
	defer server.Close()
	account := &Account{Server: server.URL, Root: e2e.NewKey(), DeviceSeed: ed.Seed(), DeviceID: e2e.DeviceID(pub)}
	if err = account.Save(); err != nil {
		t.Fatal(err)
	}
	_, err = EnrollByPairing(context.Background(), PairLink{Code: "12345678", Server: server.URL}, func([]string) bool { t.Error("unverified SAS was shown"); return false })
	if err == nil {
		t.Fatal("changed pairing commitment accepted")
	}
	select {
	case candidate := <-r.candidate:
		if !bytes.Equal(candidate, pub) {
			t.Fatal("retained Ed25519 identity replaced")
		}
	default:
		t.Fatal("retained identity was not used")
	}
	kept, err := LoadAccount()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(kept.Root, account.Root) || !bytes.Equal(kept.DeviceSeed, account.DeviceSeed) || kept.Security != nil {
		t.Fatal("failed enrollment changed existing account")
	}
}

type retainedTestAuth struct {
	remoteagentv1connect.UnimplementedAuthServiceHandler
	public ed25519.PublicKey
}

func (a *retainedTestAuth) GetChallenge(context.Context, *connect.Request[pb.GetChallengeRequest]) (*connect.Response[pb.GetChallengeResponse], error) {
	return connect.NewResponse(&pb.GetChallengeResponse{Challenge: bytes.Repeat([]byte{1}, 32)}), nil
}
func (a *retainedTestAuth) Login(_ context.Context, r *connect.Request[pb.LoginRequest]) (*connect.Response[pb.LoginResponse], error) {
	if r.Msg.DeviceId != e2e.DeviceID(a.public) || !e2e.VerifyLogin(a.public, r.Msg.Challenge, r.Msg.Signature) {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid synthetic retained identity"))
	}
	return connect.NewResponse(&pb.LoginResponse{Token: "synthetic-retained-login", ExpiresMs: time.Now().Add(time.Hour).UnixMilli(), DevicePublicKey: a.public}), nil
}
