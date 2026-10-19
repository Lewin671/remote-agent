package relay_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/e2e"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"github.com/Lewin671/remote-agent/internal/server"
)

// TestJoinByPairingAgainstRealServer exercises the Go join path end to end:
// protocol-10 version checks, state upload before approval (the state must not
// cancel the pairing), chain verification and registration.
func TestJoinByPairingAgainstRealServer(t *testing.T) {
	t.Setenv("REMOTE_AGENT_HOME", t.TempDir())
	srv, err := server.New(server.Config{DBPath: filepath.Join(t.TempDir(), "ra.db"), SetupToken: "setup"})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	phone, err := remoteclient.NewAuthority(ctx, httpSrv.URL, "setup")
	if err != nil {
		t.Fatal(err)
	}
	phoneAPI := remoteagentv1connect.NewClientServiceClient(http.DefaultClient, httpSrv.URL, connect.WithInterceptors(bearerFor(phone.Token)))
	pairingAPI := remoteagentv1connect.NewPairingServiceClient(http.DefaultClient, httpSrv.URL, connect.WithInterceptors(bearerFor(phone.Token)))
	key, err := e2e.NewPairKey()
	if err != nil {
		t.Fatal(err)
	}
	trust, err := deviceauth.PairTrustDescriptor(phone.Pin, phone.Member.ID)
	if err != nil {
		t.Fatal(err)
	}
	commit := e2e.PairDomainCommit(key.Public(), trust)
	created, err := pairingAPI.CreatePairing(ctx, connect.NewRequest(&pb.CreatePairingRequest{Commit: commit}))
	if err != nil {
		t.Fatal(err)
	}
	joined := make(chan struct {
		acct *relay.Account
		err  error
	}, 1)
	go func() {
		a, err := relay.JoinByPairing(ctx, relay.PairLink{Code: created.Msg.Code, Commit: commit, Server: httpSrv.URL}, func([]string) bool { return true })
		joined <- struct {
			acct *relay.Account
			err  error
		}{a, err}
	}()

	var st *pb.GetPairingResponse
	deadline := time.Now().Add(10 * time.Second)
	for {
		res, err := pairingAPI.GetPairing(ctx, connect.NewRequest(&pb.GetPairingRequest{PairingId: created.Msg.PairingId}))
		if err != nil {
			t.Fatal(err)
		}
		st = res.Msg
		if st.State >= pb.PairingState_PAIRING_STATE_JOINED {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("joiner did not join")
		}
		time.Sleep(10 * time.Millisecond)
	}
	candidate, err := deviceauth.NewMember(st.DevicePublicKey, st.DeviceHpkePublicKey, deviceauth.Kind(st.JoinerKind))
	if err != nil {
		t.Fatal(err)
	}
	approval, approvalSig, err := phone.RegisterPairMember(ctx, created.Msg.PairingId, candidate, deviceauth.Kind(st.JoinerKind))
	if err != nil {
		t.Fatal(err)
	}
	// The state upload happens before approval, as the design says. It must
	// not cancel the in-flight pairing.
	if _, err := phone.AddMember(ctx, candidate, st.DeviceHpkePublicKey); err != nil {
		t.Fatal(err)
	}
	if _, err := pairingAPI.RevealPairing(ctx, connect.NewRequest(&pb.RevealPairingRequest{PairingId: created.Msg.PairingId, InitiatorPublic: key.Public(), Trust: trust})); err != nil {
		t.Fatal(err)
	}
	tr := e2e.PairDomainTranscript(created.Msg.PairingId, key.Public(), st.JoinerPublic, st.DevicePublicKey, st.DeviceHpkePublicKey, int32(st.JoinerKind), trust)
	secrets, err := key.Derive(st.JoinerPublic, tr)
	if err != nil {
		t.Fatal(err)
	}
	var chain []*pb.SecurityState
	for i, s := range phone.States {
		raw, err := s.MarshalBinary()
		if err != nil {
			t.Fatal(err)
		}
		var proof []byte
		if phone.Proofs[i] != nil {
			proof, err = phone.Proofs[i].MarshalBinary()
			if err != nil {
				t.Fatal(err)
			}
		}
		chain = append(chain, &pb.SecurityState{State: raw, Recovery: proof})
	}
	lr, _ := phone.Lease.MarshalBinary()
	payload := &pb.PairingPayload{Lease: lr, Server: httpSrv.URL, Approval: approval, ApprovalSignature: approvalSig, SecurityStates: chain, AccountPublicKey: phone.AccountPublicKey}
	env, err := e2e.SealMessage(secrets.Key, &pb.Header{Kind: pb.PayloadKind_PAYLOAD_KIND_PAIRING, RequestId: created.Msg.PairingId, Sender: phone.Member.ID}, payload)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pairingAPI.ApprovePairing(ctx, connect.NewRequest(&pb.ApprovePairingRequest{PairingId: created.Msg.PairingId, Payload: env, Approval: approval, ApprovalSignature: approvalSig})); err != nil {
		t.Fatal(err)
	}
	res := <-joined
	if res.err != nil {
		t.Fatal(res.err)
	}
	if res.acct.DeviceID != candidate.ID || res.acct.Security == nil || res.acct.Security.Pin != phone.Pin {
		t.Fatalf("joined %+v", res.acct)
	}
	// The new member is in the head and can authenticate.
	if _, err := phoneAPI.GetSecurityStates(ctx, connect.NewRequest(&pb.GetSecurityStatesRequest{})); err != nil {
		t.Fatal(err)
	}
}

func bearerFor(token string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	}
}
