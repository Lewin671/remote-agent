package relay_test

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
	"github.com/Lewin671/remote-agent/gen/remoteagent/v1/remoteagentv1connect"
	"github.com/Lewin671/remote-agent/internal/relay"
	"github.com/Lewin671/remote-agent/internal/remoteclient"
)

type diskLifetimeBackend struct {
	*fakeBackend
	acked atomic.Bool
}

func (b *diskLifetimeBackend) MachineDiskAlerts() []*pb.DiskStatus {
	if b.acked.Load() {
		return nil
	}
	return []*pb.DiskStatus{{ObservedMs: 1700000000001, Pressure: pb.ResourcePressure_RESOURCE_PRESSURE_CRITICAL, AvailableBytes: 1e9}}
}
func (b *diskLifetimeBackend) AckMachineDiskAlert(int64, pb.ResourcePressure) { b.acked.Store(true) }

func TestDiskAlertRetryAndOwnerSurviveMainWSReplacement(t *testing.T) {
	var calls atomic.Int32
	e := setupWithHandler(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == remoteagentv1connect.MachineServiceNotifyProcedure && calls.Add(1) == 1 {
				connect.NewErrorWriter().Write(w, r, connect.NewError(connect.CodeUnavailable, errors.New("fixture notify offline")))
				return
			}
			next.ServeHTTP(w, r)
		})
	})
	b := &diskLifetimeBackend{fakeBackend: e.backend}
	var now atomic.Int64
	now.Store(time.Now().UnixNano())
	e.startRelayBackend(b, func(r *relay.Relay) {
		relay.SetRetryNowForTest(r, func() time.Time { return time.Unix(0, now.Load()) })
	})
	waitFor(t, "first failed disk notice", func() bool { return calls.Load() == 1 })
	e.dropStreams()
	// A signed read after dropping the old socket proves the replacement is
	// serving commands, rather than accepting a test pass while still offline.
	waitFor(t, "signed result on replacement WS", func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		_, err := e.client.DoMachine(ctx, e.acct.DeviceID, &pb.Command{Kind: &pb.Command_GetSettings{GetSettings: &pb.GetSettingsRequest{}}})
		var ce *remoteclient.CommandError
		return err == nil || errors.As(err, &ce) && ce.FromDaemon
	})
	now.Add(int64(59 * time.Second))
	time.Sleep(80 * time.Millisecond)
	if calls.Load() != 1 || b.acked.Load() {
		t.Fatal("socket replacement reset the notification retry deadline")
	}
	now.Add(int64(time.Second))
	waitFor(t, "process publisher retries and acknowledges", func() bool { return b.acked.Load() })
	if calls.Load() != 2 {
		t.Fatalf("notification sent %d times", calls.Load())
	}
}
