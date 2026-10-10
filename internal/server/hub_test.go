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

// A PutMachine that read the machine as online must not broadcast that
// after the disconnect was broadcast: clients take the flag as it comes.
func TestOnlineStateIsBroadcastInOrder(t *testing.T) {
	ts := newTestServer(t)
	_, mid, err := ts.register(t, ts.keys, pb.DeviceKind_DEVICE_KIND_MACHINE, "setup")
	if err != nil {
		t.Fatal(err)
	}
	h := ts.srv.hub
	client := &peer{dev: &device{id: "client", kind: pb.DeviceKind_DEVICE_KIND_CLIENT}, out: make(chan *pb.ServerFrame, sendQueue), cancel: func() {}}
	h.mu.Lock()
	h.clients[client] = true
	h.mu.Unlock()
	for round := range 100 {
		machine := &peer{dev: &device{id: mid, kind: pb.DeviceKind_DEVICE_KIND_MACHINE}, out: make(chan *pb.ServerFrame, sendQueue), cancel: func() {}}
		h.add(machine)
		// Several PutMachine calls are under way as the stream drops.
		var wg sync.WaitGroup
		for range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				h.machineChanged(mid)
			}()
		}
		time.Sleep(time.Duration(round%10) * 50 * time.Microsecond)
		h.remove(machine)
		wg.Wait()
		var last *pb.Machine
		for len(client.out) > 0 {
			if m := (<-client.out).GetMachineUpdated(); m != nil {
				last = m.Machine
			}
		}
		if last == nil || last.Online {
			t.Fatalf("round %d: the last word on a disconnected machine was online: %v", round, last != nil)
		}
	}
}
