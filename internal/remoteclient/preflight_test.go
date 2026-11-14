package remoteclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// A real HTTP/1 connection is warmed, then the server consumes the complete
// non-idempotent POST and drops its response. The next local failure has no
// right to turn that request into a new ID.
func TestNetworkLossAndLocalPreflightKeepDispatchEvidence(t *testing.T) {
	for _, stage := range []string{"cancel", "header", "security-header", "epoch"} {
		t.Run(stage, func(t *testing.T) {
			owner, _, mid := syntheticTerminalSecurity(t)
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set(relayEpochHeader, "network-epoch")
				if r.Method == http.MethodGet {
					w.Write([]byte("ok"))
					return
				}
				io.Copy(io.Discard, r.Body)
				posts.Add(1)
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
				}
			}))
			defer server.Close()
			client := NewClient(server.URL, "fixture", owner.DeviceID, owner.endpoint)
			env, err := client.Seal(mid, "", NewRequestID(), time.Now().Add(time.Minute), &pb.Command{Kind: &pb.Command_CreateSession{CreateSession: &pb.CreateSessionRequest{Cwd: "/work/example"}}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Forward(context.Background(), mid, env)
			if !Uncertain(err) || posts.Load() != 1 {
				t.Fatalf("opaque POST replay or lost evidence: posts=%d err=%v", posts.Load(), err)
			}
			ctx := context.Background()
			switch stage {
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "header":
				env.Header = []byte{0xff}
			case "security-header":
				env.SecurityHeader = []byte{0xff}
			case "epoch":
				client.epochMu.Lock()
				client.relayEpoch = ""
				client.http = nil
				client.epochMu.Unlock()
			}
			_, err = client.Forward(ctx, mid, env)
			if !Uncertain(err) {
				t.Fatalf("%s preflight erased original dispatch: %v", stage, err)
			}
			if stage == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal("lost cancellation cause")
			}
			if posts.Load() != 1 {
				t.Fatal("preflight delivered another command")
			}
		})
	}
}
