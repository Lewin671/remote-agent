package relay_test

import (
	"connectrpc.com/connect"
	"context"
	"errors"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/internal/outcome"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAuthenticationLossReloadsAccountAndEndpoint(t *testing.T) {
	for _, code := range []connect.Code{connect.CodeUnauthenticated, connect.CodePermissionDenied} {
		t.Run(code.String(), func(t *testing.T) {
			var denied atomic.Bool
			var failures atomic.Int32
			e := setupWithHandler(t, func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/GetSecurityStates") && denied.Load() {
						failures.Add(1)
						connect.NewErrorWriter().Write(w, r, connect.NewError(code, errors.New("test auth lost")))
						return
					}
					next.ServeHTTP(w, r)
				})
			})
			e.backend.add(t, "sess1")
			runningRelay := e.startRelayBackend(e.backend, func(r *relay.Relay) { relay.SetSecurityIntervalForTest(r, 20*time.Millisecond) })
			session := e.session(t, "sess1", 0)
			e.online(t, true)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			terminal, err := e.client.OpenTerminal(ctx, session)
			if err != nil {
				t.Fatal(err)
			}
			defer terminal.Close()
			if _, err := terminal.Read(ctx); err != nil {
				t.Fatal(err)
			}
			e.backend.mu.Lock()
			oldNative := e.backend.term
			e.backend.mu.Unlock()
			denied.Store(true)
			select {
			case <-oldNative.done:
			case <-ctx.Done():
				t.Fatal("auth failure did not retire the live terminal")
			}
			if failures.Load() == 0 {
				t.Fatal("did not exercise GetSecurityStates failure")
			}
			// A different saved transport address (same real server/tenant) must be
			// loaded while the old WS would otherwise remain healthy. Hold the first
			// new sync: WS can become Ready, but admission stays paused until verified.
			release := make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			var newStates, newStreams atomic.Int32
			newer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/stream" {
					newStreams.Add(1)
				}
				if strings.HasSuffix(r.URL.Path, "/GetSecurityStates") {
					newStates.Add(1)
					select {
					case <-release:
					case <-r.Context().Done():
						return
					}
				}
				e.srv.Handler().ServeHTTP(w, r)
			}))
			defer func() { once.Do(func() { close(release) }); newer.Close() }()
			updated := *e.acct
			updated.Server = newer.URL
			if err := updated.Save(); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "saved account and new WS", func() bool { return newStates.Load() > 0 && newStreams.Load() > 0 && runningRelay.Connected() })
			_, err = e.client.Do(ctx, session, &pb.Command{Kind: &pb.Command_Send{Send: &pb.SendRequest{SessionId: session.ID, Text: "must not run"}}})
			var decision *remoteclient.CommandError
			if !errors.As(err, &decision) || decision.Outcome != outcome.Unknown || e.backend.sendCount() != 0 {
				t.Fatalf("paused admission: %v", err)
			}
			once.Do(func() { close(release) })
			var fresh *remoteclient.Term
			waitFor(t, "fresh stream after verified recovery", func() bool { var err error; fresh, err = e.client.OpenTerminal(ctx, session); return err == nil })
			defer fresh.Close()
			if _, err := fresh.Read(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case <-oldNative.done:
			default:
				t.Fatal("retired stream resurrected")
			}
			e.backend.mu.Lock()
			newNative := e.backend.term
			e.backend.mu.Unlock()
			select {
			case <-newNative.done:
				t.Fatal("new terminal already closed")
			default:
			}
		})
	}
}
