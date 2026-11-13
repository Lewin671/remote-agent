package relay_test

import (
	"connectrpc.com/connect"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/deviceauth"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

type memberDirectory struct {
	remoteagentv1connect.UnimplementedClientServiceHandler
	state *pb.SecurityState
	epoch uint64
	lease []byte
}

func (d memberDirectory) GetSecurityStates(_ context.Context, r *connect.Request[pb.GetSecurityStatesRequest]) (*connect.Response[pb.GetSecurityStatesResponse], error) {
	result := &pb.GetSecurityStatesResponse{HeadEpoch: d.epoch, Lease: d.lease}
	if r.Msg.AfterEpoch < d.epoch {
		result.States = []*pb.SecurityState{d.state}
	}
	return connect.NewResponse(result), nil
}

// The real server keeps both clients' tokens/terminal routes alive. Only its
// authenticated state response is fault-injected, so closure proves the daemon
// installed revocation and proactively retired output, rather than a hub kick.
func TestInstalledMemberRemovalClosesOnlyThatOutputStream(t *testing.T) {
	var installed atomic.Bool
	var directory http.Handler
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if installed.Load() && r.URL.Path == remoteagentv1connect.ClientServiceGetSecurityStatesProcedure {
				directory.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	a := e.authority
	_, ed, _ := ed25519.GenerateKey(nil)
	x, _, _ := deviceauth.NewXKey()
	enrolled, err := a.Enroll(context.Background(), ed, x, deviceauth.Client)
	if err != nil {
		t.Fatal(err)
	}
	client2 := enrolled.Client(a)
	defer client2.Close()
	e.backend.add(t, "sess1")
	e.startRelayBackend(e.backend, func(r *relay.Relay) { relay.SetSecurityIntervalForTest(r, 20*time.Millisecond) })
	s1 := e.session(t, "sess1", 0)
	e.online(t, true)
	if err := e.client.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	sessions, err := client2.Sessions(context.Background())
	if err != nil || len(sessions) == 0 {
		t.Fatal(err)
	}
	s2 := sessions[0]
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	term1, err := e.client.OpenTerminal(ctx, s1)
	if err != nil {
		t.Fatal(err)
	}
	defer term1.Close()
	if _, err := term1.Read(ctx); err != nil {
		t.Fatal(err)
	}
	e.backend.mu.Lock()
	native1 := e.backend.term
	e.backend.mu.Unlock()
	term2, err := client2.OpenTerminal(ctx, s2)
	if err != nil {
		t.Fatal(err)
	}
	defer term2.Close()
	if _, err := term2.Read(ctx); err != nil {
		t.Fatal(err)
	}
	e.backend.mu.Lock()
	native2 := e.backend.term
	e.backend.mu.Unlock()
	parent := a.States[len(a.States)-1]
	var members []deviceauth.Member
	for _, m := range parent.Core.Members {
		if m.ID != e.client.DeviceID {
			members = append(members, m)
		}
	}
	secret := make([]byte, 32)
	_, _ = rand.Read(secret)
	state, err := deviceauth.BuildMembershipState(parent, members, append(append([]string{}, parent.Core.Removed...), e.client.DeviceID), a.Secret, secret, a.Ed, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := deviceauth.BuildLease(state, a.Ed, uint64(time.Now().UnixMilli()))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := state.MarshalBinary()
	lr, _ := lease.MarshalBinary()
	_, directory = remoteagentv1connect.NewClientServiceHandler(memberDirectory{state: &pb.SecurityState{State: raw}, epoch: state.Core.Epoch, lease: lr})
	installed.Store(true)
	select {
	case <-native1.done:
	case <-ctx.Done():
		t.Fatal("removed reader kept receiving output without sending input")
	}
	if err := client2.Sync(ctx); err != nil {
		t.Fatal(err)
	}
	// Establish that the daemon has a valid current head+lease before checking
	// the surviving stream, without depending on a scheduler sleep.
	waitFor(t, "new head and lease installed", func() bool {
		_, err := client2.DoMachine(ctx, s2.MachineID, &pb.Command{Kind: &pb.Command_GetSettings{GetSettings: &pb.GetSettingsRequest{}}})
		if err == nil {
			return true
		}
		var ce *remoteclient.CommandError
		return errors.As(err, &ce) && ce.FromDaemon
	})
	select {
	case <-native2.done:
		t.Fatal("unaffected reader was revoked")
	default:
	}
	native2.out <- []byte("healthy output")
	frame, err := term2.Read(ctx)
	if err != nil || string(frame.GetOutput()) != "healthy output" {
		t.Fatalf("surviving output: %v", err)
	}
}
