package relay

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/shared/devicesec"
)

type failedSecurityPull struct {
	remoteagentv1connect.UnimplementedClientServiceHandler
	code connect.Code
}

func (s failedSecurityPull) GetSecurityStates(context.Context, *connect.Request[pb.GetSecurityStatesRequest]) (*connect.Response[pb.GetSecurityStatesResponse], error) {
	return nil, connect.NewError(s.code, errors.New("synthetic pull failure"))
}
func TestSecuritySyncRequestsReloadOnAuthenticationLoss(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeUnauthenticated, connect.CodePermissionDenied, connect.CodeUnavailable} {
		t.Run(code.String(), func(t *testing.T) {
			c := &conn{r: New(publishBackend{})}
			initializeTestSecurityKeys(t, c)
			path, h := remoteagentv1connect.NewClientServiceHandler(failedSecurityPull{code: code})
			mux := http.NewServeMux()
			mux.Handle(path, h)
			srv := httptest.NewServer(mux)
			defer srv.Close()
			c.security.client = remoteagentv1connect.NewClientServiceClient(http.DefaultClient, srv.URL)
			c.security.requestSync()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			reloads := 0
			c.security.onAuthLost = func() { reloads++; cancel() }
			err := c.security.syncLoop(ctx, 20*time.Millisecond, nil)
			if code == connect.CodeUnavailable {
				if reloads != 0 || !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err, reloads)
				}
			} else {
				if reloads != 1 || !errors.Is(err, context.Canceled) || !c.security.blocked.Load() {
					t.Fatal(err, reloads)
				}
			}
		})
	}
}

// TestSecurityPullInstallsStateAndLeaseAtomically verifies that advancing the
// trusted head epoch and installing its matching lease from one
// GetSecurityStates response does not expose an intermediate window where
// HeadEpoch has advanced while Lease.Head still points at the old epoch
// (which caused concurrent requests to fail with ErrStale).
func TestSecurityPullInstallsStateAndLeaseAtomically(t *testing.T) {
	c := &conn{r: New(publishBackend{})}
	keys := initializeTestSecurityKeys(t, c)
	s := c.security
	now := s.now()

	_, recovery, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	var pub [32]byte
	copy(pub[:], recovery.Public().(ed25519.PublicKey))
	nextState, err := deviceauth.BuildRecoveryKeyRotation(keys.states[len(keys.states)-1], keys.secret, pub, keys.phone, now)
	if err != nil {
		t.Fatal(err)
	}
	nextLease, err := deviceauth.BuildLease(nextState, keys.phone, now)
	if err != nil {
		t.Fatal(err)
	}
	rawState, err := nextState.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	rawLease, err := nextLease.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}

	s.client = ownershipSecurityClient{read: func(context.Context) (*connect.Response[pb.GetSecurityStatesResponse], error) {
		return connect.NewResponse(&pb.GetSecurityStatesResponse{
			HeadEpoch: nextState.Core.Epoch,
			States:    []*pb.SecurityState{{State: rawState}},
			Lease:     rawLease,
		}), nil
	}}

	// Register a probe stream revoked on revalidateStreams() to observe the
	// endpoint's lease validity immediately after state installation.
	revalidated := false
	var observedLeaseErr error
	s.authMu.Lock()
	if s.streams == nil {
		s.streams = map[uint64]*authorizedStream{}
	}
	s.streams[999] = &authorizedStream{
		peer: devicesec.Member{ID: "probe-non-member"},
		cancel: func() {
			revalidated = true
			observedLeaseErr = s.endpoint.CheckLease(now)
		},
	}
	s.authMu.Unlock()

	if _, err := s.pull(context.Background()); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if !revalidated {
		t.Fatal("expected revalidateStreams to run during pull")
	}
	if observedLeaseErr != nil {
		t.Fatalf("endpoint observed stale lease window between state and lease install: %v", observedLeaseErr)
	}
}
