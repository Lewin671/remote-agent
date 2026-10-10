package server

import (
	"sync"
	"testing"
	"time"

	pb "github.com/Lewin671/remote-agent/gen/remoteagent/v1"
)

// Clients and daemons drop a stream whose first frame is not Ready, so
// nothing pushed while a stream opens may get ahead of it.
func TestReadyPrecedesConcurrentBroadcasts(t *testing.T) {
	ts, _, _, _, _, _ := newV2Env(t)
	cpriv, cid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_CLIENT, "")
	if err != nil {
		t.Fatal(err)
	}
	token := ts.login(t, cpriv, cid)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				ts.srv.hub.broadcast(&pb.ServerFrame{Kind: &pb.ServerFrame_DeviceRemoved{DeviceRemoved: &pb.DeviceRemoved{DeviceId: "nobody"}}})
				time.Sleep(200 * time.Microsecond) // slow enough for a stream to keep up
			}
		}
	}()
	defer wg.Wait()
	defer close(stop)
	for range 20 {
		// stream fails the test unless the first frame is Ready.
		ts.stream(t, token).ws.CloseNow()
	}
}
